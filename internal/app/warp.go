package app

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// WarpStatus describes the live state of Cloudflare WARP proxy mode.
type WarpStatus struct {
	Enabled       bool   `json:"enabled"`
	Active        bool   `json:"active"`
	Connected     bool   `json:"connected"`
	SocksURL      string `json:"socksUrl,omitempty"`
	SocksHost     string `json:"socksHost"`
	SocksPort     int    `json:"socksPort"`
	LastAttemptAt string `json:"lastAttemptAt,omitempty"`
	LastError     string `json:"lastError,omitempty"`
}

var (
	warpMu            sync.Mutex
	warpActive        bool
	warpConnected     bool
	warpLastAttemptAt time.Time
	warpLastRotate    time.Time
	warpLastError     string
)

// findWarpCli locates the warp-cli binary on Windows or in PATH.
func findWarpCli() string {
	candidates := []string{
		`C:\Program Files\Cloudflare\Cloudflare WARP\warp-cli.exe`,
		`C:\Program Files (x86)\Cloudflare\Cloudflare WARP\warp-cli.exe`,
	}
	for _, p := range candidates {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	if p, err := exec.LookPath("warp-cli"); err == nil {
		return p
	}
	if p, err := exec.LookPath("warp-cli.exe"); err == nil {
		return p
	}
	return ""
}

// ProbeSocks5 checks if a SOCKS5 listener is accepting TCP connections.
func ProbeSocks5(addr string, timeout time.Duration) bool {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// runWarpCli runs a warp-cli command with a timeout.
func runWarpCli(ctx context.Context, args ...string) (string, error) {
	bin := findWarpCli()
	if bin == "" {
		return "", fmt.Errorf("warp-cli binary not found on system")
	}
	ctxCmd, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctxCmd, bin, args...)
	out, err := cmd.CombinedOutput()
	outputStr := strings.TrimSpace(string(out))
	if err != nil {
		return outputStr, fmt.Errorf("warp-cli %s: %w (%s)", strings.Join(args, " "), err, outputStr)
	}
	return outputStr, nil
}

// EnsureWarpReady verifies or activates WARP in local SOCKS5 proxy mode.
// Only traffic explicitly sent to socksHost:socksPort routes through WARP.
func EnsureWarpReady(socksHost string, socksPort int) (string, error) {
	warpMu.Lock()
	defer warpMu.Unlock()

	if socksHost == "" {
		socksHost = "127.0.0.1"
	}
	if socksPort <= 0 {
		socksPort = 40000
	}
	addr := net.JoinHostPort(socksHost, strconv.Itoa(socksPort))
	socksURL := fmt.Sprintf("socks5://%s", addr)

	// If the SOCKS port is already listening and accepting connections, we're ready.
	if ProbeSocks5(addr, 1*time.Second) {
		warpActive = true
		warpConnected = true
		warpLastError = ""
		return socksURL, nil
	}

	warpLastAttemptAt = time.Now()

	// Try to activate WARP proxy mode via warp-cli
	ctx := context.Background()
	if _, err := runWarpCli(ctx, "proxy", "port", strconv.Itoa(socksPort)); err != nil {
		warpLastError = err.Error()
		log.Printf("[warp] config proxy port failed: %v", err)
	}
	if _, err := runWarpCli(ctx, "mode", "proxy"); err != nil {
		warpLastError = err.Error()
		log.Printf("[warp] set mode proxy failed: %v", err)
	}
	if _, err := runWarpCli(ctx, "connect"); err != nil {
		warpLastError = err.Error()
		log.Printf("[warp] connect failed: %v", err)
	}

	// Give the tunnel a moment to initialize
	time.Sleep(1200 * time.Millisecond)

	if ProbeSocks5(addr, 2*time.Second) {
		warpActive = true
		warpConnected = true
		warpLastError = ""
		log.Printf("[warp] active and listening on %s", addr)
		return socksURL, nil
	}

	warpActive = false
	warpConnected = false
	if warpLastError == "" {
		warpLastError = fmt.Sprintf("WARP proxy listener on %s is not accepting connections", addr)
	}
	return "", fmt.Errorf("%s", warpLastError)
}

// RotateWarp rotates the connection to get a fresh IP address.
func RotateWarp() error {
	warpMu.Lock()
	defer warpMu.Unlock()

	cfg := getZenConfig()
	host := "127.0.0.1"
	port := 40000
	if cfg != nil {
		if cfg.WarpSocksHost != "" {
			host = cfg.WarpSocksHost
		}
		if cfg.WarpSocksPort > 0 {
			port = cfg.WarpSocksPort
		}
	}
	socksURL := fmt.Sprintf("socks5://%s:%d", host, port)
	addr := net.JoinHostPort(host, strconv.Itoa(port))

	if !warpLastRotate.IsZero() && time.Since(warpLastRotate) < 15*time.Second {
		EvictProxyClient(socksURL)
		log.Printf("[warp] rotation already triggered %v ago (skipping reconnect)", time.Since(warpLastRotate).Round(time.Millisecond))
		return nil
	}

	ctx := context.Background()
	bin := findWarpCli()
	if bin == "" {
		return fmt.Errorf("warp-cli not found")
	}

	// Try tunnel rotate-keys first
	if out, err := runWarpCli(ctx, "tunnel", "rotate-keys"); err == nil {
		warpLastRotate = time.Now()
		log.Printf("[warp] rotated tunnel keys: %s", out)
		EvictProxyClient(socksURL)
		time.Sleep(500 * time.Millisecond)
		return nil
	}

	// Fallback to disconnect + connect
	_, _ = runWarpCli(ctx, "disconnect")
	time.Sleep(800 * time.Millisecond)
	if out, err := runWarpCli(ctx, "connect"); err != nil {
		return fmt.Errorf("reconnect failed: %w (%s)", err, out)
	}
	warpLastRotate = time.Now()
	EvictProxyClient(socksURL)

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if ProbeSocks5(addr, 400*time.Millisecond) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	return nil
}

// GetWarpStatus returns the current status snapshot.
func GetWarpStatus(cfg *zenConfigData) WarpStatus {
	warpMu.Lock()
	defer warpMu.Unlock()

	host := "127.0.0.1"
	port := 40000
	enabled := true
	if cfg != nil {
		enabled = cfg.WarpAutoFailover
		if cfg.WarpSocksHost != "" {
			host = cfg.WarpSocksHost
		}
		if cfg.WarpSocksPort > 0 {
			port = cfg.WarpSocksPort
		}
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	listening := ProbeSocks5(addr, 500*time.Millisecond)
	if listening {
		warpActive = true
		warpConnected = true
	}
	active := listening

	var lastAttempt string
	if !warpLastAttemptAt.IsZero() {
		lastAttempt = warpLastAttemptAt.UTC().Format(time.RFC3339)
	}

	var socksURL string
	if active {
		socksURL = fmt.Sprintf("socks5://%s", addr)
	}

	return WarpStatus{
		Enabled:       enabled,
		Active:        active,
		Connected:     warpConnected,
		SocksURL:      socksURL,
		SocksHost:     host,
		SocksPort:     port,
		LastAttemptAt: lastAttempt,
		LastError:     warpLastError,
	}
}

