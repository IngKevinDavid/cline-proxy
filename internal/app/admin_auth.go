package app

import (
	"cline-go-proxy/internal/kit"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// ============ 管理面板登录认证 ============
//
// ADMIN_PASSWORD(_FILE) 设置后，所有 /admin/* 需要 HMAC 签名的会话凭证：
//   - 浏览器: POST /admin/api/login 设置 HttpOnly cookie（SameSite=Strict）
//   - 脚本/curl: 登录响应返回 token，后续请求带 Authorization: Bearer <token>
//
// 会话无状态（HMAC + 过期时间），重启后密钥从 data/.session-secret 恢复，
// 已签发的会话在重启后仍然有效。

const (
	sessionCookieName = "admin_session"
	sessionTTL        = 7 * 24 * time.Hour
	loginMaxFails     = 5
	loginWindow       = time.Minute
)

var (
	sessionSecretOnce sync.Once
	sessionSecret     []byte
	loginFailsMu      sync.Mutex
	loginFails        = map[string]*loginFailState{}
)

type loginFailState struct {
	count    int
	windowAt time.Time
}

func loadSessionSecret() []byte {
	sessionSecretOnce.Do(func() {
		path := kit.ResolveDataPath(".session-secret")
		if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) >= 64 {
			if s, err := hex.DecodeString(strings.TrimSpace(string(data))); err == nil {
				sessionSecret = s
				return
			}
		}
		s := make([]byte, 32)
		if _, err := rand.Read(s); err != nil {
			panic("generate session secret: " + err.Error())
		}
		os.WriteFile(path, []byte(hex.EncodeToString(s)), 0600)
		sessionSecret = s
	})
	return sessionSecret
}

// mintSessionToken 生成 "base64url(payload).hmac" 形式的会话令牌。
func mintSessionToken() string {
	nonce := make([]byte, 12)
	rand.Read(nonce)
	payload := fmt.Sprintf("v1|%d|%s", time.Now().Add(sessionTTL).Unix(), hex.EncodeToString(nonce))
	mac := hmac.New(sha256.New, loadSessionSecret())
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + hex.EncodeToString(mac.Sum(nil))
}

// verifySessionToken 校验令牌签名与有效期。
func verifySessionToken(token string) bool {
	dot := strings.LastIndex(token, ".")
	if dot <= 0 {
		return false
	}
	payloadB64, sigHex := token[:dot], token[dot+1:]
	payload, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return false
	}
	sig, err := hex.DecodeString(sigHex)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, loadSessionSecret())
	mac.Write(payload)
	if subtle.ConstantTimeCompare(sig, mac.Sum(nil)) != 1 {
		return false
	}
	parts := strings.SplitN(string(payload), "|", 3)
	if len(parts) != 3 || parts[0] != "v1" {
		return false
	}
	var exp int64
	if _, err := fmt.Sscanf(parts[1], "%d", &exp); err != nil {
		return false
	}
	return time.Now().Unix() < exp
}

// sessionFromRequest 提取会话凭证：优先 Authorization: Bearer，其次 cookie。
func sessionFromRequest(r *http.Request) string {
	if b := r.Header.Get("Authorization"); len(b) > 7 && b[:7] == "Bearer " {
		return strings.TrimSpace(b[7:])
	}
	if c, err := r.Cookie(sessionCookieName); err == nil {
		return c.Value
	}
	return ""
}

// adminAuthMiddleware 管理面板认证中间件。未配置 ADMIN_PASSWORD 时直接放行
// （本地模式）；OPTIONS 预检放行（认证在真正请求上执行）。
func adminAuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !AdminAuthRequired() {
			next(w, r)
			return
		}
		if r.Method == http.MethodOptions {
			next(w, r)
			return
		}
		if verifySessionToken(sessionFromRequest(r)) {
			next(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(apiResponse{Error: "unauthorized: login required (POST /admin/api/login)"})
	}
}

// loginRateAllow IP 登录限流：每分钟最多 loginMaxFails 次失败尝试。
func loginRateAllow(ip string) bool {
	loginFailsMu.Lock()
	defer loginFailsMu.Unlock()
	st, ok := loginFails[ip]
	if !ok || time.Since(st.windowAt) > loginWindow {
		return true
	}
	return st.count < loginMaxFails
}

func loginRateRecordFail(ip string) {
	loginFailsMu.Lock()
	defer loginFailsMu.Unlock()
	st, ok := loginFails[ip]
	if !ok || time.Since(st.windowAt) > loginWindow {
		loginFails[ip] = &loginFailState{count: 1, windowAt: time.Now()}
		return
	}
	st.count++
}

func loginRateReset(ip string) {
	loginFailsMu.Lock()
	delete(loginFails, ip)
	loginFailsMu.Unlock()
}

// POST /admin/api/login  body: { password }
// 成功: 设置 HttpOnly 会话 cookie 并返回 token（Bearer 方式调用 API 用）。
func handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: "method not allowed"})
		return
	}
	if !AdminAuthRequired() {
		writeAPI(w, http.StatusOK, apiResponse{Success: true, Message: "auth not configured (set ADMIN_PASSWORD to enable)"})
		return
	}
	ip := clientIP(r)
	if !loginRateAllow(ip) {
		writeAPI(w, http.StatusTooManyRequests, apiResponse{Error: "too many login attempts, wait a minute"})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	defer r.Body.Close()
	var req struct {
		Password string `json:"password"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: "invalid JSON"})
		return
	}
	if subtle.ConstantTimeCompare([]byte(req.Password), []byte(AdminPasswordEnv())) != 1 {
		loginRateRecordFail(ip)
		writeAPI(w, http.StatusUnauthorized, apiResponse{Error: "wrong password"})
		return
	}
	loginRateReset(ip)
	token := mintSessionToken()
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
	})
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Message: "logged in", Data: map[string]any{"token": token, "expiresInSeconds": int(sessionTTL.Seconds())}})
}

// POST /admin/api/logout 清除会话 cookie。
func handleAdminLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: "method not allowed"})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Message: "logged out"})
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// adminLoginPageHTML 未认证时 /admin/ 返回的独立登录页（不暴露完整面板 HTML）。
const adminLoginPageHTML = `<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Cline Proxy - Login</title>
<style>
body{font-family:system-ui,sans-serif;background:#0f1115;color:#e6e6e6;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0}
.card{background:#1a1d24;padding:32px;border-radius:12px;width:320px;box-shadow:0 8px 32px rgba(0,0,0,.4)}
h1{font-size:18px;margin:0 0 16px;text-align:center}
input{width:100%;box-sizing:border-box;padding:10px 12px;border-radius:8px;border:1px solid #2c3038;background:#0f1115;color:#e6e6e6;font-size:14px;margin-bottom:12px}
button{width:100%;padding:10px;border-radius:8px;border:0;background:#4f7cff;color:#fff;font-size:14px;cursor:pointer}
button:disabled{opacity:.6;cursor:wait}
#err{color:#ff6b6b;font-size:13px;min-height:18px;margin-bottom:8px;text-align:center}
</style></head><body>
<div class="card"><h1>Cline Proxy 管理登录</h1>
<div id="err"></div>
<input type="password" id="pw" placeholder="管理员密码 (ADMIN_PASSWORD)" autofocus>
<button id="go">登录</button></div>
<script>
const b=document.getElementById('go'),e=document.getElementById('err');
async function login(){b.disabled=true;e.textContent='';
 try{const r=await fetch('/admin/api/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({password:document.getElementById('pw').value})});
 const d=await r.json();
 if(r.ok&&d.success){location.reload();return;}
 e.textContent=d.error||('HTTP '+r.status);}catch(err){e.textContent=err.message;}
 b.disabled=false;}
b.onclick=login;document.getElementById('pw').addEventListener('keydown',ev=>{if(ev.key==='Enter')login();});
</script></body></html>`
