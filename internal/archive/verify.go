package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/store"
)

// Kinds of verify findings.
const (
	FindingMissing    = "missing"    // a messages row without its file
	FindingCorrupt    = "corrupt"    // the file's hash or size differs from the row
	FindingBadPath    = "bad-path"   // stored_path is not where the hash says
	FindingOrphan     = "orphan"     // a blob file without a messages row
	FindingStaleTemp  = "stale-temp" // a file left in tmp/ by an unfinished write
	FindingUnexpected = "unexpected" // anything else in messages/ or tmp/
	FindingIOError    = "io-error"   // a file that could not be checked
)

// Finding is one problem verify found.
type Finding struct {
	Kind   string
	SHA256 string
	Path   string // relative to the data directory
	Size   int64
	Detail string
	// Places tells owners what to restore, for missing and corrupt files.
	Places []store.MessagePlace
}

// VerifyOptions configures Verify.
type VerifyOptions struct {
	Jobs        int           // parallel hashing, 1 to 16 (default 4)
	LockTimeout time.Duration // how long to wait for the blob lock (default 1 minute)
	// FixOrphans moves orphans and stale temp files to orphans/<time>/ in
	// the data directory, after reporting them.
	FixOrphans bool
	OnFinding  func(Finding)
	Progress   func(checked int) // every 10,000 messages
	Logger     *slog.Logger
	Now        func() time.Time

	beforeMove func() // for tests: runs between the check and the move
}

// VerifyResult summarizes a verify run.
type VerifyResult struct {
	Checked        int            // messages rows checked
	Findings       map[string]int // count per kind
	OrphansSkipped bool           // the blob lock was busy, no orphan check
	Moved          int            // files moved by FixOrphans
	MovedBytes     int64
	MovedTo        string   // the directory they went to, relative to the data directory
	OldOrphanDirs  []string // orphans/ directories from earlier runs
}

// Problems is the number of findings, without I/O errors.
func (r *VerifyResult) Problems() int {
	n := 0
	for k, c := range r.Findings {
		if k != FindingIOError {
			n += c
		}
	}
	return n
}

// Complete reports whether every file was checked.
func (r *VerifyResult) Complete() bool {
	return !r.OrphansSkipped && r.Findings[FindingIOError] == 0
}

const verifyPage = 1000

// Verify checks the archive: every messages row against its file, then,
// under the exclusive blob lock, every file against the rows. It never
// changes a referenced file or a database row. Errors other than findings
// (database, context) end it early with an error.
func Verify(ctx context.Context, st *store.Store, blobs *blobstore.Store, opts VerifyOptions) (*VerifyResult, error) {
	if opts.Jobs == 0 {
		opts.Jobs = 4
	}
	if opts.Jobs < 1 || opts.Jobs > 16 {
		return nil, fmt.Errorf("jobs must be between 1 and 16, got %d", opts.Jobs)
	}
	if opts.LockTimeout == 0 {
		opts.LockTimeout = time.Minute
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	v := &verifier{st: st, blobs: blobs, opts: opts, res: &VerifyResult{Findings: map[string]int{}}}
	if err := v.checkRows(ctx); err != nil {
		return v.res, err
	}
	if err := v.checkFiles(ctx); err != nil {
		return v.res, err
	}
	old, err := oldOrphanDirs(blobs.Root(), v.res.MovedTo)
	if err != nil {
		return v.res, err
	}
	v.res.OldOrphanDirs = old
	return v.res, nil
}

type verifier struct {
	st    *store.Store
	blobs *blobstore.Store
	opts  VerifyOptions
	res   *VerifyResult
}

func (v *verifier) report(f Finding) {
	v.res.Findings[f.Kind]++
	if v.opts.OnFinding != nil {
		v.opts.OnFinding(f)
	}
}

// checkRows re-hashes the file of every messages row, page by page.
func (v *verifier) checkRows(ctx context.Context) error {
	after := ""
	for {
		page, err := v.st.ListMessagesAfter(ctx, after, verifyPage)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			return nil
		}
		after = page[len(page)-1].SHA256

		findings := make([][]Finding, len(page))
		jobs := make(chan int)
		var wg sync.WaitGroup
		for range v.opts.Jobs {
			wg.Go(func() {
				for i := range jobs {
					findings[i] = v.checkRow(page[i])
				}
			})
		}
	send:
		for i := range page {
			select {
			case jobs <- i:
			case <-ctx.Done():
				break send
			}
		}
		close(jobs)
		wg.Wait()
		if err := ctx.Err(); err != nil {
			return err
		}

		var lost []string
		for _, list := range findings {
			for _, f := range list {
				if f.Kind == FindingMissing || f.Kind == FindingCorrupt {
					lost = append(lost, f.SHA256)
				}
			}
		}
		places := map[string][]store.MessagePlace{}
		if len(lost) > 0 {
			rows, err := v.st.LocationsOfMessages(ctx, lost)
			if err != nil {
				return err
			}
			for _, p := range rows {
				places[p.SHA256] = append(places[p.SHA256], p)
			}
		}
		for _, list := range findings {
			for _, f := range list {
				f.Places = places[f.SHA256]
				v.report(f)
			}
		}
		before := v.res.Checked
		v.res.Checked += len(page)
		if v.opts.Progress != nil && v.res.Checked/10000 > before/10000 {
			v.opts.Progress(v.res.Checked)
		}
	}
}

// checkRow checks one row. Files are opened only by the path the hash
// gives, never by the stored path.
func (v *verifier) checkRow(m store.StoredMessage) []Finding {
	if !blobstore.IsHash(m.SHA256) {
		return []Finding{{Kind: FindingBadPath, SHA256: m.SHA256, Path: m.StoredPath, Detail: "the hash is not 64 lowercase hex digits"}}
	}
	var out []Finding
	rel := blobstore.RelPath(m.SHA256)
	if m.StoredPath != rel {
		out = append(out, Finding{Kind: FindingBadPath, SHA256: m.SHA256, Path: m.StoredPath, Detail: "expected " + rel})
	}
	f, err := v.blobs.Open(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return append(out, Finding{Kind: FindingMissing, SHA256: m.SHA256, Path: rel, Size: m.Size})
	}
	if err != nil {
		return append(out, Finding{Kind: FindingIOError, SHA256: m.SHA256, Path: rel, Detail: err.Error()})
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return append(out, Finding{Kind: FindingIOError, SHA256: m.SHA256, Path: rel, Detail: err.Error()})
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != m.SHA256 || n != m.Size {
		detail := fmt.Sprintf("size %d, want %d", n, m.Size)
		if got != m.SHA256 {
			detail = fmt.Sprintf("content hash %s, size %d (want %d)", got, n, m.Size)
		}
		out = append(out, Finding{Kind: FindingCorrupt, SHA256: m.SHA256, Path: rel, Size: n, Detail: detail})
	}
	return out
}

// checkFiles looks for files without rows, under the exclusive blob lock so
// that no sync writes a blob whose row is not committed yet.
func (v *verifier) checkFiles(ctx context.Context) error {
	unlock, err := v.lockBlobs(ctx)
	if err != nil {
		return err
	}
	if unlock == nil {
		v.res.OrphansSkipped = true
		return nil
	}
	defer unlock()

	var batch []blobstore.Entry
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := v.checkOrphans(ctx, batch)
		batch = batch[:0]
		return err
	}
	err = v.blobs.Walk(func(e blobstore.Entry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch e.Kind {
		case blobstore.EntryBlob:
			batch = append(batch, e)
			if len(batch) >= verifyPage {
				return flush()
			}
		case blobstore.EntryTemp:
			f := Finding{Kind: FindingStaleTemp, Path: e.Path, Size: e.Size}
			v.report(f)
			if v.opts.FixOrphans {
				return v.move(f)
			}
		default:
			v.report(Finding{Kind: FindingUnexpected, Path: e.Path, Detail: e.Reason})
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := flush(); err != nil {
		return err
	}
	if v.res.Moved > 0 {
		v.opts.Logger.Info("verify moved orphans", "files", v.res.Moved, "bytes", v.res.MovedBytes, "to", v.res.MovedTo)
	}
	return nil
}

// lockBlobs takes the exclusive blob lock, retrying until LockTimeout. It
// returns a nil unlock when the lock stayed busy.
func (v *verifier) lockBlobs(ctx context.Context) (func(), error) {
	deadline := time.Now().Add(v.opts.LockTimeout)
	for {
		unlock, ok, err := v.st.TryLockBlobs(ctx)
		if err != nil {
			return nil, err
		}
		if ok {
			return unlock, nil
		}
		wait := min(time.Second, time.Until(deadline))
		if wait <= 0 {
			return nil, nil
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// checkOrphans reports the blobs of a batch that have no row and, with
// FixOrphans, moves them after checking again.
func (v *verifier) checkOrphans(ctx context.Context, batch []blobstore.Entry) error {
	exist, err := v.st.ExistingMessages(ctx, entryHashes(batch))
	if err != nil {
		return err
	}
	var orphans []blobstore.Entry
	for _, e := range batch {
		if !exist[e.SHA256] {
			orphans = append(orphans, e)
			v.report(Finding{Kind: FindingOrphan, SHA256: e.SHA256, Path: e.Path, Size: e.Size})
		}
	}
	if !v.opts.FixOrphans || len(orphans) == 0 {
		return nil
	}
	if v.opts.beforeMove != nil {
		v.opts.beforeMove()
	}
	// No sync can add a row while the lock is held, but check again so
	// that nothing referenced is ever moved.
	exist, err = v.st.ExistingMessages(ctx, entryHashes(orphans))
	if err != nil {
		return err
	}
	for _, e := range orphans {
		if exist[e.SHA256] {
			v.opts.Logger.Warn("verify keeps a file that has a row now", "sha256", e.SHA256)
			continue
		}
		if err := v.move(Finding{Kind: FindingOrphan, SHA256: e.SHA256, Path: e.Path, Size: e.Size}); err != nil {
			return err
		}
	}
	return nil
}

// move renames a file into orphans/<time>/, keeping its relative path.
func (v *verifier) move(f Finding) error {
	if v.res.MovedTo == "" {
		v.res.MovedTo = "orphans/" + v.opts.Now().UTC().Format("20060102T150405Z")
	}
	root := v.blobs.Root()
	src := filepath.Join(root, filepath.FromSlash(f.Path))
	dst := filepath.Join(root, filepath.FromSlash(v.res.MovedTo), filepath.FromSlash(f.Path))
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("%s already exists", dst)
	}
	if err := os.Rename(src, dst); err != nil {
		return err
	}
	v.res.Moved++
	v.res.MovedBytes += f.Size
	v.opts.Logger.Info("verify moved file", "kind", f.Kind, "sha256", f.SHA256, "path", f.Path, "size", f.Size)
	return nil
}

func entryHashes(entries []blobstore.Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.SHA256
	}
	return out
}

// oldOrphanDirs lists orphans/<time> directories other than skip, relative
// to root.
func oldOrphanDirs(root, skip string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, "orphans"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if p := "orphans/" + e.Name(); p != skip {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out, nil
}
