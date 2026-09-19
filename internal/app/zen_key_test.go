package app

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// zen 探测（面板 per-key "Test" 按钮）的行为契约：
//   - 整个探测固定在被探测的 key 上（绝不中途换 key、绝不影响正常轮转）；
//   - 2xx  → active，且清除该 key 的冷却；
//   - 429  → cooldown，回报上游 Retry-After 决定的预计恢复时间；
//   - 403  → error（会话已死），收割机被顺带触发。
//
// 上游用 httptest 假服务（与 zen_learn_confirm_test.go 同一模式）。

func setupZenProbeTest(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	// 默认 ZEN_HARVEST_BIN (/app/bin/opencode) 在测试机上不存在，harvestEnabled()
	// 为 false 会让 harvestOnForbidden 在计数前就返回；指向测试二进制自身
	// （只做 os.Stat 存在性检查，503 计数路径不会真执行它）。
	if exe, err := os.Executable(); err == nil {
		t.Setenv("ZEN_HARVEST_BIN", exe)
	}
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)

	savedCfg := getZenConfig()
	cfgCopy := *savedCfg
	cfgCopy.Enabled = true
	cfgCopy.BaseURL = upstream.URL
	cfgCopy.Keys = []string{"sk-aaa111", "sk-bbb222", "sk-pin333"}
	cfgCopy.Proxies = nil
	cfgCopy.Retries = 0
	setZenConfig(&cfgCopy)
	t.Cleanup(func() { setZenConfig(savedCfg) })

	savedModels, savedAliases := zenModels, zenAliases
	setZenModelForTest("aaa-probe-model", "")
	t.Cleanup(func() {
		zenModelsMu.Lock()
		zenModels, zenAliases = savedModels, savedAliases
		zenModelsMu.Unlock()
	})
}

func TestUncoolZenKey(t *testing.T) {
	key := "sk-uncool-test"
	cooldownZenKey(key, time.Hour)
	if !zenKeyCooling(key) {
		t.Fatal("precondition: key should be cooling")
	}
	uncoolZenKey(key)
	if zenKeyCooling(key) {
		t.Fatal("uncoolZenKey did not clear the cooldown")
	}
	uncoolZenKey("") // 无害 no-op，不得 panic
}

func TestZenKeyTestProbeSuccessClearsCooldown(t *testing.T) {
	var hits int32
	setupZenProbeTest(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"OK"}}]}`))
	})
	key := "sk-aaa111"
	cooldownZenKey(key, time.Hour) // 预置冷却：探测成功必须把它清掉
	if !zenKeyCooling(key) {
		t.Fatal("precondition: key should be cooling")
	}

	result, status := testZenKey(key, 0)
	if status != "active" {
		t.Fatalf("status = %q (reason=%v), want active", status, result["reason"])
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("upstream hits = %d, want 1", hits)
	}
	if zenKeyCooling(key) {
		t.Fatal("successful probe did not clear the cooldown")
	}
	if got := result["model"]; got != "aaa-probe-model" {
		t.Fatalf("probe model = %v", got)
	}
	if result["latencyMs"] == nil {
		t.Fatal("latencyMs missing from result")
	}
}

func TestZenKeyTestProbe429ReportsCooldown(t *testing.T) {
	setupZenProbeTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"quota exceeded"}`))
	})
	key := "sk-bbb222"
	result, status := testZenKey(key, 1)
	if status != "cooldown" {
		t.Fatalf("status = %q, want cooldown", status)
	}
	if !zenKeyCooling(key) {
		t.Fatal("429 probe did not cool the key (cooldownZenKey side effect missing)")
	}
	until, ok := zenKeyCooldownUntil(key)
	if !ok {
		t.Fatal("cooldownUntil missing after 429 probe")
	}
	if rem := time.Until(until); rem < 110*time.Second || rem > 121*time.Second {
		t.Fatalf("cooldown remaining = %v, want ~2m (Retry-After: 120)", rem)
	}
	if result["cooldownUntil"] == nil || result["remaining"] == nil {
		t.Fatalf("result missing cooldown fields: %v", result)
	}
}

func TestZenKeyTestProbe403ReportsSessionDeadAndTriggersHarvest(t *testing.T) {
	setupZenProbeTest(t, func(w http.ResponseWriter, r *http.Request) {
		// 纯 FreeTier 403：body 不得含限流关键词（否则 isRateLimited 会把它
		// 当 429 分支处理，测不到会话死亡路径）
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"type":"free_tier_error","message":"session check failed"}}`))
	})
	key := "sk-aaa111"
	result, status := testZenKey(key, 0)
	if status != "error" {
		t.Fatalf("status = %q, want error", status)
	}
	if got := result["httpStatus"]; got != http.StatusForbidden {
		t.Fatalf("httpStatus = %v, want 403", got)
	}
	// harvestOnForbidden 是异步触发的（go ...），给它一点时间落账
	deadline := time.Now().Add(2 * time.Second)
	fails := 0
	for {
		harvestMu.Lock()
		fails = harvestFails[key]
		harvestMu.Unlock()
		if fails == 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if fails != 1 {
		t.Fatalf("harvest fail counter = %d, want 1 (probe must trigger the harvester)", fails)
	}
	harvestMu.Lock()
	delete(harvestFails, key)
	harvestMu.Unlock()
}

// pin 语义：429 时绝不换 key、绝不重试——一次探测恰好一次上游调用，且始终是
// 被探测的 key。非 pin 路径在同样输入下会切换到下一个 key（对照组）。
func TestZenCallPinKeepsSingleKey(t *testing.T) {
	var hits int32
	var seenKeys = map[string]int{}
	setupZenProbeTest(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		seenKeys[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]++
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"quota"}`))
	})

	params := map[string]any{
		"model":    "aaa-probe-model",
		"messages": []any{map[string]any{"role": "user", "content": "Reply with exactly: OK"}},
		"max_tokens": 64,
	}

	_, _, err := callZenAPI(t.Context(), params, true, zenCallOpts{pinKey: "sk-pin333"})
	var he *zenHTTPError
	if !errors.As(err, &he) || he.Status != http.StatusTooManyRequests {
		t.Fatalf("pinned probe err = %v, want zenHTTPError 429", err)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("pinned probe made %d upstream calls, want exactly 1 (no key switch, no retry)", hits)
	}
	if n := seenKeys["sk-pin333"]; n != 1 {
		t.Fatalf("pinned key usage = %d, want 1; seen=%v", n, seenKeys)
	}
	for k := range seenKeys {
		if k != "sk-pin333" {
			t.Fatalf("pinned probe touched another key: %q (seen=%v)", k, seenKeys)
		}
	}
}

func TestZenKeyTestHandlerRejectsBadIndex(t *testing.T) {
	setupZenProbeTest(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be called for an invalid index")
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/admin/api/zen/keys/test", strings.NewReader(`{"index":99}`))
	handleZenKeyTest(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("index out of range -> %d, want 404", rec.Code)
	}

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("POST", "/admin/api/zen/keys/test", strings.NewReader(`{invalid`))
	handleZenKeyTest(rec2, req2)
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("invalid JSON -> %d, want 400", rec2.Code)
	}
}
