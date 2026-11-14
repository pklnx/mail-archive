package config

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keepia/archive/internal/blobcrypt"
)

// TestLoadSetupToken (#396, part 3): the setup token from its file
// (trimmed) or its variable; refused when both are set, from a file that
// breaks the key file rules, with a length outside 32 to 256, a character
// outside printable ASCII, or the value of one of the keys in any base64
// form. No refusal holds the token.
func TestLoadSetupToken(t *testing.T) {
	dataDir := t.TempDir()
	tok, err := NewSetupToken()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := LoadSetupToken("", "", dataDir); err != nil || got != "" {
		t.Fatalf("none: %q %v", got, err)
	}
	file := writeSecretFile(t, t.TempDir(), tok+"\n", 0o400)
	if got, err := LoadSetupToken(file, "", dataDir); err != nil || got != tok {
		t.Fatalf("file: %v", err)
	}
	if got, err := LoadSetupToken("", " "+tok+" ", dataDir); err != nil || got != tok {
		t.Fatalf("value: %v", err)
	}
	secret, _ := newSecretKey(t)
	archive, err := blobcrypt.GenerateKEK()
	if err != nil {
		t.Fatal(err)
	}
	inside := writeSecretFile(t, dataDir, tok, 0o400)
	for name, c := range map[string]struct{ file, value, want string }{
		"both":            {file, tok, "both " + EnvSetupTokenFile},
		"group-readable":  {writeSecretFile(t, t.TempDir(), tok, 0o640), "", "mode 0640"},
		"in the data dir": {inside, "", "inside the data directory"},
		"short":           {"", tok[:MinSetupToken-1], "it has 31"},
		"long":            {"", strings.Repeat("x", MaxSetupToken+1), "it has 257"},
		"control":         {"", tok[:40] + "\x7f", "printable ASCII"},
		"umlaut":          {"", tok[:40] + "ü", "printable ASCII"},
		"secret key":      {"", base64.RawURLEncoding.EncodeToString(secret), "one of the instance's keys"},
		"archive key":     {"", blobcrypt.EncodeKEK(archive), "one of the instance's keys"},
	} {
		_, err := LoadSetupToken(c.file, c.value, dataDir, secret, archive)
		var te *SetupTokenError
		if !errors.As(err, &te) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want a SetupTokenError with %q", name, err, c.want)
			continue
		}
		if strings.Contains(err.Error(), tok[:20]) {
			t.Errorf("%s: the refusal holds the token: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dataDir, "secret.key")); err != nil {
		t.Fatal(err) // the file of "in the data dir" was there
	}
}

// TestParseEncryptionAtSetup (#396, revision 1): on by default, off only
// when set so.
func TestParseEncryptionAtSetup(t *testing.T) {
	for v, want := range map[string]bool{"": true, "on": true, " ON ": true, "off": false, "Off": false} {
		if got, err := ParseEncryptionAtSetup(v); err != nil || got != want {
			t.Errorf("%q: %v %v", v, got, err)
		}
	}
	if _, err := ParseEncryptionAtSetup("no"); err == nil || !strings.Contains(err.Error(), EnvEncryptionAtSetup) {
		t.Errorf("no: %v", err)
	}
}
