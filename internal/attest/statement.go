package attest

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// StatementHeader starts the text of an export statement. It differs from
// the attestation header ("mail-archive attestation v1"), so a signature
// over one kind of text never verifies as the other (domain separation).
const StatementHeader = "mail-archive export v1\n"

// MaxStatementSize bounds what ParseStatement reads: a statement with
// thousands of owners stays far below it.
const MaxStatementSize = 8 << 20

// ErrUnsigned is returned by Statement.Verify for a statement that ends
// with "sig none" (written without an attestation key).
var ErrUnsigned = errors.New("the statement is not signed")

// StatementGroup is the customer group a statement names: its id, which
// is never given to another group, and its name at the time of the
// export.
type StatementGroup struct {
	ID   int64
	Name string
}

// Statement is the signed export statement (STATEMENT.txt) of a GoBD
// export, format v1. Its text holds every field, one per line; the
// signature covers every line before "sig".
type Statement struct {
	ID       string    // 32 lowercase hex characters
	Created  time.Time // UTC, whole seconds
	Software string    // the version that wrote the export
	// Selection is the selection in canonical form: compact JSON.
	Selection string
	TimeZone  string // IANA name, e.g. Europe/Berlin
	// Group is the export's customer group, nil for an export of accounts
	// without an owner ("group none").
	Group *StatementGroup
	// Owners are the owners' user names in the snapshot, sorted, unique.
	Owners []string
	// Counts of messages.csv, locations.csv, missing.csv and the bytes of
	// every exported message file.
	Messages, Locations, Missing, Bytes int64
	Complete, Resumed                   bool
	// FullChain: attest/attestations.jsonl holds the whole chain, not only
	// the attestation below. Leaves: attest/leaves/ is included.
	FullChain, Leaves bool
	// SHA256SUMS is the SHA-256 of the SHA256SUMS file, hex.
	SHA256SUMS string
	// The newest attestation: its seq (0 for none) and hash.
	AttestationSeq  int64
	AttestationHash string
	Key             string // base64 of the raw public key; "" for unsigned
	Sig             string // base64 Ed25519 signature of Text; "" for unsigned
}

// Text is what Sig is over: every line of the statement before "sig". It
// is built from the fields, never taken from a stored form.
func (s *Statement) Text() []byte {
	var b bytes.Buffer
	b.WriteString(StatementHeader)
	fmt.Fprintf(&b, "id %s\n", s.ID)
	fmt.Fprintf(&b, "created %s\n", s.Created.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "software %s\n", s.Software)
	fmt.Fprintf(&b, "selection %s\n", s.Selection)
	fmt.Fprintf(&b, "time-zone %s\n", s.TimeZone)
	if s.Group == nil {
		b.WriteString("group none\n")
	} else {
		fmt.Fprintf(&b, "group-id %d\n", s.Group.ID)
		fmt.Fprintf(&b, "group %s\n", jsonString(s.Group.Name))
	}
	fmt.Fprintf(&b, "owners %s\n", jsonStrings(s.Owners))
	fmt.Fprintf(&b, "messages %d\n", s.Messages)
	fmt.Fprintf(&b, "locations %d\n", s.Locations)
	fmt.Fprintf(&b, "missing %d\n", s.Missing)
	fmt.Fprintf(&b, "bytes %d\n", s.Bytes)
	fmt.Fprintf(&b, "complete %s\n", yesNo(s.Complete))
	fmt.Fprintf(&b, "resumed %s\n", yesNo(s.Resumed))
	if s.FullChain {
		b.WriteString("chain full\n")
	} else {
		b.WriteString("chain one\n")
	}
	fmt.Fprintf(&b, "leaves %s\n", yesNo(s.Leaves))
	fmt.Fprintf(&b, "sha256sums %s\n", s.SHA256SUMS)
	if s.AttestationSeq == 0 {
		b.WriteString("attestation none\n")
	} else {
		fmt.Fprintf(&b, "attestation %d %s\n", s.AttestationSeq, s.AttestationHash)
	}
	fmt.Fprintf(&b, "key %s\n", orNone(s.Key))
	return b.Bytes()
}

// File is the whole statement file: Text and the "sig" line.
func (s *Statement) File() []byte {
	return append(s.Text(), "sig "+orNone(s.Sig)+"\n"...)
}

// Sign checks the fields and sets Key and Sig. Created is truncated to
// whole seconds in UTC first, as the text holds it. A nil key leaves the
// statement unsigned ("key none", "sig none").
func (s *Statement) Sign(key ed25519.PrivateKey) error {
	s.Created = s.Created.UTC().Truncate(time.Second)
	s.Key, s.Sig = "", ""
	if err := s.wellFormed(); err != nil {
		return err
	}
	if key == nil {
		return nil
	}
	s.Key = EncodeKey(key.Public().(ed25519.PublicKey))
	s.Sig = base64.StdEncoding.EncodeToString(ed25519.Sign(key, s.Text()))
	return nil
}

// Verify checks the signature against the statement's key and, when
// trusted is not nil, that the key is trusted. An unsigned statement
// returns ErrUnsigned.
func (s *Statement) Verify(trusted Keys) error {
	if s.Sig == "" {
		return ErrUnsigned
	}
	pub, err := DecodeKey(s.Key)
	if err != nil {
		return fmt.Errorf("statement: %w", err)
	}
	sig, err := base64.StdEncoding.DecodeString(s.Sig)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("statement: malformed signature")
	}
	if !ed25519.Verify(pub, s.Text(), sig) {
		return errors.New("statement: bad signature")
	}
	if trusted != nil && !trusted[s.Key] {
		return fmt.Errorf("statement: %w", ErrUntrustedKey)
	}
	return nil
}

func (s *Statement) wellFormed() error {
	bad := func(why string) error { return fmt.Errorf("statement: %s", why) }
	switch {
	case !isHex(s.ID, 32):
		return bad("id is not 32 lowercase hex characters")
	case !s.Created.Equal(s.Created.Truncate(time.Second)) || s.Created.Year() < 1 || s.Created.Year() > 9999:
		return bad("bad creation time")
	case !isWord(s.Software):
		return bad("bad software version")
	case !isCompactJSON(s.Selection):
		return bad("selection is not compact JSON")
	case !isWord(s.TimeZone):
		return bad("bad time zone")
	case s.Group != nil && (s.Group.ID < 1 || !utf8.ValidString(s.Group.Name) || s.Group.Name == ""):
		return bad("bad group")
	case s.Messages < 0 || s.Locations < 0 || s.Missing < 0 || s.Bytes < 0:
		return bad("negative count")
	case s.Complete != (s.Missing == 0):
		return bad("complete does not match the missing count")
	case !isHash(s.SHA256SUMS):
		return bad("malformed SHA256SUMS digest")
	case s.AttestationSeq < 0 || (s.AttestationSeq > 0) != isHash(s.AttestationHash) ||
		(s.AttestationSeq == 0 && s.AttestationHash != ""):
		return bad("bad attestation")
	}
	for i, o := range s.Owners {
		if o == "" || !utf8.ValidString(o) || (i > 0 && s.Owners[i-1] >= o) {
			return bad("owners are not valid, sorted and unique")
		}
	}
	return nil
}

// ParseStatement reads a statement file. It accepts only the exact form
// File writes, so the fields it returns are the ones the signature
// covers; Verify checks the signature.
func ParseStatement(data []byte) (*Statement, error) {
	if len(data) > MaxStatementSize {
		return nil, errors.New("statement: too large")
	}
	if !utf8.Valid(data) {
		return nil, errors.New("statement: not UTF-8")
	}
	rest, ok := bytes.CutPrefix(data, []byte(StatementHeader))
	if !ok {
		return nil, errors.New("statement: not an export statement (first line)")
	}
	p := &stmtParser{rest: string(rest)}
	s := &Statement{}
	s.ID = p.field("id")
	if t, err := time.Parse(time.RFC3339, p.field("created")); err == nil {
		s.Created = t.UTC()
	} else {
		p.fail("created")
	}
	s.Software = p.field("software")
	s.Selection = p.field("selection")
	s.TimeZone = p.field("time-zone")
	if p.peek() == "group-id" {
		g := &StatementGroup{ID: p.int("group-id")}
		if err := json.Unmarshal([]byte(p.field("group")), &g.Name); err != nil {
			p.fail("group")
		}
		s.Group = g
	} else if p.field("group") != "none" {
		p.fail("group")
	}
	if err := json.Unmarshal([]byte(p.field("owners")), &s.Owners); err != nil {
		p.fail("owners")
	}
	s.Messages = p.int("messages")
	s.Locations = p.int("locations")
	s.Missing = p.int("missing")
	s.Bytes = p.int("bytes")
	s.Complete = p.yesNo("complete")
	s.Resumed = p.yesNo("resumed")
	switch p.field("chain") {
	case "full":
		s.FullChain = true
	case "one":
	default:
		p.fail("chain")
	}
	s.Leaves = p.yesNo("leaves")
	s.SHA256SUMS = p.field("sha256sums")
	if a := p.field("attestation"); a != "none" {
		seq, hash, _ := strings.Cut(a, " ")
		n, err := strconv.ParseInt(seq, 10, 64)
		if err != nil || n < 1 {
			p.fail("attestation")
		}
		s.AttestationSeq, s.AttestationHash = n, hash
	}
	if k := p.field("key"); k != "none" {
		s.Key = k
	}
	if sig := p.field("sig"); sig != "none" {
		s.Sig = sig
	}
	if p.err == nil && p.rest != "" {
		p.err = errors.New("statement: data after the sig line")
	}
	if p.err != nil {
		return nil, p.err
	}
	if err := s.wellFormed(); err != nil {
		return nil, err
	}
	if (s.Key == "") != (s.Sig == "") {
		return nil, errors.New("statement: key and sig must both be set or both be none")
	}
	// Only the canonical form is accepted: the same fields written again
	// give the same bytes. This rules out a second spelling of a value
	// (a JSON escape, a leading zero) that the signature would not bind.
	if !bytes.Equal(s.File(), data) {
		return nil, errors.New("statement: not in canonical form")
	}
	return s, nil
}

type stmtParser struct {
	rest string
	err  error
}

func (p *stmtParser) fail(name string) {
	if p.err == nil {
		p.err = fmt.Errorf("statement: bad %q line", name)
	}
}

// peek returns the name of the next line.
func (p *stmtParser) peek() string {
	name, _, _ := strings.Cut(p.rest, " ")
	return name
}

// field consumes the line "<name> <value>\n" and returns the value.
func (p *stmtParser) field(name string) string {
	if p.err != nil {
		return ""
	}
	line, rest, ok := strings.Cut(p.rest, "\n")
	value, found := strings.CutPrefix(line, name+" ")
	if !ok || !found {
		p.fail(name)
		return ""
	}
	p.rest = rest
	return value
}

func (p *stmtParser) int(name string) int64 {
	v := p.field(name)
	if p.err != nil {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		p.fail(name)
	}
	return n
}

func (p *stmtParser) yesNo(name string) bool {
	switch p.field(name) {
	case "yes":
		return true
	case "no":
		return false
	}
	p.fail(name)
	return false
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// jsonString quotes s as JSON without HTML escapes, so that any name,
// with quotes or line breaks, stays on one line.
func jsonString(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // a string always encodes
	return strings.TrimSuffix(b.String(), "\n")
}

func jsonStrings(list []string) string {
	parts := make([]string, len(list))
	for i, s := range list {
		parts[i] = jsonString(s)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// isWord: printable, non-empty, without spaces or line breaks.
func isWord(s string) bool {
	return s != "" && utf8.ValidString(s) && !strings.ContainsFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || !unicode.IsPrint(r)
	})
}

func isCompactJSON(s string) bool {
	if !json.Valid([]byte(s)) {
		return false
	}
	var b bytes.Buffer
	if err := json.Compact(&b, []byte(s)); err != nil {
		return false
	}
	return b.String() == s && !strings.ContainsAny(s, "\n\r")
}

func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	return !slices.ContainsFunc([]byte(s), func(c byte) bool {
		return (c < '0' || c > '9') && (c < 'a' || c > 'f')
	})
}
