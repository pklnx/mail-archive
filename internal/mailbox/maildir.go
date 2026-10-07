package mailbox

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// maildirFlags maps IMAP system flags to Maildir info flags.
var maildirFlags = map[string]byte{
	`\draft`:    'D',
	`\flagged`:  'F',
	`\answered`: 'R',
	`\seen`:     'S',
	`\deleted`:  'T',
}

// MaildirFlags returns the Maildir info flags for IMAP flags, in ASCII
// order. Keywords and unknown flags have no Maildir letter and are dropped.
func MaildirFlags(imapFlags []string) string {
	var out []byte
	for _, f := range imapFlags {
		if c, ok := maildirFlags[strings.ToLower(f)]; ok && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	slices.Sort(out)
	return string(out)
}

// IMAPFlags returns the IMAP flags for the info part of a Maildir file name
// ("<unique>:2,<flags>"), in the order of the letters.
func IMAPFlags(name string) []string {
	_, info, ok := strings.Cut(name, ":2,")
	if !ok {
		return nil
	}
	var out []string
	for _, c := range []byte(info) {
		for f, l := range maildirFlags {
			if l == c {
				out = append(out, `\`+strings.ToUpper(f[1:2])+f[2:])
			}
		}
	}
	return out
}

// Maildir is a Maildir directory with cur/, new/ and tmp/.
type Maildir struct {
	dir string
}

// CreateMaildir creates dir and its subdirectories with mode perm.
func CreateMaildir(dir string, perm os.FileMode) (*Maildir, error) {
	for _, sub := range []string{"cur", "new", "tmp"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), perm); err != nil {
			return nil, err
		}
	}
	return &Maildir{dir: dir}, nil
}

// MaildirMessage names a message to deliver.
type MaildirMessage struct {
	// Unique is the part of the file name before ":2,". It must be unique
	// in the Maildir and must not contain '/', ':' or NUL.
	Unique string
	Flags  []string // IMAP flags
	Date   time.Time
}

// Deliver writes a message to tmp/ and moves it into cur/ with its flags.
// The file gets mode 0600 and Date as its modification time. If r fails,
// nothing is left behind.
func (m *Maildir) Deliver(r io.Reader, msg MaildirMessage) (path string, err error) {
	if msg.Unique == "" || strings.ContainsAny(msg.Unique, "/:\x00") || msg.Unique[0] == '.' {
		return "", fmt.Errorf("invalid Maildir name %q", msg.Unique)
	}
	tmp := filepath.Join(m.dir, "tmp", msg.Unique)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // a new file; the name is checked above
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	if _, err = io.Copy(f, r); err != nil {
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	if !msg.Date.IsZero() {
		if err = os.Chtimes(tmp, msg.Date, msg.Date); err != nil {
			return "", err
		}
	}
	path = filepath.Join(m.dir, "cur", msg.Unique+":2,"+MaildirFlags(msg.Flags))
	if _, err = os.Lstat(path); err == nil {
		return "", fmt.Errorf("%s already exists", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err = os.Rename(tmp, path); err != nil {
		return "", err
	}
	return path, nil
}

// MaildirEntry is a message file in a Maildir.
type MaildirEntry struct {
	Path  string
	Flags []string // IMAP flags from the file name
}

// ReadMaildir lists the messages in new/ and cur/ of dir, sorted by path.
// Files whose name starts with '.' are skipped.
func ReadMaildir(dir string) ([]MaildirEntry, error) {
	var out []MaildirEntry
	for _, sub := range []string{"cur", "new"} {
		entries, err := os.ReadDir(filepath.Join(dir, sub))
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !e.Type().IsRegular() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			out = append(out, MaildirEntry{Path: filepath.Join(dir, sub, e.Name()), Flags: IMAPFlags(e.Name())})
		}
	}
	slices.SortFunc(out, func(a, b MaildirEntry) int { return strings.Compare(a.Path, b.Path) })
	return out, nil
}
