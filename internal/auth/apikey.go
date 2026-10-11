package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"strings"
)

// API keys let scripts call the API without a session. A key is "ma_"
// followed by 32 random bytes in lower-case base32 without padding: 52
// characters from a-z and 2-7, all word characters, so a double click
// selects the whole key. Only its SHA-256 is stored. With 256 random bits an
// unsalted hash is safe, and the lookup by hash through a unique index
// compares hashes, not keys, so its timing says nothing about a key.

// APIKeyPrefix starts every API key.
const APIKeyPrefix = "ma_"

const apiKeyChars = 52

// APIKeyHintLength is how many trailing characters of a key are stored in
// plain text, so the owner can tell keys apart. The rest keeps 236 bits.
const APIKeyHintLength = 4

var apiKeyEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewAPIKey returns a new key, its hash for the database and its hint.
func NewAPIKey() (key string, hash []byte, hint string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, "", err
	}
	key = APIKeyPrefix + strings.ToLower(apiKeyEncoding.EncodeToString(b))
	return key, HashAPIKey(key), key[len(key)-APIKeyHintLength:], nil
}

// HashAPIKey returns the database key of an API key.
func HashAPIKey(key string) []byte {
	sum := sha256.Sum256([]byte(key))
	return sum[:]
}

// ValidAPIKeyFormat reports whether key looks like an API key: the prefix
// and 52 characters from a-z and 2-7, case-sensitive.
func ValidAPIKeyFormat(key string) bool {
	rest, ok := strings.CutPrefix(key, APIKeyPrefix)
	if !ok || len(rest) != apiKeyChars {
		return false
	}
	for i := 0; i < len(rest); i++ {
		if c := rest[i]; (c < 'a' || c > 'z') && (c < '2' || c > '7') {
			return false
		}
	}
	return true
}
