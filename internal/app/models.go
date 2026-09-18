package app

import (
	"cline-go-proxy/internal/cline"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

type ModelStatus string

const (
	ModelActive  ModelStatus = "active"
	ModelRemoved ModelStatus = "removed"
	ModelUnknown ModelStatus = "unknown"
)

type ModelInfo struct {
	ID             string      `json:"id"`
	Name           string      `json:"name,omitempty"`
	Source         string      `json:"source"`
	Provider       string      `json:"provider"`
	Cost           string      `json:"cost"`
	Status         ModelStatus `json:"status"`
	RequiresStream bool        `json:"requiresStream,omitempty"`
	SyncedAt       time.Time   `json:"syncedAt,omitempty"`
}

var (
	modelsMu       sync.Mutex
	modelsCache    map[string]*ModelInfo
	modelsSyncing  bool
	modelsLastSync time.Time
)

const (
	modelsRefreshInterval = 60 * time.Second
	modelsSyncTimeout     = 25 * time.Second
)

const recommendedModelsURL = cline.ClineAPIBase + "/ai/cline/recommended-models"

// seedModelCandidates 冷启动兜底：仅在首次成功同步之前（或官方 feed 不可达）
// 作为可服务的免费集。live 列表可用后以 live 为准，这些条目同样会被修剪，
// 因此这里是"当前已知免费模型"的快照，不是永久清单。
// Status=active：兜底条目本来就是可用模型，getDefaultModel 才能在没有 live
// 结果时选出一个真正可用的默认模型（否则回退值是空字符串）。
// 2026-09-18 快照（= 官方 recommended-models 的 free 列表，容器内实测 5 个）。
func seedModelCandidates() []*ModelInfo {
	return []*ModelInfo{
		{ID: "cline-free/deepseek-v4.1-flash", Source: "free", Provider: "cline-free", Cost: "free", Status: ModelActive, RequiresStream: true},
		{ID: "cline-free/muse-spark-1.3-contributor", Source: "free", Provider: "cline-free", Cost: "free", Status: ModelActive, RequiresStream: true},
		{ID: "cline-free/solar-pro4", Source: "free", Provider: "cline-free", Cost: "free", Status: ModelActive, RequiresStream: true},
		{ID: "z-ai/glm-5.3-flash", Source: "free", Provider: "z-ai", Cost: "free", Status: ModelActive, RequiresStream: true},
		{ID: "poolside/laguna-s-2.1:free", Source: "free", Provider: "poolside", Cost: "free", Status: ModelActive},
	}
}

func initModelsCache() {
	modelsMu.Lock()
	defer modelsMu.Unlock()
	if modelsCache != nil {
		return
	}
	modelsCache = make(map[string]*ModelInfo)
	for _, m := range seedModelCandidates() {
		modelsCache[m.ID] = m
	}
}

func getFreeModels() []*ModelInfo {
	initModelsCache()
	modelsMu.Lock()
	defer modelsMu.Unlock()
	out := make([]*ModelInfo, 0, len(modelsCache))
	for _, m := range modelsCache {
		cp := *m
		out = append(out, &cp)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			if out[j-1].ID < out[j].ID {
				break
			}
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

type recommendedPayload struct {
	Free []struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
	} `json:"free"`
}

func syncRecommendedModels() (int, error) {
	initModelsCache()

	// 优先活跃账号；全部冷却时退回任意持凭证账号——模型列表与推理配额无关
	// （实测 429 期间该接口仍 200），否则模型列表会长期停留在种子兜底状态
	acc := pickAccount()
	if acc == nil {
		acc = pickAccountAny()
	}
	if acc == nil {
		return 0, fmt.Errorf("no accounts")
	}
	token, err := ensureAccountToken(acc)
	if err != nil {
		return 0, err
	}

	req, err := http.NewRequest("GET", recommendedModelsURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header = clineHeaders(token, "")
	req.Header.Set("X-Task-ID", fmt.Sprintf("sess_sync_%d", time.Now().UnixMilli()))

	client := &http.Client{Timeout: modelsSyncTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var payload recommendedPayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return 0, err
	}

	modelsMu.Lock()
	defer modelsMu.Unlock()

	live := make(map[string]bool, len(payload.Free))
	added := 0
	for _, m := range payload.Free {
		id := m.ID
		provider := id
		if i := indexByte(id, '/'); i >= 0 {
			provider = id[:i]
		}
		live[id] = true
		if cached, ok := modelsCache[id]; ok {
			cached.Source = "free"
			cached.Cost = "free"
			cached.Provider = provider
			cached.Status = ModelActive
			cached.SyncedAt = time.Now()
			if cached.Name == "" {
				cached.Name = m.Name
			}
			continue
		}
		modelsCache[id] = &ModelInfo{
			ID:             id,
			Name:           m.Name,
			Source:         "free",
			Provider:       provider,
			Cost:           "free",
			Status:         ModelActive,
			RequiresStream: indexByte(id, ':') < 0,
			SyncedAt:       time.Now(),
		}
		added++
	}

	// 修剪已从官方 feed 下线的模型，避免 /v1/models 长期展示死模型。
	// 种子同样在修剪范围内：它只是"首次成功同步之前"的冷启动兜底，live 列表
	// 可用后以 live 为准（否则 stepfun/step-3.7-flash 这类早已不在免费 feed
	// 里的种子会永久挂着）。
	// 防御: feed 短暂为空/残缺时不清空本地列表 —— live 数量不足现有模型
	// 一半时跳过本轮修剪。
	pruneEligible := len(modelsCache)
	pruned := 0
	if len(live) > 0 && len(live)*2 >= pruneEligible {
		for id := range modelsCache {
			if !live[id] {
				delete(modelsCache, id)
				pruned++
			}
		}
	} else if pruneEligible > 0 {
		log.Printf("model sync: live feed too small (%d live vs %d cached), skipping prune", len(live), pruneEligible)
	}
	if pruned > 0 {
		log.Printf("model sync: pruned %d model(s) no longer on official feed", pruned)
	}

	modelsLastSync = time.Now()
	return added, nil
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func syncModelsOnce() {
	initModelsCache()
	modelsMu.Lock()
	if modelsSyncing {
		modelsMu.Unlock()
		return
	}
	modelsSyncing = true
	modelsMu.Unlock()
	defer func() {
		modelsMu.Lock()
		modelsSyncing = false
		modelsMu.Unlock()
	}()

	added, err := syncRecommendedModels()
	if err != nil {
		log.Printf("  model sync: failed (%v), using cached list", err)
		return
	}
	if added > 0 {
		log.Printf("  model sync: %d new free models from official feed", added)
	} else {
		log.Printf("  model sync: %d free models up to date", len(getFreeModels()))
	}
}

// getDefaultModel 默认模型：面板里设置的偏好（pool.DefaultModel）优先，
// 其次取 live 列表里排序最小的可用模型（确定性 —— 之前是 map 随机序，
// 修剪掉偏好模型后会随机漂移到别的模型，客户端看到的行为不可预期）。
func getDefaultModel() string {
	initModelsCache()
	modelsMu.Lock()
	defer modelsMu.Unlock()

	if m, ok := modelsCache[defaultModel]; ok && m.Status == ModelActive {
		return defaultModel
	}
	best := ""
	for id, m := range modelsCache {
		if m.Status != ModelActive {
			continue
		}
		if best == "" || id < best {
			best = id
		}
	}
	if best != "" {
		return best
	}
	return defaultModel
}

func normalizeRequestModel(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return getDefaultModel()
	}
	initModelsCache()
	modelsMu.Lock()
	_, ok := modelsCache[id]
	modelsMu.Unlock()
	if ok {
		return id
	}
	log.Printf("  model %q not in free list, fallback to %q", id, getDefaultModel())
	return getDefaultModel()
}

func modelInFreeList(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return true
	}
	initModelsCache()
	modelsMu.Lock()
	_, ok := modelsCache[id]
	modelsMu.Unlock()
	return ok
}

// strictModelGate STRICT_MODEL_MATCH 开启时，对既不在 cline 免费模型表、
// 也不是 zen 模型的名字返回错误提示；空串表示放行。zen 模型必须放行：
// zen 故障转移时会把 zen 免费模型路由到 cline 池，由 normalizeRequestModel
// 兜底为默认模型 —— 这是故障转移的既有机制。combo 别名由调用方先改写。
func strictModelGate(model string) string {
	if !StrictModelMatchEnv() || modelInFreeList(model) {
		return ""
	}
	initZenModels()
	if _, ok := resolveZenModel(model); ok {
		return ""
	}
	return fmt.Sprintf("model %q is not available on this gateway (see /v1/models for the model list). "+
		"Set STRICT_MODEL_MATCH=false to fall back to the default model instead", model)
}

func apiModelList() []map[string]any {
	out := make([]map[string]any, 0, len(modelsCache))
	for _, m := range getFreeModels() {
		out = append(out, map[string]any{
			"id":             m.ID,
			"object":         "model",
			"created":        time.Now().UnixMilli(),
			"owned_by":       m.Provider,
			"source":         m.Source,
			"status":         m.Status,
			"cost":           m.Cost,
			"requiresStream": m.RequiresStream,
			"syncedAt":       m.SyncedAt,
		})
	}
	return out
}

func ensureModelsFresh() {
	initModelsCache()
	modelsMu.Lock()
	needSync := modelsLastSync.IsZero() || time.Since(modelsLastSync) > modelsRefreshInterval
	syncing := modelsSyncing
	modelsMu.Unlock()
	if needSync && !syncing {
		go syncModelsOnce()
	}
}

func startModelsRefresher() {
	go func() {
		syncModelsOnce()
		ticker := time.NewTicker(modelsRefreshInterval)
		for range ticker.C {
			syncModelsOnce()
		}
	}()
}
