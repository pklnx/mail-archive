package blobcrypt

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)

func newTestKeyring(t *testing.T) (*Keyring, []byte) {
	t.Helper()
	kek := randomKey(t)
	k, err := NewKeyring(kek, "archive key", t0)
	if err != nil {
		t.Fatal(err)
	}
	return k, kek
}

func reparse(t *testing.T, k *Keyring) (*Keyring, []byte) {
	t.Helper()
	data, err := k.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParseKeyring(data)
	if err != nil {
		t.Fatal(err)
	}
	return p, data
}

func TestKeyringRoundTrip(t *testing.T) {
	k, kek := newTestKeyring(t)
	id, dk, err := k.Current()
	if err != nil || id == 0 || len(dk) != KeySize {
		t.Fatalf("Current: %08x, %d bytes, %v", id, len(dk), err)
	}
	p, _ := reparse(t, k)
	if p.Opened() {
		t.Fatal("parsed keyring is open before Open")
	}
	if _, ok := p.DataKey(id); ok {
		t.Fatal("data key available before Open")
	}
	if p.ArchiveID() != k.ArchiveID() || p.Generation() != 1 || p.CurrentKey() != id {
		t.Fatalf("metadata changed: %s %d %08x", p.ArchiveID(), p.Generation(), p.CurrentKey())
	}
	if err := p.Open(kek); err != nil {
		t.Fatal(err)
	}
	if got, ok := p.DataKey(id); !ok || !bytes.Equal(got, dk) {
		t.Fatal("data key differs after the round trip")
	}
	// A file encrypted with the keyring opens with the reparsed one.
	ct := encrypt(t, id, dk, PurposeBackup, pattern(5), nil)
	if _, err := openBytes(ct, p, PurposeBackup, nil); err != nil {
		t.Fatal(err)
	}
	if err := p.Open(randomKey(t)); !errors.Is(err, ErrNoSlot) {
		t.Errorf("wrong KEK: %v, want ErrNoSlot", err)
	}
}

func TestKeyringSlots(t *testing.T) {
	k, kek := newTestKeyring(t)
	rk := randomKey(t)
	if err := k.AddSlot(rk, "recovery key", t0); err != nil {
		t.Fatal(err)
	}
	if k.Generation() != 2 {
		t.Errorf("generation %d after AddSlot, want 2", k.Generation())
	}
	if err := k.AddSlot(rk, "again", t0); err == nil {
		t.Error("second slot for the same key accepted")
	}
	_, beforeData := reparse(t, k)
	for _, key := range [][]byte{kek, rk} {
		p, _ := reparse(t, k)
		if err := p.Open(key); err != nil {
			t.Fatalf("slot %s: %v", Fingerprint(key), err)
		}
	}

	if err := k.RemoveSlot(Fingerprint(rk)); err != nil {
		t.Fatal(err)
	}
	if k.Generation() != 3 || len(k.Slots()) != 1 {
		t.Errorf("after RemoveSlot: generation %d, %d slots", k.Generation(), len(k.Slots()))
	}
	after, _ := reparse(t, k)
	if err := after.Open(rk); !errors.Is(err, ErrNoSlot) {
		t.Errorf("removed key opens the new keyring: %v", err)
	}
	// Documented: the removed key still opens the older copy.
	old, _ := ParseKeyring(beforeData)
	if err := old.Open(rk); err != nil {
		t.Errorf("removed key no longer opens the old copy: %v", err)
	}
	if err := k.RemoveSlot(Fingerprint(kek)); err == nil {
		t.Error("last slot removed")
	}
	if err := k.RemoveSlot("0000000000000000"); err == nil {
		t.Error("unknown slot removed")
	}

	unopened, _ := reparse(t, k)
	if err := unopened.AddSlot(randomKey(t), "x", t0); !errors.Is(err, ErrNotOpen) {
		t.Errorf("AddSlot on an unopened keyring: %v", err)
	}
	if err := unopened.RemoveSlot(Fingerprint(kek)); !errors.Is(err, ErrNotOpen) {
		t.Errorf("RemoveSlot on an unopened keyring: %v", err)
	}
	if _, err := unopened.Marshal(); !errors.Is(err, ErrNotOpen) {
		t.Errorf("Marshal of an unopened keyring: %v", err)
	}
	for i := len(k.Slots()); i < MaxSlots; i++ {
		if err := k.AddSlot(randomKey(t), "r", t0); err != nil {
			t.Fatal(err)
		}
	}
	if err := k.AddSlot(randomKey(t), "r", t0); err == nil {
		t.Error("slot beyond MaxSlots accepted")
	}
	for _, l := range []string{"", strings.Repeat("x", MaxLabelLen+1), "a\nb", "\xff"} {
		if err := k.RemoveSlot(k.Slots()[1].Fingerprint); err != nil {
			t.Fatal(err)
		}
		if err := k.AddSlot(randomKey(t), l, t0); err == nil {
			t.Errorf("label %q accepted", l)
		}
	}
}

// edit changes the JSON of a keyring file.
func edit(t *testing.T, data []byte, fn func(m map[string]any)) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	fn(m)
	b, _ := json.Marshal(m)
	return b
}

// TestKeyringTamper: no part of the metadata can change without a KEK.
func TestKeyringTamper(t *testing.T) {
	k, kek := newTestKeyring(t)
	rk := randomKey(t)
	if err := k.AddSlot(rk, "recovery key", t0); err != nil {
		t.Fatal(err)
	}
	_, data := reparse(t, k)
	other, _ := newTestKeyring(t)
	_, otherData := reparse(t, other)
	slot := func(m map[string]any, i int) map[string]any { return m["slots"].([]any)[i].(map[string]any) }

	opens := map[string][]byte{
		"generation":    edit(t, data, func(m map[string]any) { m["generation"] = 7 }),
		"current key":   edit(t, data, func(m map[string]any) { m["current_key"] = 12345 }),
		"archive id":    edit(t, data, func(m map[string]any) { m["archive_id"] = strings.Repeat("ab", 16) }),
		"label":         edit(t, data, func(m map[string]any) { slot(m, 0)["label"] = "evil" }),
		"created":       edit(t, data, func(m map[string]any) { slot(m, 0)["created"] = "2020-01-01T00:00:00Z" }),
		"slot removed":  edit(t, data, func(m map[string]any) { m["slots"] = m["slots"].([]any)[:1] }),
		"slots swapped": edit(t, data, func(m map[string]any) { s := m["slots"].([]any); s[0], s[1] = s[1], s[0] }),
		"data keys of another keyring": edit(t, data, func(m map[string]any) {
			var o map[string]any
			_ = json.Unmarshal(otherData, &o)
			m["sealed"] = o["sealed"]
		}),
		"wrapped key of the other slot": edit(t, data, func(m map[string]any) { slot(m, 0)["wrapped"] = slot(m, 1)["wrapped"] }),
	}
	for name, d := range opens {
		p, err := ParseKeyring(d)
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		if err := p.Open(kek); !errors.Is(err, ErrKeyringCorrupt) {
			t.Errorf("%s: Open = %v, want ErrKeyringCorrupt", name, err)
		}
	}

	parses := map[string][]byte{
		"unknown field":      edit(t, data, func(m map[string]any) { m["extra"] = 1 }),
		"format":             edit(t, data, func(m map[string]any) { m["format"] = "mail-archive keyring 2" }),
		"generation 0":       edit(t, data, func(m map[string]any) { m["generation"] = 0 }),
		"current key 0":      edit(t, data, func(m map[string]any) { m["current_key"] = 0 }),
		"upper-case id":      edit(t, data, func(m map[string]any) { m["archive_id"] = strings.ToUpper(m["archive_id"].(string)) }),
		"no slots":           edit(t, data, func(m map[string]any) { m["slots"] = []any{} }),
		"duplicate slot":     edit(t, data, func(m map[string]any) { s := m["slots"].([]any); m["slots"] = []any{s[0], s[0]} }),
		"short wrapped key":  edit(t, data, func(m map[string]any) { slot(m, 0)["wrapped"] = "AAAA" }),
		"local created time": edit(t, data, func(m map[string]any) { slot(m, 0)["created"] = "2026-11-04T11:00:00+01:00" }),
		"short data keys":    edit(t, data, func(m map[string]any) { m["sealed"] = "AAAA" }),
		"trailing data":      append(bytes.Clone(data), []byte(`{}`)...),
		"not JSON":           []byte("MA-ENC1\n"),
		"too large":          bytes.Repeat([]byte(" "), maxKeyringSize+1),
	}
	for name, d := range parses {
		if _, err := ParseKeyring(d); !errors.Is(err, ErrKeyringCorrupt) {
			t.Errorf("%s: ParseKeyring = %v, want ErrKeyringCorrupt", name, err)
		}
	}
}

// The keyring vector's KEK is derived from a public label: a test input,
// not a secret.
func vectorKEK() []byte { s := sha256.Sum256([]byte("mail-archive test vector kek 1")); return s[:] }

// TestKeyringKnownAnswer freezes the keyring format: a keyring written by
// this version must stay readable.
func TestKeyringKnownAnswer(t *testing.T) {
	path := filepath.Join("testdata", "keyring-v1.json")
	if *update {
		k, err := NewKeyring(vectorKEK(), "archive key", t0)
		if err != nil {
			t.Fatal(err)
		}
		if err := WriteKeyringFile(path, k); err != nil {
			t.Fatal(err)
		}
		_ = os.Chmod(path, 0o644) //nolint:gosec // test data
	}
	k, err := ReadKeyringFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Open(vectorKEK()); err != nil {
		t.Fatal(err)
	}
	// The vector file of the format test is encrypted with this
	// keyring's current key: decrypting one ties both formats together.
	id, dk, _ := k.Current()
	if k.Generation() != 1 || len(k.Slots()) != 1 || k.Slots()[0].Label != "archive key" || !k.Slots()[0].Created.Equal(t0) {
		t.Errorf("metadata: generation %d, slots %+v", k.Generation(), k.Slots())
	}
	ct := encrypt(t, id, dk, PurposeMessage, pattern(10), sum(pattern(10)))
	if _, err := openBytes(ct, k, PurposeMessage, sum(pattern(10))); err != nil {
		t.Fatal(err)
	}
}

func TestKeyringFile(t *testing.T) {
	dataDir := t.TempDir()
	path := KeyringPath(dataDir)
	if _, err := ReadKeyringFile(path); !errors.Is(err, ErrNoKeyring) || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing keyring: %v", err)
	}
	k, kek := newTestKeyring(t)
	if err := WriteKeyringFile(path, k); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]fs.FileMode{path: 0o600, filepath.Dir(path): 0o700} {
		info, err := os.Stat(p)
		if err != nil || info.Mode().Perm() != want {
			t.Errorf("%s: mode %v, want %v (%v)", p, info.Mode().Perm(), want, err)
		}
	}

	// A crash between writing the temporary file and the rename keeps the
	// old keyring and leaves no temporary file behind.
	if err := k.AddSlot(randomKey(t), "recovery key", t0); err != nil {
		t.Fatal(err)
	}
	crash := errors.New("crash before rename")
	err := writeKeyringFile(path, k, fileOps{rename: func(string, string) error { return crash }})
	if !errors.Is(err, crash) {
		t.Fatalf("injected crash: %v", err)
	}
	got, err := ReadKeyringFile(path)
	if err != nil || got.Generation() != 1 {
		t.Fatalf("after the crash: generation %v, %v; want the old file", got, err)
	}
	if err := got.Open(kek); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("files left after the failed replace: %v", entries)
	}

	// A temporary file left by a killed process is ignored, and the next
	// replace succeeds.
	stray := filepath.Join(filepath.Dir(path), ".keyring-123.tmp")
	if err := os.WriteFile(stray, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteKeyringFile(path, k); err != nil {
		t.Fatal(err)
	}
	got, err = ReadKeyringFile(path)
	if err != nil || got.Generation() != 2 {
		t.Fatalf("after the retry: %v, %v", got, err)
	}
	if err := os.WriteFile(path, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadKeyringFile(path); !errors.Is(err, ErrKeyringCorrupt) {
		t.Errorf("garbage keyring: %v", err)
	}
}

func TestLockKeyring(t *testing.T) {
	dataDir := t.TempDir()
	unlock, err := LockKeyring(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LockKeyring(dataDir); !errors.Is(err, ErrLocked) {
		t.Fatalf("second lock: %v, want ErrLocked", err)
	}
	unlock()
	unlock2, err := LockKeyring(dataDir)
	if err != nil {
		t.Fatalf("lock after unlock: %v", err)
	}
	unlock2()
}

func TestParseKEK(t *testing.T) {
	k := randomKey(t)
	for _, s := range []string{EncodeKEK(k), " " + EncodeKEK(k) + "\n", strings.TrimRight(EncodeKEK(k), "=")} {
		got, err := ParseKEK(s)
		if err != nil || !bytes.Equal(got, k) {
			t.Errorf("ParseKEK: %v", err)
		}
	}
	secret := "c2VjcmV0LXZhbHVlLXRoYXQtaXMtdG9vLXNob3J0"
	for _, s := range []string{"", "!!!", secret} {
		_, err := ParseKEK(s)
		if err == nil {
			t.Errorf("ParseKEK(%q) accepted", s)
		} else if s != "" && strings.Contains(err.Error(), s) {
			t.Errorf("error repeats the value: %v", err)
		}
	}
	if Fingerprint(k) == Fingerprint(randomKey(t)) || len(Fingerprint(k)) != 16 {
		t.Error("fingerprint")
	}
}
