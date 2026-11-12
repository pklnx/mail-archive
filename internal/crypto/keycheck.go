package crypto

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// keyCheckInput is what KeyCheck authenticates. A new version of the
// check gets a new input.
const keyCheckInput = "mail-archive secret key check v1"

// KeyCheck is the check value of a secret key (#396):
// HMAC-SHA256(key, "mail-archive secret key check v1"). The database
// keeps it, so that a start with another key is refused before anything
// is sealed with it. It does not reveal the key.
func KeyCheck(key []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(keyCheckInput))
	return m.Sum(nil)
}

// SameKeyCheck compares two check values in constant time.
func SameKeyCheck(a, b []byte) bool { return hmac.Equal(a, b) }

// KeyCheckFingerprint names a check value in messages: the hex of its
// first 8 bytes. Two keys with the same fingerprint are the same key, for
// all practical purposes; the fingerprint does not reveal the key.
func KeyCheckFingerprint(check []byte) string {
	if len(check) > 8 {
		check = check[:8]
	}
	return hex.EncodeToString(check)
}
