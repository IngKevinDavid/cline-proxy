package app

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// CanonicalSessionID generates a session ID conforming to OpenCode's regex:
// ^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$
func CanonicalSessionID() string {
	hexPart := make([]byte, 6) // 12 hex digits
	_, _ = rand.Read(hexPart)
	h := hex.EncodeToString(hexPart)

	const alnum = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	alnumBytes := make([]byte, 14)
	randBytes := make([]byte, 14)
	_, _ = rand.Read(randBytes)
	for i := 0; i < 14; i++ {
		alnumBytes[i] = alnum[int(randBytes[i])%len(alnum)]
	}

	return fmt.Sprintf("ses_%s%s", h, string(alnumBytes))
}

// CanonicalGateTools returns the 4 tools OpenCode FreeTier checks for:
// bash, glob, grep, read formatted for Chat Completions.
func CanonicalGateTools() []any {
	names := []string{"bash", "glob", "grep", "read"}
	tools := make([]any, len(names))
	for i, name := range names {
		tools[i] = map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        name,
				"description": name,
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			},
		}
	}
	return tools
}

// CanonicalResponsesGateTools returns the 4 tools formatted for Responses API.
func CanonicalResponsesGateTools() []any {
	names := []string{"bash", "glob", "grep", "read"}
	tools := make([]any, len(names))
	for i, name := range names {
		tools[i] = map[string]any{
			"type":        "function",
			"name":        name,
			"description": name,
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		}
	}
	return tools
}

// ApplyFreeTierChatFingerprint injects the 4 required gate tools and stream mode
// to pass OpenCode's edge security gate.
func ApplyFreeTierChatFingerprint(body map[string]any) {
	body["stream"] = true

	// Check if client provided tools
	clientTools, _ := body["tools"].([]any)
	hasTools := len(clientTools) > 0

	// Ensure all 4 gate tools are in tools list
	existing := make(map[string]bool)
	for _, t := range clientTools {
		if tm, ok := t.(map[string]any); ok {
			if fn, ok := tm["function"].(map[string]any); ok {
				if name, ok := fn["name"].(string); ok {
					existing[name] = true
				}
			}
		}
	}

	merged := append([]any{}, clientTools...)
	for _, gt := range CanonicalGateTools() {
		gtm := gt.(map[string]any)
		fn := gtm["function"].(map[string]any)
		name := fn["name"].(string)
		if !existing[name] {
			merged = append(merged, gt)
		}
	}
	body["tools"] = merged

	if !hasTools {
		// Pure text request: tool_choice none ensures models do not output blank text
		body["tool_choice"] = "none"
	} else if tc, ok := body["tool_choice"].(string); !ok || tc == "" {
		body["tool_choice"] = "auto"
	}

	body["stream_options"] = map[string]any{"include_usage": true}
}

// ApplyFreeTierResponsesFingerprint transforms payload for OpenCode /responses API.
func ApplyFreeTierResponsesFingerprint(body map[string]any) {
	body["stream"] = true

	// In /responses, input represents the conversation history
	if _, ok := body["input"]; !ok {
		if msgs, ok := body["messages"].([]any); ok {
			input := make([]any, 0, len(msgs))
			for _, m := range msgs {
				if mm, ok := m.(map[string]any); ok {
					item := map[string]any{
						"role":    mm["role"],
						"content": mm["content"],
					}
					input = append(input, item)
				}
			}
			body["input"] = input
		}
	}

	// Tools in /responses must be flat
	clientTools, _ := body["tools"].([]any)
	existing := make(map[string]bool)
	flatTools := make([]any, 0)
	for _, t := range clientTools {
		if tm, ok := t.(map[string]any); ok {
			if fn, ok := tm["function"].(map[string]any); ok {
				name, _ := fn["name"].(string)
				existing[name] = true
				flatTools = append(flatTools, map[string]any{
					"type":        "function",
					"name":        name,
					"description": fn["description"],
					"parameters":  fn["parameters"],
				})
			} else if name, ok := tm["name"].(string); ok {
				existing[name] = true
				flatTools = append(flatTools, tm)
			}
		}
	}

	for _, gt := range CanonicalResponsesGateTools() {
		gtm := gt.(map[string]any)
		name := gtm["name"].(string)
		if !existing[name] {
			flatTools = append(flatTools, gt)
		}
	}
	body["tools"] = flatTools
}

// AggregateOpenAIStream aggregates an SSE response from OpenCode into a complete
// non-streaming chat.completion response.
func AggregateOpenAIStream(resp *http.Response, model string) (*http.Response, error) {
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	// Allocate 1MB max buffer for SSE lines
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	var (
		id        = fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
		created   = time.Now().Unix()
		content   strings.Builder
		reasoning strings.Builder
		usage     map[string]any
		role      = "assistant"
		stopCause = "stop"
	)

	for scanner.Scan() {
		line := scanner.Text()
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}

		var chunk map[string]any
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if cid, ok := chunk["id"].(string); ok && cid != "" {
			id = cid
		}
		if cr, ok := chunk["created"].(float64); ok && cr > 0 {
			created = int64(cr)
		}
		if u, ok := chunk["usage"].(map[string]any); ok {
			usage = u
		}

		if choices, ok := chunk["choices"].([]any); ok && len(choices) > 0 {
			if first, ok := choices[0].(map[string]any); ok {
				if delta, ok := first["delta"].(map[string]any); ok {
					if r, ok := delta["role"].(string); ok && r != "" {
						role = r
					}
					if c, ok := delta["content"].(string); ok {
						content.WriteString(c)
					}
					if rs, ok := delta["reasoning"].(string); ok {
						reasoning.WriteString(rs)
					}
				}
				if fr, ok := first["finish_reason"].(string); ok && fr != "" {
					stopCause = fr
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading upstream SSE: %w", err)
	}

	msgObj := map[string]any{
		"role":    role,
		"content": content.String(),
	}
	if reasoning.Len() > 0 {
		msgObj["reasoning"] = reasoning.String()
	}

	resMap := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []any{
			map[string]any{
				"index":         0,
				"message":       msgObj,
				"finish_reason": stopCause,
			},
		},
	}
	if usage != nil {
		resMap["usage"] = usage
	} else {
		resMap["usage"] = map[string]any{
			"prompt_tokens":     0,
			"completion_tokens": 0,
			"total_tokens":      0,
		}
	}

	jsonBytes, err := json.Marshal(resMap)
	if err != nil {
		return nil, fmt.Errorf("marshal aggregated chat completion: %w", err)
	}

	syntheticResp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(jsonBytes)),
	}
	syntheticResp.Header.Set("Content-Type", "application/json")
	return syntheticResp, nil
}
