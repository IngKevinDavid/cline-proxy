package app

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEnhanceCodemodeTool(t *testing.T) {
	tool := map[string]any{
		"name":        "codemode",
		"description": "Run JavaScript that calls other tools.",
	}
	enhanceCodemodeTool(tool)
	desc, ok := tool["description"].(string)
	if !ok {
		t.Fatalf("expected string description")
	}
	if !strings.Contains(desc, "QUICKJS CODEMODE RULES") {
		t.Errorf("expected QUICKJS CODEMODE RULES in description, got: %s", desc)
	}
	if !strings.Contains(desc, "NEVER REDECLARE `tools`") {
		t.Errorf("expected warning against redeclaring tools, got: %s", desc)
	}
}

func TestRepairCodemodeArguments(t *testing.T) {
	badScript := `{"code": "\nconst tools = await searchTools('engram', { limit: 30 });\nreturn tools;\n"}`
	repaired := repairCodemodeArguments(badScript)

	var parsed map[string]any
	if err := json.Unmarshal([]byte(repaired), &parsed); err != nil {
		t.Fatalf("repaired json unmarshal failed: %v", err)
	}
	code := parsed["code"].(string)
	if strings.Contains(code, "const tools =") {
		t.Errorf("expected 'const tools =' to be replaced, got: %s", code)
	}
	if !strings.Contains(code, "const searchResults = await searchTools") {
		t.Errorf("expected 'const searchResults = await searchTools', got: %s", code)
	}
	if !strings.Contains(code, "return searchResults;") {
		t.Errorf("expected 'return searchResults;', got: %s", code)
	}
}
