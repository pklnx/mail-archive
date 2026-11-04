package gobd

import (
	"archive/zip"
	"cmp"
	"compress/flate"
	"errors"
	"io"
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

// DirSink writes an export as a directory tree: directories with mode
// 0700, files with 0600, each synced to disk when committed and the
// directories when the sink is closed.
type DirSink struct {
	dir  string
	dirs map[string]bool
}

// NewDirSink creates dir, which must not exist, and returns a sink into
// it.
func NewDirSink(dir string) (*DirSink, error) {
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, err
	}
	return &DirSink{dir: dir, dirs: map[string]bool{dir: true}}, nil
}

// Create implements Sink.
func (s *DirSink) Create(path string) (SinkFile, error) {
	full := filepath.Join(s.dir, filepath.FromSlash(path))
	parent := filepath.Dir(full)
	if !s.dirs[parent] {
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return nil, err
		}
		for d := parent; d != s.dir && !s.dirs[d]; d = filepath.Dir(d) {
			s.dirs[d] = true
		}
	}
	f, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // path built from format v1 names under the sink's directory
	if err != nil {
		return nil, err
	}
	return &dirFile{f: f}, nil
}

// Close syncs every directory the sink created, deepest first, so that
// the files' names are on disk too.
func (s *DirSink) Close() error {
	dirs := make([]string, 0, len(s.dirs))
	for d := range s.dirs {
		dirs = append(dirs, d)
	}
	// Deepest first: a directory's entry in its parent comes last.
	slices.SortFunc(dirs, func(a, b string) int { return cmp.Or(cmp.Compare(len(b), len(a)), strings.Compare(a, b)) })
	for _, d := range dirs {
		if err := syncDir(d); err != nil {
			return err
		}
	}
	return syncDir(filepath.Dir(s.dir))
}

type dirFile struct{ f *os.File }

func (d *dirFile) Write(p []byte) (int, error) { return d.f.Write(p) }

func (d *dirFile) Commit() error {
	if err := d.f.Sync(); err != nil {
		_ = d.f.Close()
		return err
	}
	return d.f.Close()
}

func (d *dirFile) Abort() error {
	_ = d.f.Close()
	return os.Remove(d.f.Name())
}

func syncDir(dir string) error {
	f, err := os.Open(dir) //nolint:gosec // a directory the sink created
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
