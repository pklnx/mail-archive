package gobd

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// tempFiles lists the files under dir that a sink writes under a
// temporary name.
func tempFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(d.Name(), tempPrefix) {
			out = append(out, p)
		}
		return err
	})
	return out
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// The directory sink writes into <out>.partial, each file under a
// temporary name until it is committed, and gives out its name only when
// closed.
func TestDirSinkPartial(t *testing.T) {
	out := filepath.Join(t.TempDir(), "export")
	s, err := NewDirSink(out)
	if err != nil {
		t.Fatal(err)
	}
	partial := out + PartialSuffix
	if s.Dir() != partial || !exists(partial) || exists(out) {
		t.Fatalf("dir %s", s.Dir())
	}
	f, err := s.Create("mail/2025/01/a.eml")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte("half"))
	final := filepath.Join(partial, "mail", "2025", "01", "a.eml")
	if exists(final) || len(tempFiles(t, partial)) != 1 {
		t.Fatalf("before commit: final %v, temp %v", exists(final), tempFiles(t, partial))
	}
	if err := f.Commit(); err != nil {
		t.Fatal(err)
	}
	if !exists(final) || len(tempFiles(t, partial)) != 0 {
		t.Fatalf("after commit: final %v, temp %v", exists(final), tempFiles(t, partial))
	}
	g, _ := s.Create("README.txt")
	if err := g.Abort(); err != nil || exists(filepath.Join(partial, "README.txt")) || len(tempFiles(t, partial)) != 0 {
		t.Fatalf("abort: %v", err)
	}
	// A file of the directory, not of the export, goes before the rename.
	if err := os.WriteFile(filepath.Join(partial, ".state"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.DropOnClose(".state")

	// A second export into the same path is refused while one runs.
	if _, err := NewDirSink(out); err == nil || !strings.Contains(err.Error(), "interrupted export") {
		t.Errorf("second sink: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if exists(partial) || !exists(filepath.Join(out, "mail", "2025", "01", "a.eml")) || exists(filepath.Join(out, ".state")) {
		t.Fatal("after close")
	}
	if _, err := NewDirSink(out); err == nil {
		t.Error("a sink into an existing export")
	}
	if _, err := ResumeDirSink(filepath.Join(t.TempDir(), "none")); err == nil || !strings.Contains(err.Error(), "no interrupted export") {
		t.Errorf("resume without partial: %v", err)
	}
}

// A resumed sink replaces a file of an interrupted run on commit, and the
// writer keeps a file that is already right without writing it.
func TestResumeKeepFile(t *testing.T) {
	f := newFixture(t)
	out := filepath.Join(t.TempDir(), "export")
	s, err := NewDirSink(out)
	if err != nil {
		t.Fatal(err)
	}
	w, err := NewWriter(s, Options{Statement: f.st, Location: berlin(t)})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range f.msgs[:2] {
		if err := w.AddFile(m.m, bytes.NewReader(m.body)); err != nil {
			t.Fatal(err)
		}
	}
	// Interrupted here; the second file is damaged meanwhile.
	bad := filepath.Join(out+PartialSuffix, filepath.FromSlash(MessagePath(f.msgs[1].m.SHA256, f.msgs[1].m.SortAt, berlin(t))))
	if err := os.WriteFile(bad, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	rs, err := ResumeDirSink(out)
	if err != nil {
		t.Fatal(err)
	}
	st := f.st
	last := f.chain[len(f.chain)-1]
	st.AttestationSeq, st.AttestationHash, st.Resumed = last.Seq, last.Hash, true
	w, err = NewWriter(rs, Options{Statement: st, Location: berlin(t)})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for i, m := range f.msgs {
		kept, err := w.KeepFile(m.m)
		if err != nil {
			t.Fatal(err)
		}
		if kept != (i == 0) {
			t.Errorf("message %d kept %v", i, kept)
		}
		if !kept {
			if err := w.AddFile(m.m, bytes.NewReader(m.body)); err != nil {
				t.Fatal(err)
			}
		}
		paths = append(paths, MessagePath(m.m.SHA256, m.m.SortAt, berlin(t)))
	}
	for _, m := range f.msgs {
		if err := w.AddMessageRow(m.m); err != nil {
			t.Fatal(err)
		}
	}
	for _, l := range f.locs {
		if err := w.AddLocationRow(l); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.Finish(Final{Chain: f.chain[1:], Files: func(yield func(string) error) error {
		for _, p := range paths {
			if err := yield(p); err != nil {
				return err
			}
		}
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	res, err := Verify(out, VerifyOptions{})
	if err != nil || res.Findings[KindChanged] != 0 || res.Findings[KindManifest] != 0 {
		t.Errorf("verify: %+v, %v", res, err)
	}
}

// orderSink records the order of files and syncs.
type orderSink struct{ events []string }

func (o *orderSink) Create(path string) (SinkFile, error) {
	o.events = append(o.events, "create "+path)
	return nopFile{}, nil
}
func (o *orderSink) Close() error { return nil }
func (o *orderSink) Sync() error  { o.events = append(o.events, "sync"); return nil }

type nopFile struct{}

func (nopFile) Write(p []byte) (int, error) { return len(p), nil }
func (nopFile) Commit() error               { return nil }
func (nopFile) Abort() error                { return nil }

// Everything SHA256SUMS lists is synced before SHA256SUMS is written.
func TestWriterSyncsBeforeSums(t *testing.T) {
	f := newFixture(t)
	o := &orderSink{}
	w, err := NewWriter(o, Options{Statement: f.st, Location: berlin(t)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Finish(Final{}); err != nil {
		t.Fatal(err)
	}
	i, j := slices.Index(o.events, "sync"), slices.Index(o.events, "create "+SumsFile)
	if i < 0 || j < 0 || i > j || slices.Index(o.events, "create "+ReadmeFile) > i {
		t.Errorf("order: %v", o.events)
	}
}
