// Package auth hashes passwords, creates session tokens and limits login
// attempts for the web UI.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Password length limits. Only the length is checked: long passphrases
// beat composition rules.
const (
	MinPasswordLength = 12   // characters
	MaxPasswordLength = 1024 // bytes, bounds the hashing work
)

// ValidatePassword checks a new password.
func ValidatePassword(pw string) error {
	if utf8.RuneCountInString(pw) < MinPasswordLength {
		return fmt.Errorf("the password needs at least %d characters", MinPasswordLength)
	}
	if len(pw) > MaxPasswordLength {
		return fmt.Errorf("the password must not be longer than %d bytes", MaxPasswordLength)
	}
	return nil
}

var userNamePattern = regexp.MustCompile(`^[a-z0-9._-]{1,64}$`)

// NormalizeUserName lowercases and trims a login name, so that "Patrick"
// and "patrick" are the same user.
func NormalizeUserName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// ValidateUserName checks a normalized login name.
func ValidateUserName(name string) error {
	if !userNamePattern.MatchString(name) {
		return errors.New("user names have 1 to 64 characters: a-z, 0-9, dot, hyphen, underscore")
	}
	return nil
}

// Params are Argon2id cost parameters.
type Params struct {
	Memory  uint32 // KiB
	Time    uint32
	Threads uint8
}

// DefaultParams follow the second recommendation of RFC 9106 (64 MiB).
var DefaultParams = Params{Memory: 64 * 1024, Time: 3, Threads: 4}

const (
	saltLen = 16
	keyLen  = 32
)

// ErrMalformedHash is returned for stored hashes that cannot be parsed.
var ErrMalformedHash = errors.New("malformed password hash")

// Hasher hashes and verifies passwords. It runs at most two hashes at a
// time, which bounds memory use when many logins arrive at once.
type Hasher struct {
	params Params
	slots  chan struct{}

	dummyOnce sync.Once
	dummy     string
}

// NewHasher creates a Hasher for new hashes with the given parameters.
func NewHasher(p Params) *Hasher {
	return &Hasher{params: p, slots: make(chan struct{}, 2)}
}

func (h *Hasher) acquire(ctx context.Context) error {
	select {
	case h.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Hasher) release() { <-h.slots }

// Hash returns a PHC string ($argon2id$v=19$m=…,t=…,p=…$salt$key).
func (h *Hasher) Hash(ctx context.Context, password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()
	key := argon2.IDKey([]byte(password), salt, h.params.Time, h.params.Memory, h.params.Threads, keyLen)
	return encode(h.params, salt, key), nil
}

// Verify reports whether password matches the stored hash, and whether the
// hash should be renewed because it uses other parameters than h.
func (h *Hasher) Verify(ctx context.Context, stored, password string) (ok, rehash bool, err error) {
	p, salt, key, err := decode(stored)
	if err != nil {
		return false, false, err
	}
	if err := h.acquire(ctx); err != nil {
		return false, false, err
	}
	defer h.release()
	got := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, uint32(len(key))) //nolint:gosec // length bounded by decode
	ok = subtle.ConstantTimeCompare(got, key) == 1
	return ok, ok && p != h.params, nil
}

// VerifyDummy takes as long as verifying a real password. It is used for
// unknown user names, so that response times do not reveal which names
// exist.
func (h *Hasher) VerifyDummy(ctx context.Context, password string) {
	h.dummyOnce.Do(func() {
		salt := make([]byte, saltLen)
		_, _ = rand.Read(salt)
		h.dummy = encode(h.params, salt, make([]byte, keyLen))
	})
	_, _, _ = h.Verify(ctx, h.dummy, password)
}

var b64 = base64.RawStdEncoding

func encode(p Params, salt, key []byte) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Time, p.Threads, b64.EncodeToString(salt), b64.EncodeToString(key))
}

func decode(s string) (Params, []byte, []byte, error) {
	var p Params
	parts := strings.Split(s, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return p, nil, nil, ErrMalformedHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return p, nil, nil, ErrMalformedHash
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Threads); err != nil {
		return p, nil, nil, ErrMalformedHash
	}
	// Bounds keep a corrupted row from exhausting memory or CPU.
	if p.Memory < 8 || p.Memory > 1<<21 || p.Time < 1 || p.Time > 100 || p.Threads < 1 {
		return p, nil, nil, ErrMalformedHash
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return p, nil, nil, ErrMalformedHash
	}
	key, err := b64.DecodeString(parts[5])
	if err != nil || len(key) < 16 || len(key) > 128 {
		return p, nil, nil, ErrMalformedHash
	}
	return p, salt, key, nil
}
