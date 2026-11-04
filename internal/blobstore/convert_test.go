package blobstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/blobcrypt"
)

// plainStore is a recorded store holding the given messages in plaintext,
// with the keys of ring set for a conversion in either direction.
func plainStore(t *testing.T, ring *blobcrypt.Keyring, contents ...string) (*Store, *recorder, string) {
	t.Helper()
	s, rec, root := newRecorded(t)
	for _, c := range contents {
		if _, _, err := s.Put(strings.NewReader(c)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetKeys(ring, false); err != nil {
		t.Fatal(err)
	}
	rec.take()
	return s, rec, root
}

func convert(t *testing.T, s *Store, encrypt bool, hashes ...string) []ConvertOutcome {
	t.Helper()
	c, err := s.NewConversion(encrypt)
	if err != nil {
		t.Fatal(err)
	}
	var out []ConvertOutcome
	for _, h := range hashes {
		o, err := c.Message(h)
		if err != nil {
			t.Fatalf("Message(%s): %v", h[:8], err)
		}
		out = append(out, o)
	}
	if err := c.Commit(); err != nil {
		t.Fatal(err)
	}
	return out
}

// Encrypting converts a plaintext file to the encrypted form: the new file
// is published and read back before Commit, and the plaintext is unlinked
// only by Commit, after the directory barrier that makes the new name
// durable. Decrypting is the way back.
func TestConversionRoundTrip(t *testing.T) {
	ring := testRing(t)
	content := "Subject: Umwandlung\r\n\r\n" + marker + "\r\n"
	s, rec, root := plainStore(t, ring, content)
	h := sha(content)
	plain, enc := s.abs(RelPath(h)), s.abs(EncPath(h))

	c, err := s.NewConversion(true)
	if err != nil {
		t.Fatal(err)
	}
	if o, err := c.Message(h); err != nil || o != ConvertDone {
		t.Fatalf("Message: %v, %v", o, err)
	}
	if !exists(plain) || !exists(enc) || c.Pending() != 1 {
		t.Fatalf("before Commit: plain %v enc %v pending %d", exists(plain), exists(enc), c.Pending())
	}
	rec.take()
	if err := c.Commit(); err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Dir(plain)
	want := slices.Concat(barrier(leaf), []string{"unlink:" + plain}, barrier(leaf))
	equal(t, "commit", rec.take(), want)
	if exists(plain) || !exists(enc) {
		t.Fatalf("after Commit: plain %v enc %v", exists(plain), exists(enc))
	}
	if data, _ := os.ReadFile(enc); strings.Contains(string(data), marker) {
		t.Fatal("the encrypted file holds the plaintext")
	}
	if got, m := readMessage(t, s, h); got != content || !m.Encrypted() {
		t.Fatalf("read %q encrypted %v", got, m.Encrypted())
	}
	assertNoTemp(t, root)

	if o := convert(t, s, true, h); o[0] != ConvertNone {
		t.Fatalf("again: %v", o)
	}
	if o := convert(t, s, false, h); o[0] != ConvertDone {
		t.Fatalf("decrypt: %v", o)
	}
	if !exists(plain) || exists(enc) {
		t.Fatalf("after decrypt: plain %v enc %v", exists(plain), exists(enc))
	}
	if got, _ := os.ReadFile(plain); string(got) != content {
		t.Fatalf("decrypted %q", got)
	}
	assertNoTemp(t, root)
}

// A crash at each step leaves a readable copy, and repeating the batch
// completes it: before the link the plaintext is untouched; between the
// link and the unlink both forms exist and the rerun unlinks the
// plaintext after reading the encrypted file back.
func TestConversionCrashAndRepeat(t *testing.T) {
	ring := testRing(t)
	content := "Subject: Absturz\r\n\r\nInhalt\r\n"
	s, rec, root := plainStore(t, ring, content)
	h := sha(content)
	plain, enc := s.abs(RelPath(h)), s.abs(EncPath(h))
	crash := errors.New("crash")

	c, _ := s.NewConversion(true)
	rec.failOnce("link:"+enc, crash)
	if _, err := c.Message(h); !errors.Is(err, crash) {
		t.Fatalf("link failure: %v", err)
	}
	if !exists(plain) || exists(enc) {
		t.Fatalf("after a failed link: plain %v enc %v", exists(plain), exists(enc))
	}
	assertNoTemp(t, root)

	c, _ = s.NewConversion(true)
	if o, err := c.Message(h); err != nil || o != ConvertDone {
		t.Fatal(o, err)
	}
	rec.failOnce("unlink:"+plain, crash)
	if err := c.Commit(); !errors.Is(err, crash) {
		t.Fatalf("unlink failure: %v", err)
	}
	if !exists(plain) || !exists(enc) {
		t.Fatalf("after a failed unlink: plain %v enc %v", exists(plain), exists(enc))
	}
	if o := convert(t, s, true, h); o[0] != ConvertDone {
		t.Fatalf("repeat: %v", o)
	}
	if exists(plain) || !exists(enc) {
		t.Fatalf("after the repeat: plain %v enc %v", exists(plain), exists(enc))
	}
	if got, _ := readMessage(t, s, h); got != content {
		t.Fatalf("read %q", got)
	}
}

// A plaintext file whose content does not match its name is never
// encrypted (plan, coordinator decision 5): it stays as it is.
func TestConversionLeavesCorruptPlaintext(t *testing.T) {
	ring := testRing(t)
	content := "Subject: kaputt\r\n\r\nx\r\n"
	s, _, root := plainStore(t, ring, content)
	h := sha(content)
	plain := s.abs(RelPath(h))
	if err := os.WriteFile(plain, []byte("Subject: anders\r\n\r\ny\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if o := convert(t, s, true, h); o[0] != ConvertCorrupt {
		t.Fatalf("outcome %v", o)
	}
	if !exists(plain) || exists(s.abs(EncPath(h))) {
		t.Fatal("the corrupt plaintext was converted or removed")
	}
	assertNoTemp(t, root)
}

// A damaged file of the target form next to a good source is replaced by
// an atomic rename of a fresh, read-back copy; then the source goes. With
// a damaged source too, both stay.
func TestConversionReplacesDamagedTarget(t *testing.T) {
	ring := testRing(t)
	content := "Subject: beide\r\n\r\nInhalt\r\n"
	s, rec, _ := plainStore(t, ring, content)
	h := sha(content)
	plain, enc := s.abs(RelPath(h)), s.abs(EncPath(h))
	writeForm(t, s.root, content, ring)
	data, _ := os.ReadFile(enc)
	data[len(data)-1] ^= 1
	if err := os.WriteFile(enc, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plain, []byte("damaged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if o := convert(t, s, true, h); o[0] != ConvertCorrupt || !exists(plain) || !exists(enc) {
		t.Fatalf("both damaged: %v, plain %v enc %v", o, exists(plain), exists(enc))
	}
	if err := os.WriteFile(plain, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	rec.take()
	if o := convert(t, s, true, h); o[0] != ConvertDone {
		t.Fatalf("damaged target: %v", o)
	}
	if !slices.Contains(rec.take(), "rename:"+enc) {
		t.Error("the damaged target was not replaced by a rename")
	}
	if exists(plain) {
		t.Error("the plaintext stayed")
	}
	if got, _ := readMessage(t, s, h); got != content {
		t.Fatalf("read %q", got)
	}
}

// Decrypting refuses an encrypted file that fails authentication or names
// an unknown data key: it is reported corrupt and stays.
func TestConversionDecryptCorrupt(t *testing.T) {
	ring, other := testRing(t), testRing(t)
	s, _, root := newRecorded(t)
	good, bad, foreign := "Subject: gut\r\n\r\n1\r\n", "Subject: schlecht\r\n\r\n2\r\n", "Subject: fremd\r\n\r\n3\r\n"
	writeForm(t, root, good, ring)
	badPath := writeForm(t, root, bad, ring)
	writeForm(t, root, foreign, other)
	data, _ := os.ReadFile(badPath)
	data[blobcrypt.HeaderSize+3] ^= 1
	if err := os.WriteFile(badPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.SetKeys(ring, false); err != nil {
		t.Fatal(err)
	}
	got := convert(t, s, false, sha(good), sha(bad), sha(foreign))
	if want := []ConvertOutcome{ConvertDone, ConvertCorrupt, ConvertCorrupt}; !slices.Equal(got, want) {
		t.Fatalf("outcomes %v, want %v", got, want)
	}
	for _, c := range []string{bad, foreign} {
		if !exists(s.abs(EncPath(sha(c)))) || exists(s.abs(RelPath(sha(c)))) {
			t.Errorf("%q: the encrypted file did not stay alone", c)
		}
	}
	assertNoTemp(t, root)
}

// Files below orphans/ are converted too: a moved message file by its
// name, a leftover temp file by the hash of its content, so that decrypt
// opens each by its name.
func TestConversionOrphans(t *testing.T) {
	ring := testRing(t)
	s, _, root := plainStore(t, ring)
	msg, temp := "Subject: verwaist\r\n\r\n"+marker+"\r\n", "partial "+marker
	dir := "orphans/20261104T090000Z"
	msgRel := dir + "/" + RelPath(sha(msg))
	tempRel := dir + "/tmp/put-123"
	for rel, c := range map[string]string{msgRel: msg, tempRel: temp} {
		if err := os.MkdirAll(filepath.Dir(s.abs(rel)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(s.abs(rel), []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var rels []string
	if err := s.OrphansAfter("", func(rel string, _ int64) error { rels = append(rels, rel); return nil }); err != nil {
		t.Fatal(err)
	}
	c, _ := s.NewConversion(true)
	for _, rel := range rels {
		if o, err := c.Orphan(rel); err != nil || o != ConvertDone {
			t.Fatalf("%s: %v %v", rel, o, err)
		}
	}
	if err := c.Commit(); err != nil {
		t.Fatal(err)
	}
	encMsg := dir + "/" + EncPath(sha(msg))
	encTemp := dir + "/tmp/" + sha(temp) + ".enc"
	for _, rel := range []string{msgRel, tempRel} {
		if exists(s.abs(rel)) {
			t.Errorf("%s stayed", rel)
		}
	}
	for rel, want := range map[string]string{encMsg: msg, encTemp: temp} {
		data, err := os.ReadFile(s.abs(rel))
		if err != nil || strings.Contains(string(data), marker) {
			t.Fatalf("%s: %v, plaintext %v", rel, err, strings.Contains(string(data), marker))
		}
		m, err := s.openFile(s.abs(rel), strings.TrimSuffix(filepath.Base(rel), ".enc"), true)
		if err != nil {
			t.Fatal(err)
		}
		got := make([]byte, m.Size())
		if _, err := m.ReadAt(got, 0); err != nil || string(got) != want {
			t.Fatalf("%s: %q %v", rel, got, err)
		}
		_ = m.Close()
	}

	c, _ = s.NewConversion(false)
	for _, rel := range []string{encMsg, encTemp} {
		if o, err := c.Orphan(rel); err != nil || o != ConvertDone {
			t.Fatalf("decrypt %s: %v %v", rel, o, err)
		}
	}
	if err := c.Commit(); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{msgRel: msg, dir + "/tmp/" + sha(temp) + ".eml": temp} {
		if got, err := os.ReadFile(s.abs(rel)); err != nil || string(got) != want {
			t.Errorf("%s: %q %v", rel, got, err)
		}
	}
	assertNoTemp(t, root)
}

// MessagesAfter lists each hash once, in order, after the cursor, and
// skips what is not a blob.
func TestMessagesAfter(t *testing.T) {
	ring := testRing(t)
	var contents []string
	for i := range 40 {
		contents = append(contents, fmt.Sprintf("Subject: %d\r\n\r\n%d\r\n", i, i))
	}
	s, _, root := plainStore(t, ring, contents...)
	writeForm(t, root, contents[0], ring) // both forms of one hash
	if err := os.WriteFile(filepath.Join(root, "messages", "stray.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, c := range contents {
		want = append(want, sha(c))
	}
	slices.Sort(want)
	list := func(after string) []string {
		var got []string
		if err := s.MessagesAfter(after, func(h string, size int64) error {
			if size <= 0 {
				t.Errorf("%s: size %d", h, size)
			}
			got = append(got, h)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := list(""); !slices.Equal(got, want) {
		t.Fatalf("all: %d hashes, want %d", len(got), len(want))
	}
	if got := list(want[16]); !slices.Equal(got, want[17:]) {
		t.Fatalf("after the 17th: %v", got)
	}
	if got := list("orphans/"); len(got) != 0 {
		t.Fatalf("after messages/: %v", got)
	}
}

// OrphansAfter resumes in WalkDir order, also where that differs from the
// byte order of the paths ("a/b" comes before "a-x").
func TestOrphansAfter(t *testing.T) {
	s, _, _ := newRecorded(t)
	files := []string{"orphans/a/b", "orphans/a/c", "orphans/a-x/d", "orphans/b"}
	for _, rel := range files {
		if err := os.MkdirAll(filepath.Dir(s.abs(rel)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(s.abs(rel), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	list := func(after string) []string {
		var got []string
		if err := s.OrphansAfter(after, func(rel string, _ int64) error { got = append(got, rel); return nil }); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := list(""); !slices.Equal(got, files) {
		t.Fatalf("all: %v", got)
	}
	for i, f := range files {
		if got := list(f); !slices.Equal(got, files[i+1:]) {
			t.Errorf("after %s: %v", f, got)
		}
	}
	empty, _, _ := newRecorded(t)
	if err := empty.OrphansAfter("", func(string, int64) error { t.Fatal("called"); return nil }); err != nil {
		t.Fatalf("no orphans/: %v", err)
	}
}

// MoveTempAside moves every file in tmp/, whatever its age, below the
// given directory and keeps nothing in tmp/.
func TestMoveTempAside(t *testing.T) {
	s, rec, root := newRecorded(t)
	old, young := filepath.Join(root, "tmp", "put-old"), filepath.Join(root, "tmp", "put-young")
	for _, p := range []string{old, young} {
		if err := os.WriteFile(p, []byte(marker), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	if err := os.Chtimes(old, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	rec.take()
	moved, err := s.MoveTempAside("orphans/T")
	if err != nil || len(moved) != 2 {
		t.Fatalf("moved %v, %v", moved, err)
	}
	assertNoTemp(t, root)
	for _, rel := range []string{"orphans/T/tmp/put-old", "orphans/T/tmp/put-young"} {
		if !slices.Contains(moved, rel) || !exists(s.abs(rel)) {
			t.Errorf("%s not moved: %v", rel, moved)
		}
	}
	// Each move synchronizes the new directory chain up to the root and
	// the old directory.
	ev := rec.take()
	for _, d := range []string{filepath.Join(root, "orphans", "T", "tmp"), filepath.Join(root, "orphans", "T"),
		filepath.Join(root, "orphans"), root, filepath.Join(root, "tmp")} {
		if !slices.Contains(ev, "sync:"+d) {
			t.Errorf("no barrier on %s: %v", d, ev)
		}
	}
}

// MoveAside keeps the relative path below the target directory.
func TestMoveAside(t *testing.T) {
	ring := testRing(t)
	s, _, root := newRecorded(t)
	content := "Subject: beiseite\r\n\r\nx\r\n"
	enc := writeForm(t, root, content, ring)
	rel := EncPath(sha(content))
	if err := s.MoveAside(rel, "orphans/T"); err != nil {
		t.Fatal(err)
	}
	if exists(enc) || !exists(filepath.Join(root, "orphans", "T", filepath.FromSlash(rel))) {
		t.Fatal("not moved")
	}
	if err := os.WriteFile(enc, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.MoveAside(rel, "orphans/T"); err == nil || errors.Is(err, syscall.ENOENT) {
		t.Fatalf("an existing target: %v", err)
	}
}

// The copy written to tmp/ is read back before it is published: damaged
// after it was written (here as it is closed), it is refused, nothing is
// linked, and the source stays the only copy (review round 1 of #513, B1).
// Both directions.
func TestConversionReadsBackTempCopy(t *testing.T) {
	for _, encrypt := range []bool{true, false} {
		t.Run(fmt.Sprintf("encrypt=%v", encrypt), func(t *testing.T) {
			ring := testRing(t)
			content := "Subject: Rueckleseprobe\r\n\r\nInhalt\r\n"
			s, rec, root := newRecorded(t)
			if encrypt {
				if _, _, err := s.Put(strings.NewReader(content)); err != nil {
					t.Fatal(err)
				}
			} else {
				writeForm(t, root, content, ring)
			}
			if err := s.SetKeys(ring, false); err != nil {
				t.Fatal(err)
			}
			h := sha(content)
			src, dst := s.abs(RelPath(h)), s.abs(EncPath(h))
			if !encrypt {
				src, dst = dst, src
			}
			rec.onBefore("close:TEMP", func(string) {
				temps, _ := filepath.Glob(filepath.Join(root, "tmp", "convert-*"))
				for _, p := range temps {
					data, err := os.ReadFile(p)
					if err != nil || len(data) <= blobcrypt.HeaderSize+2 && encrypt {
						t.Errorf("temp copy %s: %d bytes, %v", p, len(data), err)
						continue
					}
					i := min(blobcrypt.HeaderSize+2, len(data)-1)
					data[i] ^= 1
					if err := os.WriteFile(p, data, 0o600); err != nil {
						t.Error(err)
					}
				}
			})
			c, err := s.NewConversion(encrypt)
			if err != nil {
				t.Fatal(err)
			}
			if o, err := c.Message(h); err == nil {
				t.Fatalf("Message accepted a damaged temp copy: %v", o)
			}
			if exists(dst) || !exists(src) || c.Pending() != 0 {
				t.Fatalf("target %v source %v pending %d", exists(dst), exists(src), c.Pending())
			}
			assertNoTemp(t, root)
		})
	}
}
