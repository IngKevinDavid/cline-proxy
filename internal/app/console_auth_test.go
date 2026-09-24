package app

import (
	"testing"
)

func TestConsoleAuthDiscovery(t *testing.T) {
	auth, err := GetConsoleAuth()
	if err != nil {
		t.Logf("Console auth not available on this machine (expected if not logged in): %v", err)
		return
	}
	if auth.AccessToken == "" {
		t.Errorf("expected non-empty access token")
	}
	if auth.ActiveOrgID == "" {
		t.Errorf("expected non-empty active org id")
	}
	t.Logf("Console Auth Found: email=%s org=%s tokenPrefix=%s", auth.Email, auth.ActiveOrgID, auth.AccessToken[:8])
}
