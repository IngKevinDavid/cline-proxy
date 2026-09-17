package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cline-go-proxy/internal/kit"
)

// ============ zen 会话收割机（session harvester） ============
//
// 背景：zen 免费层按服务端会话绑定——只有服务端"见过"的 sess_ ID 才能通过
// FreeTier 检查，本地随机生成的 sess_ 必 403。网关自身无法凭空造出有效会话，
// 唯一能 mint 新会话的是官方 opencode CLI（`opencode run` 会在服务端注册
// 新 session，见其本地 sqlite/log）。
//
// 方案：镜像内嵌官方 CLI（二进制，见 Dockerfile）；网关按需调用它跑一条
// 极小请求（transient run，不污染用户项目），从其日志/数据库里读出刚 mint
// 的 sess_，存入 sticky 会话表。触发时机：
//  1. 启动时：key 无 sticky 会话 → 收割一个（避免首请求必 403）；
//  2. 运行时：某 key 连续 FreeTier 403 达阈值 → 后台收割新会话替换；
//  3. 定时：每 HARVEST_INTERVAL 小时为最久未更新的 key 补一个（默认 6h）。
//
// 开关：ZEN_HARVEST=0 关闭（默认开启，需 CLI 存在）；ZEN_HARVEST_BIN 指定
// CLI 路径（默认 /app/bin/opencode）；ZEN_HARVEST_INTERVAL_HOURS 默认 6。
// CLI 认证：容器内 HOME 的 .local/share/opencode/auth.json（首次部署时由
// 管理员按文档写入，见 docs/zen-harvester.md）；每个 key 收割时临时覆盖该
// 文件，收割完恢复（串行化 + 文件锁，保证并发安全）。

var (
	harvestMu      sync.Mutex
	harvestFails   = map[string]int{} // key -> 连续收割失败次数
	harvestLastTry = map[string]time.Time{}
)

// harvestEnabled 是否启用收割机：显式 ZEN_HARVEST=0 关闭；
// CLI 二进制不存在时自动降级（日志提示一次）。
func harvestEnabled() bool {
	if strings.TrimSpace(os.Getenv("ZEN_HARVEST")) == "0" {
		return false
	}
	if _, err := os.Stat(harvestBin()); err != nil {
		return false
	}
	return true
}

func harvestBin() string {
	if p := strings.TrimSpace(os.Getenv("ZEN_HARVEST_BIN")); p != "" {
		return p
	}
	return "/app/bin/opencode"
}

// harvestHome 容器内 CLI 的 HOME：auth.json 与 sqlite 都落在这里，
// 与 DATA_DIR 分开（DATA_DIR 是网关状态，HOME 是 CLI 身份）。
func harvestHome() string {
	if h := strings.TrimSpace(os.Getenv("ZEN_HARVEST_HOME")); h != "" {
		return h
	}
	return "/app/.opencode-home"
}

func harvestAuthPath() string {
	return filepath.Join(harvestHome(), ".local", "share", "opencode", "auth.json")
}

// harvestInterval 定时收割间隔（默认 6h，最小 1h）。
func harvestInterval() time.Duration {
	var hours int
	if n, err := fmt.Sscanf(strings.TrimSpace(os.Getenv("ZEN_HARVEST_INTERVAL_HOURS")), "%d", &hours); err == nil && n == 1 && hours >= 1 {
		return time.Duration(hours) * time.Hour
	}
	return 6 * time.Hour
}

// harvestSession 为指定 key 收割一个新 live 会话：
// 用该 key 的 zen token 临时写入 CLI auth.json，跑一条最小 run，
// 从 CLI 日志中提取本次 mint 的 sess_，写入 sticky 表。
// 返回新 session，失败返回错误（调用方保留旧会话继续）。
func harvestSession(ctx context.Context, key string) (string, error) {
	harvestMu.Lock()
	defer harvestMu.Unlock()

	bin := harvestBin()
	home := harvestHome()
	authPath := harvestAuthPath()

	// 1. 备份现有 auth.json（多 key 轮流覆盖，必须恢复）
	var prevAuth []byte
	if b, err := os.ReadFile(authPath); err == nil {
		prevAuth = b
	}
	if err := os.MkdirAll(filepath.Dir(authPath), 0700); err != nil {
		return "", fmt.Errorf("harvest mkdir: %w", err)
	}
	authObj := map[string]any{
		"opencode": map[string]any{"type": "api", "key": key},
	}
	ab, _ := json.Marshal(authObj)
	if err := os.WriteFile(authPath, ab, 0600); err != nil {
		return "", fmt.Errorf("harvest write auth: %w", err)
	}
	defer func() {
		if prevAuth != nil {
			_ = os.WriteFile(authPath, prevAuth, 0600)
		} else {
			_ = os.Remove(authPath)
		}
	}()

	// 2. 跑最小 run：transient 会话 + 最便宜模型（title-gen 用的 nano 级），
	// 只为在服务端 mint session，不关心回答内容（回答可能因 key 配额 429，
	// 但 session 在 run 开始即已创建）。超时 120s。
	runCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, bin, "run",
		"--model", "opencode/gpt-5.4-nano",
		"Reply with exactly: OK")
	cmd.Dir = home
	// 收割 run 不走任何代理：直连公网（容器须能直连 opencode.ai；
	// 代理池是给网关上游用的，收割是 CLI 自己的注册握手）。
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"XDG_DATA_HOME="+filepath.Join(home, ".local", "share"),
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"HTTP_PROXY=",
		"HTTPS_PROXY=",
		"http_proxy=",
		"https_proxy=",
		"ALL_PROXY=",
		"all_proxy=",
		"NO_PROXY=*",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// 3. 从 CLI 日志提取本次 mint 的 sess_（取最新创建的一条）。
	// 注意：回答 429 不影响——session 在 run 开始即创建，日志照写。
	before := latestHarvestSession(home)
	sess := ""
	for i := 0; i < 3 && sess == ""; i++ {
		_ = cmd.Run() // 回答内容不重要；session 在 run 开始即 mint
		if s := latestHarvestSession(home); s != "" && s != before {
			sess = s
		}
	}
	if sess == "" {
		tail := kit.Truncate(stderr.String()+stdout.String(), 300)
		return "", fmt.Errorf("harvest: no session minted (cli output: %s)", tail)
	}

	// 4. 写入 sticky 表
	loadZenSessions()
	zenSessMu.Lock()
	e, ok := zenSessions[key]
	if !ok || e == nil {
		e = &zenSessionEntry{UA: zenNativeUA}
		zenSessions[key] = e
	}
	e.Session = sess
	zenSessMu.Unlock()
	saveZenSessions()
	log.Printf("zen harvest: key#%d minted live session %s", keyIndex(key), kit.Truncate(sess, 24))
	return sess, nil
}

// latestHarvestSession 从 CLI 日志读最新创建的 session ID。
// 路径：$HOME/.local/share/opencode/log/opencode.log，行含
// `message=created id=ses_...`（实测 2026-09-17）。
func latestHarvestSession(home string) string {
	logPath := filepath.Join(home, ".local", "share", "opencode", "log", "opencode.log")
	f, err := os.Open(logPath)
	if err != nil {
		return ""
	}
	defer f.Close()
	latest := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.Contains(line, "message=created id=ses_") {
			continue
		}
		if i := strings.Index(line, "id=ses_"); i >= 0 {
			rest := line[i+3:]
			j := 0
			for j < len(rest) && isSessChar(rest[j]) {
				j++
			}
			if j > 4 {
				latest = rest[:j]
			}
		}
	}
	return latest
}

func isSessChar(c byte) bool {
	return c == '_' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9')
}

// saveZenSessions 对外持久化（harvester 用；内部写已在锁外调用）。
func saveZenSessions() {
	zenSessMu.Lock()
	defer zenSessMu.Unlock()
	saveZenSessionsLocked()
}

// startZenHarvester 启动定时收割循环：每小时检查一次，为"从未收割成功且
// 无 live 会话"的 key 或"最久未更新超过间隔"的 key 收割。
func startZenHarvester() {
	go func() {
		// 启动时先给无会话的 key 收割（错开 10s，避免与 model sync 抢资源）
		time.Sleep(10 * time.Second)
		harvestMissingSessions()
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if !harvestEnabled() {
				continue
			}
			cfg := getZenConfig()
			if !cfg.Enabled {
				continue
			}
			interval := harvestInterval()
			if interval < time.Hour {
				interval = time.Hour
			}
			for _, k := range cfg.Keys {
				if k == "" || k == "public" {
					continue
				}
				loadZenSessions()
				zenSessMu.Lock()
				e := zenSessions[k]
				var updated int64
				if e != nil {
					updated = e.Updated
				}
				zenSessMu.Unlock()
				if time.Since(time.Unix(updated, 0)) < interval {
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
				_, _ = harvestSession(ctx, k)
				cancel()
				time.Sleep(5 * time.Second) // 多 key 错峰
			}
		}
	}()
}

// harvestMissingSessions 启动时为无 sticky 会话的 key 各收割一个。
func harvestMissingSessions() {
	if !harvestEnabled() {
		return
	}
	cfg := getZenConfig()
	if !cfg.Enabled {
		return
	}
	for _, k := range cfg.Keys {
		if k == "" || k == "public" {
			continue
		}
		loadZenSessions()
		zenSessMu.Lock()
		_, ok := zenSessions[k]
		zenSessMu.Unlock()
		if ok {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
		if _, err := harvestSession(ctx, k); err != nil {
			log.Printf("zen harvest: key#%d startup harvest failed (%v), will retry on 403/later", keyIndex(k), err)
		}
		cancel()
		time.Sleep(5 * time.Second)
	}
}

// harvestOnForbidden 某 key 连续 FreeTier 403 时调用：阈值（默认连续 2 次）
// 达到后后台收割新会话。同步返回（收割本身串行快，失败不阻塞请求）。
func harvestOnForbidden(key string) {
	if !harvestEnabled() {
		return
	}
	harvestMu.Lock()
	harvestFails[key]++
	n := harvestFails[key]
	last := harvestLastTry[key]
	harvestMu.Unlock()

	if n < 2 {
		return // 给 MarkZenSessionDead 的随机轮换一次机会
	}
	if time.Since(last) < 10*time.Minute {
		return // 冷却：同 key 10 分钟内只收割一次
	}
	harvestMu.Lock()
	harvestLastTry[key] = time.Now()
	harvestMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	if _, err := harvestSession(ctx, key); err != nil {
		log.Printf("zen harvest: key#%d harvest failed (%v)", keyIndex(key), err)
		return
	}
	harvestMu.Lock()
	harvestFails[key] = 0
	harvestMu.Unlock()
}

// harvestMarkSuccess key 成功 200 后清零其连续失败计数。
func harvestMarkSuccess(key string) {
	harvestMu.Lock()
	delete(harvestFails, key)
	harvestMu.Unlock()
}
