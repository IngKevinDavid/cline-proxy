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
// 若某会话被服务端限流（429/503），调用方按 key 冷却语义处理，必要时可
// 主动调用 ResetZenSession(key) 换新身份（保留手动逃生口）。

type zenSessionEntry struct {
	Session string `json:"session"`
	UA      string `json:"ua"`
	Updated int64  `json:"updated"`
}

var (
	zenSessMu      sync.Mutex
	zenSessions    = map[string]*zenSessionEntry{} // zen key -> sticky identity
	zenSessLoaded  bool
	zenSessPath    string
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
		}
		zenSessions[key] = e
		saveZenSessionsLocked()
		log.Printf("zen sticky session created for key#%d: %s", keyIndex(key), kit.Truncate(e.Session, 24))
	}
	e.Updated = time.Now().Unix()
	return e.Session, "msg_" + kit.RandAlphaNum(26), e.UA
}

// ResetZenSession 丢弃 key 绑定的会话并换新身份（限流逃生口）。
// 下一次 StickyZenIdentity 会创建全新会话。
func ResetZenSession(key string) {
	loadZenSessions()
	zenSessMu.Lock()
	defer zenSessMu.Unlock()
	delete(zenSessions, key)
	saveZenSessionsLocked()
}

// MarkZenSessionDead 标记 key 绑定的会话已失效（上游 FreeTier 403）：
// 下一次 StickyZenIdentity 会换新会话 ID（同 key 下 UA 保留）。
// 避免失效会话被永久复用导致整 key 持续 403。
func MarkZenSessionDead(key string) {
	loadZenSessions()
	zenSessMu.Lock()
	defer zenSessMu.Unlock()
	if e, ok := zenSessions[key]; ok && e != nil {
		e.Session = "sess_" + kit.RandAlphaNum(26)
		saveZenSessionsLocked()
		log.Printf("zen sticky session rotated for key#%d (upstream rejected old session)", keyIndex(key))
	}
}
