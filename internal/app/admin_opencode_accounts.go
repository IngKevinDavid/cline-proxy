package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"cline-go-proxy/internal/kit"
)

// GET /admin/api/opencode/accounts
func handleOpenCodeAccountsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: "method not allowed"})
		return
	}
	store := GetOpenCodeAccountStore()
	state, _ := store.Load()
	accounts := store.ListAccounts()
	fidelity := GetFidelityProfileInfo()
	writeAPI(w, http.StatusOK, apiResponse{
		Success: true,
		Data: map[string]any{
			"accounts":         accounts,
			"total":            len(accounts),
			"active":           state.Active,
			"routing_strategy": state.RoutingStrategy,
			"fidelity":         fidelity,
		},
	})
}

// POST /admin/api/opencode/accounts/active
func handleOpenCodeAccountSetActive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: "method not allowed"})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	defer r.Body.Close()

	var req struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: "invalid JSON"})
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: "account name is required"})
		return
	}

	store := GetOpenCodeAccountStore()
	if err := store.SetActive(req.Name); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}

	writeAPI(w, http.StatusOK, apiResponse{Success: true, Message: "active account updated"})
}

// POST /admin/api/opencode/accounts/strategy
func handleOpenCodeAccountSetStrategy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: "method not allowed"})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	defer r.Body.Close()

	var req struct {
		Strategy string `json:"strategy"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: "invalid JSON"})
		return
	}

	store := GetOpenCodeAccountStore()
	if err := store.SetStrategy(req.Strategy); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}

	writeAPI(w, http.StatusOK, apiResponse{Success: true, Message: "routing strategy updated"})
}

// POST /admin/api/opencode/accounts/add
func handleOpenCodeAccountAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: "method not allowed"})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	defer r.Body.Close()

	var req struct {
		Name         string `json:"name"`
		Token        string `json:"token"`
		RefreshToken string `json:"refresh_token"`
		OrgID        string `json:"org_id"`
		Route        string `json:"route"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: "invalid JSON"})
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Token = strings.TrimSpace(req.Token)
	if req.Name == "" {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: "account name is required"})
		return
	}
	if req.Token == "" && req.RefreshToken == "" {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: "token or refresh_token is required"})
		return
	}

	auth := req.Token
	if auth != "" && !strings.HasPrefix(auth, "Bearer ") {
		auth = "Bearer " + auth
	}

	store := GetOpenCodeAccountStore()
	record := OpenCodeAccountRecord{
		Name:             req.Name,
		Authorization:    auth,
		RefreshToken:     req.RefreshToken,
		OrgID:            req.OrgID,
		Route:            req.Route,
		CredentialSource: "manual",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
	}

	// If only refresh token provided, trigger initial refresh immediately
	if record.Authorization == "" && record.RefreshToken != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := store.AddAccount(record); err != nil {
			writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
			return
		}
		if err := store.RefreshAccountToken(ctx, record.Name); err != nil {
			writeAPI(w, http.StatusOK, apiResponse{
				Success: true,
				Message: fmt.Sprintf("account added, but initial token refresh failed: %v", err),
			})
			return
		}
		writeAPI(w, http.StatusOK, apiResponse{Success: true, Message: "account added and authenticated successfully"})
		return
	}

	if err := store.AddAccount(record); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}

	writeAPI(w, http.StatusOK, apiResponse{Success: true, Message: "account added successfully"})
}

// POST /admin/api/opencode/accounts/delete
func handleOpenCodeAccountDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: "method not allowed"})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	defer r.Body.Close()

	var req struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: "invalid JSON"})
		return
	}

	store := GetOpenCodeAccountStore()
	if err := store.DeleteAccount(req.Name); err != nil {
		writeAPI(w, http.StatusNotFound, apiResponse{Error: err.Error()})
		return
	}

	writeAPI(w, http.StatusOK, apiResponse{Success: true, Message: "account deleted"})
}

// POST /admin/api/opencode/accounts/route
func handleOpenCodeAccountRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: "method not allowed"})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	defer r.Body.Close()

	var req struct {
		Name  string `json:"name"`
		Route string `json:"route"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: "invalid JSON"})
		return
	}

	store := GetOpenCodeAccountStore()
	if err := store.UpdateAccountRoute(req.Name, req.Route); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}

	writeAPI(w, http.StatusOK, apiResponse{Success: true, Message: "route updated"})
}

// POST /admin/api/opencode/accounts/refresh
func handleOpenCodeAccountRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: "method not allowed"})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	defer r.Body.Close()

	var req struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: "invalid JSON"})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	store := GetOpenCodeAccountStore()
	if err := store.RefreshAccountToken(ctx, req.Name); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}

	writeAPI(w, http.StatusOK, apiResponse{Success: true, Message: "token refreshed successfully"})
}

// POST /admin/api/opencode/accounts/test
func handleOpenCodeAccountTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: "method not allowed"})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	defer r.Body.Close()

	var req struct {
		Name  string `json:"name"`
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: "invalid JSON"})
		return
	}

	store := GetOpenCodeAccountStore()
	acc, err := store.PickAccount(req.Name)
	if err != nil {
		writeAPI(w, http.StatusNotFound, apiResponse{Error: err.Error()})
		return
	}

	result, status := testOpenCodeAccount(acc, req.Model)
	result["status"] = status
	log.Printf("Test OpenCode account %q (route=%s): status=%s reason=%v",
		acc.Name, acc.Route, status, result["reason"])

	writeAPI(w, http.StatusOK, apiResponse{
		Success: status == "active",
		Message: status,
		Data:    result,
	})
}

// GET /admin/api/opencode/fidelity
func handleOpenCodeFidelity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: "method not allowed"})
		return
	}
	writeAPI(w, http.StatusOK, apiResponse{
		Success: true,
		Data:    GetFidelityProfileInfo(),
	})
}

func testOpenCodeAccount(acc *OpenCodeAccountRecord, modelID string) (map[string]any, string) {
	result := map[string]any{
		"name":       acc.Name,
		"route":      acc.Route,
		"token_mask": maskZenKey(acc.Authorization),
	}

	var zm *ZenModel
	if strings.TrimSpace(modelID) != "" {
		m, ok := resolveZenFreeModel(modelID)
		if !ok {
			result["reason"] = fmt.Sprintf("unknown model: %s", modelID)
			return result, "error"
		}
		zm = m
	} else {
		zm = zenProbeModel()
	}
	if zm == nil {
		result["reason"] = "no probe model available"
		return result, "error"
	}
	result["model"] = zm.ID

	// Determine proxy URL for account route
	pinProxy := resolveRouteProxyURL(acc.Route)
	if acc.Route == OpenCodeRouteDirect {
		pinProxy = "direct"
	}

	// Pinned probe call
	rawToken := strings.TrimPrefix(acc.Authorization, "Bearer ")
	keyParam := rawToken
	if acc.OrgID != "" {
		keyParam += "#" + acc.OrgID
	}

	testParams := map[string]any{
		"model":      zm.ID,
		"messages":   []any{map[string]any{"role": "user", "content": "Reply with exactly: OK"}},
		"max_tokens": 32,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	opts := zenCallOpts{pinKey: keyParam, pinProxy: pinProxy}
	var resp *http.Response
	var err error

	if zm.Upstream == "responses" {
		resp, _, err = callZenResponsesAPI(ctx, testParams, false, opts)
	} else {
		resp, _, err = callZenAPI(ctx, testParams, false, opts)
	}

	if err != nil {
		store := GetOpenCodeAccountStore()
		if isRateLimitedError(err) {
			store.CooldownAccount(acc.Name, 15*time.Minute)
			result["reason"] = fmt.Sprintf("Rate limited (429): %v", err)
			return result, "cooldown"
		}
		result["reason"] = fmt.Sprintf("Error: %v", err)
		return result, "error"
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		store := GetOpenCodeAccountStore()
		store.MarkAccountSuccess(acc.Name)
		result["reason"] = "OK (200)"
		return result, "active"
	}

	b, _ := io.ReadAll(resp.Body)
	result["reason"] = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, kit.Truncate(string(b), 120))
	return result, "error"
}

func isRateLimitedError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "429") || strings.Contains(s, "rate limit") || strings.Contains(s, "freeusagelimit")
}

