package app

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

//go:embed profiles/opencode-cli/1.18.23/system_base.txt
var openCodeCLI11823SystemBase string

//go:embed profiles/opencode-cli/1.18.23/skills.txt
var openCodeCLI11823Skills string

//go:embed profiles/opencode-cli/1.18.23/tools.json
var openCodeCLI11823ToolsJSON []byte

//go:embed profiles/opencode-cli/1.18.23/manifest.json
var openCodeCLI11823ManifestJSON []byte

//go:embed profiles/opencode-cli/2.0.15/tools.json
var openCodeCLI2015ToolsJSON []byte

//go:embed profiles/opencode-cli/2.0.15/manifest.json
var openCodeCLI2015ManifestJSON []byte

//go:embed profiles/opencode-cli/2.0.22/tools.json
var openCodeCLI2022ToolsJSON []byte

//go:embed profiles/opencode-cli/2.0.22/manifest.json
var openCodeCLI2022ManifestJSON []byte

//go:embed profiles/opencode-cli/2.0.22/system_base.txt
var openCodeCLI2022SystemBase string

//go:embed profiles/opencode-cli/2.0.22/skills.txt
var openCodeCLI2022Skills string

//go:embed profiles/opencode-cli/2.0.22/code_mode.txt
var openCodeCLI2022CodeMode string

type OpenCodeCLIProfileManifest struct {
	Version          string   `json:"version"`
	SystemBaseSHA256 string   `json:"system_base_sha256"`
	SkillsSHA256     string   `json:"skills_sha256"`
	ToolsExactSHA256 string   `json:"tools_exact_sha256"`
	ToolNames        []string `json:"tool_names"`
	CodeModeSHA256   string   `json:"code_mode_sha256"`
}

type OpenCodeCLIProfile struct {
	Version    string   `json:"version"`
	SystemBase string   `json:"system_base"`
	Skills     string   `json:"skills"`
	CodeMode   string   `json:"code_mode"`
	Tools      []any    `json:"tools"`
	ToolNames  []string `json:"tool_names"`
	Verified   bool     `json:"verified"`
}

var (
	profile2022Once sync.Once
	profile2022     OpenCodeCLIProfile
	profile2022Err  error
)

func LoadOpenCodeCLI2022Profile() (OpenCodeCLIProfile, error) {
	profile2022Once.Do(func() {
		sb := strings.ReplaceAll(openCodeCLI2022SystemBase, "\r\n", "\n")
		sk := strings.ReplaceAll(openCodeCLI2022Skills, "\r\n", "\n")
		cm := strings.ReplaceAll(openCodeCLI2022CodeMode, "\r\n", "\n")

		var manifest OpenCodeCLIProfileManifest
		if err := json.Unmarshal(openCodeCLI2022ManifestJSON, &manifest); err != nil {
			profile2022Err = fmt.Errorf("decode OpenCode CLI 2.0.22 manifest: %w", err)
			return
		}

		var tools []any
		if err := json.Unmarshal(openCodeCLI2022ToolsJSON, &tools); err != nil {
			profile2022Err = fmt.Errorf("decode OpenCode CLI 2.0.22 tools: %w", err)
			return
		}

		encoded, err := canonicalJSONBytes(tools)
		if err != nil {
			profile2022Err = err
			return
		}

		if manifest.Version != "2.0.22" ||
			sha256Bytes(encoded) != manifest.ToolsExactSHA256 ||
			sha256String(sb) != manifest.SystemBaseSHA256 ||
			sha256String(sk) != manifest.SkillsSHA256 ||
			sha256String(cm) != manifest.CodeModeSHA256 ||
			len(tools) != len(manifest.ToolNames) {
			profile2022Err = fmt.Errorf("OpenCode CLI 2.0.22 profile hash mismatch (tools=%t, sb=%t, sk=%t)",
				sha256Bytes(encoded) == manifest.ToolsExactSHA256,
				sha256String(sb) == manifest.SystemBaseSHA256,
				sha256String(sk) == manifest.SkillsSHA256)
			return
		}

		for index, raw := range tools {
			tool, _ := raw.(map[string]any)
			function, _ := tool["function"].(map[string]any)
			if function["name"] != manifest.ToolNames[index] {
				profile2022Err = fmt.Errorf("OpenCode CLI 2.0.22 tool %d name mismatch", index)
				return
			}
		}

		profile2022 = OpenCodeCLIProfile{
			Version:    manifest.Version,
			SystemBase: strings.TrimSpace(sb),
			Skills:     strings.TrimSpace(sk),
			CodeMode:   strings.TrimSpace(cm),
			Tools:      tools,
			ToolNames:  manifest.ToolNames,
			Verified:   true,
		}
	})
	return profile2022, profile2022Err
}

// GetFidelityProfileInfo returns metadata for the admin panel and telemetry
func GetFidelityProfileInfo() map[string]any {
	p, err := LoadOpenCodeCLI2022Profile()
	if err != nil {
		return map[string]any{
			"version":  "2.0.22",
			"verified": false,
			"error":    err.Error(),
		}
	}
	return map[string]any{
		"version":    p.Version,
		"verified":   p.Verified,
		"tool_count": len(p.Tools),
		"tool_names": p.ToolNames,
	}
}

func canonicalJSONBytes(value any) ([]byte, error) {
	var buffer strings.Builder
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return []byte(strings.TrimSuffix(buffer.String(), "\n")), nil
}

func sha256String(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return sha256Bytes([]byte(value))
}

func sha256Bytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
