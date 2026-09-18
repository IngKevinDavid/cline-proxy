package app

import (
	"bufio"
	"bytes"
	"cline-go-proxy/internal/kit"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ZenModel opencode zen 免费模型定义
type ZenModel struct {
	ID        string   `json:"id"`
	Aliases   []string `json:"aliases,omitempty"`
	Context   int      `json:"context"`
	Output    int      `json:"output"`
	Source    string   `json:"source"`               // seed=内置 / registry=公共目录同步 / synced=zen 上游同步
	Upstream  string   `json:"upstream,omitempty"`   // 强制原生上游端点: "responses"；空=默认 chat/completions
	ToolCall  bool     `json:"toolCall,omitempty"`   // 目录声明的 tool_call 能力
	Reasoning bool     `json:"reasoning,omitempty"`  // 目录声明的 reasoning 能力
	Attach    bool     `json:"attachment,omitempty"` // 目录声明的图片/附件输入能力
}

// zenSeedModels 内置免费模型种子：与官方 CLI `opencode models`
//（无认证也可用，见空 HOME 实测）列出的免费条目一致。
// 免费资格 = 公共目录 cost.input==0 && cost.output==0（big-pickle 这类
// 无 "free" 后缀的免费模型也因此入选；deepseek-v4-flash 这类
// status=deprecated 的条目即使 cost 为 0 也不入选）。
// union-alpha 是临时免费活动（下周才开放），不进种子——开放后同步层会
// 经价格门自动纳入，无需手动维护。
// 同步层（syncZenModels）以 zen 真源 membership + 目录价格/状态交叉校验为准，
// 种子只作为首次启动/同步失败的兜底。
var zenSeedModels = []ZenModel{
	{ID: "mimo-v2.5-free", Aliases: []string{"mimo-v2.5", "mimo"}, Context: 200000, Output: 32000, Source: "seed"},
	{ID: "nemotron-3-ultra-free", Aliases: []string{"nemotron-3-ultra", "nemotron"}, Context: 1000000, Output: 128000, Source: "seed"},
	{ID: "nemotron-3.5-lightning-free", Aliases: []string{"nemotron-3.5-lightning", "nemotron-lightning"}, Context: 262144, Output: 262144, Source: "seed"},
	{ID: "ling-3.0-flash-fin-free", Aliases: []string{"ling-3.0-flash-fin", "ling-fin", "ling"}, Context: 262144, Output: 32768, Source: "seed"},
	// big-pickle：zen 创始免费模型（无 free 后缀，目录 cost 0/0，1.18.31 实测可用；
	// opencode zen 免费层的默认别名，永久保留在种子中）
	{ID: "big-pickle", Aliases: []string{"pickle"}, Context: 200000, Output: 32000, Source: "seed"},
	// muse-spark 只在原生 /v1/responses 端点上可用：官方 opencode CLI 实测
	// 对该模型只发 POST /zen/v1/responses（带 tools + reason、返回 SSE），
	// chat/completions 上该模型 500（需经 responses 原生调用再转回 chat 形态）
	{ID: "muse-spark-1.3-contributor-free", Aliases: []string{"muse-spark-contributor"}, Context: 1048576, Output: 131072, Source: "seed", Upstream: "responses"},
	{ID: "muse-spark-1.2-contributor-free", Aliases: []string{"muse-spark"}, Context: 1048576, Output: 131072, Source: "seed", Upstream: "responses"},
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

// isZenFreeModel 免费判定: seed 白名单、live 同步（已过价格门）、
// registry/synced 或通用的 -free 后缀。big-pickle/union-alpha 无 free 后缀，
func isZenFreeModel(m *ZenModel) bool {
	if m == nil {
		return false
	}
	if m.Source == "seed" || m.Source == "live" {
		return true
	}
	return strings.HasSuffix(m.ID, "-free")
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
			// 损坏配置改名留档，避免下次保存把可手工恢复的原文覆盖掉
			stamp := time.Now().Format("20060102-150405")
			if renErr := os.Rename(path, path+".corrupt-"+stamp); renErr == nil {
				log.Printf("zen config is corrupt JSON; moved to %s.corrupt-%s", path, stamp)
			}
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
	data, err := json.MarshalIndent(zenConfig, "", "  ")
	if err != nil {
		log.Printf("zen config marshal failed: %v", err)
		return
	}
	// 原子写: 临时文件 + rename，崩溃中途写入不会截断原文件
	path := kit.ResolveDataPath(".zen-config.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		log.Printf("zen config save failed: %v", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("zen config save failed (rename): %v", err)
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
	old := zenConfig
	zenConfig = c
	zenConfigMu.Unlock()
	saveZenConfig()
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
	// 代理列表变化时: 索引会位移,按索引记录的冷却整体失效,直接清空;
	// 同时驱逐已移除代理的钉定客户端,释放其空闲连接
	if proxiesChanged(old.Proxies, c.Proxies) {
		zenProxyCooldownsMu.Lock()
		zenProxyCooldowns = map[int]time.Time{}
		zenProxyCooldownsMu.Unlock()
	}
	validProxy := map[string]bool{"": true}
	for _, p := range c.Proxies {
		validProxy[p] = true
	}
	proxyClientCacheMu.Lock()
	for u, cl := range proxyClientCache {
		if !validProxy[u] {
			delete(proxyClientCache, u)
			if cl != nil {
				cl.CloseIdleConnections()
			}
		}
	}
	proxyClientCacheMu.Unlock()
}

// proxiesChanged 按顺序比较两个代理列表是否不同（数量或任一位置变化都算）。
func proxiesChanged(a, b []string) bool {
	if len(a) != len(b) {
		return true
	}
	for i := range a {
		if a[i] != b[i] {
			return true
		}
	}
	return false
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
			return k
		}
	}
	// 全部冷却中：仍按轮转返回（保持请求流动，上游会再次限流）
	k := keys[zenKeyIdx%len(keys)]
	zenKeyIdx = (zenKeyIdx + 1) % len(keys)
	return k
}

// markZenKeySuccess 在上游 200 后累计 key 的成功调用数（仅内存态，面板展示用）。
// 计数放在成功路径而非选取路径，失败重试才不会虚增用量。
func markZenKeySuccess(key string) {
	if key == "" {
		return
	}
	zenKeyMu.Lock()
	zenKeyUsage[key]++
	zenKeyMu.Unlock()
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

// pinnedZenKey 取 ZEN_PIN_KEY 指定的固定 key（1-based 序号，如 "2"）。
// 排障/单 key 直测用：所有 attempt 都用该 key，避免轮换污染归因。
// 未设置或越界时返回 ""（正常轮转）。
func pinnedZenKey() string {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("ZEN_PIN_KEY")))
	if err != nil || n < 1 {
		return ""
	}
	keys := getZenConfig().Keys
	if n > len(keys) {
		return ""
	}
	return keys[n-1]
}

// ============ zen 上游调用 ============

// zenGateToolSpecs 免费层两个端点必须携带的 opencode 工具集（工具名与官方
// CLI 一致）。FreeTier 中间件按工具名校验"请求是否来自 opencode CLI"
//（2026-09-18 实测解码：缺工具/工具名不齐 → 403 FreeTierError，即使会话
// 有效；乱序、假描述、仅核心 5 名(bash/edit/glob/grep/read)也通过）。
// 描述用精简占位即可——工具注入后模型在 tool_choice=none（chat）或
// 文本语义（responses）下不产生实质工具调用，回复保持纯文本。
// 单一来源：改名/增删必须同时满足两个端点的校验，只改这里。
var zenGateToolSpecs = []struct{ name, desc string }{
	{"bash", "Execute a bash command"},
	{"edit", "Edit a file"},
	{"glob", "Find files by glob pattern"},
	{"grep", "Search file contents"},
	{"read", "Read a file"},
	{"skill", "Load a skill"},
	{"task", "Run a background task"},
	{"todowrite", "Write a todo list"},
	{"webfetch", "Fetch a web page"},
	{"websearch", "Search the web"},
	{"write", "Write a file"},
}

// zenGateTools chat 端点形态：tools[].{type,function:{name,description,parameters}}。
func zenGateTools() []map[string]any {
	out := make([]map[string]any, 0, len(zenGateToolSpecs))
	for _, s := range zenGateToolSpecs {
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        s.name,
				"description": s.desc,
				"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
			},
		})
	}
	return out
}

// zenResponsesGateTools responses 端点形态：flat tools[].{type,name,description,
// parameters,strict}（chat 嵌套形态上游报 400 "tools[0] missing required field
// name"）。工具名校验与 chat 端点同源；且 responses 端点 tool_choice 只接受
// "auto"（"none" 报 400 "only `auto` allowed"）。
func zenResponsesGateTools() []map[string]any {
	out := make([]map[string]any, 0, len(zenGateToolSpecs))
	for _, s := range zenGateToolSpecs {
		out = append(out, map[string]any{
			"type":        "function",
			"name":        s.name,
			"description": s.desc,
			"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
			"strict":      false,
		})
	}
	return out
}

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
	// FreeTier gate（2026-09-18 实测解码）：zen 免费层 chat 端点校验请求体
	// 是否携带 opencode 工具集，缺失则 403（"can only be used from within
	// OpenCode"）。恒注入 11 个规范工具名保证过闸。tool_choice 按客户端
	// 是否带工具分流（实测对比）：
	//  - 客户端无工具（纯文本问答）：tool_choice=none。auto 会让部分模型
	//    （ling 多轮实测）在有工具但无需调用时返回空文本；none 保证回复。
	//  - 客户端带工具（IDE agent 模式，Cursor 等）：透传客户端工具 +
	//    tool_choice=auto，模型可发起调用、IDE 执行后回传结果（实测
	//    ling 调用客户端自定义 get_weather 成功）。覆盖客户端 tool_choice
	//    为 auto：none 会同时压制客户端工具。
	var clientTools []any
	if ct, ok := params["tools"].([]any); ok && len(ct) > 0 {
		clientTools = ct
	}
	if len(clientTools) > 0 {
		merged := zenGateTools()
		for _, t := range clientTools {
			tm, ok := t.(map[string]any)
			if !ok {
				continue
			}
			fn, ok := tm["function"].(map[string]any)
			if !ok {
				continue
			}
			n, _ := fn["name"].(string)
			if n != "" && !containsGateTool(n) {
				merged = append(merged, tm)
			}
		}
		body["tools"] = merged
		body["tool_choice"] = "auto"
	} else {
		body["tools"] = zenGateTools()
		body["tool_choice"] = "none"
	}
	return body
}

// containsGateTool 判断工具名是否为 FreeTier gate 的 11 个规范工具之一。
// 客户端与 gate 重名的工具不加第二份（如 Cursor 也发 bash/edit/read）。
func containsGateTool(name string) bool {
	for _, s := range zenGateToolSpecs {
		if s.name == name {
			return true
		}
	}
	return false
}

// ============ zen 上游调用：原生 /v1/responses 端点 ============
//
// 背景: 部分 zen 免费模型（如 muse-spark）在 chat/completions 端点上 500，
// 官方 opencode CLI 对它们只发原生 POST /zen/v1/responses（Responses 形态
// 请求体 + SSE 事件流响应）。网关对这类模型走同样路径：请求体按 Responses
// 形态构造，响应再转回 chat completions 形态，上下游都不感知差异。

// buildZenResponsesBody 把 chat 请求参数映射为 Responses 形态请求体：
// messages -> input（system/developer 指令并入 instructions），
// max_tokens/max_completion_tokens -> max_output_tokens。
// 字段经官方 CLI 实际请求逐字段核对：reasoning.summary=auto、
// include=[reasoning.encrypted_content]、tool_choice 缺省 auto、
// prompt_cache_key=会话 ID 均为 CLI 常发字段，缺省会与原生形态不一致。
// promptKey 为本次上游会话 ID（调用方传入本次请求的 sess_ 值）。
func buildZenResponsesBody(params map[string]any, stream bool, modelID string, promptKey string) map[string]any {
	body := map[string]any{
		"model":  modelID,
		"stream": true, // FreeTier gate 硬要求：responses 端点只接受 stream=true，
		// stream=false（即使显式传）上游按"非 CLI"请求 403。网关非流式客户端
		// 由调用方聚合（responsesSSEToChat），body 恒发 true。
		"store": false,
	}
	if promptKey != "" {
		body["prompt_cache_key"] = promptKey
	}
	var input []any
	if msgs, ok := params["messages"].([]any); ok {
		for _, m := range msgs {
			mm, ok := m.(map[string]any)
			if !ok {
				continue
			}
			role, _ := mm["role"].(string)
			content := responsesContentToInput(mm["content"])
			if role == "system" || role == "developer" {
				// system/developer 指令提升为顶层 instructions（Responses 标准字段；
				// 官方 CLI 发 role=developer 条目且无顶层 instructions——网关把
				// 历史 system/developer 内容并入 instructions 保持等价）
				if s, ok := content.(string); ok && s != "" {
					if prev, _ := body["instructions"].(string); prev != "" {
						body["instructions"] = prev + "\n\n" + s
					} else {
						body["instructions"] = s
					}
				}
				continue
			}
			if role == "tool" {
				// tool 结果 -> function_call_output（Responses 标准形态）
				callID, _ := mm["tool_call_id"].(string)
				out := ""
				switch o := content.(type) {
				case string:
					out = o
				default:
					if b, err := json.Marshal(content); err == nil {
						out = string(b)
					}
				}
				input = append(input, map[string]any{
					"type":    "function_call_output",
					"call_id": callID,
					"output":  out,
				})
				continue
			}
			entry := map[string]any{"role": role, "content": content}
			// assistant 历史 tool_calls -> function_call 条目（保留调用链）。
			// 内容与工具调用分开：assistant 的文本内容仍在 entry 里，每个
			// tool_call 各 append 一条 function_call 输入（旧实现 break 只保
			// 第一条且把 entry 换成 function_call，丢掉正文文本）。
			if role == "assistant" {
				if tcs, ok := mm["tool_calls"].([]any); ok {
					for _, tc := range tcs {
						tcm, ok := tc.(map[string]any)
						if !ok {
							continue
						}
						fn, _ := tcm["function"].(map[string]any)
						id, _ := tcm["id"].(string)
						name := ""
						args := ""
						if fn != nil {
							name, _ = fn["name"].(string)
							args, _ = fn["arguments"].(string)
						}
						input = append(input, map[string]any{
							"type":      "function_call",
							"id":        id,
							"call_id":   id,
							"name":      name,
							"arguments": args,
						})
					}
				}
			}
			input = append(input, entry)
		}
	}
	if len(input) == 0 {
		input = []any{map[string]any{"role": "user", "content": ""}}
	}
	body["input"] = input
	// 输出上限：Responses 字段名是 max_output_tokens（官方 CLI 实测发送该字段）
	if mt := asInt(params["max_tokens"]); mt > 0 {
		body["max_output_tokens"] = mt
	} else if mt := asInt(params["max_completion_tokens"]); mt > 0 {
		body["max_output_tokens"] = mt
	}
	// 透传采样参数（Responses 与 chat 同名）
	for _, k := range []string{"temperature", "top_p"} {
		if v, ok := params[k]; ok {
			body[k] = v
		}
	}
	// reasoning_effort 在 Responses 形态下保留为 reasoning.effort（官方 CLI 实测发送）
	if eff, ok := params["reasoning_effort"].(string); ok && eff != "" {
		body["reasoning"] = map[string]any{"effort": eff, "summary": "auto"}
	} else if eff, ok := params["reasoningEffort"].(string); ok && eff != "" {
		body["reasoning"] = map[string]any{"effort": eff, "summary": "auto"}
	} else {
		// 官方 CLI 即使无 effort 也发 reasoning.summary=auto；缺该字段的
		// 请求与原生形态不一致，补默认值保持一致
		body["reasoning"] = map[string]any{"summary": "auto"}
	}
	// include 原生字段：官方 CLI 实测发送 reasoning.encrypted_content；
	// 缺省时补齐以匹配原生请求形态
	body["include"] = []any{"reasoning.encrypted_content"}
	// FreeTier gate（2026-09-18 实测解码）：/v1/responses 端点同样校验请求体
	// 是否携带 opencode 工具集，缺失则 403；且必须 flat 形态 + tool_choice=auto
	//（chat 嵌套形态 400 missing name、tool_choice=none 400 only auto allowed）。
	// 恒注入全部 11 个规范工具名 + auto——auto 是 responses 端点唯一接受的值。
	// 与 chat 路径一致：透传客户端自定义工具（嵌套 chat 形态转 flat），
	// 让 IDE agent 模式的工具调用在 spark 上可用。
	merged := zenResponsesGateTools()
	if ct, ok := params["tools"].([]any); ok && len(ct) > 0 {
		for _, t := range ct {
			tm, ok := t.(map[string]any)
			if !ok {
				continue
			}
			fn, ok := tm["function"].(map[string]any)
			if !ok {
				continue
			}
			n, _ := fn["name"].(string)
			if n == "" || containsGateTool(n) {
				continue
			}
			desc, _ := fn["description"].(string)
			merged = append(merged, map[string]any{
				"type":        "function",
				"name":        n,
				"description": desc,
				"parameters":  fn["parameters"],
				"strict":      false,
			})
		}
	}
	body["tools"] = merged
	body["tool_choice"] = "auto"
	return body
}

// responsesContentToInput chat content -> Responses input content。
// 字符串原样；parts 数组只保留文本/图片两种 Responses 原生类型。
func responsesContentToInput(content any) any {
	if s, ok := content.(string); ok {
		return s
	}
	parts, ok := content.([]any)
	if !ok {
		return ""
	}
	out := make([]any, 0, len(parts))
	for _, p := range parts {
		pm, ok := p.(map[string]any)
		if !ok {
			continue
		}
		t, _ := pm["type"].(string)
		switch t {
		case "text", "input_text":
			txt, _ := pm["text"].(string)
			out = append(out, map[string]any{"type": "input_text", "text": txt})
		case "image_url":
			url := ""
			if u, ok := pm["image_url"].(map[string]any); ok {
				url, _ = u["url"].(string)
			}
			if url != "" {
				// Responses 原生图片 part 是 input_image；chat 形态的
				// image_url 会被上游拒绝/忽略
				out = append(out, map[string]any{"type": "input_image", "image_url": url})
			}
		}
	}
	if len(out) == 1 {
		if one, ok := out[0].(map[string]any); ok && one["type"] == "input_text" {
			txt, _ := one["text"].(string)
			return txt
		}
	}
	return out
}

// responsesSSEToChat 把原生 responses SSE 事件流聚合成 chat completions 形态。
// 处理两种事件风格（上游按次返回其一）：
//   - delta 流：response.output_text.delta / function_call_arguments.delta /
//     reasoning_summary_text.delta（逐块增量文本）
//   - output_item.done：response.output_item.done 内 item.content[].text 全量
//     文本（muse-spark 对纯文本问答只发该事件，无 delta 流）
// usage 从 response.completed 或 response.incomplete 提取；
// incomplete_details.reason 映射为 finish 原因（length→length）。
// zenSSECall 单个 function_call 输出项的流式累积器。上游可能并行/连续输出
// 多个工具调用，必须按 item 分开累积参数，否则两个调用的 arguments delta
// 会串成一段坏 JSON（上游实测：webfetch 双调用 jam 成 {"query":...}{"url":...}）。
type zenSSECall struct {
	id, name, callID string
	args             strings.Builder
	final            string // output_item.done 一次性给全参（无 delta 事件时）
}

func responsesSSEToChat(resp *http.Response) (map[string]any, error) {
	defer resp.Body.Close()
	var text, reasoning strings.Builder
	var usage map[string]any
	var incompleteReason string
	failed := false
	calls := []*zenSSECall{}
	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			line = strings.TrimRight(line, "\r\n")
			if strings.HasPrefix(line, "data:") {
				payload := strings.TrimSpace(line[5:])
				if payload != "" && payload != "[DONE]" {
					var ev map[string]any
					if json.Unmarshal([]byte(payload), &ev) == nil {
						typ, _ := ev["type"].(string)
						switch typ {
						case "response.output_text.delta":
							if d, _ := ev["delta"].(string); d != "" {
								text.WriteString(d)
							}
						case "response.reasoning_summary_text.delta":
							if d, _ := ev["delta"].(string); d != "" {
								reasoning.WriteString(d)
							}
						case "response.function_call_arguments.delta":
							if d, _ := ev["delta"].(string); d != "" {
								// 按 item_id 找累积器；找不到（顺序异常）时追加到最后
								// 一个或新建。正常流：added 先于 delta。
								var target *zenSSECall
								if iid, _ := ev["item_id"].(string); iid != "" {
									for _, c := range calls {
										if c.id == iid || (c.callID != "" && c.callID == iid) {
											target = c
											break
										}
									}
								}
								if target == nil && len(calls) > 0 {
									target = calls[len(calls)-1]
								}
								if target == nil {
									target = &zenSSECall{}
									calls = append(calls, target)
								}
								target.args.WriteString(d)
							}
						case "response.output_item.added":
							if item, ok := ev["item"].(map[string]any); ok {
								if it, _ := item["type"].(string); it == "function_call" {
									call := &zenSSECall{}
									if id, _ := item["id"].(string); id != "" {
										call.id = id
									}
									if cid, _ := item["call_id"].(string); cid != "" {
										call.callID = cid
									}
									if n, _ := item["name"].(string); n != "" {
										call.name = n
									}
									calls = append(calls, call)
								}
							}
						case "response.output_item.done":
							// 全量输出项：message 条目 content[].output_text/text 即最终文本；
							// function_call 条目（无 delta 事件的模型）一次性给全参
							if item, ok := ev["item"].(map[string]any); ok {
								switch it, _ := item["type"].(string); it {
								case "message":
									if content, ok := item["content"].([]any); ok {
										for _, part := range content {
											pm, ok := part.(map[string]any)
											if !ok {
												continue
											}
											pt, _ := pm["type"].(string)
											if pt != "output_text" && pt != "text" {
												continue
											}
											if t, _ := pm["text"].(string); t != "" {
												text.WriteString(t)
											}
										}
									}
								case "function_call":
									// 全量 arguments（无 delta 事件的模型一次给全）。
									// 只在 args 尚未累积时采纳，避免与 delta 重复。
									var a string
									if av, ok := item["arguments"].(string); ok {
										a = av
									}
									if a != "" {
										var target *zenSSECall
										if id, _ := item["id"].(string); id != "" {
											for _, c := range calls {
												if c.id == id {
													target = c
													break
												}
											}
										}
										if target == nil && len(calls) > 0 {
											target = calls[len(calls)-1]
										}
										if target == nil {
											target = &zenSSECall{}
											calls = append(calls, target)
										}
										if n, _ := item["name"].(string); n != "" {
											target.name = n
										}
										if id, _ := item["id"].(string); id != "" {
											target.id = id
										}
										if cid, _ := item["call_id"].(string); cid != "" {
											target.callID = cid
										}
										if target.args.Len() == 0 {
											target.final = a
										}
									}
								}
							}
						case "response.completed", "response.incomplete", "response.failed":
							if r, ok := ev["response"].(map[string]any); ok {
								if u, ok := r["usage"].(map[string]any); ok {
									usage = u
								}
								if det, ok := r["incomplete_details"].(map[string]any); ok {
									if reason, _ := det["reason"].(string); reason != "" {
										incompleteReason = reason
									}
								}
								if typ == "response.failed" {
									failed = true
									if incompleteReason == "" {
										incompleteReason = "error"
									}
								}
							}
						}
					}
				}
			}
		}
		if err != nil {
			break
		}
	}
	if failed {
		return nil, fmt.Errorf("zen responses upstream failed: %s", incompleteReason)
	}
	textStr := text.String()
	msg := map[string]any{"role": "assistant", "content": textStr}
	finish := "stop"
	if incompleteReason == "max_output_tokens" || incompleteReason == "length" {
		finish = "length"
	}
	if len(calls) > 0 {
		outCalls := make([]any, 0, len(calls))
		for _, call := range calls {
			if call.name == "" && call.args.Len() == 0 && call.final == "" {
				continue // 空壳（added 但无任何内容）
			}
			raw := call.final
			if call.args.Len() > 0 {
				raw = call.args.String()
			}
			fixed := repairToolArguments(raw)
			id := call.callID
			if id == "" {
				id = call.id
			}
			if id == "" {
				id = fmt.Sprintf("call_%x", time.Now().UnixNano())
			}
			outCalls = append(outCalls, map[string]any{
				"id":   id,
				"type": "function",
				"function": map[string]any{
					"name":      call.name,
					"arguments": fixed,
				},
			})
		}
		if len(outCalls) > 0 {
			msg["tool_calls"] = outCalls
			finish = "tool_calls"
		}
	}
	if reasoning.Len() > 0 {
		msg["reasoning_content"] = reasoning.String()
	}
	out := map[string]any{
		"id":      fmt.Sprintf("chatcmpl-%x", time.Now().UnixNano()),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   "",
		"choices": []any{map[string]any{
			"index":         0,
			"message":       msg,
			"finish_reason": finish,
		}},
		"usage": map[string]any{
			"prompt_tokens":     0,
			"completion_tokens": 0,
			"total_tokens":      0,
		},
	}
	if usage != nil {
		u := map[string]any{
			"prompt_tokens":     float64(0),
			"completion_tokens": float64(0),
			"total_tokens":      float64(0),
		}
		if v, ok := usage["input_tokens"].(float64); ok {
			u["prompt_tokens"] = v
		}
		if v, ok := usage["output_tokens"].(float64); ok {
			u["completion_tokens"] = v
		}
		if v, ok := usage["total_tokens"].(float64); ok {
			u["total_tokens"] = v
		} else {
			u["total_tokens"] = u["prompt_tokens"].(float64) + u["completion_tokens"].(float64)
		}
		out["usage"] = u
	}
	return out, nil
}

// callZenResponsesAPI 原生 /v1/responses 端点调用：与 callZenAPI 相同的
// 轮转/重试/冷却/统计语义，只差请求体形态与端点路径。
//   - stream=true 时返回原生 SSE 流（调用方自行转换呈现）
//   - stream=false 时在内部把 SSE 聚合成 chat completions 形态返回，
//     响应体可直接按 chat 结构解码
// 返回 (响应, 命中限流次数, 错误)。
//
// prompt_cache_key 绑定本次上游会话：官方 CLI 发 prompt_cache_key=<会话 ID>
// 且与 x-opencode-session 同值。网关此前每 attempt 换新 sess_ 导致同一请求
// 的 header 与 body 会话不一致；现 body 在请求体构造时绑定当次 sess_。
func callZenResponsesAPI(ctx context.Context, params map[string]any, stream bool) (*http.Response, int, error) {
	cfg := getZenConfig()
	model, _ := params["model"].(string)
	zm, ok := resolveZenModel(model)
	if !ok {
		return nil, 0, fmt.Errorf("model %q is not a known zen model", model)
	}
	endpoint := strings.TrimRight(cfg.BaseURL, "/") + "/responses"

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
	retryKey := "" // 非空时重试沿用该 key（保持 key sess_ 一致）

	for attempt := 0; ; attempt++ {
		proxyURL, pidx := pickUpstreamProxy()
		viaProxy := "direct"
		if proxyURL != "" {
			viaProxy = maskProxyURL(proxyURL)
		}
		// 先选 key：attempt==0 或尚无粘性 key 时轮转；重试链内沿用
		// retryKey（会话绑定要求 key 与 sess_ 一致；换 key 由下方
		// 限流/403 分支显式改写 retryKey 后 continue 实现）。
		// ZEN_PIN_KEY=n 时固定用第 n 个 key（排障直测），默认轮转。
		var key string
		if pk := pinnedZenKey(); pk != "" {
			key = pk
			retryKey = pk
		} else if retryKey == "" {
			retryKey = pickZenKey()
		}
		key = retryKey
		sess, user, ua := "", "", ""
		if key != "" {
			// 会话粘性：同一 key 复用稳定的 sess_/UA（服务端会话绑定要求），
			// msg_ 请求 ID 仍每次随机。
			sess, user, ua = StickyZenIdentity(key)
		} else {
			sess, user, ua = kit.FreshZenIdentity()
		}
		body := buildZenResponsesBody(params, stream, zm.ID, sess)
		bodyJSON, err := json.Marshal(body)
		if err != nil {
			return nil, rateLimited, fmt.Errorf("marshal zen responses body: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(bodyJSON))
		if err != nil {
			return nil, rateLimited, fmt.Errorf("create zen responses request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", ua)
		req.Header.Set("x-opencode-session", sess)
		req.Header.Set("x-opencode-request", user)
		req.Header.Set("x-opencode-client", "cli")
		req.Header.Set("x-opencode-project", "global")
		log.Printf("  zen upstream: model=%s responses stream=%v via=%s key=#%d attempt=%d session=%s",
			zm.ID, stream, viaProxy, keyIndex(key), attempt+1, kit.Truncate(sess, 24))

		resp, err := proxyClientFor(proxyURL).Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, rateLimited, fmt.Errorf("client aborted: %w", err)
			}
			if pidx >= 0 {
				cooldownUpstreamProxy(pidx, 2*time.Minute)
				log.Printf("  zen proxy failed (%v), cooldown exit %s for 2m", err, viaProxy)
			}
			if attempt < retries {
				log.Printf("  zen responses network error (%v), retry %d/%d after %v", err, attempt+1, retries, delay)
				if !sleepCtx(ctx, kit.WithRetryJitter(delay)) {
					return nil, rateLimited, fmt.Errorf("client aborted during retry wait")
				}
				delay *= 2
				continue
			}
			return nil, rateLimited, fmt.Errorf("zen responses request: %w", err)
		}
		if resp.StatusCode == http.StatusOK {
			markZenKeySuccess(key)
			markZenSuccess()
			harvestMarkSuccess(key)
			if stream {
				return resp, rateLimited, nil
			}
			// 非流式：内部聚合为 chat completions 形态后返回
			chat, aerr := responsesSSEToChat(resp)
			if aerr != nil {
				return nil, rateLimited, aerr
			}
			chat["model"] = zm.ID
			data, merr := json.Marshal(chat)
			if merr != nil {
				return nil, rateLimited, merr
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewReader(data)),
			}, rateLimited, nil
		}

		bodyBytes := kit.ReadBody(resp)
		resp.Body.Close()
		reason := fmt.Sprintf("zen API %d: %s", resp.StatusCode, kit.Truncate(bodyBytes, 500))

		if isRateLimited(resp.StatusCode, bodyBytes) {
			rateLimited++
			rl := parseRetryAfter(resp.Header.Get("Retry-After"))
			cooldownZenKey(key, rl)
			if next := pickZenKey(); next != "" && next != key && !zenKeyCooling(next) {
				// 必须同步改写 retryKey：循环头 key=retryKey，只改 key 不改
				// retryKey 会让下一次迭代继续用刚冷却的旧 key，无限 429 空转。
				key = next
				retryKey = next
				log.Printf("  zen responses rate limited (%d), switching to next zen key (%d configured)", resp.StatusCode, len(cfg.Keys))
				continue
			}
			if attempt < retries {
				wait := delay
				if retryAfter := parseRetryAfter(resp.Header.Get("Retry-After")); retryAfter > wait {
					wait = retryAfter
				}
				if wait > 30*time.Second {
					wait = 30 * time.Second
				}
				log.Printf("  zen responses rate limited (%d), retry %d/%d after %v", resp.StatusCode, attempt+1, retries, wait)
				if !sleepCtx(ctx, kit.WithRetryJitter(wait)) {
					return nil, rateLimited, fmt.Errorf("client aborted during retry wait")
				}
				delay *= 2
				continue
			}
			markZenFail()
			return nil, rateLimited, fmt.Errorf("%s", reason)
		}

		if resp.StatusCode >= 500 {
			markZenFail()
		}
		// 会话失效（FreeTier 403 且非限流）：该 key 的 sess_ 已被服务端
		// 遗忘，复用只会持续 403。本地随机 sess_ 必 403，不轮换；后台
		// 收割机（连续 403 达阈值）mint 真会话补上。本次按轮转换 key 重试。
		if resp.StatusCode == http.StatusForbidden {
			go harvestOnForbidden(key)
			if attempt < retries {
				if next := pickZenKey(); next != "" && !zenKeyCooling(next) {
					key = next
					retryKey = next
					log.Printf("  zen responses session rejected (403), switching to key#%d", keyIndex(next))
					continue
				}
			}
		}
		return nil, rateLimited, fmt.Errorf("%s", reason)
	}
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
		// 客户端身份：会话粘性——同一 key 复用稳定的 sess_/UA（服务端
		// 会话绑定要求，随机 sess_ 会 403），msg_ 请求 ID 每次随机。
		// ZEN_PIN_KEY=n 时固定用第 n 个 key（单 key 直测/排障），默认轮转。
		key := pickZenKey()
		if pk := pinnedZenKey(); pk != "" {
			key = pk
		}
		sess, user, ua := StickyZenIdentity(key)
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", ua)
		req.Header.Set("x-opencode-session", sess)
		req.Header.Set("x-opencode-request", user)
		req.Header.Set("x-opencode-client", "cli")
		req.Header.Set("x-opencode-project", "global")

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
			markZenKeySuccess(key)
			markZenSuccess()
			harvestMarkSuccess(key)
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
		// 会话失效（FreeTier 403 且非限流）：该 key 的 sess_ 已被服务端遗忘，
		// 复用只会持续 403。后台收割机（连续 403 达阈值）mint 真会话补上；
		// 本次直接轮转下一 key 重试（循环头每次 pickZenKey，天然换 key）。
		if resp.StatusCode == http.StatusForbidden {
			go harvestOnForbidden(key)
			if attempt < retries {
				continue
			}
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
			"id":        cp.ID,
			"context":   cp.Context,
			"output":    cp.Output,
			"source":    cp.Source,
			"upstream":  cp.Upstream,
			"toolCall":  cp.ToolCall,
			"reasoning": cp.Reasoning,
			"attach":    cp.Attach,
		})
	}
	zenModelsMu.RUnlock()
	return out
}

// opencodeModelsRegistry 公共模型目录（官方 CLI 同源，无需认证；
// 替代 zen /v1/models，后者用 "public" key 恒失败，只能靠种子兜底）。
const opencodeModelsRegistry = "https://models.opencode.ai/api.json"

// zenModelOverlay 公共目录里比 zen 真源多的限额/旗标字段。
type zenModelOverlay struct {
	Context, Output     int
	ToolCall, Reasoning bool
	Attachment          bool
}

// fetchZenRegistry 拉取公共目录 api.json，返回（限额 overlay、价格门集合、
// 是否可达）。价格门 = cost.input==0 && cost.output==0 且 status 非
// deprecated。模型 ID 以条目内 id 优先、map key 兜底。目录不可达返回
// (空, 空, false)，调用方按 fail-open 处理。
// 唯一实现：syncZenModels 的层 2/3 与收割机 harvestMintModels 共用，
// 避免两处价格门逻辑漂移。
func fetchZenRegistry() (map[string]zenModelOverlay, map[string]bool, bool) {
	overlay := map[string]zenModelOverlay{}
	freeGate := map[string]bool{}
	oreq, err := http.NewRequest("GET", opencodeModelsRegistry, nil)
	if err != nil {
		return overlay, freeGate, false
	}
	oreq.Header.Set("User-Agent", "opencode/latest/cli")
	client := &http.Client{Timeout: 25 * time.Second}
	oresp, err := client.Do(oreq)
	if err != nil {
		return overlay, freeGate, false
	}
	defer oresp.Body.Close()
	if oresp.StatusCode != 200 {
		return overlay, freeGate, false
	}
	var payload map[string]struct {
		Models map[string]struct {
			ID         string `json:"id"`
			Status     string `json:"status"`
			ToolCall   bool   `json:"tool_call"`
			Reasoning  bool   `json:"reasoning"`
			Attachment bool   `json:"attachment"`
			Limit      struct {
				Context int `json:"context"`
				Output  int `json:"output"`
			} `json:"limit"`
			Cost struct {
				Input  float64 `json:"input"`
				Output float64 `json:"output"`
			} `json:"cost"`
		} `json:"models"`
	}
	if json.NewDecoder(oresp.Body).Decode(&payload) != nil {
		return overlay, freeGate, false
	}
	prov, ok := payload["opencode"]
	if !ok {
		return overlay, freeGate, false
	}
	for id, m := range prov.Models {
		if m.ID != "" {
			id = m.ID
		}
		overlay[id] = zenModelOverlay{
			Context: m.Limit.Context, Output: m.Limit.Output,
			ToolCall: m.ToolCall, Reasoning: m.Reasoning, Attachment: m.Attachment,
		}
		if m.Cost.Input == 0 && m.Cost.Output == 0 &&
			!strings.Contains(strings.ToLower(m.Status), "deprecat") {
			freeGate[id] = true
		}
	}
	return overlay, freeGate, true
}

// syncZenModels 同步免费模型，三层来源：
//  1. 真源（membership）：GET zen /v1/models（Bearer "public" 即可，无需认证），
//     当前在线的模型 ID 集合（2026-09-18 实测 71 个，含 big-pickle/union-alpha
//     等无 free 后缀条目；官方 CLI `opencode models` 无认证列出的是同一集合的子集）。
//     只有该层能增删模型。
//  2. 免费资格（pricing gate）：公共目录 models.opencode.ai 的 opencode 条目
//     cost.input==0 && cost.output==0 且 status 无 deprecated 字样。
//     无 free 后缀但价格为 0 的模型（big-pickle/union-alpha）因此入选；
//     价格非 0 的付费模型即使在线也被排除；status=deprecated 的
//     deepseek-v4-flash-free 即使价格为 0 也被排除（上游已报 Model unavailable）。
//     目录不可达时沿用"ID 含 free 即免费"的旧规则（fail-open，保证离线可用）。
//  3. 限额覆盖（overlay）：公共目录的 limit.context/output + tool_call /
//     reasoning / attachment 旗标（zen 真源条目只有 id，无限额字段）。
// 返回新增模型数。真源不可达时返回错误并保留旧表（种子兜底）。
func syncZenModels() (int, error) {
	initZenModels()
	cfg := getZenConfig()
	client := &http.Client{Timeout: 25 * time.Second}

	// --- 层 1：zen 真源 membership ---
	endpoint := strings.TrimRight(cfg.BaseURL, "/") + "/models"
	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Key)
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("zen models HTTP %d", resp.StatusCode)
	}
	var live struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&live); err != nil {
		return 0, err
	}
	liveFree := map[string]bool{}
	for _, item := range live.Data {
		if item.ID == "" {
			continue
		}
		liveFree[item.ID] = true
	}
	if len(liveFree) == 0 {
		return 0, fmt.Errorf("zen models: empty model list (feed anomaly, keeping old table)")
	}

	// --- 层 2+3：公共目录价格门 + 限额 overlay（失败不致命：价格门沿用
	// "ID 含 free 即免费"旧规则，限额保留种子估算） ---
	overlay, freeGate, registryOK := fetchZenRegistry()
	// isFreeModel 免费判定：目录可达时以价格门为准；目录不可达时回退到
	// 旧规则（ID 含 free），保证离线/抖动时免费模型仍可用。
	isFreeModel := func(id string) bool {
		if registryOK {
			return freeGate[id]
		}
		return strings.Contains(strings.ToLower(id), "free")
	}

	zenModelsMu.Lock()
	defer zenModelsMu.Unlock()
	added := 0
	for id := range liveFree {
		// 价格门：在线但非免费（付费模型）不同步进免费表
		if !isFreeModel(id) {
			continue
		}
		ov := overlay[id]
		if cur, ok := zenModels[id]; ok {
			// 存量模型：overlay 限额覆盖种子估算（种子只保 ID/别名/Upstream），
			// Source 升级为 live（修剪只动 live/registry/synced，不动 seed）。
			if ov.Context > 0 {
				cur.Context = ov.Context
			}
			if ov.Output > 0 {
				cur.Output = ov.Output
			}
			if ov != (zenModelOverlay{}) {
				cur.ToolCall, cur.Reasoning, cur.Attach = ov.ToolCall, ov.Reasoning, ov.Attachment
			}
			if cur.Source == "seed" {
				cur.Source = "live"
			}
			continue
		}
		// 跳过与免费模型别名冲突的 ID(如付费的 deepseek-v4-flash),保证别名解析不被覆盖
		if _, conflict := zenAliases[id]; conflict {
			continue
		}
		// 新模型：overlay 限额为准（缺失才回退 200K/32K）；Upstream 按家族规则：
		// muse-spark 走原生 responses（实测 chat/completions 500），其余默认
		// （端点自适应会在走错时自动学习，无需手动维护）。
		ctx, out := ov.Context, ov.Output
		if ctx <= 0 {
			ctx = 200000
		}
		if out <= 0 {
			out = 32768
		}
		upstream := ""
		if strings.Contains(strings.ToLower(id), "muse-spark") {
			upstream = "responses"
		}
		zenModels[id] = &ZenModel{
			ID:        id,
			Context:   ctx,
			Output:    out,
			Source:    "live",
			Upstream:  upstream,
			ToolCall:  ov.ToolCall,
			Reasoning: ov.Reasoning,
			Attach:    ov.Attachment,
		}
		added++
	}
	// 修剪已下线/转付费/已废弃的 live/registry/synced 模型，避免 /v1/models
	// 展示死模型。seed 模型是手工维护的兜底，只升级 Source 不删除——但种子中
	// 已被价格门判为非免费的条目（如 deepseek-v4-flash-free 已 deprecated）
	// 会在下方的种子对账中一并移除，见 seedReconcile。
	// 防御: 真源短暂返回空表/残表(网关抖动、接口变更)时不得清空本地列表 ——
	// liveFree 为空已在上游提前返回错误；此处再要求 liveFree 数量不低于
	// 现有 managed 一半才修剪。
	managedCount := 0
	for _, m := range zenModels {
		// 只统计 managed 来源（live）；seed 由下方种子对账单独处理。
		// 早期版本还写过 registry/synced，现统一只写 live。
		if m.Source == "live" {
			managedCount++
		}
	}
	pruned := 0
	if len(liveFree) > 0 && len(liveFree)*2 >= managedCount {
		for id, m := range zenModels {
			if m.Source == "live" && (!liveFree[id] || !isFreeModel(id)) {
				delete(zenModels, id)
				pruned++
			}
		}
	} else if managedCount > 0 {
		log.Printf("zen model sync: live feed too small (%d live vs %d managed), skipping prune", len(liveFree), managedCount)
	}
	// 种子对账：种子是兜底而非圣旨——种子条目若本次同步中既不在真源、
	// 也不再通过价格门（如已 deprecated），说明已实质下线，从表中移除，
	// 避免种子长期展示死模型（deepseek-v4-flash-free 即此情形）。
	// 种子文件本身保留原文（git 可见变更），运行时表以同步结果为准。
	for _, s := range zenSeedModels {
		if m, ok := zenModels[s.ID]; ok && m.Source == "seed" {
			if !liveFree[s.ID] || !isFreeModel(s.ID) {
				delete(zenModels, s.ID)
				for _, a := range s.Aliases {
					delete(zenAliases, a)
				}
				pruned++
			}
		}
	}
	if pruned > 0 {
		log.Printf("zen model sync: pruned %d model(s) no longer on upstream feed", pruned)
	}
	if added > 0 {
		log.Printf("zen model sync: %d new free model(s) from zen feed", added)
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
