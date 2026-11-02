package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const openCodeDeviceBaseURL = "https://opencode.ai/console/auth/device"

type OpenCodeDeviceToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	OrgID        string `json:"org_id"`
	Error        string `json:"error"`
}

type OpenCodeDeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri_complete"`
	ExpiresIn       int64  `json:"expires_in"`
	Interval        int64  `json:"interval"`
}

type OpenCodeBrowserSession struct {
	SessionID       string    `json:"session_id"`
	Name            string    `json:"name"`
	Route           string    `json:"route"`
	Status          string    `json:"status"` // starting, pending, validating, completed, failed, expired, canceled
	VerificationURI string    `json:"verification_uri,omitempty"`
	UserCode        string    `json:"user_code,omitempty"`
	ExpiresAt       time.Time `json:"expires_at"`
	Error           string    `json:"error,omitempty"`
	cancel          context.CancelFunc
}

var (
	openCodeBrowserMu      sync.Mutex
	openCodeBrowserSession *OpenCodeBrowserSession
)

func resolveRouteProxyURL(route string) string {
	switch strings.ToLower(strings.TrimSpace(route)) {
	case OpenCodeRouteWARP:
		return "socks5://127.0.0.1:40000"
	case OpenCodeRouteTor:
		return "socks5://127.0.0.1:9052"
	case OpenCodeRouteProxy:
		p, _ := pickProxyOrFailover(false)
		return p
	default:
		return ""
	}
}

func openCodeAuthHTTPClient(route string) *http.Client {
	proxyURL := resolveRouteProxyURL(route)
	return proxyClientFor(proxyURL)
}

func openCodeDevicePost(ctx context.Context, client *http.Client, endpoint string, payload any, output any) (int, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, errors.New("invalid device request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("device auth request failed: %w", err)
	}
	defer response.Body.Close()
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(output); err != nil {
		return response.StatusCode, errors.New("invalid device authorization response")
	}
	return response.StatusCode, nil
}

func StartOpenCodeDeviceFlow(name, route string) (*OpenCodeBrowserSession, error) {
	openCodeBrowserMu.Lock()
	defer openCodeBrowserMu.Unlock()

	normName := strings.TrimSpace(name)
	if normName == "" {
		return nil, errors.New("account name is required")
	}

	store := GetOpenCodeAccountStore()
	state, err := store.Load()
	if err != nil {
		return nil, err
	}
	for _, a := range state.Accounts {
		if strings.EqualFold(a.Name, normName) {
			return nil, fmt.Errorf("account %q already exists in pool", normName)
		}
	}

	if openCodeBrowserSession != nil && openCodeBrowserSession.cancel != nil {
		if openCodeBrowserSession.Status == "pending" || openCodeBrowserSession.Status == "starting" {
			return nil, errors.New("an OpenCode authorization session is already active")
		}
	}

	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, errors.New("cannot generate session ID")
	}
	sessionID := hex.EncodeToString(random[:])

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	s := &OpenCodeBrowserSession{
		SessionID: sessionID,
		Name:      normName,
		Route:     route,
		Status:    "starting",
		ExpiresAt: time.Now().Add(15 * time.Minute),
		cancel:    cancel,
	}
	openCodeBrowserSession = s

	go runOpenCodeDeviceSession(ctx, s)
	return s, nil
}

func CancelOpenCodeDeviceFlow() bool {
	openCodeBrowserMu.Lock()
	defer openCodeBrowserMu.Unlock()
	if openCodeBrowserSession != nil && openCodeBrowserSession.cancel != nil {
		openCodeBrowserSession.cancel()
		openCodeBrowserSession.cancel = nil
		openCodeBrowserSession.Status = "canceled"
		openCodeBrowserSession.VerificationURI = ""
		openCodeBrowserSession.UserCode = ""
		return true
	}
	return false
}

func GetOpenCodeDeviceFlowStatus() map[string]any {
	openCodeBrowserMu.Lock()
	defer openCodeBrowserMu.Unlock()
	if openCodeBrowserSession == nil {
		return map[string]any{"status": "idle"}
	}
	return map[string]any{
		"session_id":       openCodeBrowserSession.SessionID,
		"name":             openCodeBrowserSession.Name,
		"route":            openCodeBrowserSession.Route,
		"status":           openCodeBrowserSession.Status,
		"verification_uri": openCodeBrowserSession.VerificationURI,
		"user_code":        openCodeBrowserSession.UserCode,
		"expires_at":       openCodeBrowserSession.ExpiresAt,
		"error":            openCodeBrowserSession.Error,
	}
}

func runOpenCodeDeviceSession(ctx context.Context, s *OpenCodeBrowserSession) {
	client := openCodeAuthHTTPClient(s.Route)

	defer func() {
		openCodeBrowserMu.Lock()
		defer openCodeBrowserMu.Unlock()
		if openCodeBrowserSession != s {
			return
		}
		if s.cancel != nil {
			if s.Status != "completed" {
				if errors.Is(ctx.Err(), context.DeadlineExceeded) || !time.Now().Before(s.ExpiresAt) {
					s.Status = "expired"
				} else if s.Status != "canceled" {
					s.Status = "failed"
					if s.Error == "" {
						s.Error = "OpenCode authorization could not be completed"
					}
				}
			}
			s.cancel()
			s.cancel = nil
		}
	}()

	var device OpenCodeDeviceCode
	status, err := openCodeDevicePost(ctx, client, openCodeDeviceBaseURL+"/code", map[string]any{
		"client_id":          "opencode-cli",
		"supports_org_scope": true,
	}, &device)

	if err != nil || status != 200 || device.DeviceCode == "" || device.ExpiresIn <= 0 {
		openCodeBrowserMu.Lock()
		s.Status = "failed"
		s.Error = "could not request device code from opencode.ai"
		openCodeBrowserMu.Unlock()
		return
	}

	verificationURI := device.VerificationURI
	if verificationURI == "" {
		verificationURI = fmt.Sprintf("https://opencode.ai/auth/device?code=%s", url.QueryEscape(device.UserCode))
	}

	lifetime := min(device.ExpiresIn, int64(15*60))
	childCtx, childCancel := context.WithTimeout(ctx, time.Duration(lifetime)*time.Second)
	defer childCancel()

	openCodeBrowserMu.Lock()
	if s.cancel == nil {
		openCodeBrowserMu.Unlock()
		return
	}
	s.ExpiresAt = time.Now().Add(time.Duration(lifetime) * time.Second)
	s.Status = "pending"
	s.VerificationURI = verificationURI
	s.UserCode = device.UserCode
	openCodeBrowserMu.Unlock()

	interval := time.Duration(max(int64(1), min(device.Interval, int64(60)))) * time.Second
	if interval == 0 {
		interval = 5 * time.Second
	}

	for {
		timer := time.NewTimer(interval)
		select {
		case <-childCtx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		var tok OpenCodeDeviceToken
		tokenStatus, tokenErr := openCodeDevicePost(childCtx, client, openCodeDeviceBaseURL+"/token", map[string]any{
			"grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
			"device_code": device.DeviceCode,
			"client_id":   "opencode-cli",
		}, &tok)

		if tokenErr != nil {
			continue
		}

		if tokenStatus == 400 || tokenStatus == 200 {
			if tok.Error == "authorization_pending" {
				continue
			}
			if tok.Error == "slow_down" {
				interval = min(interval+5*time.Second, 60*time.Second)
				continue
			}
		}

		if tokenStatus != 200 || tok.AccessToken == "" || tok.Error != "" {
			continue
		}

		// Token received!
		openCodeBrowserMu.Lock()
		s.Status = "validating"
		openCodeBrowserMu.Unlock()

		record := OpenCodeAccountRecord{
			Name:             s.Name,
			Authorization:    "Bearer " + tok.AccessToken,
			RefreshToken:     tok.RefreshToken,
			ExpiresAt:        time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).UnixMilli(),
			OrgID:            tok.OrgID,
			Route:            s.Route,
			CredentialSource: "browser_device",
			CreatedAt:        time.Now().UTC().Format(time.RFC3339),
		}

		store := GetOpenCodeAccountStore()
		if err := store.AddAccount(record); err != nil {
			openCodeBrowserMu.Lock()
			s.Status = "failed"
			s.Error = err.Error()
			openCodeBrowserMu.Unlock()
			return
		}

		openCodeBrowserMu.Lock()
		s.Status = "completed"
		s.VerificationURI = ""
		s.UserCode = ""
		openCodeBrowserMu.Unlock()
		return
	}
}

// ============ Admin Handlers for Device Flow ============

func handleOpenCodeOAuthStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: "method not allowed"})
		return
	}
	var input struct {
		Name  string `json:"name"`
		Route string `json:"route"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: "invalid JSON body"})
		return
	}
	if strings.TrimSpace(input.Name) == "" {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: "account name is required"})
		return
	}

	session, err := StartOpenCodeDeviceFlow(input.Name, input.Route)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}

	writeAPI(w, http.StatusAccepted, apiResponse{
		Success: true,
		Data: map[string]any{
			"session_id": session.SessionID,
			"name":       session.Name,
			"route":      session.Route,
			"status":     session.Status,
		},
	})
}

func handleOpenCodeOAuthStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: "method not allowed"})
		return
	}
	status := GetOpenCodeDeviceFlowStatus()
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Data: status})
}

func handleOpenCodeOAuthCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete && r.Method != http.MethodPost {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: "method not allowed"})
		return
	}
	ok := CancelOpenCodeDeviceFlow()
	writeAPI(w, http.StatusOK, apiResponse{Success: ok})
}
