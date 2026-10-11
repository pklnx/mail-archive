package attest

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testKey is a fixed key, so the golden signature below stays the same.
func testKey() ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
}

func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func sampleEntry() Entry {
	return Entry{
		Seq: 12, Time: time.Date(2026, 10, 11, 3, 0, 0, 0, time.UTC),
		MessagesAfter: 4711, MessagesUpto: 4790, MessagesCount: 79,
		Total: 4790, Leaves: hashOf("leaves"), Prev: hashOf("prev"),
	}
}

func TestTextGolden(t *testing.T) {
	e := sampleEntry()
	e.Sign(testKey())
	want := "mail-archive attestation v1\n" +
		"seq 12\n" +
		"time 2026-10-11T03:00:00Z\n" +
		"messages 4711 4790 79\n" +
		"deletions 0 0 0\n" +
		"total 4790\n" +
		"leaves " + hashOf("leaves") + "\n" +
		"prev " + hashOf("prev") + "\n" +
		"key " + EncodeKey(testKey().Public().(ed25519.PublicKey)) + "\n"
	if got := string(e.Text()); got != want {
		t.Fatalf("text:\n%s\nwant:\n%s", got, want)
	}
	if e.Hash != hashOf(want) {
		t.Errorf("hash %s, want sha256 of the text", e.Hash)
	}
	// Ed25519 is deterministic: a fixed key and text give a fixed signature.
	const wantSig = "iRFCpVOGs6Sd63xehw1uYOKH2vX2/PkM/a1Tf8KhdmdQpDEzEDrTGtTqo+LzzY/ExzeN8uUVOQ9fpTpHzPWLCg=="
	if e.Sig != wantSig {
		t.Errorf("signature %s, want %s", e.Sig, wantSig)
	}
}

func TestSignCheck(t *testing.T) {
	key := testKey()
	trusted := Keys{EncodeKey(key.Public().(ed25519.PublicKey)): true}
	e := sampleEntry()
	e.Time = e.Time.Add(400 * time.Millisecond)
	e.Sign(key)
	if e.Time.Nanosecond() != 0 {
		t.Errorf("time not truncated: %v", e.Time)
	}
	if err := e.Check(trusted); err != nil {
		t.Fatal(err)
	}

	changes := map[string]func(*Entry){
		"seq":       func(e *Entry) { e.Seq++ },
		"time":      func(e *Entry) { e.Time = e.Time.Add(time.Second) },
		"count":     func(e *Entry) { e.MessagesCount-- },
		"upto":      func(e *Entry) { e.MessagesUpto++ },
		"deletions": func(e *Entry) { e.DeletionsUpto, e.DeletionsCount = 1, 1 },
		"total":     func(e *Entry) { e.Total++ },
		"leaves":    func(e *Entry) { e.Leaves = hashOf("other") },
		"prev":      func(e *Entry) { e.Prev = ZeroHash },
	}
	for name, change := range changes {
		c := e
		change(&c)
		if err := c.Check(trusted); err == nil || !strings.Contains(err.Error(), "hash") {
			t.Errorf("%s changed: %v, want a hash error", name, err)
		}
		// Even with the hash fixed, the signature no longer matches.
		sum := sha256.Sum256(c.Text())
		c.Hash = hex.EncodeToString(sum[:])
		if err := c.Check(trusted); err == nil || !strings.Contains(err.Error(), "signature") {
			t.Errorf("%s changed, hash fixed: %v, want a signature error", name, err)
		}
	}

	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{8}, ed25519.SeedSize))
	o := sampleEntry()
	o.Sign(other)
	if err := o.Check(trusted); !errors.Is(err, ErrUntrustedKey) {
		t.Errorf("other key: %v", err)
	}
	// A signature swapped in from another entry is refused.
	s := e
	s.Sig = o.Sig
	if err := s.Check(trusted); err == nil {
		t.Error("swapped signature accepted")
	}
}

func chainOf(t *testing.T, key ed25519.PrivateKey, n int) []Entry {
	t.Helper()
	var out []Entry
	var prev *Entry
	for i := range n {
		e := Next(prev)
		e.MessagesUpto = e.MessagesAfter + int64(i) + 1
		e.MessagesCount = int64(i) + 1
		e.Total = e.MessagesUpto
		e.Time = time.Date(2026, 10, 1+i, 3, 0, 0, 0, time.UTC)
		e.Leaves = hashOf(string(rune('a' + i)))
		e.Sign(key)
		out = append(out, e)
		prev = &out[len(out)-1]
	}
	return out
}

func TestCheckChain(t *testing.T) {
	key := testKey()
	trusted := Keys{EncodeKey(key.Public().(ed25519.PublicKey)): true}
	chain := chainOf(t, key, 3)
	if err := CheckChain(chain, trusted); err != nil {
		t.Fatal(err)
	}
	if err := CheckChain(nil, trusted); err != nil {
		t.Fatal(err)
	}
	if err := CheckChain([]Entry{chain[0], chain[2]}, trusted); err == nil {
		t.Error("gap accepted")
	}
	if err := CheckChain(chain[1:], trusted); err == nil {
		t.Error("chain without #1 accepted")
	}
	// A re-signed entry with a range that does not continue is refused.
	bad := append([]Entry(nil), chain...)
	bad[2].MessagesAfter++
	bad[2].Sign(key)
	if err := CheckChain(bad, trusted); err == nil || !strings.Contains(err.Error(), "range") {
		t.Errorf("range: %v", err)
	}
	// A re-signed entry breaks the link to the next one.
	bad = append([]Entry(nil), chain...)
	bad[1].Total++
	bad[1].Sign(key)
	if err := CheckChain(bad, trusted); err == nil || !strings.Contains(err.Error(), "prev") {
		t.Errorf("prev: %v", err)
	}
}

func TestExamineChain(t *testing.T) {
	key, other := testKey(), ed25519.NewKeyFromSeed(bytes.Repeat([]byte{8}, ed25519.SeedSize))
	pub := EncodeKey(key.Public().(ed25519.PublicKey))
	trusted := Keys{pub: true}
	chain := chainOf(t, key, 4)
	if r := ExamineChain(chain, trusted); len(r.Problems) != 0 || len(r.KeyChanges) != 0 {
		t.Fatalf("clean: %+v", r)
	}

	// Every problem is reported, not only the first.
	bad := append([]Entry(nil), chain...)
	bad[0].Sig, bad[1].Sig = bad[1].Sig, bad[0].Sig // swapped signatures
	bad = append(bad[:2], bad[3])                   // a gap
	r := ExamineChain(bad, trusted)
	want := []string{"#1: bad signature", "#2: bad signature", "#4: expected #3"}
	if len(r.Problems) != len(want) {
		t.Fatalf("problems: %q", r.Problems)
	}
	for i, w := range want {
		if !strings.Contains(r.Problems[i], w) {
			t.Errorf("problem %d: %q, want %q", i, r.Problems[i], w)
		}
	}

	// A rotation: trusted while both keys are, reported as a key change.
	rotated := append([]Entry(nil), chain...)
	for i := 2; i < len(rotated); i++ {
		rotated[i].Prev = rotated[i-1].Hash
		rotated[i].Sign(other)
	}
	trusted[EncodeKey(other.Public().(ed25519.PublicKey))] = true
	if r := ExamineChain(rotated, trusted); len(r.Problems) != 0 || len(r.KeyChanges) != 1 || r.KeyChanges[0] != 3 {
		t.Fatalf("rotation: %+v", r)
	}
	// Without the retired key, the old entries are untrusted.
	r = ExamineChain(rotated, Keys{EncodeKey(other.Public().(ed25519.PublicKey)): true})
	if len(r.Problems) != 2 || !strings.Contains(r.Problems[0], "#1: signed with a key that is not trusted") {
		t.Fatalf("untrusted: %q", r.Problems)
	}
}

func TestParseLeaves(t *testing.T) {
	a, b := hashOf("a"), hashOf("b")
	data := "mail-archive leaves v1\n+ 3 " + a + "\n+ 7 " + b + "\n- 1 2 " + a + " retention\n"
	l, err := ParseLeaves([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Messages) != 2 || l.Messages[1] != (Leaf{Seq: 7, SHA256: b}) ||
		len(l.Deletions) != 1 || l.Deletions[0] != (Deletion{ID: 1, MessageSeq: 2, SHA256: a, Reason: "retention"}) {
		t.Fatalf("parsed: %+v", l)
	}
	if l, err := ParseLeaves([]byte("mail-archive leaves v1\n")); err != nil || len(l.Messages)+len(l.Deletions) != 0 {
		t.Fatalf("empty: %+v %v", l, err)
	}
	for name, bad := range map[string]string{
		"no header":    "+ 3 " + a + "\n",
		"out of order": "mail-archive leaves v1\n+ 7 " + a + "\n+ 3 " + b + "\n",
		"no newline":   "mail-archive leaves v1\n+ 3 " + a,
		"extra space":  "mail-archive leaves v1\n+  3 " + a + "\n",
		"bad hash":     "mail-archive leaves v1\n+ 3 " + strings.ToUpper(a) + "\n",
		"unknown line": "mail-archive leaves v1\n* 3 " + a + "\n",
		"bad number":   "mail-archive leaves v1\n+ x " + a + "\n",
	} {
		if _, err := ParseLeaves([]byte(bad)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestLeavesGolden(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewLeavesWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	a, b, c := hashOf("a"), hashOf("b"), hashOf("c")
	for _, err := range []error{
		w.Message(3, a), w.Message(7, b),
		w.Deletion(Deletion{ID: 1, MessageSeq: 2, SHA256: c, Reason: "retention"}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	sum, messages, deletions, err := w.Finish()
	if err != nil {
		t.Fatal(err)
	}
	want := "mail-archive leaves v1\n+ 3 " + a + "\n+ 7 " + b + "\n- 1 2 " + c + " retention\n"
	if buf.String() != want {
		t.Fatalf("leaves:\n%s\nwant:\n%s", buf.String(), want)
	}
	if sum != hashOf(want) || messages != 2 || deletions != 1 {
		t.Errorf("sum %s messages %d deletions %d", sum, messages, deletions)
	}

	w, _ = NewLeavesWriter(&bytes.Buffer{})
	if err := w.Message(3, a); err != nil {
		t.Fatal(err)
	}
	if w.Message(3, b) == nil || w.Message(4, "nothex") == nil {
		t.Error("bad message line accepted")
	}
	if err := w.Deletion(Deletion{ID: 1, MessageSeq: 1, SHA256: a, Reason: "retention"}); err != nil {
		t.Fatal(err)
	}
	if w.Message(9, b) == nil {
		t.Error("message after deletion accepted")
	}
	if w.Deletion(Deletion{ID: 2, MessageSeq: 1, SHA256: a, Reason: "two words"}) == nil {
		t.Error("reason with a space accepted")
	}
}

func TestEmptyLeaves(t *testing.T) {
	var buf bytes.Buffer
	w, _ := NewLeavesWriter(&buf)
	sum, _, _, err := w.Finish()
	if err != nil || sum != hashOf("mail-archive leaves v1\n") {
		t.Errorf("empty leaves: %s %v", sum, err)
	}
}

func TestLineRoundTrip(t *testing.T) {
	e := sampleEntry()
	e.Sign(testKey())
	b, err := MarshalLine(&e)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(b, []byte("}\n")) || bytes.Count(b, []byte("\n")) != 1 {
		t.Fatalf("line %q", b)
	}
	got, err := ParseLine(b)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Same(&e) {
		t.Errorf("round trip: %+v\nwant %+v", got, e)
	}

	withExtra := bytes.Replace(b, []byte(`{"seq"`), []byte(`{"extra":1,"seq"`), 1)
	if _, err := ParseLine(withExtra); err == nil {
		t.Error("unknown field accepted")
	}
	if _, err := ParseLine(append(bytes.TrimSuffix(b, []byte("\n")), []byte(`{}`)...)); err == nil {
		t.Error("trailing data accepted")
	}
	local := bytes.Replace(b, []byte(`"2026-10-11T03:00:00Z"`), []byte(`"2026-10-11T05:00:00+02:00"`), 1)
	if _, err := ParseLine(local); err == nil {
		t.Error("time not in UTC accepted")
	}
}

func TestFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ChainFile)
	if entries, err := ReadFile(path); err != nil || entries != nil {
		t.Fatalf("missing file: %v %v", entries, err)
	}
	chain := chainOf(t, testKey(), 2)
	for i := range chain {
		if err := AppendFile(path, &chain[i]); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ReadFile(path)
	if err != nil || len(got) != 2 || !got[1].Same(&chain[1]) {
		t.Fatalf("read: %v %v", got, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}

	data, _ := os.ReadFile(path)
	if err := os.WriteFile(path, data[:len(data)-5], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(path); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("torn line: %v", err)
	}

	p, err := CreateLeaves(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Message(1, hashOf("x")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := p.Finish(); err != nil {
		t.Fatal(err)
	}
	if err := p.Commit(1); err != nil {
		t.Fatal(err)
	}
	p.Abort() // no-op after Commit
	if b, err := os.ReadFile(LeavesPath(dir, 1)); err != nil || !strings.HasPrefix(string(b), "mail-archive leaves v1\n+ 1 ") {
		t.Fatalf("leaves file: %q %v", b, err)
	}

	stale, _ := CreateLeaves(dir)
	_ = stale.f.Close()
	if err := RemoveStaleLeaves(dir); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, LeavesDir))
	if len(entries) != 1 || entries[0].Name() != "1.txt" {
		t.Errorf("after cleanup: %v", entries)
	}
}

func TestKeys(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "attest")
	keyFile := filepath.Join(dir, DefaultKeyFile)
	if _, err := LoadSigner(keyFile, dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("no key: %v", err)
	}
	pub, err := Keygen(keyFile, dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(keyFile); fi.Mode().Perm() != 0o600 {
		t.Errorf("key mode %v", fi.Mode())
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("dir mode %v", fi.Mode())
	}
	key, err := LoadSigner(keyFile, dir)
	if err != nil || !pub.Equal(key.Public()) {
		t.Fatalf("load: %v", err)
	}
	if _, err := Keygen(keyFile, dir); err == nil || !strings.Contains(err.Error(), "--rotate") {
		t.Errorf("second keygen: %v", err)
	}

	// PKCS#8 and SPKI round trips.
	der, _ := MarshalPrivateKey(key)
	if k2, err := ParsePrivateKey(der); err != nil || !k2.Equal(key) {
		t.Errorf("private round trip: %v", err)
	}

	if err := os.Chmod(keyFile, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSigner(keyFile, dir); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("mode 0644: %v", err)
	}
	_ = os.Chmod(keyFile, 0o600)

	trusted, err := LoadTrusted(dir)
	if err != nil || len(trusted) != 1 || !trusted[EncodeKey(pub)] {
		t.Fatalf("trusted: %v %v", trusted, err)
	}

	newPub, retired, err := Rotate(keyFile, dir)
	if err != nil || !retired.Equal(pub) || newPub.Equal(pub) {
		t.Fatalf("rotate: %v", err)
	}
	key2, err := LoadSigner(keyFile, dir)
	if err != nil || !newPub.Equal(key2.Public()) {
		t.Fatalf("load after rotate: %v", err)
	}
	trusted, _ = LoadTrusted(dir)
	if len(trusted) != 2 || !trusted[EncodeKey(pub)] || !trusted[EncodeKey(newPub)] {
		t.Errorf("trusted after rotate: %v", trusted)
	}

	// A key that does not match signing.pub is refused.
	other, _ := MarshalPrivateKey(testKey())
	if err := os.WriteFile(keyFile, other, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSigner(keyFile, dir); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Errorf("mismatch: %v", err)
	}

	// Rotating after the key was lost retires signing.pub.
	if err := os.Remove(keyFile); err != nil {
		t.Fatal(err)
	}
	if _, retired, err := Rotate(keyFile, dir); err != nil || !retired.Equal(newPub) {
		t.Errorf("rotate without key: %v", err)
	}
	if _, _, err := Rotate(filepath.Join(t.TempDir(), "k"), t.TempDir()); err == nil {
		t.Error("rotate without any key succeeded")
	}
}
