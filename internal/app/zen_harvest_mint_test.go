package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
	harvestRunFn = func(ctx context.Context, bin, home, model string) {
		atomic.AddInt32(&n, 1)
		if failFor[home] {
			return // 不写日志行 = mint 失败
		}
		writeFakeSessionLog(t, home, fmt.Sprintf("ses_fake%d%010d", atomic.LoadInt32(&n), time.Now().UnixNano()%1e10))
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
	t.Setenv("ZEN_HARVEST_CONCURRENCY", "3")
	t.Setenv("DATA_DIR", dir)

	zenSessMu.Lock()
	zenSessions = map[string]*zenSessionEntry{}
	zenSessLoaded = true
	zenSessPath = ""
	zenSessMu.Unlock()
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
	harvestRunFn = func(ctx context.Context, bin, home, model string) {
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
