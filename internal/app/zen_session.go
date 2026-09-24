package app

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"cline-go-proxy/internal/kit"
)

// ============ zen 会话粘性（sticky session） ============
//
// 背景（2026-09-17 实测结论）: zen 免费层按 x-opencode-session /
// prompt_cache_key 做服务端会话绑定——随机 sess_ 即使格式与官方一致也会
// 403 FreeTierError；只有服务端见过存活的会话 ID 才能通过。用 CLI 存活
// 会话回放网关请求 200，用新随机会话回放同一请求 403。
//
// 因此网关不再每次请求 mint 新会话：每个 zen key 绑定一个稳定的 sess_ ID
// （+ 固定的 ai-sdk 形态 UA），msg_ 请求 ID 仍每次随机。会话文件持久化到
// DATA_DIR/.zen-sessions.json，重启后沿用同一身份，避免重启即失活。
//
// 限流规避的取舍：此前"每次新身份"策略正是为了规避 session 维度限流；
// 但免费层会话绑定的优先级更高——无存活会话时请求根本到不了记账层。
// 若某会话被服务端限流（429/503），调用方按 key 冷却语义处理；会话失效
// （FreeTier 403）由收割机后台换新（harvestOnForbidden，见 zen_harvest.go），
// 本地随机 sess_ 必 403，无手动换新意义。

type zenSessionEntry struct {
	Session string `json:"session"`
	UA      string `json:"ua"`
	Updated int64  `json:"updated"`
	// Minted 标记该会话由收割机 CLI 实际 mint（服务端见过）；false 表示
	// 本地随机兜底（启动竞态窗口内的占位），收割机不得跳过此类 key。
	Minted bool `json:"minted,omitempty"`
	// HarvestedAt 最近一次成功收割时间（unix 秒）；周期性收割以此为准，
	// 区别于 Updated（每次请求都会刷新，活跃 key 会被永久跳过）。
	HarvestedAt int64 `json:"harvestedAt,omitempty"`
}

var (
	zenSessMu      sync.Mutex
	zenSessions    = map[string]*zenSessionEntry{} // zen key -> sticky identity
	zenSessLoaded  bool
	zenSessPath    string
	// zenNativeUA 官方 CLI 1.18.31 的原生 ai-sdk 形态 UA（会话粘性与
	// 轮换列表共用；与 Dockerfile opencode-ai@1.18.31、tls_bun.go 指纹
	// 版本耦合——升 CLI 版本需同步这三处）。
	zenNativeUA    = "opencode/1.18.31 ai-sdk/provider-utils/4.0.40 runtime/bun/1.3.14"
)

// zenSessionFile 会话持久化路径（DATA_DIR 优先，容器 volume 挂载点）。
func zenSessionFile() string {
	if zenSessPath == "" {
		zenSessPath = kit.ResolveDataPath(".zen-sessions.json")
	}
	return zenSessPath
}

// loadZenSessions 启动时/首次使用时加载持久化会话（只读一次，失败则空跑）。
func loadZenSessions() {
	zenSessMu.Lock()
	defer zenSessMu.Unlock()
	if zenSessLoaded {
		return
	}
	zenSessLoaded = true
	data, err := os.ReadFile(zenSessionFile())
	if err != nil {
		// ENOENT 是首启正常路径（还没有会话文件）；其他错误要让运维看见，
		// 否则会静默以空表运行，并在下次 save 时把原文件覆盖掉。
		if !os.IsNotExist(err) {
			log.Printf("zen sessions read failed (%s): %v", zenSessionFile(), err)
		}
		return
	}
	var m map[string]*zenSessionEntry
	if err := json.Unmarshal(data, &m); err != nil {
		// 解析失败先把坏文件改名留证，再以空表启动——与 loadZenConfig 同做法。
		// 不备份的话，紧随其后的 save 会直接覆盖，出问题时无从追查。
		_ = os.Rename(zenSessionFile(), zenSessionFile()+".corrupt")
		log.Printf("zen sessions parse failed, starting fresh (backup: %s.corrupt): %v", zenSessionFile(), err)
		return
	}
	migrated := 0
	for k, e := range m {
		if e != nil && e.Session != "" {
			// 旧版本文件没有 minted 字段（随并行收割引入）。用前缀回认已有会话：
			// 收割 CLI mint 的是 ses_*，本地占位是 sess_*（"sess_" 不以 "ses_"
			// 开头，两类不会混淆）。缺这一步，升级后首启会把整池 key 判成
			// "未 mint" 全量重 mint，白烧一遍本就紧张的额度。
			if !e.Minted && strings.HasPrefix(e.Session, "ses_") {
				e.Minted = true
				// 同时把收割时间认到 Updated：这些会话大概率还能用，不该在
				// 升级后的第一次周期扫描就把整池重 mint 一遍。真失效了还有
				// 403 路径（连续 2 次）立刻补收，不必预防性烧额度。
				if e.HarvestedAt == 0 && e.Updated > 0 {
					e.HarvestedAt = e.Updated
				}
				migrated++
			}
			zenSessions[k] = e
		}
	}
	log.Printf("zen sessions loaded: %d key(s) with sticky identity", len(zenSessions))
	if migrated > 0 {
		log.Printf("zen sessions migrated: %d key(s) marked CLI-minted from session id prefix", migrated)
	}
}

// saveZenSessionsLocked 持久化当前会话表（调用方持有 zenSessMu）。
func saveZenSessionsLocked() {
	data, err := json.MarshalIndent(zenSessions, "", "  ")
	if err != nil {
		return
	}
	tmp := zenSessionFile() + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		log.Printf("zen sessions save failed: %v", err)
		return
	}
	if err := os.Rename(tmp, zenSessionFile()); err != nil {
		log.Printf("zen sessions save failed (rename): %v", err)
	}
}

// isConsoleKey 判断 key 是否属于 OpenCode Console OAuth 令牌（st_... 或本地 console auth）。
// Console 令牌原生使用 CanonicalSessionID()，不需要 CLI session harvesting。
func isConsoleKey(key string) bool {
	if key == "" || key == "public" {
		return false
	}
	token := key
	if idx := strings.Index(key, "#"); idx != -1 {
		token = key[:idx]
	}
	if strings.HasPrefix(token, "st_") {
		return true
	}
	if auth, err := GetConsoleAuth(); err == nil && auth != nil && auth.AccessToken != "" && token == auth.AccessToken {
		return true
	}
	return false
}

// zenSessionLive 该 key 是否已有服务端认得的 live 会话（CLI mint 过或 Console OAuth 令牌）。
// false = 会话是本地随机占位，任何上游请求都必 403——请求路径据此跳过它，
// 收割机据此决定要不要补收。
func zenSessionLive(key string) bool {
	if key == "" || key == "public" {
		return false
	}
	if isConsoleKey(key) {
		return true
	}
	loadZenSessions()
	zenSessMu.Lock()
	defer zenSessMu.Unlock()
	e := zenSessions[key]
	return e != nil && e.Minted && e.Session != ""
}

// zenLiveKeys 批量查询（一次加锁），供 pickZenKey 在轮转时优先挑 live key。
func zenLiveKeys(keys []string) map[string]bool {
	if len(keys) == 0 {
		return nil
	}
	loadZenSessions()
	zenSessMu.Lock()
	defer zenSessMu.Unlock()
	live := make(map[string]bool, len(keys))
	for _, k := range keys {
		if isConsoleKey(k) {
			live[k] = true
			continue
		}
		if e := zenSessions[k]; e != nil && e.Minted && e.Session != "" {
			live[k] = true
		}
	}
	return live
}

// zenSessionSnapshot 每个 key 的会话状态（管理面板展示；session 截断显示）。
type zenSessionSnapshot struct {
	Minted      bool
	Live        bool
	Session     string
	HarvestedAt int64
}

func zenSessionSnapshotOf(key string) zenSessionSnapshot {
	if isConsoleKey(key) {
		return zenSessionSnapshot{
			Minted:  true,
			Live:    true,
			Session: "console-oauth",
		}
	}
	loadZenSessions()
	zenSessMu.Lock()
	defer zenSessMu.Unlock()
	e := zenSessions[key]
	if e == nil {
		return zenSessionSnapshot{}
	}
	live := e.Minted && e.Session != ""
	return zenSessionSnapshot{
		Minted:      e.Minted,
		Live:        live,
		Session:     kit.Truncate(e.Session, 12),
		HarvestedAt: e.HarvestedAt,
	}
}

// zenSessionDesc 供日志使用的会话描述：一眼区分"从未 mint 的本地占位"和
// "mint 过但被服务端拒绝"——前者必然 403（预期内），后者才是会话寿命到期的证据。
// 没有这个区分，403 日志无法回答"会话到底能活多久"，只能靠猜。
func zenSessionDesc(key string) string {
	if isConsoleKey(key) {
		return "console-oauth"
	}
	s := zenSessionSnapshotOf(key)
	switch {
	case !s.Minted && s.Session == "":
		return "no session"
	case !s.Minted:
		return "placeholder (never minted, always 403)"
	case s.HarvestedAt <= 0:
		return "minted (age unknown)"
	default:
		age := time.Since(time.Unix(s.HarvestedAt, 0)).Round(time.Minute)
		return fmt.Sprintf("minted %v ago", age)
	}
}

// StickyZenIdentity 取 key 绑定的稳定身份：会话 ID 与 UA 跨请求复用，
// 请求 ID 每次全新（与官方 CLI 语义一致：同会话内多 msg_）。
// 返回 (session, request, user-agent)。
func StickyZenIdentity(key string) (sess, req, ua string) {
	loadZenSessions()
	zenSessMu.Lock()
	defer zenSessMu.Unlock()
	e, ok := zenSessions[key]
	if !ok || e.Session == "" {
		e = &zenSessionEntry{
			Session: "sess_" + kit.RandAlphaNum(26),
			UA:      zenNativeUA,
			// Minted=false：随机 ID 只是启动竞态窗口内的占位（真实请求
			// 先于收割机到达时），收割机启动扫描不跳过此类 key。
		}
		zenSessions[key] = e
		saveZenSessionsLocked()
		log.Printf("zen sticky session created for key#%d: %s (unminted placeholder, harvester will mint)", keyIndex(key), kit.Truncate(e.Session, 24))
	}
	e.Updated = time.Now().Unix()
	// 补 UA：旧版本文件或外部改写的条目可能缺这一项，空 UA 发出会被上游
	// 按非 CLI 流量处理。会话 ID 是服务端绑定的一部分，UA 必须与 mint 时一致。
	if e.UA == "" {
		e.UA = zenNativeUA
	}
	return e.Session, "msg_" + kit.RandAlphaNum(26), e.UA
}

// ResetZenSession 曾用于丢弃 key 绑定的会话并换新身份（限流逃生口）。
// 已移除：本地随机 sess_ 必 403（服务端只认见过存活的会话），换新必须
// 走收割机 harvestSession（CLI mint），见 harvestOnForbidden。

// MarkZenSessionDead 曾把失效会话轮换成随机 ID。已移除：随机 ID 必 403
//（zen_session.go 顶部注释），403 恢复直接走收割机 harvestOnForbidden
//（2 次连续 403 触发，10 分钟冷却），旧 CLI 会话保留为"最后已知"标记。
