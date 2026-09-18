package app

import (
	"encoding/json"
	"log"
	"os"
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
		return
	}
	var m map[string]*zenSessionEntry
	if err := json.Unmarshal(data, &m); err != nil {
		log.Printf("zen sessions parse failed, starting fresh: %v", err)
		return
	}
	for k, e := range m {
		if e != nil && e.Session != "" {
			zenSessions[k] = e
		}
	}
	log.Printf("zen sessions loaded: %d key(s) with sticky identity", len(zenSessions))
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

// zenSessionLive 该 key 是否已有服务端认得的 live 会话（CLI mint 过）。
// false = 会话是本地随机占位，任何上游请求都必 403——请求路径据此跳过它，
// 收割机据此决定要不要补收。
func zenSessionLive(key string) bool {
	if key == "" || key == "public" {
		return false
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
	return e.Session, "msg_" + kit.RandAlphaNum(26), e.UA
}

// ResetZenSession 曾用于丢弃 key 绑定的会话并换新身份（限流逃生口）。
// 已移除：本地随机 sess_ 必 403（服务端只认见过存活的会话），换新必须
// 走收割机 harvestSession（CLI mint），见 harvestOnForbidden。

// MarkZenSessionDead 曾把失效会话轮换成随机 ID。已移除：随机 ID 必 403
//（zen_session.go 顶部注释），403 恢复直接走收割机 harvestOnForbidden
//（2 次连续 403 触发，10 分钟冷却），旧 CLI 会话保留为"最后已知"标记。
