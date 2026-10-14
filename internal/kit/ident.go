package kit

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

func WithRetryJitter(delay time.Duration) time.Duration {
	if delay <= 0 {
		return delay
	}
	// 在退避时间上增加 0%~25% 抖动，避免多个请求同时重试。
	jitter := time.Duration(float64(delay) * float64(RandIntn(26)) / 100)
	const maxDuration = time.Duration(1<<63 - 1)
	if delay > maxDuration-jitter {
		return maxDuration
	}
	return delay + jitter
}

// ============================================================================
// 客户端身份轮换: 模拟多个独立 opencode 客户端
// 源码中 x-opencode-session / x-opencode-request / User-Agent 均为客户端随机生成,
// 服务端可能按这些标识记账(实测 "Worker local total request limit reached" 疑似 session 级)。
// 每次请求生成全新身份 = 每次都是"新客户端",从源头规避身份维度的限流。
// ============================================================================

var ZenUserAgents = []string{
	"opencode/1.18.31 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14",
	"opencode/1.18.31 ai-sdk/provider-utils/4.0.40 runtime/bun/1.3.14",
	"opencode/latest/1.18.14/cli",
	"opencode/latest/1.18.13/cli",
	"opencode/1.18.14/cli",
	"opencode/1.18.13/cli",
	"opencode/1.18.12/cli",
	"opencode/1.18.11/cli",
	"opencode/latest/1.18.14/desktop",
	"opencode/latest/1.18.13/desktop",
}

// mustRand 读满 n 字节加密随机数；crypto/rand 失败说明系统熵源异常，
// 此时身份/防抖动全部失效，宁可 panic 也不静默使用全零字节。
func mustRand(b []byte) {
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
}

func RandHex(n int) string {
	b := make([]byte, n)
	mustRand(b)
	return hex.EncodeToString(b)
}

func RandIntn(n int) int {
	if n <= 0 {
		return 0
	}
	b := make([]byte, 4)
	mustRand(b)
	v := int(b[0])<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3])
	if v < 0 {
		v = -v
	}
	return v % n
}

var (
	ocIDMu        sync.Mutex
	ocIDLastMilli uint64
	ocIDCounter   uint64
)

func ocIDTimestamp() (uint64, uint64) {
	ocIDMu.Lock()
	defer ocIDMu.Unlock()
	now := uint64(time.Now().UnixMilli())
	if now != ocIDLastMilli {
		ocIDLastMilli = now
		ocIDCounter = 0
	}
	ocIDCounter++
	return now, ocIDCounter
}

// NewOCShapeID generates a 26-character ID conforming to OpenCode's gate
// (packages/schema/src/identifier.ts): the first 12 lowercase hex characters
// represent the 48-bit timestamp + counter, inverted (bitwise NOT) for descending
// (used for ses_ session IDs) and standard for ascending (used for msg_ request IDs).
// The remaining 14 characters are random base62 characters.
func NewOCShapeID(descending bool) string {
	const hexChars = "0123456789abcdef"
	const alnumChars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	ts, counter := ocIDTimestamp()
	current := ts*0x1000 + counter
	value := current
	if descending {
		value = ^current
	}
	var b [26]byte
	for i := 0; i < 6; i++ {
		byteVal := byte(value >> (40 - 8*i))
		b[i*2] = hexChars[byteVal>>4]
		b[i*2+1] = hexChars[byteVal&0xf]
	}
	var rnd [14]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		for i := range rnd {
			rnd[i] = alnumChars[(counter+uint64(i)*31)%uint64(len(alnumChars))]
		}
	} else {
		for i := range rnd {
			rnd[i] = alnumChars[rnd[i]%byte(len(alnumChars))]
		}
	}
	copy(b[12:], rnd[:])
	return string(b[:])
}

// CanonicalSessionID generates a session ID conforming to OpenCode's regex:
// ^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$ with descending inverted timestamp.
func CanonicalSessionID() string {
	return "ses_" + NewOCShapeID(true)
}

// CanonicalRequestID generates a request ID conforming to OpenCode's format:
// ^msg_[0-9a-f]{12}[0-9A-Za-z]{14}$ with ascending timestamp.
func CanonicalRequestID() string {
	return "msg_" + NewOCShapeID(false)
}

// FreshZenIdentity 生成一组全新客户端身份 (session, request, user-agent)。
func FreshZenIdentity() (string, string, string) {
	return CanonicalSessionID(),
		CanonicalRequestID(),
		ZenUserAgents[0]
}

// RandAlphaNum 生成 n 个大小写字母+数字（匹配官方 CLI 的 sess_/msg_ ID 字符集）。
func RandAlphaNum(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	mustRand(b)
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b)
}
