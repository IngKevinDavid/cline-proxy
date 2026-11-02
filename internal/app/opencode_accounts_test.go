package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenCodeAccountStore(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "opencode_acc_test_*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	storePath := filepath.Join(tmpDir, "opencode-accounts.json")
	store := &OpenCodeAccountStore{path: storePath}

	// 1. Initial load should be empty
	state, err := store.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if len(state.Accounts) != 0 {
		t.Errorf("expected 0 accounts, got %d", len(state.Accounts))
	}

	// 2. Add Account
	acc1 := OpenCodeAccountRecord{
		Name:          "dev-account-1",
		Authorization: "token-secret-123",
		Route:         OpenCodeRouteWARP,
		RefreshToken:  "refresh-secret-123",
		ExpiresAt:     time.Now().Add(1 * time.Hour).UnixMilli(),
		OrgID:         "org_test_1",
	}
	if err := store.AddAccount(acc1); err != nil {
		t.Fatalf("AddAccount failed: %v", err)
	}

	// 3. Verify ListAccounts masks token
	list := store.ListAccounts()
	if len(list) != 1 {
		t.Fatalf("expected 1 account in list, got %d", len(list))
	}
	if list[0]["name"] != "dev-account-1" {
		t.Errorf("expected name dev-account-1, got %v", list[0]["name"])
	}
	if list[0]["route"] != OpenCodeRouteWARP {
		t.Errorf("expected route warp, got %v", list[0]["route"])
	}
	if list[0]["status"] != "active" {
		t.Errorf("expected status active, got %v", list[0]["status"])
	}

	// 4. Duplicate name should fail
	if err := store.AddAccount(acc1); err == nil {
		t.Errorf("expected duplicate account error, got nil")
	}

	// 5. Add second account
	acc2 := OpenCodeAccountRecord{
		Name:          "dev-account-2",
		Authorization: "Bearer token-secret-456",
		Route:         OpenCodeRouteTor,
	}
	if err := store.AddAccount(acc2); err != nil {
		t.Fatalf("AddAccount 2 failed: %v", err)
	}

	// 6. Pick Account round-robin
	p1, err := store.PickAccount("")
	if err != nil {
		t.Fatalf("PickAccount 1 failed: %v", err)
	}
	p2, err := store.PickAccount("")
	if err != nil {
		t.Fatalf("PickAccount 2 failed: %v", err)
	}
	if p1.Name == p2.Name {
		t.Errorf("expected different accounts in round-robin, got both %s", p1.Name)
	}

	// 7. Pinned Pick
	pinned, err := store.PickAccount("dev-account-1")
	if err != nil {
		t.Fatalf("PickAccount pinned failed: %v", err)
	}
	if pinned.Name != "dev-account-1" {
		t.Errorf("expected dev-account-1, got %s", pinned.Name)
	}

	// 8. Delete Account
	if err := store.DeleteAccount("dev-account-1"); err != nil {
		t.Fatalf("DeleteAccount failed: %v", err)
	}
	listAfter := store.ListAccounts()
	if len(listAfter) != 1 {
		t.Errorf("expected 1 account after delete, got %d", len(listAfter))
	}
}
