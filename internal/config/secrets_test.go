package config

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/keepia/archive/internal/blobcrypt"
	"github.com/keepia/archive/internal/crypto"
)

// newSecretKey returns a random secret key and its base64 form.
func newSecretKey(t *testing.T) ([]byte, string) {
	t.Helper()
	enc, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	key, err := crypto.ParseKey(enc)
	if err != nil {
		t.Fatal(err)
	}
	return key, enc
}

// writeSecretFile writes content to a file secret.key in dir with mode.
func writeSecretFile(t *testing.T, dir, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, "secret.key")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestLoadSecretKey (#396): the secret key from its file or its variable,
// the file under the rules of the archive key file; every refusal is a
// SecretKeyError that names the reason and never the key.
func TestLoadSecretKey(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	key, enc := newSecretKey(t)
	file := writeSecretFile(t, t.TempDir(), enc+"\n", 0o400)

	got, source, err := LoadSecretKey(file, "", dataDir)
	if err != nil || !bytes.Equal(got, key) || source != "file "+file {
		t.Fatalf("file: %v, %q", err, source)
	}
	got, source, err = LoadSecretKey("", enc, dataDir)
	if err != nil || !bytes.Equal(got, key) || source != EnvSecretKey {
		t.Fatalf("value: %v, %q", err, source)
	}
	if got, _, err := LoadSecretKey(" ", "", dataDir); got != nil || err != nil {
		t.Fatalf("not configured: %v, %v", got, err)
	}
	if _, _, err := LoadSecretKey(writeSecretFile(t, t.TempDir(), enc, 0o600), "", dataDir); err != nil {
		t.Errorf("mode 0600 refused: %v", err)
	}

	// A malformed key that must not show up in the message either: valid
	// base64 of 20 bytes instead of 32, built here so that no key-like
	// literal sits in the source.
	badKey := base64.RawStdEncoding.EncodeToString([]byte(strings.Repeat("not-a-key-", 2)))
	notRegular := filepath.Join(t.TempDir(), "dir")
	_ = os.Mkdir(notRegular, 0o700)
	for name, c := range map[string]struct {
		file, value, want string
	}{
		"both set":        {file, enc, "both " + EnvSecretKeyFile + " and " + EnvSecretKey},
		"group-readable":  {writeSecretFile(t, t.TempDir(), enc, 0o440), "", "mode 0440"},
		"world-readable":  {writeSecretFile(t, t.TempDir(), enc, 0o404), "", "mode 0404"},
		"in the data dir": {writeSecretFile(t, dataDir, enc, 0o400), "", "inside the data directory"},
		"not regular":     {notRegular, "", "not a regular file"},
		"missing":         {filepath.Join(dataDir, "..", "missing.key"), "", "no such file"},
		"malformed file":  {writeSecretFile(t, t.TempDir(), badKey, 0o400), "", EnvSecretKeyFile + ": secret key must be 32 bytes"},
		"malformed value": {"", badKey, EnvSecretKey + ": secret key must be 32 bytes"},
		"not base64":      {"", "%%%", "not valid base64"},
	} {
		t.Run(name, func(t *testing.T) {
			got, _, err := LoadSecretKey(c.file, c.value, dataDir)
			var keyErr *SecretKeyError
			if got != nil || !errors.As(err, &keyErr) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, %v; want a SecretKeyError with %q", got, err, c.want)
			}
			for _, secret := range []string{enc, badKey, base64.RawStdEncoding.EncodeToString(key)} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("the message holds the key: %v", err)
				}
			}
		})
	}
}

// TestLoadSecretKeyFile (#396): Load takes the secret key from its file,
// and an archive key equal to it is refused also then, by Load and by
// ArchiveKeyFromEnv.
func TestLoadSecretKeyFile(t *testing.T) {
	t.Setenv(EnvDatabaseURL, "postgres://localhost/x")
	t.Setenv(EnvDataDir, t.TempDir())
	key, enc := newSecretKey(t)
	file := writeSecretFile(t, t.TempDir(), enc+"\n", 0o400)
	t.Setenv(EnvSecretKeyFile, file)
	t.Setenv(EnvSecretKey, "")
	cfg, err := Load()
	if err != nil || !bytes.Equal(cfg.SecretKey, key) || cfg.SecretKeySource != "file "+file {
		t.Fatalf("Load: %v", err)
	}
	t.Setenv(EnvArchiveKey, blobcrypt.EncodeKEK(key))
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "equals "+EnvSecretKey) {
		t.Errorf("Load with the archive key equal to the secret key file: %v", err)
	}
	if _, _, err := ArchiveKeyFromEnv(); err == nil || !strings.Contains(err.Error(), "equals "+EnvSecretKey) {
		t.Errorf("ArchiveKeyFromEnv with the archive key equal to the secret key file: %v", err)
	}
	t.Setenv(EnvSecretKey, enc)
	var keyErr *SecretKeyError
	if _, err := Load(); !errors.As(err, &keyErr) {
		t.Errorf("Load with both set: %v", err)
	}
}

// TestChildEnv (#396): a child gets the kept variables of the process and
// the extra ones, nothing else; a KEEPIA_ARCHIVE_*, MAIL_ARCHIVE_* (the old
// names, #585) or PG* variable of the process never, even when it is kept
// by mistake.
func TestChildEnv(t *testing.T) {
	environ := []string{
		"PATH=/usr/bin", "HOME=/home/x", "TZ=Europe/Berlin", "PATH=/second",
		EnvSecretKey + "=marker-secret", EnvSecretKeyFile + "=/run/secret.key",
		EnvKeyringCopy + "=marker-ring", EnvDatabaseURL + "=postgres://u:marker-pw@h/db",
		"MAIL_ARCHIVE_SECRET_KEY=marker-old-secret", "MAIL_ARCHIVE_DATABASE_URL=postgres://u:marker-old-pw@h/db",
		"PGPASSWORD=marker-pg", "UNRELATED=marker-x",
		"NOEQUALS",
	}
	keep := []string{"PATH", "TZ", "LANG", EnvSecretKey, EnvKeyringCopy, "MAIL_ARCHIVE_SECRET_KEY", "PGPASSWORD"}
	got := ChildEnv(environ, keep, EnvDataDir+"=/data", "PGHOST=db")
	want := []string{"PATH=/usr/bin", "TZ=Europe/Berlin", EnvDataDir + "=/data", "PGHOST=db"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := ChildEnv(environ, nil); len(got) != 0 {
		t.Errorf("nothing kept: %q", got)
	}
}
