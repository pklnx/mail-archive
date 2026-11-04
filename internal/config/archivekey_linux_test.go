//go:build linux

package config

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestLoadArchiveKeyBindMount reproduces the Compose layout with a real
// bind mount: a key file reached through a mount of <data>/keys is refused.
// Needs root (mount); skipped otherwise.
func TestLoadArchiveKeyBindMount(t *testing.T) {
	dataDir := t.TempDir()
	keysInData := filepath.Join(dataDir, "keys")
	if err := os.Mkdir(keysInData, 0o700); err != nil {
		t.Fatal(err)
	}
	mnt := t.TempDir()
	if err := syscall.Mount(keysInData, mnt, "", syscall.MS_BIND, ""); err != nil {
		t.Skipf("bind mount not possible here: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Unmount(mnt, 0) })
	file := writeKeyFile(t, mnt, newKEK(t), 0o400)
	if _, _, err := LoadArchiveKey(file, "", dataDir, nil); err == nil || !strings.Contains(err.Error(), "inside the data directory") {
		t.Fatalf("key file through a bind mount of <data>/keys: %v", err)
	}
}
