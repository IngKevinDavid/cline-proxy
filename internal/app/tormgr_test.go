package app

import (
	"os"
	"testing"
)

func TestFindTorBinary(t *testing.T) {
	bin := FindTorBinary()
	if bin == "" {
		t.Fatalf("expected to find tor binary in tools/tor/tor.exe, got empty")
	}
	info, err := os.Stat(bin)
	if err != nil {
		t.Fatalf("failed to stat tor binary at %s: %v", bin, err)
	}
	if info.Size() < 1000000 {
		t.Fatalf("tor binary at %s has unexpected size %d", bin, info.Size())
	}
}
