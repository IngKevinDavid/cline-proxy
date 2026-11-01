package app

import (
	"strings"
	"testing"
)

// 目录可达时价格门是唯一权威：免费集合原样采纳，不掺种子、不看名字后缀。
func TestZenDesiredFromLiveRegistryWins(t *testing.T) {
	free := map[string]bool{
		"big-pickle":                      true, // 无 free 后缀但 cost 0/0
		"mimo-v2.5-free":                  true,
		"muse-spark-1.3-contributor-free": true,
	}
	// CLI 列表里还有个已 unlisted 的免费模型 + 一个付费模型
	cli := []string{"opencode/big-pickle", "opencode/mimo-v2.5-free", "opencode/ghost-free", "opencode/gpt-5.5"}
	got, err := zenDesiredFromLive(true, free, cli)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != len(free) {
		t.Fatalf("desired = %v, want exactly the registry free set %v", got, free)
	}
	for id := range free {
		if !got[id] {
			t.Errorf("registry-free model %q missing from desired", id)
		}
	}
	if got["ghost-free"] {
		t.Error("CLI-only id must not be added when the registry is reachable (registry is the authority)")
	}
	if got["gpt-5.5"] {
		t.Error("paid model leaked into the free catalog")
	}
}

// 目录不可达：用 opencode models 成员表 + "-free"/big-pickle 启发式（fail-open）。
func TestZenDesiredFromLiveCLIFallback(t *testing.T) {
	cli := []string{
		"opencode/big-pickle",     // 无后缀但已知免费别名
		"opencode/mimo-v2.5-free", // 后缀命中
		"opencode/gpt-5.5",        // 付费：必须排除
		"",                        // 空行
	}
	got, err := zenDesiredFromLive(false, nil, cli)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got["big-pickle"] || !got["mimo-v2.5-free"] {
		t.Fatalf("desired = %v, want big-pickle + mimo-v2.5-free", got)
	}
	if got["gpt-5.5"] {
		t.Error("paid id must not pass the fallback filter")
	}
	if len(got) != 2 {
		t.Fatalf("desired = %v, want exactly 2 entries", got)
	}
}

// 两个来源都不可用：报错（调用方保留旧表），绝不能把空集当成"没有免费模型"。
func TestZenDesiredFromLiveNoSources(t *testing.T) {
	if _, err := zenDesiredFromLive(false, nil, nil); err == nil {
		t.Fatal("expected error when registry is down and CLI gave nothing")
	}
	if _, err := zenDesiredFromLive(true, map[string]bool{}, nil); err == nil {
		t.Fatal("expected error when registry returned no free models and CLI gave nothing")
	}
}

// 目录可达但免费集合为空（接口异常）时，应退回 CLI 而不是清空目录。
func TestZenDesiredFromLiveEmptyRegistryFallsBackToCLI(t *testing.T) {
	got, err := zenDesiredFromLive(true, map[string]bool{}, []string{"opencode/ling-3.0-flash-fin-free"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got["ling-3.0-flash-fin-free"] {
		t.Fatalf("desired = %v, want the CLI free entry", got)
	}
}

// live 列表是权威：不在其中的条目一律移除（含种子），而不是只升级来源。
func TestZenPruneRemovesStaleSeedEntries(t *testing.T) {
	zenModelsMu.Lock()
	savedModels, savedAliases := zenModels, zenAliases
	zenModels = map[string]*ZenModel{
		"mimo-v2.5-free":         {ID: "mimo-v2.5-free", Source: "seed", Aliases: []string{"mimo"}},
		"deepseek-v4-flash-free": {ID: "deepseek-v4-flash-free", Source: "seed"}, // 已 deprecated
		"big-pickle":             {ID: "big-pickle", Source: "seed"},
	}
	zenAliases = map[string]*ZenModel{"mimo": zenModels["mimo-v2.5-free"]}
	zenModelsMu.Unlock()
	defer func() {
		zenModelsMu.Lock()
		zenModels, zenAliases = savedModels, savedAliases
		zenModelsMu.Unlock()
	}()

	desired := map[string]bool{"mimo-v2.5-free": true, "big-pickle": true}
	pruneZenModelsTo(desired)

	zenModelsMu.RLock()
	defer zenModelsMu.RUnlock()
	if _, ok := zenModels["deepseek-v4-flash-free"]; ok {
		t.Error("deprecated seed survived a live sync (should be pruned)")
	}
	if _, ok := zenModels["mimo-v2.5-free"]; !ok {
		t.Error("live model was pruned")
	}
	if _, ok := zenAliases["mimo"]; !ok {
		t.Error("alias of a surviving model must be kept")
	}
}

// 残表防御：live 结果明显偏小（接口抖动）时不得清空。
func TestZenPruneSkipsOnSuspiciouslySmallLiveList(t *testing.T) {
	zenModelsMu.Lock()
	savedModels, savedAliases := zenModels, zenAliases
	zenModels = map[string]*ZenModel{}
	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		zenModels[id] = &ZenModel{ID: id, Source: "seed"}
	}
	zenAliases = map[string]*ZenModel{}
	zenModelsMu.Unlock()
	defer func() {
		zenModelsMu.Lock()
		zenModels, zenAliases = savedModels, savedAliases
		zenModelsMu.Unlock()
	}()

	pruneZenModelsTo(map[string]bool{"a": true}) // 1 live vs 6 known

	zenModelsMu.RLock()
	defer zenModelsMu.RUnlock()
	if len(zenModels) != 6 {
		t.Fatalf("table size = %d, want 6 (small live list must not prune)", len(zenModels))
	}
}

// 种子兜底：没有 live 来源时表里仍是种子（离线可用），且种子里的别名可解析。
func TestZenSeedBootstrapAliasesResolve(t *testing.T) {
	initZenModels()
	m, ok := resolveZenFreeModel("mimo")
	if !ok {
		t.Fatal("alias mimo must resolve to a free zen model")
	}
	if m.ID != "mimo-v2.6-flash-free" {
		t.Fatalf("alias mimo -> %q, want mimo-v2.6-flash-free", m.ID)
	}
	if !strings.HasSuffix(m.ID, "-free") && m.ID != "big-pickle" {
		t.Fatalf("resolved %q is not a free-looking id", m.ID)
	}
}


// 真实物理限额保护：上游 models.opencode.ai 声明保守的 200k 元数据，
// applyZenCatalog 必须使用实测验证的真实物理窗口（DeepSeek 1,048,576、GLM 900k 等），
// 防止 IDE/Agent 发生过早的上下文压缩。
func TestZenApplyCatalogPreservesRealLimits(t *testing.T) {
	zenModelsMu.Lock()
	savedModels, savedAliases := zenModels, zenAliases
	zenModels = map[string]*ZenModel{
		"deepseek-v4.1-flash-free": {
			ID:      "deepseek-v4.1-flash-free",
			Aliases: []string{"deepseek"},
			Context: 1048576,
			Output:  32000,
			Source:  "seed",
		},
	}
	zenAliases = map[string]*ZenModel{
		"deepseek": zenModels["deepseek-v4.1-flash-free"],
	}
	zenModelsMu.Unlock()
	defer func() {
		zenModelsMu.Lock()
		zenModels, zenAliases = savedModels, savedAliases
		zenModelsMu.Unlock()
	}()

	desired := map[string]bool{
		"deepseek-v4.1-flash-free": true,
		"glm-5.3-flash-free":       true,
		"step-5-preview-free":      true,
	}
	overlay := map[string]zenModelOverlay{
		// 模拟 models.opencode.ai 下发的保守元数据 (200k)
		"deepseek-v4.1-flash-free": {Context: 200000, Output: 8192, ToolCall: true, Reasoning: true},
		"glm-5.3-flash-free":       {Context: 200000, Output: 8192, ToolCall: true, Reasoning: true},
		"step-5-preview-free":      {Context: 200000, Output: 8192, ToolCall: true, Reasoning: true},
	}

	applyZenCatalog(desired, overlay)

	zenModelsMu.RLock()
	defer zenModelsMu.RUnlock()

	ds := zenModels["deepseek-v4.1-flash-free"]
	if ds == nil {
		t.Fatal("deepseek-v4.1-flash-free missing from zenModels")
	}
	if ds.Context != 1048576 {
		t.Fatalf("deepseek context = %d, want real limit 1048576 (not the conservative 200k)", ds.Context)
	}
	if ds.Output != 32000 {
		t.Fatalf("deepseek output = %d, want real limit 32000", ds.Output)
	}

	// 验证别名解析也指向具有真实物理限额的实例
	if aliasModel, ok := zenAliases["deepseek"]; !ok || aliasModel.Context != 1048576 {
		t.Fatalf("deepseek alias context = %v, want 1048576", aliasModel)
	}

	glm := zenModels["glm-5.3-flash-free"]
	if glm == nil || glm.Context != 900000 {
		t.Fatalf("glm-5.3-flash-free context = %v, want real limit 900000", glm)
	}

	step := zenModels["step-5-preview-free"]
	if step == nil || step.Context != 1000000 {
		t.Fatalf("step-5-preview-free context = %v, want real limit 1000000", step)
	}
}
