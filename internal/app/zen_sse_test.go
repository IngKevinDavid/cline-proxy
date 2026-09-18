package app

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func sseEvent(typ, fields string) string {
	return "data: " + `{"type":"` + typ + `",` + fields + "}\n\n"
}

func sseResp(body string) *http.Response {
	return &http.Response{Body: io.NopCloser(strings.NewReader(body))}
}

func toolCallArgs(t *testing.T, chat map[string]any) []string {
	t.Helper()
	choices, _ := chat["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(choices))
	}
	msg, _ := choices[0].(map[string]any)["message"].(map[string]any)
	calls, _ := msg["tool_calls"].([]any)
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		fn, _ := c.(map[string]any)["function"].(map[string]any)
		args, _ := fn["arguments"].(string)
		out = append(out, args)
	}
	return out
}

// 两个连续 function_call 项、各自带 delta 流（spark 产生两次调用时的形态）。
func twoCallSSE() string {
	var b strings.Builder
	b.WriteString(sseEvent("response.created", `"response":{"id":"resp_x"}`))
	b.WriteString(sseEvent("response.output_item.added", `"output_index":0,"item":{"type":"function_call","id":"call_1","name":"get_weather"}`))
	b.WriteString(sseEvent("response.function_call_arguments.delta", `"item_id":"call_1","delta":"{\"city\":\"Hanoi\""}`))
	b.WriteString(sseEvent("response.function_call_arguments.delta", `"item_id":"call_1","delta":"}"}`))
	b.WriteString(sseEvent("response.output_item.done", `"output_index":0,"item":{"type":"function_call","id":"call_1","name":"get_weather","arguments":"{\"city\":\"Hanoi\"}"}`))
	b.WriteString(sseEvent("response.output_item.added", `"output_index":1,"item":{"type":"function_call","id":"call_2","name":"get_weather"}`))
	b.WriteString(sseEvent("response.function_call_arguments.delta", `"item_id":"call_2","delta":"{\"city\":\"London\"}"}`))
	b.WriteString(sseEvent("response.output_item.done", `"output_index":1,"item":{"type":"function_call","id":"call_2","name":"get_weather","arguments":"{\"city\":\"London\"}"}`))
	b.WriteString(sseEvent("response.completed", `"response":{"id":"resp_x","usage":{"input_tokens":10,"output_tokens":20}}`))
	return b.String()
}

func TestResponsesSSEToChatTwoToolCalls(t *testing.T) {
	chat, err := responsesSSEToChat(sseResp(twoCallSSE()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := toolCallArgs(t, chat)
	want := []string{`{"city":"Hanoi"}`, `{"city":"London"}`}
	if len(got) != 2 {
		t.Fatalf("expected 2 tool_calls, got %d: %v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d arguments = %q, want %q (jammed multi-call bug)", i, got[i], want[i])
		}
	}
}

// 无 delta 事件，只有 output_item.done 给全参（mimo 类上游的形态）：
// 两个调用必须各自归位，不能串参数、不能互相覆盖。
func TestResponsesSSEToChatDoneOnlyTwoCalls(t *testing.T) {
	var b strings.Builder
	b.WriteString(sseEvent("response.output_item.done", `"output_index":0,"item":{"type":"function_call","id":"call_a","call_id":"call_a","name":"read","arguments":"{\"file_path\":\"a.txt\"}"}`))
	b.WriteString(sseEvent("response.output_item.done", `"output_index":1,"item":{"type":"function_call","id":"call_b","call_id":"call_b","name":"read","arguments":"{\"file_path\":\"b.txt\"}"}`))
	b.WriteString(sseEvent("response.output_item.done", `"output_index":2,"item":{"type":"message","content":[{"type":"output_text","text":"done"}]}`))
	b.WriteString(sseEvent("response.completed", `"response":{"usage":{"input_tokens":1,"output_tokens":2}}`))
	chat, err := responsesSSEToChat(sseResp(b.String()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := toolCallArgs(t, chat)
	want := []string{`{"file_path":"a.txt"}`, `{"file_path":"b.txt"}`}
	if len(got) != 2 {
		t.Fatalf("expected 2 tool_calls, got %d: %v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d arguments = %q, want %q", i, got[i], want[i])
		}
	}
	if c, _ := chat["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"].(string); c != "done" {
		t.Errorf("content = %q, want %q", c, "done")
	}
}

// 只有 arguments.done 收尾事件（无 output_item.done、无 delta）：
// 参数必须被采纳，不能丢成空调用。
func TestResponsesSSEToChatArgumentsDoneOnly(t *testing.T) {
	var b strings.Builder
	b.WriteString(sseEvent("response.output_item.added", `"output_index":0,"item":{"type":"function_call","id":"call_1","name":"glob"}`))
	b.WriteString(sseEvent("response.function_call_arguments.done", `"item_id":"call_1","arguments":"{\"pattern\":\"*.go\"}"`))
	b.WriteString(sseEvent("response.completed", `"response":{"usage":{"input_tokens":1,"output_tokens":2}}`))
	chat, err := responsesSSEToChat(sseResp(b.String()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := toolCallArgs(t, chat)
	if len(got) != 1 || got[0] != `{"pattern":"*.go"}` {
		t.Fatalf("arguments = %v, want [{\"pattern\":\"*.go\"}]", got)
	}
}

// 领先于 added 的孤儿 delta（顺序异常）：单调用场景下仍要归位，
// 不能凭空生成第二个无名调用。
func TestResponsesSSEToChatOrphanDeltaAdopted(t *testing.T) {
	var b strings.Builder
	b.WriteString(sseEvent("response.function_call_arguments.delta", `"item_id":"call_1","delta":"{\"q\":\"x\"}"`))
	b.WriteString(sseEvent("response.output_item.added", `"output_index":0,"item":{"type":"function_call","id":"call_1","name":"grep"}`))
	b.WriteString(sseEvent("response.output_item.done", `"output_index":0,"item":{"type":"function_call","id":"call_1","name":"grep"}`))
	b.WriteString(sseEvent("response.completed", `"response":{"usage":{"input_tokens":1,"output_tokens":2}}`))
	chat, err := responsesSSEToChat(sseResp(b.String()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := toolCallArgs(t, chat)
	if len(got) != 1 || got[0] != `{"q":"x"}` {
		t.Fatalf("arguments = %v, want [{\"q\":\"x\"}] (orphan delta not adopted)", got)
	}
	msg, _ := chat["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	name, _ := msg["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["name"].(string)
	if name != "grep" {
		t.Errorf("name = %q, want grep (added should update in place)", name)
	}
}

// 无参调用：arguments 必须是合法 JSON 字符串 "{}"，空串会让 IDE 解析失败。
func TestResponsesSSEToChatEmptyArgsBecomeObject(t *testing.T) {
	var b strings.Builder
	b.WriteString(sseEvent("response.output_item.done", `"output_index":0,"item":{"type":"function_call","id":"call_1","name":"skill","arguments":""}`))
	b.WriteString(sseEvent("response.completed", `"response":{"usage":{"input_tokens":1,"output_tokens":2}}`))
	chat, err := responsesSSEToChat(sseResp(b.String()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := toolCallArgs(t, chat)
	if len(got) != 1 || got[0] != "{}" {
		t.Fatalf("arguments = %v, want [{}]", got)
	}
}

// 无名空壳（added 后既无参数也无 name）不得产出一个空函数名调用。
func TestResponsesSSEToChatNamelessShellDropped(t *testing.T) {
	var b strings.Builder
	b.WriteString(sseEvent("response.output_item.added", `"output_index":0,"item":{"type":"function_call","id":"call_1"}`))
	b.WriteString(sseEvent("response.output_item.done", `"output_index":1,"item":{"type":"message","content":[{"type":"output_text","text":"hi"}]}`))
	b.WriteString(sseEvent("response.completed", `"response":{"usage":{"input_tokens":1,"output_tokens":2}}`))
	chat, err := responsesSSEToChat(sseResp(b.String()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	msg, _ := chat["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	if _, ok := msg["tool_calls"]; ok {
		t.Fatalf("expected no tool_calls, got %v", msg["tool_calls"])
	}
}

// delta 已给正文时不再采纳 output_item.done 的全量文本（否则文本翻倍）。
func TestResponsesSSEToChatNoTextDuplication(t *testing.T) {
	var b strings.Builder
	b.WriteString(sseEvent("response.output_text.delta", `"delta":"Hello "`))
	b.WriteString(sseEvent("response.output_text.delta", `"delta":"world"`))
	b.WriteString(sseEvent("response.output_item.done", `"output_index":0,"item":{"type":"message","content":[{"type":"output_text","text":"Hello world"}]}`))
	b.WriteString(sseEvent("response.completed", `"response":{"usage":{"input_tokens":1,"output_tokens":2}}`))
	chat, err := responsesSSEToChat(sseResp(b.String()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c, _ := chat["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"].(string)
	if c != "Hello world" {
		t.Fatalf("content = %q, want %q", c, "Hello world")
	}
}

// 非 SSE 响应体（上游 JSON 错误）：必须报错，不能下发空成功。
func TestResponsesSSEToChatNonSSEBodyErrors(t *testing.T) {
	body := `{"error":{"message":"rate limited","type":"rate_limit_error"}}`
	_, err := responsesSSEToChat(sseResp(body))
	if err == nil {
		t.Fatal("expected error for non-SSE body, got nil")
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("error = %v, want it to carry upstream message", err)
	}
}

// 上游 response.failed 携带的 error.message 要带出去（否则只剩 "error"）。
func TestResponsesSSEToChatFailedCarriesMessage(t *testing.T) {
	body := sseEvent("response.failed", `"response":{"error":{"message":"session expired"},"incomplete_details":{"reason":"error"}}`)
	_, err := responsesSSEToChat(sseResp(body))
	if err == nil || !strings.Contains(err.Error(), "session expired") {
		t.Fatalf("error = %v, want it to contain 'session expired'", err)
	}
}
