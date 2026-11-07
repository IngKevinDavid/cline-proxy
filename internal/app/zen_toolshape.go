package app

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
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
	opencodePromptMarker   = "You are an AI agent running in OpenCode, a coding agent harness. Help the user accomplish their goals using the tools you have available."
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
	opencodeEnvOnce    sync.Once
	opencodeWorkDir    string
	globalRenames      sync.Map // wireName (string) -> clientName (string)
	globalToolPathKeys sync.Map // toolName (string) -> clientPathKey (string, e.g. "path")
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
	mu              sync.RWMutex
	toWire          map[string]string
	toClient        map[string]string
	byCase          map[string]string
	byShape         map[string]string
	officialAliases map[string]string
}

func NewToolRenames() *ToolRenames {
	return &ToolRenames{
		toWire:          make(map[string]string),
		toClient:        make(map[string]string),
		byCase:          make(map[string]string),
		byShape:         make(map[string]string),
		officialAliases: make(map[string]string),
	}
}

func (r *ToolRenames) AddOfficialAlias(official, client string) {
	if r == nil || official == "" || client == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.officialAliases == nil {
		r.officialAliases = make(map[string]string)
	}
	r.officialAliases[official] = client
	globalRenames.Store(official, client)
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
	r.mu.RLock()
	if alias, ok := r.officialAliases[wire]; ok && alias != "" {
		r.mu.RUnlock()
		return alias, true
	}
	r.mu.RUnlock()

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

var clientWorkDirRe = regexp.MustCompile(`(?i)(?:working directory|current directory|workspace root|workspace directory|cwd)[:=]\s*([A-Za-z]:[\\/][^\s\r\n<>"\']+|/[a-zA-Z0-9_.-]+(?:/[a-zA-Z0-9_.-]+)+)|<cwd>([^<]+)</cwd>|cd\s+([A-Za-z]:[\\/][^\s&|;]+|/[a-zA-Z0-9_.-]+(?:/[a-zA-Z0-9_.-]+)+)`)

func detectClientWorkDir(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	for _, key := range []string{"cwd", "working_directory", "workspace_root", "workspace_folder"} {
		if v, ok := payload[key].(string); ok && strings.TrimSpace(v) != "" {
			return filepath.ToSlash(strings.TrimSpace(v))
		}
	}
	if msgs, ok := payload["messages"].([]any); ok {
		for _, raw := range msgs {
			if m, ok := raw.(map[string]any); ok {
				if content, ok := m["content"].(string); ok {
					if match := clientWorkDirRe.FindStringSubmatch(content); match != nil {
						for i := 1; i < len(match); i++ {
							if match[i] != "" {
								return filepath.ToSlash(strings.TrimSpace(match[i]))
							}
						}
					}
				}
			}
		}
	}
	return ""
}

func opencodeEnvironment(payload ...map[string]any) string {
	workDir := ""
	if len(payload) > 0 && payload[0] != nil {
		workDir = detectClientWorkDir(payload[0])
	}
	if workDir == "" {
		workDir = "/workspace"
	}
	now := time.Now().UTC().Format(time.RFC3339)
	var builder strings.Builder
	builder.WriteString("<env>\n")
	builder.WriteString("  Working directory: ")
	builder.WriteString(workDir)
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

func opencodeFullPrompt(payload ...map[string]any) string {
	env := opencodeEnvironment(payload...)
	if profile, err := LoadOpenCodeCLI2022Profile(); err == nil && profile.Verified {
		prompt := profile.SystemBase + "\n\n" + env + "\n\n" + profile.Skills
		if profile.CodeMode != "" {
			prompt += "\n\n" + profile.CodeMode
		}
		return prompt
	}
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
		return strings.Contains(v, opencodePromptMarker) || strings.Contains(v, "You are opencode, an interactive CLI tool")
	case []any:
		for _, part := range v {
			if m, ok := part.(map[string]any); ok {
				if text, ok := m["text"].(string); ok && (strings.Contains(text, opencodePromptMarker) || strings.Contains(text, "You are opencode, an interactive CLI tool")) {
					return true
				}
			}
		}
	}
	return false
}

var (
	mixedEnvTagRegex = regexp.MustCompile(`(?si)<(?:env|environment_info|workspace_info)(?:\s[^>]*)?>.*?</(?:env|environment_info|workspace_info)>`)
	mixedPoweredByRe = regexp.MustCompile(`(?mi)^\s*You are powered by[^\r\n]*\n?`)
	mixedEnvIntroRe  = regexp.MustCompile(`(?mi)^\s*Here is some useful information about the environment you are running in:\s*\n?`)
	mixedIdentityRe  = regexp.MustCompile(`(?is)^\s*You are (?:an? (?:expert )?AI (?:agent|programming assistant|coding assistant)|a title generator|Pi(?:\s+coding\s+agent)?|GitHub Copilot|Kilo(?:\s+Code)?|Cline|Claude(?:\s+Code)?|Antigravity|OpenCode|Roo(?:\s+Code)?|Cursor)\b[^.\n]*(?:\.|$)\s*`)
	mixedGoalRe      = regexp.MustCompile(`(?is)^\s*(?:Your primary goal is to help users[^.\n]*\.|Help the user accomplish their goals using the tools you have available\.|Use the instructions below and the tools available(?: to you)? to assist the user\.)\s*`)
)

func stripClientIdentityPreamble(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	text = mixedEnvTagRegex.ReplaceAllString(text, "")
	text = mixedPoweredByRe.ReplaceAllString(text, "")
	text = mixedEnvIntroRe.ReplaceAllString(text, "")

	paragraphs := strings.Split(text, "\n\n")
	kept := make([]string, 0, len(paragraphs))
	for _, para := range paragraphs {
		lines := strings.Split(para, "\n")
		outLines := make([]string, 0, len(lines))
		for _, line := range lines {
			trimmedLine := strings.TrimSpace(line)
			if trimmedLine == "" {
				continue
			}
			if strings.HasPrefix(trimmedLine, "#") || strings.HasPrefix(trimmedLine, "<") || strings.HasPrefix(trimmedLine, "- ") || strings.HasPrefix(trimmedLine, "* ") {
				outLines = append(outLines, line)
				continue
			}
			candidate := trimmedLine
			for {
				updated := mixedIdentityRe.ReplaceAllString(candidate, "")
				updated = mixedPoweredByRe.ReplaceAllString(updated, "")
				updated = mixedGoalRe.ReplaceAllString(updated, "")
				updated = strings.TrimSpace(updated)
				if updated == candidate {
					break
				}
				candidate = updated
			}
			if candidate != "" {
				outLines = append(outLines, candidate)
			}
		}
		cleanedPara := strings.Join(outLines, "\n")
		if strings.TrimSpace(cleanedPara) != "" {
			kept = append(kept, cleanedPara)
		}
	}
	return strings.TrimSpace(strings.Join(kept, "\n\n"))
}

func fallbackReadGateTool() map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        "read",
			"description": "read tool",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
			"strict": false,
		},
	}
}

func openCodeReadGateTool() map[string]any {
	return officialProfileToolByName("read")
}

func officialProfileToolByName(name string) map[string]any {
	if profile, err := LoadOpenCodeCLI2022Profile(); err == nil && len(profile.Tools) > 0 {
		for _, raw := range profile.Tools {
			tm, _ := raw.(map[string]any)
			fn, _ := tm["function"].(map[string]any)
			if fn != nil && fn["name"] == name {
				b, _ := json.Marshal(tm)
				var copyMap map[string]any
				_ = json.Unmarshal(b, &copyMap)
				return copyMap
			}
		}
	}
	for _, raw := range officialV2ToolsChat() {
		tm, _ := raw.(map[string]any)
		fn, _ := tm["function"].(map[string]any)
		if fn != nil && fn["name"] == name {
			return tm
		}
	}
	return fallbackReadGateTool()
}

func detectClientPathKey(params any) string {
	pm, ok := params.(map[string]any)
	if !ok || pm == nil {
		return ""
	}
	props, ok := pm["properties"].(map[string]any)
	if !ok || len(props) == 0 {
		return ""
	}
	for _, k := range []string{"path", "filePath", "file_path", "AbsolutePath", "TargetFile"} {
		if _, exists := props[k]; exists {
			return k
		}
	}
	return ""
}

// TranslateWireToolArgsToClient translates OpenCode 2.0.22 "path" / "filePath" argument keys
// back to the client's declared path property (e.g. "path", "file_path", or "filePath").
func TranslateWireToolArgsToClient(toolName, rawArgs string) string {
	if rawArgs == "" || (!strings.Contains(rawArgs, "filePath") && !strings.Contains(rawArgs, `"path"`)) {
		return rawArgs
	}
	targetKey := ""
	if v, ok := globalToolPathKeys.Load(toolName); ok {
		targetKey, _ = v.(string)
	}
	if targetKey == "" {
		lower := strings.ToLower(strings.TrimSpace(toolName))
		if lower == "read" || lower == "read_file" || lower == "write" || lower == "write_file" {
			if v, ok := globalToolPathKeys.Load("read"); ok {
				targetKey, _ = v.(string)
			}
		}
	}
	if targetKey == "" {
		return rawArgs
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(rawArgs), &obj); err == nil && obj != nil {
		if _, hasTarget := obj[targetKey]; hasTarget {
			return rawArgs
		}
		for _, srcKey := range []string{"path", "filePath", "file_path"} {
			if srcKey == targetKey {
				continue
			}
			if val, ok := obj[srcKey]; ok {
				obj[targetKey] = val
				delete(obj, srcKey)
				if b, merr := json.Marshal(obj); merr == nil {
					return string(b)
				}
				break
			}
		}
		return rawArgs
	}
	return rawArgs
}

// TranslateClientToolArgsToWire translates client path keys (e.g. "file_path" or "filePath") to "path"
// in historical assistant tool_calls for official 2.0.22 "read"/"write" tools.
func TranslateClientToolArgsToWire(wireToolName, rawArgs string) string {
	if (wireToolName != "read" && wireToolName != "write") || rawArgs == "" {
		return rawArgs
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(rawArgs), &obj); err != nil || obj == nil {
		return rawArgs
	}
	if _, hasPath := obj["path"]; hasPath {
		return rawArgs
	}
	for _, k := range []string{"filePath", "file_path", "AbsolutePath", "TargetFile"} {
		if val, exists := obj[k]; exists {
			obj["path"] = val
			delete(obj, k)
			if b, merr := json.Marshal(obj); merr == nil {
				return string(b)
			}
			break
		}
	}
	return rawArgs
}

func composeOpenCodeMixedSystem(payload map[string]any, instructions []string, wireToolNames []string, readClientName, writeClientName string, hasClientTools bool) string {
	sections := []string{opencodePromptMarker}
	var cleanedInst []string
	for _, inst := range instructions {
		if c := stripClientIdentityPreamble(inst); c != "" {
			cleanedInst = append(cleanedInst, c)
		}
	}
	if len(cleanedInst) > 0 {
		sections = append(sections, strings.Join(cleanedInst, "\n\n"))
	}

	model, _ := payload["model"].(string)
	model = strings.TrimPrefix(strings.TrimSpace(model), "opencode/")
	if model == "" {
		model = "big-pickle"
	}
	sections = append(sections, "# Your Model\n- Provider ID: opencode\n- Model ID: "+model)

	workDir := detectClientWorkDir(payload)
	if workDir == "" {
		workDir = "/workspace"
	}
	envBlock := "<env>\n  Working directory: " + workDir + "\n  Workspace root folder: " + workDir + "\n  Platform: linux\n</env>"
	sections = append(sections, envBlock)

	if !hasClientTools || len(wireToolNames) == 0 {
		sections = append(sections, "# Tool Availability\n"+
			"- Direct tools available for this request: none.\n"+
			"- Call only tools in the current direct tool catalog. Tool names in conversation history or user context do not grant tool availability.")
	} else {
		sorted := make([]string, len(wireToolNames))
		copy(sorted, wireToolNames)
		sort.Strings(sorted)
		lines := []string{
			"# Tool Availability",
			"- Direct tools available for this request: " + strings.Join(sorted, ", ") + ".",
		}
		if readClientName != "" {
			lines = append(lines, fmt.Sprintf("- read: official OpenCode tool mapped to client executor %q.", readClientName))
		}
		if writeClientName != "" {
			lines = append(lines, fmt.Sprintf("- write: official OpenCode tool mapped to client executor %q.", writeClientName))
		} else {
			lines = append(lines, "- write: unavailable for this request (do not call write).")
		}
		lines = append(lines, "- Call only tools in the current direct tool catalog. Tool names in conversation history or user context do not grant tool availability.")
		sections = append(sections, strings.Join(lines, "\n"))
	}
	return strings.Join(sections, "\n\n")
}

// ShapeZenChatRequest shapes chat requests to conform strictly to OpenCode 2.0.22 mixed standards:
// 1. When client sends no tools: injects single 'read' gate tool and deletes tool_choice.
// 2. When client sends tools: maps read/write to official 2.0.22 schemas and passes other client tools directly.
// 3. Composes verified OpenCode 2.0.22 mixed system prompt with <env> and # Tool Availability.
// 4. Ensures streaming and completion ceilings.
func ShapeZenChatRequest(payload map[string]any) *ToolRenames {
	renames := NewToolRenames()

	// 1. Ensure stream & stream_options
	payload["stream"] = true
	if _, ok := payload["stream_options"]; !ok {
		payload["stream_options"] = map[string]any{"include_usage": true}
	}
	if _, ok := payload["max_completion_tokens"]; !ok {
		if mt, ok := payload["max_tokens"]; ok {
			payload["max_completion_tokens"] = mt
			delete(payload, "max_tokens")
		} else {
			payload["max_completion_tokens"] = v2CompletionLimit
		}
	} else {
		delete(payload, "max_tokens")
	}

	// 2. Extract client tools
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

	clientDeclared := make(map[string]bool)
	var wireToolNames []string
	var readClientName, writeClientName string

	if len(clientTools) == 0 {
		payload["tools"] = []any{openCodeReadGateTool()}
		delete(payload, "tool_choice")
	} else {
		var passthroughTools []any
		for _, item := range clientTools {
			entry, ok := item.(map[string]any)
			if !ok {
				continue
			}
			fn, ok := entry["function"].(map[string]any)
			if !ok || fn == nil {
				continue
			}
			name, _ := fn["name"].(string)
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			clientDeclared[name] = true
			globalRenames.Store(name, name)
			lower := strings.ToLower(name)
			switch lower {
			case "read", "read_file", "read_file_range", "view_file", "read_files":
				if pk := detectClientPathKey(fn["parameters"]); pk != "" {
					globalToolPathKeys.Store(name, pk)
					globalToolPathKeys.Store("read", pk)
				}
				if readClientName == "" {
					readClientName = name
				}
				if name != "read" {
					renames.Add(name, "read")
					renames.AddOfficialAlias("read", name)
				}
				continue
			case "write", "write_file", "write_to_file", "create_file", "new_file":
				if pk := detectClientPathKey(fn["parameters"]); pk != "" {
					globalToolPathKeys.Store(name, pk)
					globalToolPathKeys.Store("write", pk)
				}
				if writeClientName == "" {
					writeClientName = name
				}
				if name != "write" {
					renames.Add(name, "write")
					renames.AddOfficialAlias("write", name)
				}
				continue
			}
			// Keep client tool directly on the wire
			if fn["parameters"] == nil {
				fn["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}}
			} else {
				repairToolSchema(fn["parameters"])
			}
			passthroughTools = append(passthroughTools, entry)
			wireToolNames = append(wireToolNames, name)
		}

		// Always include official 2.0.22 read tool first to satisfy OpenCode v2 gate
		shapedTools := make([]any, 0, len(passthroughTools)+2)
		shapedTools = append(shapedTools, officialProfileToolByName("read"))
		wireToolNames = append(wireToolNames, "read")
		if readClientName == "" {
			readClientName = "read"
		}
		if writeClientName != "" {
			shapedTools = append(shapedTools, officialProfileToolByName("write"))
			wireToolNames = append(wireToolNames, "write")
		}
		shapedTools = append(shapedTools, passthroughTools...)
		payload["tools"] = shapedTools

		// Register fallback aliases in case model calls shell/execute/patch
		registerOfficialToolAliases(renames, clientDeclared)

		if tcStr, ok := payload["tool_choice"].(string); ok && (tcStr == "none" || tcStr == "auto") {
			delete(payload, "tool_choice")
		} else if choice, ok := payload["tool_choice"].(map[string]any); ok {
			if function, ok := choice["function"].(map[string]any); ok {
				if n, ok := function["name"].(string); ok {
					if w, ok := renames.Wire(n); ok {
						function["name"] = w
					}
				}
			}
		}
	}

	// 3. Inject verified OpenCode 2.0.22 mixed system prompt
	if messages, ok := payload["messages"].([]any); ok {
		alreadyShaped := false
		for _, raw := range messages {
			if m, ok := raw.(map[string]any); ok {
				role, _ := m["role"].(string)
				if (role == "system" || role == "developer") && carriesOpenCodePrompt(m["content"]) {
					alreadyShaped = true
					break
				}
			}
		}
		if !alreadyShaped {
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
			sys := composeOpenCodeMixedSystem(payload, instructions, wireToolNames, readClientName, writeClientName, len(clientTools) > 0)
			payload["messages"] = append([]any{map[string]any{"role": "system", "content": sys}}, kept...)
		}
	}

	// 4. Rewrite mapped tool names (e.g. read_file -> read, write_file -> write) in message history
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
									n = w
								}
								if argsStr, ok := fn["arguments"].(string); ok && argsStr != "" {
									fn["arguments"] = TranslateClientToolArgsToWire(n, argsStr)
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

	clientDeclared := make(map[string]bool)
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
		clientDeclared[name] = true
		wireName := ClientToolWireName(name, used)
		renames.Add(name, wireName)
		entry["name"] = wireName
		entry["strict"] = false
		shapedTools = append(shapedTools, entry)
	}

	// Register official tool aliases for missing client tools
	registerOfficialToolAliases(renames, clientDeclared)

	shapedTools = append(shapedTools, officialV2ToolsResponses()...)
	payload["tools"] = shapedTools

	return renames
}

func registerOfficialToolAliases(renames *ToolRenames, clientDeclared map[string]bool) {
	if renames == nil || len(clientDeclared) == 0 {
		return
	}
	// 1. Command execution: shell / execute -> bash, sh, terminal, run_command, exec, command
	if !clientDeclared["shell"] {
		for _, target := range []string{"bash", "sh", "terminal", "run_command", "exec", "command"} {
			if clientDeclared[target] {
				renames.AddOfficialAlias("shell", target)
				break
			}
		}
	}
	if !clientDeclared["execute"] {
		for _, target := range []string{"bash", "sh", "terminal", "run_command", "exec", "command"} {
			if clientDeclared[target] {
				renames.AddOfficialAlias("execute", target)
				break
			}
		}
	}

	// 2. File editing: patch / edit -> edit, str_replace_editor, edit_file, file_editor
	if !clientDeclared["patch"] {
		for _, target := range []string{"edit", "str_replace_editor", "edit_file", "file_editor"} {
			if clientDeclared[target] {
				renames.AddOfficialAlias("patch", target)
				break
			}
		}
	}

	// 3. File search: glob / grep -> find, file_search, search, find_in_files
	if !clientDeclared["glob"] {
		for _, target := range []string{"find", "file_search", "search"} {
			if clientDeclared[target] {
				renames.AddOfficialAlias("glob", target)
				break
			}
		}
	}
	if !clientDeclared["grep"] {
		for _, target := range []string{"search", "find_in_files"} {
			if clientDeclared[target] {
				renames.AddOfficialAlias("grep", target)
				break
			}
		}
	}

	// 4. File read / write
	if !clientDeclared["read"] && clientDeclared["read_file"] {
		renames.AddOfficialAlias("read", "read_file")
	}
	if !clientDeclared["write"] {
		if clientDeclared["write_file"] {
			renames.AddOfficialAlias("write", "write_file")
		} else if clientDeclared["new_file"] {
			renames.AddOfficialAlias("write", "new_file")
		}
	}
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

