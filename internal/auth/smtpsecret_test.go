package auth

import (
	"bytes"
	"crypto/sha256"
	"strings"
	"testing"
)

func TestNewSMTPSecret(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		secret, hash, hint, err := NewSMTPSecret()
		if err != nil {
			t.Fatal(err)
		}
		if len(secret) != 56 || !strings.HasPrefix(secret, "mas_") || !ValidSMTPSecretFormat(secret) {
			t.Fatalf("secret %q", secret)
		}
		if strings.Trim(secret[4:], "abcdefghijklmnopqrstuvwxyz234567") != "" {
			t.Fatalf("secret %q has other characters", secret)
		}
		if sum := sha256.Sum256([]byte(secret)); !bytes.Equal(hash, sum[:]) || !bytes.Equal(HashSMTPSecret(secret), hash) {
			t.Fatal("hash is not the SHA-256 of the secret")
		}
		if hint != secret[52:] || len(hint) != 4 {
			t.Fatalf("hint %q of %q", hint, secret)
		}
		// Neither kind of credential passes for the other.
		if ValidAPIKeyFormat(secret) {
			t.Fatalf("secret %q and API key formats overlap", secret)
		}
		if seen[secret] {
			t.Fatal("duplicate secret")
		}
		seen[secret] = true
	}
	key, _, _, _ := NewAPIKey()
	if ValidSMTPSecretFormat(key) {
		t.Fatalf("API key %q passes as SMTP secret", key)
	}
}

func TestValidSMTPSecretFormat(t *testing.T) {
	secret, _, _, _ := NewSMTPSecret()
	for _, bad := range []string{
		"", "mas_", secret[:55], secret + "a", "MAS_" + secret[4:], "mas_" + strings.ToUpper(secret[4:]),
		"mas_" + strings.Repeat("1", 52), "mas_" + strings.Repeat("a", 51) + "-", "ma_" + secret[4:], "xxx_" + secret[4:],
	} {
		if ValidSMTPSecretFormat(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestSMTPUsername(t *testing.T) {
	for _, id := range []int64{1, 42, 1 << 40, 9223372036854775807} {
		name := SMTPUsername(id)
		got, ok := ParseSMTPUsername(name)
		if !ok || got != id {
			t.Errorf("round trip of %d: %q gives %d %v", id, name, got, ok)
		}
	}
	if SMTPUsername(42) != "smtp-42" {
		t.Errorf("SMTPUsername(42) = %q", SMTPUsername(42))
	}
	secret, _, _, _ := NewSMTPSecret()
	for _, bad := range []string{
		"", "smtp", "smtp-", "smtp-01x", "smtp-x", "smtp-01", "smtp-0", "smtp--1", "smtp-+1", "smtp- 1", "smtp-1 ",
		"SMTP-42", "smtp_42", " smtp-42", "smtp-42\n",
		"smtp-" + strings.Repeat("9", 20), // 20 digits
		"smtp-9223372036854775808",        // 19 digits, past int64
		secret, "smtp-" + secret,
	} {
		if id, ok := ParseSMTPUsername(bad); ok {
			t.Errorf("%q accepted as %d", bad, id)
		}
	}
}
