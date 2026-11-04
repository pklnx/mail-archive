package blobcrypt

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// fingerprintInfo is the HMAC message of a KEK fingerprint.
const fingerprintInfo = "mail-archive kek fingerprint v1"

// FingerprintSize is the length of a fingerprint in bytes (16 hex digits).
const FingerprintSize = 8

// GenerateKEK returns a new random archive key (KEK).
func GenerateKEK() ([]byte, error) {
	k := make([]byte, KeySize)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	return k, nil
}

// EncodeKEK is the base64 form of a KEK, as in the key file.
func EncodeKEK(kek []byte) string { return base64.StdEncoding.EncodeToString(kek) }

// ParseKEK decodes a base64 (standard or URL alphabet, padded or not) KEK
// of KeySize bytes. Surrounding white space is ignored. Errors never
// contain the value.
func ParseKEK(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("the archive key is empty")
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		k, err := enc.DecodeString(s)
		if err == nil {
			if len(k) != KeySize {
				return nil, fmt.Errorf("the archive key must be %d bytes, got %d (create one with `mail-archive encryption keygen`)", KeySize, len(k))
			}
			return k, nil
		}
	}
	return nil, errors.New("the archive key is not valid base64 (create one with `mail-archive encryption keygen`)")
}

// Fingerprint identifies a KEK without revealing it: the first 8 bytes of
// HMAC-SHA256(KEK, "mail-archive kek fingerprint v1"), in hex.
func Fingerprint(kek []byte) string {
	m := hmac.New(sha256.New, kek)
	m.Write([]byte(fingerprintInfo))
	return hex.EncodeToString(m.Sum(nil)[:FingerprintSize])
}
