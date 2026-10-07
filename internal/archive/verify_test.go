package archive_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// archiveEnv is a database and blob store with one user, account and folder.
type archiveEnv struct {
	t      *testing.T
	ctx    context.Context
	st     *store.Store
	blobs  *blobstore.Store
	data   string
	user   *store.User
	acc    *store.Account
	folder *store.Folder
}

func newArchiveEnv(t *testing.T) *archiveEnv {
	t.Helper()
	e := &archiveEnv{t: t, ctx: context.Background(), st: storetest.New(t), data: t.TempDir()}
	var err error
	if e.blobs, err = blobstore.New(e.data); err != nil {
		t.Fatal(err)
	}
	if e.user, err = e.st.CreateUser(e.ctx, "anna", "h", false); err != nil {
		t.Fatal(err)
	}
	e.acc = e.account(e.user, "mail")
	e.folder = e.folderOf(e.acc, "INBOX", 1)
	return e
}

func (e *archiveEnv) account(owner *store.User, name string) *store.Account {
	e.t.Helper()
	a := &store.Account{Name: name, Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}}
	if owner != nil {
		a.OwnerID = &owner.ID
	}
	if err := e.st.CreateAccount(e.ctx, a); err != nil {
		e.t.Fatal(err)
	}
	return a
}

func (e *archiveEnv) folderOf(a *store.Account, name string, uidValidity uint32) *store.Folder {
	e.t.Helper()
	f, err := e.st.GetOrCreateFolder(e.ctx, a.ID, name)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.ResetFolder(e.ctx, f.ID, uidValidity); err != nil {
		e.t.Fatal(err)
	}
	f.UIDValidity = uidValidity
	return f
}

// save stores raw as a blob with a row and a location in f.
func (e *archiveEnv) save(f *store.Folder, uid uint32, raw string, flags ...string) blobstore.Blob {
	e.t.Helper()
	b, _, err := e.blobs.Put(strings.NewReader(raw))
	if err != nil {
		e.t.Fatal(err)
	}
	e.saveRow(f, uid, store.MessageMeta{SHA256: b.SHA256, Size: b.Size, StoredPath: b.Path}, flags...)
	return b
}

func (e *archiveEnv) saveRow(f *store.Folder, uid uint32, meta store.MessageMeta, flags ...string) {
	e.t.Helper()
	loc := store.Location{FolderID: f.ID, UIDValidity: f.UIDValidity, UID: uid, Flags: flags,
		InternalDate: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).Add(time.Duration(uid) * time.Hour)}
	if _, err := e.st.SaveBatch(e.ctx, f.ID, uid, []store.MessageMeta{meta}, []store.Location{loc}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *archiveEnv) path(rel string) string { return filepath.Join(e.data, filepath.FromSlash(rel)) }

func (e *archiveEnv) write(rel, content string) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Dir(e.path(rel)), 0o700); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(e.path(rel), []byte(content), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func (e *archiveEnv) verify(opts archive.VerifyOptions) (*archive.VerifyResult, map[string][]archive.Finding) {
	e.t.Helper()
	found := map[string][]archive.Finding{}
	opts.OnFinding = func(f archive.Finding) { found[f.Kind] = append(found[f.Kind], f) }
	opts.Logger = quiet
	res, err := archive.Verify(e.ctx, e.st, e.blobs, opts)
	if err != nil {
		e.t.Fatal(err)
	}
	return res, found
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func msg(n int) string {
	return "Subject: message " + strings.Repeat("x", n) + "\r\n\r\nFrom the body\r\n"
}

func TestVerifyClean(t *testing.T) {
	e := newArchiveEnv(t)
	e.save(e.folder, 1, msg(1))
	e.save(e.folder, 2, msg(2))
	res, found := e.verify(archive.VerifyOptions{Jobs: 1})
	if res.Checked != 2 || res.Problems() != 0 || !res.Complete() || len(found) != 0 {
		t.Fatalf("result %+v, findings %v", res, found)
	}
}

func TestVerifyFindings(t *testing.T) {
	e := newArchiveEnv(t)
	missing := e.save(e.folder, 1, msg(1))
	corrupt := e.save(e.folder, 2, msg(2))
	ok := e.save(e.folder, 3, msg(3))
	if err := os.Remove(e.path(missing.Path)); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(e.path(corrupt.Path))
	data[5] ^= 1
	if err := os.WriteFile(e.path(corrupt.Path), data, 0o600); err != nil {
		t.Fatal(err)
	}
	// A row with the wrong size, and one with a wrong stored path.
	sized, _, _ := e.blobs.Put(strings.NewReader(msg(4)))
	e.saveRow(e.folder, 4, store.MessageMeta{SHA256: sized.SHA256, Size: sized.Size + 1, StoredPath: sized.Path})
	moved, _, _ := e.blobs.Put(strings.NewReader(msg(5)))
	e.saveRow(e.folder, 5, store.MessageMeta{SHA256: moved.SHA256, Size: moved.Size, StoredPath: "../elsewhere.eml"})
	// Files without rows.
	orphan, _, _ := e.blobs.Put(strings.NewReader(msg(6)))
	e.write("tmp/put-123", "half a message")
	e.write("messages/ab/notes.txt", "x")

	res, found := e.verify(archive.VerifyOptions{})
	if res.Checked != 5 || !res.Complete() {
		t.Fatalf("result %+v", res)
	}
	want := map[string]int{
		archive.FindingMissing: 1, archive.FindingCorrupt: 2, archive.FindingBadPath: 1,
		archive.FindingOrphan: 1, archive.FindingStaleTemp: 1, archive.FindingUnexpected: 1,
	}
	for kind, n := range want {
		if len(found[kind]) != n || res.Findings[kind] != n {
			t.Errorf("%s: %d findings (%d counted), want %d: %+v", kind, len(found[kind]), res.Findings[kind], n, found[kind])
		}
	}
	if res.Problems() != 7 {
		t.Errorf("problems = %d", res.Problems())
	}
	m := found[archive.FindingMissing][0]
	if m.SHA256 != missing.SHA256 || len(m.Places) != 1 || m.Places[0].Owner != "anna" || m.Places[0].Account != "mail" ||
		m.Places[0].Folder != "INBOX" || m.Places[0].UID != 1 {
		t.Errorf("missing: %+v", m)
	}
	for _, c := range found[archive.FindingCorrupt] {
		if c.SHA256 != corrupt.SHA256 && c.SHA256 != sized.SHA256 || len(c.Places) != 1 {
			t.Errorf("corrupt: %+v", c)
		}
	}
	if b := found[archive.FindingBadPath][0]; b.SHA256 != moved.SHA256 || b.Path != "../elsewhere.eml" {
		t.Errorf("bad path: %+v", b)
	}
	if o := found[archive.FindingOrphan][0]; o.SHA256 != orphan.SHA256 || o.Path != orphan.Path {
		t.Errorf("orphan: %+v", o)
	}
	// verify changes nothing.
	for _, p := range []string{ok.Path, orphan.Path, "tmp/put-123", "messages/ab/notes.txt"} {
		if _, err := os.Stat(e.path(p)); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
}

func TestVerifyFixOrphansMovesOnlyOrphans(t *testing.T) {
	e := newArchiveEnv(t)
	kept := e.save(e.folder, 1, msg(1))
	before, _ := os.ReadFile(e.path(kept.Path))
	orphan, _, _ := e.blobs.Put(strings.NewReader(msg(2)))
	e.write("tmp/put-1", "half")
	e.write("messages/ab/notes.txt", "x")

	now := time.Date(2026, 10, 7, 19, 1, 2, 0, time.UTC)
	res, _ := e.verify(archive.VerifyOptions{FixOrphans: true, Now: func() time.Time { return now }})
	if res.Moved != 2 || res.MovedBytes != orphan.Size+4 || res.MovedTo != "orphans/20261007T190102Z" {
		t.Fatalf("result %+v", res)
	}
	for _, p := range []string{orphan.Path, "tmp/put-1"} {
		if _, err := os.Stat(e.path(p)); !os.IsNotExist(err) {
			t.Errorf("%s still there: %v", p, err)
		}
		if _, err := os.Stat(e.path(res.MovedTo + "/" + p)); err != nil {
			t.Errorf("%s not moved: %v", p, err)
		}
	}
	after, _ := os.ReadFile(e.path(kept.Path))
	if !bytes.Equal(before, after) {
		t.Error("referenced file changed")
	}
	if _, err := os.Stat(e.path("messages/ab/notes.txt")); err != nil {
		t.Errorf("unexpected file was touched: %v", err)
	}

	// The next run finds no orphans and points to the moved files.
	res, found := e.verify(archive.VerifyOptions{})
	if len(found[archive.FindingOrphan]) != 0 || len(res.OldOrphanDirs) != 1 || res.OldOrphanDirs[0] != "orphans/20261007T190102Z" {
		t.Errorf("second run: %+v %v", res, found)
	}
}

// While a sync writes blobs, verify cannot tell orphans from blobs whose
// rows are not committed yet: it skips that phase and moves nothing.
func TestVerifySkipsOrphansWhileSyncing(t *testing.T) {
	e := newArchiveEnv(t)
	unlock, ok, err := e.st.TryLockSyncForWrite(e.ctx, e.acc.ID)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	defer unlock()
	pending, _, _ := e.blobs.Put(strings.NewReader(msg(1)))
	res, found := e.verify(archive.VerifyOptions{FixOrphans: true, LockTimeout: 200 * time.Millisecond})
	if !res.OrphansSkipped || res.Complete() || res.Moved != 0 || len(found[archive.FindingOrphan]) != 0 {
		t.Fatalf("result %+v %v", res, found)
	}
	if _, err := os.Stat(e.path(pending.Path)); err != nil {
		t.Fatal(err)
	}
}

// A row that appears between the check and the move keeps the file.
func TestVerifyKeepsFileThatGotARow(t *testing.T) {
	e := newArchiveEnv(t)
	b, _, _ := e.blobs.Put(strings.NewReader(msg(1)))
	opts := archive.VerifyOptions{FixOrphans: true}
	archive.SetBeforeMove(&opts, func() {
		e.saveRow(e.folder, 1, store.MessageMeta{SHA256: b.SHA256, Size: b.Size, StoredPath: b.Path})
	})
	res, found := e.verify(opts)
	if len(found[archive.FindingOrphan]) != 1 || res.Moved != 0 {
		t.Fatalf("result %+v %v", res, found)
	}
	if _, err := os.Stat(e.path(b.Path)); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyDatabaseErrorIsAnError(t *testing.T) {
	e := newArchiveEnv(t)
	e.st.Close()
	if _, err := archive.Verify(e.ctx, e.st, e.blobs, archive.VerifyOptions{Logger: quiet}); err == nil {
		t.Fatal("no error with a closed database")
	}
}

// A sync that starts while verify holds the blob lock waits, then commits
// its messages, which verify then finds intact.
func TestSyncWaitsForVerify(t *testing.T) {
	alice := imapmemserver.NewUser("alice", "pw-a")
	createMailboxes(t, alice, "INBOX")
	appendMsg(t, alice, "INBOX", rawMessage("a1", "one"))
	appendMsg(t, alice, "INBOX", rawMessage("a2", "two"))
	f := newFixture(t, alice)
	f.addAccount("alice", "alice", "pw-a")
	acc, err := f.account("alice")
	if err != nil {
		t.Fatal(err)
	}

	lockRelease, ok, err := f.store.TryLockBlobs(f.ctx)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	// Release on failure too: the store does not close with a held lock.
	unlock := sync.OnceFunc(lockRelease)
	defer unlock()
	done := make(chan archive.AccountResult, 1)
	go func() { done <- f.syncer.SyncAccount(f.ctx, acc) }()
	select {
	case r := <-done:
		t.Fatalf("sync ran while the blob lock was held: %+v", r)
	case <-time.After(300 * time.Millisecond):
	}
	if n := f.countBlobFiles(); n != 0 {
		t.Fatalf("%d blobs written while the blob lock was held", n)
	}
	unlock()
	expectResult(t, <-done, 2, 2)

	res, err := archive.Verify(f.ctx, f.store, f.blobs, archive.VerifyOptions{Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	if res.Checked != 2 || res.Problems() != 0 || !res.Complete() {
		t.Fatalf("result %+v", res)
	}
}
