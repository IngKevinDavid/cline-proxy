package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"cline-go-proxy/internal/kit"
)

// ConsoleAuth represents an OpenCode Console OAuth session.
type ConsoleAuth struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenExpiry  int64     `json:"token_expiry"` // milliseconds
	ActiveOrgID  string    `json:"active_org_id"`
	Email        string    `json:"email"`
	LastUpdated  time.Time `json:"last_updated"`
}

var (
	consoleAuthMu sync.RWMutex
	cachedConsole *ConsoleAuth
)

func consoleCacheFile() string {
	return kit.ResolveDataPath(".opencode-console.json")
}

// GetConsoleAuth retrieves active Console credentials, refreshing from opencode db if expired or missing.
func GetConsoleAuth() (*ConsoleAuth, error) {
	consoleAuthMu.RLock()
	if cachedConsole != nil && isConsoleAuthValid(cachedConsole) {
		auth := *cachedConsole
		consoleAuthMu.RUnlock()
		return &auth, nil
	}
	consoleAuthMu.RUnlock()

	consoleAuthMu.Lock()
	defer consoleAuthMu.Unlock()

	// Double-check under write lock
	if cachedConsole != nil && isConsoleAuthValid(cachedConsole) {
		auth := *cachedConsole
		return &auth, nil
	}

	auth, err := loadConsoleAuth()
	if err != nil {
		// Fallback to disk cache if available even if slightly older
		if diskAuth := readConsoleAuthDisk(); diskAuth != nil && diskAuth.AccessToken != "" {
			cachedConsole = diskAuth
			res := *diskAuth
			return &res, nil
		}
		return nil, err
	}

	cachedConsole = auth
	saveConsoleAuthDisk(auth)
	res := *auth
	return &res, nil
}

func isConsoleAuthValid(a *ConsoleAuth) bool {
	if a == nil || a.AccessToken == "" {
		return false
	}
	if a.TokenExpiry <= 0 {
		return true
	}
	// Expire 2 minutes early to avoid in-flight expiration
	nowMs := time.Now().UnixMilli()
	return a.TokenExpiry > (nowMs + 120_000)
}

func readConsoleAuthDisk() *ConsoleAuth {
	data, err := os.ReadFile(consoleCacheFile())
	if err != nil || len(data) == 0 {
		return nil
	}
	var a ConsoleAuth
	if err := json.Unmarshal(data, &a); err != nil {
		return nil
	}
	return &a
}

func saveConsoleAuthDisk(a *ConsoleAuth) {
	if a == nil {
		return
	}
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return
	}
	tmp := consoleCacheFile() + ".tmp"
	_ = os.WriteFile(tmp, data, 0600)
	_ = os.Rename(tmp, consoleCacheFile())
}

// loadConsoleAuth extracts active credentials via opencode db CLI.
func loadConsoleAuth() (*ConsoleAuth, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()

	query := "SELECT a.access_token, a.refresh_token, a.token_expiry, s.active_org_id, a.email FROM account a JOIN account_state s ON a.id = s.active_account_id LIMIT 1;"
	cmd := exec.CommandContext(ctx, "opencode", "db", query)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("opencode db failed (%w): %s", err, strings.TrimSpace(string(out)))
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return nil, errors.New("opencode db returned no active account rows")
	}

	// Line 0 is header: access_token \t refresh_token \t token_expiry \t active_org_id \t email
	// Line 1 is data
	row := strings.Split(strings.TrimRight(lines[1], "\r"), "\t")
	if len(row) < 4 {
		return nil, fmt.Errorf("opencode db returned unexpected row format: %q", lines[1])
	}

	accessToken := strings.TrimSpace(row[0])
	refreshToken := strings.TrimSpace(row[1])
	tokenExpiry, _ := strconv.ParseInt(strings.TrimSpace(row[2]), 10, 64)
	activeOrgID := strings.TrimSpace(row[3])
	email := ""
	if len(row) > 4 {
		email = strings.TrimSpace(row[4])
	}

	if accessToken == "" {
		return nil, errors.New("opencode db active account has empty access_token")
	}

	auth := &ConsoleAuth{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		TokenExpiry:  tokenExpiry,
		ActiveOrgID:  activeOrgID,
		Email:        email,
		LastUpdated:  time.Now(),
	}

	log.Printf("console auth loaded: account=%s org=%s expiry=%s",
		email, activeOrgID, time.UnixMilli(tokenExpiry).Format(time.RFC3339))
	return auth, nil
}
