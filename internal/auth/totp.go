package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 interoperable TOTP uses HMAC-SHA1.
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// TOTPSecretBytes is the size of generated TOTP secrets.
	TOTPSecretBytes = 20
	TOTPDigits      = 6
	TOTPPeriod      = 30 * time.Second
	TOTPWindow      = 1
	RecoveryCodeCount = 10
	RecoveryCodeLength = 16
)

const recoveryAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// GenerateTOTPSecret returns a cryptographically random Base32 secret suitable
// for use with RFC 6238 authenticator applications.
func GenerateTOTPSecret() (string, error) {
	b := make([]byte, TOTPSecretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b), nil
}

// GenerateTOTP returns the six-digit RFC 6238 code for secret at t.
func GenerateTOTP(secret string, t time.Time) (string, error) {
	key, err := decodeTOTPSecret(secret)
	if err != nil {
		return "", err
	}
	return totpForCounter(key, uint64(t.Unix()/int64(TOTPPeriod/time.Second))), nil //nolint:gosec // Unix TOTP counters are non-negative and far below uint64 limits.
}

// ValidateTOTP checks a code at now and returns the matched time-step counter.
// The accepted window is the current step plus one adjacent step on either side.
func ValidateTOTP(secret, code string, now time.Time) (uint64, bool, error) {
	key, err := decodeTOTPSecret(secret)
	if err != nil {
		return 0, false, err
	}
	if len(code) != TOTPDigits {
		return 0, false, nil
	}
	current := now.Unix() / int64(TOTPPeriod/time.Second)
	for delta := -TOTPWindow; delta <= TOTPWindow; delta++ {
		counter := current + int64(delta)
		if counter < 0 {
			continue
		}
		if hmac.Equal([]byte(totpForCounter(key, uint64(counter))), []byte(code)) {
			return uint64(counter), true, nil
		}
	}
	return 0, false, nil
}

func decodeTOTPSecret(secret string) ([]byte, error) {
	secret = strings.ToUpper(strings.TrimSpace(secret))
	if secret == "" {
		return nil, errors.New("empty TOTP secret")
	}
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.TrimRight(secret, "="))
	if err != nil || len(key) < 16 {
		return nil, errors.New("invalid TOTP secret")
	}
	return key, nil
}

func totpForCounter(key []byte, counter uint64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	mac := hmac.New(sha1.New, key) //nolint:gosec // RFC 6238 specifies HMAC-SHA1 for the interoperable profile.
	_, _ = mac.Write(msg[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	bin := (uint32(sum[offset])&0x7f)<<24 |
		(uint32(sum[offset+1]) << 16) |
		(uint32(sum[offset+2]) << 8) |
		uint32(sum[offset+3])
	return fmt.Sprintf("%06d", bin%1000000)
}

// GenerateRecoveryCodes creates high-entropy one-time recovery codes.
func GenerateRecoveryCodes() ([]string, error) {
	const alphabetLen = len(recoveryAlphabet)
	limit := byte(256 / alphabetLen * alphabetLen)
	out := make([]string, RecoveryCodeCount)
	buf := make([]byte, 1)
	for i := range out {
		var b strings.Builder
		for n := 0; n < RecoveryCodeLength; {
			if _, err := rand.Read(buf); err != nil {
				return nil, err
			}
			if buf[0] >= limit {
				continue
			}
			if n > 0 && n%4 == 0 {
				b.WriteByte('-')
			}
			b.WriteByte(recoveryAlphabet[int(buf[0])%len(recoveryAlphabet)])
			n++
		}
		out[i] = b.String()
	}
	return out, nil
}

// RecoveryCodeHash returns a keyed hash suitable for database lookup. The
// key is derived from MAIL_ARCHIVE_SECRET_KEY, so a database-only leak cannot
// be used to verify guesses offline.
func RecoveryCodeHash(secretKey []byte, code string) []byte {
	k := hmac.New(sha256.New, secretKey)
	_, _ = k.Write([]byte("mail-archive recovery codes v1"))
	key := k.Sum(nil)
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(strings.ToLower(strings.TrimSpace(code))))
	return h.Sum(nil)
}
