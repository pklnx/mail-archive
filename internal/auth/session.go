package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"time"
)

// Session lifetimes: a session ends after IdleTimeout without use and after
// MaxSessionAge at the latest.
const (
	IdleTimeout   = 7 * 24 * time.Hour
	MaxSessionAge = 30 * 24 * time.Hour
)

// NewSessionToken returns a random token for the cookie and its hash for
// the database.
func NewSessionToken() (token string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashSessionToken(token), nil
}

// HashSessionToken returns the database key of a session token.
func HashSessionToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
