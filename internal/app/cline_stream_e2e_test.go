package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// 这是 cline "必须流式"自学习的端到端回归测试：真实 callClineAPI（唯一的生产
// 调用路径）+ 本地假上游。
//
// 为什么必须有这一层：callClineAutoStream 的早先实现只在"callClineAPI 返回
// 500 响应"时才嗅探错误体，而 callClineAPI 对非 200 一律返回 nil 响应 +
// error（响应体已读尽关闭）。单测用一个假上游塞 (500 响应, nil error) —— 那是
// 生产里不可能出现的形态，于是测试全绿而功能在生产里永远不触发。
//
// 本测试固定住两件事：
//  1. callClineAPI 对非 200 返回 *clineAPIError（携带状态码 + body）；
//  2. callClineAutoStream 因此能真的学到该模型并改用 stream=true 重试。
func TestAutoStreamLearnThroughRealCallee(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	clineStreamMu.Lock()
	clineStreamLearned = nil
	clineStreamMu.Unlock()

	var sawStream atomic.Value
	var calls int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		stream, _ := body["stream"].(bool)
		sawStream.Store(stream)
		if !stream {
			// cline 对"必须流式"模型的非流式调用固定答复
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"empty response content","success":false}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()

	// 假上游接管上游基址；APIToken 账号不需要刷新（ensureAccountToken 直接
	// 返回静态 key），因此整个真实调用链无需网络。
	origBase := clineAPIBase
	clineAPIBase = upstream.URL
	defer func() { clineAPIBase = origBase }()

	poolMu.Lock()
	savedPool := pool
	pool = &AccountPool{Accounts: []*Account{{
		AccountID: "probe-1", Email: "probe@test", APIToken: "sk-test-static-key", Status: "active",
	}}}
	poolMu.Unlock()
	defer func() {
		poolMu.Lock()
		pool = savedPool
		poolMu.Unlock()
	}()

	params := map[string]any{
		"model":    "poolside/laguna-s-2.1:free", // 含 ":" → 命名约定判定"无需流式"
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	}

	// 前置条件：约定判定该模型不需要流式，否则测不到学习路径
	if modelNeedsStream("poolside/laguna-s-2.1:free") {
		t.Fatal("precondition failed: model is already force-streamed by the naming convention")
	}

	resp, _, streamed, err := callClineAutoStream(context.Background(), params, false, false)
	if err != nil {
		t.Fatalf("learn-and-retry should succeed, got err=%v", err)
	}
	if resp == nil {
		t.Fatal("no response returned")
	}
	defer resp.Body.Close()
	if !streamed {
		t.Fatal("streamed must be true so the caller aggregates the SSE body")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("upstream calls = %d, want 2 (non-stream probe + streamed retry)", got)
	}
	if s, _ := sawStream.Load().(bool); !s {
		t.Fatal("the retry must have used stream=true")
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "\"ok\"") {
		t.Fatalf("retry body not returned: %s", body)
	}
	if !clineStreamRequired("poolside/laguna-s-2.1:free") {
		t.Fatal("model must be learned as stream-required")
	}
	// 后续请求直接命中，无需再付一次非流式探测的代价
	if !modelNeedsStream("poolside/laguna-s-2.1:free") {
		t.Fatal("learned model must be force-streamed on later requests")
	}
}

// callClineAPI 的非 200 契约：nil 响应 + 携带状态码的类型化错误。
// 端点学习/流式学习都依赖它；这条断言是那两个功能的立足点。
func TestCallClineAPINon200ReturnsTypedError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"bad gateway"}`))
	}))
	defer upstream.Close()

	origBase := clineAPIBase
	clineAPIBase = upstream.URL
	defer func() { clineAPIBase = origBase }()

	poolMu.Lock()
	savedPool := pool
	pool = &AccountPool{Accounts: []*Account{{
		AccountID: "probe-2", Email: "probe2@test", APIToken: "sk-test-static-key", Status: "active",
	}}}
	poolMu.Unlock()
	defer func() {
		poolMu.Lock()
		pool = savedPool
		poolMu.Unlock()
	}()

	resp, _, err := callClineAPI(context.Background(), map[string]any{
		"model":    "z-ai/glm-5.3-flash",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	}, false, false)

	if resp != nil {
		t.Fatalf("non-200 must not hand back a response (body is consumed): %+v", resp)
	}
	if err == nil {
		t.Fatal("non-200 must be an error")
	}
	var apiErr *clineAPIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error must be *clineAPIError so callers can classify it by status, got %T", err)
	}
	if apiErr.Status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", apiErr.Status)
	}
	if !strings.Contains(apiErr.Body, "bad gateway") {
		t.Fatalf("body not preserved: %q", apiErr.Body)
	}
	// 502 是瞬时错误：绝不能触发端点学习
	if isWrongEndpoint(err) {
		t.Fatal("a 502 must never be classified as a wrong endpoint")
	}
}