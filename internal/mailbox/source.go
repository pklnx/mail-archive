package mailbox

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

// Limits for import sources.
const (
	MaxFolders       = 10000
	MaxFolderNameLen = 1000
)

// Kinds of SourceFolder.
const (
	KindMbox    = "mbox"
	KindMaildir = "maildir"
	KindEML     = "eml"
)

// ReasonArchive is the Skipped reason of a ZIP or gzip file in an EML
// directory: probably an export that was not unpacked.
const ReasonArchive = "compressed archive; unpack it first"

// SourceFolder is one folder found in an import source: an mbox file, a
// Maildir directory or a directory of .eml files.
type SourceFolder struct {
	// Name is slash-separated, like an IMAP folder name. ListEML leaves it
	// empty for files directly in the source directory.
	Name string
	Path string
	Kind string
}

// Skipped is something the walk left out, for a debug log line.
type Skipped struct {
	Path   string
	Reason string
}

// ListMbox finds the mbox files in path:
//   - a single file: one folder named after the file without ".mbox";
//   - an Apple Mail bundle "Name.mbox" with a file "mbox" inside: "Name";
//   - a directory, walked recursively: every regular file whose first
//     non-empty line starts with "From ". Thunderbird's "X.sbd/Y" and
//     Apple's "X.mbox/Y.mbox" give "X/Y".
//
// Symlinks below path are not followed, and files that are not regular are
// skipped.
func ListMbox(path string) ([]SourceFolder, []Skipped, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if !fi.IsDir() {
		if !fi.Mode().IsRegular() {
			return nil, nil, fmt.Errorf("%s is not a regular file", path)
		}
		if err := checkMbox(path); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", path, err)
		}
		return []SourceFolder{{Name: folderPart(filepath.Base(path)), Path: path, Kind: KindMbox}}, nil, nil
	}
	var out []SourceFolder
	var skipped []Skipped
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(path, p)
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			skipped = append(skipped, Skipped{rel, "not a regular file"})
			return nil
		}
		if reason := checkMbox(p); reason != nil {
			skipped = append(skipped, Skipped{rel, reason.Error()})
			return nil //nolint:nilerr // not an mbox file: skip it, keep walking
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if parts[len(parts)-1] == "mbox" && len(parts) > 1 && strings.HasSuffix(parts[len(parts)-2], ".mbox") {
			parts = parts[:len(parts)-1] // Apple Mail: Name.mbox/mbox
		}
		if len(parts) == 1 && parts[0] == "mbox" {
			parts = []string{folderPart(filepath.Base(path))} // path is the bundle itself
		}
		for i := range parts {
			parts[i] = folderPart(parts[i])
		}
		out = append(out, SourceFolder{Name: strings.Join(parts, "/"), Path: p, Kind: KindMbox})
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return out, skipped, checkFolders(out)
}

// folderPart strips the suffixes mail programs add to folder files.
func folderPart(name string) string {
	for _, suffix := range []string{".mbox", ".sbd", ".mbx"} {
		if n, ok := strings.CutSuffix(name, suffix); ok && n != "" {
			return n
		}
	}
	return name
}

// checkMbox tells whether the file looks like an mbox file.
func checkMbox(path string) error {
	f, err := os.Open(path) //nolint:gosec // a file of the import source
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	r := bufio.NewReaderSize(f, lineBuffer)
	if head, _ := r.Peek(4); bytes.HasPrefix(head, []byte{0x1f, 0x8b}) || bytes.HasPrefix(head, []byte("PK\x03\x04")) {
		return ErrCompressed
	}
	for {
		line, err := r.ReadSlice('\n')
		if len(line) > 0 && !isBlank(line) {
			if bytes.HasPrefix(line, []byte("From ")) {
				return nil
			}
			return ErrNotMbox
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return errors.New("empty file")
			}
			return err
		}
	}
}

// ListMaildir finds the folders of a Maildir: path itself (if it has cur
// and new) is INBOX, Maildir++ subfolders ".A.B" are "A/B", with modified
// UTF-7 decoded.
func ListMaildir(path string) ([]SourceFolder, []Skipped, error) {
	if !isMaildir(path) {
		return nil, nil, fmt.Errorf("%s is not a Maildir (no cur and new directories)", path)
	}
	out := []SourceFolder{{Name: "INBOX", Path: path, Kind: KindMaildir}}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, nil, err
	}
	var skipped []Skipped
	for _, e := range entries {
		name := e.Name()
		switch {
		case name == "cur" || name == "new" || name == "tmp":
			continue
		case !strings.HasPrefix(name, ".") || name == "." || name == "..":
			skipped = append(skipped, Skipped{name, "not a Maildir++ folder"})
			continue
		case !e.IsDir():
			skipped = append(skipped, Skipped{name, "not a directory"})
			continue
		case !isMaildir(filepath.Join(path, name)):
			skipped = append(skipped, Skipped{name, "no cur and new directories"})
			continue
		}
		var parts []string
		for _, p := range strings.Split(name[1:], ".") {
			decoded, err := DecodeModifiedUTF7(p)
			if err != nil {
				return nil, nil, fmt.Errorf("folder %q: %w", name, err)
			}
			parts = append(parts, decoded)
		}
		out = append(out, SourceFolder{Name: strings.Join(parts, "/"), Path: filepath.Join(path, name), Kind: KindMaildir})
	}
	return out, skipped, checkFolders(out)
}

func isMaildir(dir string) bool {
	for _, sub := range []string{"cur", "new"} {
		fi, err := os.Lstat(filepath.Join(dir, sub))
		if err != nil || !fi.IsDir() {
			return false
		}
	}
	return true
}

// ListEML finds the directories under path that hold .eml files (in any
// case). Each is a folder named by its slash-separated path relative to
// path; files directly in path give a folder with an empty name, which the
// caller must name. Hidden files and directories (a leading ".", which also
// covers macOS "._x.eml" files) and symlinks are skipped, and so is every
// file without the .eml suffix.
func ListEML(path string) ([]SourceFolder, []Skipped, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if !fi.IsDir() {
		if err := checkNotCompressed(path); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", path, err)
		}
		return nil, nil, fmt.Errorf("%s is not a directory; --format eml reads a directory of .eml files", path)
	}
	var out []SourceFolder
	var skipped []Skipped
	seen := map[string]bool{}
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == path {
			return nil
		}
		rel, err := filepath.Rel(path, p)
		if err != nil {
			return err
		}
		if strings.HasPrefix(d.Name(), ".") {
			skipped = append(skipped, Skipped{rel, "hidden"})
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		switch {
		case !d.Type().IsRegular():
			skipped = append(skipped, Skipped{rel, "not a regular file"})
		case isArchiveName(d.Name()):
			skipped = append(skipped, Skipped{rel, ReasonArchive})
		case !isEML(d.Name()):
			skipped = append(skipped, Skipped{rel, "not an .eml file"})
		default:
			dir := filepath.Dir(p)
			if !seen[dir] {
				seen[dir] = true
				name := ""
				if dir != path {
					relDir, _ := filepath.Rel(path, dir)
					name = filepath.ToSlash(relDir)
				}
				out = append(out, SourceFolder{Name: name, Path: dir, Kind: KindEML})
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if len(out) > MaxFolders {
		return nil, nil, fmt.Errorf("%d folders, at most %d per import", len(out), MaxFolders)
	}
	return out, skipped, nil
}

func isEML(name string) bool {
	return len(name) > 4 && strings.EqualFold(name[len(name)-4:], ".eml")
}

func isArchiveName(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".zip") || strings.HasSuffix(lower, ".gz") || strings.HasSuffix(lower, ".tgz")
}

// checkNotCompressed returns ErrCompressed for a gzip or zip file.
func checkNotCompressed(path string) error {
	f, err := os.Open(path) //nolint:gosec // a file of the import source
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, 4)
	n, _ := io.ReadFull(f, head)
	if bytes.HasPrefix(head[:n], []byte{0x1f, 0x8b}) || bytes.HasPrefix(head[:n], []byte("PK\x03\x04")) {
		return ErrCompressed
	}
	return nil
}

// EMLFile is a message file in an EML folder.
type EMLFile struct {
	Path string
	Size int64
	Date time.Time // the file's modification time
}

// EMLFiles lists the .eml files directly in dir, not in its
// subdirectories, sorted by name (byte order) so that repeated imports
// assign the same UIDs. Hidden files and symlinks are left out.
func EMLFiles(dir string) ([]EMLFile, error) {
	entries, err := os.ReadDir(dir) // sorted by name
	if err != nil {
		return nil, err
	}
	var out []EMLFile
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || !e.Type().IsRegular() || !isEML(name) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		out = append(out, EMLFile{Path: filepath.Join(dir, name), Size: info.Size(), Date: info.ModTime()})
	}
	return out, nil
}

// checkFolders enforces the limits on folder names and numbers.
func checkFolders(folders []SourceFolder) error {
	if len(folders) > MaxFolders {
		return fmt.Errorf("%d folders, at most %d per import", len(folders), MaxFolders)
	}
	for _, f := range folders {
		if err := CheckFolderName(f.Name); err != nil {
			return fmt.Errorf("%s: %w", f.Path, err)
		}
	}
	return nil
}

// CheckFolderName accepts valid UTF-8 without control characters, at most
// MaxFolderNameLen bytes.
func CheckFolderName(name string) error {
	switch {
	case name == "":
		return errors.New("empty folder name")
	case len(name) > MaxFolderNameLen:
		return fmt.Errorf("folder name longer than %d bytes", MaxFolderNameLen)
	case !utf8.ValidString(name):
		return errors.New("folder name is not valid UTF-8")
	case strings.IndexFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f || r >= 0x80 && r < 0xa0 }) >= 0:
		return errors.New("folder name contains control characters")
	}
	return nil
}

// MaildirFile is a message file in a Maildir folder.
type MaildirFile struct {
	Path  string
	Flags []string
	Date  time.Time // the file's modification time
}

// MaildirFiles lists the regular files in cur/ and new/, sorted by name so
// that repeated imports see the same order.
func MaildirFiles(dir string) ([]MaildirFile, []Skipped, error) {
	var out []MaildirFile
	var skipped []Skipped
	for _, sub := range []string{"cur", "new"} {
		entries, err := os.ReadDir(filepath.Join(dir, sub))
		if err != nil {
			return nil, nil, err
		}
		for _, e := range entries {
			p := filepath.Join(dir, sub, e.Name())
			if !e.Type().IsRegular() {
				skipped = append(skipped, Skipped{p, "not a regular file"})
				continue
			}
			info, err := e.Info()
			if err != nil {
				return nil, nil, err
			}
			out = append(out, MaildirFile{Path: p, Flags: IMAPFlags(e.Name()), Date: info.ModTime()})
		}
	}
	slices.SortFunc(out, func(a, b MaildirFile) int {
		return strings.Compare(filepath.Base(a.Path), filepath.Base(b.Path))
	})
	return out, skipped, nil
}

// DecodeModifiedUTF7 decodes an IMAP folder name in modified UTF-7 (RFC
// 3501): "&" starts base64 of UTF-16 with ',' for '/', "-" ends it, and
// "&-" is "&".
func DecodeModifiedUTF7(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '&' {
			if c < 0x20 || c > 0x7e {
				return "", fmt.Errorf("invalid character in modified UTF-7: %q", c)
			}
			b.WriteByte(c)
			continue
		}
		end := strings.IndexByte(s[i+1:], '-')
		if end < 0 {
			return "", errors.New("unterminated modified UTF-7 sequence")
		}
		enc := s[i+1 : i+1+end]
		i += end + 1
		if enc == "" {
			b.WriteByte('&')
			continue
		}
		raw, err := base64.RawStdEncoding.DecodeString(strings.ReplaceAll(enc, ",", "/"))
		if err != nil || len(raw)%2 != 0 {
			return "", fmt.Errorf("invalid modified UTF-7 sequence %q", enc)
		}
		units := make([]uint16, len(raw)/2)
		for j := range units {
			units[j] = uint16(raw[2*j])<<8 | uint16(raw[2*j+1])
		}
		b.WriteString(string(utf16.Decode(units)))
	}
	return b.String(), nil
}
