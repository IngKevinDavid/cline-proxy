package kit

import (
	"encoding/hex"
	"testing"
	"time"
)

func TestCanonicalSessionIDDescendingWindow(t *testing.T) {
	now := time.Now()
	nowMilli := now.UnixMilli()
	epochBase := (nowMilli >> 36) << 36

	for i := 0; i < 100; i++ {
		sid := CanonicalSessionID()
		if !canonicalSessionPattern.MatchString(sid) {
			t.Fatalf("session ID %q failed canonical pattern", sid)
		}
		timeHex := sid[4:16]
		if !descendingTimePartPattern.MatchString(timeHex) {
			t.Fatalf("session ID %q failed descending time pattern %q", sid, timeHex)
		}
		rawBytes, err := hex.DecodeString(timeHex)
		if err != nil {
			t.Fatalf("failed to decode time hex: %v", err)
		}
		inverted := uint64(rawBytes[0])<<40 | uint64(rawBytes[1])<<32 |
			uint64(rawBytes[2])<<24 | uint64(rawBytes[3])<<16 |
			uint64(rawBytes[4])<<8 | uint64(rawBytes[5])
		current := ^inverted & 0xFFFFFFFFFFFF
		low36 := int64(current >> 12)
		tsMilli := epochBase | low36
		// If low36 wrapped around epoch boundary
		if tsMilli > nowMilli+86400000 {
			tsMilli -= (1 << 36)
		}
		ts := time.UnixMilli(tsMilli)

		// Verify timestamp is recent (within 35 days in past and not in future)
		if ts.After(now.Add(24 * time.Hour)) {
			t.Fatalf("session timestamp %v is in future relative to %v", ts, now)
		}
		if ts.Before(now.Add(-35 * 24 * time.Hour)) {
			t.Fatalf("session timestamp %v is too far in past relative to %v", ts, now)
		}
	}
}

func TestCanonicalSessionIDFor(t *testing.T) {
	// Make a genuinely official ID
	gen := CanonicalSessionID()
	if got := CanonicalSessionIDFor(gen); got != gen {
		t.Fatalf("expected official session to stay verbatim, got %q != %q", got, gen)
	}

	// UUID or foreign string gets mapped deterministically
	sig := "my-conversation-uuid-12345"
	s1 := CanonicalSessionIDFor(sig)
	s2 := CanonicalSessionIDFor(sig)
	if s1 != s2 {
		t.Fatalf("expected deterministic session for same signal, got %q != %q", s1, s2)
	}
	if !canonicalSessionPattern.MatchString(s1) {
		t.Fatalf("derived session %q failed canonical pattern", s1)
	}
}
