package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"cline-go-proxy/internal/kit"
)

const (
	openCodeAccountsFileName = "opencode-accounts.json"

	OpenCodeRouteDefault = "default"
	OpenCodeRouteDirect  = "direct"
	OpenCodeRouteWARP    = "warp"
	OpenCodeRouteTor     = "tor"
	OpenCodeRouteProxy   = "proxy"

	openCodeDeviceTokenURL = "https://opencode.ai/console/auth/device/token"
)

type OpenCodeAccountRecord struct {
	Name             string `json:"name"`
	Authorization    string `json:"authorization"`
	CreatedAt        string `json:"created_at"`
	Route            string `json:"route,omitempty"`
	CredentialSource string `json:"credential_source,omitempty"`
	RefreshToken     string `json:"refresh_token,omitempty"`
	ExpiresAt        int64  `json:"expires_at,omitempty"` // Unix milliseconds
	OrgID            string `json:"org_id,omitempty"`
	Disabled         bool   `json:"disabled,omitempty"`
	RateLimitedUntil string `json:"rate_limited_until,omitempty"`
	LastError        string `json:"last_error,omitempty"`
	LastUsedAt       string `json:"last_used_at,omitempty"`
}

type OpenCodeAccountsState struct {
	Accounts        []OpenCodeAccountRecord `json:"accounts"`
	Active          string                  `json:"active,omitempty"`
	RoutingStrategy string                  `json:"routing_strategy,omitempty"`
}

type OpenCodeAccountStore struct {
	mu       sync.Mutex
	path     string
	rrCursor int
}

var (
	globalOpenCodeAccounts     *OpenCodeAccountStore
	globalOpenCodeAccountsOnce sync.Once
)

func GetOpenCodeAccountStore() *OpenCodeAccountStore {
	globalOpenCodeAccountsOnce.Do(func() {
		path := kit.ResolveDataPath(openCodeAccountsFileName)
		globalOpenCodeAccounts = &OpenCodeAccountStore{path: path}
	})
	return globalOpenCodeAccounts
}

func (s *OpenCodeAccountStore) Load() (OpenCodeAccountsState, error) {
	if s == nil {
		return OpenCodeAccountsState{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadUnlocked()
}

func (s *OpenCodeAccountStore) loadUnlocked() (OpenCodeAccountsState, error) {
	state := OpenCodeAccountsState{
		RoutingStrategy: "fill",
	}
	if s == nil || s.path == "" {
		return state, nil
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if len(data) == 0 {
		return state, nil
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("invalid opencode accounts json: %w", err)
	}
	if state.RoutingStrategy == "" {
		state.RoutingStrategy = "fill"
	}
	sort.Slice(state.Accounts, func(i, j int) bool {
		return strings.ToLower(state.Accounts[i].Name) < strings.ToLower(state.Accounts[j].Name)
	})
	return state, nil
}

func (s *OpenCodeAccountStore) Save(state OpenCodeAccountsState) error {
	if s == nil || s.path == "" {
		return errors.New("opencode account store path not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveUnlocked(state)
}

func (s *OpenCodeAccountStore) saveUnlocked(state OpenCodeAccountsState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *OpenCodeAccountStore) ListAccounts() []map[string]any {
	state, err := s.Load()
	if err != nil {
		return nil
	}
	res := make([]map[string]any, 0, len(state.Accounts))
	now := time.Now()
	for _, acc := range state.Accounts {
		tokenMask := maskZenKey(acc.Authorization)
		isExpired := acc.ExpiresAt > 0 && time.Now().UnixMilli() >= acc.ExpiresAt
		isCooldown := false
		if acc.RateLimitedUntil != "" {
			if t, err := time.Parse(time.RFC3339, acc.RateLimitedUntil); err == nil && t.After(now) {
				isCooldown = true
			}
		}

		route := acc.Route
		if route == "" {
			route = OpenCodeRouteDefault
		}

		status := "active"
		if acc.Disabled {
			status = "disabled"
		} else if isCooldown {
			status = "cooldown"
		} else if isExpired {
			status = "expired"
		}

		res = append(res, map[string]any{
			"name":               acc.Name,
			"is_active":          strings.EqualFold(acc.Name, state.Active),
			"token_mask":         tokenMask,
			"route":              route,
			"status":             status,
			"org_id":             acc.OrgID,
			"credential_source":  acc.CredentialSource,
			"expires_at":         acc.ExpiresAt,
			"rate_limited_until": acc.RateLimitedUntil,
			"last_error":         acc.LastError,
			"last_used_at":       acc.LastUsedAt,
			"created_at":         acc.CreatedAt,
			"has_refresh":        acc.RefreshToken != "",
		})
	}
	return res
}

func (s *OpenCodeAccountStore) AddAccount(record OpenCodeAccountRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadUnlocked()
	if err != nil {
		return err
	}

	normName := strings.TrimSpace(record.Name)
	if normName == "" {
		return errors.New("account name cannot be empty")
	}

	for _, a := range state.Accounts {
		if strings.EqualFold(a.Name, normName) {
			return fmt.Errorf("account with name %q already exists", normName)
		}
	}

	record.Name = normName
	if !strings.HasPrefix(record.Authorization, "Bearer ") && record.Authorization != "" {
		record.Authorization = "Bearer " + strings.TrimSpace(record.Authorization)
	}
	if record.Route == "" {
		record.Route = OpenCodeRouteDefault
	}
	if record.CreatedAt == "" {
		record.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}

	state.Accounts = append(state.Accounts, record)
	if state.Active == "" {
		state.Active = record.Name
	}
	return s.saveUnlocked(state)
}

func (s *OpenCodeAccountStore) DeleteAccount(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadUnlocked()
	if err != nil {
		return err
	}

	filtered := make([]OpenCodeAccountRecord, 0, len(state.Accounts))
	found := false
	for _, a := range state.Accounts {
		if strings.EqualFold(a.Name, name) {
			found = true
			continue
		}
		filtered = append(filtered, a)
	}
	if !found {
		return fmt.Errorf("account %q not found", name)
	}

	state.Accounts = filtered
	if strings.EqualFold(state.Active, name) {
		if len(filtered) > 0 {
			state.Active = filtered[0].Name
		} else {
			state.Active = ""
		}
	}
	return s.saveUnlocked(state)
}

func (s *OpenCodeAccountStore) UpdateAccountRoute(name, route string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadUnlocked()
	if err != nil {
		return err
	}

	found := false
	for i, a := range state.Accounts {
		if strings.EqualFold(a.Name, name) {
			state.Accounts[i].Route = route
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("account %q not found", name)
	}
	return s.saveUnlocked(state)
}

func (s *OpenCodeAccountStore) PickAccount(pinName string) (*OpenCodeAccountRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadUnlocked()
	if err != nil {
		return nil, err
	}

	if len(state.Accounts) == 0 {
		return nil, errors.New("no opencode accounts in pool")
	}

	now := time.Now()

	// If pinned by name
	if pinName != "" {
		for i, a := range state.Accounts {
			if strings.EqualFold(a.Name, pinName) {
				acc := state.Accounts[i]
				return &acc, nil
			}
		}
		return nil, fmt.Errorf("pinned account %q not found", pinName)
	}

	isAvailable := func(a OpenCodeAccountRecord) bool {
		if a.Disabled {
			return false
		}
		if a.RateLimitedUntil != "" {
			if t, err := time.Parse(time.RFC3339, a.RateLimitedUntil); err == nil && t.After(now) {
				return false
			}
		}
		return true
	}

	// Filter available accounts (not disabled, not in cooldown)
	available := make([]OpenCodeAccountRecord, 0, len(state.Accounts))
	for _, a := range state.Accounts {
		if isAvailable(a) {
			available = append(available, a)
		}
	}

	if len(available) == 0 {
		return nil, errors.New("all opencode accounts in pool are in cooldown or disabled")
	}

	strategy := strings.ToLower(strings.TrimSpace(state.RoutingStrategy))
	if strategy == "round-robin" {
		idx := s.rrCursor % len(available)
		s.rrCursor++
		picked := available[idx]
		return &picked, nil
	}

	// Strategy: "fill" / "sticky" (default)
	// 1. If currently active account is available, keep using it (sticky)
	if state.Active != "" {
		for _, a := range available {
			if strings.EqualFold(a.Name, state.Active) {
				picked := a
				return &picked, nil
			}
		}
	}

	// 2. Active account is either unset or not available (cooling/disabled).
	// Advance to the next available account in circular sequence starting after state.Active.
	activeIdx := -1
	if state.Active != "" {
		for i, a := range state.Accounts {
			if strings.EqualFold(a.Name, state.Active) {
				activeIdx = i
				break
			}
		}
	}

	for i := 1; i <= len(state.Accounts); i++ {
		candidateIdx := (activeIdx + i) % len(state.Accounts)
		candidate := state.Accounts[candidateIdx]
		if isAvailable(candidate) {
			state.Active = candidate.Name
			_ = s.saveUnlocked(state)
			return &candidate, nil
		}
	}

	// Fallback to first available account
	picked := available[0]
	state.Active = picked.Name
	_ = s.saveUnlocked(state)
	return &picked, nil
}

// SetActive manually sets the sticky active account
func (s *OpenCodeAccountStore) SetActive(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadUnlocked()
	if err != nil {
		return err
	}
	found := false
	for _, a := range state.Accounts {
		if strings.EqualFold(a.Name, name) {
			found = true
			state.Active = a.Name
			break
		}
	}
	if !found {
		return fmt.Errorf("account %q not found in pool", name)
	}
	return s.saveUnlocked(state)
}

// SetStrategy updates the account selection strategy ("fill" or "round-robin")
func (s *OpenCodeAccountStore) SetStrategy(strategy string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadUnlocked()
	if err != nil {
		return err
	}
	norm := strings.ToLower(strings.TrimSpace(strategy))
	if norm == "sticky" {
		norm = "fill"
	}
	if norm != "fill" && norm != "round-robin" {
		return fmt.Errorf("invalid routing strategy %q (allowed: fill, round-robin)", strategy)
	}
	state.RoutingStrategy = norm
	return s.saveUnlocked(state)
}

func (s *OpenCodeAccountStore) CooldownAccount(name string, d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadUnlocked()
	if err != nil {
		return
	}

	for i, a := range state.Accounts {
		if strings.EqualFold(a.Name, name) {
			state.Accounts[i].RateLimitedUntil = time.Now().Add(d).UTC().Format(time.RFC3339)
			_ = s.saveUnlocked(state)
			break
		}
	}
}

func (s *OpenCodeAccountStore) MarkAccountSuccess(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadUnlocked()
	if err != nil {
		return
	}

	for i, a := range state.Accounts {
		if strings.EqualFold(a.Name, name) {
			state.Accounts[i].RateLimitedUntil = ""
			state.Accounts[i].LastError = ""
			state.Accounts[i].LastUsedAt = time.Now().UTC().Format(time.RFC3339)
			_ = s.saveUnlocked(state)
			break
		}
	}
}

// RefreshAccountToken refreshes an account's OAuth access token via device token refresh endpoint
func (s *OpenCodeAccountStore) RefreshAccountToken(ctx context.Context, name string) error {
	s.mu.Lock()
	state, err := s.loadUnlocked()
	if err != nil {
		s.mu.Unlock()
		return err
	}

	var target *OpenCodeAccountRecord
	targetIdx := -1
	for i, a := range state.Accounts {
		if strings.EqualFold(a.Name, name) {
			target = &state.Accounts[i]
			targetIdx = i
			break
		}
	}
	s.mu.Unlock()

	if target == nil {
		return fmt.Errorf("account %q not found", name)
	}
	if target.RefreshToken == "" {
		return fmt.Errorf("account %q does not have a refresh token (static credential)", name)
	}

	// Request refresh
	client := &http.Client{Timeout: 20 * time.Second}
	payload := map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": target.RefreshToken,
		"client_id":     "opencode-cli",
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, openCodeDeviceTokenURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("refresh returned HTTP %d: %s", resp.StatusCode, kit.Truncate(string(respBody), 120))
	}

	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		OrgID        string `json:"org_id"`
		Error        string `json:"error"`
	}
	if err := json.Unmarshal(respBody, &tok); err != nil {
		return fmt.Errorf("decode refresh token response: %w", err)
	}

	if tok.Error != "" || tok.AccessToken == "" {
		return fmt.Errorf("token refresh error: %s", tok.Error)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	state, err = s.loadUnlocked()
	if err != nil {
		return err
	}

	if targetIdx < len(state.Accounts) && strings.EqualFold(state.Accounts[targetIdx].Name, name) {
		state.Accounts[targetIdx].Authorization = "Bearer " + tok.AccessToken
		if tok.RefreshToken != "" {
			state.Accounts[targetIdx].RefreshToken = tok.RefreshToken
		}
		if tok.ExpiresIn > 0 {
			state.Accounts[targetIdx].ExpiresAt = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).UnixMilli()
		}
		if tok.OrgID != "" {
			state.Accounts[targetIdx].OrgID = tok.OrgID
		}
		state.Accounts[targetIdx].LastError = ""
		return s.saveUnlocked(state)
	}

	return nil
}

// ActiveAccountCount returns the number of active, non-disabled accounts in the pool
func (s *OpenCodeAccountStore) ActiveAccountCount() int {
	state, err := s.Load()
	if err != nil {
		return 0
	}
	count := 0
	for _, a := range state.Accounts {
		if !a.Disabled {
			count++
		}
	}
	return count
}

// FindAccountByToken locates an OpenCode account by its authorization token
func FindAccountByToken(token string) *OpenCodeAccountRecord {
	clean := strings.TrimPrefix(strings.TrimSpace(token), "Bearer ")
	if clean == "" || clean == "public" {
		return nil
	}
	store := GetOpenCodeAccountStore()
	if store == nil {
		return nil
	}
	state, err := store.Load()
	if err != nil {
		return nil
	}
	for _, a := range state.Accounts {
		aTok := strings.TrimPrefix(strings.TrimSpace(a.Authorization), "Bearer ")
		if aTok == clean || (len(aTok) > 20 && strings.HasPrefix(clean, aTok)) {
			res := a
			return &res
		}
	}
	return nil
}

