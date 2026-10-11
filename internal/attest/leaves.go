package attest

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"strconv"
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

// Leaf is a message line of a leaves file.
type Leaf struct {
	Seq    int64
	SHA256 string
}

// Leaves is a parsed leaves file.
type Leaves struct {
	Messages  []Leaf
	Deletions []Deletion
}

// ParseLeaves reads a leaves file. It accepts only what LeavesWriter
// writes, so that a file it accepts hashes to the same digest when written
// again.
func ParseLeaves(data []byte) (*Leaves, error) {
	rest, ok := strings.CutPrefix(string(data), leavesHeader)
	if !ok {
		return nil, errors.New("leaves: no header")
	}
	// The writer checks the order and format of each line.
	w, _ := NewLeavesWriter(io.Discard)
	out := &Leaves{}
	for n := 2; rest != ""; n++ {
		line, after, ok := strings.Cut(rest, "\n")
		if !ok {
			return nil, fmt.Errorf("leaves: line %d is incomplete", n)
		}
		rest = after
		f := strings.Split(line, " ")
		var err error
		switch {
		case len(f) == 3 && f[0] == "+":
			var l Leaf
			if l.Seq, err = strconv.ParseInt(f[1], 10, 64); err == nil {
				l.SHA256 = f[2]
				err = w.Message(l.Seq, l.SHA256)
			}
			out.Messages = append(out.Messages, l)
		case len(f) == 5 && f[0] == "-":
			var d Deletion
			if d.ID, err = strconv.ParseInt(f[1], 10, 64); err == nil {
				if d.MessageSeq, err = strconv.ParseInt(f[2], 10, 64); err == nil {
					d.SHA256, d.Reason = f[3], f[4]
					err = w.Deletion(d)
				}
			}
			out.Deletions = append(out.Deletions, d)
		default:
			err = errors.New("unknown line")
		}
		if err != nil {
			return nil, fmt.Errorf("leaves: line %d: %w", n, err)
		}
	}
	return out, nil
}
