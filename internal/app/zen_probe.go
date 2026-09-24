package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"cline-go-proxy/internal/kit"
)

const opencodeInferenceBase = "https://opencode.ai/inference/openai/v1"

// resolveInferenceAuth resolves the bearer token and headers for probing or requests.
// Priority:
// 1. Console OAuth token (from opencode db)
// 2. Static Zen API key (oc_sk_...)
func resolveInferenceAuth() (token string, orgID string, isConsole bool) {
	if auth, err := GetConsoleAuth(); err == nil && auth != nil && auth.AccessToken != "" {
		return auth.AccessToken, auth.ActiveOrgID, true
	}
	if key := pickZenKey(); key != "" {
		return key, "", false
	}
	return "", "", false
}

// buildInferenceHeaders sets the required headers for OpenCode's edge security.
func buildInferenceHeaders(req *http.Request, token string, orgID string, isConsole bool) {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "opencode/1.18.31 ai-sdk/provider-utils/4.0.40 runtime/bun/1.3.14")
	req.Header.Set("x-opencode-session", CanonicalSessionID())
	req.Header.Set("x-opencode-client", "cli")
	req.Header.Set("x-opencode-request", "msg_"+kit.RandAlphaNum(20))
	if isConsole && orgID != "" {
		req.Header.Set("x-opencode-org-id", orgID)
	}
}

// ProbeZenModelEndpoint probes the given model. It tests chat/completions first;
// if that fails, it falls back to responses. The working route is persisted.
func ProbeZenModelEndpoint(ctx context.Context, modelID string) (string, error) {
	token, orgID, isConsole := resolveInferenceAuth()
	if token == "" {
		return "", fmt.Errorf("no valid opencode credentials found (neither console oauth nor zen keys)")
	}

	client := &http.Client{Timeout: 12 * time.Second}

	// Step 1: Probe chat/completions
	chatURL := opencodeInferenceBase + "/chat/completions"
	chatPayload := map[string]any{
		"model": modelID,
		"messages": []any{
			map[string]any{"role": "user", "content": "ping"},
		},
		"max_tokens": 5,
	}
	ApplyFreeTierChatFingerprint(chatPayload)
	chatBody, _ := json.Marshal(chatPayload)

	req1, err := http.NewRequestWithContext(ctx, "POST", chatURL, bytes.NewReader(chatBody))
	if err == nil {
		buildInferenceHeaders(req1, token, orgID, isConsole)
		resp1, err1 := client.Do(req1)
		if err1 == nil {
			status := resp1.StatusCode
			bodyText := kit.Truncate(kit.ReadBody(resp1), 200)
			resp1.Body.Close()

			if status == http.StatusOK {
				learnZenEndpoint(modelID, "chat")
				log.Printf("zen probe: model %s verified on chat/completions (HTTP 200)", modelID)
				return "chat", nil
			}
			log.Printf("zen probe: model %s chat/completions returned HTTP %d: %s, falling back to responses", modelID, status, bodyText)
		} else {
			log.Printf("zen probe: model %s chat/completions request error: %v, falling back to responses", modelID, err1)
		}
	}

	// Step 2: Probe responses
	respURL := opencodeInferenceBase + "/responses"
	responsesPayload := map[string]any{
		"model": modelID,
		"input": []any{
			map[string]any{"role": "user", "content": "ping"},
		},
		"max_tokens": 5,
	}
	ApplyFreeTierResponsesFingerprint(responsesPayload)
	respBody, _ := json.Marshal(responsesPayload)

	req2, err := http.NewRequestWithContext(ctx, "POST", respURL, bytes.NewReader(respBody))
	if err != nil {
		return "", fmt.Errorf("create responses request: %w", err)
	}
	buildInferenceHeaders(req2, token, orgID, isConsole)

	resp2, err2 := client.Do(req2)
	if err2 != nil {
		return "", fmt.Errorf("responses probe network error: %w", err2)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode == http.StatusOK {
		learnZenEndpoint(modelID, "responses")
		log.Printf("zen probe: model %s fallback verified on responses (HTTP 200)", modelID)
		return "responses", nil
	}

	bodyText2 := kit.Truncate(kit.ReadBody(resp2), 200)
	return "", fmt.Errorf("model %s failed on both endpoints (responses status %d: %s)", modelID, resp2.StatusCode, bodyText2)
}

// AutoVerifyZenModels probes any active free models that haven't been verified yet.
func AutoVerifyZenModels(ctx context.Context) (int, int) {
	initZenModels()
	zenModelsMu.RLock()
	var toProbe []string
	for id, m := range zenModels {
		if isZenFreeModel(m) && (m.Upstream == "" || m.Upstream == "unknown") {
			toProbe = append(toProbe, id)
		}
	}
	zenModelsMu.RUnlock()

	verified := 0
	failed := 0

	for _, id := range toProbe {
		select {
		case <-ctx.Done():
			return verified, failed
		default:
		}
		endpoint, err := ProbeZenModelEndpoint(ctx, id)
		if err == nil && endpoint != "" {
			verified++
		} else {
			failed++
		}
	}

	if verified > 0 || failed > 0 {
		log.Printf("zen probe auto-verification complete: %d verified, %d failed", verified, failed)
	}
	return verified, failed
}

// PruneLearnedEndpoints removes entries for models that no longer exist in desired catalog.
func PruneLearnedEndpoints(desired map[string]bool) {
	if len(desired) == 0 {
		return
	}
	zenEndpointSaveMu.Lock()
	defer zenEndpointSaveMu.Unlock()

	learned := loadZenEndpointsFile()
	if len(learned) == 0 {
		return
	}

	changed := false
	cleaned := make(map[string]string)
	for id, ep := range learned {
		if desired[id] {
			cleaned[id] = ep
		} else {
			log.Printf("zen probe: pruned deprecated model %s from learned endpoints", id)
			changed = true
		}
	}

	if changed {
		data, err := json.MarshalIndent(cleaned, "", "  ")
		if err != nil {
			return
		}
		tmp := zenEndpointFile() + ".tmp"
		_ = os.WriteFile(tmp, data, 0600)
		_ = os.Rename(tmp, zenEndpointFile())
	}
}
