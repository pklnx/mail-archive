package attest

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"
)

const leavesHeader = "mail-archive leaves v1\n"

// Deletion is a recorded deletion of a message.
type Deletion struct {
	ID         int64
	MessageSeq int64
	SHA256     string
	Reason     string
}

// LeavesWriter writes a leaves file and hashes it as it goes: first the
// messages in seq order ("+ <seq> <sha256>"), then the deletions in id
// order ("- <id> <message_seq> <sha256> <reason>").
type LeavesWriter struct {
	w         *bufio.Writer
	h         hash.Hash
	lastSeq   int64
	lastDel   int64
	deletions bool
	messages  int64
	deleted   int64
}

// NewLeavesWriter starts a leaves file on w.
func NewLeavesWriter(w io.Writer) (*LeavesWriter, error) {
	h := sha256.New()
	l := &LeavesWriter{w: bufio.NewWriter(io.MultiWriter(w, h)), h: h}
	_, err := l.w.WriteString(leavesHeader)
	return l, err
}

// Message adds a message line. Messages come in increasing seq order and
// before every deletion.
func (l *LeavesWriter) Message(seq int64, sha string) error {
	if l.deletions {
		return errors.New("leaves: message after a deletion")
	}
	if seq <= l.lastSeq || !isHash(sha) {
		return fmt.Errorf("leaves: bad message line %d %q", seq, sha)
	}
	l.lastSeq = seq
	l.messages++
	_, err := l.w.WriteString("+ " + formatInt(seq) + " " + sha + "\n")
	return err
}

// Deletion adds a deletion line, in increasing id order.
func (l *LeavesWriter) Deletion(d Deletion) error {
	l.deletions = true
	if d.ID <= l.lastDel || d.MessageSeq < 1 || !isHash(d.SHA256) || d.Reason == "" ||
		strings.ContainsAny(d.Reason, " \t\r\n") {
		return fmt.Errorf("leaves: bad deletion line %d", d.ID)
	}
	l.lastDel = d.ID
	l.deleted++
	_, err := l.w.WriteString("- " + formatInt(d.ID) + " " + formatInt(d.MessageSeq) + " " + d.SHA256 + " " + d.Reason + "\n")
	return err
}

// Finish flushes the file and returns its SHA-256 and the number of
// message and deletion lines.
func (l *LeavesWriter) Finish() (sum string, messages, deletions int64, err error) {
	if err := l.w.Flush(); err != nil {
		return "", 0, 0, err
	}
	return hex.EncodeToString(l.h.Sum(nil)), l.messages, l.deleted, nil
}
