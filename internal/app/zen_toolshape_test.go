package app

import (
	"strings"
	"testing"
)

func TestToolRenamesAndRestoration(t *testing.T) {
	renames := NewToolRenames()
	used := map[string]bool{}

	clientTool := "pdf_reader_extract"
	wireName := ClientToolWireName(clientTool, used)

	if !strings.HasPrefix(wireName, "clint_") {
		t.Fatalf("wireName = %q, want prefix 'clint_'", wireName)
	}
	renames.Add(clientTool, wireName)

	// Test exact lookup
	restored := renames.Restore(wireName)
	if restored != clientTool {
		t.Fatalf("renames.Restore(%q) = %q, want %q", wireName, restored, clientTool)
	}

	// Test global restoration
	globalRestored := GlobalRestoreToolName(wireName)
	if globalRestored != clientTool {
		t.Fatalf("GlobalRestoreToolName(%q) = %q, want %q", wireName, globalRestored, clientTool)
	}

	// Test fallback: unknown clint_ hash strips clint_
	unseen := "clint_unknown99"
	fallbackRestored := GlobalRestoreToolName(unseen)
	if fallbackRestored != "unknown99" {
		t.Fatalf("GlobalRestoreToolName(%q) = %q, want %q", unseen, fallbackRestored, "unknown99")
	}
}

func TestShapeZenChatRequest(t *testing.T) {
	payload := map[string]any{
		"model": "deepseek-v4.1-flash-free",
		"messages": []any{
			map[string]any{"role": "user", "content": "Hello"},
		},
		"tools": []any{
			map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        "custom_agent_action",
					"description": "Perform custom action",
					"parameters":  map[string]any{"type": "object"},
				},
			},
		},
	}

	renames := ShapeZenChatRequest(payload)

	// 1. Verify stream & limits
	if payload["stream"] != true {
		t.Errorf("stream = %v, want true", payload["stream"])
	}
	if payload["max_completion_tokens"] != v2CompletionLimit {
		t.Errorf("max_completion_tokens = %v, want %d", payload["max_completion_tokens"], v2CompletionLimit)
	}

	// 2. Verify official system prompt injection
	messages, ok := payload["messages"].([]any)
	if !ok || len(messages) < 2 {
		t.Fatalf("messages = %v, expected at least 2 messages", payload["messages"])
	}
	sysMsg, _ := messages[0].(map[string]any)
	if sysMsg["role"] != "system" {
		t.Errorf("first message role = %v, want 'system'", sysMsg["role"])
	}
	sysContent, _ := sysMsg["content"].(string)
	if !strings.Contains(sysContent, opencodePromptMarker) && !strings.Contains(sysContent, "You are an AI agent running in OpenCode") {
		t.Errorf("system content missing marker %q", opencodePromptMarker)
	}
	if !strings.Contains(sysContent, "<env>") {
		t.Errorf("system content missing <env> block")
	}

	// 3. Verify tool obfuscation and completion
	tools, ok := payload["tools"].([]any)
	if !ok {
		t.Fatalf("tools is not []any")
	}
	// Expected: 1 obfuscated client tool + 13 official tools = 14 tools
	if len(tools) != 14 {
		t.Fatalf("tools count = %d, want 14 (1 client + 13 official)", len(tools))
	}

	// Check that custom_agent_action is obfuscated
	firstTool := tools[0].(map[string]any)["function"].(map[string]any)
	wireName := firstTool["name"].(string)
	if !strings.HasPrefix(wireName, "clint_") {
		t.Errorf("client tool name = %q, expected 'clint_' prefix", wireName)
	}
	if wire, ok := renames.Wire("custom_agent_action"); !ok || wire != wireName {
		t.Errorf("renames.Wire('custom_agent_action') = %q, want %q", wire, wireName)
	}

	// Check that official tools are present (e.g. patch, edit, execute)
	toolNames := make(map[string]bool)
	for _, tool := range tools {
		fn := tool.(map[string]any)["function"].(map[string]any)
		toolNames[fn["name"].(string)] = true
	}
	for _, expected := range []string{"patch", "edit", "shell", "read", "grep"} {
		if !toolNames[expected] {
			t.Errorf("missing official tool %q in shaped tools", expected)
		}
	}
}

func TestNormalizeOpenAIResponseRestoresToolName(t *testing.T) {
	clientName := "analyze_data_file"
	wireName := ClientToolWireName(clientName, nil)
	RememberToolRename(wireName, clientName)

	// Test non-streaming message
	rawResponse := map[string]any{
		"id": "chatcmpl-test",
		"choices": []any{
			map[string]any{
				"message": map[string]any{
					"role": "assistant",
					"tool_calls": []any{
						map[string]any{
							"id":   "call_123",
							"type": "function",
							"function": map[string]any{
								"name":      wireName,
								"arguments": "{\"path\":\"test.txt\"}",
							},
						},
					},
				},
			},
		},
	}

	normalized := normalizeOpenAIResponse(rawResponse)
	choices := normalized["choices"].([]any)
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	tcs := msg["tool_calls"].([]any)
	fn := tcs[0].(map[string]any)["function"].(map[string]any)

	if fn["name"] != clientName {
		t.Errorf("normalized tool name = %q, want restored %q", fn["name"], clientName)
	}

	// Test streaming delta
	streamResponse := map[string]any{
		"choices": []any{
			map[string]any{
				"delta": map[string]any{
					"tool_calls": []any{
						map[string]any{
							"index": float64(0),
							"function": map[string]any{
								"name": wireName,
							},
						},
					},
				},
			},
		},
	}

	normStream := normalizeOpenAIResponse(streamResponse)
	streamChoices := normStream["choices"].([]any)
	delta := streamChoices[0].(map[string]any)["delta"].(map[string]any)
	streamTCs := delta["tool_calls"].([]any)
	streamFn := streamTCs[0].(map[string]any)["function"].(map[string]any)

	if streamFn["name"] != clientName {
		t.Errorf("normalized stream tool name = %q, want restored %q", streamFn["name"], clientName)
	}
}

