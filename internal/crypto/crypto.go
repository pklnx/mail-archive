// Package crypto encrypts secrets (such as IMAP passwords) at rest using
// AES-256-GCM.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

// KeySize is the required key length in bytes (AES-256).
const KeySize = 32

// Sealer encrypts and decrypts small secrets.
type Sealer struct {
	aead cipher.AEAD
}

// ParseKey decodes a base64 (standard or URL encoding) key of KeySize bytes.
func ParseKey(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		key, err := enc.DecodeString(s)
		if err == nil {
			if len(key) != KeySize {
				return nil, fmt.Errorf("secret key must be %d bytes, got %d", KeySize, len(key))
			}
			return key, nil
		}
	}
	return nil, errors.New("secret key is not valid base64")
}

// GenerateKey returns a new random key, base64 encoded.
func GenerateKey() (string, error) {
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(key), nil
}

// NewSealer creates a Sealer from a raw key.
func NewSealer(key []byte) (*Sealer, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("key must be %d bytes", KeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead}, nil
}

// Seal encrypts plaintext. The result is nonce || ciphertext. The optional
// associated data binds the ciphertext to a context (e.g. an account name).
func (s *Sealer) Seal(plaintext, associated []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, plaintext, associated), nil
}

// Open decrypts data produced by Seal.
func (s *Sealer) Open(sealed, associated []byte) ([]byte, error) {
	n := s.aead.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("ciphertext too short")
	}
	plaintext, err := s.aead.Open(nil, sealed[:n], sealed[n:], associated)
	if err != nil {
		return nil, errors.New("decryption failed (wrong key or corrupted data)")
	}
	return plaintext, nil
}
