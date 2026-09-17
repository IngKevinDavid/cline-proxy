package app

import (
	"bufio"
	"bytes"
	"cline-go-proxy/internal/cline"
	"cline-go-proxy/internal/kit"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"
)

var defaultModel = "deepseek/deepseek-v4-flash"

var proxyListenAddress = "0.0.0.0:3457"

const (
	defaultMaxTokens       = 128000
	defaultReasoningEffort = "high"
)

var passThroughKeys = []string{
	"tools", "tool_choice", "parallel_tool_calls", "functions", "function_call",
	"temperature", "top_p", "top_k", "stop", "presence_penalty", "frequency_penalty",
	"response_format", "user", "n", "logit_bias", "seed", "logprobs", "top_logprobs",
	"stream_options", "metadata",
}

type chatRequest struct {
	Model               string          `json:"model"`
	Messages            json.RawMessage `json:"messages"`
	Stream              bool            `json:"stream,omitempty"`
	MaxTokens           int             `json:"max_tokens,omitempty"`
	MaxCompletionTokens int             `json:"max_completion_tokens,omitempty"`
	Tools               json.RawMessage `json:"tools,omitempty"`
	ToolChoice          json.RawMessage `json:"tool_choice,omitempty"`
	ReasoningEffort     string          `json:"reasoning_effort,omitempty"`
	ReasoningEffortAlt  string          `json:"reasoningEffort,omitempty"`
	Extra               map[string]any  `json:"-"`
}

func StartProxy(host string, port int) error {
	if strings.TrimSpace(host) == "" {
		host = "0.0.0.0"
	}
	ApplyEnvConfig()

	// 公网暴露 fail-closed 检查：非回环监听必须有 API_KEY 与 ADMIN_PASSWORD
	if err := CheckPublicExposure(host); err != nil {
		return err
	}

	initLogFile()

	// 池为空时从种子文件导入cline账号（容器无状态重建场景）
	seedAccountsFromFile()

	p := loadPool()
	activeCount := 0
	for _, a := range p.Accounts {
		if a.Status == "active" {
			// 预热 token：APIToken 账号 token 恒有效（ensureAccountToken 直接返回），
			// OAuth 账号仅在缺失/过期时刷新
			if a.APIToken == "" && (a.AccessToken == "" || time.Now().UnixMilli() >= a.ExpiresAt) {
				if err := refreshAccountToken(a); err != nil {
					log.Printf("  Pre-warm failed for %s: %v", a.Email, err)
					continue
				}
			}
			activeCount++
		}
	}
	log.Printf("Loaded %d active accounts from pool", activeCount)

	freePort(port)

	startModelsRefresher()
	startZenModelsRefresher()
	startPoolFlusher()
	loadZenEndpoints()
	startZenHarvester()
	initStats()
	LoadRequestLogsFromFile()
	go cleanupCompactStates()

	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/admin/", http.StatusFound)
	})

	mux.HandleFunc("/v1/health", corsHandler(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":         "ok",
			"version":        "go-1.1",
			"activeAccounts": liveActiveAccountCount(),
		})
	}))
	mux.HandleFunc("/health", corsHandler(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":         "ok",
			"version":        "go-1.1",
			"activeAccounts": liveActiveAccountCount(),
		})
	}))

	// Admin API (frontend + REST)
	registerAdminRoutes(mux)

	apiKeyHandler := func(next http.HandlerFunc) http.HandlerFunc {
		return corsHandler(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get("x-api-key")
			if key == "" {
				if b := r.Header.Get("Authorization"); len(b) > 7 && b[:7] == "Bearer " {
					key = b[7:]
				}
			}

			// API_KEY 环境变量优先：设置后它是 /v1 唯一有效凭证（无状态部署模式）
			if envKey := APIKeyEnv(); envKey != "" {
				if subtle.ConstantTimeCompare([]byte(key), []byte(envKey)) != 1 {
					writeJSON(w, http.StatusUnauthorized, map[string]any{
						"error": map[string]string{
							"message": "invalid API key (server requires the API_KEY env value)",
							"type":    "auth_error",
						},
					})
					return
				}
				next(w, r)
				return
			}

			// 未设置 API_KEY：沿用管理面板生成的动态 key 列表；列表为空则放行（本地模式）
			keys := poolKeysSnapshot()
			if len(keys) == 0 {
				next(w, r)
				return
			}

			valid := false
			for _, k := range keys {
				if subtle.ConstantTimeCompare([]byte(key), []byte(k)) == 1 {
					valid = true
					break
				}
			}

			if !valid {
				writeJSON(w, http.StatusUnauthorized, map[string]any{
					"error": map[string]string{
						"message": "invalid API key. Generate one at /admin/ or set x-api-key header",
						"type":    "auth_error",
					},
				})
				return
			}
			next(w, r)
		})
	}

	modelsHandler := apiKeyHandler(func(w http.ResponseWriter, r *http.Request) {
		ensureModelsFresh()
		data := apiModelList()
		// 合并 zen 免费模型
		cfg := getZenConfig()
		if cfg.Enabled {
			for _, zm := range zenModelList() {
				data = append(data, map[string]any{
					"id":       zm["id"],
					"object":   "model",
					"created":  time.Now().UnixMilli(),
					"owned_by": "opencode-zen",
					"source":   "zen-free",
					"status":   "active",
					"cost":     "free",
					"context":  zm["context"],
					"output":   zm["output"],
				})
			}
		}
		// combo 别名模型（仪表盘自定义的虚拟模型 ID）
		for _, c := range listCombos() {
			owned := "cline"
			if c.Platform == "zen" {
				owned = "opencode-zen"
			}
			data = append(data, map[string]any{
				"id":       c.ID,
				"object":   "model",
				"created":  c.CreatedAt.UnixMilli(),
				"owned_by": owned,
				"source":   "combo",
				"status":   "active",
				"cost":     "free",
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
	})
	mux.HandleFunc("/v1/models", modelsHandler)
	mux.HandleFunc("/models", modelsHandler)

	chatHandler := apiKeyHandler(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": map[string]string{"message": err.Error(), "type": "parse_error"},
			})
			return
		}

		var params map[string]any
		if err := json.Unmarshal(body, &params); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": map[string]string{"message": err.Error(), "type": "parse_error"},
			})
			return
		}

		isStream, _ := params["stream"].(bool)
		toolCount := 0
		if tools, ok := params["tools"]; ok {
			if t, ok := tools.([]any); ok {
				toolCount = len(t)
			}
		}
		model, _ := params["model"].(string)
		log.Printf("  client: stream=%v tools=%d model=%s", isStream, toolCount, model)

		// combo 别名模型：改写为平台上游真实模型后按平台路由;
		// combo 可声明 useProxies 让该别名走出口代理池
		useProxies := clineProxiesEnabled()
		if c := resolveCombo(model); c != nil {
			log.Printf("  combo %q -> %s model %q (useProxies=%v)", model, c.Platform, c.Target, c.UseProxies)
			params["model"] = c.Target
			model = c.Target
			if c.UseProxies {
				useProxies = true
			}
		}

		// Override system prompt from override.md for OpenAI format
		applyOverride(params)

		// zen 免费模型路由
		if route := routeModel(model); route == "zen" {
			handleZenChat(w, r, params)
			return
		} else if route == "reject" {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": map[string]string{"message": fmt.Sprintf("model %q is a paid zen model; only free zen models are proxied", model), "type": "invalid_request_error"},
			})
			return
		}

		// 走到这说明是 cline 路由：未知模型名显式拒绝，而不是静默替换成默认模型
		if msg := strictModelGate(model); msg != "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": map[string]string{"message": msg, "type": "invalid_request_error"},
			})
			return
		}

		upstreamStream := isStream
		if !isStream {
			model := getDefaultModel()
			if m, ok := params["model"].(string); ok && m != "" {
				model = normalizeRequestModel(m)
			}
			if modelNeedsStream(model) {
				upstreamStream = true
				log.Printf("  model %s requires stream: forcing upstream stream, will aggregate", model)
			}
		}

		// cline 路由需要账号池；zen 免费模型不需要（纯 ZEN_KEYS 部署也能用），
		// 因此该检查放在路由之后而不是函数开头
		if poolAccountCount() == 0 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"error": map[string]string{
					"message": "No accounts in pool. Run with --add-account or add via /admin/.",
					"type":    "auth_error",
				},
			})
			return
		}

		resp, acc, err := callClineAPI(r.Context(), params, upstreamStream, useProxies)
		if err != nil {
			log.Printf("  api error: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": map[string]string{"message": err.Error(), "type": "api_error"},
			})
			return
		}
		defer resp.Body.Close()

		usageFn := accountUsageFn(acc, params)

		if isStream {
			handleStreamResponseWithUsage(w, resp, usageFn)
			return
		}

		if upstreamStream {
			out, err := collectStreamResponse(resp)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{
					"error": map[string]string{"message": err.Error(), "type": "parse_error"},
				})
				return
			}
			if u, ok := out["usage"].(map[string]any); ok && len(u) > 0 {
				usageFn(u)
			}
			out = normalizeOpenAIResponse(out)
			truncateAtStopSequences(out, stopSequencesFrom(params))
			contentStr, _ := getNested(out, "choices", 0, "message", "content").(string)
			finishReason := getNested(out, "choices", 0, "finish_reason")
			log.Printf("  nonstream (aggregated): model=%v content_len=%d finish=%v",
				out["model"], len(contentStr), finishReason)
			writeJSON(w, http.StatusOK, out)
			return
		}

		handleNonStreamResponseWithUsage(w, resp, usageFn, stopSequencesFrom(params))
	})
	mux.HandleFunc("/v1/chat/completions", chatHandler)
	mux.HandleFunc("/chat/completions", chatHandler)

	// Anthropic Messages API support
	anthropicHandler := apiKeyHandler(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		handleAnthropicMessages(w, r)
	})
	mux.HandleFunc("/v1/messages", anthropicHandler)
	mux.HandleFunc("/messages", anthropicHandler)

	// OpenAI Responses API
	responsesHandler := apiKeyHandler(handleResponses)
	mux.HandleFunc("/v1/responses", responsesHandler)
	mux.HandleFunc("/responses", responsesHandler)

	addr := fmt.Sprintf("%s:%d", host, port)
	proxyListenAddress = addr
	server := &http.Server{
		Addr: addr,
		// 公网暴露必备：慢速头攻击（slowloris）防护与空闲连接回收。
		// 不设全局 ReadTimeout/WriteTimeout —— SSE 流式响应是长连接，
		// 超时由请求 ctx（客户端断开即取消）和请求体上限控制。
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
		Handler:           requestLogMiddleware(mux),
	}

	fmt.Println("")
	fmt.Println(strings.Repeat("=", 58))
	fmt.Println("  Cline Go Proxy v1.0 - No CLI Required")
	fmt.Println(strings.Repeat("=", 58))
	fmt.Printf("  http://%s\n", addr)
	fmt.Printf("  http://%s/v1\n", addr)
	fmt.Println("  API Key: any value")
	fmt.Printf("  Model:   %s (auto-detected)\n", getDefaultModel())
	fmt.Printf("  Accounts: %d total, %d active\n", len(loadPool().Accounts), activeCount)
	fmt.Println(strings.Repeat("=", 58))

	// 优雅停机：docker stop 发 SIGTERM，等待在途请求（含 SSE 流）最多
	// 10s 后退出，而不是直接掐断
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()
	select {
	case err := <-errCh:
		return err
	case <-stop:
		log.Printf("  shutdown signal received, draining connections...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		flushPoolNow()
		log.Printf("  shutdown complete")
		return nil
	}
}

// initLogFile 将日志同时输出到控制台与 cline-proxy.log（追加模式），
// 控制台窗口滚动内容有限，文件可完整保留所有日志。
func initLogFile() {
	path := kit.ResolveDataPath("cline-proxy.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Printf("  open log file failed: %v", err)
		return
	}
	log.SetOutput(io.MultiWriter(os.Stderr, f))
	log.Printf("========== proxy started, log file: %s ==========", path)
}

func corsHandler(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, x-api-key, anthropic-version, anthropic-beta")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		h(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// applyOverride 用 override.md 替换系统提示词。默认关闭：需设置
// APPLY_SYSTEM_PROMPT_OVERRIDE=true 才启用（编码 IDE / Agent 保留自己的提示词）。
func applyOverride(params map[string]any) {
	if !SystemPromptOverrideEnabled() {
		return
	}
	override := loadOverrideContent()
	if override == "" {
		return
	}
	if msgs, ok := params["messages"].([]any); ok {
		found := false
		for _, m := range msgs {
			if mm, ok := m.(map[string]any); ok {
				if mm["role"] == "system" {
					mm["content"] = override
					found = true
					break
				}
			}
		}
		if !found {
			params["messages"] = append([]any{map[string]any{"role": "system", "content": override}}, msgs...)
		}
	}
}

// handleZenChat opencode zen 免费模型分支: 压缩 -> 上游 -> 透传,并记录统计
func handleZenChat(w http.ResponseWriter, r *http.Request, params map[string]any) {
	cfg := getZenConfig()
	if !cfg.Enabled {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": map[string]string{"message": "zen upstream disabled in /admin/ settings", "type": "api_error"},
		})
		return
	}
	model, _ := params["model"].(string)
	zm, ok := resolveZenFreeModel(model)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"message": fmt.Sprintf("model %q is not a free zen model", model), "type": "invalid_request_error"},
		})
		return
	}
	isStream, _ := params["stream"].(bool)
	tracker := newZenStatsTracker(zenStatsRecord{
		TS:           time.Now().UnixMilli(),
		Upstream:     "zen",
		Model:        zm.ID,
		Stream:       isStream,
		PromptTokens: estimateJSON(params),
	})

	sid := requestSessionID(params, r.Header)
	out := maybeCompact(params, zm, sid)
	tracker.rec.Compacted = out.changed
	tracker.rec.CompactionTokens = out.compactTokens
	if out.changed {
		log.Printf("  zen: %s", out.note)
	}

	// 端点自适应：Upstream=="" 的模型先走 chat/completions；若上游报
	// "wrong-endpoint"（500/400 系列 + 端点错误特征），学习为 responses 并
	// 本次直接改走原生 responses 路径重试。muse-spark 等已知 responses 模型
	// 仍由 Upstream 字段直达，无额外探测开销。
	// （trade-off 记录：曾考虑按目录 provider.npm 推断端点，但 29 个免费模型
	// 中 23 个无 npm 字段、无规律可循——运行时探测是唯一可靠信号。）
	if zm.Upstream == "responses" {
		handleZenResponsesNative(w, r, params, zm, tracker)
		return
	}

	resp, rateLimited, err := callZenAPI(r.Context(), params, isStream)
	if err != nil && isWrongEndpoint(err) {
		learnZenEndpoint(zm.ID, "responses")
		log.Printf("  zen endpoint auto-learn: model=%s chat/completions rejected (%v), retrying responses",
			zm.ID, kit.Truncate(err.Error(), 120))
		handleZenResponsesNative(w, r, params, zm, tracker)
		return
	}
	if err != nil {
		log.Printf("  zen api error: %v", err)
		tracker.rec.RateLimited = rateLimited
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": map[string]string{"message": err.Error(), "type": "api_error"},
		})
		tracker.finish(false, http.StatusBadGateway)
		return
	}
	tracker.rec.RateLimited = rateLimited
	defer resp.Body.Close()
	tracker.rec.Status = resp.StatusCode

	usageFn := func(u map[string]any) {
		if pt, ok := u["prompt_tokens"].(float64); ok {
			tracker.rec.CompletionTokens += int(pt) - tracker.rec.PromptTokens
			if tracker.rec.CompletionTokens < 0 {
				tracker.rec.CompletionTokens = 0
			}
		}
		if ct, ok := u["completion_tokens"].(float64); ok {
			tracker.rec.CompletionTokens = int(ct)
		}
	}

	if isStream {
		handleStreamResponseWithUsage(w, resp, usageFn)
		tracker.finish(true, resp.StatusCode)
		return
	}
	handleNonStreamResponseWithUsage(w, resp, usageFn, nil)
	tracker.finish(true, resp.StatusCode)
}

// handleZenChatDirect chat/completions 直调（端点自适应的反向纠错口：
// responses 路径上报"该用 chat"时，学回 chat 后经此重试，避免递归回主入口）。
func handleZenChatDirect(w http.ResponseWriter, r *http.Request, params map[string]any, zm *ZenModel, tracker *zenStatsTracker) {
	isStream, _ := params["stream"].(bool)
	resp, rateLimited, err := callZenAPI(r.Context(), params, isStream)
	if err != nil {
		log.Printf("  zen api error: %v", err)
		tracker.rec.RateLimited = rateLimited
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": map[string]string{"message": err.Error(), "type": "api_error"},
		})
		tracker.finish(false, http.StatusBadGateway)
		return
	}
	tracker.rec.RateLimited = rateLimited
	defer resp.Body.Close()
	tracker.rec.Status = resp.StatusCode
	usageFn := func(u map[string]any) {
		if ct, ok := u["completion_tokens"].(float64); ok {
			tracker.rec.CompletionTokens = int(ct)
		}
	}
	if isStream {
		handleStreamResponseWithUsage(w, resp, usageFn)
		tracker.finish(true, resp.StatusCode)
		return
	}
	handleNonStreamResponseWithUsage(w, resp, usageFn, nil)
	tracker.finish(true, resp.StatusCode)
}

// handleZenResponsesNative 原生 responses 端点模型（Upstream=="responses"）的
// chat 入口：上游走 callZenResponsesAPI，流式时把原生 SSE 转成 chat SSE
// 即时下发，非流式时把聚合好的 chat 直接呈现。统计/日志语义与 chat 路径一致。
func handleZenResponsesNative(w http.ResponseWriter, r *http.Request, params map[string]any, zm *ZenModel, tracker *zenStatsTracker) {
	usageFn := func(u map[string]any) {
		if pt, ok := u["prompt_tokens"].(float64); ok {
			tracker.rec.CompletionTokens += int(pt) - tracker.rec.PromptTokens
			if tracker.rec.CompletionTokens < 0 {
				tracker.rec.CompletionTokens = 0
			}
		}
		if ct, ok := u["completion_tokens"].(float64); ok {
			tracker.rec.CompletionTokens = int(ct)
		}
	}
	clientStream, _ := params["stream"].(bool)
	// spark 等原生 responses 模型只接受 stream=true（官方 CLI 恒发 true；
	// stream=false（即使显式传）上游按"非 CLI"请求 403）。网关非流式请求
	// 在上游侧强制 stream=true 再聚合（与 cline 路由 force-stream 聚合约
	// 定一致），客户端仍按非流式接收。
	resp, rateLimited, err := callZenResponsesAPI(r.Context(), params, true)
	if err != nil {
		// 反向纠错：若 responses 路径上报"该用 chat"（未来反向模型），
		// 学回 chat 并改走 chat 路径重试一次。
		if isWrongEndpointResponses(err) {
			learnZenEndpoint(zm.ID, "")
			log.Printf("  zen endpoint auto-learn: model=%s responses rejected (%v), retrying chat",
				zm.ID, kit.Truncate(err.Error(), 120))
			handleZenChatDirect(w, r, params, zm, tracker)
			return
		}
		log.Printf("  zen responses api error: %v", err)
		tracker.rec.RateLimited = rateLimited
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": map[string]string{"message": err.Error(), "type": "api_error"},
		})
		tracker.finish(false, http.StatusBadGateway)
		return
	}
	tracker.rec.RateLimited = rateLimited
	defer resp.Body.Close()
	tracker.rec.Status = resp.StatusCode

	if clientStream {
		// 原生 responses SSE 需要先转成 chat SSE 再下发：
		// 先聚合（保持 tool_calls/reasoning/usage 完整），再以 chat
		// chunk 形态逐块发出（长文本仍保持流式体感）
		log.Printf("  zen responses upstream stream: aggregating then re-emitting as chat SSE")
		chat, aerr := responsesSSEToChat(resp)
		if aerr != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"error": map[string]string{"message": aerr.Error(), "type": "api_error"},
			})
			tracker.finish(false, http.StatusBadGateway)
			return
		}
		chat["model"] = zm.ID
		emitChatAsSSE(w, chat, usageFn)
		tracker.finish(true, resp.StatusCode)
		return
	}
	// 非流式客户端：上游已强制 stream=true，resp.Body 是原生 responses SSE，
	// 先聚合成 chat 再按非流式呈现。
	chat, aerr := responsesSSEToChat(resp)
	if aerr != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": map[string]string{"message": aerr.Error(), "type": "api_error"},
		})
		tracker.finish(false, http.StatusBadGateway)
		return
	}
	chat["model"] = zm.ID
	data, merr := json.Marshal(chat)
	if merr != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": map[string]string{"message": merr.Error(), "type": "api_error"},
		})
		tracker.finish(false, http.StatusBadGateway)
		return
	}
	handleNonStreamResponseWithUsage(w, &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(data)),
	}, usageFn, nil)
	tracker.finish(true, http.StatusOK)
}

// emitChatAsSSE 把聚合好的 chat completions 按标准 chat SSE chunk 下发：
// role 首块 + 内容分片（每 ~2KB 一块）+ tool_calls（如有）+ usage 尾块 + [DONE]。
func emitChatAsSSE(w http.ResponseWriter, chat map[string]any, usageFn func(map[string]any)) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}
	emit := func(obj map[string]any) {
		data, _ := json.Marshal(obj)
		w.Write(append(append([]byte("data: "), data...), '\n', '\n'))
		flush()
	}
	model, _ := chat["model"].(string)
	msg, _ := getNested(chat, "choices", 0, "message").(map[string]any)
	if msg == nil {
		msg = map[string]any{}
	}
	finish, _ := getNested(chat, "choices", 0, "finish_reason").(string)
	chunk := func(delta map[string]any, fr any) {
		emit(map[string]any{
			"id":      fmt.Sprintf("chatcmpl-%x", time.Now().UnixNano()),
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   model,
			"choices": []any{map[string]any{
				"index":         0,
				"delta":         delta,
				"finish_reason": fr,
			}},
		})
	}
	chunk(map[string]any{"role": "assistant"}, nil)
	content, _ := msg["content"].(string)
	for len(content) > 0 {
		n := 2048
		if len(content) < n {
			n = len(content)
		}
		chunk(map[string]any{"content": content[:n]}, nil)
		content = content[n:]
	}
	if tcs, ok := msg["tool_calls"].([]any); ok && len(tcs) > 0 {
		for i, tc := range tcs {
			tcm, _ := tc.(map[string]any)
			if tcm == nil {
				continue
			}
			fn, _ := tcm["function"].(map[string]any)
			if fn == nil {
				fn = map[string]any{}
			}
			id, _ := tcm["id"].(string)
			name, _ := fn["name"].(string)
			args, _ := fn["arguments"].(string)
			chunk(map[string]any{"tool_calls": []any{map[string]any{
				"index": i,
				"id":    id,
				"type":  "function",
				"function": map[string]any{
					"name":      name,
					"arguments": args,
				},
			}}}, nil)
		}
	}
	if u, ok := chat["usage"].(map[string]any); ok && len(u) > 0 {
		if usageFn != nil {
			usageFn(u)
		}
		chunk(map[string]any{}, nil)
		last := map[string]any{
			"id":      fmt.Sprintf("chatcmpl-%x", time.Now().UnixNano()),
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   model,
			"choices": []any{map[string]any{
				"index":         0,
				"delta":         map[string]any{},
				"finish_reason": finish,
			}},
			"usage": u,
		}
		emit(last)
	} else {
		chunk(map[string]any{}, finish)
	}
	w.Write([]byte("data: [DONE]\n\n"))
	flush()
	log.Printf("  zen responses re-emitted as chat SSE: finish=%s", finish)
}

func cleanMessages(messages []any) []any {
	cleaned := make([]any, 0, len(messages))
	for _, m := range messages {
		msg, ok := m.(map[string]any)
		if !ok {
			cleaned = append(cleaned, m)
			continue
		}
		cleaned = append(cleaned, msg)
	}
	return cleaned
}

// asInt 把数值型 any 转为 int（客户端 JSON 解码得到 float64，
// anthropic/responses 内部转换路径写入 int/int64）。
func asInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}

// liveActiveAccountCount /health 的实时活跃账号数（启动时算一次会让
// 健康检查永远显示旧值）。
func liveActiveAccountCount() int {
	_, active := poolStatusSnapshot()
	return active
}

func buildUpstreamBody(params map[string]any, stream bool) map[string]any {
	sessionID := "sess_" + kit.RandHex(8)

	maxTokens := defaultMaxTokens
	// max_tokens 可能是 float64（客户端 JSON 解码）或 int（anthropic/responses
	// 内部转换路径写入）—— 两种都要接受，否则转换路径永远回退到 128000
	if mt := asInt(params["max_tokens"]); mt > 0 {
		maxTokens = mt
	} else if mt := asInt(params["max_completion_tokens"]); mt > 0 {
		maxTokens = mt
	}

	model := getDefaultModel()
	if m, ok := params["model"].(string); ok && m != "" {
		model = normalizeRequestModel(m)
	}

	body := map[string]any{
		"model":            model,
		"max_tokens":       maxTokens,
		"session_id":       sessionID,
		"reasoning_effort": defaultReasoningEffort,
	}

	if msgsRaw, ok := params["messages"]; ok {
		if msgsArr, ok := msgsRaw.([]any); ok {
			body["messages"] = cleanMessages(msgsArr)
		} else {
			body["messages"] = msgsRaw
		}
	}

	if stream {
		body["stream"] = true
	}

	if re, ok := params["reasoning_effort"].(string); ok && re != "" {
		body["reasoning_effort"] = re
	} else if re, ok := params["reasoningEffort"].(string); ok && re != "" {
		body["reasoning_effort"] = re
	}

	for _, key := range passThroughKeys {
		if val, ok := params[key]; ok {
			body[key] = val
		}
	}

	enforceToolChoiceNone(body)

	return body
}

// enforceToolChoiceNone 保证 tool_choice:"none" 的语义。部分上游模型
// （尤其 zen 免费档）会忽略该指令，仍然输出 tool_call —— 而客户端
// （IDE）发出 none 时明确表示这一轮不要工具调用。最可靠的做法是
// 网关侧直接把 tools 从上游请求中移除：模型看不到工具，只能文本回答。
func enforceToolChoiceNone(body map[string]any) {
	if tc, ok := body["tool_choice"].(string); ok && tc == "none" {
		delete(body, "tools")
		delete(body, "tool_choice")
	}
}

func clineHeaders(token, sessionID string) http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	h.Set("Content-Type", "application/json")
	h.Set("X-Task-ID", sessionID)
	// Go 默认 UA（Go-http-client/1.1）是指纹异常点，容易被上游风控拦截；
	// 与管理面板探测请求保持一致的客户端标识。cfg.Headers 里可覆盖。
	h.Set("User-Agent", "Cline/3.0.50")

	cfg := getProxyConfig()
	for k, v := range cfg.Headers {
		h.Set(k, v)
	}

	return h
}

// callClineAPI 调用 cline 上游。
// ctx 来自客户端请求: IDE abort/取消时立即终止,不冷却账号。
// useProxies 为 true 时走出口代理池（每次尝试 round-robin 挑选）;
// 代理路径上的网络错误只冷却代理本身,绝不冷却账号 —— 代理故障不污染账号池。
func callClineAPI(ctx context.Context, params map[string]any, stream bool, useProxies bool) (*http.Response, *Account, error) {
	acc := pickAccount()
	if acc == nil {
		return nil, nil, fmt.Errorf("no active accounts available: %s", describePoolStatus())
	}

	token, err := ensureAccountToken(acc)
	if err != nil {
		// Try other accounts
		return nil, nil, fmt.Errorf("account %s token failed: %w", acc.Email, err)
	}

	body := buildUpstreamBody(params, stream)
	sessionID, _ := body["session_id"].(string)

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return nil, acc, fmt.Errorf("marshal body: %w", err)
	}

	toolCount := 0
	if tools, ok := params["tools"]; ok {
		if t, ok := tools.([]any); ok {
			toolCount = len(t)
		}
	}
	log.Printf("  upstream: account=%s stream=%v tools=%d msgs=%d max_tokens=%v effort=%v proxies=%v",
		truncateEmail(acc.Email), stream, toolCount, getMsgCount(params), body["max_tokens"], body["reasoning_effort"], useProxies)

	// 代理模式: 网络错误冷却该出口并换下一个代理重试（最多 3 次）,
	// 不冷却账号; 直连模式: 保持原语义（网络错误 5 分钟短冷却）。
	client := kit.HTTPClient
	attempts := 1
	if useProxies {
		attempts = 3
	}
	var resp *http.Response
	var lastErr error
	for i := 0; i < attempts; i++ {
		proxyURL, pidx := "", -1
		if useProxies {
			proxyURL, pidx = pickUpstreamProxy()
			client = proxyClientFor(proxyURL)
			log.Printf("  cline upstream via %s", maskProxyURL(proxyURL))
		}
		// 每次尝试重建请求: bytes.Reader 只能读一次,
		// 复用已发送的 req 会以 "ContentLength=N with Body length 0" 失败
		req, rerr := http.NewRequestWithContext(ctx, "POST", cline.ClineAPIBase+"/chat/completions", bytes.NewReader(bodyJSON))
		if rerr != nil {
			return nil, acc, fmt.Errorf("create request: %w", rerr)
		}
		req.Header = clineHeaders(token, sessionID)
		resp, lastErr = client.Do(req)
		if lastErr == nil {
			break
		}
		if ctx.Err() != nil {
			return nil, acc, fmt.Errorf("client aborted: %w", lastErr)
		}
		if useProxies {
			// 隧道层失败才冷却,且只冷 2 分钟: 上游过载也会表现为连接重置,
			// 长冷却会让几次慢请求毒化整个池;2 分钟能跳过真死代理又快速自愈
			cooldownUpstreamProxy(pidx, 2*time.Minute)
			log.Printf("  cline proxy failed (%v), cooldown exit 2m, retrying on next", lastErr)
			continue
		}
		// 直连网络错误：临时短冷却 5 分钟
		markAccountCooldown(acc, "network error: "+lastErr.Error(), 5*time.Minute)
		return nil, acc, fmt.Errorf("upstream request: %w", lastErr)
	}
	if lastErr != nil {
		// 所有代理出口都失败（useProxies 时）——不冷却账号
		return nil, acc, fmt.Errorf("upstream request (all proxy exits failed): %w", lastErr)
	}

	if resp.StatusCode == 401 {
		resp.Body.Close()
		// Refresh token and retry（重建请求: 上一次 Do 已消费请求体）
		if rerr := refreshAccountToken(acc); rerr == nil {
			token = acc.AccessToken
			req2, cerr := http.NewRequestWithContext(ctx, "POST", cline.ClineAPIBase+"/chat/completions", bytes.NewReader(bodyJSON))
			if cerr != nil {
				return nil, acc, fmt.Errorf("create request: %w", cerr)
			}
			req2.Header = clineHeaders(token, sessionID)
			// 注意: 不能用 := —— 那会在 if 块内遮蔽外层 resp，重试成功的
			// 响应被丢弃（body 泄漏）而调用方仍拿到旧的 401
			resp2, derr := client.Do(req2)
			if derr != nil {
				return nil, acc, fmt.Errorf("upstream retry: %w", derr)
			}
			if resp2.StatusCode == 401 {
				resp2.Body.Close()
				poolMu.Lock()
				acc.Status = "expired"
				savePoolLocked()
				poolMu.Unlock()
				return nil, acc, fmt.Errorf("account %s token expired permanently", truncateEmail(acc.Email))
			}
			resp = resp2
		} else {
			poolMu.Lock()
			acc.Status = "expired"
			savePoolLocked()
			poolMu.Unlock()
			return nil, acc, fmt.Errorf("account %s refresh failed: %w", truncateEmail(acc.Email), rerr)
		}
	}

	if resp.StatusCode != 200 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		// Mark account on cooldown on rate limits
		if resp.StatusCode == 429 {
			reason := kit.Truncate(string(bodyBytes), 500)
			duration := parseInferenceCapDuration(string(bodyBytes))
			if duration <= 0 {
				duration = parseRetryAfter(resp.Header.Get("Retry-After"))
			}
			markAccountCooldown(acc, "429: "+reason, duration)
			log.Printf("  account %s cooldown %v (reason: %s)", truncateEmail(acc.Email), duration, reason)
		}
		return nil, acc, fmt.Errorf("API %d: %s", resp.StatusCode, kit.Truncate(string(bodyBytes), 500))
	}

	bumpUsage(acc)
	return resp, acc, nil
}

// accountUsageFn 构造账号 token 记账回调：从上游 usage 提取
// prompt_tokens + completion_tokens，计入该账号今日/累计消耗。
func accountUsageFn(acc *Account, params map[string]any) func(map[string]any) {
	return func(u map[string]any) {
		var pt, ct float64
		if v, ok := u["prompt_tokens"].(float64); ok {
			pt = v
		}
		if v, ok := u["completion_tokens"].(float64); ok {
			ct = v
		}
		tokens := int64(pt + ct)
		if tokens <= 0 && params != nil {
			// 上游未返回 usage 时用入站请求估算兜底（与 zen 统计一致）
			tokens = int64(estimateJSON(params))
		}
		recordAccountTokens(acc, tokens)
	}
}

func truncateEmail(email string) string {
	if len(email) <= 12 {
		return email
	}
	parts := splitEmail(email)
	if len(parts) == 2 && len(parts[0]) > 3 {
		return parts[0][:3] + "***@" + parts[1]
	}
	if len(email) > 12 {
		return email[:8] + "..."
	}
	return email
}

func splitEmail(email string) []string {
	for i := 0; i < len(email); i++ {
		if email[i] == '@' {
			return []string{email[:i], email[i+1:]}
		}
	}
	return []string{email}
}

func getMsgCount(params map[string]any) int {
	if msgs, ok := params["messages"].([]any); ok {
		return len(msgs)
	}
	return 0
}

func handleStreamResponseWithUsage(w http.ResponseWriter, upstream *http.Response, onUsage func(map[string]any)) {
	// Check Flusher support BEFORE writing any headers
	flusher, ok := w.(http.Flusher)
	if !ok {
		// Client connection doesn't support streaming.
		// Aggregate the upstream stream into a single response, then emit it as SSE events
		// so downstream SSE parsers (like AxonHub) receive a valid SSE stream.
		log.Printf("  streaming not supported for client, falling back to SSE-wrapped aggregation")
		out, err := collectStreamResponse(upstream)
		if err != nil {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("data: {\"error\":{\"message\":\"aggregation failed\",\"type\":\"parse_error\"}}\n\n"))
			w.Write([]byte("data: [DONE]\n\n"))
			return
		}
		if u, ok := out["usage"].(map[string]any); ok && len(u) > 0 {
			onUsage(u)
		}
		out = normalizeOpenAIResponse(out)

		// Emit the aggregated response as SSE events
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(http.StatusOK)

		// Build a content delta chunk
		contentVal := getNested(out, "choices", 0, "message", "content")
		delta := map[string]any{
			"role":    "assistant",
			"content": contentVal,
		}
		if tc := getNested(out, "choices", 0, "message", "tool_calls"); tc != nil {
			delta["tool_calls"] = tc
		}
		chunk := map[string]any{
			"id":      out["id"],
			"object":  "chat.completion.chunk",
			"created": out["created"],
			"model":   out["model"],
			"choices": []map[string]any{
				{
					"index":         0,
					"delta":         delta,
					"finish_reason": nil,
				},
			},
		}
		if u, ok := out["usage"]; ok {
			chunk["usage"] = u
		}
		chunkJSON, _ := json.Marshal(chunk)
		w.Write([]byte("data: " + string(chunkJSON) + "\n\n"))

		// Done chunk —— finish_reason 透传上游真实值（tool_calls/length 等），
		// 硬编码 "stop" 会让依赖 finish_reason 的客户端误判工具调用回合
		doneFR := getNested(out, "choices", 0, "finish_reason")
		if s, ok := doneFR.(string); !ok || s == "" {
			doneFR = "stop"
		}
		doneChunk := map[string]any{
			"id":      out["id"],
			"object":  "chat.completion.chunk",
			"created": out["created"],
			"model":   out["model"],
			"choices": []map[string]any{
				{
					"index":         0,
					"delta":         map[string]any{},
					"finish_reason": doneFR,
				},
			},
		}
		doneJSON, _ := json.Marshal(doneChunk)
		w.Write([]byte("data: " + string(doneJSON) + "\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)

	reader := bufio.NewReader(upstream.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				if line != "" {
					w.Write([]byte(line + "\n"))
				}
			}
			break
		}

		line = strings.TrimRight(line, "\r\n")

		if strings.HasPrefix(line, "data:") {
			payload := strings.TrimSpace(line[5:])
			if payload == "" || payload == "[DONE]" {
				w.Write([]byte(line + "\n\n"))
				flusher.Flush()
				continue
			}

			// Try to normalize the response
			var obj map[string]any
			if err := json.Unmarshal([]byte(payload), &obj); err == nil {
				if onUsage != nil {
					if u, ok := obj["usage"].(map[string]any); ok && len(u) > 0 {
						onUsage(u)
					}
				}
				// Some Cline responses wrap in {data: {...}}
				if data, ok := obj["data"]; ok {
					if d, ok := data.(map[string]any); ok {
						if _, hasChoices := d["choices"]; hasChoices {
							obj = d
						}
						if _, hasID := d["id"]; hasID {
							obj = d
						}
					}
				}
				normalized := normalizeOpenAIResponse(obj)
				if normBytes, err := json.Marshal(normalized); err == nil {
					w.Write([]byte("data: " + string(normBytes) + "\n\n"))
					flusher.Flush()
					continue
				}
			}
		}

		w.Write([]byte(line + "\n"))
		flusher.Flush()
	}
}

func handleNonStreamResponseWithUsage(w http.ResponseWriter, upstream *http.Response, onUsage func(map[string]any), stops []string) {
	var raw map[string]any
	if err := json.NewDecoder(upstream.Body).Decode(&raw); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": map[string]string{"message": err.Error(), "type": "parse_error"},
		})
		return
	}

	if onUsage != nil {
		if u, ok := raw["usage"].(map[string]any); ok && len(u) > 0 {
			onUsage(u)
		}
	}

	// Some Cline responses wrap in {data: {...}}
	out := raw
	if data, ok := raw["data"]; ok {
		if d, ok := data.(map[string]any); ok {
			out = d
		}
	}

	out = normalizeOpenAIResponse(out)
	truncateAtStopSequences(out, stops)

	if msg, ok := getNested(out, "choices", 0, "message").(map[string]any); ok {
		tc, _ := msg["tool_calls"].([]any)
		content, _ := msg["content"].(string)
		log.Printf("  nonstream finish=%v tool_calls=%d content_len=%d",
			getNested(out, "choices", 0, "finish_reason"),
			len(tc), len(content))
	}

	writeJSON(w, http.StatusOK, out)
}

// stopSequencesFrom 从客户端请求中提取 stop 序列（OpenAI 允许 string 或 string[]）。
// json.RawMessage 也是 string 的底层类型但不能直接断言成 string —— Anthropic
// 转换路径写入的 stop 就是 RawMessage，漏掉它会让 /v1/messages 的
// stop_sequences 截断兜底整个失效。
func stopSequencesFrom(params map[string]any) []string {
	var stops []string
	addString := func(s string) {
		if s != "" {
			stops = append(stops, s)
		}
	}
	switch v := params["stop"].(type) {
	case string:
		addString(v)
	case json.RawMessage:
		var one string
		if json.Unmarshal(v, &one) == nil {
			addString(one)
			break
		}
		var many []string
		if json.Unmarshal(v, &many) == nil {
			for _, s := range many {
				addString(s)
			}
		}
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				addString(s)
			}
		}
	case []string:
		for _, s := range v {
			addString(s)
		}
	}
	return stops
}

// truncateAtStopSequences 在网关侧兜底执行 OpenAI 语义的 stop 截断：
// cline 上游对 stop 的执行不可靠（实测时而不截断、时而整体吞掉内容），
// 因此对非流式响应在返回前做确定性截断（截到最早出现的 stop 序列之前）。
// 流式透传路径不做缓冲截断，避免破坏 SSE 的逐块转发。
func truncateAtStopSequences(out map[string]any, stops []string) {
	if len(stops) == 0 {
		return
	}
	choices, ok := out["choices"].([]any)
	if !ok || len(choices) == 0 {
		return
	}
	choice, ok := choices[0].(map[string]any)
	if !ok {
		return
	}
	msg, ok := choice["message"].(map[string]any)
	if !ok {
		return
	}
	content, ok := msg["content"].(string)
	if !ok || content == "" {
		return
	}
	cut := -1
	for _, s := range stops {
		if i := strings.Index(content, s); i >= 0 && (cut < 0 || i < cut) {
			cut = i
		}
	}
	if cut >= 0 {
		msg["content"] = content[:cut]
	}
}

// toolAccum 流式工具调用累积器（按上游 index 分桶）
type toolAccum struct {
	call map[string]any
	args strings.Builder
}

func collectStreamResponse(upstream *http.Response) (map[string]any, error) {
	var (
		model        string
		content      strings.Builder
		finishReason string
		usage        map[string]any
		// 并行工具调用按上游 index 分桶累积（与 chatStreamToResponses 一致），
		// 上游交错下发 index 0/1/0/1 片段时单桶会互相污染
		pendingTools = map[int]*toolAccum{}
	)

	reader := bufio.NewReader(upstream.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			// 真实读错误（连接中断等）：把部分内容当成功返回会让客户端拿到
			// 截断的代码/工具参数还毫无察觉 —— 上抛由调用方返回 500 重试
			return nil, fmt.Errorf("upstream stream read failed after %d bytes: %w", content.Len(), err)
		}
		line = strings.TrimRight(line, "\r\n")

		if strings.HasPrefix(line, "data:") {
			payload := strings.TrimSpace(line[5:])
			if payload == "" || payload == "[DONE]" {
				if err == io.EOF {
					break
				}
				continue
			}
			var obj map[string]any
			if json.Unmarshal([]byte(payload), &obj) != nil {
				continue
			}
			if data, ok := obj["data"]; ok {
				if d, ok := data.(map[string]any); ok {
					obj = d
				}
			}
			if m, ok := obj["model"].(string); ok && m != "" {
				model = m
			}
			if u, ok := obj["usage"].(map[string]any); ok && len(u) > 0 {
				usage = u
			}
			choices, _ := getNested(obj, "choices").([]any)
			if len(choices) == 0 {
				continue
			}
			choice, _ := choices[0].(map[string]any)
			if choice == nil {
				continue
			}
			delta, _ := choice["delta"].(map[string]any)
			if delta == nil {
				delta = choice
			}
			if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
				finishReason = fr
			}
			if c, ok := delta["content"].(string); ok && c != "" {
				content.WriteString(c)
			}
			if tcRaw, ok := delta["tool_calls"].([]any); ok {
				for _, tc := range tcRaw {
					tcMap, _ := tc.(map[string]any)
					if tcMap == nil {
						continue
					}
					idx := 0
					if i, ok := tcMap["index"].(float64); ok {
						idx = int(i)
					}
					acc := pendingTools[idx]
					if acc == nil {
						acc = &toolAccum{call: map[string]any{
							"id":       tcMap["id"],
							"type":     "function",
							"function": map[string]any{"name": "", "arguments": ""},
						}}
						pendingTools[idx] = acc
					}
					if fn, ok := tcMap["function"].(map[string]any); ok {
						if n, ok := fn["name"].(string); ok && n != "" {
							acc.call["function"].(map[string]any)["name"] = n
						}
						// cline 上游在工具调用的起始分片发送 "arguments":""（空字符串）。
						// 空串必须整体跳过：若走对象分支会把空串序列化成字面量 ""
						// 追加进参数，最终得到 "\"\"\"\"{\"city\":...}" 这样的坏 JSON。
						if a, ok := fn["arguments"].(string); ok {
							if a != "" {
								acc.args.WriteString(a)
							}
						} else if aRaw, ok := fn["arguments"]; ok && aRaw != nil {
							// 某些上游一次性下发完整对象 —— 序列化后追加，不能丢弃
							if bts, merr := json.Marshal(aRaw); merr == nil {
								acc.args.WriteString(string(bts))
							}
						}
					}
				}
			}
		}
		if err == io.EOF {
			break
		}
	}
	// 按 index 顺序汇出工具调用
	toolCalls := make([]any, 0, len(pendingTools))
	for i := 0; len(pendingTools) > 0; i++ {
		acc, ok := pendingTools[i]
		if !ok {
			break
		}
		acc.call["function"].(map[string]any)["arguments"] = repairToolArguments(acc.args.String())
		toolCalls = append(toolCalls, acc.call)
		delete(pendingTools, i)
	}

	message := map[string]any{
		"role":    "assistant",
		"content": content.String(),
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}
	if finishReason == "" {
		// 流以 EOF 结束但从未收到 finish_reason 块 —— 补一个合法值，
		// 空串对 OpenAI 方言客户端是非法 finish_reason
		finishReason = "stop"
	}
	choice := map[string]any{
		"index":         0,
		"message":       message,
		"finish_reason": finishReason,
	}
	out := map[string]any{
		"id":      "chatcmpl_" + fmt.Sprintf("%x", time.Now().UnixMilli()),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{choice},
	}
	if usage != nil {
		out["usage"] = usage
	}
	return out, nil
}

func modelNeedsStream(modelID string) bool {
	initModelsCache()
	modelsMu.Lock()
	defer modelsMu.Unlock()
	if m, ok := modelsCache[modelID]; ok && m.RequiresStream {
		return true
	}
	return false
}

// Anthropic Messages API support
type anthropicMsg struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type toolAccumulator struct {
	index   int
	id      string
	name    string
	args    string
	emitted bool
}

type anthropicReq struct {
	Model       string          `json:"model"`
	MaxTokens   int             `json:"max_tokens"`
	Messages    []anthropicMsg  `json:"messages"`
	System      json.RawMessage `json:"system,omitempty"`
	Stream      bool            `json:"stream,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
	TopK        *int            `json:"top_k,omitempty"`
	Stop        json.RawMessage `json:"stop_sequences,omitempty"`
	Tools       json.RawMessage `json:"tools,omitempty"`
	ToolChoice  json.RawMessage `json:"tool_choice,omitempty"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
	Extra       map[string]any  `json:"-"`
}

func loadOverrideContent() string {
	data, err := os.ReadFile("override.md")
	if err != nil {
		// override.md 是可选功能，文件不存在时静默使用客户端自带提示词
		return ""
	}
	content := strings.TrimSpace(string(data))
	if content != "" {
		log.Printf("  using override.md as system prompt (%d bytes)", len(content))
	} else {
		log.Printf("  override.md is empty, using client system prompt")
	}
	return content
}

func extractStringContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Try string first
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Try array of content blocks
	var blocks []map[string]any
	if err := json.Unmarshal(raw, &blocks); err == nil {
		parts := []string{}
		for _, b := range blocks {
			if b["type"] == "text" {
				if t, ok := b["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func anthropicToolsToOpenAI(tools []any) []any {
	out := make([]any, 0, len(tools))
	for _, t := range tools {
		if tMap, ok := t.(map[string]any); ok {
			// Already in OpenAI format
			if tMap["type"] == "function" {
				out = append(out, t)
				continue
			}
			// Convert Anthropic format to OpenAI
			oai := map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        tMap["name"],
					"description": tMap["description"],
					"parameters":  tMap["input_schema"],
				},
			}
			out = append(out, oai)
		}
	}
	return out
}

func anthropicToOpenAI(req anthropicReq) map[string]any {
	openAI := map[string]any{
		"model":      req.Model,
		"max_tokens": req.MaxTokens,
		"stream":     req.Stream,
		"messages":   []any{},
	}
	// 指针字段：temperature=0（确定性输出）与未设置必须区分开
	if req.Temperature != nil {
		openAI["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		openAI["top_p"] = *req.TopP
	}
	// stop_sequences 是合法的 OpenAI stop 参数，之前被静默丢弃会导致
	// 依赖停止序列的 Agent 生成失控
	if len(req.Stop) > 0 && string(req.Stop) != "null" {
		openAI["stop"] = json.RawMessage(req.Stop)
	}
	// Convert Anthropic tools to OpenAI format
	if req.Tools != nil {
		var toolsArr []any
		if err := json.Unmarshal(req.Tools, &toolsArr); err == nil {
			openAI["tools"] = anthropicToolsToOpenAI(toolsArr)
		}
	}
	if req.ToolChoice != nil {
		// Anthropic 形状 {"type":"any"}/{"type":"tool","name":x} 映射为
		// OpenAI 形状 "required"/{"type":"function",...}，原样透传会被上游 400
		var tc map[string]any
		if json.Unmarshal(req.ToolChoice, &tc) == nil {
			switch tc["type"] {
			case "auto":
				openAI["tool_choice"] = "auto"
			case "none":
				openAI["tool_choice"] = "none"
			case "any":
				openAI["tool_choice"] = "required"
			case "tool":
				if n, _ := tc["name"].(string); n != "" {
					openAI["tool_choice"] = map[string]any{
						"type":     "function",
						"function": map[string]any{"name": n},
					}
				}
			default:
				openAI["tool_choice"] = req.ToolChoice
			}
		}
	}

	msgs := []any{}

	// System prompt: 默认用请求自带的 system；仅 APPLY_SYSTEM_PROMPT_OVERRIDE=true
	// 时才用 override.md 替换（与 chat 路径 applyOverride 行为一致）
	sysContent := ""
	if SystemPromptOverrideEnabled() {
		sysContent = loadOverrideContent()
	}
	if sysContent == "" && req.System != nil {
		sysContent = extractStringContent(req.System)
	}
	if sysContent != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": sysContent})
	}

	for _, m := range req.Messages {
		switch c := m.Content.(type) {
		case string:
			msgs = append(msgs, map[string]any{"role": m.Role, "content": c})
		case []any:
			textParts := []string{}
			var toolCalls []any
			var toolResults []map[string]any

			for _, block := range c {
				if b, ok := block.(map[string]any); ok {
					switch b["type"] {
					case "text":
						if t, ok := b["text"].(string); ok {
							textParts = append(textParts, t)
						}
					case "image":
						// skip images
					case "tool_use":
						argsStr := "{}"
						if input, ok := b["input"]; ok && input != nil {
							if s, ok := input.(string); ok {
								argsStr = s
							} else if bts, err := json.Marshal(input); err == nil {
								argsStr = string(bts)
							}
						}
						tc := map[string]any{
							"id":   b["id"],
							"type": "function",
							"function": map[string]any{
								"name":      b["name"],
								"arguments": argsStr,
							},
						}
						toolCalls = append(toolCalls, tc)
					case "tool_result":
						toolCallID, _ := b["tool_use_id"].(string)
						if toolCallID == "" {
							continue
						}
						toolResults = append(toolResults, map[string]any{
							"role":         "tool",
							"content":      anthropicContentToString(b["content"]),
							"tool_call_id": toolCallID,
						})
					}
				}
			}

			if m.Role == "assistant" && len(toolCalls) > 0 {
				msg := map[string]any{
					"role":       "assistant",
					"content":    strings.Join(textParts, "\n"),
					"tool_calls": toolCalls,
				}
				msgs = append(msgs, msg)
				log.Printf("  anthropic req: assistant tool_calls=%d", len(toolCalls))
			} else if m.Role == "user" && len(toolResults) > 0 {
				for _, tr := range toolResults {
					msgs = append(msgs, tr)
					content, _ := tr["content"].(string)
					id, _ := tr["tool_call_id"].(string)
					log.Printf("  anthropic req: tool_result id=%s content_len=%d prefix=%s", id, len(content), kit.Truncate(content, 400))
				}
				// 混合块中 tool_result 与 text 并存时，text 不能丢
				//（Claude Code 在工具执行期间允许用户输入）
				if len(textParts) > 0 {
					msgs = append(msgs, map[string]any{"role": "user", "content": strings.Join(textParts, "\n")})
				}
			} else {
				content := strings.Join(textParts, "\n")
				msgs = append(msgs, map[string]any{"role": m.Role, "content": content})
			}
		}
	}

	openAI["messages"] = msgs
	return openAI
}

// repairToolArguments 修复上游偶发的损坏 tool arguments（杂散 "" 前缀、
// 整体被字符串包裹、截断 JSON —— parseToolArgs 注释记录的已知怪癖）。
// 参数本身合法时原样返回；可修复时重新序列化为规范 JSON 字符串；
// 修复失败时原样返回（不吞掉上游原始值）。
func repairToolArguments(args string) string {
	if args == "" || json.Valid([]byte(args)) {
		return args
	}
	v, err := parseToolArgs(args)
	if err != nil {
		return args
	}
	b, err := json.Marshal(v)
	if err != nil {
		return args
	}
	return string(b)
}

// parseToolArgs 解析工具调用参数 JSON，带容错修复
func parseToolArgs(raw string) (any, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]any{}, nil
	}
	// 清理杂散引号前缀：上游流式输出偶发 "" 前缀（如 ""{"file_path":...}）
	for strings.HasPrefix(raw, `""`) {
		raw = strings.TrimPrefix(raw, `""`)
	}
	raw = strings.TrimSpace(raw)
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err == nil {
		if v == nil {
			return map[string]any{}, nil
		}
		return v, nil
	}
	// 整体被 JSON 字符串包裹（"{\"file_path\": ...}"）时，解包字符串后再解析
	if strings.HasPrefix(raw, `"`) && strings.HasSuffix(raw, `"`) && len(raw) >= 2 {
		var s string
		if json.Unmarshal([]byte(raw), &s) == nil {
			var v2 any
			if json.Unmarshal([]byte(s), &v2) == nil && v2 != nil {
				return v2, nil
			}
		}
	}
	fixed := raw
	if strings.HasPrefix(fixed, "{") && !strings.HasSuffix(fixed, "}") {
		fixed += "}"
	} else if strings.HasPrefix(fixed, "[") && !strings.HasSuffix(fixed, "]") {
		fixed += "]"
	}
	if strings.HasSuffix(fixed, ",") {
		fixed = strings.TrimRight(fixed, ",") + "}"
	}
	if err := json.Unmarshal([]byte(fixed), &v); err == nil && v != nil {
		return v, nil
	}
	// 最终兜底：从杂散内容中提取首个 JSON 对象/数组
	if i := strings.IndexAny(raw, "{["); i >= 0 {
		openCh := raw[i]
		closeCh := byte('}')
		if openCh == '[' {
			closeCh = ']'
		}
		if j := strings.LastIndex(raw, string(closeCh)); j > i {
			sub := raw[i : j+1]
			if json.Unmarshal([]byte(sub), &v) == nil && v != nil {
				return v, nil
			}
		}
	}
	return nil, fmt.Errorf("invalid json: %s", kit.Truncate(raw, 120))
}

// extractToolSchemas 从 Anthropic 请求的 tools 定义中解析每个工具的 input_schema 属性集合，
// 用于转发 tool_use 时裁剪 input，避免多余字段触发客户端校验失败。
func extractToolSchemas(tools json.RawMessage) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	if len(tools) == 0 {
		return out
	}
	var arr []map[string]any
	if err := json.Unmarshal(tools, &arr); err != nil {
		return out
	}
	for _, t := range arr {
		name, _ := t["name"].(string)
		if name == "" {
			continue
		}
		schema, _ := t["input_schema"].(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		var propNames []string
		for k := range props {
			propNames = append(propNames, k)
		}
		var required []string
		if req, ok := schema["required"].([]any); ok {
			for _, r := range req {
				if s, ok := r.(string); ok {
					required = append(required, s)
				}
			}
		}
		log.Printf("  tool schema: name=%s properties=%v required=%v", name, propNames, required)
		if len(props) == 0 {
			continue
		}
		allowed := map[string]bool{}
		for k := range props {
			allowed[k] = true
		}
		out[name] = allowed
	}
	return out
}

// filterToolInput 将工具参数裁剪到客户端 schema 允许的字段内；
// 找不到 schema 或过滤后为空时保留原参数，避免丢参数。
func filterToolInput(name string, input map[string]any, schemas map[string]map[string]bool) map[string]any {
	allowed, ok := schemas[name]
	if !ok || len(allowed) == 0 {
		return input
	}
	out := map[string]any{}
	for k, v := range input {
		if allowed[k] {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return input
	}
	return out
}

// anthropicContentToString 将 Anthropic content（字符串或块数组）转为纯文本
func anthropicContentToString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	if arr, ok := v.([]any); ok {
		parts := []string{}
		for _, it := range arr {
			if b, ok := it.(map[string]any); ok {
				if t, ok := b["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func openAIToAnthropic(openAI map[string]any) map[string]any {
	out := map[string]any{
		"id":    "msg_" + fmt.Sprintf("%x", time.Now().UnixMilli()),
		"type":  "message",
		"role":  "assistant",
		"model": getNested(openAI, "model"),
	}

	choices := getNested(openAI, "choices")
	if choices == nil {
		out["content"] = []any{map[string]any{"type": "text", "text": ""}}
		out["stop_reason"] = "end_turn"
		out["usage"] = map[string]any{"input_tokens": 0, "output_tokens": 0}
		return out
	}

	text := ""
	choice0, ok := getNested(openAI, "choices", 0).(map[string]any)
	if !ok {
		out["content"] = []any{map[string]any{"type": "text", "text": text}}
		out["stop_reason"] = "end_turn"
		out["usage"] = map[string]any{"input_tokens": 0, "output_tokens": 0}
		return out
	}
	msg, _ := choice0["message"].(map[string]any)
	if msg == nil {
		msg, _ = choice0["delta"].(map[string]any)
	}
	if msg != nil {
		if c, ok := msg["content"].(string); ok {
			text = sanitizeContent(c)
		}
	}

	contentBlocks := []any{map[string]any{"type": "text", "text": text}}

	// Convert tool_calls to Anthropic tool_use blocks
	if msg != nil {
		if tc, ok := msg["tool_calls"].([]any); ok && len(tc) > 0 {
			contentBlocks = []any{}
			if text != "" {
				contentBlocks = append(contentBlocks, map[string]any{"type": "text", "text": text})
			}
			for _, tcItem := range tc {
				if tcMap, ok := tcItem.(map[string]any); ok {
					funcData, _ := tcMap["function"].(map[string]any)
					if funcData == nil {
						continue
					}
					input := funcData["arguments"]
					// OpenAI arguments is a JSON string; Anthropic expects an object
					if argsStr, ok := input.(string); ok {
						var argsObj any
						if json.Unmarshal([]byte(argsStr), &argsObj) == nil {
							input = argsObj
						}
					}
					if input == nil {
						input = map[string]any{}
					}
					id, _ := tcMap["id"].(string)
					if id == "" {
						id = fmt.Sprintf("toolu_%x_%d", time.Now().UnixMilli(), len(contentBlocks))
					}
					name, _ := funcData["name"].(string)
					if name == "" {
						continue
					}
					block := map[string]any{
						"type":  "tool_use",
						"id":    id,
						"name":  name,
						"input": input,
					}
					contentBlocks = append(contentBlocks, block)
				}
			}
		}
	}

	out["content"] = contentBlocks

	switch getNested(openAI, "choices", 0, "finish_reason") {
	case "stop":
		out["stop_reason"] = "end_turn"
	case "length":
		out["stop_reason"] = "max_tokens"
	case "tool_calls":
		out["stop_reason"] = "tool_use"
	default:
		out["stop_reason"] = "end_turn"
	}

	// usage 字段必须有确定数值：缺 key 时置 0，null 会炸掉 Anthropic 客户端
	// 的整数断言（上下文预估 / 费用统计）
	usage := map[string]any{"input_tokens": 0, "output_tokens": 0}
	if u := getNested(openAI, "usage"); u != nil {
		if um, ok := u.(map[string]any); ok {
			if pt, ok := um["prompt_tokens"].(float64); ok {
				usage["input_tokens"] = int(pt)
			}
			if ct, ok := um["completion_tokens"].(float64); ok {
				usage["output_tokens"] = int(ct)
			}
		}
	}
	out["usage"] = usage

	return out
}

func handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"message": err.Error(), "type": "parse_error"},
		})
		return
	}

	var req anthropicReq
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"message": err.Error(), "type": "parse_error"},
		})
		return
	}

	if len(req.Messages) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"message": "messages is required", "type": "parse_error"},
		})
		return
	}

	toolSchemas := extractToolSchemas(req.Tools)
	if len(toolSchemas) > 0 {
		log.Printf("  anthropic tools: %d schemas", len(toolSchemas))
	}

	if req.MaxTokens == 0 {
		req.MaxTokens = defaultMaxTokens
	}

	// combo 别名模型：改写为目标上游模型（anthropicToOpenAI 取 req.Model）
	useProxies := clineProxiesEnabled()
	if c := resolveCombo(req.Model); c != nil {
		log.Printf("  anthropic combo %q -> %s model %q (useProxies=%v)", req.Model, c.Platform, c.Target, c.UseProxies)
		req.Model = c.Target
		if c.UseProxies {
			useProxies = true
		}
	}

	openAIReq := anthropicToOpenAI(req)

	log.Printf("  anthropic: model=%s stream=%v msgs=%d", req.Model, req.Stream, len(req.Messages))

	// zen 免费模型路由
	if route := routeModel(req.Model); route == "zen" {
		handleZenAnthropic(w, r, req, openAIReq, toolSchemas)
		return
	} else if route == "reject" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"message": fmt.Sprintf("model %q is a paid zen model; only free zen models are proxied", req.Model), "type": "invalid_request_error"},
		})
		return
	}

	// cline 路由：未知模型名显式拒绝（与 chat 路径一致）
	if msg := strictModelGate(req.Model); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"message": msg, "type": "invalid_request_error"},
		})
		return
	}

	total, activeCount := poolStatusSnapshot()

	if total == 0 && activeCount == 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"error": map[string]string{
				"message": "No accounts in pool",
				"type":    "auth_error",
			},
		})
		return
	}

	upstreamStream := req.Stream
	if !req.Stream && modelNeedsStream(normalizeRequestModel(req.Model)) {
		upstreamStream = true
		log.Printf("  anthropic model %s requires stream: forcing upstream stream, will aggregate", req.Model)
	}

	resp, acc, err := callClineAPI(r.Context(), openAIReq, upstreamStream, useProxies)
	if err != nil {
		log.Printf("  anthropic api error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": map[string]string{"message": err.Error(), "type": "api_error"},
		})
		return
	}
	defer resp.Body.Close()

	usageFn := accountUsageFn(acc, openAIReq)

	if req.Stream {
		handleAnthropicStreamWithUsage(w, resp, normalizeRequestModel(req.Model), toolSchemas, usageFn)
		return
	}

	if upstreamStream {
		out, err := collectStreamResponse(resp)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": map[string]string{"message": err.Error(), "type": "parse_error"},
			})
			return
		}
		if u, ok := out["usage"].(map[string]any); ok && len(u) > 0 {
			usageFn(u)
		}
		out = normalizeOpenAIResponse(out)
		truncateAtStopSequences(out, stopSequencesFrom(openAIReq))
		anthropicResp := openAIToAnthropic(out)
		if tc, ok := getNested(out, "choices", 0, "message", "tool_calls").([]any); ok && len(tc) > 0 {
			anthropicResp["stop_reason"] = "tool_use"
		}
		writeJSON(w, http.StatusOK, anthropicResp)
		return
	}

	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": map[string]string{"message": err.Error(), "type": "parse_error"},
		})
		return
	}
	if u, ok := raw["usage"].(map[string]any); ok && len(u) > 0 {
		usageFn(u)
	}
	out := raw
	if data, ok := raw["data"]; ok {
		if d, ok := data.(map[string]any); ok {
			out = d
		}
	}
	out = normalizeOpenAIResponse(out)
	truncateAtStopSequences(out, stopSequencesFrom(openAIReq))
	anthropicResp := openAIToAnthropic(out)

	if tc, ok := getNested(out, "choices", 0, "message", "tool_calls").([]any); ok && len(tc) > 0 {
		anthropicResp["stop_reason"] = "tool_use"
	}

	writeJSON(w, http.StatusOK, anthropicResp)
}

// handleZenAnthropic Anthropic Messages 请求路由到 zen 免费模型上游
func handleZenAnthropic(w http.ResponseWriter, r *http.Request, req anthropicReq, openAIReq map[string]any, toolSchemas map[string]map[string]bool) {
	cfg := getZenConfig()
	if !cfg.Enabled {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": map[string]string{"message": "zen upstream disabled in /admin/ settings", "type": "api_error"},
		})
		return
	}
	zm, ok := resolveZenFreeModel(req.Model)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"message": fmt.Sprintf("model %q is not a free zen model", req.Model), "type": "invalid_request_error"},
		})
		return
	}
	isStream := req.Stream
	tracker := newZenStatsTracker(zenStatsRecord{
		TS:           time.Now().UnixMilli(),
		Upstream:     "zen",
		Model:        zm.ID,
		Stream:       isStream,
		PromptTokens: estimateJSON(openAIReq),
	})

	sid := requestSessionID(openAIReq, r.Header)
	out := maybeCompact(openAIReq, zm, sid)
	tracker.rec.Compacted = out.changed
	tracker.rec.CompactionTokens = out.compactTokens
	if out.changed {
		log.Printf("  anthropic zen: %s", out.note)
	}

	// 原生 responses 端点模型：先经 responses 原生调用拿到 chat 形态，
	// 再走与 chat 路径完全相同的 anthropic 转换。上游恒 stream=true
	//（spark 只接受 stream=true；false 会 403），客户端形态由 isStream 决定。
	if zm.Upstream == "responses" {
		resp, rateLimited, rerr := callZenResponsesAPI(r.Context(), openAIReq, true)
		if rerr != nil {
			log.Printf("  anthropic zen responses api error: %v", rerr)
			tracker.rec.RateLimited = rateLimited
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"error": map[string]string{"message": rerr.Error(), "type": "api_error"},
			})
			tracker.finish(false, http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		// 上游恒 stream=true：resp.Body 是原生 responses SSE，先聚合
		var chat map[string]any
		if chat, rerr = responsesSSEToChat(resp); rerr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": map[string]string{"message": rerr.Error(), "type": "parse_error"},
			})
			tracker.finish(false, http.StatusInternalServerError)
			return
		}
		if isStream {
			// responses 原生流已聚合为 chat：按非流式 anthropic 呈现
			//（SSE 逐块转发需原生 event 形态，聚合后已无原生块可转）
			chat["model"] = zm.ID
			anthropicResp := openAIToAnthropic(normalizeOpenAIResponse(chat))
			if tc, ok := getNested(chat, "choices", 0, "message", "tool_calls").([]any); ok && len(tc) > 0 {
				anthropicResp["stop_reason"] = "tool_use"
			}
			tracker.finish(true, http.StatusOK)
			writeJSON(w, http.StatusOK, anthropicResp)
			return
		}
		tracker.rec.RateLimited = rateLimited
		tracker.rec.Status = http.StatusOK
		if u, ok := chat["usage"].(map[string]any); ok && len(u) > 0 {
			if ct, ok := u["completion_tokens"].(float64); ok {
				tracker.rec.CompletionTokens = int(ct)
			}
		}
		chat = normalizeOpenAIResponse(chat)
		truncateAtStopSequences(chat, stopSequencesFrom(openAIReq))
		anthropicResp := openAIToAnthropic(chat)
		if tc, ok := getNested(chat, "choices", 0, "message", "tool_calls").([]any); ok && len(tc) > 0 {
			anthropicResp["stop_reason"] = "tool_use"
		}
		tracker.finish(true, http.StatusOK)
		writeJSON(w, http.StatusOK, anthropicResp)
		return
	}

	resp, rateLimited, err := callZenAPI(r.Context(), openAIReq, isStream)
	if err != nil && isWrongEndpoint(err) {
		learnZenEndpoint(zm.ID, "responses")
		log.Printf("  anthropic zen endpoint auto-learn: model=%s chat/completions rejected (%v), retrying responses",
			zm.ID, kit.Truncate(err.Error(), 120))
		resp2, rateLimited2, rerr := callZenResponsesAPI(r.Context(), openAIReq, true)
		if rerr != nil {
			log.Printf("  anthropic zen responses api error: %v", rerr)
			tracker.rec.RateLimited = rateLimited2
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"error": map[string]string{"message": rerr.Error(), "type": "api_error"},
			})
			tracker.finish(false, http.StatusBadGateway)
			return
		}
		defer resp2.Body.Close()
		chat, aerr := responsesSSEToChat(resp2)
		if aerr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": map[string]string{"message": aerr.Error(), "type": "parse_error"},
			})
			tracker.finish(false, http.StatusInternalServerError)
			return
		}
		tracker.rec.RateLimited = rateLimited2
		tracker.rec.Status = http.StatusOK
		if u, ok := chat["usage"].(map[string]any); ok && len(u) > 0 {
			if ct, ok := u["completion_tokens"].(float64); ok {
				tracker.rec.CompletionTokens = int(ct)
			}
		}
		chat["model"] = zm.ID
		chat = normalizeOpenAIResponse(chat)
		truncateAtStopSequences(chat, stopSequencesFrom(openAIReq))
		anthropicResp := openAIToAnthropic(chat)
		if tc, ok := getNested(chat, "choices", 0, "message", "tool_calls").([]any); ok && len(tc) > 0 {
			anthropicResp["stop_reason"] = "tool_use"
		}
		tracker.finish(true, http.StatusOK)
		writeJSON(w, http.StatusOK, anthropicResp)
		return
	}
	if err != nil {
		log.Printf("  anthropic zen api error: %v", err)
		tracker.rec.RateLimited = rateLimited
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": map[string]string{"message": err.Error(), "type": "api_error"},
		})
		tracker.finish(false, http.StatusBadGateway)
		return
	}
	tracker.rec.RateLimited = rateLimited
	defer resp.Body.Close()
	tracker.rec.Status = resp.StatusCode

	usageFn := func(u map[string]any) {
		if ct, ok := u["completion_tokens"].(float64); ok {
			tracker.rec.CompletionTokens = int(ct)
		}
	}

	if isStream {
		handleAnthropicStreamWithUsage(w, resp, zm.ID, toolSchemas, usageFn)
		tracker.finish(true, resp.StatusCode)
		return
	}

	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": map[string]string{"message": err.Error(), "type": "parse_error"},
		})
		tracker.finish(false, http.StatusInternalServerError)
		return
	}
	if u, ok := raw["usage"].(map[string]any); ok && len(u) > 0 {
		usageFn(u)
	}
	chatOut := raw
	if data, ok := raw["data"]; ok {
		if d, ok := data.(map[string]any); ok {
			chatOut = d
		}
	}
	chatOut = normalizeOpenAIResponse(chatOut)
	anthropicResp := openAIToAnthropic(chatOut)
	if tc, ok := getNested(chatOut, "choices", 0, "message", "tool_calls").([]any); ok && len(tc) > 0 {
		anthropicResp["stop_reason"] = "tool_use"
	}
	writeJSON(w, http.StatusOK, anthropicResp)
	tracker.finish(true, resp.StatusCode)
}

func handleAnthropicStreamWithUsage(w http.ResponseWriter, upstream *http.Response, modelName string, toolSchemas map[string]map[string]bool, onUsage func(map[string]any)) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		log.Printf("  anthropic stream: streaming not supported for client, falling back to non-stream aggregation")
		out, err := collectStreamResponse(upstream)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": map[string]string{"message": err.Error(), "type": "parse_error"},
			})
			return
		}
		if u, ok := out["usage"].(map[string]any); ok && len(u) > 0 {
			onUsage(u)
		}
		out = normalizeOpenAIResponse(out)
		anthropicResp := openAIToAnthropic(out)
		writeJSON(w, http.StatusOK, anthropicResp)
		return
	}

	log.Printf("  anthropic stream: starting real-time forward")
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)

	// 原始 SSE 落盘（完整对话内容，无上限）仅在 STREAM_LOG=true 时开启 ——
	// 公网长跑部署默认关闭，避免磁盘无限增长与对话内容留存
	var streamLog *os.File
	if StreamLogEnabled() {
		if sf, err := os.OpenFile(kit.ResolveDataPath("cline-proxy-stream.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
			streamLog = sf
		}
	}
	defer func() {
		if streamLog != nil {
			streamLog.Close()
		}
	}()

	emit := func(event string, data any) {
		d, _ := json.Marshal(data)
		line := fmt.Sprintf("event: %s\ndata: %s\n\n", event, string(d))
		w.Write([]byte(line))
		if streamLog != nil {
			streamLog.WriteString(line)
		}
		flusher.Flush()
	}

	msgID := "msg_" + fmt.Sprintf("%x", time.Now().UnixMilli())
	stopReason := "end_turn"
	// lastUsage 记录最近一次上游 usage 块，message_delta 时回传给客户端
	var lastUsage map[string]any
	emit("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":      msgID,
			"type":    "message",
			"role":    "assistant",
			"content": []any{},
			"model":   modelName,
			"usage": map[string]any{
				"input_tokens":  0,
				"output_tokens": 0,
			},
			"stop_reason": nil,
		},
	})

	textIndex := new(int)
	*textIndex = -1
	hasText := false
	pendingTools := map[int]*toolAccumulator{}
	emitIndex := 0
	nextIndex := func() int {
		i := emitIndex
		emitIndex++
		return i
	}

	emitToolBlock := func(acc *toolAccumulator) {
		acc.emitted = true
		if acc.name == "" {
			log.Printf("  tool_use missing name, skipping (id=%s)", acc.id)
			return
		}
		idx := nextIndex()
		id := acc.id
		if id == "" {
			id = fmt.Sprintf("toolu_%x_%d", time.Now().UnixMilli(), idx)
			log.Printf("  tool_use missing id, generated %s", id)
		}
		argsObj, err := parseToolArgs(acc.args)
		if err != nil {
			log.Printf("  tool args parse failed for %s: %v (raw: %s)", acc.name, err, kit.Truncate(acc.args, 300))
			argsObj = map[string]any{}
		}
		if inputMap, ok := argsObj.(map[string]any); ok {
			argsObj = filterToolInput(acc.name, inputMap, toolSchemas)
		}
		parsed, _ := json.Marshal(argsObj)
		// 只记名字/长度: 完整工具入参（文件内容等）不该无条件进日志
		log.Printf("  tool_use emit: name=%s id=%s input_len=%d", acc.name, id, len(parsed))
		emit("content_block_start", map[string]any{
			"type":  "content_block_start",
			"index": idx,
			"content_block": map[string]any{
				"type":  "tool_use",
				"id":    id,
				"name":  acc.name,
				"input": map[string]any{},
			},
		})
		emit("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": idx,
			"delta": map[string]any{
				"type":         "input_json_delta",
				"partial_json": string(parsed),
			},
		})
		emit("content_block_stop", map[string]any{
			"type":  "content_block_stop",
			"index": idx,
		})
	}

	processSSELine := func(line string) {
		line = strings.TrimRight(line, "\r\n")
		if !strings.HasPrefix(line, "data:") {
			return
		}
		payload := strings.TrimSpace(line[5:])
		if payload == "" || payload == "[DONE]" {
			return
		}

		var obj map[string]any
		if err := json.Unmarshal([]byte(payload), &obj); err != nil {
			return
		}
		if onUsage != nil {
			if u, ok := obj["usage"].(map[string]any); ok && len(u) > 0 {
				lastUsage = u
				onUsage(u)
			}
		}
		if data, ok := obj["data"]; ok {
			if d, ok := data.(map[string]any); ok {
				obj = d
			}
		}

		if errPayload, ok := obj["error"]; ok {
			errBody, _ := json.Marshal(errPayload)
			log.Printf("  upstream SSE error: %s", string(errBody))
			emit("error", map[string]any{"type": "error", "error": errPayload})
			return
		}

		choices, _ := getNested(obj, "choices").([]any)
		if len(choices) == 0 {
			return
		}
		choice, _ := choices[0].(map[string]any)
		if choice == nil {
			return
		}

		delta, ok := choice["delta"].(map[string]any)
		if !ok {
			delta = choice
		}

		if c, ok := delta["content"].(string); ok && c != "" {
			if !hasText {
				hasText = true
				*textIndex = nextIndex()
				emit("content_block_start", map[string]any{
					"type":  "content_block_start",
					"index": *textIndex,
					"content_block": map[string]any{
						"type": "text",
						"text": "",
					},
				})
			}
			emit("content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": *textIndex,
				"delta": map[string]any{
					"type": "text_delta",
					"text": sanitizeContent(c),
				},
			})
		}

		if tcRaw, ok := delta["tool_calls"].([]any); ok {
			for _, tc := range tcRaw {
				tcMap, _ := tc.(map[string]any)
				if tcMap == nil {
					continue
				}
				idx := 0
				if i, ok := tcMap["index"].(float64); ok {
					idx = int(i)
				}
				acc, exists := pendingTools[idx]
				if !exists {
					acc = &toolAccumulator{index: idx}
					pendingTools[idx] = acc
				}
				if id, ok := tcMap["id"].(string); ok && id != "" {
					acc.id = id
				}
				if fn, ok := tcMap["function"].(map[string]any); ok {
					if name, ok := fn["name"].(string); ok && name != "" {
						acc.name = name
					}
					// 空字符串分片整体跳过（否则序列化成字面量 "" 拼进参数，见
					// collectStreamResponse 同注）；非字符串对象序列化后追加
					if args, ok := fn["arguments"].(string); ok {
						if args != "" {
							acc.args += args
						}
					} else if argsRaw, ok := fn["arguments"]; ok && argsRaw != nil {
						if bts, err := json.Marshal(argsRaw); err == nil {
							acc.args += string(bts)
						}
					}
				}
			}
		}

		if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
			switch fr {
			case "length":
				stopReason = "max_tokens"
			case "tool_calls":
				stopReason = "tool_use"
			}
		}
	}

	var streamErr error
	reader := bufio.NewReader(upstream.Body)

	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			processSSELine(line)
		}
		if err != nil {
			if err != io.EOF {
				streamErr = err
			}
			break
		}
	}

	// Stop text block if active
	if hasText {
		emit("content_block_stop", map[string]any{
			"type":  "content_block_stop",
			"index": *textIndex,
		})
	}

	// Emit any remaining un-emitted tool blocks（按 index 升序 —— map 遍历
	// 顺序随机，会让相同请求每次得到不同的工具块顺序）
	idxs := make([]int, 0, len(pendingTools))
	for i := range pendingTools {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	for _, i := range idxs {
		if acc := pendingTools[i]; !acc.emitted {
			emitToolBlock(acc)
		}
	}

	// 上游中途断流（非 EOF 的真实读错误）: 不能继续伪造 message_delta/end_turn
	// + message_stop —— 那会把截断的响应当成完整成功消息交给客户端
	//（Responses 路径对此发 response.failed，这里按 Anthropic 规范发 error 事件）
	if streamErr != nil {
		log.Printf("  anthropic stream upstream error: %v", streamErr)
		emit("error", map[string]any{
			"type":  "error",
			"error": map[string]any{"type": "api_error", "message": kit.Truncate("upstream stream interrupted: "+streamErr.Error(), 300)},
		})
		return
	}

	// 上游 usage 透传给 Anthropic 客户端（Claude Code / ZCode 用它做
	// 上下文预估与自动 compact；一直报 0 会让长会话撑爆上下文才报错）
	usageOut := map[string]any{"output_tokens": 0}
	if lastUsage != nil {
		if ct, ok := lastUsage["completion_tokens"].(float64); ok {
			usageOut["output_tokens"] = int(ct)
		}
		if pt, ok := lastUsage["prompt_tokens"].(float64); ok {
			usageOut["input_tokens"] = int(pt)
		}
	}
	emit("message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   stopReason,
			"stop_sequence": nil,
		},
		"usage": usageOut,
	})

	emit("message_stop", map[string]any{"type": "message_stop"})
	log.Printf("  anthropic stream done: hasText=%v tools=%d reason=%s", hasText, len(pendingTools), stopReason)
}

func normalizeOpenAIResponse(obj map[string]any) map[string]any {
	out := make(map[string]any)
	for k, v := range obj {
		if k == "provider_metadata" || k == "proxy_metadata" {
			continue
		}
		out[k] = v
	}

	if choices, ok := out["choices"].([]any); ok {
		normalized := make([]any, 0, len(choices))
		for _, ch := range choices {
			if c, ok := ch.(map[string]any); ok {
				nc := make(map[string]any)
				for k, v := range c {
					if k == "provider_metadata" || k == "proxy_metadata" {
						continue
					}
					nc[k] = v
				}
				if msg, ok := nc["message"].(map[string]any); ok {
					nc["message"] = normalizeMessage(msg)
				}
				if delta, ok := nc["delta"].(map[string]any); ok {
					nd := make(map[string]any)
					for k, v := range delta {
						if k == "provider_metadata" || k == "proxy_metadata" {
							continue
						}
						nd[k] = v
					}
					if tc, ok := nd["tool_calls"].([]any); ok && len(tc) > 0 {
						if nd["content"] == nil {
							nd["content"] = ""
						}
					}
					nc["delta"] = nd
				}
				normalized = append(normalized, nc)
			} else {
				normalized = append(normalized, ch)
			}
		}
		out["choices"] = normalized
	}

	return out
}

func sanitizeContent(s string) string {
	return s
}

func normalizeMessage(msg map[string]any) map[string]any {
	out := make(map[string]any)
	for k, v := range msg {
		if k == "provider_metadata" || k == "proxy_metadata" {
			continue
		}
		out[k] = v
	}
	if tc, ok := out["tool_calls"].([]any); ok && len(tc) > 0 {
		if out["content"] == nil {
			out["content"] = ""
		}
		// 非流式聚合路径的兜底修复：上游偶发损坏 arguments 在这里恢复
		for _, t := range tc {
			if tm, ok := t.(map[string]any); ok {
				if fn, ok := tm["function"].(map[string]any); ok {
					if a, ok := fn["arguments"].(string); ok {
						fn["arguments"] = repairToolArguments(a)
					}
				}
			}
		}
	}
	if c, ok := out["content"].(string); ok {
		out["content"] = sanitizeContent(c)
	}
	return out
}

func getNested(obj map[string]any, keys ...any) any {
	current := any(obj)
	for _, key := range keys {
		switch k := key.(type) {
		case string:
			if m, ok := current.(map[string]any); ok {
				current = m[k]
			} else {
				return nil
			}
		case int:
			if arr, ok := current.([]any); ok && k < len(arr) {
				current = arr[k]
			} else {
				return nil
			}
		default:
			return nil
		}
	}
	return current
}

// freePort 开发便利：Windows 上强杀占用端口的进程。仅当显式设置
// KILL_PORT_ON_START=true 时启用 —— 默认关闭，生产环境无差别强杀
// 未知进程是危险的（可能干掉合法持有端口的服务）。
func freePort(port int) {
	if v, ok := envBool("KILL_PORT_ON_START"); !ok || !v {
		return
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return // port is free
	}
	conn.Close()

	// Try to kill the process using the port
	cmd := kit.ExecCommand("powershell", "-Command",
		fmt.Sprintf(`$p=Get-NetTCPConnection -LocalPort %d -ErrorAction SilentlyContinue; if($p){$p.OwningProcess | Sort-Object -Unique | ForEach-Object {Stop-Process -Id $_ -Force -ErrorAction SilentlyContinue}}`, port))
	_ = cmd.Run()
	// 杀进程后确认端口确实释放，避免旧进程尚未退出时立刻竞争监听。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err != nil {
			return
		}
		conn.Close()
		time.Sleep(100 * time.Millisecond)
	}
}

// parseInferenceCapDuration 从 Cline 429 错误体中解析 "Try again in 17h 59m" 形式的等待时长。
// 支持 "17h 59m"、"17h"、"59m"、"30s"、"1d 2h 30m" 等组合。
func parseInferenceCapDuration(body string) time.Duration {
	// 在错误体中查找 "Try again in ..." 子串
	idx := strings.Index(body, "Try again in")
	if idx < 0 {
		return 0
	}
	rest := body[idx+len("Try again in"):]
	// 截取到下一个引号或换行
	end := len(rest)
	if i := strings.IndexAny(rest, "\"\n\r}"); i >= 0 {
		end = i
	}
	segment := strings.TrimSpace(rest[:end])
	return parseHumanDuration(segment)
}

// parseHumanDuration 解析 "17h 59m" / "2h" / "59m" / "30s" / "1d 2h" 之类的时长。
func parseHumanDuration(s string) time.Duration {
	if s == "" {
		return 0
	}
	var total time.Duration
	num := 0
	valid := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			num = num*10 + int(c-'0')
			valid = true
		case c == 'd':
			total += time.Duration(num) * 24 * time.Hour
			num, valid = 0, false
		case c == 'h':
			total += time.Duration(num) * time.Hour
			num, valid = 0, false
		case c == 'm' && i+1 < len(s) && s[i+1] == 's':
			total += time.Duration(num) * time.Millisecond
			num, valid = 0, false
			i++
		case c == 'm':
			total += time.Duration(num) * time.Minute
			num, valid = 0, false
		case c == 's':
			total += time.Duration(num) * time.Second
			num, valid = 0, false
		case c == ' ':
			// 分隔符
		default:
			// 未知字符，重置
			num, valid = 0, false
		}
	}
	if total <= 0 {
		return 0
	}
	_ = valid
	return total
}

// parseRetryAfter 解析 HTTP Retry-After 头（秒数或 HTTP 日期）。
func parseRetryAfter(header string) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0
	}
	// 尝试秒数
	if secs, err := parseIntSafe(header); err == nil {
		return time.Duration(secs) * time.Second
	}
	// 尝试 HTTP 日期
	if t, err := http.ParseTime(header); err == nil {
		d := time.Until(t)
		if d > 0 {
			return d
		}
	}
	return 0
}

func parseIntSafe(s string) (int, error) {
	var n int
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, fmt.Errorf("not a number")
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, nil
}
