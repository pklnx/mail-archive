package blobstore

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/keepia/archive/internal/blobcrypt"
)

// headerKeyOf reads the data key id from the header of an encrypted file.
func headerKeyOf(t *testing.T, path string) uint32 {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := blobcrypt.ParseHeader(b)
	if err != nil {
		t.Fatal(err)
	}
	return h.KeyID
}

// rekeyStore is a recorded store with the given messages encrypted under
// the keyring's first data key, and then a new current key set, as at the
// start of `encryption rekey`. It returns the old and the new key id.
func rekeyStore(t *testing.T, contents ...string) (s *Store, rec *recorder, root string, ring *blobcrypt.Keyring, oldID, newID uint32) {
	t.Helper()
	ring = testRing(t)
	s, rec, root = newEncrypted(t, ring)
	for _, c := range contents {
		if _, _, err := s.Put(strings.NewReader(c)); err != nil {
			t.Fatal(err)
		}
	}
	oldID = ring.CurrentKey()
	newID, err := ring.AddCurrentKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetKeys(ring, true); err != nil {
		t.Fatal(err)
	}
	rec.take()
	return s, rec, root, ring, oldID, newID
}

// A crash between the new copy in tmp/ and the rename leaves the old copy
// intact and readable; the repeat re-encrypts it in place by one rename,
// and Commit synchronizes the directory (#437, Done when). Counter-proof:
// unlinking the old file before the rename (an unlink followed by a link)
// loses the message at the injected crash.
func TestRekeyCrashBeforeRename(t *testing.T) {
	content := "Subject: Neuer Schluessel\r\n\r\n" + marker + "\r\n"
	s, rec, root, _, oldID, newID := rekeyStore(t, content)
	h := sha(content)
	enc := s.abs(EncPath(h))
	before, _ := os.ReadFile(enc)
	crash := errors.New("crash")

	c, err := s.NewRekey()
	if err != nil {
		t.Fatal(err)
	}
	rec.failOnce("rename:"+enc, crash)
	if _, err := c.Message(h); !errors.Is(err, crash) {
		t.Fatalf("rename failure: %v", err)
	}
	if after, _ := os.ReadFile(enc); !bytes.Equal(after, before) {
		t.Fatal("the old copy changed")
	}
	if got, _ := readMessage(t, s, h); got != content {
		t.Fatalf("old copy reads %q", got)
	}
	if !slices.Contains(c.NamedKeys(), oldID) {
		t.Errorf("named keys %v lack the old key %08x", c.NamedKeys(), oldID)
	}
	assertNoTemp(t, root)

	rec.take()
	c, _ = s.NewRekey()
	if out, err := c.Message(h); err != nil || out != ConvertDone {
		t.Fatalf("repeat: %v %v", out, err)
	}
	ev := rec.take()
	if i := slices.Index(ev, "rename:"+enc); i < 0 || !slices.Contains(ev[:i], "sync:TEMP") {
		t.Fatalf("the new copy was not synchronized before the rename: %v", ev)
	}
	if err := c.Commit(); err != nil {
		t.Fatal(err)
	}
	if ev := rec.take(); !slices.Equal(ev[:3], barrier(filepath.Dir(enc))) {
		t.Errorf("Commit: %v, want the directory barrier first", ev)
	}
	if id := headerKeyOf(t, enc); id != newID {
		t.Fatalf("header names %08x, want %08x", id, newID)
	}
	if got, _ := readMessage(t, s, h); got != content {
		t.Fatalf("read %q", got)
	}
	if got := c.NamedKeys(); !slices.Equal(got, []uint32{newID}) {
		t.Errorf("named keys %v", got)
	}
	assertNoTemp(t, root)
	c, _ = s.NewRekey()
	if out, err := c.Message(h); err != nil || out != ConvertNone {
		t.Fatalf("again: %v %v", out, err)
	}
}

// A file that cannot be decrypted stays as it is and keeps its key named;
// a plaintext straggler next to an old-key file is dropped against the
// re-encrypted copy.
func TestRekeyCorruptAndStraggler(t *testing.T) {
	bad := "Subject: kaputt\r\n\r\n" + strings.Repeat("x", 3000) + "\r\n"
	good := "Subject: Nachzuegler\r\n\r\nInhalt\r\n"
	s, _, _, _, oldID, newID := rekeyStore(t, bad, good)
	badPath := s.abs(EncPath(sha(bad)))
	b, _ := os.ReadFile(badPath)
	b[len(b)/2] ^= 1
	if err := os.WriteFile(badPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	plain := s.abs(RelPath(sha(good)))
	if err := os.WriteFile(plain, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}

	c, _ := s.NewRekey()
	if out, err := c.Message(sha(bad)); err != nil || out != ConvertCorrupt {
		t.Fatalf("corrupt: %v %v", out, err)
	}
	if out, err := c.Message(sha(good)); err != nil || out != ConvertDone {
		t.Fatalf("straggler: %v %v", out, err)
	}
	if err := c.Commit(); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(badPath); !bytes.Equal(after, b) {
		t.Error("the corrupt file changed")
	}
	if exists(plain) || headerKeyOf(t, s.abs(EncPath(sha(good)))) != newID {
		t.Errorf("straggler: plaintext left %v", exists(plain))
	}
	if got := c.NamedKeys(); !slices.Equal(got, sortedIDs(oldID, newID)) {
		t.Errorf("named keys %v", got)
	}
}

func sortedIDs(ids ...uint32) []uint32 { slices.Sort(ids); return ids }

// Orphans: an encrypted orphan named by its hash is re-encrypted; one
// without a hash in its name cannot be opened, stays and keeps its key.
func TestRekeyOrphans(t *testing.T) {
	a, b := "Subject: Waise\r\n\r\nA\r\n", "Subject: Waise\r\n\r\nB\r\n"
	s, _, root, _, oldID, newID := rekeyStore(t, a, b)
	dir := filepath.Join(root, OrphansDir, "20261104T090000Z")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	named := filepath.Join(dir, sha(a)+encExt)
	unnamed := filepath.Join(dir, "unbekannt"+encExt)
	if err := os.Rename(s.abs(EncPath(sha(a))), named); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(s.abs(EncPath(sha(b))), unnamed); err != nil {
		t.Fatal(err)
	}
	c, _ := s.NewRekey()
	rel := func(p string) string { r, _ := filepath.Rel(root, p); return filepath.ToSlash(r) }
	if out, err := c.Orphan(rel(named)); err != nil || out != ConvertDone {
		t.Fatalf("named orphan: %v %v", out, err)
	}
	if out, err := c.Orphan(rel(unnamed)); err != nil || out != ConvertCorrupt {
		t.Fatalf("unnamed orphan: %v %v", out, err)
	}
	if err := c.Commit(); err != nil {
		t.Fatal(err)
	}
	if headerKeyOf(t, named) != newID || headerKeyOf(t, unnamed) != oldID {
		t.Error("wrong keys after the rekey")
	}
	if got := c.NamedKeys(); !slices.Equal(got, sortedIDs(oldID, newID)) {
		t.Errorf("named keys %v", got)
	}
	c.ResetNamed()
	if len(c.NamedKeys()) != 0 {
		t.Error("ResetNamed kept keys")
	}
}

// The rekey's new copy is read back before it replaces the old file: a
// copy damaged after it was written is refused, the old file stays as it
// was, and no temp file is left (review round 1 of #519, B; the rekey
// variant of TestConversionReadsBackTempCopy). Counter-proof: without the
// readBack call in reencrypt, the damaged copy replaces the original.
func TestRekeyReadsBackTempCopy(t *testing.T) {
	content := "Subject: Rueckleseprobe\r\n\r\nInhalt\r\n"
	s, rec, root, _, oldID, _ := rekeyStore(t, content)
	h := sha(content)
	enc := s.abs(EncPath(h))
	before, _ := os.ReadFile(enc)
	rec.onBefore("close:TEMP", func(string) {
		temps, _ := filepath.Glob(filepath.Join(root, "tmp", "rekey-*"))
		if len(temps) != 1 {
			t.Errorf("temp copies %v", temps)
		}
		for _, p := range temps {
			data, err := os.ReadFile(p)
			if err != nil || len(data) <= blobcrypt.HeaderSize+2 {
				t.Errorf("temp copy %s: %d bytes, %v", p, len(data), err)
				continue
			}
			data[blobcrypt.HeaderSize+2] ^= 1
			if err := os.WriteFile(p, data, 0o600); err != nil {
				t.Error(err)
			}
		}
	})
	c, err := s.NewRekey()
	if err != nil {
		t.Fatal(err)
	}
	o, err := c.Message(h)
	if err == nil {
		t.Fatalf("damaged temp copy accepted: outcome %v", o)
	}
	if after, _ := os.ReadFile(enc); !bytes.Equal(after, before) {
		t.Fatal("the old file changed")
	}
	if !slices.Contains(c.NamedKeys(), oldID) && headerKeyOf(t, enc) != oldID {
		t.Error("the old file does not name the old key")
	}
	assertNoTemp(t, root)
}
