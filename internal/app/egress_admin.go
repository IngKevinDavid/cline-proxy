package app

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// RotationEvent records an automated or manual IP rotation.
type RotationEvent struct {
	ID        int       `json:"id"`
	Timestamp string    `json:"timestamp"`
	Type      string    `json:"type"`      // "tor", "warp", "proxy"
	Trigger   string    `json:"trigger"`   // "manual", "auto_429", "failover"
	OldIP     string    `json:"oldIp"`
	NewIP     string    `json:"newIp"`
	Status    string    `json:"status"`    // "ok", "error"
	Message   string    `json:"message"`
}

var (
	egressMu         sync.Mutex
	rotationHistory  []RotationEvent
	nextRotationID   = 1
	cachedDirectIP   string
	cachedDirectAt   time.Time
	cachedTorIP      string
	cachedTorAt      time.Time
	cachedWarpIP     string
	cachedWarpAt     time.Time
	torRotateCount   int
	warpRotateCount  int
)

// RecordRotationEvent records an IP rotation event in the in-memory history.
func RecordRotationEvent(egressType, trigger, oldIP, newIP, status, message string) {
	egressMu.Lock()
	defer egressMu.Unlock()

	ev := RotationEvent{
		ID:        nextRotationID,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Type:      egressType,
		Trigger:   trigger,
		OldIP:     oldIP,
		NewIP:     newIP,
		Status:    status,
		Message:   message,
	}
	nextRotationID++

	// Keep latest 50 events (prepended so newest is first)
	rotationHistory = append([]RotationEvent{ev}, rotationHistory...)
	if len(rotationHistory) > 50 {
		rotationHistory = rotationHistory[:50]
	}
	log.Printf("[egress] %s rotation recorded: %s -> %s (trigger: %s, status: %s)", egressType, oldIP, newIP, trigger, status)
}

// probeIPViaClient probes an IP address using an http.Client.
func probeIPViaClient(client *http.Client) (string, error) {
	endpoints := []string{
		"https://api.ipify.org",
		"https://checkip.amazonaws.com",
		"https://icanhazip.com",
		"https://ifconfig.me/ip",
	}

	for _, url := range endpoints {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			cancel()
			continue
		}
		req.Header.Set("User-Agent", "curl/7.88.1")
		resp, err := client.Do(req)
		if err != nil {
			cancel()
			continue
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		cancel()
		if err == nil && resp.StatusCode == http.StatusOK {
			ip := strings.TrimSpace(string(body))
			if len(ip) >= 7 && len(ip) <= 45 && !strings.Contains(ip, "<") {
				return ip, nil
			}
		}
	}
	return "", fmt.Errorf("unable to determine IP from upstream endpoints")
}

// ProbeDirectIP returns the direct public IP of the host machine.
func ProbeDirectIP(force bool) string {
	egressMu.Lock()
	if !force && cachedDirectIP != "" && time.Since(cachedDirectAt) < 2*time.Minute {
		ip := cachedDirectIP
		egressMu.Unlock()
		return ip
	}
	egressMu.Unlock()

	client := &http.Client{Timeout: 5 * time.Second}
	ip, err := probeIPViaClient(client)
	if err == nil && ip != "" {
		egressMu.Lock()
		cachedDirectIP = ip
		cachedDirectAt = time.Now()
		egressMu.Unlock()
		return ip
	}

	egressMu.Lock()
	defer egressMu.Unlock()
	return cachedDirectIP
}

// ProbeTorIP returns the current Tor exit IP via SOCKS5 proxy.
func ProbeTorIP(force bool) string {
	egressMu.Lock()
	if !force && cachedTorIP != "" && time.Since(cachedTorAt) < 30*time.Second {
		ip := cachedTorIP
		egressMu.Unlock()
		return ip
	}
	egressMu.Unlock()

	socksAddr := fmt.Sprintf("127.0.0.1:%d", torSocksPort)
	if !ProbeSocks5(socksAddr, 1*time.Second) {
		return ""
	}

	socksURL := fmt.Sprintf("socks5://%s", socksAddr)
	client := proxyClientFor(socksURL)
	ip, err := probeIPViaClient(client)
	if err == nil && ip != "" {
		egressMu.Lock()
		cachedTorIP = ip
		cachedTorAt = time.Now()
		egressMu.Unlock()
		return ip
	}

	egressMu.Lock()
	defer egressMu.Unlock()
	return cachedTorIP
}

// ProbeWarpIP returns the current WARP exit IP via SOCKS5 proxy.
func ProbeWarpIP(force bool) string {
	egressMu.Lock()
	if !force && cachedWarpIP != "" && time.Since(cachedWarpAt) < 30*time.Second {
		ip := cachedWarpIP
		egressMu.Unlock()
		return ip
	}
	egressMu.Unlock()

	cfg := getZenConfig()
	host := cfg.WarpSocksHost
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.WarpSocksPort
	if port <= 0 {
		port = 40000
	}
	socksAddr := fmt.Sprintf("%s:%d", host, port)
	if !ProbeSocks5(socksAddr, 1*time.Second) {
		return ""
	}

	warpMu.Lock()
	warpActive = true
	warpConnected = true
	warpMu.Unlock()

	socksURL := fmt.Sprintf("socks5://%s", socksAddr)
	client := proxyClientFor(socksURL)
	ip, err := probeIPViaClient(client)
	if err == nil && ip != "" {
		egressMu.Lock()
		cachedWarpIP = ip
		cachedWarpAt = time.Now()
		egressMu.Unlock()
		return ip
	}

	egressMu.Lock()
	defer egressMu.Unlock()
	return cachedWarpIP
}

// GetEgressOverview returns the complete status of Direct, Tor, and WARP egress.
func GetEgressOverview() map[string]any {
	cfg := getZenConfig()
	warpStatus := GetWarpStatus(cfg)

	torReady := (torActive || ProbeSocks5(fmt.Sprintf("127.0.0.1:%d", torSocksPort), 500*time.Millisecond))
	if torReady {
		torActive = true
	}

	directIP := ProbeDirectIP(false)
	torIP := ""
	if torReady {
		torIP = ProbeTorIP(false)
	}
	warpIP := ""
	if warpStatus.Active {
		warpIP = ProbeWarpIP(false)
	}

	egressMu.Lock()
	history := make([]RotationEvent, len(rotationHistory))
	copy(history, rotationHistory)
	torRotates := torRotateCount
	warpRotates := warpRotateCount
	egressMu.Unlock()

	torData := map[string]any{
		"active":        torReady,
		"socksPort":     torSocksPort,
		"controlPort":   torCtrlPort,
		"currentIp":     torIP,
		"rotationCount": torRotates,
		"lastRotateAt":  "",
	}
	if !torLastRotate.IsZero() {
		torData["lastRotateAt"] = torLastRotate.UTC().Format(time.RFC3339)
	}

	warpData := map[string]any{
		"enabled":       warpStatus.Enabled,
		"active":        warpStatus.Active,
		"connected":     warpStatus.Connected,
		"socksHost":     warpStatus.SocksHost,
		"socksPort":     warpStatus.SocksPort,
		"currentIp":     warpIP,
		"rotationCount": warpRotates,
		"lastAttemptAt": warpStatus.LastAttemptAt,
		"lastError":     warpStatus.LastError,
	}

	return map[string]any{
		"directIp": directIP,
		"tor":      torData,
		"warp":     warpData,
		"history":  history,
	}
}

// handleAdminEgressStatus handles GET /admin/api/egress/status
func handleAdminEgressStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: "method not allowed"})
		return
	}
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Data: GetEgressOverview()})
}

// handleAdminTorRotate handles POST /admin/api/tor/rotate
func handleAdminTorRotate(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: "method not allowed"})
		return
	}

	// 1. Ensure Tor is ready
	if _, err := EnsureTorReady(torSocksPort, torCtrlPort); err != nil {
		RecordRotationEvent("tor", "manual", cachedTorIP, "", "error", err.Error())
		writeAPI(w, http.StatusBadGateway, apiResponse{Error: "tor not ready: " + err.Error()})
		return
	}

	oldIP := ProbeTorIP(false)

	// 2. Execute rotation
	if err := RotateTor(0); err != nil {
		RecordRotationEvent("tor", "manual", oldIP, "", "error", err.Error())
		writeAPI(w, http.StatusBadGateway, apiResponse{Error: "rotate tor failed: " + err.Error()})
		return
	}

	egressMu.Lock()
	torRotateCount++
	cachedTorIP = "" // invalidate cache
	egressMu.Unlock()

	// 3. Probe new IP
	newIP := ProbeTorIP(true)
	RecordRotationEvent("tor", "manual", oldIP, newIP, "ok", "Tor circuit rotated successfully")

	writeAPI(w, http.StatusOK, apiResponse{
		Success: true,
		Data: map[string]any{
			"oldIp":   oldIP,
			"newIp":   newIP,
			"message": "Tor exit circuit rotated successfully",
		},
	})
}

// handleAdminWarpRotate handles POST /admin/api/warp/rotate
func handleAdminWarpRotate(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: "method not allowed"})
		return
	}

	oldIP := ProbeWarpIP(false)

	if err := RotateWarp(); err != nil {
		RecordRotationEvent("warp", "manual", oldIP, "", "error", err.Error())
		writeAPI(w, http.StatusBadGateway, apiResponse{Error: "rotate warp failed: " + err.Error()})
		return
	}

	egressMu.Lock()
	warpRotateCount++
	cachedWarpIP = "" // invalidate cache
	egressMu.Unlock()

	newIP := ProbeWarpIP(true)
	RecordRotationEvent("warp", "manual", oldIP, newIP, "ok", "WARP IP rotated successfully")

	writeAPI(w, http.StatusOK, apiResponse{
		Success: true,
		Data: map[string]any{
			"oldIp":   oldIP,
			"newIp":   newIP,
			"message": "WARP connection rotated successfully",
		},
	})
}
