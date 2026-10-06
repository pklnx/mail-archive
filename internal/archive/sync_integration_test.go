package archive_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/imapsync"
	"github.com/pklnx/mail-archive/internal/imaptest"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

func rawMessage(id, subject string) []byte {
	return fmt.Appendf(nil, "Message-ID: <%s@example.com>\r\nFrom: sender@example.com\r\n"+
		"Subject: %s\r\nDate: Mon, 5 Oct 2026 10:00:00 +0000\r\n\r\nBody of %s\r\n", id, subject, id)
}

func appendMsg(t *testing.T, u *imapmemserver.User, mailbox string, raw []byte) {
	t.Helper()
	imaptest.Append(t, u, mailbox, raw)
}

func createMailboxes(t *testing.T, u *imapmemserver.User, names ...string) {
	t.Helper()
	imaptest.CreateMailboxes(t, u, names...)
}

type fixture struct {
	t       *testing.T
	ctx     context.Context
	store   *store.Store
	blobs   *blobstore.Store
	dataDir string
	sealer  *crypto.Sealer
	syncer  *archive.Syncer
	host    string
	port    int
}

func newFixture(t *testing.T, users ...*imapmemserver.User) *fixture {
	st := storetest.New(t)
	dataDir := t.TempDir()
	blobs, err := blobstore.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	keyStr, _ := crypto.GenerateKey()
	key, _ := crypto.ParseKey(keyStr)
	sealer, err := crypto.NewSealer(key)
	if err != nil {
		t.Fatal(err)
	}
	host, port := imaptest.Start(t, users...)
	return &fixture{
		t: t, ctx: context.Background(), store: st, blobs: blobs, dataDir: dataDir, sealer: sealer,
		host: host, port: port,
		syncer: &archive.Syncer{
			Store: st, Blobs: blobs, Sealer: sealer, BatchSize: 2,
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
	}
}

func (f *fixture) addAccount(name, user, password string, excluded ...string) {
	f.t.Helper()
	acc := &store.Account{
		Name: name, Host: f.host, Port: f.port, TLSMode: store.TLSModeNone,
		Username: user, ExcludedFolders: excluded, Enabled: true,
	}
	seal := func(id int64) ([]byte, error) { return archive.SealPassword(f.sealer, id, password) }
	if err := f.store.CreateAccountSealed(f.ctx, acc, seal); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) sync() map[string]archive.AccountResult {
	f.t.Helper()
	results, err := f.syncer.SyncAll(f.ctx, nil, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	out := map[string]archive.AccountResult{}
	for _, r := range results {
		out[r.Account] = r
	}
	return out
}

func (f *fixture) stats() (locations map[string]int64, unique int64) {
	f.t.Helper()
	stats, unique, err := f.store.Stats(f.ctx, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	locations = map[string]int64{}
	for _, s := range stats {
		locations[s.Account] = s.Locations
	}
	return locations, unique
}

func (f *fixture) countBlobFiles() int {
	n := 0
	_ = filepath.WalkDir(filepath.Join(f.dataDir, "messages"), func(_ string, d os.DirEntry, _ error) error {
		if d != nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

func expectResult(t *testing.T, r archive.AccountResult, fetched, added int) {
	t.Helper()
	if r.Err != nil {
		t.Fatalf("%s: unexpected error: %v", r.Account, r.Err)
	}
	if r.Fetched != fetched || r.New != added {
		t.Fatalf("%s: fetched=%d new=%d, want fetched=%d new=%d", r.Account, r.Fetched, r.New, fetched, added)
	}
}

func TestSyncMergesAndDeduplicates(t *testing.T) {
	alice := imapmemserver.NewUser("alice", "pw-a")
	createMailboxes(t, alice, "INBOX", "Archive")
	bob := imapmemserver.NewUser("bob", "pw-b")
	createMailboxes(t, bob, "INBOX", "Junk")

	shared := rawMessage("shared", "Sent to both")
	appendMsg(t, alice, "INBOX", shared)
	appendMsg(t, alice, "INBOX", rawMessage("a1", "Alice only"))
	appendMsg(t, alice, "Archive", rawMessage("a2", "Archived"))
	appendMsg(t, bob, "INBOX", shared)
	appendMsg(t, bob, "INBOX", rawMessage("b1", "Bob only"))
	appendMsg(t, bob, "Junk", rawMessage("spam", "Spam"))

	f := newFixture(t, alice, bob)
	f.addAccount("alice", "alice", "pw-a")
	f.addAccount("bob", "bob", "pw-b", "junk") // exclusion is case-insensitive

	// First run: everything is new; the shared message is stored once.
	res := f.sync()
	expectResult(t, res["alice"], 3, 3)
	expectResult(t, res["bob"], 2, 1)
	locs, unique := f.stats()
	if unique != 4 || locs["alice"] != 3 || locs["bob"] != 2 {
		t.Fatalf("unique=%d locations=%v", unique, locs)
	}
	if n := f.countBlobFiles(); n != 4 {
		t.Fatalf("blob files = %d, want 4", n)
	}

	// Second run: nothing new.
	res = f.sync()
	expectResult(t, res["alice"], 0, 0)
	expectResult(t, res["bob"], 0, 0)

	// Incremental: only the new message is fetched.
	appendMsg(t, alice, "INBOX", rawMessage("a3", "Later"))
	res = f.sync()
	expectResult(t, res["alice"], 1, 1)
	expectResult(t, res["bob"], 0, 0)

	// UIDVALIDITY change: the folder is rescanned, content is deduplicated.
	if err := alice.Delete("Archive"); err != nil {
		t.Fatal(err)
	}
	createMailboxes(t, alice, "Archive")
	appendMsg(t, alice, "Archive", rawMessage("a2", "Archived"))
	res = f.sync()
	expectResult(t, res["alice"], 1, 0)
	locs, unique = f.stats()
	if unique != 5 || locs["alice"] != 5 {
		t.Fatalf("after rescan: unique=%d locations=%v", unique, locs)
	}

	// Metadata was parsed from the stored message.
	sum := sha256.Sum256(shared)
	meta, err := f.store.GetMessage(f.ctx, hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	if meta.Subject != "Sent to both" || meta.MessageID != "shared@example.com" || meta.SentAt == nil {
		t.Fatalf("unexpected metadata: %+v", meta)
	}

	assertServerUntouched(t, f.host, f.port, "alice", "pw-a", "INBOX")
}

// assertServerUntouched checks that archiving did not set \Seen.
func assertServerUntouched(t *testing.T, host string, port int, user, pw, mailbox string) {
	t.Helper()
	conn, err := imapsync.Dial(context.Background(), imapsync.Config{
		Host: host, Port: port, TLSMode: imapsync.TLSModeNone, Username: user, Password: pw,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Examine(mailbox); err != nil {
		t.Fatal(err)
	}
	discard := func(r io.Reader) (struct{}, error) { _, err := io.Copy(io.Discard, r); return struct{}{}, err }
	n := 0
	err = imapsync.FetchAfter(conn, 0, discard, func(m imapsync.Message[struct{}]) error {
		n++
		if slices.Contains(m.Flags, string(imap.FlagSeen)) {
			t.Errorf("message %d has \\Seen after archiving", m.UID)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no messages checked")
	}
}

func TestSyncContinuesAfterAccountFailure(t *testing.T) {
	good := imapmemserver.NewUser("good", "pw")
	createMailboxes(t, good, "INBOX")
	appendMsg(t, good, "INBOX", rawMessage("g1", "Hello"))

	f := newFixture(t, good)
	f.addAccount("broken", "good", "wrong-password")
	f.addAccount("good", "good", "pw")

	res := f.sync()
	if res["broken"].Err == nil {
		t.Fatal("expected login error for broken account")
	}
	expectResult(t, res["good"], 1, 1)

	stats, _, err := f.store.Stats(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range stats {
		want := "ok"
		if s.Account == "broken" {
			want = "failed"
		}
		if s.LastStatus == nil || *s.LastStatus != want {
			t.Errorf("%s: last status = %v, want %s", s.Account, s.LastStatus, want)
		}
	}
}

// account returns the only account with this name.
func (f *fixture) account(name string) (*store.Account, error) {
	list, err := f.store.ListAccountsByName(f.ctx, name)
	if err != nil {
		return nil, err
	}
	if len(list) != 1 {
		return nil, store.ErrNotFound
	}
	return list[0], nil
}

func TestSyncSkipsAccountDeletedMeanwhile(t *testing.T) {
	u := imapmemserver.NewUser("u", "pw")
	createMailboxes(t, u, "INBOX")
	appendMsg(t, u, "INBOX", rawMessage("m1", "Hello"))
	f := newFixture(t, u)
	f.addAccount("short-lived", "u", "pw")
	a, _ := f.account("short-lived") // loaded, e.g. by the runner

	// Deleted before the sync takes the lock.
	unlock, ok, err := f.store.TryLockSync(f.ctx, a.ID)
	if err != nil || !ok {
		t.Fatal("lock", ok, err)
	}
	if _, err := f.store.DeleteOrRemoveAccount(f.ctx, a.Ref()); err != nil {
		t.Fatal(err)
	}
	unlock()

	if res := f.syncer.SyncAccount(f.ctx, a); !errors.Is(res.Err, archive.ErrAccountRemoved) {
		t.Fatalf("sync of a deleted account: %v", res.Err)
	}
	runs, err := f.store.LastRuns(f.ctx)
	if err != nil || len(runs) != 0 {
		t.Fatalf("runs recorded: %v, %v", runs, err)
	}
}
