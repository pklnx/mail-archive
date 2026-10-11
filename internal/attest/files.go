package attest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// line is an Entry in the attestation file.
type line struct {
	Seq            int64  `json:"seq"`
	Time           string `json:"time"`
	MessagesAfter  int64  `json:"messages_after"`
	MessagesUpto   int64  `json:"messages_upto"`
	MessagesCount  int64  `json:"messages_count"`
	DeletionsAfter int64  `json:"deletions_after"`
	DeletionsUpto  int64  `json:"deletions_upto"`
	DeletionsCount int64  `json:"deletions_count"`
	Total          int64  `json:"total"`
	Leaves         string `json:"leaves"`
	Prev           string `json:"prev"`
	Key            string `json:"key"`
	Hash           string `json:"hash"`
	Sig            string `json:"sig"`
}

// MarshalLine encodes an entry as one line of the attestation file,
// including the newline.
func MarshalLine(e *Entry) ([]byte, error) {
	b, err := json.Marshal(line{
		Seq: e.Seq, Time: e.Time.UTC().Format(time.RFC3339),
		MessagesAfter: e.MessagesAfter, MessagesUpto: e.MessagesUpto, MessagesCount: e.MessagesCount,
		DeletionsAfter: e.DeletionsAfter, DeletionsUpto: e.DeletionsUpto, DeletionsCount: e.DeletionsCount,
		Total: e.Total, Leaves: e.Leaves, Prev: e.Prev, Key: e.Key, Hash: e.Hash, Sig: e.Sig,
	})
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// ParseLine decodes a line of the attestation file. Unknown fields are
// refused. It does not check the hash or signature (Entry.Check).
func ParseLine(b []byte) (Entry, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var l line
	if err := dec.Decode(&l); err != nil {
		return Entry{}, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Entry{}, errors.New("data after the object")
	}
	t, err := time.Parse(time.RFC3339, l.Time)
	if err != nil || t.UTC().Format(time.RFC3339) != l.Time {
		return Entry{}, fmt.Errorf("time %q is not RFC 3339 in UTC", l.Time)
	}
	return Entry{
		Seq: l.Seq, Time: t.UTC(),
		MessagesAfter: l.MessagesAfter, MessagesUpto: l.MessagesUpto, MessagesCount: l.MessagesCount,
		DeletionsAfter: l.DeletionsAfter, DeletionsUpto: l.DeletionsUpto, DeletionsCount: l.DeletionsCount,
		Total: l.Total, Leaves: l.Leaves, Prev: l.Prev, Key: l.Key, Hash: l.Hash, Sig: l.Sig,
	}, nil
}

// ReadFile reads an attestation file. A missing file holds no entries.
func ReadFile(path string) ([]Entry, error) {
	f, err := os.Open(path) //nolint:gosec // path in the data directory or given by the operator
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []Entry
	r := bufio.NewReader(f)
	for n := 1; ; n++ {
		b, err := r.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			if len(b) > 0 {
				return nil, fmt.Errorf("%s: line %d is incomplete (no newline)", path, n)
			}
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		e, err := ParseLine(b)
		if err != nil {
			return nil, fmt.Errorf("%s: line %d: %w", path, n, err)
		}
		out = append(out, e)
	}
}

// AppendFile appends an entry to the attestation file and syncs it to
// disk, and the directory too when the file is new.
func AppendFile(path string, e *Entry) error {
	b, err := MarshalLine(e)
	if err != nil {
		return err
	}
	return appendSync(path, b)
}

// LeavesPath is where the leaves file of attestation seq is.
func LeavesPath(dir string, seq int64) string {
	return filepath.Join(dir, LeavesDir, strconv.FormatInt(seq, 10)+".txt")
}

const leavesTempPrefix = ".tmp-"

// PendingLeaves is a leaves file being written to a temp file; Commit
// moves it into place.
type PendingLeaves struct {
	*LeavesWriter
	f   *os.File
	dir string
}

// CreateLeaves starts a leaves file in dir.
func CreateLeaves(dir string) (*PendingLeaves, error) {
	if err := ensureDir(dir); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(filepath.Join(dir, LeavesDir), leavesTempPrefix+"*")
	if err != nil {
		return nil, err
	}
	w, err := NewLeavesWriter(f)
	if err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, err
	}
	return &PendingLeaves{LeavesWriter: w, f: f, dir: dir}, nil
}

// Commit syncs the file and renames it to the leaves file of seq. Finish
// must have been called.
func (p *PendingLeaves) Commit(seq int64) error {
	if err := p.f.Sync(); err != nil {
		return err
	}
	if err := p.f.Close(); err != nil {
		return err
	}
	if err := os.Rename(p.f.Name(), LeavesPath(p.dir, seq)); err != nil {
		return err
	}
	p.f = nil
	return syncDir(filepath.Join(p.dir, LeavesDir))
}

// Abort removes the temp file unless Commit succeeded.
func (p *PendingLeaves) Abort() {
	if p.f != nil {
		_ = p.f.Close()
		_ = os.Remove(p.f.Name())
		p.f = nil
	}
}

// RemoveStaleLeaves removes temp files left by a crash. Only call it while
// no other attestation is written.
func RemoveStaleLeaves(dir string) error {
	entries, err := os.ReadDir(filepath.Join(dir, LeavesDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), leavesTempPrefix) {
			if err := os.Remove(filepath.Join(dir, LeavesDir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// ensureDir creates dir and its leaves directory, readable only by the
// owner.
func ensureDir(dir string) error {
	return os.MkdirAll(filepath.Join(dir, LeavesDir), 0o700)
}

// appendSync appends data to path (mode 0600 if new) and syncs it.
func appendSync(path string, data []byte) error {
	_, statErr := os.Lstat(path)
	created := errors.Is(statErr, fs.ErrNotExist)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600) //nolint:gosec // path in the data directory
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if created {
		return syncDir(filepath.Dir(path))
	}
	return nil
}

// createSync writes a new file with O_EXCL, mode 0600, and syncs it.
func createSync(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // path from configuration
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

// replaceSync writes path through a temp file (mode 0600) and rename.
func replaceSync(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // directory in the data directory
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}
