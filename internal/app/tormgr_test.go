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

func TestEnsureTorReadyAndRotate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow Tor test in short mode")
	}
	torURL, err := EnsureTorReady(9052, 9053)
	if err != nil {
		t.Fatalf("EnsureTorReady failed: %v", err)
	}
	if torURL != "socks5://127.0.0.1:9052" {
		t.Fatalf("unexpected torURL: %s", torURL)
	}
	t.Cleanup(func() {
		// keep tor running or stop
	})
	if err := RotateTor(9053); err != nil {
		t.Logf("RotateTor note: %v", err)
	}
}

