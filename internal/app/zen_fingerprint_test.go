package app

import (
	"bytes"
	"io"
	"net/http"
	"regexp"
	"testing"
)

func TestCanonicalSessionID(t *testing.T) {
	re := regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`)
	for i := 0; i < 50; i++ {
		sid := CanonicalSessionID()
		if !re.MatchString(sid) {
			t.Fatalf("session ID %q does not match required format", sid)
		}
	}
}

func TestApplyFreeTierChatFingerprint(t *testing.T) {
	body := map[string]any{
		"model":    "mimo-v2.5-free",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		"stream":   false,
	}
	ApplyFreeTierChatFingerprint(body)
	if body["stream"] != true {
		t.Errorf("expected stream to be true")
	}
	if body["tool_choice"] != "none" {
		t.Errorf("expected tool_choice to be none for text request, got %v", body["tool_choice"])
	}
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 4 {
		t.Fatalf("expected 4 gate tools, got %d", len(tools))
	}
}

func TestAggregateOpenAIStream(t *testing.T) {
	sseData := `data: {"id":"gen-1","object":"chat.completion.chunk","choices":[{"delta":{"content":"Hello"}}]}

data: {"id":"gen-1","object":"chat.completion.chunk","choices":[{"delta":{"content":" world!"},"finish_reason":"stop"}]}

data: [DONE]
`
	resp := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewBufferString(sseData)),
	}
	agg, err := AggregateOpenAIStream(resp, "test-model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if agg.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", agg.StatusCode)
	}
}
