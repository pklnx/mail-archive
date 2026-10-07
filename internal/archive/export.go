package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/mailbox"
	"github.com/pklnx/mail-archive/internal/store"
)

// Export formats.
const (
	FormatMbox    = "mbox"
	FormatMaildir = "maildir"
)

// ExportOptions selects what Export writes. The accounts must all belong
// to Owner (nil for accounts without owner); the database query checks it.
type ExportOptions struct {
	Format     string
	Out        string // must not exist yet
	Owner      *int64
	AccountIDs []int64
	Folder     string // empty: all folders
	Logger     *slog.Logger
	Now        func() time.Time

	afterStart func() // for tests: runs right after the start is recorded
}

// SkippedMessage is a message Export could not write.
type SkippedMessage struct {
	SHA256  string
	Account string
	Folder  string
	Reason  string
}

// ExportResult summarizes an export.
type ExportResult struct {
	Folders  int
	Messages int
	Bytes    int64
	Skipped  []SkippedMessage
}

const exportPage = 1000

// Export writes the selected folders as one mbox file or one Maildir per
// folder, in <Out>/<account>/<folder>. It writes into a sibling directory
// and renames it to Out at the end, so Out appears complete or not at all.
// Locations added after the start are left out, and a message found twice
// in a folder is written once. A missing or corrupt file is skipped and
// listed in the result.
func Export(ctx context.Context, st *store.Store, blobs *blobstore.Store, opts ExportOptions) (*ExportResult, error) {
	if opts.Format != FormatMbox && opts.Format != FormatMaildir {
		return nil, fmt.Errorf("unknown format %q (use mbox or maildir)", opts.Format)
	}
	if len(opts.AccountIDs) == 0 {
		return nil, errors.New("no accounts to export")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	out, err := exportTarget(opts.Out, blobs.Root())
	if err != nil {
		return nil, err
	}
	maxID, err := st.MaxLocationID(ctx)
	if err != nil {
		return nil, err
	}
	if opts.afterStart != nil {
		opts.afterStart()
	}
	folders, err := st.ListExportFolders(ctx, opts.Owner, opts.AccountIDs, opts.Folder)
	if err != nil {
		return nil, err
	}
	if opts.Folder != "" && len(folders) == 0 {
		return nil, fmt.Errorf("folder %q not found", opts.Folder)
	}

	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		return nil, err
	}
	partial, err := os.MkdirTemp(filepath.Dir(out), filepath.Base(out)+".partial-")
	if err != nil {
		return nil, err
	}
	done := false
	defer func() {
		if !done {
			_ = os.RemoveAll(partial)
		}
	}()

	e := &exporter{st: st, blobs: blobs, opts: opts, maxID: maxID, now: opts.Now(), res: &ExportResult{}}
	for _, f := range folders {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		dir := filepath.Join(partial, PathSegment(f.Account))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		n, bytes, err := e.folder(ctx, f, filepath.Join(dir, PathSegment(f.Name)))
		if err != nil {
			return nil, fmt.Errorf("%s/%s: %w", f.Account, f.Name, err)
		}
		opts.Logger.Info("exported folder", "account", f.Account, "folder", f.Name, "format", opts.Format, "messages", n, "bytes", bytes)
		e.res.Folders++
	}
	if _, err := os.Lstat(out); err == nil {
		return nil, fmt.Errorf("%s was created meanwhile", out)
	}
	if err := os.Rename(partial, out); err != nil {
		return nil, err
	}
	done = true
	return e.res, nil
}

// exportTarget checks the output directory: it must not exist and must not
// be inside the blob store's messages/ or tmp/.
func exportTarget(out, dataDir string) (string, error) {
	if out == "" {
		return "", errors.New("no output directory")
	}
	abs, err := filepath.Abs(out)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(abs); err == nil {
		return "", fmt.Errorf("%s already exists", out)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	resolved := resolveExisting(abs)
	data := resolveExisting(dataDir)
	for _, sub := range []string{"messages", "tmp"} {
		if within(resolved, filepath.Join(data, sub)) {
			return "", fmt.Errorf("%s is inside the archive's %s directory", out, sub)
		}
	}
	return abs, nil
}

// resolveExisting resolves symlinks in the longest existing prefix of p.
func resolveExisting(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	rest := ""
	for cur := abs; ; cur = filepath.Dir(cur) {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest)
		}
		if filepath.Dir(cur) == cur {
			return abs
		}
		rest = filepath.Join(filepath.Base(cur), rest)
	}
}

func within(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	if err != nil {
		return false
	}
	return rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// PathSegment turns an account or folder name into exactly one path
// segment: '/', '\', '%', NUL, control characters and a leading '.' are
// percent-encoded, so "." and ".." cannot occur. Distinct names stay
// distinct.
func PathSegment(name string) string {
	if name == "" {
		return "%"
	}
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == '/' || c == '\\' || c == '%' || c < 0x20 || c == 0x7f || (i == 0 && c == '.') {
			fmt.Fprintf(&b, "%%%02X", c)
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

type exporter struct {
	st    *store.Store
	blobs *blobstore.Store
	opts  ExportOptions
	maxID int64
	now   time.Time
	res   *ExportResult
}

// sink writes one message of a folder.
type sink interface {
	write(r io.Reader, loc store.ExportLocation, date time.Time) error
	close() error
}

func (e *exporter) folder(ctx context.Context, f store.ExportFolder, path string) (n int, bytes int64, err error) {
	var s sink
	if e.opts.Format == FormatMbox {
		s, err = newMboxSink(path + ".mbox")
	} else {
		s, err = newMaildirSink(path)
	}
	if err != nil {
		return 0, 0, err
	}
	defer func() {
		if cerr := s.close(); err == nil {
			err = cerr
		}
	}()

	seen := map[string]bool{}
	afterValidity, afterUID := int64(-1), int64(-1)
	for {
		page, err := e.st.ListExportLocations(ctx, e.opts.Owner, f.ID, e.maxID, afterValidity, afterUID, exportPage)
		if err != nil {
			return n, bytes, err
		}
		if len(page) == 0 {
			return n, bytes, nil
		}
		last := page[len(page)-1]
		afterValidity, afterUID = int64(last.UIDValidity), int64(last.UID)
		for _, loc := range page {
			if err := ctx.Err(); err != nil {
				return n, bytes, err
			}
			if seen[loc.SHA256] {
				continue
			}
			seen[loc.SHA256] = true
			reason, err := e.message(s, loc)
			if err != nil {
				return n, bytes, err
			}
			if reason != "" {
				e.res.Skipped = append(e.res.Skipped, SkippedMessage{SHA256: loc.SHA256, Account: f.Account, Folder: f.Name, Reason: reason})
				continue
			}
			n++
			bytes += loc.Size
			e.res.Messages++
			e.res.Bytes += loc.Size
		}
	}
}

// message writes one message. A missing or corrupt file gives a reason and
// leaves nothing behind; other errors end the export.
func (e *exporter) message(s sink, loc store.ExportLocation) (skipped string, err error) {
	if !blobstore.IsHash(loc.SHA256) {
		return "invalid hash", nil
	}
	f, err := e.blobs.Open(blobstore.RelPath(loc.SHA256))
	if errors.Is(err, fs.ErrNotExist) {
		return "file missing", nil
	}
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	date := e.now
	switch {
	case loc.InternalDate != nil:
		date = *loc.InternalDate
	case loc.SentAt != nil:
		date = *loc.SentAt
	}
	r := &checkedReader{r: f, h: sha256.New(), want: loc.SHA256, size: loc.Size}
	err = s.write(r, loc, date)
	var bad *blobError
	if errors.As(err, &bad) {
		return bad.reason, nil
	}
	return "", err
}

// blobError is a problem with the stored file, not with the output.
type blobError struct{ reason string }

func (e *blobError) Error() string { return e.reason }

// checkedReader hashes what it reads and fails at the end if the content
// is not what the hash promises.
type checkedReader struct {
	r    io.Reader
	h    hash.Hash
	n    int64
	want string
	size int64
}

func (c *checkedReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.h.Write(p[:n])
	c.n += int64(n)
	if errors.Is(err, io.EOF) {
		if hex.EncodeToString(c.h.Sum(nil)) != c.want || c.n != c.size {
			return n, &blobError{"file corrupt (hash or size differs)"}
		}
		return n, io.EOF
	}
	if err != nil {
		return n, &blobError{"read error: " + err.Error()}
	}
	return n, nil
}

type mboxSink struct {
	f *os.File
	w *mailbox.MboxWriter
}

func newMboxSink(path string) (*mboxSink, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // a new file in the export, named by PathSegment
	if err != nil {
		return nil, err
	}
	return &mboxSink{f: f, w: mailbox.NewMboxWriter(f)}, nil
}

func (m *mboxSink) write(r io.Reader, _ store.ExportLocation, date time.Time) error {
	start, err := m.f.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	err = m.w.WriteMessage(r, date)
	var bad *blobError
	if errors.As(err, &bad) {
		// Cut the partly written message off again.
		if terr := m.f.Truncate(start); terr != nil {
			return terr
		}
		if _, serr := m.f.Seek(start, io.SeekStart); serr != nil {
			return serr
		}
	}
	return err
}

func (m *mboxSink) close() error {
	if err := m.f.Sync(); err != nil {
		_ = m.f.Close()
		return err
	}
	return m.f.Close()
}

type maildirSink struct {
	md *mailbox.Maildir
}

func newMaildirSink(path string) (*maildirSink, error) {
	md, err := mailbox.CreateMaildir(path, 0o700)
	if err != nil {
		return nil, err
	}
	return &maildirSink{md: md}, nil
}

func (m *maildirSink) write(r io.Reader, loc store.ExportLocation, date time.Time) error {
	_, err := m.md.Deliver(r, mailbox.MaildirMessage{
		Unique: fmt.Sprintf("%d.%s.mail-archive", date.Unix(), loc.SHA256[:16]),
		Flags:  loc.Flags,
		Date:   date,
	})
	return err
}

func (m *maildirSink) close() error { return nil }
