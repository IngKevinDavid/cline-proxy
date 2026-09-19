package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"cline-go-proxy/internal/kit"
)

// ============ Zen 免费模型管理 API ============

// GET /admin/api/zen/config
func handleZenConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: "method not allowed"})
		return
	}
	cfg := getZenConfig()
	data := map[string]any{
		"enabled":         cfg.Enabled,
		"key":             cfg.Key,
		"keys":            cfg.Keys,
		"keyStates":       zenKeyStatus(),
		"baseURL":         cfg.BaseURL,
		"proxies":         cfg.Proxies,
		"proxyStrategy":   cfg.ProxyStrategy,
		"maxConcurrency":  cfg.MaxConcurrency,
		"retries":         cfg.Retries,
		"failover":        cfg.Failover,
		"failoverCount":   cfg.FailoverCount,
		"failoverMinutes": cfg.FailoverMinutes,
		"compaction":      cfg.Compaction,
		"runtime": map[string]any{
			"failoverActive": zenFailedNow(),
			"proxyCooldowns": zenProxyCooldownStatus(),
		},
	}
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Data: data})
}

// POST /admin/api/zen/config/update
func handleZenConfigUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: "method not allowed"})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	defer r.Body.Close()

	cur := getZenConfig()
	var patch struct {
		Enabled         *bool    `json:"enabled"`
		Key             *string  `json:"key"`
		Keys            []string `json:"keys"`
		BaseURL         *string  `json:"baseURL"`
		Proxies         []string `json:"proxies"`
		ProxyStrategy   *string  `json:"proxyStrategy"`
		MaxConcurrency  *int     `json:"maxConcurrency"`
		Retries         *int     `json:"retries"`
		Failover        *bool    `json:"failover"`
		FailoverCount   *int     `json:"failoverCount"`
		FailoverMinutes *int     `json:"failoverMinutes"`
		Compaction      *struct {
			Auto         *bool   `json:"auto"`
			Buffer       *int    `json:"buffer"`
			KeepTokens   *int    `json:"keepTokens"`
			SummaryModel *string `json:"summaryModel"`
			MaxSummary   *int    `json:"maxSummary"`
		} `json:"compaction"`
	}
	if err := json.Unmarshal(body, &patch); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: "invalid JSON: " + err.Error()})
		return
	}
	next := &zenConfigData{
		Enabled:         cur.Enabled,
		Key:             cur.Key,
		Keys:            cur.Keys,
		BaseURL:         cur.BaseURL,
		Proxies:         cur.Proxies,
		ProxyStrategy:   cur.ProxyStrategy,
		MaxConcurrency:  cur.MaxConcurrency,
		Retries:         cur.Retries,
		Failover:        cur.Failover,
		FailoverCount:   cur.FailoverCount,
		FailoverMinutes: cur.FailoverMinutes,
		Compaction:      cur.Compaction,
	}
	if patch.Enabled != nil {
		next.Enabled = *patch.Enabled
	}
	if patch.Keys != nil {
		// 多 key 池整体替换；兼容旧单 key 字段（key 非空时视为单元素列表）
		if len(patch.Keys) == 0 && (patch.Key == nil || *patch.Key == "") {
			writeAPI(w, http.StatusBadRequest, apiResponse{Error: "keys list is empty"})
			return
		}
		if len(patch.Keys) > 0 {
			next.Keys = patch.Keys
		}
	}
	if patch.Key != nil && *patch.Key != "" && patch.Keys == nil {
		// 旧客户端单 key 提交 → 单元素池
		next.Keys = []string{*patch.Key}
	}
	if patch.BaseURL != nil && *patch.BaseURL != "" {
		next.BaseURL = strings.TrimRight(*patch.BaseURL, "/")
	}
	if patch.Proxies != nil {
		if err := validateProxyList(patch.Proxies); err != nil {
			writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
			return
		}
		next.Proxies = patch.Proxies
	}
	if patch.ProxyStrategy != nil && *patch.ProxyStrategy != "" {
		next.ProxyStrategy = *patch.ProxyStrategy
	}
	if patch.MaxConcurrency != nil && *patch.MaxConcurrency > 0 {
		next.MaxConcurrency = *patch.MaxConcurrency
	}
	if patch.Retries != nil && *patch.Retries >= 0 {
		next.Retries = *patch.Retries
	}
	if patch.Failover != nil {
		next.Failover = *patch.Failover
	}
	if patch.FailoverCount != nil && *patch.FailoverCount > 0 {
		next.FailoverCount = *patch.FailoverCount
	}
	if patch.FailoverMinutes != nil && *patch.FailoverMinutes > 0 {
		next.FailoverMinutes = *patch.FailoverMinutes
	}
	if patch.Compaction != nil {
		base := cur.Compaction
		if patch.Compaction.Buffer != nil {
			base.Buffer = *patch.Compaction.Buffer
		}
		if patch.Compaction.KeepTokens != nil {
			base.KeepTokens = *patch.Compaction.KeepTokens
		}
		if patch.Compaction.SummaryModel != nil {
			base.SummaryModel = *patch.Compaction.SummaryModel
		}
		if patch.Compaction.MaxSummary != nil {
			base.MaxSummary = *patch.Compaction.MaxSummary
		}
		if patch.Compaction.Auto != nil {
			base.Auto = *patch.Compaction.Auto
		}
		next.Compaction = base
	}
	setZenConfig(next)
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Data: getZenConfig()})
}

// GET /admin/api/opencode/models — 只返回免费模型
func handleZenModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: "method not allowed"})
		return
	}
	initZenModels()
	zenModelsMu.RLock()
	models := make([]map[string]any, 0, len(zenModels))
	for _, m := range zenModels {
		if !isZenFreeModel(m) {
			continue
		}
		models = append(models, map[string]any{
			"id":        m.ID,
			"aliases":   m.Aliases,
			"context":   m.Context,
			"output":    m.Output,
			"source":    m.Source,
			"upstream":  m.Upstream,
			"toolCall":  m.ToolCall,
			"reasoning": m.Reasoning,
			"attach":    m.Attach,
		})
	}
	zenModelsMu.RUnlock()
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{"models": models, "count": len(models)}})
}

// POST /admin/api/zen/models/refresh
func handleZenModelsRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: "method not allowed"})
		return
	}
	added, err := syncZenModels()
	if err != nil {
		writeAPI(w, http.StatusBadGateway, apiResponse{Error: "sync failed: " + err.Error()})
		return
	}
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Message: fmt.Sprintf("synced, %d new models", added)})
}

// GET /admin/api/zen/stats
func handleZenStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: "method not allowed"})
		return
	}
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Data: zenStatsSnapshot()})
}

// GET /admin/api/zen/sessions
// 每个 key 的 live 会话状态 + 当前（或最后一次）手动 mint 任务的进度。
func handleZenSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: "method not allowed"})
		return
	}
	keys := getZenConfig().Keys
	sessions := make([]map[string]any, 0, len(keys))
	live := 0
	for i, k := range keys {
		s := zenSessionSnapshotOf(k)
		if s.Live {
			live++
		}
		entry := map[string]any{
			"index":     i,
			"keyMask":   maskZenKey(k),
			"noKey":     k == "" || k == "public",
			"live":      s.Live,
			"minted":    s.Minted,
			"session":   s.Session,
			"harvested": "",
		}
		if s.HarvestedAt > 0 {
			entry["harvested"] = time.Unix(s.HarvestedAt, 0).Format(time.RFC3339)
		}
		sessions = append(sessions, entry)
	}
	data := map[string]any{
		"harvestEnabled":    harvestEnabled(),
		"concurrency":       harvestConcurrency(),
		"intervalHours":     int(harvestInterval() / time.Hour),
		"keyTimeoutSeconds": int(harvestKeyBudget() / time.Second),
		"liveCount":         live,
		"total":             len(keys),
		"sessions":          sessions,
		"job":               zenMintJobStatus(),
	}
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Data: data})
}

// POST /admin/api/zen/keys/test   body: {"index": 0}
// 对单个 zen key 发一个极小探测请求（与 cline 账号的 Test 按钮同语义）。
// 整个探测固定在该 key 上（zenCallOpts.pinKey），绝不影响正常轮转；成功即
// 清除该 key 的冷却——真实 2xx 是"该 key 现在可用"的最强证据，比干等上游的
// Retry-After 更可信。429 原样上报冷却与预计恢复时间（探测本身会让 key 重新
// 进入冷却，时长来自上游 Retry-After，上限 24h）；403 = 会话已死，探测已顺带
// 触发收割机，提示去 mint。返回的 status: active / cooldown / error。
func handleZenKeyTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: "method not allowed"})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	defer r.Body.Close()
	var req struct {
		Index int `json:"index"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: "invalid JSON"})
		return
	}
	cfg := getZenConfig()
	if req.Index < 0 || req.Index >= len(cfg.Keys) {
		writeAPI(w, http.StatusNotFound, apiResponse{Error: "key index out of range"})
		return
	}
	key := cfg.Keys[req.Index]
	if key == "" || key == "public" {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: "nothing to test: the anonymous public key has no credential"})
		return
	}

	result, status := testZenKey(key, req.Index)
	log.Printf("Test zen key #%d (%s): status=%s model=%s reason=%v",
		req.Index+1, maskZenKey(key), status, result["model"], result["reason"])

	writeAPI(w, http.StatusOK, apiResponse{
		Success: status == "active",
		Message: status,
		Data:    result,
	})
}

// testZenKey 执行单 key 探测：默认 zen 模型 + 一条 "Reply with exactly: OK"，
// 按模型的 Upstream 字段走 chat 或原生 responses 上游（与正常请求同一条路，
// 含 FreeTier gate、会话粘性与冷却副作用）。返回 (结果, 状态)。
func testZenKey(key string, index int) (map[string]any, string) {
	result := map[string]any{
		"index":   index,
		"keyMask": maskZenKey(key),
	}
	zm := zenProbeModel()
	if zm == nil {
		result["reason"] = "no zen model available to probe (model catalog empty)"
		return result, "error"
	}
	result["model"] = zm.ID

	params := map[string]any{
		"model":    zm.ID,
		"messages": []any{map[string]any{"role": "user", "content": "Reply with exactly: OK"}},
		// 上游 2xx 即探测成功；不追求可读内容，但太小会撞上推理模型的
		// 隐性预算（_finish_reason=length），64 足够容纳一个词。
		"max_tokens": 64,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	start := time.Now()
	var (
		resp *http.Response
		err  error
	)
	opts := []zenCallOpts{{pinKey: key}}
	if zm.Upstream == "responses" {
		resp, _, err = callZenResponsesAPI(ctx, params, true, opts...)
	} else {
		resp, _, err = callZenAPI(ctx, params, true, opts...)
	}
	result["latencyMs"] = time.Since(start).Milliseconds()

	he := (*zenHTTPError)(nil)
	if err != nil && errors.As(err, &he) {
		switch he.Status {
		case http.StatusTooManyRequests:
			// 调用链已对该 key 执行 cooldownZenKey(Retry-After 或 1min 默认)
			result["httpStatus"] = he.Status
			result["reason"] = "429 rate limited: " + kit.Truncate(he.Body, 300)
			if until, ok := zenKeyCooldownUntil(key); ok {
				result["cooldownUntil"] = until.UTC().Format(time.RFC3339)
				result["remaining"] = formatDuration(time.Until(until))
			}
			return result, "cooldown"
		case http.StatusForbidden:
			result["httpStatus"] = he.Status
			result["reason"] = "session rejected (403) — this key's session is no longer live; the harvester was just triggered, use the mint buttons below to retry now"
			return result, "error"
		default:
			result["httpStatus"] = he.Status
			result["reason"] = fmt.Sprintf("API %d: %s", he.Status, kit.Truncate(he.Body, 300))
			return result, "error"
		}
	}
	if err != nil {
		// 网络/超时：不冷却、不改状态（与 cline 的网络错误分支语义一致地保守）
		result["reason"] = "upstream call failed: " + err.Error()
		return result, "error"
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		result["httpStatus"] = resp.StatusCode
		result["reason"] = fmt.Sprintf("API %d: %s", resp.StatusCode, kit.Truncate(string(bodyBytes), 300))
		return result, "error"
	}

	// 成功：清除冷却 + 复位 403 连败计数（与 cline "测试成功即复位"一致），
	// 并计入该 key 的用量。
	uncoolZenKey(key)
	harvestMarkSuccess(key)
	markZenKeySuccess(key)
	result["reason"] = "ok"
	return result, "active"
}

// POST /admin/api/zen/sessions/mint
// 手动 mint 全池 live 会话（面板「Force mint/refresh live session ids」）。
// 后台执行，立即返回——11 个 key 全量重 mint 要几十秒，同步响应会撞反向代理超时。
// body: {"force": true} —— force=true 连已有 live 会话的 key 也重 mint；
// 缺省 false 只补未 mint 的 key。任务进行中重复调用返回当前进度（单飞）。
func handleZenSessionsMint(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: "method not allowed"})
		return
	}
	if !harvestEnabled() {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: "harvester unavailable: opencode CLI not present (ZEN_HARVEST_BIN)"})
		return
	}
	var body struct {
		Force *bool `json:"force"`
	}
	if r.Body != nil {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 4096))
		if err != nil {
			writeAPI(w, http.StatusBadRequest, apiResponse{Error: "read body: " + err.Error()})
			return
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				writeAPI(w, http.StatusBadRequest, apiResponse{Error: "invalid JSON: " + err.Error()})
				return
			}
		}
	}
	force := body.Force != nil && *body.Force
	started, state := startZenMintJob(getZenConfig().Keys, force)
	msg := "mint job started"
	if !started {
		msg = "mint job already running"
	}
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Message: msg, Data: state})
}
