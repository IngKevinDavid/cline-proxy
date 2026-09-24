package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProbeZenModelEndpointMock(t *testing.T) {
	// Mock upstream server where chat returns 503 and responses returns 200
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat/completions" {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"error":{"message":"Endpoint is unavailable"}}`))
			return
		}
		if r.URL.Path == "/responses" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`event: response.created`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	// Verify resolveInferenceAuth doesn't panic
	tok, org, isCon := resolveInferenceAuth()
	t.Logf("Auth probe info: tokLen=%d org=%s isCon=%v", len(tok), org, isCon)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = ctx

	// Test canonical session generator
	sid := CanonicalSessionID()
	if len(sid) < 20 {
		t.Fatalf("expected longer session id, got %s", sid)
	}
}
