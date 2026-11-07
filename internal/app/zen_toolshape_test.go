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

	// 3. Verify mixed tool shaping: official read gate + passthrough client tool
	tools, ok := payload["tools"].([]any)
	if !ok {
		t.Fatalf("tools is not []any")
	}
	// Expected: 1 official read tool + 1 passthrough client tool = 2 tools
	if len(tools) != 2 {
		t.Fatalf("tools count = %d, want 2 (read + custom_agent_action)", len(tools))
	}

	toolNames := make(map[string]bool)
	for _, tool := range tools {
		fn := tool.(map[string]any)["function"].(map[string]any)
		toolNames[fn["name"].(string)] = true
	}
	for _, expected := range []string{"read", "custom_agent_action"} {
		if !toolNames[expected] {
			t.Errorf("missing tool %q in shaped tools", expected)
		}
	}
	_ = renames
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

func TestOfficialToolAliasMapping(t *testing.T) {
	// Client declares "bash" and "edit"
	payload := map[string]any{
		"model": "deepseek-v4.1-flash-free",
		"messages": []any{
			map[string]any{"role": "user", "content": "Run tests"},
		},
		"tools": []any{
			map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        "bash",
					"description": "Run shell commands",
					"parameters":  map[string]any{"type": "object"},
				},
			},
			map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        "edit",
					"description": "Edit file",
					"parameters":  map[string]any{"type": "object"},
				},
			},
		},
	}

	renames := ShapeZenChatRequest(payload)

	// Verify "shell" maps to "bash"
	if got := renames.Restore("shell"); got != "bash" {
		t.Errorf("renames.Restore('shell') = %q, want 'bash'", got)
	}
	if got := GlobalRestoreToolName("shell"); got != "bash" {
		t.Errorf("GlobalRestoreToolName('shell') = %q, want 'bash'", got)
	}

	// Verify "patch" maps to "edit"
	if got := renames.Restore("patch"); got != "edit" {
		t.Errorf("renames.Restore('patch') = %q, want 'edit'", got)
	}
	if got := GlobalRestoreToolName("patch"); got != "edit" {
		t.Errorf("GlobalRestoreToolName('patch') = %q, want 'edit'", got)
	}

	// Verify normalizeOpenAIResponse restores "shell" tool call back to "bash"
	rawResp := map[string]any{
		"choices": []any{
			map[string]any{
				"message": map[string]any{
					"role": "assistant",
					"tool_calls": []any{
						map[string]any{
							"id":   "call_shell_1",
							"type": "function",
							"function": map[string]any{
								"name":      "shell",
								"arguments": `{"command":"pytest"}`,
							},
						},
					},
				},
			},
		},
	}
	normalized := normalizeOpenAIResponse(rawResp)
	ch := normalized["choices"].([]any)[0].(map[string]any)
	fn := ch["message"].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "bash" {
		t.Errorf("normalizeOpenAIResponse tool name = %q, want 'bash'", fn["name"])
	}
}

func TestDeterministicSessionDerivation(t *testing.T) {
	// Turn 1
	paramsTurn1 := map[string]any{
		"messages": []any{
			map[string]any{"role": "system", "content": "You are a helpful assistant"},
			map[string]any{"role": "user", "content": "First turn instructions"},
		},
	}
	sid1 := DeriveRequestSessionID(paramsTurn1, nil)

	// Turn 2: same system + first user, but with assistant response and second user turn
	paramsTurn2 := map[string]any{
		"messages": []any{
			map[string]any{"role": "system", "content": "You are a helpful assistant"},
			map[string]any{"role": "user", "content": "First turn instructions"},
			map[string]any{"role": "assistant", "content": "Understood, proceeding."},
			map[string]any{"role": "user", "content": "Second turn question"},
		},
	}
	sid2 := DeriveRequestSessionID(paramsTurn2, nil)

	if sid1 != sid2 {
		t.Fatalf("session ID diverged across turns: turn1=%q turn2=%q", sid1, sid2)
	}
	if !strings.HasPrefix(sid1, "ses_") {
		t.Fatalf("session ID %q missing 'ses_' prefix", sid1)
	}
}

func TestNormalizeUsageCache(t *testing.T) {
	// Test prompt_tokens_details.cached_tokens
	u1 := map[string]any{
		"prompt_tokens":     float64(100),
		"completion_tokens": float64(20),
		"prompt_tokens_details": map[string]any{
			"cached_tokens": float64(80),
		},
	}
	normalizeUsageCache(u1)
	if u1["cache_read_input_tokens"] != int64(80) {
		t.Errorf("u1 cache_read_input_tokens = %v, want 80", u1["cache_read_input_tokens"])
	}

	// Test opencode.tokens.cache_read
	u2 := map[string]any{
		"prompt_tokens":     float64(200),
		"completion_tokens": float64(30),
		"opencode": map[string]any{
			"tokens": map[string]any{
				"cache_read": float64(150),
			},
		},
	}
	normalizeUsageCache(u2)
	if u2["cache_read_input_tokens"] != int64(150) {
		t.Errorf("u2 cache_read_input_tokens = %v, want 150", u2["cache_read_input_tokens"])
	}
	ptd, ok := u2["prompt_tokens_details"].(map[string]any)
	if !ok || ptd["cached_tokens"] != int64(150) {
		t.Errorf("u2 prompt_tokens_details = %v, want cached_tokens=150", u2["prompt_tokens_details"])
	}
}

func TestDetectClientWorkDir(t *testing.T) {
	// Test from cwd property
	p1 := map[string]any{
		"cwd": `G:\Datos_Bolivia\sc-prec4sa`,
	}
	if got := detectClientWorkDir(p1); got != "G:/Datos_Bolivia/sc-prec4sa" {
		t.Errorf("detectClientWorkDir(p1) = %q, want 'G:/Datos_Bolivia/sc-prec4sa'", got)
	}

	// Test from messages
	p2 := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "Please inspect files. <cwd>G:/Datos_Bolivia/sc-prec4sa</cwd>"},
		},
	}
	if got := detectClientWorkDir(p2); got != "G:/Datos_Bolivia/sc-prec4sa" {
		t.Errorf("detectClientWorkDir(p2) = %q, want 'G:/Datos_Bolivia/sc-prec4sa'", got)
	}

	// Test fallback
	p3 := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "Hello world"},
		},
	}
	env := opencodeEnvironment(p3)
	if strings.Contains(env, "01_proyectos/cline-proxy-foxy") {
		t.Errorf("opencodeEnvironment leaked internal proxy directory: %s", env)
	}
	if !strings.Contains(env, "Working directory: /workspace") {
		t.Errorf("opencodeEnvironment missing default /workspace: %s", env)
	}
}

func TestOpenCodeReadGateAndPathArgTranslation(t *testing.T) {
	// 1. No-tool request injects full official 2.0.22 read tool schema (with path/offset/limit)
	noToolsPayload := map[string]any{
		"model": "mimo-v2.6-flash-free",
		"messages": []any{
			map[string]any{"role": "user", "content": "Reply with PONG"},
		},
	}
	ShapeZenChatRequest(noToolsPayload)
	tools, ok := noToolsPayload["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("no-tools request tools = %v, want 1 official read gate tool", noToolsPayload["tools"])
	}
	fn, _ := tools[0].(map[string]any)["function"].(map[string]any)
	params, _ := fn["parameters"].(map[string]any)
	props, _ := params["properties"].(map[string]any)
	if _, hasPath := props["path"]; !hasPath {
		t.Fatalf("official read gate tool missing path in parameters.properties: %v", props)
	}

	// 2. Client with read_file(file_path) gets path <-> file_path argument translation
	withReadPayload := map[string]any{
		"model": "big-pickle",
		"messages": []any{
			map[string]any{"role": "user", "content": "Read package.json"},
		},
		"tools": []any{
			map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        "read_file",
					"description": "Read file",
					"parameters": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"file_path": map[string]any{"type": "string"},
						},
						"required": []any{"file_path"},
					},
				},
			},
		},
	}
	ShapeZenChatRequest(withReadPayload)
	translatedToClient := TranslateWireToolArgsToClient("read_file", `{"path":"package.json","limit":10}`)
	if !strings.Contains(translatedToClient, `"file_path":"package.json"`) || strings.Contains(translatedToClient, `"path"`) {
		t.Fatalf("TranslateWireToolArgsToClient = %s, want file_path instead of path", translatedToClient)
	}
	translatedToWire := TranslateClientToolArgsToWire("read", `{"file_path":"package.json"}`)
	if !strings.Contains(translatedToWire, `"path":"package.json"`) || strings.Contains(translatedToWire, `"file_path"`) {
		t.Fatalf("TranslateClientToolArgsToWire = %s, want path instead of file_path", translatedToWire)
	}
}


