// Package attest holds the formats of the tamper-evidence chain: the signed
// text of an attestation, its leaves file, the attestation file and the
// key files. It has no database: the store keeps the chain, this package
// says how it is written and checked.
//
// An attestation covers the messages archived since the previous one, by
// their sequence number in the messages table, and the deletions recorded
// since then. Its leaves file lists them; the SHA-256 of that file, the
// counts, the previous attestation's hash and the public key make up the
// text that is hashed and signed with Ed25519.
package attest

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ZeroHash is the previous hash of the first attestation.
var ZeroHash = strings.Repeat("0", 64)

// ErrUntrustedKey is returned for an attestation signed with a key that is
// neither signing.pub nor one of the retired keys.
var ErrUntrustedKey = errors.New("signed with a key that is not trusted")

// Entry is one attestation.
type Entry struct {
	Seq  int64
	Time time.Time // UTC, whole seconds
	// The messages with MessagesAfter < seq <= MessagesUpto, and how many
	// there were.
	MessagesAfter, MessagesUpto, MessagesCount int64
	// The deletions with DeletionsAfter < id <= DeletionsUpto.
	DeletionsAfter, DeletionsUpto, DeletionsCount int64
	// Total is the number of messages in the same snapshot.
	Total  int64
	Leaves string // SHA-256 of the leaves file, hex
	Prev   string // Hash of the previous attestation, or ZeroHash
	Key    string // base64 of the raw public key
	Hash   string // SHA-256 of Text, hex
	Sig    string // base64 of the Ed25519 signature of Text
}

// Text is what Hash and Sig are over. It is built from the fields, never
// taken from a stored form, so a change to any field breaks both.
func (e *Entry) Text() []byte {
	var b bytes.Buffer
	b.WriteString("mail-archive attestation v1\n")
	fmt.Fprintf(&b, "seq %d\n", e.Seq)
	fmt.Fprintf(&b, "time %s\n", e.Time.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "messages %d %d %d\n", e.MessagesAfter, e.MessagesUpto, e.MessagesCount)
	fmt.Fprintf(&b, "deletions %d %d %d\n", e.DeletionsAfter, e.DeletionsUpto, e.DeletionsCount)
	fmt.Fprintf(&b, "total %d\n", e.Total)
	fmt.Fprintf(&b, "leaves %s\n", e.Leaves)
	fmt.Fprintf(&b, "prev %s\n", e.Prev)
	fmt.Fprintf(&b, "key %s\n", e.Key)
	return b.Bytes()
}

// Sign sets Key, Hash and Sig. Time is truncated to whole seconds in UTC
// first, as the text holds it.
func (e *Entry) Sign(key ed25519.PrivateKey) {
	e.Time = e.Time.UTC().Truncate(time.Second)
	e.Key = EncodeKey(key.Public().(ed25519.PublicKey))
	text := e.Text()
	sum := sha256.Sum256(text)
	e.Hash = hex.EncodeToString(sum[:])
	e.Sig = base64.StdEncoding.EncodeToString(ed25519.Sign(key, text))
}

// Same reports whether two entries have the same fields, hash and
// signature.
func (e *Entry) Same(o *Entry) bool {
	return bytes.Equal(e.Text(), o.Text()) && e.Hash == o.Hash && e.Sig == o.Sig
}

// Check verifies the entry on its own: well-formed fields, the hash and
// the signature over its text, and a key in trusted.
func (e *Entry) Check(trusted Keys) error {
	if err := e.wellFormed(); err != nil {
		return err
	}
	text := e.Text()
	sum := sha256.Sum256(text)
	if hex.EncodeToString(sum[:]) != e.Hash {
		return fmt.Errorf("attestation #%d: hash does not match its fields", e.Seq)
	}
	pub, err := DecodeKey(e.Key)
	if err != nil {
		return fmt.Errorf("attestation #%d: %w", e.Seq, err)
	}
	sig, err := base64.StdEncoding.DecodeString(e.Sig)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("attestation #%d: malformed signature", e.Seq)
	}
	if !ed25519.Verify(pub, text, sig) {
		return fmt.Errorf("attestation #%d: bad signature", e.Seq)
	}
	if !trusted[e.Key] {
		return fmt.Errorf("attestation #%d: %w", e.Seq, ErrUntrustedKey)
	}
	return nil
}

func (e *Entry) wellFormed() error {
	bad := func(why string) error { return fmt.Errorf("attestation #%d: %s", e.Seq, why) }
	switch {
	case e.Seq < 1:
		return bad("seq below 1")
	case !e.Time.Equal(e.Time.Truncate(time.Second)):
		return bad("time has fractions of a second")
	case e.MessagesAfter < 0 || e.MessagesUpto < e.MessagesAfter || e.MessagesCount < 0:
		return bad("bad messages range")
	case e.DeletionsAfter < 0 || e.DeletionsUpto < e.DeletionsAfter || e.DeletionsCount < 0:
		return bad("bad deletions range")
	case e.Total < 0:
		return bad("negative total")
	case !isHash(e.Leaves) || !isHash(e.Prev) || !isHash(e.Hash):
		return bad("malformed hash")
	}
	return nil
}

// CheckChain checks every entry (Check) and that they form a chain: seq
// counts from 1, each prev is the previous hash, and each range starts
// where the previous one ended.
func CheckChain(entries []Entry, trusted Keys) error {
	prev := Entry{Hash: ZeroHash}
	for i := range entries {
		e := &entries[i]
		if err := e.Check(trusted); err != nil {
			return err
		}
		if e.Seq != int64(i)+1 {
			return fmt.Errorf("attestation #%d: expected #%d (gap or reordering)", e.Seq, i+1)
		}
		if e.Prev != prev.Hash {
			return fmt.Errorf("attestation #%d: prev does not match the hash of #%d", e.Seq, prev.Seq)
		}
		if e.MessagesAfter != prev.MessagesUpto || e.DeletionsAfter != prev.DeletionsUpto {
			return fmt.Errorf("attestation #%d: range does not start where #%d ended", e.Seq, prev.Seq)
		}
		prev = *e
	}
	return nil
}

// Next returns the fields of the attestation that follows prev (nil for
// the first): seq, prev hash and where the ranges start.
func Next(prev *Entry) Entry {
	if prev == nil {
		return Entry{Seq: 1, Prev: ZeroHash}
	}
	return Entry{
		Seq: prev.Seq + 1, Prev: prev.Hash,
		MessagesAfter: prev.MessagesUpto, DeletionsAfter: prev.DeletionsUpto,
	}
}

func isHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// formatInt is strconv.FormatInt in base 10.
func formatInt(n int64) string { return strconv.FormatInt(n, 10) }

// ChainReport is what ExamineChain found.
type ChainReport struct {
	// Problems are broken links, bad hashes or signatures and untrusted
	// keys, each naming the attestation.
	Problems []string
	// KeyChanges are the attestations signed with another key than the
	// one before (a rotation).
	KeyChanges []int64
}

// ExamineChain checks entries like CheckChain but goes on after a problem
// and reports every one. A broken entry still counts as the previous one
// for the next link, so a single changed entry is reported once for itself
// and once for the link to it.
func ExamineChain(entries []Entry, trusted Keys) ChainReport {
	var r ChainReport
	prev := Entry{Hash: ZeroHash}
	for i := range entries {
		e := &entries[i]
		if err := e.Check(trusted); err != nil {
			r.Problems = append(r.Problems, err.Error())
		}
		switch {
		case e.Seq != int64(i)+1:
			r.Problems = append(r.Problems, fmt.Sprintf("attestation #%d: expected #%d (gap or reordering)", e.Seq, i+1))
		case e.Prev != prev.Hash:
			r.Problems = append(r.Problems, fmt.Sprintf("attestation #%d: prev does not match the hash of #%d", e.Seq, prev.Seq))
		case e.MessagesAfter != prev.MessagesUpto || e.DeletionsAfter != prev.DeletionsUpto:
			r.Problems = append(r.Problems, fmt.Sprintf("attestation #%d: range does not start where #%d ended", e.Seq, prev.Seq))
		}
		if i > 0 && e.Key != prev.Key {
			r.KeyChanges = append(r.KeyChanges, e.Seq)
		}
		prev = *e
	}
	return r
}
