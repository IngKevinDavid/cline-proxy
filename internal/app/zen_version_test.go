package app

import (
	"context"
	"strings"
	"testing"
)

func TestZenDynamicVersion(t *testing.T) {
	// 1. Initial default version check
	ua := zenCLIUserAgent()
	if !strings.HasPrefix(ua, "opencode/latest/") || !strings.HasSuffix(ua, "/cli") {
		t.Fatalf("unexpected user-agent shape: %s", ua)
	}

	// 2. Fetch from npm registry test
	ctx := context.Background()
	ver, err := fetchNpmOpenCodeVersion(ctx)
	if err == nil {
		if !strings.Contains(ver, ".") {
			t.Fatalf("invalid npm version string: %s", ver)
		}
	}

	// 3. Update client version test
	resolved := updateZenClientVersion()
	if resolved == "" {
		t.Fatalf("updateZenClientVersion returned empty string")
	}

	curVer := GetZenClientVersion()
	if curVer != resolved {
		t.Fatalf("GetZenClientVersion() = %s, want %s", curVer, resolved)
	}

	// 4. Manual override test
	cfg := getZenConfig()
	oldVer := cfg.ClientVersion
	defer func() {
		cfg.ClientVersion = oldVer
		setZenConfig(cfg)
	}()

	cfg.ClientVersion = "9.9.9"
	setZenConfig(cfg)

	if got := GetZenClientVersion(); got != "9.9.9" {
		t.Fatalf("expected override 9.9.9, got %s", got)
	}
	if gotUA := zenCLIUserAgent(); gotUA != "opencode/latest/9.9.9/cli" {
		t.Fatalf("expected UA opencode/latest/9.9.9/cli, got %s", gotUA)
	}
}
