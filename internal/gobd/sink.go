package gobd

import (
	"archive/zip"
	"cmp"
	"compress/flate"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// ErrCannotUndo is returned by SinkFile.Abort when a sink cannot take a
// file back (a ZIP entry, once written, stays in the archive).
var ErrCannotUndo = errors.New("the file cannot be taken back out of a ZIP")

// Sink receives the files of an export one after the other. Paths are
// format v1 paths ("mail/2024/03/<sha>.eml").
type Sink interface {
	// Create starts a file. The previous file is committed or aborted
	// first.
	Create(path string) (SinkFile, error)
	// Close finishes the export: the ZIP's central directory, or the
	// directories synced to disk.
	Close() error
}

// SinkFile is one file being written.
type SinkFile interface {
	io.Writer
	// Commit ends the file.
	Commit() error
	// Abort removes the file, or returns ErrCannotUndo.
	Abort() error
}

// PartialSuffix names the directory a DirSink writes into until it is
// closed: <out>.partial.
const PartialSuffix = ".partial"

// tempPrefix starts the name of a file being written. Such files never
// end up in an export: Commit renames them, Abort removes them, and a
// resumed export removes those an interrupted run left.
const tempPrefix = ".tmp-"

// DirSink writes an export as a directory tree into <out>.partial:
// directories with mode 0700, files with 0600, each file written to a
// temporary name and renamed when committed. Sync puts everything written
// so far on disk (syncfs on Linux, else every file and directory synced
// one by one); Close syncs, renames <out>.partial to out and syncs out's
// parent, so out appears complete or not at all. An interrupted export
// leaves <out>.partial, which ResumeDirSink continues.
type DirSink struct {
	out, dir string
	dirs     map[string]bool
	drop     []string
}

// NewDirSink starts an export into out. Neither out nor <out>.partial may
// exist; out's parent must.
func NewDirSink(out string) (*DirSink, error) {
	if err := mustNotExist(out); err != nil {
		return nil, err
	}
	dir := out + PartialSuffix
	if err := os.Mkdir(dir, 0o700); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("%s exists: an interrupted export; resume it or remove it", dir)
		}
		return nil, err
	}
	return &DirSink{out: out, dir: dir, dirs: map[string]bool{dir: true}}, nil
}

// ResumeDirSink continues an interrupted export into out: <out>.partial
// must exist and out must not. Files already there stay until they are
// written again or removed.
func ResumeDirSink(out string) (*DirSink, error) {
	if err := mustNotExist(out); err != nil {
		return nil, err
	}
	dir := out + PartialSuffix
	fi, err := os.Lstat(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%s does not exist: there is no interrupted export to resume", dir)
		}
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}
	return &DirSink{out: out, dir: dir, dirs: map[string]bool{dir: true}}, nil
}

func mustNotExist(p string) error {
	if _, err := os.Lstat(p); err == nil {
		return fmt.Errorf("%s already exists", p)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// Dir is the directory being written, <out>.partial.
func (s *DirSink) Dir() string { return s.dir }

// DropOnClose names a file of the directory (not of the export, such as a
// resume state file) that Close removes before it syncs and renames.
func (s *DirSink) DropOnClose(path string) { s.drop = append(s.drop, path) }

func (s *DirSink) full(path string) string { return filepath.Join(s.dir, filepath.FromSlash(path)) }

// Create implements Sink. The file is written under a temporary name in
// its directory and gets its name in Commit, replacing a file of that
// name (from an interrupted run).
func (s *DirSink) Create(path string) (SinkFile, error) {
	full := s.full(path)
	parent := filepath.Dir(full)
	if !s.dirs[parent] {
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return nil, err
		}
		for d := parent; d != s.dir && !s.dirs[d]; d = filepath.Dir(d) {
			s.dirs[d] = true
		}
	}
	// CreateTemp: a new name, O_EXCL, mode 0600.
	f, err := os.CreateTemp(parent, tempPrefix+filepath.Base(full)+"-*")
	if err != nil {
		return nil, err
	}
	return &dirFile{f: f, final: full}, nil
}

// Existing opens a file already in the directory, for a resumed export.
func (s *DirSink) Existing(path string) (io.ReadCloser, error) {
	return os.Open(s.full(path)) //nolint:gosec // a format v1 path under the sink's directory
}

// Sync puts every file and directory written so far on disk.
func (s *DirSink) Sync() error { return syncTree(s.dir) }

// Close syncs the directory, renames it to out and syncs out's parent.
func (s *DirSink) Close() error {
	for _, p := range s.drop {
		if err := os.Remove(s.full(p)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if err := s.Sync(); err != nil {
		return err
	}
	if err := mustNotExist(s.out); err != nil {
		return err
	}
	if err := os.Rename(s.dir, s.out); err != nil {
		return err
	}
	return syncDir(filepath.Dir(s.out))
}

type dirFile struct {
	f     *os.File
	final string
}

func (d *dirFile) Write(p []byte) (int, error) { return d.f.Write(p) }

func (d *dirFile) Commit() error {
	if err := d.f.Close(); err != nil {
		_ = os.Remove(d.f.Name())
		return err
	}
	return os.Rename(d.f.Name(), d.final)
}

func (d *dirFile) Abort() error {
	_ = d.f.Close()
	return os.Remove(d.f.Name())
}

// syncTreeByFile syncs every file and directory under dir, deepest
// directories last within their parent: the fallback where syncfs is
// missing or fails.
func syncTreeByFile(dir string) error {
	var dirs []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, p)
			return nil
		}
		return syncPath(p)
	})
	if err != nil {
		return err
	}
	// Deepest first: a directory's entry in its parent comes last.
	slices.SortFunc(dirs, func(a, b string) int { return cmp.Or(cmp.Compare(len(b), len(a)), strings.Compare(a, b)) })
	for _, d := range dirs {
		if err := syncPath(d); err != nil {
			return err
		}
	}
	return nil
}

func syncDir(dir string) error { return syncPath(dir) }

func syncPath(p string) error {
	f, err := os.Open(p) //nolint:gosec // a path the sink wrote
	if err != nil {
		return err
	}
	err = f.Sync()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// ZipSink writes an export as a ZIP: Deflate at BestSpeed, zip64 where
// needed, every entry with mode 0600 and the same time, so that the same
// input gives the same bytes. archive/zip keeps about 300 bytes per entry
// in memory until Close (MaxZIPMessages).
type ZipSink struct {
	zw       *zip.Writer
	modified time.Time
}

// NewZipSink writes a ZIP to w. modified is every entry's time (the
// statement's creation time).
func NewZipSink(w io.Writer, modified time.Time) *ZipSink {
	zw := zip.NewWriter(w)
	zw.RegisterCompressor(zip.Deflate, func(w io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(w, flate.BestSpeed)
	})
	return &ZipSink{zw: zw, modified: modified.UTC().Truncate(time.Second)}
}

// Create implements Sink.
func (s *ZipSink) Create(path string) (SinkFile, error) {
	h := &zip.FileHeader{Name: path, Method: zip.Deflate, Modified: s.modified}
	h.SetMode(0o600)
	w, err := s.zw.CreateHeader(h)
	if err != nil {
		return nil, err
	}
	return zipFile{w}, nil
}

// Close writes the central directory. It does not close the underlying
// writer.
func (s *ZipSink) Close() error { return s.zw.Close() }

type zipFile struct{ io.Writer }

func (zipFile) Commit() error { return nil }
func (zipFile) Abort() error  { return ErrCannotUndo }
