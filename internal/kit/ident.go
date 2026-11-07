package kit

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
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

var (
	canonicalSessionPattern   = regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`)
	descendingTimePartPattern = regexp.MustCompile(`^[0-9a-f]{9}f[c-f][0-9a-f]$`)
)

const sessionTimestampWindow = 2 * time.Hour

var bootNonce = mintBootNonce()

func mintBootNonce() []byte {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		for i := range buf {
			buf[i] = byte(i * 37)
		}
	}
	return buf
}

// descendingTimePart renders a plausible recent timestamp in the official
// client's Identifier.descending() layout: a 36-bit timestamp shifted into the
// top of a 48-bit word, ORed with counter 1, then bitwise-NOT-ed so the low 12
// hex digits read ffe. The hash only picks the offset within
// sessionTimestampWindow; it never supplies the date itself.
func descendingTimePart(now time.Time, hash []byte) string {
	window := uint64(sessionTimestampWindow / time.Millisecond)
	offset := (uint64(hash[0])<<24 | uint64(hash[1])<<16 | uint64(hash[2])<<8 | uint64(hash[3])) % window
	base := now.UTC().Truncate(24 * time.Hour).Add(-time.Duration(offset) * time.Millisecond)
	current := uint64(base.UnixMilli())<<12 | 1
	inverted := ^current & 0xFFFFFFFFFFFF
	return hex.EncodeToString([]byte{
		byte(inverted >> 40), byte(inverted >> 32), byte(inverted >> 24),
		byte(inverted >> 16), byte(inverted >> 8), byte(inverted),
	})
}

// IsOfficialSessionID reports whether signal is a session minted by the
// official client's Identifier.descending().
func IsOfficialSessionID(signal string) bool {
	return canonicalSessionPattern.MatchString(signal) && descendingTimePartPattern.MatchString(signal[4:16])
}

// NewOCShapeID generates a 26-character ID conforming to OpenCode's gate
// (packages/schema/src/identifier.ts): the first 12 lowercase hex characters
// represent the 48-bit timestamp.
// For descending IDs (ses_ session IDs), it uses descending inverted timestamp within recent window.
// For ascending IDs (msg_ request IDs), it uses standard ascending millisecond timestamp.
// The remaining 14 characters are random base62 characters.
func NewOCShapeID(descending bool) string {
	const alnumChars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	var timePart string
	if descending {
		var randBytes [4]byte
		mustRand(randBytes[:])
		timePart = descendingTimePart(time.Now(), randBytes[:])
	} else {
		ts, counter := ocIDTimestamp()
		value := (ts*0x1000 + counter) & 0xffffffffffff
		var tb [6]byte
		for i := 0; i < 6; i++ {
			tb[i] = byte(value >> (40 - 8*i))
		}
		timePart = hex.EncodeToString(tb[:])
	}
	var rnd [14]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		for i := range rnd {
			rnd[i] = alnumChars[uint64(i*31)%uint64(len(alnumChars))]
		}
	} else {
		for i := range rnd {
			rnd[i] = alnumChars[(rnd[i]^bootNonce[i%len(bootNonce)])%byte(len(alnumChars))]
		}
	}
	return timePart + string(rnd[:])
}

// CanonicalSessionID generates a session ID conforming to OpenCode's regex:
// ^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$ with descending inverted timestamp.
func CanonicalSessionID() string {
	return "ses_" + NewOCShapeID(true)
}

// CanonicalSessionIDFor preserves official sessions, or mints a deterministic
// session ID salted with bootNonce for a downstream conversation signal.
func CanonicalSessionIDFor(signal string) string {
	if IsOfficialSessionID(signal) {
		return signal
	}
	if signal == "" {
		return CanonicalSessionID()
	}
	hasher := sha256.New()
	hasher.Write([]byte("ses\x00"))
	hasher.Write(bootNonce)
	hasher.Write([]byte{0})
	hasher.Write([]byte(signal))
	sum := hasher.Sum(nil)
	timePart := descendingTimePart(time.Now(), sum[:6])
	const alnumChars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	var rnd [14]byte
	for i := 0; i < 14; i++ {
		rnd[i] = alnumChars[sum[6+i]%byte(len(alnumChars))]
	}
	return "ses_" + timePart + string(rnd[:])
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
