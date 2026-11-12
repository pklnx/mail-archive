package crypto

import (
	"encoding/hex"
	"testing"
)

// TestKeyCheck (#396): the check value is HMAC-SHA256(key, "mail-archive
// secret key check v1"), fixed by a vector computed elsewhere (Python's
// hmac), so that a stored value stays valid across versions; its
// fingerprint is the hex of the first 8 bytes.
func TestKeyCheck(t *testing.T) {
	key := make([]byte, KeySize)
	for i := range key {
		key[i] = byte(i)
	}
	const want = "456886d6bcfd4c437aa1c56bb3ace73c5dbcc81ef3bbc954da649ff2c2b7a8f8"
	check := KeyCheck(key)
	if got := hex.EncodeToString(check); got != want {
		t.Fatalf("check %s, want %s", got, want)
	}
	if got := KeyCheckFingerprint(check); got != want[:16] {
		t.Errorf("fingerprint %s", got)
	}
	other := make([]byte, KeySize)
	if SameKeyCheck(check, KeyCheck(other)) || !SameKeyCheck(check, KeyCheck(key)) {
		t.Error("SameKeyCheck")
	}
}
