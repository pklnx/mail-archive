package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pklnx/mail-archive/internal/blobcrypt"
)

func newKEK(t *testing.T) []byte {
	t.Helper()
	k, err := blobcrypt.GenerateKEK()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func writeKeyFile(t *testing.T, dir string, kek []byte, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, "archive.key")
	if err := os.WriteFile(p, []byte(blobcrypt.EncodeKEK(kek)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadArchiveKey(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	keys := t.TempDir()
	kek := newKEK(t)
	file := writeKeyFile(t, keys, kek, 0o400)

	got, source, err := LoadArchiveKey(file, "", dataDir, nil)
	if err != nil || !bytes.Equal(got, kek) || source != "file "+file {
		t.Fatalf("file: %v, %q", err, source)
	}
	got, source, err = LoadArchiveKey("", blobcrypt.EncodeKEK(kek), dataDir, nil)
	if err != nil || !bytes.Equal(got, kek) || source != EnvArchiveKey {
		t.Fatalf("value: %v, %q", err, source)
	}
	if got, _, err := LoadArchiveKey(" ", "", dataDir, nil); got != nil || err != nil {
		t.Fatalf("not configured: %v, %v", got, err)
	}
	if _, _, err := LoadArchiveKey(writeKeyFile(t, t.TempDir(), kek, 0o600), "", dataDir, nil); err != nil {
		t.Errorf("mode 0600 refused: %v", err)
	}

	inData := writeKeyFile(t, dataDir, kek, 0o400)
	link := filepath.Join(t.TempDir(), "data-link")
	if err := os.Symlink(dataDir, link); err != nil {
		t.Fatal(err)
	}
	notRegular := filepath.Join(t.TempDir(), "dir")
	_ = os.Mkdir(notRegular, 0o700)
	refused := map[string]struct {
		file, value string
		secret      []byte
		want        string
	}{
		"both set":            {file, blobcrypt.EncodeKEK(kek), nil, "both"},
		"group-readable":      {writeKeyFile(t, t.TempDir(), kek, 0o440), "", nil, "mode 0440"},
		"world-readable":      {writeKeyFile(t, t.TempDir(), kek, 0o404), "", nil, "mode 0404"},
		"group-writable":      {writeKeyFile(t, t.TempDir(), kek, 0o620), "", nil, "mode 0620"},
		"in the data dir":     {inData, "", nil, "inside the data directory"},
		"data dir by symlink": {filepath.Join(link, "archive.key"), "", nil, "inside the data directory"},
		"not a regular file":  {notRegular, "", nil, "not a regular file"},
		"missing":             {filepath.Join(keys, "missing.key"), "", nil, "no such file"},
		"equal to secret key": {file, "", kek, "equals " + EnvSecretKey},
		"value equal secret":  {"", blobcrypt.EncodeKEK(kek), kek, "equals " + EnvSecretKey},
		"short value":         {"", "c2hvcnQ=", nil, "32 bytes"},
		"not base64":          {"", "not base64!", nil, "base64"},
	}
	for name, c := range refused {
		_, _, err := LoadArchiveKey(c.file, c.value, dataDir, c.secret)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error with %q", name, err, c.want)
			continue
		}
		if strings.Contains(err.Error(), blobcrypt.EncodeKEK(kek)) {
			t.Errorf("%s: error contains the key", name)
		}
	}
}

// TestLoadRefusesArchiveKey: a misconfigured archive key stops every
// command that loads the configuration.
func TestLoadRefusesArchiveKey(t *testing.T) {
	t.Setenv(EnvDatabaseURL, "postgres://unused")
	t.Setenv(EnvDataDir, t.TempDir())
	kek := newKEK(t)
	t.Setenv(EnvArchiveKeyFile, writeKeyFile(t, t.TempDir(), kek, 0o644))
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), EnvArchiveKeyFile) {
		t.Fatalf("Load with a world-readable key file: %v", err)
	}
	t.Setenv(EnvArchiveKeyFile, writeKeyFile(t, t.TempDir(), kek, 0o400))
	cfg, err := Load()
	if err != nil || !bytes.Equal(cfg.ArchiveKey, kek) {
		t.Fatalf("Load with a good key file: %v", err)
	}
}

func TestWithinDir(t *testing.T) {
	d := t.TempDir()
	for p, want := range map[string]bool{
		d:                           true,
		filepath.Join(d, "a", "b"):  true,
		filepath.Join(d, "..", "x"): false,
		d + "-sibling":              false,
		filepath.Join(d, "..a"):     true,
		filepath.Join(d, "x", ".."): true,
		filepath.Dir(d):             false,
	} {
		if got, err := WithinDir(p, d); err != nil || got != want {
			t.Errorf("WithinDir(%q) = %v, %v; want %v", p, got, err, want)
		}
	}
}
