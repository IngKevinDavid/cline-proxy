package app

import (
	"testing"
)

func TestOpenCodeCLI2022ProfileVerification(t *testing.T) {
	profile, err := LoadOpenCodeCLI2022Profile()
	if err != nil {
		t.Fatalf("LoadOpenCodeCLI2022Profile failed: %v", err)
	}

	if !profile.Verified {
		t.Fatalf("expected profile to be verified")
	}

	if profile.Version != "2.0.22" {
		t.Errorf("expected version 2.0.22, got %s", profile.Version)
	}

	if len(profile.Tools) != 12 {
		t.Errorf("expected 12 tools, got %d", len(profile.Tools))
	}

	if len(profile.ToolNames) != 12 {
		t.Errorf("expected 12 tool names, got %d", len(profile.ToolNames))
	}

	info := GetFidelityProfileInfo()
	if info["verified"] != true {
		t.Errorf("GetFidelityProfileInfo expected verified: true, got %v", info["verified"])
	}
}
