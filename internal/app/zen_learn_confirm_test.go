package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
)

// 端点学习必须"重试成功才落盘"。
//
// 裸 500 既是"走错端点"的信号，也是上游瞬时故障的形态（实测 muse-spark 在
// 正确的 responses 端点上也会偶发 {"error":"Internal server error"}）。旧实现
// 一见 500 就 learnZenEndpoint 并持久化：瞬时故障会把 chat 原生模型永久翻转到
// responses —— 目录同步不改写既有条目的 Upstream，只能人工删文件恢复。
func TestEndpointLearnOnlyPersistsWhenRetrySucceeds(t *testing.T) {
	// 不用 t.TempDir()：统计写入会一直占着 $DATA_DIR/zen-stats.jsonl，Windows 上
	// TempDir 的自动清理会因句柄未释放而失败（与测试断言无关的假失败）。
	dir, err := os.MkdirTemp("", "zenlearn")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATA_DIR", dir)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	// 假 zen 上游：/chat/completions 一律 500；/responses 由开关控制成败
	var responsesOK atomic.Bool
	var responsesHits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat/completions":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"error","message":"Internal server error"}}`))
		case "/responses":
			atomic.AddInt32(&responsesHits, 1)
			if !responsesOK.Load() {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"type":"error","error":{"type":"error","message":"Internal server error"}}`))
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}}` + "\n\n"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	savedCfg := getZenConfig()
	cfg := savedCfg
	cfg.BaseURL = upstream.URL
	cfg.Keys = []string{"probe-zen-key"}
	cfg.Proxies = nil
	cfg.Enabled = true
	cfg.Retries = 0
	setZenConfig(cfg)
	defer setZenConfig(savedCfg)

	model := "audit-probe-chat-native"
	// 只注册这一个模型，避免污染其他测试的模型表
	savedModels := zenModels
	savedAliases := zenAliases
	setZenModelForTest(model, "")
	defer func() {
		zenModelsMu.Lock()
		zenModels, zenAliases = savedModels, savedAliases
		zenModelsMu.Unlock()
	}()

	params := map[string]any{
		"model":    model,
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		"stream":   false,
	}
	zm, ok := resolveZenModel(model)
	if !ok {
		t.Fatal("test model not registered")
	}

	// 情形 1：responses 也失败 → 绝不能学习（这是瞬时故障，不是端点信号）
	responsesOK.Store(false)
	rec := &flushRecorder{header: http.Header{}}
	if handled := handleZenResponsesNative(rec, httptest.NewRequest("POST", "/v1/chat/completions", nil), params, zm, newZenStatsTracker(zenStatsRecord{})); handled {
		t.Fatal("a failed retry must not be reported as handled")
	}
	// 直接验证"chat 500 + responses 500"这一轮的完整路径
	rec2 := &flushRecorder{header: http.Header{}}
	handleZenChat(rec2, httptest.NewRequest("POST", "/v1/chat/completions", nil), params)
	if got := currentZenUpstream(model); got == "responses" {
		t.Fatalf("transient 500 must not flip the model to responses (upstream=%q)", got)
	}
	if learned := loadZenEndpointsFile(); learned[model] != "" {
		t.Fatalf("transient 500 must not be persisted: %v", learned)
	}
	firstHits := atomic.LoadInt32(&responsesHits)
	if firstHits == 0 {
		t.Fatal("the retry should still have been attempted")
	}

	// 情形 2：responses 真的能服务 → 这时才学习并持久化
	responsesOK.Store(true)
	rec3 := &flushRecorder{header: http.Header{}}
	handleZenChat(rec3, httptest.NewRequest("POST", "/v1/chat/completions", nil), params)
	if got := currentZenUpstream(model); got != "responses" {
		t.Fatalf("a confirmed working responses endpoint must be learned (upstream=%q)", got)
	}
	if learned := loadZenEndpointsFile(); learned[model] != "responses" {
		t.Fatalf("confirmed endpoint not persisted: %v", learned)
	}
}

// setZenModelForTest 在测试里挂一个独立的模型表。
func setZenModelForTest(id, upstream string) {
	zenModelsMu.Lock()
	zenModels = map[string]*ZenModel{id: {ID: id, Context: 200000, Output: 32768, Source: "live", Upstream: upstream}}
	zenAliases = map[string]*ZenModel{}
	zenModelsMu.Unlock()
}

// currentZenUpstream 测试辅助：读模型当前生效的端点。
func currentZenUpstream(id string) string {
	zenModelsMu.RLock()
	defer zenModelsMu.RUnlock()
	if m, ok := zenModels[id]; ok && m != nil {
		return m.Upstream
	}
	return ""
}
