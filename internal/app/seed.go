package app

import (
	"cline-go-proxy/internal/cline"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"
)

// seedAccountsFromFile 池为空时从 CLINE_ACCOUNTS_SEED_FILE 指定的 JSON 文件导入
// cline 账号（[{refreshToken,email}] 数组），让容器在全新主机上无需 OAuth 点击即可重建。
// 逐个校验 refresh token（换新 access token），失败的跳过并记录日志。
func seedAccountsFromFile() {
	path := envStr("CLINE_ACCOUNTS_SEED_FILE")
	if path == "" {
		return
	}
	if p := loadPool(); len(p.Accounts) > 0 {
		log.Printf("seed file configured but pool already has %d accounts, skipping", len(p.Accounts))
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("seed file read failed: %v", err)
		return
	}
	var items []struct {
		RefreshToken string `json:"refreshToken"`
		Email        string `json:"email"`
	}
	if err := json.Unmarshal(data, &items); err != nil {
		log.Printf("seed file parse failed: %v", err)
		return
	}
	if len(items) == 0 {
		return
	}
	added := 0
	for _, item := range items {
		if item.RefreshToken == "" {
			continue
		}
		resp, err := cline.RefreshClineToken(item.RefreshToken)
		if err != nil {
			log.Printf("  seed account skipped (invalid refreshToken): %v", err)
			continue
		}
		email := item.Email
		if email == "" {
			email = fmt.Sprintf("seeded_%d", time.Now().UnixMilli())
		}
		acc := &Account{
			AccountID:    fmt.Sprintf("acc_%d", time.Now().UnixMilli()),
			Email:        email,
			RefreshToken: item.RefreshToken,
			AccessToken:  "workos:" + resp.Data.AccessToken,
			ExpiresAt:    cline.ParseExpiry(resp.Data.ExpiresAt) - 60000,
			Status:       "active",
			CreatedAt:    time.Now(),
		}
		if resp.Data.RefreshToken != "" {
			acc.RefreshToken = resp.Data.RefreshToken
		}
		addAccount(acc)
		log.Printf("  seed account added: %s", email)
		added++
	}
	log.Printf("account seeding done: %d/%d imported", added, len(items))
}
