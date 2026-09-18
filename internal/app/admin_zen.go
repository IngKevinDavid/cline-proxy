package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
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
		"harvestEnabled": harvestEnabled(),
		"concurrency":    harvestConcurrency(),
		"intervalHours":  int(harvestInterval() / time.Hour),
		"liveCount":      live,
		"total":          len(keys),
		"sessions":       sessions,
		"job":            zenMintJobStatus(),
	}
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Data: data})
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
