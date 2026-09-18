package app

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// synthetic responses SSE with TWO sequential function_call items, each with
// delta-streamed arguments — the case spark produces when it emits two calls.
func sseBody() io.ReadCloser {
	ev := func(typ string, fields string) string {
		return "data: " + `{"type":"` + typ + `",` + fields + "}\n\n"
	}
	var b strings.Builder
	b.WriteString(ev("response.created", `"response":{"id":"resp_x"}`))
	b.WriteString(ev("response.output_item.added", `"output_index":0,"item":{"type":"function_call","id":"call_1","name":"get_weather"}`))
	b.WriteString(ev("response.function_call_arguments.delta", `"item_id":"call_1","delta":"{\"city\":\"Hanoi\""}`))
	b.WriteString(ev("response.function_call_arguments.delta", `"item_id":"call_1","delta":"}"}`))
	b.WriteString(ev("response.output_item.done", `"output_index":0,"item":{"type":"function_call","id":"call_1","name":"get_weather","arguments":"{\"city\":\"Hanoi\"}"}`))
	b.WriteString(ev("response.output_item.added", `"output_index":1,"item":{"type":"function_call","id":"call_2","name":"get_weather"}`))
	b.WriteString(ev("response.function_call_arguments.delta", `"item_id":"call_2","delta":"{\"city\":\"London\"}"}`))
	b.WriteString(ev("response.output_item.done", `"output_index":1,"item":{"type":"function_call","id":"call_2","name":"get_weather","arguments":"{\"city\":\"London\"}"}`))
	b.WriteString(ev("response.completed", `"response":{"id":"resp_x","usage":{"input_tokens":10,"output_tokens":20}}`))
	return io.NopCloser(strings.NewReader(b.String()))
}

func TestResponsesSSEToChatTwoToolCalls(t *testing.T) {
	chat, err := responsesSSEToChat(&http.Response{Body: sseBody()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tcs, _ := chat["choices"].([]any)
	if len(tcs) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(tcs))
	}
	msg, _ := tcs[0].(map[string]any)["message"].(map[string]any)
	calls, _ := msg["tool_calls"].([]any)
	if len(calls) != 2 {
		t.Fatalf("expected 2 tool_calls, got %d: %v", len(calls), calls)
	}
	for i, want := range []string{`{"city":"Hanoi"}`, `{"city":"London"}`} {
		got, _ := calls[i].(map[string]any)["function"].(map[string]any)["arguments"].(string)
		if got != want {
			t.Errorf("call %d arguments = %q, want %q (jammed multi-call bug)", i, got, want)
		}
	}
}
