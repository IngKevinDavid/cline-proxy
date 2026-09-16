package app

import (
	"bytes"
	"cline-go-proxy/internal/kit"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// ZenModel opencode zen 免费模型定义
type ZenModel struct {
	ID      string   `json:"id"`
	Aliases []string `json:"aliases,omitempty"`
	Context int      `json:"context"`
	Output  int      `json:"output"`
	Source  string   `json:"source"` // seed=内置 / synced=动态同步
}

// zenSeedModels 内置免费模型种子：仅收录 zen /v1/models 当前在线的免费模型。
// 曾在此但已从上游下线的模型（ling-3.0-flash-free / longcat-2.0-free /
// north-mini-code-free / laguna-s-2.1-free / big-pickle）已移除——syncZenModels
// 会同步上游在线列表并修剪掉线下模型，种子只作为首次启动/同步失败的兜底。
var zenSeedModels = []ZenModel{
	{"mimo-v2.5-free", []string{"mimo-v2.5", "mimo"}, 200000, 32000, "seed"},
	{"nemotron-3-ultra-free", []string{"nemotron-3-ultra", "nemotron"}, 1000000, 128000, "seed"},
	{"nemotron-3.5-lightning-free", []string{"nemotron-3.5-lightning", "nemotron-lightning"}, 200000, 32768, "seed"},
	{"ling-3.0-flash-fin-free", []string{"ling-3.0-flash-fin", "ling-fin", "ling"}, 200000, 32768, "seed"},
	{"deepseek-v4-flash-free", []string{"deepseek-v4-flash", "deepseek-v4"}, 200000, 128000, "seed"},
	{"muse-spark-1.3-contributor-free", []string{"muse-spark-contributor"}, 200000, 32768, "seed"},
	{"muse-spark-1.2-contributor-free", []string{"muse-spark"}, 200000, 32768, "seed"},
}

var (
	zenModelsMu sync.RWMutex
	zenModels   = make(map[string]*ZenModel) // 主表:ID
	zenAliases  = make(map[string]*ZenModel) // 别名表
)

const zenAPIBase = "https://opencode.ai/zen/v1"

func initZenModels() {
	zenModelsMu.Lock()
	defer zenModelsMu.Unlock()
	if len(zenModels) > 0 {
		return
	}
	for _, m := range zenSeedModels {
		cp := m
		zenModels[cp.ID] = &cp
		for _, a := range cp.Aliases {
			zenAliases[a] = &cp
		}
	}
}

// resolveZenModel 解析模型名到 zen 模型。支持 "opencode/<id>" 前缀与别名。
// 别名优先: 同步来的付费同名模型(如 deepseek-v4-flash)不会覆盖 free 别名解析。
func resolveZenModel(id string) (*ZenModel, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, false
	}
	zenModelsMu.RLock()
	defer zenModelsMu.RUnlock()
	if m, ok := zenAliases[id]; ok {
		return m, true
	}
	if strings.HasPrefix(id, "opencode/") {
		short := strings.TrimPrefix(id, "opencode/")
		if m, ok := zenAliases[short]; ok {
			return m, true
		}
		if m, ok := zenModels[short]; ok {
			return m, true
		}
	}
	if m, ok := zenModels[id]; ok {
		return m, true
	}
	return nil, false
}

// isZenFreeModel 免费判定: seed 白名单 或 ID 带 -free 后缀
func isZenFreeModel(m *ZenModel) bool {
	if m == nil {
		return false
	}
	return m.Source == "seed" || strings.HasSuffix(m.ID, "-free")
}

// resolveZenFreeModel 只解析免费 zen 模型
func resolveZenFreeModel(id string) (*ZenModel, bool) {
	m, ok := resolveZenModel(id)
	if !ok || !isZenFreeModel(m) {
		return nil, false
	}
	return m, true
}

// routeModel 决定请求走哪个上游: "zen" / "cline" / "reject"
// zen 免费模型 -> zen; zen 付费模型 -> reject(400); 其他 -> cline
// 故障转移: zen 连续失败期间,zen 免费模型请求临时路由到 cline 账号池
func routeModel(id string) string {
	id = strings.TrimSpace(id)
	// combo 别名兜底：调用方通常已把 model 改写为 target，这里防止 combo ID
	// 直接进入路由（按 combo 声明的平台走，不解析为普通模型）
	if c := resolveCombo(id); c != nil {
		return c.Platform
	}
	initZenModels()
	cfg := getZenConfig()
	if zm, ok := resolveZenModel(id); ok {
		if isZenFreeModel(zm) {
			// 与 cline 模型表冲突时(几乎不可能)走 cline
			initModelsCache()
			modelsMu.Lock()
			_, inCline := modelsCache[id]
			modelsMu.Unlock()
			if !inCline {
				if cfg.Failover && zenFailedNow() {
					log.Printf("  failover: zen degraded, %q routed to cline pool", id)
					return "cline"
				}
				return "zen"
			}
		} else {
			return "reject"
		}
	}
	if strings.HasPrefix(id, "opencode/") {
		short := strings.TrimPrefix(id, "opencode/")
		if zm, ok := resolveZenModel(short); ok {
			if isZenFreeModel(zm) {
				if cfg.Failover && zenFailedNow() {
					log.Printf("  failover: zen degraded, %q routed to cline pool", id)
					return "cline"
				}
				return "zen"
			}
			return "reject"
		}
	}
	return "cline"
}

// ============ zen 配置 ============

type zenCompactConfig struct {
	Auto         bool   `json:"auto"`         // 官方风格摘要压缩开关
	Buffer       int    `json:"buffer"`       // 预留输出缓冲 token,默认 20000
	KeepTokens   int    `json:"keepTokens"`   // 尾部保留 token 预算,默认 8000
	SummaryModel string `json:"summaryModel"` // 摘要模型,空=用请求模型
	MaxSummary   int    `json:"maxSummary"`   // 摘要最大输出 token,默认 4096
}

type zenConfigData struct {
	Enabled         bool             `json:"enabled"`
	Key             string           `json:"key"`            // 兼容字段：始终等于 Keys[0]（旧版单 key 读取用）
	Keys            []string         `json:"keys,omitempty"` // zen 多 key 池，请求按 round-robin 轮转
	BaseURL         string           `json:"baseURL"`
	Proxies         []string         `json:"proxies"`         // http(s)/socks5 代理,轮询出口
	ProxyStrategy   string           `json:"proxyStrategy"`   // round_robin / random / fill
	MaxConcurrency  int              `json:"maxConcurrency"`  // zen 上游最大并发,防 worker 瞬时超限,默认 8
	Retries         int              `json:"retries"`         // 限流/网络错误重试次数,默认 3
	Failover        bool             `json:"failover"`        // zen 连续失败后故障转移到 cline 账号池,默认 true
	FailoverCount   int              `json:"failoverCount"`   // 触发故障转移的连续失败次数,默认 3
	FailoverMinutes int              `json:"failoverMinutes"` // 故障转移窗口(分钟),默认 5
	Compaction      zenCompactConfig `json:"compaction"`
}

func defaultZenConfig() *zenConfigData {
	return &zenConfigData{
		Enabled:         true,
		Key:             "public",
		Keys:            []string{"public"},
		BaseURL:         zenAPIBase,
		ProxyStrategy:   "round_robin",
		MaxConcurrency:  8,
		Retries:         3,
		Failover:        true,
		FailoverCount:   3,
		FailoverMinutes: 5,
		Compaction: zenCompactConfig{
			Auto:       true,
			Buffer:     20000,
			KeepTokens: 8000,
			MaxSummary: 4096,
		},
	}
}

var (
	zenConfig   = loadZenConfig()
	zenConfigMu sync.Mutex
)

// ============ 限流防御状态机 ============

var (
	zenSem       chan struct{} // 并发信号量
	zenFailCount int           // 连续失败计数
	zenFailUntil time.Time     // 故障转移截止时间
	zenStateMu   sync.Mutex
)

func init() {
	rebuildZenSem()
}

func rebuildZenSem() {
	cfg := getZenConfig()
	n := cfg.MaxConcurrency
	if n <= 0 {
		n = 8
	}
	zenStateMu.Lock()
	zenSem = make(chan struct{}, n)
	zenStateMu.Unlock()
}

func markZenSuccess() {
	zenStateMu.Lock()
	zenFailCount = 0
	zenFailUntil = time.Time{}
	zenStateMu.Unlock()
}

func markZenFail() {
	cfg := getZenConfig()
	thr := cfg.FailoverCount
	if thr <= 0 {
		thr = 3
	}
	window := cfg.FailoverMinutes
	if window <= 0 {
		window = 5
	}
	zenStateMu.Lock()
	zenFailCount++
	if zenFailCount >= thr {
		zenFailUntil = time.Now().Add(time.Duration(window) * time.Minute)
	}
	zenStateMu.Unlock()
}

// zenFailedNow zen 是否处于故障转移状态
func zenFailedNow() bool {
	zenStateMu.Lock()
	defer zenStateMu.Unlock()
	if zenFailUntil.IsZero() {
		return false
	}
	if time.Now().After(zenFailUntil) {
		zenFailCount = 0
		zenFailUntil = time.Time{}
		return false
	}
	return true
}

// isRateLimited 限流信号识别: 429/503 直接命中; 502/403 按错误体关键词
func isRateLimited(status int, body string) bool {
	if status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable {
		return true
	}
	if status == http.StatusBadGateway || status == http.StatusForbidden {
		low := strings.ToLower(body)
		for _, kw := range []string{"resourceexhausted", "limit reached", "rate limit", "too many", "overloaded", "busy"} {
			if strings.Contains(low, kw) {
				return true
			}
		}
	}
	return false
}

// normalizeZenKeys 规范 key 池：迁移旧单 key 字段、去空去重、回退默认 "public"、
// 同步兼容字段 Key = Keys[0]。
func normalizeZenKeys(cfg *zenConfigData) {
	if len(cfg.Keys) == 0 && cfg.Key != "" {
		cfg.Keys = []string{cfg.Key}
	}
	cleaned := make([]string, 0, len(cfg.Keys))
	seen := map[string]bool{}
	for _, k := range cfg.Keys {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		cleaned = append(cleaned, k)
	}
	if len(cleaned) == 0 {
		cleaned = []string{"public"}
	}
	cfg.Keys = cleaned
	cfg.Key = cleaned[0]
}

func loadZenConfig() *zenConfigData {
	path := kit.ResolveDataPath(".zen-config.json")
	cfg := defaultZenConfig()
	fileExists := false
	if data, err := os.ReadFile(path); err == nil {
		fileExists = true
		if err := json.Unmarshal(data, cfg); err != nil {
			log.Printf("zen config parse failed: %v", err)
		}
	}
	// ZEN_KEYS 环境变量：配置为空（无文件或只有默认 public key）时注入多 key 池
	if envKeys := envList("ZEN_KEYS"); len(envKeys) > 0 {
		if !fileExists || len(cfg.Keys) == 0 || (len(cfg.Keys) == 1 && cfg.Keys[0] == "public") {
			cfg.Keys = envKeys
			log.Printf("zen keys seeded from ZEN_KEYS env: %d key(s)", len(envKeys))
		}
	}
	normalizeZenKeys(cfg)
	if cfg.BaseURL == "" {
		cfg.BaseURL = zenAPIBase
	}
	return cfg
}

func saveZenConfig() {
	zenConfigMu.Lock()
	defer zenConfigMu.Unlock()
	data, _ := json.MarshalIndent(zenConfig, "", "  ")
	if err := os.WriteFile(kit.ResolveDataPath(".zen-config.json"), data, 0600); err != nil {
		log.Printf("zen config save failed: %v", err)
	}
}

func getZenConfig() *zenConfigData {
	zenConfigMu.Lock()
	defer zenConfigMu.Unlock()
	return zenConfig
}

func setZenConfig(c *zenConfigData) {
	normalizeZenKeys(c)
	zenConfigMu.Lock()
	zenConfig = c
	zenConfigMu.Unlock()
	saveZenConfig()
	rebuildZenTransport()
	rebuildZenSem()
	// 清理已移除 key 的轮转状态
	valid := map[string]bool{}
	for _, k := range c.Keys {
		valid[k] = true
	}
	zenKeyMu.Lock()
	for k := range zenKeyCool {
		if !valid[k] {
			delete(zenKeyCool, k)
		}
	}
	for k := range zenKeyUsage {
		if !valid[k] {
			delete(zenKeyUsage, k)
		}
	}
	zenKeyIdx = 0
	zenKeyMu.Unlock()
}

// validateProxyList 校验代理列表格式: 支持 http/https/socks5/socks5h, 必须包含 host:port。
func validateProxyList(proxies []string) error {
	for _, p := range proxies {
		line := strings.TrimSpace(p)
		if line == "" {
			continue
		}
		u, err := url.Parse(line)
		if err != nil {
			return fmt.Errorf("代理格式无效 %q: %v", line, err)
		}
		switch u.Scheme {
		case "http", "https", "socks5", "socks5h":
		default:
			return fmt.Errorf("代理 %q 协议不受支持（支持 http/https/socks5/socks5h）", line)
		}
		if u.Host == "" {
			return fmt.Errorf("代理 %q 缺少 host:port", line)
		}
		if _, _, err := net.SplitHostPort(u.Host); err != nil {
			return fmt.Errorf("代理 %q 缺少端口: %v", line, err)
		}
	}
	return nil
}

// ============ zen 多 key 轮转（round_robin 默认策略） ============

var (
	zenKeyMu    sync.Mutex
	zenKeyIdx   int
	zenKeyUsage = map[string]int64{} // 每 key 成功调用计数（内存态）
	zenKeyCool  = map[string]time.Time{}
)

// pickZenKey round-robin 选取一个未冷却的 key；全部冷却时按轮转顺序返回下一个。
// 返回 "" 表示当前没有配置任何 key。
func pickZenKey() string {
	keys := getZenConfig().Keys
	if len(keys) == 0 {
		return ""
	}
	zenKeyMu.Lock()
	defer zenKeyMu.Unlock()
	now := time.Now()
	// 先清理过期冷却
	for k, until := range zenKeyCool {
		if now.After(until) {
			delete(zenKeyCool, k)
		}
	}
	for i := 0; i < len(keys); i++ {
		idx := (zenKeyIdx + i) % len(keys)
		k := keys[idx]
		if _, cooling := zenKeyCool[k]; !cooling {
			zenKeyIdx = (idx + 1) % len(keys)
			zenKeyUsage[k]++
			return k
		}
	}
	// 全部冷却中：仍按轮转返回（保持请求流动，上游会再次限流）
	k := keys[zenKeyIdx%len(keys)]
	zenKeyIdx = (zenKeyIdx + 1) % len(keys)
	zenKeyUsage[k]++
	return k
}

// cooldownZenKey 将 key 置为冷却，冷却期内 round-robin 跳过它。
// 默认的 "public" key 仅在它是池中唯一 key 时跳过冷却（无其他 key 可轮转）；
// 多 key 池中 "public" 也参与冷却轮转。
func cooldownZenKey(key string, d time.Duration) {
	if key == "" {
		return
	}
	if key == "public" && len(getZenConfig().Keys) <= 1 {
		return
	}
	if d <= 0 {
		d = time.Minute
	}
	if d > maxCooldown {
		d = maxCooldown
	}
	zenKeyMu.Lock()
	zenKeyCool[key] = time.Now().Add(d)
	zenKeyMu.Unlock()
}

// zenKeyCooling 查询 key 是否处于冷却期。
func zenKeyCooling(key string) bool {
	zenKeyMu.Lock()
	defer zenKeyMu.Unlock()
	until, ok := zenKeyCool[key]
	if !ok {
		return false
	}
	if time.Now().After(until) {
		delete(zenKeyCool, key)
		return false
	}
	return true
}

// zenKeyStatus 每个 key 的运行时状态（管理面板展示用，key 值打码）。
func zenKeyStatus() []map[string]any {
	keys := getZenConfig().Keys
	zenKeyMu.Lock()
	defer zenKeyMu.Unlock()
	now := time.Now()
	out := make([]map[string]any, 0, len(keys))
	for i, k := range keys {
		st := map[string]any{
			"index":   i,
			"keyMask": kit.Truncate(k, 8) + "…",
			"usage":   zenKeyUsage[k],
			"current": i == zenKeyIdx%maxInt(len(keys), 1),
		}
		if until, cooling := zenKeyCool[k]; cooling && now.Before(until) {
			st["cooling"] = true
			st["cooldownUntil"] = until.Format(time.RFC3339)
		} else {
			st["cooling"] = false
		}
		out = append(out, st)
	}
	return out
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ============ zen 上游调用 ============

// buildZenBody 构造 zen 请求体:只带 OpenAI 兼容字段,改写模型为 zen ID
func buildZenBody(params map[string]any, stream bool) map[string]any {
	body := map[string]any{}
	for _, key := range passThroughKeys {
		if val, ok := params[key]; ok {
			body[key] = val
		}
	}
	for _, key := range []string{"model", "messages", "max_tokens", "max_completion_tokens", "stream"} {
		if val, ok := params[key]; ok {
			body[key] = val
		}
	}
	if stream {
		body["stream"] = true
	}
	if model, ok := params["model"].(string); ok {
		if m, ok := resolveZenModel(model); ok {
			body["model"] = m.ID
		}
	}
	delete(body, "reasoning_effort")
	delete(body, "reasoningEffort")
	enforceToolChoiceNone(body)
	return body
}

// callZenAPI 调用 zen 上游,带限流防御: 并发信号量 + 指数退避重试 + 代理冷却 + 故障计数
// 返回 (响应, 命中限流次数, 错误)。
// ctx 来自客户端请求: 客户端取消（IDE abort）时立即终止上游调用,不重试、
// 不计数、不冷却任何 key/代理 —— 客户端行为不会污染限流状态。
func callZenAPI(ctx context.Context, params map[string]any, stream bool) (*http.Response, int, error) {
	cfg := getZenConfig()
	body := buildZenBody(params, stream)

	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal zen body: %w", err)
	}

	endpoint := strings.TrimRight(cfg.BaseURL, "/") + "/chat/completions"

	// 先取信号量再等待：不能在持有 zenStateMu 时阻塞在 channel 上，
	// 否则并发打满时（in-flight 调用要拿 zenStateMu 记成功/失败才能释放槽位）
	// 会形成确定性死锁。ctx 取消（客户端 abort）时不再占用槽位。
	zenStateMu.Lock()
	sem := zenSem
	zenStateMu.Unlock()
	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		return nil, 0, fmt.Errorf("client aborted: %w", ctx.Err())
	}
	defer func() { <-sem }()

	retries := cfg.Retries
	if retries <= 0 {
		retries = 3
	}
	delay := time.Second
	rateLimited := 0

	for attempt := 0; ; attempt++ {
		// 代理轮转（round_robin 默认）: 每次上游尝试显式挑选出口,
		// 冷却中的代理被跳过; 未配置代理时直连。
		proxyURL, pidx := pickUpstreamProxy()
		viaProxy := "direct"
		if proxyURL != "" {
			viaProxy = maskProxyURL(proxyURL)
		}
		req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(bodyJSON))
		if err != nil {
			return nil, rateLimited, fmt.Errorf("create zen request: %w", err)
		}
		// 客户端身份轮换: 每次请求模拟全新 opencode 客户端,规避 session/UA 维度限流
		sess, user, ua := kit.FreshZenIdentity()
		// 多 key 轮转: round-robin 选取未冷却的 key（单 key 时行为不变）
		key := pickZenKey()
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", ua)
		req.Header.Set("x-opencode-session", sess)
		req.Header.Set("x-opencode-request", user)
		req.Header.Set("x-opencode-client", "cli")

		model, _ := params["model"].(string)
		if m, ok := resolveZenModel(model); ok {
			req.Header.Set("x-opencode-model", m.ID)
		}
		log.Printf("  zen upstream: model=%s stream=%v msgs=%d via=%s key=#%d attempt=%d session=%s",
			body["model"], stream, getMsgCount(params), viaProxy, keyIndex(key), attempt+1, kit.Truncate(sess, 24))

		resp, err := proxyClientFor(proxyURL).Do(req)
		if err != nil {
			// 客户端取消: 立即返回,不重试不冷却不计故障
			if ctx.Err() != nil {
				return nil, rateLimited, fmt.Errorf("client aborted: %w", err)
			}
			// 隧道层失败才冷却出口: 拨号/握手/连接被重置。冷却从 5m 缩到 2m ——
			// 上游过载时 CF 重置连接的表现与死代理相同,5~10 分钟的冷却会让
			// 几次慢模型测试就毒化整个池;2 分钟仍能跳过真死代理,又快速自愈。
			if pidx >= 0 {
				cooldownUpstreamProxy(pidx, 2*time.Minute)
				log.Printf("  zen proxy failed (%v), cooldown exit %s for 2m", err, viaProxy)
			}
			// 网络错误:退避重试(不计入故障转移,瞬时可恢复);ctx 取消时中断等待
			if attempt < retries {
				log.Printf("  zen network error (%v), retry %d/%d after %v", err, attempt+1, retries, delay)
				if !sleepCtx(ctx, kit.WithRetryJitter(delay)) {
					return nil, rateLimited, fmt.Errorf("client aborted during retry wait")
				}
				delay *= 2
				continue
			}
			return nil, rateLimited, fmt.Errorf("zen request: %w", err)
		}
		if resp.StatusCode == http.StatusOK {
			markZenSuccess()
			return resp, rateLimited, nil
		}

		bodyBytes := kit.ReadBody(resp)
		resp.Body.Close()
		reason := fmt.Sprintf("zen API %d: %s", resp.StatusCode, kit.Truncate(bodyBytes, 500))

		if isRateLimited(resp.StatusCode, bodyBytes) {
			rateLimited++
			// 不冷却出口代理: 代理成功送达了 HTTP 响应,它没有故障。限流是 zen
			// 对 key/身份/IP 组合的判定,把出口毒化 10 分钟只会让上游繁忙期
			// (慢模型 503/429)把整个池打瘫;下一次尝试的轮转自然换到下一出口,
			// key 冷却 + 轮转已足够分摊负载。
			// 冷却当前 key；若还有其他未冷却 key 则立即切换重试（不睡眠）
			rl := parseRetryAfter(resp.Header.Get("Retry-After"))
			cooldownZenKey(key, rl)
			if next := pickZenKey(); next != "" && next != key && !zenKeyCooling(next) {
				log.Printf("  zen rate limited (%d), switching to next zen key (%d configured)", resp.StatusCode, len(cfg.Keys))
				continue
			}
			if attempt < retries {
				// Retry-After 封顶 30s：更长的等待没有意义（槽位被占、客户端早已离开）
				wait := delay
				if retryAfter := parseRetryAfter(resp.Header.Get("Retry-After")); retryAfter > wait {
					wait = retryAfter
				}
				if wait > 30*time.Second {
					wait = 30 * time.Second
				}
				log.Printf("  zen rate limited (%d), retry %d/%d after %v", resp.StatusCode, attempt+1, retries, wait)
				if !sleepCtx(ctx, kit.WithRetryJitter(wait)) {
					return nil, rateLimited, fmt.Errorf("client aborted during retry wait")
				}
				delay *= 2
				continue
			}
			markZenFail()
			return nil, rateLimited, fmt.Errorf("%s", reason)
		}

		// 非 2xx：只有限流信号或服务端错误才推进全局故障转移，
		// 客户端侧 400/401（提示词超限、key 配错）不应污染 failover 状态
		if resp.StatusCode >= 500 {
			markZenFail()
		}
		return nil, rateLimited, fmt.Errorf("%s", reason)
	}
}

// sleepCtx ctx 感知的睡眠；返回 false 表示等待期间被取消。
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// keyIndex key 在池中的序号（日志用）
func keyIndex(key string) int {
	for i, k := range getZenConfig().Keys {
		if k == key {
			return i + 1
		}
	}
	return 0
}

func zenModelList() []map[string]any {
	initZenModels()
	zenModelsMu.RLock()
	out := make([]map[string]any, 0, len(zenModels))
	for _, m := range zenModels {
		if !isZenFreeModel(m) {
			continue
		}
		cp := *m
		out = append(out, map[string]any{
			"id":      cp.ID,
			"context": cp.Context,
			"output":  cp.Output,
			"source":  cp.Source,
		})
	}
	zenModelsMu.RUnlock()
	return out
}

// syncZenModels 拉取 zen /v1/models,动态合并到模型表
func syncZenModels() (int, error) {
	initZenModels()
	cfg := getZenConfig()
	endpoint := strings.TrimRight(cfg.BaseURL, "/") + "/models"
	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Key)
	client := &http.Client{Timeout: 25 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return 0, err
	}

	zenModelsMu.Lock()
	defer zenModelsMu.Unlock()
	added := 0
	live := make(map[string]bool, len(payload.Data))
	for _, item := range payload.Data {
		id := item.ID
		if id == "" {
			continue
		}
		live[id] = true
		if _, ok := zenModels[id]; ok {
			continue
		}
		// 跳过与免费模型别名冲突的 ID(如付费的 deepseek-v4-flash),保证别名解析不被覆盖
		if _, conflict := zenAliases[id]; conflict {
			continue
		}
		// 新模型:默认按 200K 上下文接入,输出按 32K
		zenModels[id] = &ZenModel{
			ID:      id,
			Context: 200000,
			Output:  32768,
			Source:  "synced",
		}
		added++
	}
	// 修剪已从上游下线的 synced 模型，避免 /v1/models 长期展示死模型。
	// 种子模型是手工维护的兜底，不在修剪范围内。
	pruned := 0
	for id, m := range zenModels {
		if m.Source == "synced" && !live[id] {
			delete(zenModels, id)
			pruned++
		}
	}
	if pruned > 0 {
		log.Printf("zen model sync: pruned %d model(s) no longer on upstream feed", pruned)
	}
	return added, nil
}

// startZenModelsRefresher 定时同步 zen 模型列表(默认 10 分钟)
func startZenModelsRefresher() {
	go func() {
		if _, err := syncZenModels(); err != nil {
			log.Printf("zen model sync: failed (%v), using seed list", err)
		}
		ticker := time.NewTicker(10 * time.Minute)
		for range ticker.C {
			cfg := getZenConfig()
			if !cfg.Enabled {
				continue
			}
			if added, err := syncZenModels(); err != nil {
				log.Printf("zen model sync: failed (%v)", err)
			} else if added > 0 {
				log.Printf("zen model sync: %d new models from official feed", added)
			}
		}
	}()
}
