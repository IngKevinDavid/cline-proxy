package app

import (
	"testing"
	"time"
)

func TestRepairToolSchema(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"validProp": map[string]any{"type": "string"},
		},
		"required": []any{"validProp", "missingProp"},
	}

	repairToolSchema(schema)

	req, ok := schema["required"].([]any)
	if !ok {
		t.Fatalf("expected required to be []any, got %T", schema["required"])
	}
	if len(req) != 1 || req[0] != "validProp" {
		t.Fatalf("expected required to contain only 'validProp', got %v", req)
	}

	// When all required are missing, required key should be removed
	schema2 := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"foo": map[string]any{"type": "string"},
		},
		"required": []any{"ghost1", "ghost2"},
	}
	repairToolSchema(schema2)
	if _, exists := schema2["required"]; exists {
		t.Fatalf("expected required key to be deleted when no properties match, got %v", schema2["required"])
	}
}

func TestProbeSocks5NonExistent(t *testing.T) {
	// Probe a closed port, must return false within timeout and not panic
	ok := ProbeSocks5("127.0.0.1:49999", 100*time.Millisecond)
	if ok {
		t.Errorf("expected closed port to return false")
	}
}

func TestPickProxyOrFailover(t *testing.T) {
	// Default configuration should return ("", -1) when no proxies and no failover requested
	url, idx := pickProxyOrFailover(false)
	if url != "" || idx != -1 {
		t.Errorf("expected ('', -1) with empty proxies, got (%q, %d)", url, idx)
	}
}
