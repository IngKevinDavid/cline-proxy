package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"

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
// 误判防护（v2）：判定只看"上游按 HTTP 拒绝了"——具体状态码 + 响应体特征词，
// 绝不做整条错误文本的子串匹配。曾经的实现匹配任意错误串，而 DNS 失败的错误
// 文本是 "dial tcp: lookup ...: no such host"，命中关键词表里的 "no such" 后
// 会把一次瞬时断网固化成"该模型该走 responses"并持久化（目录同步不会改写既有
// 条目的 Upstream，不自愈）。纠错口：responses 路径若报反向特征，learn 回 chat。

// zenEndpointFile 端点学习结果持久化路径。
func zenEndpointFile() string {
	return kit.ResolveDataPath(".zen-endpoints.json")
}

// zenHTTPError zen 上游返回的非 2xx 答复（携带状态码）。
// 端点学习必须只依据"上游真的按 HTTP 拒绝了"，而不是错误文本——网络层错误
//（DNS 解析失败、拨号超时）的错误串里天然含 "no such host" 之类的词，靠子串
// 匹配会把一次瞬时断网永久固化成"该模型该走 responses"，且写进持久化文件后
// 目录同步不会自愈。类型化错误让判定只看状态码。
type zenHTTPError struct {
	Status int
	Body   string
}

func (e *zenHTTPError) Error() string {
	return fmt.Sprintf("zen API %d: %s", e.Status, e.Body)
}

// isWrongEndpoint 上游错误是否呈"走错端点"特征。
// 判定严格基于状态码：
//   - 500：实测 spark 在 chat 端点上就是裸 500（body 是通用 Internal server error），
//     这是唯一能识别该模型端点的信号，保留。
//   - 400/404/405/422：再看 body 是否带路由特征词。
//   - 其余（含 502/503/504 瞬时网关、429/403 限流会话、以及所有非 zenHTTPError
//     的网络层错误）：一律不学习。
func isWrongEndpoint(err error) bool {
	var he *zenHTTPError
	if !errors.As(err, &he) {
		return false // 网络层/序列化/取消等错误：与端点无关
	}
	if he.Status == http.StatusInternalServerError {
		return true
	}
	switch he.Status {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusUnprocessableEntity:
	default:
		return false
	}
	msg := strings.ToLower(he.Body)
	// 限流/配额/会话类 4xx 走正常重试/换 key，不做端点学习
	for _, kw := range []string{
		"freetier", "free tier", "rate limit", "ratelimit", "too many",
		"overloaded", "busy", "quota", "credit", "payment", "billing",
		"limit reached", "resourceexhausted", "session",
	} {
		if strings.Contains(msg, kw) {
			return false
		}
	}
	// 走错端点特征词（只看 4xx 的响应体，不再看任意错误文本）
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
// 同样只看带状态码的上游答复，避免网络错误误翻转。
func isWrongEndpointResponses(err error) bool {
	var he *zenHTTPError
	if !errors.As(err, &he) {
		return false
	}
	if he.Status != http.StatusBadRequest && he.Status != http.StatusNotFound && he.Status != http.StatusInternalServerError {
		return false
	}
	msg := strings.ToLower(he.Body)
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
// 与 applyZenCatalog 同样用副本替换：池内条目一旦发布就不再改写。
func learnZenEndpoint(modelID, upstream string) {
	initZenModels()
	zenModelsMu.Lock()
	m, ok := zenModels[modelID]
	if ok && m != nil && m.Upstream != upstream {
		next := *m
		next.Upstream = upstream
		zenModels[modelID] = &next
		log.Printf("zen endpoint learned: model=%s upstream=%q (persisted)", modelID, upstream)
	}
	zenModelsMu.Unlock()
	saveZenEndpoints()
}

// loadZenEndpointsFile 读取学习结果文件（空/损坏 → nil）。
func loadZenEndpointsFile() map[string]string {
	data, err := os.ReadFile(zenEndpointFile())
	if err != nil || len(data) == 0 {
		return nil
	}
	var learned map[string]string
	if err := json.Unmarshal(data, &learned); err != nil {
		log.Printf("zen endpoints parse failed, ignoring: %v", err)
		return nil
	}
	return learned
}

// applyZenEndpoints 把学习结果敷用到模型表上（调用方不持锁）。返回生效条数。
func applyZenEndpoints(learned map[string]string) int {
	if len(learned) == 0 {
		return 0
	}
	initZenModels()
	zenModelsMu.Lock()
	defer zenModelsMu.Unlock()
	n := 0
	for id, up := range learned {
		if up != "responses" && up != "" {
			continue
		}
		m, ok := zenModels[id]
		if !ok || m == nil || m.Upstream == up {
			continue
		}
		next := *m
		next.Upstream = up
		zenModels[id] = &next
		n++
	}
	return n
}

// loadZenEndpoints 启动时恢复学习结果（覆盖种子/目录的 Upstream）。
func loadZenEndpoints() {
	learned := loadZenEndpointsFile()
	if n := applyZenEndpoints(learned); n > 0 {
		log.Printf("zen endpoints loaded: %d learned override(s)", n)
	}
}

// reapplyLearnedEndpoints 目录同步后重新敷用学习结果。
//
// 启动时 loadZenEndpoints 只覆盖当时已存在的条目：模型目录是首次同步才填进来的
//（启动顺序上刷新协程与 loadZenEndpoints 并行），同步新增/重建的条目不在视野里。
// 不同步后补敷用，这些模型每次重启都要重新探测一遍端点。
func reapplyLearnedEndpoints() {
	learned := loadZenEndpointsFile()
	if n := applyZenEndpoints(learned); n > 0 {
		log.Printf("zen endpoints reapplied: %d learned override(s) after catalog sync", n)
	}
}

// saveZenEndpoints 持久化当前 Upstream 非空的学习结果。
//
// 串行化：learnZenEndpoint 由并发请求路径调用，且它在释放 zenModelsMu 之后才走到
// 这里。两个 goroutine 同时写同一个 .tmp 再 rename，后一次 rename 会对已不存在的
// tmp 报错，读到半个文件的 loadZenEndpoints 还会把整个学习表当损坏丢掉。
var zenEndpointSaveMu sync.Mutex

func saveZenEndpoints() {
	zenEndpointSaveMu.Lock()
	defer zenEndpointSaveMu.Unlock()

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
