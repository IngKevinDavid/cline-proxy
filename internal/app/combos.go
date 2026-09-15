package app

import (
	"cline-go-proxy/internal/kit"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Combo 仪表盘自定义的别名模型：/v1 请求 model 命中 combo ID 时，
// 上游改写为同平台的 target 模型。严格同平台：cline combo 只能选 cline
// 模型列表中的模型，zen combo 只能选 zen 免费模型，保存时校验。
type Combo struct {
	ID         string    `json:"id"`
	Platform   string    `json:"platform"` // cline | zen
	Target     string    `json:"target"`
	UseProxies bool      `json:"useProxies,omitempty"` // 该别名的上游调用走出口代理池（round-robin）
	CreatedAt  time.Time `json:"createdAt"`
}

var (
	combosMu     sync.Mutex
	combosLoaded bool
	combosList   []*Combo
)

var comboIDRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{1,63}$`)

func combosPath() string { return kit.ResolveDataPath("combos.json") }

func loadCombosLocked() {
	if combosLoaded {
		return
	}
	combosLoaded = true
	data, err := os.ReadFile(combosPath())
	if err != nil {
		return
	}
	var list []*Combo
	if json.Unmarshal(data, &list) == nil {
		for _, c := range list {
			if c != nil && c.ID != "" && c.Target != "" {
				combosList = append(combosList, c)
			}
		}
	}
}

func saveCombosLocked() {
	data, _ := json.MarshalIndent(combosList, "", "  ")
	if err := os.WriteFile(combosPath(), data, 0600); err != nil {
		log.Printf("combos save failed: %v", err)
	}
}

// listCombos 返回全部 combo（副本语义，只读使用）。
func listCombos() []*Combo {
	combosMu.Lock()
	defer combosMu.Unlock()
	loadCombosLocked()
	out := make([]*Combo, len(combosList))
	copy(out, combosList)
	return out
}

// resolveCombo 查找 combo；请求路由与 /v1/models 展示用。
func resolveCombo(id string) *Combo {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	combosMu.Lock()
	defer combosMu.Unlock()
	loadCombosLocked()
	for _, c := range combosList {
		if c.ID == id {
			return c
		}
	}
	return nil
}

// validateCombo 保存前校验：
//   - ID 格式合法（URL 安全）且不与任何真实模型 ID 冲突
//   - platform ∈ {cline, zen}
//   - target 必须存在于对应平台的模型列表（严格同平台，禁止跨平台选择）
func validateCombo(id, platform, target string) error {
	if !comboIDRe.MatchString(id) {
		return fmt.Errorf("invalid combo id %q: use 2-64 chars, letters/digits/._- and start with letter or digit", id)
	}
	if platform != "cline" && platform != "zen" {
		return fmt.Errorf("invalid platform %q: must be cline or zen", platform)
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return fmt.Errorf("target model is required")
	}
	// ID 不得与真实模型冲突（含 zen 别名与 opencode/ 前缀形式）
	initModelsCache()
	modelsMu.Lock()
	_, inCline := modelsCache[id]
	modelsMu.Unlock()
	if inCline {
		return fmt.Errorf("combo id %q conflicts with a real cline model", id)
	}
	if _, isZen := resolveZenModel(id); isZen {
		return fmt.Errorf("combo id %q conflicts with a real zen model", id)
	}
	if _, isZen := resolveZenModel(strings.TrimPrefix(id, "opencode/")); isZen && strings.HasPrefix(id, "opencode/") {
		return fmt.Errorf("combo id %q conflicts with a real zen model", id)
	}
	if resolveCombo(id) != nil {
		return fmt.Errorf("combo id %q already exists", id)
	}

	switch platform {
	case "cline":
		initModelsCache()
		modelsMu.Lock()
		_, ok := modelsCache[target]
		modelsMu.Unlock()
		if !ok {
			return fmt.Errorf("target %q is not in the cline model list (cross-platform selection is not allowed)", target)
		}
	case "zen":
		if _, ok := resolveZenFreeModel(target); !ok {
			return fmt.Errorf("target %q is not a free zen model (cross-platform selection is not allowed)", target)
		}
	}
	return nil
}

// addCombo 校验并持久化新 combo。
func addCombo(id, platform, target string, useProxies bool) (*Combo, error) {
	id = strings.TrimSpace(id)
	target = strings.TrimSpace(target)
	if err := validateCombo(id, platform, target); err != nil {
		return nil, err
	}
	c := &Combo{ID: id, Platform: platform, Target: target, UseProxies: useProxies, CreatedAt: time.Now()}
	combosMu.Lock()
	defer combosMu.Unlock()
	loadCombosLocked()
	combosList = append(combosList, c)
	saveCombosLocked()
	return c, nil
}

// deleteCombo 按 ID 删除，返回是否删除了条目。
func deleteCombo(id string) bool {
	id = strings.TrimSpace(id)
	combosMu.Lock()
	defer combosMu.Unlock()
	loadCombosLocked()
	for i, c := range combosList {
		if c.ID == id {
			combosList = append(combosList[:i], combosList[i+1:]...)
			saveCombosLocked()
			return true
		}
	}
	return false
}

// ============ 管理 API ============

// GET /admin/api/combos
func handleCombosList(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: "method not allowed"})
		return
	}
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{"combos": listCombos()}})
}

// POST /admin/api/combos  body: { id, platform, target }
func handleComboCreate(w http.ResponseWriter, r *http.Request) {
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
		ID         string `json:"id"`
		Platform   string `json:"platform"`
		Target     string `json:"target"`
		UseProxies bool   `json:"useProxies"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: "invalid JSON"})
		return
	}
	c, err := addCombo(req.ID, req.Platform, req.Target, req.UseProxies)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	log.Printf("combo created: %s -> %s/%s", c.ID, c.Platform, c.Target)
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Message: "combo created", Data: c})
}

// POST /admin/api/combos/delete  body: { id }
func handleComboDelete(w http.ResponseWriter, r *http.Request) {
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
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: "invalid JSON"})
		return
	}
	if !deleteCombo(req.ID) {
		writeAPI(w, http.StatusNotFound, apiResponse{Error: "combo not found: " + req.ID})
		return
	}
	log.Printf("combo deleted: %s", req.ID)
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Message: "combo deleted"})
}
