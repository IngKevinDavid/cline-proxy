package app

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	torMu         sync.Mutex
	torCmd        *exec.Cmd
	torSocksPort  = 9052
	torCtrlPort   = 9053
	torActive     bool
	torLastRotate time.Time
)

// FindTorBinary locates tor.exe inside tools/tor/tor.exe or in PATH.
func FindTorBinary() string {
	// 1. Check relative candidate paths (from root, subpackage, or executable dir)
	candidates := []string{
		filepath.Join("tools", "tor", "tor.exe"),
		filepath.Join("..", "tools", "tor", "tor.exe"),
		filepath.Join("..", "..", "tools", "tor", "tor.exe"),
	}
	// 2. Relative to the executable
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(exeDir, "tools", "tor", "tor.exe"),
			filepath.Join(exeDir, "..", "tools", "tor", "tor.exe"),
			filepath.Join(exeDir, "..", "..", "tools", "tor", "tor.exe"),
		)
	}
	for _, p := range candidates {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			abs, _ := filepath.Abs(p)
			return abs
		}
	}
	// 3. System PATH
	if p, err := exec.LookPath("tor.exe"); err == nil {
		return p
	}
	if p, err := exec.LookPath("tor"); err == nil {
		return p
	}
	return ""
}

// EnsureTorReady ensures the self-contained Tor daemon is running and listening on socksPort.
func EnsureTorReady(socksPort, controlPort int) (string, error) {
	torMu.Lock()
	defer torMu.Unlock()

	if socksPort <= 0 {
		socksPort = 9052
	}
	if controlPort <= 0 {
		controlPort = 9053
	}
	torSocksPort = socksPort
	torCtrlPort = controlPort

	socksAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)
	socksURL := fmt.Sprintf("socks5://%s", socksAddr)

	if ProbeSocks5(socksAddr, 800*time.Millisecond) {
		torActive = true
		return socksURL, nil
	}

	torBin := FindTorBinary()
	if torBin == "" {
		return "", fmt.Errorf("tor binary not found in tools/tor/ or PATH")
	}

	dataDir := filepath.Join(os.TempDir(), "cline-proxy-tor-data")
	_ = os.MkdirAll(dataDir, 0o700)

	args := []string{
		"--SocksPort", strconv.Itoa(socksPort),
		"--ControlPort", strconv.Itoa(controlPort),
		"--CookieAuthentication", "0",
		"--DataDirectory", dataDir,
		"--RunAsDaemon", "0",
	}

	cmd := exec.Command(torBin, args...)
	// Set windowsHide if available via sysprocattr or simple execution
	hideWindow(cmd)

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start tor (%s): %w", torBin, err)
	}
	torCmd = cmd
	log.Printf("[tor] launched child process (pid %d) from %s", cmd.Process.Pid, torBin)

	// Wait up to 15 seconds for SOCKS port to become available
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(800 * time.Millisecond)
		if ProbeSocks5(socksAddr, 1*time.Second) {
			torActive = true
			log.Printf("[tor] listener ready on %s", socksAddr)
			return socksURL, nil
		}
	}

	return "", fmt.Errorf("timeout waiting for tor socks listener on %s", socksAddr)
}

// RotateTor connects to the Tor control port and issues SIGNAL NEWNYM to get a fresh exit circuit.
func RotateTor(controlPort int) error {
	torMu.Lock()
	defer torMu.Unlock()

	if controlPort <= 0 {
		controlPort = torCtrlPort
		if controlPort <= 0 {
			controlPort = 9053
		}
	}

	// Tor drops NEWNYM signals sent less than ~10s apart.
	// If a rotation occurred recently, wait for the remaining time instead of dropping the rotation request.
	if elapsed := time.Since(torLastRotate); elapsed < 10*time.Second {
		wait := 10*time.Second - elapsed
		log.Printf("[tor] pacing rotation (waiting %v for Tor gap floor)", wait.Round(time.Millisecond))
		time.Sleep(wait)
	}

	ctrlAddr := fmt.Sprintf("127.0.0.1:%d", controlPort)
	conn, err := net.DialTimeout("tcp", ctrlAddr, 3*time.Second)
	if err != nil {
		return fmt.Errorf("dial tor control port %s: %w", ctrlAddr, err)
	}
	defer conn.Close()

	r := bufio.NewReader(conn)

	// Send AUTHENTICATE
	if _, err := fmt.Fprintf(conn, "AUTHENTICATE\r\n"); err != nil {
		return fmt.Errorf("write AUTHENTICATE: %w", err)
	}
	reply, err := r.ReadString('\n')
	if err != nil || !strings.HasPrefix(reply, "250") {
		return fmt.Errorf("AUTHENTICATE rejected: %s (%v)", strings.TrimSpace(reply), err)
	}

	// Send SIGNAL NEWNYM
	if _, err := fmt.Fprintf(conn, "SIGNAL NEWNYM\r\n"); err != nil {
		return fmt.Errorf("write SIGNAL NEWNYM: %w", err)
	}
	reply, err = r.ReadString('\n')
	if err != nil || !strings.HasPrefix(reply, "250") {
		return fmt.Errorf("SIGNAL NEWNYM rejected: %s (%v)", strings.TrimSpace(reply), err)
	}

	torLastRotate = time.Now()
	// Evict cached HTTP client and close idle connections so requests do not reuse the old exit node connection.
	EvictProxyClient(fmt.Sprintf("socks5://127.0.0.1:%d", torSocksPort))
	log.Printf("[tor] circuit rotation requested (SIGNAL NEWNYM accepted, connections evicted)")
	time.Sleep(1 * time.Second)
	return nil
}

// StopTor terminates the supervised Tor process if running.
func StopTor() {
	torMu.Lock()
	defer torMu.Unlock()

	if torCmd != nil && torCmd.Process != nil {
		_ = torCmd.Process.Kill()
		_ = torCmd.Wait()
		torCmd = nil
		torActive = false
		log.Printf("[tor] child process terminated")
	}
}
