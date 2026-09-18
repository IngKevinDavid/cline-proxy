package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeHarvestCLI 替换真实 CLI：按 HOME 逐个写入一个"刚 mint 的"会话日志行，
// 于是 latestHarvestSession 能从中读出会话 ID。用桩而非真 CLI（185MB Bun 二进制、
// 需要真实 zen 凭据），让收割逻辑可以在任何机器上跑测试。
func fakeHarvestCLI(t *testing.T, failFor map[string]bool) (calls *int32) {
	var n int32
	calls = &n
	prev := harvestRunFn
	harvestRunFn = func(ctx context.Context, bin, home, model string) harvestRunResult {
		atomic.AddInt32(&n, 1)
		if failFor[home] {
			// 不写日志行 = mint 失败；带上 CLI 输出，验证错误信息会转述它
			return harvestRunResult{ExitCode: 1, Output: "stub: no session minted", Elapsed: time.Millisecond}
		}
		writeFakeSessionLog(t, home, fmt.Sprintf("ses_fake%d%010d", atomic.LoadInt32(&n), time.Now().UnixNano()%1e10))
		return harvestRunResult{ExitCode: 0, Elapsed: time.Millisecond}
	}
	t.Cleanup(func() { harvestRunFn = prev })
	return calls
}

func writeFakeSessionLog(t *testing.T, home, id string) {
	t.Helper()
	dir := filepath.Join(home, ".local", "share", "opencode", "log")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir log: %v", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "opencode.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "INFO message=created id=%s\n", id); err != nil {
		t.Fatalf("write log: %v", err)
	}
}

// 隔离测试环境：每次用独立 HOME 与 DATA_DIR，并清空进程级会话表。
func setupHarvestTest(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "harvest-test")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("ZEN_HARVEST_HOME", filepath.Join(dir, "home"))
	t.Setenv("ZEN_HARVEST_BIN", "/bin/true")
	t.Setenv("DATA_DIR", dir)

	zenSessMu.Lock()
	zenSessions = map[string]*zenSessionEntry{}
	zenSessLoaded = true
	zenSessPath = ""
	zenSessMu.Unlock()
	// 收割相关的进程级计数同样要清：否则上一个用例残留的 harvestLastSweep
	// 会把本用例的 key 判成"刚试过"，周期补收用例随机失败。
	harvestMu.Lock()
	harvestFails = map[string]int{}
	harvestLastTry = map[string]time.Time{}
	harvestLastSweep = map[string]time.Time{}
	harvestMu.Unlock()
	mintModelsMu.Lock()
	mintModelsCache = nil
	mintModelsAt = time.Time{}
	mintModelsMu.Unlock()
	harvestSemOnce = sync.Once{}
	harvestKeyMu.Lock()
	harvestKeyLocks = map[string]*sync.Mutex{}
	harvestKeyMu.Unlock()
	return dir
}

// 每个 key 的 HOME 必须互不相同（并行收割的前提），且路径里不得出现 key 本身
// （路径会进日志/进程列表）。
func TestHarvestHomeIsPerKeyAndDoesNotLeakKey(t *testing.T) {
	setupHarvestTest(t)
	a, b := harvestHomeForKey("sk-secret-key-aaaaaaaa"), harvestHomeForKey("sk-secret-key-bbbbbbbb")
	if a == b {
		t.Fatalf("two keys share a harvest HOME: %s", a)
	}
	if harvestHomeForKey("sk-secret-key-aaaaaaaa") != a {
		t.Fatal("harvestHomeForKey must be deterministic for the same key")
	}
	for _, p := range []string{a, b} {
		if filepath.Base(p) == "" || len(filepath.Base(p)) > 16 {
			t.Fatalf("unexpected dir name %q", filepath.Base(p))
		}
		if contains(p, "secret") || contains(p, "sk-") {
			t.Fatalf("harvest HOME leaks the key material: %s", p)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// 收割不再触碰公共 HOME 的 auth.json：管理员写在默认 HOME 里的真实凭据
// （人工 `opencode auth login` 的结果）必须原封不动。旧实现每次收割都覆盖它
// 再恢复，恢复失败即永久丢失管理员凭据。
func TestHarvestLeavesSharedHomeAuthUntouched(t *testing.T) {
	dir := setupHarvestTest(t)
	fakeHarvestCLI(t, nil)
	sharedAuth := harvestAuthPath(harvestHome())
	if err := os.MkdirAll(filepath.Dir(sharedAuth), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	sentinel := []byte(`{"opencode":{"type":"api","key":"ADMIN-REAL-KEY"}}`)
	if err := os.WriteFile(sharedAuth, sentinel, 0600); err != nil {
		t.Fatalf("seed auth: %v", err)
	}
	sess, err := harvestSession(context.Background(), "sk-harvest-me")
	if err != nil {
		t.Fatalf("harvestSession: %v", err)
	}
	if sess == "" {
		t.Fatal("harvestSession returned an empty session")
	}
	got, err := os.ReadFile(sharedAuth)
	if err != nil {
		t.Fatalf("shared auth.json vanished: %v", err)
	}
	if !reflect.DeepEqual(got, sentinel) {
		t.Fatalf("shared auth.json was modified: %s", got)
	}
	_ = dir
}

// 收割后该 key 必须被标为 live，且会话 ID 落进 sticky 表。
func TestHarvestMarksKeyLive(t *testing.T) {
	setupHarvestTest(t)
	fakeHarvestCLI(t, nil)
	key := "sk-live-key"
	if zenSessionLive(key) {
		t.Fatal("key must not be live before harvesting")
	}
	if _, err := harvestSession(context.Background(), key); err != nil {
		t.Fatalf("harvestSession: %v", err)
	}
	if !zenSessionLive(key) {
		t.Fatal("key must be live after a successful harvest")
	}
}

// 未 mint 的 key 不得被 pickZenKey 选中，只要池里还有 live 的 key：
// 本地随机 sess_ 必 403，选中它等于白等一次上游往返（首启时 11 个 key 逐个
// 试过去就是把首个请求拖到分钟级的原因）。
func TestPickZenKeyPrefersLiveSessions(t *testing.T) {
	setupHarvestTest(t)
	cfg := getZenConfig()
	cfg2 := *cfg
	cfg2.Enabled = true
	cfg2.Keys = []string{"sk-dead-1", "sk-live-2", "sk-dead-3"}
	setZenConfig(&cfg2)
	t.Cleanup(func() { c := *cfg; setZenConfig(&c) })

	zenKeyMu.Lock()
	zenKeyCool = map[string]time.Time{}
	zenKeyIdx = 0
	zenKeyMu.Unlock()

	zenSessMu.Lock()
	zenSessions["sk-live-2"] = &zenSessionEntry{Session: "ses_minted", Minted: true}
	zenSessMu.Unlock()

	for i := 0; i < 5; i++ {
		if got := pickZenKey(); got != "sk-live-2" {
			t.Fatalf("pickZenKey = %q; must prefer the only live key (unminted keys always 403)", got)
		}
	}
}

// 池里全是未 mint 的 key 时仍须返回 key（首启窗口内收割机正在 mint，
// 请求必须发得出去；此时 403 是唯一可用信号）。
func TestPickZenKeyStillReturnsWhenNothingMinted(t *testing.T) {
	setupHarvestTest(t)
	cfg := getZenConfig()
	cfg2 := *cfg
	cfg2.Enabled = true
	cfg2.Keys = []string{"sk-a", "sk-b"}
	setZenConfig(&cfg2)
	t.Cleanup(func() { c := *cfg; setZenConfig(&c) })
	zenKeyMu.Lock()
	zenKeyCool = map[string]time.Time{}
	zenKeyMu.Unlock()

	got := pickZenKey()
	if got != "sk-a" && got != "sk-b" {
		t.Fatalf("pickZenKey = %q; must still hand out a key when none is minted", got)
	}
}

// mintZenSessions(force=false) 跳过已有 live 会话的 key，force=true 全部重 mint。
func TestMintZenSessionsSkipAndForce(t *testing.T) {
	setupHarvestTest(t)
	calls := fakeHarvestCLI(t, nil)
	keys := []string{"sk-1", "sk-2", "public"}

	first := mintZenSessions(context.Background(), keys, false, nil)
	if len(first) != 3 {
		t.Fatalf("got %d outcomes, want 3", len(first))
	}
	if !first[0].OK || !first[1].OK {
		t.Fatalf("real keys must mint: %+v", first[:2])
	}
	if !first[2].Skipped {
		t.Fatal(`"public" is a no-key sentinel and must be skipped`)
	}
	if n := atomic.LoadInt32(calls); n != 2 {
		t.Fatalf("CLI ran %d times, want 2 (one per real key)", n)
	}

	second := mintZenSessions(context.Background(), keys, false, nil)
	for _, o := range second[:2] {
		if !o.Skipped {
			t.Fatalf("non-force mint must skip keys that are already live: %+v", o)
		}
	}
	if n := atomic.LoadInt32(calls); n != 2 {
		t.Fatalf("non-force re-mint ran the CLI again (%d calls)", n)
	}

	forced := mintZenSessions(context.Background(), keys, true, nil)
	if !forced[0].OK || !forced[1].OK {
		t.Fatalf("force mint must re-mint live keys: %+v", forced[:2])
	}
	if n := atomic.LoadInt32(calls); n != 4 {
		t.Fatalf("force mint must run the CLI for both keys (got %d total calls)", n)
	}
}

// mint 失败不得覆盖既有 live 会话（面板「Force」按错也不至于把好会话弄丢）。
func TestMintFailureKeepsExistingSession(t *testing.T) {
	setupHarvestTest(t)
	key := "sk-keep-me"
	fakeHarvestCLI(t, nil)
	if _, err := harvestSession(context.Background(), key); err != nil {
		t.Fatalf("initial harvest: %v", err)
	}
	zenSessMu.Lock()
	good := zenSessions[key].Session
	zenSessMu.Unlock()

	// 之后所有 CLI 调用都不产出会话
	fakeHarvestCLI(t, map[string]bool{harvestHomeForKey(key): true})
	out := mintZenSessions(context.Background(), []string{key}, true, nil)
	if out[0].OK || out[0].Err == "" {
		t.Fatalf("failing mint must report an error: %+v", out[0])
	}
	zenSessMu.Lock()
	after := zenSessions[key].Session
	zenSessMu.Unlock()
	if after != good {
		t.Fatalf("failed mint replaced a working session: %s -> %s", good, after)
	}
}

// 并发上限必须生效：否则 11 个 key 会同时起 11 个 Bun 进程。
func TestMintRespectsConcurrencyLimit(t *testing.T) {
	setupHarvestTest(t)
	t.Setenv("ZEN_HARVEST_CONCURRENCY", "2")
	harvestSemOnce = sync.Once{}

	var inFlight, peak int32
	prev := harvestRunFn
	harvestRunFn = func(ctx context.Context, bin, home, model string) harvestRunResult {
		cur := atomic.AddInt32(&inFlight, 1)
		for {
			old := atomic.LoadInt32(&peak)
			if cur <= old || atomic.CompareAndSwapInt32(&peak, old, cur) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		writeFakeSessionLog(t, home, "ses_peak"+filepath.Base(home))
		return harvestRunResult{ExitCode: 0, Elapsed: 30 * time.Millisecond}
	}
	t.Cleanup(func() { harvestRunFn = prev })

	keys := []string{"sk-1", "sk-2", "sk-3", "sk-4", "sk-5", "sk-6"}
	mintZenSessions(context.Background(), keys, true, nil)
	if got := atomic.LoadInt32(&peak); got > 2 {
		t.Fatalf("peak concurrency = %d, want <= 2", got)
	}
	if got := atomic.LoadInt32(&peak); got < 2 {
		t.Fatalf("peak concurrency = %d; minting was effectively serial", got)
	}
}

// 面板端点：sessions 快照必须报出 live 计数与未 mint 的 key。
func TestZenSessionsSnapshot(t *testing.T) {
	setupHarvestTest(t)
	fakeHarvestCLI(t, nil)
	cfg := getZenConfig()
	cfg2 := *cfg
	cfg2.Enabled = true
	cfg2.Keys = []string{"sk-one", "sk-two"}
	setZenConfig(&cfg2)
	t.Cleanup(func() { c := *cfg; setZenConfig(&c) })

	if _, err := harvestSession(context.Background(), "sk-one"); err != nil {
		t.Fatalf("harvest: %v", err)
	}
	snaps := zenKeyStatus()
	if len(snaps) != 2 {
		t.Fatalf("got %d key states, want 2", len(snaps))
	}
	if snaps[0]["sessionLive"] != true {
		t.Fatalf("key #1 must report sessionLive after minting: %+v", snaps[0])
	}
	if snaps[1]["sessionLive"] != false {
		t.Fatalf("key #2 must report sessionLive=false before minting: %+v", snaps[1])
	}
}

// 非 force 且全部已 live 时，进度回调必须为每个 key 各报一次（skipped），
// 否则面板任务的 done 永远追不上 total，界面卡在 "minting N/M"。
func TestMintReportsSkippedProgress(t *testing.T) {
	setupHarvestTest(t)
	fakeHarvestCLI(t, nil)
	keys := []string{"sk-a", "sk-b", "public"}
	if out := mintZenSessions(context.Background(), keys, false, nil); !out[0].OK || !out[1].OK {
		t.Fatalf("setup mint failed: %+v", out)
	}

	got := map[int]zenMintOutcome{}
	all := mintZenSessions(context.Background(), keys, false, func(i int, o zenMintOutcome) { got[i] = o })
	if len(got) != len(all) {
		t.Fatalf("progress reported %d key(s), want %d", len(got), len(all))
	}
	for i, o := range all {
		if !got[i].Done {
			t.Fatalf("key %d never reported progress (panel would hang on pending)", i)
		}
		if !o.Skipped {
			t.Fatalf("key %d should be skipped when already live: %+v", i, o)
		}
	}
}

// 任务快照在启动瞬间就必须带好 index/keyMask：否则面板表格里未完成的槽位
// 是零值，全渲染成 "#1"。
func TestMintJobSnapshotPrefilled(t *testing.T) {
	setupHarvestTest(t)
	fakeHarvestCLI(t, nil)
	keys := []string{"sk-first", "sk-second"}
	started, snap := startZenMintJob(keys, false)
	if !started {
		t.Fatal("first job must start")
	}
	// 单飞：第二个任务不得抢占
	if again, _ := startZenMintJob(keys, true); again {
		t.Fatal("a second mint job must not start while one is running")
	}
	res, _ := snap["results"].([]map[string]any)
	if len(res) != 2 {
		t.Fatalf("snapshot has %d results, want 2", len(res))
	}
	for i, r := range res {
		if r["index"] != i {
			t.Fatalf("result %d has index %v; placeholders must be pre-filled", i, r["index"])
		}
		if r["keyMask"] == "" {
			t.Fatalf("result %d has an empty keyMask", i)
		}
	}
	waitZenMintJobDone(t)
}

func waitZenMintJobDone(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		st := zenMintJobStatus()
		if st != nil && st["finished"] == true {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("mint job did not finish in time")
}

// 刷新间隔必须短于 zen 的 5h 额度窗口：等于/超过窗口意味着每轮都有一段时间
// 全池会话已过期（请求先 403，再靠 harvestOnForbidden 逐个补救）。
func TestHarvestIntervalBelowQuotaWindow(t *testing.T) {
	t.Setenv("ZEN_HARVEST_INTERVAL_HOURS", "")
	if got := harvestInterval(); got != 4*time.Hour {
		t.Fatalf("default interval = %v, want 4h", got)
	}
	if got := harvestInterval(); got >= 5*time.Hour {
		t.Fatalf("default interval %v must stay under the 5h quota window", got)
	}
	// 显式配置仍然被尊重（大池子/小池子可以自己权衡）
	t.Setenv("ZEN_HARVEST_INTERVAL_HOURS", "2")
	if got := harvestInterval(); got != 2*time.Hour {
		t.Fatalf("explicit interval = %v, want 2h", got)
	}
	t.Setenv("ZEN_HARVEST_INTERVAL_HOURS", "99")
	if got := harvestInterval(); got != 99*time.Hour {
		t.Fatalf("explicit interval = %v, want 99h (operator's call)", got)
	}
	// 非法值回落到默认，而不是 0（0 会让 fresh > 0 && ... 的判断永远为真，
	// 每小时给每个 key 都收割一次）
	t.Setenv("ZEN_HARVEST_INTERVAL_HOURS", "abc")
	if got := harvestInterval(); got != 4*time.Hour {
		t.Fatalf("garbage interval = %v, want the 4h default", got)
	}
}

// 403 日志要能区分"从未 mint 的占位会话"（预期内）与"minted 会话被拒"
// （会话寿命到期的证据）——没有这个区分就无法回答"会话能活多久"。
func TestZenSessionDescDistinguishesExpiry(t *testing.T) {
	setupHarvestTest(t)
	key := "sk-desc"
	if got := zenSessionDesc(key); got != "no session" {
		t.Fatalf("unharvested key desc = %q", got)
	}

	zenSessMu.Lock()
	zenSessions[key] = &zenSessionEntry{Session: "sess_placeholder"}
	zenSessMu.Unlock()
	if got := zenSessionDesc(key); !contains(got, "placeholder") {
		t.Fatalf("placeholder desc = %q; must be distinguishable from a minted session", got)
	}

	zenSessMu.Lock()
	zenSessions[key] = &zenSessionEntry{Session: "ses_real", Minted: true, HarvestedAt: time.Now().Add(-5 * time.Hour).Unix()}
	zenSessMu.Unlock()
	got := zenSessionDesc(key)
	if !contains(got, "minted") || !contains(got, "ago") {
		t.Fatalf("minted desc = %q; must report the session age", got)
	}
}

// 定时补收的检查频率必须明显高于刷新间隔：间隔到与真正执行之间差一个 tick，
// 若两者同量级（4h 目标 + 1h ticker），实际刷新落在 4h-5h，顶到额度窗口边缘。
func TestPeriodicTickFinerThanInterval(t *testing.T) {
	tick := 10 * time.Minute
	for _, hours := range []int{1, 4, 6, 24} {
		t.Setenv("ZEN_HARVEST_INTERVAL_HOURS", fmt.Sprint(hours))
		iv := harvestInterval()
		if iv <= tick*4 {
			t.Fatalf("interval %v is too close to the %v tick; refresh timing would drift a whole tick", iv, tick)
		}
	}
	// 默认值必须小于 5h 额度窗口，且比 tick 粗得多
	t.Setenv("ZEN_HARVEST_INTERVAL_HOURS", "")
	def := harvestInterval()
	if def >= 5*time.Hour {
		t.Fatalf("default %v must stay under the 5h window", def)
	}
	if def < 2*time.Hour {
		t.Fatalf("default %v is needlessly aggressive for the shared IP quota", def)
	}
}

// ============ 审计回归用例（2026-09-18 会话 mint 审计） ============

// 从未 mint 成功的 key（本地随机占位）即使用户请求一直在刷新 Updated，
// 也必须留在周期补收名单里——否则"被请求过的 key 永不周期收割"，而这类 key
// 恰恰是一直 403 的那一类，只能等 403 阈值兜底。
func TestPeriodicSweepIncludesNeverMintedActiveKey(t *testing.T) {
	setupHarvestTest(t)
	interval := 4 * time.Hour
	active := "sk-active-placeholder"
	zenSessMu.Lock()
	zenSessions[active] = &zenSessionEntry{
		Session: "sess_placeholderplaceholder00",
		Updated: time.Now().Unix(), // 每次请求都刷新，正是旧基准踩的坑
	}
	zenSessMu.Unlock()

	got := periodicSweepCandidates([]string{active}, interval)
	if len(got) != 1 || got[0] != active {
		t.Fatalf("placeholder key with fresh Updated must still be swept, got %v", got)
	}
}

// 新鲜度只看 HarvestedAt：刚 mint 的跳过，超过 interval 的补收。
func TestPeriodicSweepUsesHarvestedAtNotUpdated(t *testing.T) {
	setupHarvestTest(t)
	interval := 4 * time.Hour
	fresh, stale := "sk-fresh", "sk-stale"
	zenSessMu.Lock()
	zenSessions[fresh] = &zenSessionEntry{Session: "ses_fresh", Minted: true, HarvestedAt: time.Now().Unix()}
	zenSessions[stale] = &zenSessionEntry{Session: "ses_stale", Minted: true, HarvestedAt: time.Now().Add(-5 * time.Hour).Unix()}
	zenSessMu.Unlock()

	got := periodicSweepCandidates([]string{fresh, stale}, interval)
	if len(got) != 1 || got[0] != stale {
		t.Fatalf("want only the over-interval key swept, got %v", got)
	}
}

// 从未 mint 成功的 key 按 interval 节流：刚试过就不该在下个 tick 再试，
// 否则持续失败的 key 会变成每 10 分钟一次的无限重试。
func TestPeriodicSweepThrottlesRepeatAttempts(t *testing.T) {
	setupHarvestTest(t)
	interval := 4 * time.Hour
	k := "sk-never-mints"
	if got := periodicSweepCandidates([]string{k}, interval); len(got) != 1 {
		t.Fatalf("first sweep should include %s, got %v", k, got)
	}
	markSweepTried([]string{k})
	if got := periodicSweepCandidates([]string{k}, interval); len(got) != 0 {
		t.Fatalf("sweep throttled by interval, got %v", got)
	}
	// 过了 interval 之后重新入选（把上次尝试时间往前挪，等价于等待）
	harvestMu.Lock()
	harvestLastSweep[k] = time.Now().Add(-interval - time.Minute)
	harvestMu.Unlock()
	if got := periodicSweepCandidates([]string{k}, interval); len(got) != 1 {
		t.Fatalf("sweep should retry after interval, got %v", got)
	}
}

// 配置里删掉的 key 不能顺带删掉它的 per-key 锁：在途 mint 正持有那把锁，
// 删了条目再 lockHarvestKey 会新建一把，同 key 就能并发 mint（共用 HOME，
// auth.json 与 CLI 日志互相覆盖）。
func TestPruneKeepsPerKeyMintLocks(t *testing.T) {
	setupHarvestTest(t)
	k := "sk-removed-while-minting"
	harvestMu.Lock()
	harvestFails[k] = 0
	harvestLastTry[k] = time.Now()
	harvestLastSweep[k] = time.Now()
	harvestMu.Unlock()

	unlock := lockHarvestKey(k) // 模拟在途 mint 持有该 key 的锁
	defer unlock()
	pruneZenKeyState(map[string]bool{}) // 该 key 已从配置移除

	harvestKeyMu.Lock()
	_, still := harvestKeyLocks[k]
	harvestKeyMu.Unlock()
	if !still {
		t.Fatal("prune removed a per-key mint lock that may be held in flight")
	}
	// 计数类状态照旧清理（这些没有"被持有"语义）
	harvestMu.Lock()
	_, fails := harvestFails[k]
	_, lastTry := harvestLastTry[k]
	_, lastSweep := harvestLastSweep[k]
	harvestMu.Unlock()
	if fails || lastTry || lastSweep {
		t.Fatalf("harvest counters not pruned: fails=%v lastTry=%v lastSweep=%v", fails, lastTry, lastSweep)
	}
}

// 旧版本会话文件没有 minted 字段：用会话 ID 前缀回认（CLI mint 的是 ses_*，
// 本地占位是 sess_*），避免升级后首启把整池 key 全量重 mint、白烧额度。
func TestLegacySessionFileMigration(t *testing.T) {
	dir := setupHarvestTest(t)
	now := time.Now().Unix()
	legacy := fmt.Sprintf(`{
  "sk-legacy-live": {"session": "ses_legacyminted000000000000", "ua": "opencode/1.18.31", "updated": %d},
  "sk-legacy-old": {"session": "ses_legacyancient00000000000", "ua": "opencode/1.18.31", "updated": %d},
  "sk-legacy-placeholder": {"session": "sess_placeholder000000000000", "ua": "opencode/1.18.31", "updated": %d}
}`, now, now-30*24*3600, now)
	if err := os.WriteFile(filepath.Join(dir, ".zen-sessions.json"), []byte(legacy), 0600); err != nil {
		t.Fatalf("write legacy file: %v", err)
	}
	zenSessMu.Lock()
	zenSessions = map[string]*zenSessionEntry{}
	zenSessLoaded = false
	zenSessPath = ""
	zenSessMu.Unlock()
	loadZenSessions()

	if !zenSessionLive("sk-legacy-live") {
		t.Fatal("legacy ses_* session must be recognised as CLI-minted (no re-mint on upgrade)")
	}
	if zenSessionLive("sk-legacy-placeholder") {
		t.Fatal("legacy sess_* placeholder must NOT be treated as live")
	}
	if !zenSessionLive("sk-legacy-old") {
		t.Fatal("aged legacy ses_* session is still CLI-minted (live), just old")
	}
	// 迁移时把收割时间认到 Updated：否则升级后第一次周期扫描会把整池重 mint
	zenSessMu.Lock()
	mig := zenSessions["sk-legacy-live"].HarvestedAt
	zenSessMu.Unlock()
	if mig != now {
		t.Fatalf("migrated entry should inherit Updated as HarvestedAt, got %d want %d", mig, now)
	}
	// 近期 mint 的迁移条目不该立刻被周期扫描重 mint；而 Updated 很旧的
	// （会话大概率已过期）和仍是占位的都必须入选。
	got := periodicSweepCandidates([]string{"sk-legacy-live", "sk-legacy-old", "sk-legacy-placeholder"}, 4*time.Hour)
	want := []string{"sk-legacy-old", "sk-legacy-placeholder"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sweep candidates = %v, want %v", got, want)
	}
	// 空 UA 回填：旧文件缺 ua 时请求会带空 UA，破坏指纹伪装
	zenSessMu.Lock()
	zenSessions["sk-legacy-live"].UA = ""
	zenSessMu.Unlock()
	_, _, ua := StickyZenIdentity("sk-legacy-live")
	if ua != zenNativeUA {
		t.Fatalf("empty UA must be backfilled, got %q", ua)
	}
}

// 坏文件先备份再空跑：否则紧随其后的 save 会直接覆盖，出问题时无从追查。
func TestCorruptSessionFileIsBackedUp(t *testing.T) {
	dir := setupHarvestTest(t)
	path := filepath.Join(dir, ".zen-sessions.json")
	if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}
	zenSessMu.Lock()
	zenSessions = map[string]*zenSessionEntry{}
	zenSessLoaded = false
	zenSessPath = ""
	zenSessMu.Unlock()
	loadZenSessions()

	if _, err := os.Stat(path + ".corrupt"); err != nil {
		t.Fatalf("corrupt session file must be kept as .corrupt: %v", err)
	}
	zenSessMu.Lock()
	n := len(zenSessions)
	zenSessMu.Unlock()
	if n != 0 {
		t.Fatalf("corrupt file must start with an empty table, got %d entries", n)
	}
}

// 掩码不得泄露短 key（kit.Truncate 对短串原样返回）。
func TestMaskZenKeyNeverLeaksShortKey(t *testing.T) {
	if m := maskZenKey("sk-1"); contains(m, "sk-1") {
		t.Fatalf("short key leaked in mask: %q", m)
	}
	if m := maskZenKey("sk-abcdefghijklmn"); m != "sk-abc…" {
		t.Fatalf("long key mask: got %q", m)
	}
	if m := maskZenKey(""); m != "-" {
		t.Fatalf("empty key mask: got %q", m)
	}
	if m := maskZenKey("public"); !contains(m, "public") {
		t.Fatalf("public sentinel mask: got %q", m)
	}
}

// 任务快照不得在锁外读取：worker goroutine 会在锁内写 job.outcomes，
// 锁外读同一份切片是数据竞争（go test -race 会命中）。
func TestMintJobStatusHasNoSnapshotRace(t *testing.T) {
	setupHarvestTest(t)
	fakeHarvestCLI(t, nil)
	keys := []string{"sk-race-1", "sk-race-2", "sk-race-3", "public"}

	if started, _ := startZenMintJob(keys, false); !started {
		t.Fatal("job did not start")
	}
	// 与任务并发地读状态 / 重复点击（后者会走"已有任务在跑"的快照分支）
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			zenMintJobStatus()
			startZenMintJob(keys, false)
		}
	}()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		st := zenMintJobStatus()
		if st != nil && st["running"] == false {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	<-done
	zenMintJobMu.Lock()
	finished := zenMintJobCur != nil && zenMintJobCur.finished
	zenMintJobMu.Unlock()
	if !finished {
		t.Fatal("mint job did not finish")
	}
}

// 默认并发必须是 1：Bun CLI 每次启动 burst 一大块 CPU/内存，1-2 核实例上并发
// 会把单次 mint 拖到慢于每 key 预算，表现为"全池 mint 失败"（2026-09-18 线上
// 事故：小实例 4 key 并发 5，全部 150s 超时无会话；同机并发 1 全部成功）。
func TestHarvestConcurrencyDefaultsToSerial(t *testing.T) {
	t.Setenv("ZEN_HARVEST_CONCURRENCY", "")
	if got := harvestConcurrency(); got != 1 {
		t.Fatalf("default harvest concurrency = %d, want 1 (serial)", got)
	}
	t.Setenv("ZEN_HARVEST_CONCURRENCY", "4")
	if got := harvestConcurrency(); got != 4 {
		t.Fatalf("explicit concurrency ignored: got %d", got)
	}
	t.Setenv("ZEN_HARVEST_CONCURRENCY", "99")
	if got := harvestConcurrency(); got != 8 {
		t.Fatalf("concurrency clamp broken: got %d", got)
	}
}

// 批次上限必须随 key 数放大：并发 1 时一批的耗时是 key 数 × 每 key 预算，
// 固定 15 分钟会把 11 个 key 的批次从中间砍断（后面的 key 连试都没试）。
func TestHarvestBatchTimeoutScalesWithKeyCount(t *testing.T) {
	setupHarvestTest(t)
	t.Setenv("ZEN_HARVEST_CONCURRENCY", "1")
	t.Setenv("ZEN_HARVEST_KEY_TIMEOUT_SECONDS", "150")
	perKey := harvestKeyBudget()

	if d := harvestBatchTimeout(11); d < 11*perKey {
		t.Fatalf("11-key serial batch timeout %v < 11 × %v — batch would be cut off mid-way", d, perKey)
	}
	if d := harvestBatchTimeout(1); d != 15*time.Minute {
		t.Fatalf("single-key batch timeout = %v, want the 15m floor", d)
	}
	if d := harvestBatchTimeout(500); d != 45*time.Minute {
		t.Fatalf("huge batch timeout = %v, want the 45m ceiling", d)
	}
	// 并发越高，批次数越少（同样的 key 数用更短的批次上限）
	t.Setenv("ZEN_HARVEST_CONCURRENCY", "4")
	if d4, d1 := harvestBatchTimeout(8), func() time.Duration {
		t.Setenv("ZEN_HARVEST_CONCURRENCY", "1")
		return harvestBatchTimeout(8)
	}(); d4 >= d1 {
		t.Fatalf("parallel batch timeout %v should be below serial %v", d4, d1)
	}
}

// CLI 输出会被写进日志与面板错误里，其中不得出现 key（CLI 报错时可能把
// auth 内容带出来）。
func TestHarvestOutputTailRedactsKeys(t *testing.T) {
	raw := "\x1b[91mError:\x1b[0m auth failed for sk-abcdefghijklmnopqrstuvwxyz012345\n  retry\n\n done"
	got := harvestOutputTail(raw)
	if contains(got, "sk-abcdefghijklmnopqrstuvwxyz012345") {
		t.Fatalf("key leaked into CLI output tail: %q", got)
	}
	if !contains(got, "sk-***") {
		t.Fatalf("key not redacted: %q", got)
	}
	if contains(got, "\x1b[") || contains(got, "\n") {
		t.Fatalf("control chars not stripped: %q", got)
	}
	if !contains(got, "auth failed") || !contains(got, "done") {
		t.Fatalf("diagnostic content lost: %q", got)
	}
	if long := harvestOutputTail(strings.Repeat("x", 5000)); len(long) > 420 {
		t.Fatalf("output tail not truncated: %d bytes", len(long))
	}
}

// mint 失败的错误信息必须转述 CLI 自己的话，否则线上只能看到
// "no session minted"——无法区分 CLI 起不来 / 被拖慢超时 / 上游拒绝。
func TestMintFailureSurfacesCLIOutput(t *testing.T) {
	setupHarvestTest(t)
	prev := harvestRunFn
	harvestRunFn = func(ctx context.Context, bin, home, model string) harvestRunResult {
		return harvestRunResult{ExitCode: 7, Output: "boom: cannot start cli", Err: fmt.Errorf("exit status 7")}
	}
	t.Cleanup(func() { harvestRunFn = prev })

	_, err := harvestSession(context.Background(), "sk-diag")
	if err == nil {
		t.Fatal("expected mint failure")
	}
	msg := err.Error()
	if !contains(msg, "boom: cannot start cli") || !contains(msg, "exit=7") {
		t.Fatalf("error does not carry the CLI's own output: %q", msg)
	}
}

// 一次 CLI 都没跑起来时，错误里绝不能出现 `exit=0 err=<nil>` 这种零值——那读
// 起来像"CLI 跑成功了但上游没给会话"，会把排查引到上游去。真实 run 与桩都会
// 留下 Elapsed，所以零值只可能意味着预算在首次尝试前就耗尽（批次超时 / 取消）。
func TestNoSessionErrDistinguishesNeverRan(t *testing.T) {
	never := harvestNoSessionErr([]string{"opencode/big-pickle"}, harvestRunResult{})
	if contains(never.Error(), "exit=0") || contains(never.Error(), "err=<nil>") {
		t.Fatalf("zero-value result rendered as a successful run: %q", never.Error())
	}
	if !contains(never.Error(), "no CLI run started") {
		t.Fatalf("never-ran case not named: %q", never.Error())
	}
	// 真的跑过（有 Elapsed）时仍要转述 CLI 的输出。
	ran := harvestNoSessionErr([]string{"opencode/big-pickle"},
		harvestRunResult{ExitCode: -1, Err: fmt.Errorf("signal: killed"), Elapsed: 60 * time.Second, Output: "killed by timeout"})
	if !contains(ran.Error(), "exit=-1") || !contains(ran.Error(), "killed by timeout") {
		t.Fatalf("real run lost its diagnostics: %q", ran.Error())
	}
}
