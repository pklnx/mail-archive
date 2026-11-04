package attest

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

func sampleStatement() Statement {
	return Statement{
		ID:        "0123456789abcdef0123456789abcdef",
		Created:   time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC),
		Software:  "v1.5.0",
		Selection: `{"group":"Meier GmbH","users":["anna","ben"]}`,
		TimeZone:  "Europe/Berlin",
		Group:     &StatementGroup{ID: 7, Name: "Meier GmbH"},
		Owners:    []string{"anna", "ben"},
		Messages:  3, Locations: 4, Missing: 0, Bytes: 1234,
		Complete:       true,
		SHA256SUMS:     hashOf("sums"),
		AttestationSeq: 12, AttestationHash: hashOf("att"),
	}
}

func TestStatementGolden(t *testing.T) {
	s := sampleStatement()
	if err := s.Sign(testKey()); err != nil {
		t.Fatal(err)
	}
	want := "mail-archive export v1\n" +
		"id 0123456789abcdef0123456789abcdef\n" +
		"created 2026-11-04T10:00:00Z\n" +
		"software v1.5.0\n" +
		`selection {"group":"Meier GmbH","users":["anna","ben"]}` + "\n" +
		"time-zone Europe/Berlin\n" +
		"group-id 7\n" +
		`group "Meier GmbH"` + "\n" +
		`owners ["anna","ben"]` + "\n" +
		"messages 3\nlocations 4\nmissing 0\nbytes 1234\n" +
		"complete yes\nresumed no\nchain one\nleaves no\n" +
		"sha256sums " + hashOf("sums") + "\n" +
		"attestation 12 " + hashOf("att") + "\n" +
		"key " + EncodeKey(testKey().Public().(ed25519.PublicKey)) + "\n"
	if got := string(s.Text()); got != want {
		t.Fatalf("text:\n%s\nwant:\n%s", got, want)
	}
	file := s.File()
	if !bytes.HasPrefix(file, []byte(want)) || !bytes.HasSuffix(file, []byte("sig "+s.Sig+"\n")) {
		t.Fatalf("file:\n%s", file)
	}
	p, err := ParseStatement(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(p.File(), file) {
		t.Fatalf("round trip:\n%s", p.File())
	}
	if err := p.Verify(Keys{s.Key: true}); err != nil {
		t.Fatal(err)
	}
}

// Every line of the signed text is covered: changing any value either
// makes the file unparseable or breaks the signature.
func TestStatementTamper(t *testing.T) {
	s := sampleStatement()
	if err := s.Sign(testKey()); err != nil {
		t.Fatal(err)
	}
	trusted := Keys{s.Key: true}
	file := string(s.File())
	lines := strings.Split(strings.TrimSuffix(file, "\n"), "\n")
	changes := map[string]string{
		"id":          "id 0123456789abcdef0123456789abcdee",
		"created":     "created 2026-11-04T10:00:01Z",
		"software":    "software v1.5.1",
		"selection":   `selection {"users":["anna"]}`,
		"time-zone":   "time-zone UTC",
		"group-id":    "group-id 8",
		"group":       `group "Schulz KG"`,
		"owners":      `owners ["anna"]`,
		"messages":    "messages 4",
		"locations":   "locations 5",
		"bytes":       "bytes 1235",
		"resumed":     "resumed yes",
		"chain":       "chain full",
		"leaves":      "leaves yes",
		"sha256sums":  "sha256sums " + hashOf("other"),
		"attestation": "attestation 11 " + hashOf("att"),
	}
	for name, repl := range changes {
		t.Run(name, func(t *testing.T) {
			found := false
			out := make([]string, len(lines))
			for i, l := range lines {
				out[i] = l
				if strings.HasPrefix(l, name+" ") {
					out[i], found = repl, true
				}
			}
			if !found {
				t.Fatalf("no %q line", name)
			}
			p, err := ParseStatement([]byte(strings.Join(out, "\n") + "\n"))
			if err == nil {
				err = p.Verify(trusted)
			}
			if err == nil {
				t.Fatalf("changed %s passed", name)
			}
		})
	}
	// A valid signature by a key that is not trusted.
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, ed25519.SeedSize))
	s2 := sampleStatement()
	_ = s2.Sign(other)
	p, err := ParseStatement(s2.File())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Verify(trusted); !errors.Is(err, ErrUntrustedKey) {
		t.Fatalf("untrusted key: %v", err)
	}
	if err := p.Verify(nil); err != nil {
		t.Fatalf("with nil trusted, the own key checks: %v", err)
	}
}

// Neither signature passes for the other kind of text: the statement's
// signed text starts with its own header, and an attestation's text with
// another.
func TestStatementDomainSeparation(t *testing.T) {
	key := testKey()
	trusted := Keys{EncodeKey(key.Public().(ed25519.PublicKey)): true}
	s := sampleStatement()
	if err := s.Sign(key); err != nil {
		t.Fatal(err)
	}
	e := sampleEntry()
	e.Sign(key)

	if !bytes.HasPrefix(s.Text(), []byte(StatementHeader)) || bytes.HasPrefix(s.Text(), []byte("mail-archive attestation v1\n")) {
		t.Fatalf("statement text does not start with its own header:\n%s", s.Text())
	}
	if !ed25519.Verify(key.Public().(ed25519.PublicKey), s.Text(), mustB64(t, s.Sig)) {
		t.Fatal("the signature is not over the whole text, header included")
	}
	// The statement's signature on the attestation.
	e2 := e
	e2.Sig = s.Sig
	if err := e2.Check(trusted); err == nil {
		t.Fatal("a statement signature verified as an attestation")
	}
	// The attestation's text and signature as a statement.
	if _, err := ParseStatement(append(e.Text(), "sig "+e.Sig+"\n"...)); err == nil {
		t.Fatal("an attestation parsed as a statement")
	}
	// The attestation's signature on the statement.
	s2 := s
	s2.Sig = e.Sig
	if err := s2.Verify(trusted); err == nil {
		t.Fatal("an attestation signature verified as a statement")
	}
}

// T-G9 (part): the group lines are inside the signed text, and any name
// stays on one line.
func TestStatementGroupLines(t *testing.T) {
	key := testKey()
	for _, name := range []string{`Meier "GmbH"`, "Zeile 1\nZeile 2", "<&>", "none", "Müller & Söhne"} {
		s := sampleStatement()
		s.Group.Name = name
		if err := s.Sign(key); err != nil {
			t.Fatal(err)
		}
		text := string(s.Text())
		if strings.Count(text, "\n") != strings.Count(string(sampleStatementText(t)), "\n") {
			t.Fatalf("%q: the name takes more than one line:\n%s", name, text)
		}
		p, err := ParseStatement(s.File())
		if err != nil {
			t.Fatalf("%q: %v", name, err)
		}
		if p.Group == nil || p.Group.Name != name || p.Group.ID != 7 {
			t.Fatalf("%q: parsed %+v", name, p.Group)
		}
		if err := p.Verify(nil); err != nil {
			t.Fatal(err)
		}
	}
	// Changing the group's name after signing breaks the signature.
	s := sampleStatement()
	_ = s.Sign(key)
	tampered := strings.Replace(string(s.File()), `group "Meier GmbH"`, `group "Schulz KG"`, 1)
	p, err := ParseStatement([]byte(tampered))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Verify(nil); err == nil {
		t.Fatal("a changed group name kept a valid signature")
	}
	// No group.
	s = sampleStatement()
	s.Group, s.Owners = nil, nil
	_ = s.Sign(key)
	if !strings.Contains(string(s.Text()), "\ngroup none\nowners []\n") || strings.Contains(string(s.Text()), "group-id") {
		t.Fatalf("no group:\n%s", s.Text())
	}
	p, err = ParseStatement(s.File())
	if err != nil || p.Group != nil {
		t.Fatalf("no group: %v %+v", err, p)
	}
}

func sampleStatementText(t *testing.T) []byte {
	t.Helper()
	s := sampleStatement()
	if err := s.Sign(testKey()); err != nil {
		t.Fatal(err)
	}
	return s.Text()
}

// Only the canonical form parses: another spelling of a value would not be
// what the signature binds.
func TestStatementCanonical(t *testing.T) {
	s := sampleStatement()
	_ = s.Sign(testKey())
	file := string(s.File())
	for name, f := range map[string]string{
		"leading zero":    strings.Replace(file, "messages 3\n", "messages 03\n", 1),
		"plus sign":       strings.Replace(file, "messages 3\n", "messages +3\n", 1),
		"json escape":     strings.Replace(file, `group "Meier GmbH"`, `group "Meier\u0020GmbH"`, 1),
		"owners spacing":  strings.Replace(file, `owners ["anna","ben"]`, `owners ["anna", "ben"]`, 1),
		"crlf":            strings.ReplaceAll(file, "\n", "\r\n"),
		"trailing data":   file + "x\n",
		"missing sig":     strings.TrimSuffix(file, "sig "+s.Sig+"\n"),
		"reordered":       strings.Replace(file, "complete yes\nresumed no\n", "resumed no\ncomplete yes\n", 1),
		"unsorted owners": strings.Replace(file, `owners ["anna","ben"]`, `owners ["ben","anna"]`, 1),
		"complete lie":    strings.Replace(file, "missing 0\n", "missing 1\n", 1),
		"key without sig": strings.Replace(file, "sig "+s.Sig+"\n", "sig none\n", 1),
		"bad header":      strings.Replace(file, "mail-archive export v1\n", "mail-archive export v2\n", 1),
		"utc offset":      strings.Replace(file, "created 2026-11-04T10:00:00Z", "created 2026-11-04T11:00:00+01:00", 1),
	} {
		if _, err := ParseStatement([]byte(f)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
	// Unsigned statements parse and say so.
	u := sampleStatement()
	if err := u.Sign(nil); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(u.File()), "key none\nsig none\n") {
		t.Fatalf("unsigned:\n%s", u.File())
	}
	p, err := ParseStatement(u.File())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Verify(nil); !errors.Is(err, ErrUnsigned) {
		t.Fatalf("unsigned verify: %v", err)
	}
}

func TestStatementSignRefusesBadFields(t *testing.T) {
	for name, f := range map[string]func(*Statement){
		"id":         func(s *Statement) { s.ID = "XYZ" },
		"selection":  func(s *Statement) { s.Selection = "{ }" },
		"software":   func(s *Statement) { s.Software = "v1 beta" },
		"owners":     func(s *Statement) { s.Owners = []string{"ben", "anna"} },
		"duplicates": func(s *Statement) { s.Owners = []string{"anna", "anna"} },
		"group":      func(s *Statement) { s.Group.ID = 0 },
		"complete":   func(s *Statement) { s.Missing = 2 },
		"att":        func(s *Statement) { s.AttestationHash = "" },
	} {
		s := sampleStatement()
		f(&s)
		if err := s.Sign(testKey()); err == nil {
			t.Errorf("%s: signed", name)
		}
	}
}

func FuzzParseStatement(f *testing.F) {
	s := sampleStatement()
	_ = s.Sign(testKey())
	f.Add(s.File())
	u := sampleStatement()
	u.Group, u.Owners = nil, nil
	_ = u.Sign(nil)
	f.Add(u.File())
	f.Add([]byte(strings.Replace(string(s.File()), "messages 3\n", "messages 03\n", 1)))
	f.Add([]byte(strings.Replace(string(s.File()), `group "Meier GmbH"`, `group "Meier\u0020GmbH"`, 1)))
	f.Add([]byte(StatementHeader))
	f.Add([]byte(StatementHeader + "id x\n"))
	e := sampleEntry()
	e.Sign(testKey())
	f.Add(append(e.Text(), "sig "+e.Sig+"\n"...))
	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := ParseStatement(data)
		if err != nil {
			return
		}
		// Whatever parses is canonical: written again, it is the input.
		if !bytes.Equal(p.File(), data) {
			t.Fatalf("not canonical:\n%q\n%q", p.File(), data)
		}
		_ = p.Verify(nil)
	})
}

func TestScanLeavesMatchesParse(t *testing.T) {
	var b bytes.Buffer
	w, _ := NewLeavesWriter(&b)
	_ = w.Message(3, hashOf("a"))
	_ = w.Message(5, hashOf("b"))
	_ = w.Deletion(Deletion{ID: 1, MessageSeq: 2, SHA256: hashOf("c"), Reason: "retention"})
	want, _, _, _ := w.Finish()
	var got []Leaf
	sum, err := ScanLeaves(bytes.NewReader(b.Bytes()), func(l Leaf) error { got = append(got, l); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if sum != want || len(got) != 2 || got[0].Seq != 3 || got[1].SHA256 != hashOf("b") {
		t.Fatalf("scan: %s %+v", sum, got)
	}
	for name, data := range map[string]string{
		"no header":  "+ 1 " + hashOf("a") + "\n",
		"order":      "mail-archive leaves v1\n+ 5 " + hashOf("a") + "\n+ 3 " + hashOf("b") + "\n",
		"incomplete": "mail-archive leaves v1\n+ 5 " + hashOf("a"),
		"long line":  "mail-archive leaves v1\n+ 5 " + strings.Repeat("a", 2000) + "\n",
		"unknown":    "mail-archive leaves v1\n* 5\n",
	} {
		if _, err := ScanLeaves(strings.NewReader(data), func(Leaf) error { return nil }); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if _, err := ParseLeaves([]byte(data)); err == nil && name != "long line" {
			t.Errorf("%s: ParseLeaves accepted", name)
		}
	}
}

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
