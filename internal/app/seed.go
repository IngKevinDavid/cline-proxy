package app

import (
	"cline-go-proxy/internal/cline"
	"cline-go-proxy/internal/kit"
	"encoding/json"
	"log"
	"os"
	"time"
)

// seedAccountsFromFile 池为空时从 CLINE_ACCOUNTS_SEED_FILE 指定的 JSON 文件导入
// cline 账号，让容器在全新主机上无需 OAuth 点击即可重建。支持两种条目：
//   - {"refreshToken": "...", "email": "..."}   OAuth 账号（逐个换新 token 校验）
//   - {"apiToken": "sk_...", "email": "..."}    静态 API key（直接作为 Bearer，
//     不刷新不校验；key 被吊销时首次 401 会标记 expired）
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
		APIToken     string `json:"apiToken"`
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
		email := item.Email
		if email == "" {
			email = "seeded_" + kit.RandHex(4)
		}
		acc := &Account{
			AccountID: "acc_" + kit.RandHex(8),
			Email:     email,
			Status:    "active",
			CreatedAt: time.Now(),
		}
		if item.APIToken != "" {
			// 静态 API key 账号：无 refresh token，token 即凭证
			acc.APIToken = item.APIToken
			addAccount(acc)
			log.Printf("  seed account added (api key): %s", email)
			added++
			continue
		}
		if item.RefreshToken == "" {
			continue
		}
		resp, err := cline.RefreshClineToken(item.RefreshToken)
		if err != nil {
			log.Printf("  seed account skipped (invalid refreshToken): %v", err)
			continue
		}
		acc.RefreshToken = item.RefreshToken
		acc.AccessToken = "workos:" + resp.Data.AccessToken
		acc.ExpiresAt = clineExpiryMs(resp.Data.ExpiresAt)
		if resp.Data.RefreshToken != "" {
			acc.RefreshToken = resp.Data.RefreshToken
		}
		addAccount(acc)
		log.Printf("  seed account added: %s", email)
		added++
	}
	log.Printf("account seeding done: %d/%d imported", added, len(items))
}
