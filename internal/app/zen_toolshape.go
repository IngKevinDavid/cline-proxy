package app

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed opencode_prompt_v2.txt
var opencodePromptV2 string

//go:embed opencode_tools.json
var opencodeToolsJSON []byte

type opencodeToolDef struct {
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

var (
	opencodeToolsOnce sync.Once
	opencodeToolsMap  map[string]opencodeToolDef
	v2OfficialToolOrder = []string{
		"edit", "execute", "glob", "grep", "question", "read", "shell",
		"skill", "subagent", "webfetch", "websearch", "write", "patch",
	}
)

const (
	ClientToolPrefix       = "clint_"
	clientToolDigestLength = 12
	toolWireNameLimit      = 64
	toolNameSeparators     = "_.- \t\r\n"
	opencodePromptMarker   = "You are opencode, an interactive CLI tool"
	v2CompletionLimit      = 32000
)

const opencodeSkills = `Skills provide specialized instructions and workflows for specific tasks.
Use the skill tool to load a skill when a task matches its description.
<available_skills>
  <skill>
    <name>customize-opencode</name>
    <description>Use ONLY when the user is editing or creating opencode's own configuration: opencode.json, opencode.jsonc, files under .opencode/, or files under ~/.config/opencode/. Also use when creating or fixing opencode agents, subagents, skills, plugins, MCP servers, or permission rules. Do not use for the user's own application code, or for any project that is not configuring opencode itself.</description>
    <location>&lt;built-in&gt;</location>
  </skill>
</available_skills>`

var (
	opencodeEnvOnce sync.Once
	opencodeWorkDir string
	globalRenames   sync.Map // wireName (string) -> clientName (string)
)

func getOpencodeTools() map[string]opencodeToolDef {
	opencodeToolsOnce.Do(func() {
		m := make(map[string]opencodeToolDef)
		if len(opencodeToolsJSON) > 0 {
			_ = json.Unmarshal(opencodeToolsJSON, &m)
		}
		opencodeToolsMap = m
	})
	return opencodeToolsMap
}

// ToolRenames tracks bidirectional name mappings between client tool names
// and wire names (clint_<hash12>).
type ToolRenames struct {
	mu       sync.RWMutex
	toWire   map[string]string
	toClient map[string]string
	byCase   map[string]string
	byShape  map[string]string
}

func NewToolRenames() *ToolRenames {
	return &ToolRenames{
		toWire:   make(map[string]string),
		toClient: make(map[string]string),
		byCase:   make(map[string]string),
		byShape:  make(map[string]string),
	}
}

func (r *ToolRenames) Empty() bool {
	if r == nil {
		return true
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.toWire) == 0
}

func (r *ToolRenames) Wire(client string) (string, bool) {
	if r == nil || client == "" {
		return "", false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	wire, ok := r.toWire[client]
	return wire, ok
}

func (r *ToolRenames) Client(wire string) (string, bool) {
	if r == nil || wire == "" {
		return "", false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	client, ok := r.toClient[wire]
	return client, ok
}

func (r *ToolRenames) Add(client, wire string) {
	if r == nil || client == "" || wire == "" || client == wire {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.toWire[client]; ok && existing != wire {
		return
	}
	if _, taken := r.toClient[wire]; taken {
		return
	}
	r.toWire[client] = wire
	r.toClient[wire] = client
	r.bindFolded(r.byCase, strings.ToLower(client), client)
	r.bindFolded(r.byShape, foldToolName(client), client)

	// Also store globally for transparent restoration in stream decoders
	globalRenames.Store(wire, client)
}

func (r *ToolRenames) bindFolded(folded map[string]string, key, client string) {
	if folded == nil || key == "" {
		return
	}
	if existing, ok := folded[key]; ok {
		if existing != client {
			folded[key] = ""
		}
		return
	}
	folded[key] = client
}

func foldToolName(name string) string {
	var builder strings.Builder
	for _, char := range strings.ToLower(name) {
		if !strings.ContainsRune(toolNameSeparators, char) {
			builder.WriteRune(char)
		}
	}
	return builder.String()
}

func (r *ToolRenames) Resolve(wire string) (string, bool) {
	if r == nil || wire == "" {
		return GlobalRestoreToolName(wire), false
	}
	if client, ok := r.Client(wire); ok {
		return client, client != wire
	}
	client := wire
	if rest, ok := strings.CutPrefix(wire, ClientToolPrefix); ok && rest != "" {
		client = rest
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if _, declared := r.toWire[client]; declared {
		return client, client != wire
	}
	lower := strings.ToLower(client)
	if match, ok := r.byCase[lower]; ok && match != "" {
		return match, match != wire
	}
	if match, ok := r.byShape[foldToolName(client)]; ok && match != "" {
		return match, match != wire
	}
	return GlobalRestoreToolName(wire), false
}

func (r *ToolRenames) Restore(wire string) string {
	client, _ := r.Resolve(wire)
	return client
}

// ClientToolWireName produces an opaque wire name (clint_ + 12 hex digest)
func ClientToolWireName(name string, used map[string]bool) string {
	if used == nil {
		used = map[string]bool{}
	}
	digest := sha256.Sum256([]byte(name))
	clean := hex.EncodeToString(digest[:])[:clientToolDigestLength]
	max := toolWireNameLimit - len(ClientToolPrefix)
	if len(clean) > max {
		clean = clean[:max]
	}
	base := ClientToolPrefix + clean
	if !used[base] {
		used[base] = true
		return base
	}
	for suffix := 2; ; suffix++ {
		tag := "_" + strconv.Itoa(suffix)
		cut := toolWireNameLimit - len(tag)
		if cut > len(base) {
			cut = len(base)
		}
		candidate := base[:cut] + tag
		if !used[candidate] {
			used[candidate] = true
			return candidate
		}
	}
}

// GlobalRestoreToolName looks up the original tool name globally
func GlobalRestoreToolName(wire string) string {
	if wire == "" {
		return wire
	}
	if val, ok := globalRenames.Load(wire); ok {
		if s, ok := val.(string); ok && s != "" {
			return s
		}
	}
	// Fallback: if wire starts with clint_, strip it
	if strings.HasPrefix(wire, ClientToolPrefix) {
		trimmed := strings.TrimPrefix(wire, ClientToolPrefix)
		if trimmed != "" {
			return trimmed
		}
	}
	return wire
}

// RememberToolRename stores a mapping in the global registry
func RememberToolRename(wire, client string) {
	if wire != "" && client != "" {
		globalRenames.Store(wire, client)
	}
}

func opencodeEnvironment() string {
	opencodeEnvOnce.Do(func() {
		dir, err := os.Getwd()
		if err != nil || dir == "" {
			dir = "/workspace"
		}
		opencodeWorkDir = filepath.ToSlash(dir)
	})
	now := time.Now().UTC().Format(time.RFC3339)
	var builder strings.Builder
	builder.WriteString("<env>\n")
	builder.WriteString("  Working directory: ")
	builder.WriteString(opencodeWorkDir)
	builder.WriteString("\n")
	builder.WriteString("  Platform: ")
	builder.WriteString(runtime.GOOS)
	builder.WriteString("\n")
	builder.WriteString("  Today's date: ")
	builder.WriteString(now[:10])
	builder.WriteString("\n")
	builder.WriteString("</env>")
	return builder.String()
}

func opencodeFullPrompt() string {
	env := opencodeEnvironment()
	return opencodePromptV2 + "\n\n" + env + "\n\n" + opencodeSkills
}

func v2ShellDescription(base string) string {
	if runtime.GOOS != "windows" {
		return base
	}
	return strings.Replace(base, "on Linux using bash", "on Windows using powershell", 1)
}

func officialV2ToolsChat() []any {
	toolsDef := getOpencodeTools()
	tools := make([]any, 0, len(v2OfficialToolOrder))
	for _, name := range v2OfficialToolOrder {
		desc := "Agent tool " + name
		params := map[string]any{"type": "object", "properties": map[string]any{}}
		if def, ok := toolsDef[name]; ok {
			desc = def.Description
			if def.Parameters != nil {
				params = def.Parameters
			}
		}
		if name == "shell" {
			desc = v2ShellDescription(desc)
		}
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        name,
				"description": desc,
				"parameters":  params,
				"strict":      false,
			},
		})
	}
	return tools
}

func officialV2ToolsResponses() []any {
	toolsDef := getOpencodeTools()
	tools := make([]any, 0, len(v2OfficialToolOrder))
	for _, name := range v2OfficialToolOrder {
		desc := "Agent tool " + name
		params := map[string]any{"type": "object", "properties": map[string]any{}}
		if def, ok := toolsDef[name]; ok {
			desc = def.Description
			if def.Parameters != nil {
				params = def.Parameters
			}
		}
		if name == "shell" {
			desc = v2ShellDescription(desc)
		}
		tools = append(tools, map[string]any{
			"type":        "function",
			"name":        name,
			"description": desc,
			"parameters":  params,
			"strict":      false,
		})
	}
	return tools
}

func carriesOpenCodePrompt(content any) bool {
	switch v := content.(type) {
	case string:
		return strings.Contains(v, opencodePromptMarker)
	case []any:
		for _, part := range v {
			if m, ok := part.(map[string]any); ok {
				if text, ok := m["text"].(string); ok && strings.Contains(text, opencodePromptMarker) {
					return true
				}
			}
		}
	}
	return false
}

func injectOfficialChatPrompt(payload map[string]any) {
	messages, ok := payload["messages"].([]any)
	if !ok {
		return
	}
	// Check if already carries prompt
	for _, raw := range messages {
		if m, ok := raw.(map[string]any); ok {
			role, _ := m["role"].(string)
			if role == "system" || role == "developer" {
				if carriesOpenCodePrompt(m["content"]) {
					return
				}
			}
		}
	}

	kept := make([]any, 0, len(messages)+1)
	var instructions []string
	for _, raw := range messages {
		m, ok := raw.(map[string]any)
		if !ok {
			kept = append(kept, raw)
			continue
		}
		role, _ := m["role"].(string)
		if role != "system" && role != "developer" {
			kept = append(kept, raw)
			continue
		}
		switch c := m["content"].(type) {
		case string:
			if strings.TrimSpace(c) != "" {
				instructions = append(instructions, strings.TrimSpace(c))
			}
		case []any:
			for _, part := range c {
				if pm, ok := part.(map[string]any); ok {
					if text, ok := pm["text"].(string); ok && strings.TrimSpace(text) != "" {
						instructions = append(instructions, strings.TrimSpace(text))
					}
				}
			}
		}
	}

	system := opencodeFullPrompt()
	if len(instructions) > 0 {
		system += "\n\n" + strings.Join(instructions, "\n\n")
	}
	payload["messages"] = append([]any{map[string]any{"role": "system", "content": system}}, kept...)
}

// ShapeZenChatRequest shapes chat requests to conform strictly to OpenCode v2 standards:
// 1. Injects official system prompt with <env> and skills if not present.
// 2. Obfuscates client tools to clint_<hash12> and appends the 13 official tools.
// 3. Rewrites past messages and tool_choice.
// 4. Ensures streaming and completion ceilings.
func ShapeZenChatRequest(payload map[string]any) *ToolRenames {
	renames := NewToolRenames()
	used := map[string]bool{}

	// 1. Ensure stream & stream_options
	payload["stream"] = true
	if _, ok := payload["stream_options"]; !ok {
		payload["stream_options"] = map[string]any{"include_usage": true}
	}
	if _, ok := payload["max_completion_tokens"]; !ok {
		if _, ok := payload["max_tokens"]; !ok {
			payload["max_completion_tokens"] = v2CompletionLimit
		}
	}

	// 2. Inject system prompt
	injectOfficialChatPrompt(payload)

	// 3. Obfuscate client tools and append 13 official tools
	var clientTools []any
	if raw, ok := payload["tools"].([]any); ok {
		clientTools = raw
	}
	if legacy, ok := payload["functions"].([]any); ok {
		for _, rawFn := range legacy {
			if fn, ok := rawFn.(map[string]any); ok {
				clientTools = append(clientTools, map[string]any{
					"type":     "function",
					"function": fn,
				})
			}
		}
		delete(payload, "functions")
		delete(payload, "function_call")
	}

	shapedTools := make([]any, 0, len(clientTools)+len(v2OfficialToolOrder))
	for _, item := range clientTools {
		entry, ok := item.(map[string]any)
		if !ok {
			shapedTools = append(shapedTools, item)
			continue
		}
		fn, ok := entry["function"].(map[string]any)
		if !ok || fn == nil {
			shapedTools = append(shapedTools, item)
			continue
		}
		name, _ := fn["name"].(string)
		if name == "" {
			shapedTools = append(shapedTools, item)
			continue
		}
		wireName := ClientToolWireName(name, used)
		renames.Add(name, wireName)
		fn["name"] = wireName
		fn["strict"] = false
		shapedTools = append(shapedTools, entry)
	}

	// Append official 13 tools
	shapedTools = append(shapedTools, officialV2ToolsChat()...)
	payload["tools"] = shapedTools

	// 4. Rewrite tool references in tool_choice and message history
	if choice, ok := payload["tool_choice"].(map[string]any); ok {
		if function, ok := choice["function"].(map[string]any); ok {
			if n, ok := function["name"].(string); ok {
				if w, ok := renames.Wire(n); ok {
					function["name"] = w
				}
			}
		}
	}
	if messages, ok := payload["messages"].([]any); ok {
		for _, rawMsg := range messages {
			msg, ok := rawMsg.(map[string]any)
			if !ok {
				continue
			}
			if toolCalls, ok := msg["tool_calls"].([]any); ok {
				for _, rawCall := range toolCalls {
					if call, ok := rawCall.(map[string]any); ok {
						if fn, ok := call["function"].(map[string]any); ok {
							if n, ok := fn["name"].(string); ok {
								if w, ok := renames.Wire(n); ok {
									fn["name"] = w
								}
							}
						}
					}
				}
			}
		}
	}

	return renames
}

// ShapeZenResponsesRequest shapes responses endpoint requests.
func ShapeZenResponsesRequest(payload map[string]any) *ToolRenames {
	renames := NewToolRenames()
	used := map[string]bool{}

	payload["stream"] = true
	if _, ok := payload["stream_options"]; !ok {
		payload["stream_options"] = map[string]any{"include_usage": true}
	}

	var clientTools []any
	if raw, ok := payload["tools"].([]any); ok {
		clientTools = raw
	}

	shapedTools := make([]any, 0, len(clientTools)+len(v2OfficialToolOrder))
	for _, item := range clientTools {
		entry, ok := item.(map[string]any)
		if !ok {
			shapedTools = append(shapedTools, item)
			continue
		}
		name, _ := entry["name"].(string)
		if name == "" {
			shapedTools = append(shapedTools, item)
			continue
		}
		wireName := ClientToolWireName(name, used)
		renames.Add(name, wireName)
		entry["name"] = wireName
		entry["strict"] = false
		shapedTools = append(shapedTools, entry)
	}

	shapedTools = append(shapedTools, officialV2ToolsResponses()...)
	payload["tools"] = shapedTools

	return renames
}

// RestoreToolNameInChoice restores obfuscated tool names in choices/deltas.
func RestoreToolNameInChoice(choice map[string]any, renames *ToolRenames) {
	for _, key := range []string{"message", "delta"} {
		if container, ok := choice[key].(map[string]any); ok {
			if toolCalls, ok := container["tool_calls"].([]any); ok {
				for _, rawCall := range toolCalls {
					if call, ok := rawCall.(map[string]any); ok {
						if fn, ok := call["function"].(map[string]any); ok {
							if n, ok := fn["name"].(string); ok && n != "" {
								if renames != nil {
									fn["name"] = renames.Restore(n)
								} else {
									fn["name"] = GlobalRestoreToolName(n)
								}
							}
						}
					}
				}
			}
		}
	}
}
