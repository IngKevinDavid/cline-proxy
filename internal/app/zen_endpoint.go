package app

import (
	"encoding/json"
	"log"
	"os"
	"strings"

	"cline-go-proxy/internal/kit"
)

// ============ zen 端点自适应（endpoint auto-learn） ============
//
// 背景：zen 免费层每个模型只服务一个原生端点（/chat/completions 或
// /v1/responses；官方 CLI 按模型分别只发其中之一）。网关事先不知道新模型的
// 端点——公共目录无该信号（29 个免费模型中 23 个无 provider 字段），唯一可靠
// 信号是上游拒绝本身：走错端点时上游报 500/400 + 端点错误特征。
//
// 机制：Upstream=="" 的模型先走 chat/completions（默认路径，零探测开销）；
// 若上游按"走错端点"模式拒绝，learnZenEndpoint 把该模型记为 responses 并
// 持久化（DATA_DIR/.zen-endpoints.json），本次请求直接改走原生 responses
// 路径重试。学习结果跨重启保留，后续请求零开销直达。
//
// 已知 responses 模型（muse-spark，Upstream="responses"）不受影响：直达原生
// 路径，无探测、无重试。
//
// 误判防护：只有"端点错误特征"才学习——普通 403/429/配额/超载不触发；
// 学错方向时 chat 路径的下一次请求会再次 500？不——一旦记为 responses 就不再
// 走 chat。纠错口：若 responses 路径报"走错端点"特征（如未来出现反向模型），
// learn 回 chat。见 handleZenResponsesNative 内的反向分支。

// zenEndpointFile 端点学习结果持久化路径。
func zenEndpointFile() string {
	return kit.ResolveDataPath(".zen-endpoints.json")
}

// isWrongEndpoint 上游错误是否呈"走错端点"特征：
// 500（实测 spark 在 chat 端点上 500，body 是通用 "Internal server error"）
// 或 4xx 带端点/模型路由特征词。限流/配额/会话/超载一律排除（isRateLimited
// 先行）。502/503/504 视为瞬时网关/过载，不学习——它们会误把模型翻转并
// 持久化到错误端点。
func isWrongEndpoint(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	// 先排除：限流配额类 403/429 走正常重试/换 key，不做端点学习
	for _, kw := range []string{
		"freetier", "free tier", "rate limit", "ratelimit", "too many",
		"overloaded", "busy", "quota", "credit", "payment", "billing",
		"limit reached", "resourceexhausted", "session",
	} {
		if strings.Contains(msg, kw) {
			return false
		}
	}
	// 走错端点特征：精确的 500（错误串以 "zen api 500" 开头，见
	// callZenAPI/callZenResponsesAPI 的 reason 格式），或 4xx 带路由特征词。
	// 不做裸 "zen api 5" 子串匹配——那会把 502/503/504 也当成走错端点。
	if strings.HasPrefix(msg, "zen api 500") {
		return true
	}
	for _, kw := range []string{
		"not found", "no such", "unknown model", "unsupported",
		"invalid endpoint", "wrong endpoint", "use /v1/responses",
		"use /chat/completions", "endpoint",
	} {
		if strings.Contains(msg, kw) {
			return true
		}
	}
	return false
}

// isWrongEndpointResponses 反向特征：responses 路径上报"该用 chat"。
// 目前未观测到实例，保留纠错口。
func isWrongEndpointResponses(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, kw := range []string{
		"use /chat/completions", "use /v1/chat", "invalid_endpoint",
	} {
		if strings.Contains(msg, kw) {
			return true
		}
	}
	return false
}

// learnZenEndpoint 学习并持久化某模型的原生端点（"responses" 或 ""）。
func learnZenEndpoint(modelID, upstream string) {
	initZenModels()
	zenModelsMu.Lock()
	m, ok := zenModels[modelID]
	if ok && m != nil && m.Upstream != upstream {
		m.Upstream = upstream
		log.Printf("zen endpoint learned: model=%s upstream=%q (persisted)", modelID, upstream)
	}
	zenModelsMu.Unlock()
	saveZenEndpoints()
}

// loadZenEndpoints 启动时恢复学习结果（覆盖种子/目录的 Upstream）。
func loadZenEndpoints() {
	data, err := os.ReadFile(zenEndpointFile())
	if err != nil || len(data) == 0 {
		return
	}
	var learned map[string]string
	if err := json.Unmarshal(data, &learned); err != nil {
		log.Printf("zen endpoints parse failed, ignoring: %v", err)
		return
	}
	initZenModels()
	zenModelsMu.Lock()
	defer zenModelsMu.Unlock()
	n := 0
	for id, up := range learned {
		if m, ok := zenModels[id]; ok && m != nil && (up == "responses" || up == "") {
			if m.Upstream != up {
				m.Upstream = up
				n++
			}
		}
	}
	if n > 0 {
		log.Printf("zen endpoints loaded: %d learned override(s)", n)
	}
}

// saveZenEndpoints 持久化当前 Upstream 非空的学习结果。
func saveZenEndpoints() {
	zenModelsMu.RLock()
	learned := map[string]string{}
	for id, m := range zenModels {
		if m != nil && m.Upstream == "responses" && m.Source != "seed" {
			learned[id] = m.Upstream
		}
	}
	zenModelsMu.RUnlock()
	data, err := json.MarshalIndent(learned, "", "  ")
	if err != nil {
		return
	}
	tmp := zenEndpointFile() + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		log.Printf("zen endpoints save failed: %v", err)
		return
	}
	if err := os.Rename(tmp, zenEndpointFile()); err != nil {
		log.Printf("zen endpoints save failed (rename): %v", err)
	}
}
