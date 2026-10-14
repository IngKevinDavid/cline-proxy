package app

import (
	"bytes"
	"cline-go-proxy/internal/kit"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	openCodeDaemonDefaultPort = 49374
	openCodeDaemonBaseURL     = "http://127.0.0.1:49374"
)

var (
	zenDaemonMu         sync.RWMutex
	zenDaemonPassword   string
	zenDaemonLastCheck  time.Time
	zenDaemonAvailable  bool
	zenDaemonCheckDelay = 15 * time.Second
)

// getOpenCodeServicePassword discovers the local OpenCode daemon password from service.json.
func getOpenCodeServicePassword() string {
	zenDaemonMu.RLock()
	if zenDaemonPassword != "" && time.Since(zenDaemonLastCheck) < 5*time.Minute {
		pwd := zenDaemonPassword
		zenDaemonMu.RUnlock()
		return pwd
	}
	zenDaemonMu.RUnlock()

	zenDaemonMu.Lock()
	defer zenDaemonMu.Unlock()

	var candidatePaths []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidatePaths = append(candidatePaths, filepath.Join(home, ".config", "opencode", "service.json"))
	}
	if userProfile := os.Getenv("USERPROFILE"); userProfile != "" {
		candidatePaths = append(candidatePaths, filepath.Join(userProfile, ".config", "opencode", "service.json"))
	}
	if appData := os.Getenv("APPDATA"); appData != "" {
		candidatePaths = append(candidatePaths, filepath.Join(appData, "opencode", "service.json"))
	}

	for _, p := range candidatePaths {
		data, err := os.ReadFile(p)
		if err != nil || len(data) == 0 {
			continue
		}
		var cfg struct {
			Password string `json:"password"`
		}
		if err := json.Unmarshal(data, &cfg); err == nil && cfg.Password != "" {
			zenDaemonPassword = cfg.Password
			return zenDaemonPassword
		}
	}
	return zenDaemonPassword
}

// isLocalOpenCodeDaemonAvailable probes the local OpenCode service health at http://127.0.0.1:49374/api/info.
func isLocalOpenCodeDaemonAvailable() bool {
	zenDaemonMu.RLock()
	if time.Since(zenDaemonLastCheck) < zenDaemonCheckDelay {
		avail := zenDaemonAvailable
		zenDaemonMu.RUnlock()
		return avail
	}
	zenDaemonMu.RUnlock()

	zenDaemonMu.Lock()
	defer zenDaemonMu.Unlock()

	zenDaemonLastCheck = time.Now()

	pwd := zenDaemonPassword
	if pwd == "" {
		// Discover without holding outer lock indefinitely
		var candidatePaths []string
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			candidatePaths = append(candidatePaths, filepath.Join(home, ".config", "opencode", "service.json"))
		}
		if userProfile := os.Getenv("USERPROFILE"); userProfile != "" {
			candidatePaths = append(candidatePaths, filepath.Join(userProfile, ".config", "opencode", "service.json"))
		}
		for _, p := range candidatePaths {
			if data, err := os.ReadFile(p); err == nil && len(data) > 0 {
				var cfg struct {
					Password string `json:"password"`
				}
				if json.Unmarshal(data, &cfg) == nil && cfg.Password != "" {
					pwd = cfg.Password
					zenDaemonPassword = pwd
					break
				}
			}
		}
	}

	if pwd == "" {
		zenDaemonAvailable = false
		return false
	}

	client := &http.Client{Timeout: 1 * time.Second}
	req, err := http.NewRequest("GET", openCodeDaemonBaseURL+"/api/info", nil)
	if err != nil {
		zenDaemonAvailable = false
		return false
	}
	authHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte("opencode:"+pwd))
	req.Header.Set("Authorization", authHeader)

	resp, err := client.Do(req)
	if err != nil {
		zenDaemonAvailable = false
		return false
	}
	defer resp.Body.Close()

	zenDaemonAvailable = (resp.StatusCode == http.StatusOK)
	return zenDaemonAvailable
}

// formatDaemonPrompt renders OpenAI messages and tools into a single prompt for OpenCode daemon generation.
func formatDaemonPrompt(params map[string]any) string {
	var sb strings.Builder

	// 1. Tool declarations
	if rawTools, ok := params["tools"].([]any); ok && len(rawTools) > 0 {
		sb.WriteString("[Available Tools]\n")
		sb.WriteString("You have access to the following tools:\n")
		for _, t := range rawTools {
			tm, ok := t.(map[string]any)
			if !ok {
				continue
			}
			fn, _ := tm["function"].(map[string]any)
			if fn == nil {
				continue
			}
			name, _ := fn["name"].(string)
			desc, _ := fn["description"].(string)
			paramsJSON, _ := json.Marshal(fn["parameters"])
			sb.WriteString(fmt.Sprintf("- Function: %s\n  Description: %s\n  Parameters: %s\n\n", name, desc, string(paramsJSON)))
		}
		sb.WriteString("TOOL CALLING RULES:\n")
		sb.WriteString("1. If you need to call a tool, you MUST respond ONLY with the XML format below and no extraneous text:\n")
		sb.WriteString("<tool_call>\n{\"name\": \"FUNCTION_NAME\", \"arguments\": {\"ARG_NAME\": \"VALUE\"}}\n</tool_call>\n")
		sb.WriteString("2. Do not wrap the <tool_call> block inside markdown code blocks (```).\n\n")
	}

	// 2. Conversation messages
	if msgs, ok := params["messages"].([]any); ok {
		for _, m := range msgs {
			mm, ok := m.(map[string]any)
			if !ok {
				continue
			}
			role, _ := mm["role"].(string)
			content := formatMessageContent(mm["content"])

			switch role {
			case "system":
				sb.WriteString("[System]\n")
				sb.WriteString(content)
				sb.WriteString("\n\n")
			case "user":
				sb.WriteString("[User]\n")
				sb.WriteString(content)
				sb.WriteString("\n\n")
			case "assistant":
				sb.WriteString("[Assistant]\n")
				if content != "" {
					sb.WriteString(content)
					sb.WriteString("\n")
				}
				if tcs, ok := mm["tool_calls"].([]any); ok {
					for _, tc := range tcs {
						tcm, _ := tc.(map[string]any)
						if tcm == nil {
							continue
						}
						fn, _ := tcm["function"].(map[string]any)
						name, _ := fn["name"].(string)
						args, _ := fn["arguments"].(string)
						sb.WriteString(fmt.Sprintf("<tool_call>\n{\"name\": %q, \"arguments\": %s}\n</tool_call>\n", name, args))
					}
				}
				sb.WriteString("\n")
			case "tool":
				callID, _ := mm["tool_call_id"].(string)
				if callID != "" {
					sb.WriteString(fmt.Sprintf("[Tool Result for %s]\n", callID))
				} else {
					sb.WriteString("[Tool Result]\n")
				}
				sb.WriteString(content)
				sb.WriteString("\n\n")
			default:
				sb.WriteString(fmt.Sprintf("[%s]\n", strings.Title(role)))
				sb.WriteString(content)
				sb.WriteString("\n\n")
			}
		}
	}

	sb.WriteString("[Assistant]\n")
	return sb.String()
}

func formatMessageContent(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			if im, ok := item.(map[string]any); ok {
				if t, _ := im["type"].(string); t == "text" {
					if txt, _ := im["text"].(string); txt != "" {
						parts = append(parts, txt)
					}
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		if b, err := json.Marshal(content); err == nil {
			return string(b)
		}
		return fmt.Sprint(content)
	}
}

// callZenViaLocalDaemon executes inference via the running OpenCode service daemon on 127.0.0.1:49374.
// It returns an *http.Response delivering standard OpenAI SSE chunks matching what proxy.go expects.
func callZenViaLocalDaemon(ctx context.Context, params map[string]any, stream bool, opts ...zenCallOpts) (*http.Response, int, error) {
	pwd := getOpenCodeServicePassword()
	if pwd == "" {
		return nil, 0, fmt.Errorf("local opencode service password not found")
	}

	modelRaw, _ := params["model"].(string)
	modelID := strings.TrimPrefix(modelRaw, "opencode/")
	if zm, ok := resolveZenModel(modelID); ok && zm != nil {
		modelID = zm.ID
	}

	authHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte("opencode:"+pwd))
	client := &http.Client{Timeout: 90 * time.Second}

	// 1. Create a session on the local daemon
	reqCreate, err := http.NewRequestWithContext(ctx, "POST", openCodeDaemonBaseURL+"/api/session", bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, 0, fmt.Errorf("create session request: %w", err)
	}
	reqCreate.Header.Set("Authorization", authHeader)
	reqCreate.Header.Set("Content-Type", "application/json")

	respCreate, err := client.Do(reqCreate)
	if err != nil {
		return nil, 0, fmt.Errorf("create opencode daemon session: %w", err)
	}
	defer respCreate.Body.Close()

	if respCreate.StatusCode != http.StatusOK {
		body := kit.ReadBody(respCreate)
		return nil, 0, fmt.Errorf("opencode daemon session create failed (%d): %s", respCreate.StatusCode, body)
	}

	var sessionObj struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(respCreate.Body).Decode(&sessionObj); err != nil || sessionObj.Data.ID == "" {
		return nil, 0, fmt.Errorf("parse opencode daemon session ID: %w", err)
	}
	sid := sessionObj.Data.ID

	// Defer session cleanup
	defer func(sessionID string) {
		go func() {
			delCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			delReq, err := http.NewRequestWithContext(delCtx, "DELETE", openCodeDaemonBaseURL+"/api/session/"+sessionID, nil)
			if err == nil {
				delReq.Header.Set("Authorization", authHeader)
				if resp, err := client.Do(delReq); err == nil {
					_ = resp.Body.Close()
				}
			}
		}()
	}(sid)

	// 2. Patch model for session
	patchBody, _ := json.Marshal(map[string]any{
		"model": map[string]string{
			"id":         modelID,
			"providerID": "opencode",
		},
	})
	reqPatch, err := http.NewRequestWithContext(ctx, "PATCH", openCodeDaemonBaseURL+"/api/session/"+sid, bytes.NewReader(patchBody))
	if err != nil {
		return nil, 0, fmt.Errorf("patch session model request: %w", err)
	}
	reqPatch.Header.Set("Authorization", authHeader)
	reqPatch.Header.Set("Content-Type", "application/json")

	respPatch, err := client.Do(reqPatch)
	if err != nil {
		return nil, 0, fmt.Errorf("patch opencode daemon model: %w", err)
	}
	_ = respPatch.Body.Close()

	// 3. Generate text from daemon
	promptText := formatDaemonPrompt(params)
	genBody, _ := json.Marshal(map[string]string{
		"prompt": promptText,
	})

	reqGen, err := http.NewRequestWithContext(ctx, "POST", openCodeDaemonBaseURL+"/api/session/"+sid+"/generate", bytes.NewReader(genBody))
	if err != nil {
		return nil, 0, fmt.Errorf("generate text request: %w", err)
	}
	reqGen.Header.Set("Authorization", authHeader)
	reqGen.Header.Set("Content-Type", "application/json")

	log.Printf("  zen daemon bridge: model=%s session=%s prompt_len=%d", modelID, sid, len(promptText))

	respGen, err := client.Do(reqGen)
	if err != nil {
		return nil, 0, fmt.Errorf("generate text from daemon: %w", err)
	}
	defer respGen.Body.Close()

	if respGen.StatusCode != http.StatusOK {
		body := kit.ReadBody(respGen)
		return nil, 0, fmt.Errorf("opencode daemon generate failed (%d): %s", respGen.StatusCode, body)
	}

	var genResult struct {
		Data struct {
			Text string `json:"text"`
		} `json:"data"`
	}
	if err := json.NewDecoder(respGen.Body).Decode(&genResult); err != nil {
		return nil, 0, fmt.Errorf("decode opencode daemon generate response: %w", err)
	}
	rawText := genResult.Data.Text

	// 4. Construct OpenAI chat completion response map
	promptTokens := estimateJSON(params)
	completionTokens := estimateText(rawText)
	now := time.Now().Unix()
	chatcmplID := fmt.Sprintf("chatcmpl-daemon-%x", time.Now().UnixNano())

	chat := map[string]any{
		"id":      chatcmplID,
		"object":  "chat.completion",
		"created": now,
		"model":   modelID,
		"choices": []any{
			map[string]any{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": rawText,
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      promptTokens + completionTokens,
		},
	}

	// Parse and extract XML/JSON tool calls if any
	repairXMLToolCalls(chat)

	// 5. Serialize into standard OpenAI SSE chunks
	msg, _ := getNested(chat, "choices", 0, "message").(map[string]any)
	if msg == nil {
		msg = map[string]any{}
	}
	finishReason, _ := getNested(chat, "choices", 0, "finish_reason").(string)
	if finishReason == "" {
		finishReason = "stop"
	}
	cleanedContent, _ := msg["content"].(string)

	var sseBuf bytes.Buffer

	emitChunk := func(delta map[string]any, fr any) {
		chunkObj := map[string]any{
			"id":      chatcmplID,
			"object":  "chat.completion.chunk",
			"created": now,
			"model":   modelID,
			"choices": []any{
				map[string]any{
					"index":         0,
					"delta":         delta,
					"finish_reason": fr,
				},
			},
		}
		data, _ := json.Marshal(chunkObj)
		sseBuf.WriteString("data: ")
		sseBuf.Write(data)
		sseBuf.WriteString("\n\n")
	}

	// Initial role chunk
	emitChunk(map[string]any{"role": "assistant"}, nil)

	// Content chunk(s)
	if cleanedContent != "" {
		emitChunk(map[string]any{"content": cleanedContent}, nil)
	}

	// Tool calls chunk(s)
	if tcs, ok := msg["tool_calls"].([]any); ok && len(tcs) > 0 {
		for i, tc := range tcs {
			tcm, _ := tc.(map[string]any)
			if tcm == nil {
				continue
			}
			fn, _ := tcm["function"].(map[string]any)
			name, _ := fn["name"].(string)
			args, _ := fn["arguments"].(string)
			id, _ := tcm["id"].(string)

			emitChunk(map[string]any{
				"tool_calls": []any{
					map[string]any{
						"index": i,
						"id":    id,
						"type":  "function",
						"function": map[string]any{
							"name":      name,
							"arguments": args,
						},
					},
				},
			}, nil)
		}
	}

	// Final finish chunk
	emitChunk(map[string]any{}, finishReason)

	// Usage chunk
	usageObj := map[string]any{
		"id":      chatcmplID,
		"object":  "chat.completion.chunk",
		"created": now,
		"model":   modelID,
		"usage":   chat["usage"],
	}
	usageData, _ := json.Marshal(usageObj)
	sseBuf.WriteString("data: ")
	sseBuf.Write(usageData)
	sseBuf.WriteString("\n\n")

	// Done marker
	sseBuf.WriteString("data: [DONE]\n\n")

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
		},
		Body:          io.NopCloser(bytes.NewReader(sseBuf.Bytes())),
		ContentLength: int64(sseBuf.Len()),
	}

	return resp, 0, nil
}
