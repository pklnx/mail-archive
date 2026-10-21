package blobstore

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// recorder replaces the filesystem operations of a Store: it records them
// in order, fails chosen ones once and runs hooks before them.
type recorder struct {
	mu     sync.Mutex
	events []string
	fail   map[string]error             // "open:", "sync:", "close:", "link:", "unlink:" + path
	before map[string]func(path string) // same keys; runs before the operation
}

func newRecorder() *recorder {
	return &recorder{fail: map[string]error{}, before: map[string]func(string){}}
}

// do records an operation and returns its injected failure, if any.
func (r *recorder) do(op, path string) error {
	key := op + ":" + path
	r.mu.Lock()
	hook := r.before[key]
	r.mu.Unlock()
	if hook != nil {
		hook(path)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, key)
	err := r.fail[key]
	delete(r.fail, key)
	return err
}

func (r *recorder) failOnce(key string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fail[key] = err
}

func (r *recorder) onBefore(key string, fn func(string)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.before[key] = fn
}

// take returns the recorded events and starts a new list.
func (r *recorder) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	ev := r.events
	r.events = nil
	return ev
}

type recorded struct {
	r    *recorder
	path string
	f    syncCloser
}

func (h recorded) Sync() error {
	if err := h.r.do("sync", h.path); err != nil {
		return err
	}
	return h.f.Sync()
}

func (h recorded) Close() error {
	cerr := h.f.Close()
	if err := h.r.do("close", h.path); err != nil {
		return err
	}
	return cerr
}

func (r *recorder) ops() fsops {
	return fsops{
		open: func(name string) (syncCloser, error) {
			if err := r.do("open", name); err != nil {
				return nil, err
			}
			f, err := os.Open(name)
			if err != nil {
				return nil, err
			}
			return recorded{r: r, path: name, f: f}, nil
		},
		temp: func(f *os.File) syncCloser { return recorded{r: r, path: "TEMP", f: f} },
		link: func(oldname, newname string) error {
			if err := r.do("link", newname); err != nil {
				return err
			}
			return os.Link(oldname, newname)
		},
		unlink: func(name string) error {
			if err := r.do("unlink", name); err != nil {
				return err
			}
			return os.Remove(name)
		},
	}
}

// barrier is the events of a successful directory barrier.
func barrier(dirs ...string) []string {
	var out []string
	for _, d := range dirs {
		out = append(out, "open:"+d, "sync:"+d, "close:"+d)
	}
	return out
}

// physTempDir is a fresh directory with symlinks resolved.
func physTempDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// ancestors lists dir and its parents up to the filesystem root.
func ancestors(dir string) []string {
	out := []string{dir}
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			return out
		}
		dir = parent
		out = append(out, dir)
	}
}

func equal(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s:\n got  %q\n want %q", what, got, want)
	}
}

var errInjected = errors.New("injected failure")

func TestSyncDirFailures(t *testing.T) {
	dir := physTempDir(t)
	rec := newRecorder()
	s := &Store{root: dir, phys: dir, ops: rec.ops()}

	rec.failOnce("open:"+dir, errInjected)
	if err := s.syncDir(dir); !errors.Is(err, errInjected) {
		t.Errorf("open failure: %v", err)
	}
	equal(t, "open failure", rec.take(), []string{"open:" + dir})

	// A failed sync still closes; the sync error is the one returned.
	rec.failOnce("sync:"+dir, errInjected)
	rec.failOnce("close:"+dir, errors.New("close failure"))
	if err := s.syncDir(dir); !errors.Is(err, errInjected) || !strings.Contains(err.Error(), dir) {
		t.Errorf("sync failure: %v", err)
	}
	equal(t, "sync failure", rec.take(), barrier(dir))

	rec.failOnce("close:"+dir, errInjected)
	if err := s.syncDir(dir); !errors.Is(err, errInjected) {
		t.Errorf("close failure: %v", err)
	}
	equal(t, "close failure", rec.take(), barrier(dir))

	// Unsupported directory sync is no success.
	rec.failOnce("sync:"+dir, syscall.EINVAL)
	if err := s.syncDir(dir); !errors.Is(err, syscall.EINVAL) {
		t.Errorf("unsupported sync: %v", err)
	}
}

func TestNewSyncsAncestry(t *testing.T) {
	base := physTempDir(t)
	root := filepath.Join(base, "a", "b", "store")
	rec := newRecorder()
	want := append(barrier(filepath.Join(root, "messages"), filepath.Join(root, "tmp")), barrier(ancestors(root)...)...)

	// An ancestor fails: New fails and names it.
	a := filepath.Join(base, "a")
	rec.failOnce("sync:"+a, errInjected)
	_, err := newStore(root, rec.ops())
	var ae *AncestorError
	if !errors.As(err, &ae) || ae.Dir != a || !errors.Is(err, errInjected) {
		t.Fatalf("failed ancestor: %v", err)
	}
	ev := rec.take()
	equal(t, "up to the failure", ev, want[:slices.Index(want, "close:"+a)+1])

	// The retry finds every directory and still synchronizes all of them,
	// through the filesystem root.
	s, err := newStore(root, rec.ops())
	if err != nil {
		t.Fatal(err)
	}
	equal(t, "retry", rec.take(), want)
	if s.Root() != root {
		t.Errorf("Root() = %q", s.Root())
	}

	// The store root and its directories are not ancestors.
	rec.failOnce("sync:"+filepath.Join(root, "tmp"), errInjected)
	if _, err := newStore(root, rec.ops()); !errors.Is(err, errInjected) || errors.As(err, &ae) {
		t.Errorf("tmp failure: %v", err)
	}
}

func TestNewResolvesSymlinks(t *testing.T) {
	base := physTempDir(t)
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	// A relative root below a symlinked ancestor.
	t.Chdir(base)
	root := filepath.Join("link", "store")
	rec := newRecorder()
	s, err := newStore(root, rec.ops())
	if err != nil {
		t.Fatal(err)
	}
	phys := filepath.Join(real, "store")
	want := append(barrier(filepath.Join(phys, "messages"), filepath.Join(phys, "tmp")), barrier(ancestors(phys)...)...)
	equal(t, "physical barrier", rec.take(), want)
	if s.Root() != root {
		t.Errorf("Root() = %q, want the configured %q", s.Root(), root)
	}

	// Put and Remove synchronize the physical shard directories too.
	blob, _, err := s.Put(strings.NewReader("symlinked"))
	if err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(phys, "messages", blob.SHA256[:2], blob.SHA256[2:4])
	ev := rec.take()
	equal(t, "put barrier", ev[len(ev)-9:], barrier(leaf, filepath.Dir(leaf), filepath.Join(phys, "messages")))
	if err := s.Remove(blob.SHA256); err != nil {
		t.Fatal(err)
	}
	ev = rec.take()
	equal(t, "remove barrier", ev[1:], barrier(leaf))
}

// newRecorded opens a store whose operations are recorded, with the
// initialization events dropped.
func newRecorded(t *testing.T) (*Store, *recorder, string) {
	t.Helper()
	root := physTempDir(t)
	rec := newRecorder()
	s, err := newStore(root, rec.ops())
	if err != nil {
		t.Fatal(err)
	}
	rec.take()
	return s, rec, root
}

func shardDirs(root, hash string) []string {
	leaf := filepath.Join(root, "messages", hash[:2], hash[2:4])
	return []string{leaf, filepath.Dir(leaf), filepath.Join(root, "messages")}
}

func assertContent(t *testing.T, s *Store, blob Blob, want string) {
	t.Helper()
	got, err := os.ReadFile(s.abs(blob.Path))
	if err != nil || string(got) != want {
		t.Errorf("content %q, %v; want %q", got, err, want)
	}
}

func assertNoTemp(t *testing.T, root string) {
	t.Helper()
	if entries, _ := os.ReadDir(filepath.Join(root, "tmp")); len(entries) != 0 {
		t.Errorf("temp files left: %v", entries)
	}
}

func TestPutBarrierOrder(t *testing.T) {
	s, rec, root := newRecorded(t)
	content := "Subject: order\r\n\r\nbody\r\n"
	blob, created, err := s.Put(strings.NewReader(content))
	if err != nil || !created {
		t.Fatalf("put: %v created=%v", err, created)
	}
	final := filepath.Join(root, filepath.FromSlash(blob.Path))
	want := append([]string{"sync:TEMP", "close:TEMP", "link:" + final}, barrier(shardDirs(root, blob.SHA256)...)...)
	equal(t, "new blob", rec.take(), want)

	// A duplicate synchronizes the existing file and its directories; its
	// own copy is neither synchronized nor linked.
	if _, created, err := s.Put(strings.NewReader(content)); err != nil || created {
		t.Fatalf("duplicate: %v created=%v", err, created)
	}
	want = append(barrier(final), barrier(shardDirs(root, blob.SHA256)...)...)
	equal(t, "duplicate", rec.take(), want)
	assertContent(t, s, blob, content)
	assertNoTemp(t, root)
}

func TestPutFailures(t *testing.T) {
	const content = "Subject: fail\r\n\r\nbody\r\n"
	probe, _, _ := newRecorded(t)
	b, _, err := probe.Put(strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	hash := b.SHA256
	for _, tc := range []struct {
		name      string
		existing  bool                            // the file is there before the failing Put
		key       func(root, final string) string // the operation that fails
		published bool                            // the final file stays after the failure
	}{
		{"temp sync", false, func(string, string) string { return "sync:TEMP" }, false},
		{"temp close", false, func(string, string) string { return "close:TEMP" }, false},
		{"link", false, func(_, final string) string { return "link:" + final }, false},
		{"leaf after publication", false, func(root, _ string) string { return "sync:" + shardDirs(root, hash)[0] }, true},
		{"messages after publication", false, func(root, _ string) string { return "close:" + shardDirs(root, hash)[2] }, true},
		{"existing file sync", true, func(_, final string) string { return "sync:" + final }, true},
		{"existing file close", true, func(_, final string) string { return "close:" + final }, true},
		{"existing shard", true, func(root, _ string) string { return "sync:" + shardDirs(root, hash)[1] }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, rec, root := newRecorded(t)
			final := filepath.Join(root, filepath.FromSlash(RelPath(hash)))
			if tc.existing {
				if _, _, err := s.Put(strings.NewReader(content)); err != nil {
					t.Fatal(err)
				}
			}
			rec.failOnce(tc.key(root, final), errInjected)
			if _, _, err := s.Put(strings.NewReader(content)); !errors.Is(err, errInjected) {
				t.Fatalf("put: %v", err)
			}
			if _, err := os.Lstat(final); (err == nil) != tc.published {
				t.Fatalf("final file after the failure: %v, want published=%v", err, tc.published)
			}
			assertNoTemp(t, root)
			rec.take()

			// The retry succeeds and completes the barrier.
			blob, created, err := s.Put(strings.NewReader(content))
			if err != nil || created != !tc.published {
				t.Fatalf("retry: %v created=%v", err, created)
			}
			ev := rec.take()
			equal(t, "retry barrier", ev[len(ev)-9:], barrier(shardDirs(root, hash)...))
			if tc.published {
				equal(t, "retry syncs the existing file", ev[:3], barrier(final))
			}
			assertContent(t, s, blob, content)
			assertNoTemp(t, root)
		})
	}
}

// A sweep or verify --fix-orphans removes the existing file between the
// lookup and its barrier (SMTP puts hold no blob lock): Put publishes its
// own copy instead of failing.
func TestPutExistingFileVanishes(t *testing.T) {
	s, rec, root := newRecorded(t)
	content := "Subject: vanish\r\n\r\nbody\r\n"
	blob, _, err := s.Put(strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	final := filepath.Join(root, filepath.FromSlash(blob.Path))
	before, err := os.Stat(final)
	if err != nil {
		t.Fatal(err)
	}
	removed := false
	rec.onBefore("open:"+final, func(p string) {
		if !removed {
			removed = true
			if err := os.Remove(p); err != nil {
				t.Error(err)
			}
		}
	})
	rec.take()
	got, created, err := s.Put(strings.NewReader(content))
	if err != nil || !created || got != blob {
		t.Fatalf("put: %+v created=%v %v", got, created, err)
	}
	want := append([]string{"open:" + final, "sync:TEMP", "close:TEMP", "link:" + final}, barrier(shardDirs(root, blob.SHA256)...)...)
	equal(t, "fallback", rec.take(), want)
	after, err := os.Stat(final)
	if err != nil || os.SameFile(before, after) {
		t.Fatalf("the file is not this call's copy: %v", err)
	}
	assertContent(t, s, blob, content)
	assertNoTemp(t, root)
}

func TestPutUnsupportedLink(t *testing.T) {
	s, rec, root := newRecorded(t)
	b, _, _ := newRecorded(t)
	blob, _, _ := b.Put(strings.NewReader("x"))
	rec.failOnce("link:"+filepath.Join(root, filepath.FromSlash(blob.Path)), syscall.EPERM)
	if _, _, err := s.Put(strings.NewReader("x")); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("put without hard links: %v", err)
	}
	if s.Exists(blob.SHA256) {
		t.Error("published without a link")
	}
}

// Two Store instances share a root and store the same message at once.
// B gets EEXIST from its link while A, the publisher, is held inside its
// directory barrier. B must not replace A's file; it synchronizes it
// itself, fails once in its own barrier, retries and returns before A.
func TestPutConcurrentInstances(t *testing.T) {
	root := physTempDir(t)
	recA, recB := newRecorder(), newRecorder()
	a, err := newStore(root, recA.ops())
	if err != nil {
		t.Fatal(err)
	}
	b, err := newStore(root, recB.ops())
	if err != nil {
		t.Fatal(err)
	}
	recA.take()
	recB.take()
	content := "Subject: race\r\n\r\nsame bytes\r\n"
	probe, _, _ := newRecorded(t)
	pb, _, _ := probe.Put(strings.NewReader(content))
	hash := pb.SHA256
	final := filepath.Join(root, filepath.FromSlash(RelPath(hash)))
	leaf := shardDirs(root, hash)[0]

	bAtLink, aPublished, releaseA := make(chan struct{}), make(chan struct{}), make(chan struct{})
	recB.onBefore("link:"+final, func(string) {
		close(bAtLink)
		<-aPublished
	})
	var published os.FileInfo
	recA.onBefore("open:"+leaf, func(string) {
		var err error
		if published, err = os.Stat(final); err != nil {
			t.Error(err)
		}
		close(aPublished)
		<-releaseA
	})
	recB.failOnce("sync:"+shardDirs(root, hash)[2], errInjected)

	type result struct {
		blob    Blob
		created bool
		err     error
	}
	resA, resB := make(chan result, 1), make(chan result, 1)
	go func() {
		blob, created, err := b.Put(strings.NewReader(content))
		resB <- result{blob, created, err}
	}()
	<-bAtLink
	go func() {
		blob, created, err := a.Put(strings.NewReader(content))
		resA <- result{blob, created, err}
	}()

	// B fails in its own barrier: no success, and A's file stays.
	rb := <-resB
	if !errors.Is(rb.err, errInjected) {
		t.Fatalf("B's first put: %+v", rb)
	}
	want := []string{"sync:TEMP", "close:TEMP", "link:" + final}
	want = append(want, barrier(final)...)
	want = append(want, barrier(shardDirs(root, hash)...)...) // the last sync fails, the close follows
	equal(t, "B", recB.take(), want)
	// B retries while A is still inside its barrier.
	blob, created, err := b.Put(strings.NewReader(content))
	if err != nil || created {
		t.Fatalf("B's retry: %v created=%v", err, created)
	}
	equal(t, "B's retry", recB.take(), append(barrier(final), barrier(shardDirs(root, hash)...)...))
	select {
	case r := <-resA:
		t.Fatalf("A returned before its barrier: %+v", r)
	default:
	}
	close(releaseA)
	ra := <-resA
	if ra.err != nil || !ra.created || ra.blob != blob {
		t.Fatalf("A: %+v", ra)
	}
	evA := recA.take()
	equal(t, "A", evA, append([]string{"sync:TEMP", "close:TEMP", "link:" + final}, barrier(shardDirs(root, hash)...)...))
	now, err := os.Stat(final)
	if err != nil || !os.SameFile(published, now) {
		t.Fatalf("A's file was replaced: %v", err)
	}
	got, _ := os.ReadFile(final)
	if !bytes.Equal(got, []byte(content)) {
		t.Errorf("content %q", got)
	}
	assertNoTemp(t, root)
}

func TestRemoveDurable(t *testing.T) {
	put := func(t *testing.T, s *Store) string {
		t.Helper()
		blob, _, err := s.Put(strings.NewReader("Subject: remove\r\n\r\nbody\r\n"))
		if err != nil {
			t.Fatal(err)
		}
		return blob.SHA256
	}
	t.Run("existing file", func(t *testing.T) {
		s, rec, root := newRecorded(t)
		hash := put(t, s)
		rec.take()
		if err := s.Remove(hash); err != nil {
			t.Fatal(err)
		}
		dirs := shardDirs(root, hash)
		equal(t, "remove", rec.take(), append([]string{"unlink:" + s.abs(RelPath(hash))}, barrier(dirs[0])...))
		if s.Exists(hash) {
			t.Error("file still there")
		}
	})
	t.Run("missing file", func(t *testing.T) {
		s, rec, root := newRecorded(t)
		hash := put(t, s)
		if err := os.Remove(s.abs(RelPath(hash))); err != nil {
			t.Fatal(err)
		}
		rec.take()
		if err := s.Remove(hash); err != nil {
			t.Fatal(err)
		}
		equal(t, "remove", rec.take(), append([]string{"unlink:" + s.abs(RelPath(hash))}, barrier(shardDirs(root, hash)[0])...))
	})
	t.Run("missing leaf shard", func(t *testing.T) {
		s, rec, root := newRecorded(t)
		hash := put(t, s)
		dirs := shardDirs(root, hash)
		if err := os.RemoveAll(dirs[0]); err != nil {
			t.Fatal(err)
		}
		rec.take()
		if err := s.Remove(hash); err != nil {
			t.Fatal(err)
		}
		equal(t, "remove", rec.take(), append([]string{"unlink:" + s.abs(RelPath(hash)), "open:" + dirs[0]},
			barrier(dirs[1], dirs[2], root)...))
		if _, err := os.Stat(dirs[0]); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Remove created the shard: %v", err)
		}
	})
	t.Run("missing intermediate shard", func(t *testing.T) {
		s, rec, root := newRecorded(t)
		hash := put(t, s)
		dirs := shardDirs(root, hash)
		if err := os.RemoveAll(dirs[1]); err != nil {
			t.Fatal(err)
		}
		rec.take()
		if err := s.Remove(hash); err != nil {
			t.Fatal(err)
		}
		equal(t, "remove", rec.take(), append([]string{"unlink:" + s.abs(RelPath(hash)), "open:" + dirs[0], "open:" + dirs[1]},
			barrier(dirs[2], root)...))
	})
	t.Run("unrelated failures", func(t *testing.T) {
		s, rec, root := newRecorded(t)
		hash := put(t, s)
		final := s.abs(RelPath(hash))
		rec.failOnce("unlink:"+final, syscall.EIO)
		if err := s.Remove(hash); !errors.Is(err, syscall.EIO) {
			t.Errorf("unlink failure: %v", err)
		}
		if !s.Exists(hash) {
			t.Error("file gone")
		}
		// A shard that cannot be opened is no missing shard.
		rec.take()
		rec.failOnce("open:"+shardDirs(root, hash)[0], fs.ErrPermission)
		if err := s.Remove(hash); !errors.Is(err, fs.ErrPermission) {
			t.Errorf("open failure: %v", err)
		}
		equal(t, "no walk up", rec.take(), []string{"unlink:" + final, "open:" + shardDirs(root, hash)[0]})
	})
	t.Run("sync failure after unlink", func(t *testing.T) {
		s, rec, root := newRecorded(t)
		hash := put(t, s)
		leaf := shardDirs(root, hash)[0]
		rec.failOnce("sync:"+leaf, errInjected)
		if err := s.Remove(hash); !errors.Is(err, errInjected) {
			t.Fatalf("remove: %v", err)
		}
		if s.Exists(hash) {
			t.Fatal("the unlink did not happen")
		}
		rec.take()
		// The retry finds the file gone and still synchronizes.
		if err := s.Remove(hash); err != nil {
			t.Fatal(err)
		}
		equal(t, "retry", rec.take(), append([]string{"unlink:" + s.abs(RelPath(hash))}, barrier(leaf)...))
	})
}
