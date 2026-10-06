package auth

import (
	"testing"
	"time"
)

func TestTOTPRFC6238Vectors(t *testing.T) {
	secret := "12345678901234567890"
	for _, tc := range []struct {
		unix int64
		want string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
		{20000000000, "353130"},
	} {
		got, err := GenerateTOTP(base32NoPadding(secret), time.Unix(tc.unix, 0).UTC())
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("at %d: got %s, want %s", tc.unix, got, tc.want)
		}
	}
}

func TestTOTPWindowAndReplayCounter(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	code, err := GenerateTOTP(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	counter, ok, err := ValidateTOTP(secret, code, now)
	if err != nil || !ok {
		t.Fatalf("current code: counter=%d ok=%v err=%v", counter, ok, err)
	}
	if _, ok, err := ValidateTOTP(secret, code, now.Add(TOTPPeriod)); err != nil || !ok {
		t.Fatalf("adjacent step should validate: ok=%v err=%v", ok, err)
	}
	if _, ok, err := ValidateTOTP(secret, code, now.Add(2*TOTPPeriod)); err != nil || ok {
		t.Fatalf("old code outside window should fail: ok=%v err=%v", ok, err)
	}
}

func TestRecoveryCodes(t *testing.T) {
	codes, err := GenerateRecoveryCodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != RecoveryCodeCount {
		t.Fatalf("got %d codes", len(codes))
	}
	seen := map[string]bool{}
	for _, code := range codes {
		if seen[code] {
			t.Fatalf("duplicate code %q", code)
		}
		seen[code] = true
		if len(code) != RecoveryCodeLength+RecoveryCodeLength/4-1 {
			t.Fatalf("unexpected formatted length for %q", code)
		}
	}
	h1 := RecoveryCodeHash([]byte("key"), codes[0])
	h2 := RecoveryCodeHash([]byte("key"), codes[0])
	if string(h1) != string(h2) {
		t.Fatal("hash is not deterministic")
	}
	if string(h1) == string(RecoveryCodeHash([]byte("other"), codes[0])) {
		t.Fatal("hash is not keyed")
	}
}

func base32NoPadding(raw string) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
	_ = alphabet
	// The RFC test secret is already the raw byte sequence; encode it for the
	// application API without padding.
	return encodeTestSecret([]byte(raw))
}

func encodeTestSecret(raw []byte) string {
	const table = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
	var out string
	var acc uint
	var bits uint
	for _, b := range raw {
		acc = acc<<8 | uint(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out += string(table[(acc>>bits)&31])
		}
	}
	if bits > 0 {
		out += string(table[(acc<<(5-bits))&31])
	}
	return out
}
