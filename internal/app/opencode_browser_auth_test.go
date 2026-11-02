package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOpenCodeDeviceFlowMock(t *testing.T) {
	pollCount := 0
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/code" {
			json.NewEncoder(w).Encode(map[string]any{
				"device_code":               "mock-device-123",
				"user_code":                 "ABCD-1234",
				"verification_uri_complete": "https://opencode.ai/auth/device?code=ABCD-1234",
				"expires_in":                300,
				"interval":                  1,
			})
			return
		}
		if r.URL.Path == "/token" {
			pollCount++
			if pollCount < 2 {
				json.NewEncoder(w).Encode(map[string]any{
					"error": "authorization_pending",
				})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "mock-access-token-999",
				"refresh_token": "mock-refresh-token-999",
				"expires_in":    3600,
				"org_id":        "mock-org-1",
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Direct test of device post
	client := mockServer.Client()
	var dev OpenCodeDeviceCode
	status, err := openCodeDevicePost(ctx, client, mockServer.URL+"/code", map[string]any{"client_id": "test"}, &dev)
	if err != nil || status != 200 {
		t.Fatalf("openCodeDevicePost /code failed: %v, status: %d", err, status)
	}
	if dev.UserCode != "ABCD-1234" {
		t.Errorf("expected user_code ABCD-1234, got %s", dev.UserCode)
	}

	var tok OpenCodeDeviceToken
	status, err = openCodeDevicePost(ctx, client, mockServer.URL+"/token", map[string]any{"device_code": "mock-device-123"}, &tok)
	if err != nil || status != 200 {
		t.Fatalf("openCodeDevicePost /token 1 failed: %v", err)
	}
	if tok.Error != "authorization_pending" {
		t.Errorf("expected authorization_pending, got %s", tok.Error)
	}

	status, err = openCodeDevicePost(ctx, client, mockServer.URL+"/token", map[string]any{"device_code": "mock-device-123"}, &tok)
	if err != nil || status != 200 {
		t.Fatalf("openCodeDevicePost /token 2 failed: %v", err)
	}
	if tok.AccessToken != "mock-access-token-999" {
		t.Errorf("expected mock-access-token-999, got %s", tok.AccessToken)
	}
}
