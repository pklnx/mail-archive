package archive_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/imaptest"
	"github.com/pklnx/mail-archive/internal/store"
)

var fillCutoff = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func fillMessage(id string) []byte {
	if id == "" {
		return []byte("From: a@example.com\r\nSubject: no id\r\n\r\nNo Message-ID.\r\n")
	}
	return fmt.Appendf(nil, "Message-ID: <%s@example.com>\r\nFrom: a@example.com\r\nSubject: %s\r\n\r\nBody of %s\r\n", id, id, id)
}

// journal stores the journal copy of a message: the same Message-ID, other
// bytes (Postfix adds a Received: line).
func (f *fixture) journal(acc *store.Account, id string) {
	f.t.Helper()
	raw := append([]byte("Received: from mx by archive\r\n"), fillMessage(id)...)
	blob, created, err := f.blobs.Put(bytes.NewReader(raw))
	if err != nil {
		f.t.Fatal(err)
	}
	meta, err := archive.BuildMeta(f.ctx, f.store, f.blobs, blob, created)
	if err != nil {
		f.t.Fatal(err)
	}
	env := store.Envelope{MailFrom: "a@example.com", RcptTo: []string{"alice@example.com"}, ReceivedAt: time.Now(), HELO: "mx"}
	if _, err := f.store.ReceiveMessage(f.ctx, acc.ID, meta, env, func() bool { return true }); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) smtpAccount() *store.Account {
	f.t.Helper()
	acc := &store.Account{Name: "journal", Enabled: true}
	if err := f.store.CreateSMTPAccount(f.ctx, acc, []string{"archive@archive.local"}); err != nil {
		f.t.Fatal(err)
	}
	return acc
}

func (f *fixture) filler() *archive.Filler {
	return &archive.Filler{Store: f.store, Blobs: f.blobs, Sealer: f.sealer, BatchSize: 2, Logger: slog.New(slog.NewTextHandler(f.logs, nil))}
}

func (f *fixture) createFill(acc *store.Account, password string, excluded ...string) *store.Fill {
	f.t.Helper()
	fill := &store.Fill{
		AccountID: acc.ID, Host: f.host, Port: f.port, TLSMode: store.TLSModeNone, Username: "alice@example.com",
		Excluded: excluded, Cutoff: &fillCutoff,
	}
	seal := func(id int64) ([]byte, error) { return archive.SealFillPassword(f.sealer, id, password) }
	if err := f.store.CreateFill(f.ctx, fill, seal); err != nil {
		f.t.Fatal(err)
	}
	return fill
}

func folderOf(fill *store.Fill, source string) store.FillFolder {
	for _, ff := range fill.Folders {
		if ff.SourceName == source {
			return ff
		}
	}
	return store.FillFolder{}
}

func fillUser(t *testing.T) *imapmemserver.User {
	u := imapmemserver.NewUser("alice@example.com", "secret")
	imaptest.CreateMailboxes(t, u, "INBOX", "Sent", "Spam")
	berlin := time.FixedZone("CEST", 2*3600)
	for _, m := range []struct {
		id string
		at time.Time
	}{
		{"m1", fillCutoff.Add(-48 * time.Hour)},             // imported
		{"m2", fillCutoff.Add(-10 * time.Minute)},           // journaled
		{"m3", fillCutoff.Add(30 * time.Minute).In(berlin)}, // in the window: imported
		{"m4", fillCutoff.Add(2 * time.Hour)},               // after the cut-off
		{"m5", fillCutoff.Add(-24 * time.Hour)},             // journaled long before
		{"", fillCutoff.Add(-time.Hour)},                    // no Message-ID: imported
	} {
		imaptest.AppendAt(t, u, "INBOX", fillMessage(m.id), m.at)
	}
	imaptest.AppendAt(t, u, "Sent", fillMessage("s1"), fillCutoff.Add(-5*24*time.Hour))
	imaptest.AppendAt(t, u, "Spam", fillMessage("spam"), fillCutoff.Add(-time.Hour))
	return u
}

func TestFill(t *testing.T) {
	f := newFixture(t, fillUser(t))
	acc := f.smtpAccount()
	f.journal(acc, "m2")
	f.journal(acc, "m5")
	fill := f.createFill(acc, "secret", "Spam")
	f.srv.ResetCommands()

	got, err := f.filler().Run(f.ctx, fill.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.FillOK || got.PasswordEnc != nil || got.FinishedAt == nil {
		t.Fatalf("fill %+v", got)
	}
	inbox, sent := folderOf(got, "INBOX"), folderOf(got, "Sent")
	if want := (store.FillCounts{Scanned: 6, Imported: 3, New: 3, AfterCutoff: 1, Journaled: 2}); inbox.Counts != want || !inbox.Done {
		t.Errorf("INBOX %+v, want %+v", inbox, want)
	}
	if want := (store.FillCounts{Scanned: 1, Imported: 1, New: 1}); sent.Counts != want {
		t.Errorf("Sent %+v", sent.Counts)
	}
	if len(got.Folders) != 2 || inbox.FolderName != store.SMTPFolder || sent.FolderName != store.SMTPFolder {
		t.Errorf("folders %+v", got.Folders)
	}
	// Only the imported messages were downloaded: UIDs 1, 3 and 6 of INBOX
	// (in batches of two headers) and 1 of Sent.
	if b := f.srv.BodyFetches(); !slices.Equal(b, []string{"1", "3", "6", "1"}) {
		t.Errorf("body fetches %v", b)
	}
	// Everything is in JOURNAL: two journaled messages with an envelope,
	// UIDs 1 and 2, and four imported ones after them, which name the fill.
	locs, _ := f.stats()
	if locs["journal"] != 6 {
		t.Errorf("locations %v", locs)
	}
	folders := f.journalLocations(acc)
	if len(folders) != 6 {
		t.Fatalf("journal locations %+v", folders)
	}
	for i, l := range folders {
		if l.uid != int64(i+1) || l.filled != (i >= 2) || l.filled != (l.fillID == fill.ID) {
			t.Errorf("location %d: %+v", i, l)
		}
	}
	if last, _ := f.store.LastReceived(f.ctx); last[acc.ID].IsZero() {
		t.Error("no last received")
	}

	// A second run of an ended fill does nothing.
	if _, err := f.filler().Run(f.ctx, fill.ID); !errors.Is(err, store.ErrFillNotRunning) {
		t.Errorf("run an ended fill: %v", err)
	}
}

// journalLocation is a location in an smtp account's journal folder.
type journalLocation struct {
	uid    int64
	filled bool
	fillID int64
}

// journalLocations returns the locations in the journal folder of acc by
// UID.
func (f *fixture) journalLocations(acc *store.Account) []journalLocation {
	f.t.Helper()
	conn, err := pgx.Connect(f.ctx, f.dbURL)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = conn.Close(f.ctx) }()
	rows, err := conn.Query(f.ctx, `SELECT l.uid, e.location_id IS NULL, coalesce(l.fill_id, 0)
		FROM message_locations l JOIN folders fo ON fo.id = l.folder_id LEFT JOIN envelopes e ON e.location_id = l.id
		WHERE fo.account_id = $1 AND fo.name = $2 ORDER BY l.uid`, acc.ID, store.SMTPFolder)
	if err != nil {
		f.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (journalLocation, error) {
		var l journalLocation
		err := r.Scan(&l.uid, &l.filled, &l.fillID)
		return l, err
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

// A message filed in two IMAP folders, or moved between them, is stored
// once in JOURNAL.
func TestFillOneFolder(t *testing.T) {
	u := imapmemserver.NewUser("alice@example.com", "secret")
	imaptest.CreateMailboxes(t, u, "INBOX", "Clients/Miller")
	imaptest.AppendAt(t, u, "INBOX", fillMessage("both"), fillCutoff.Add(-time.Hour))
	imaptest.AppendAt(t, u, "Clients/Miller", fillMessage("both"), fillCutoff.Add(-time.Hour))
	imaptest.AppendAt(t, u, "Clients/Miller", fillMessage("miller"), fillCutoff.Add(-time.Hour))
	f := newFixture(t, u)
	acc := f.smtpAccount()
	fill := f.createFill(acc, "secret")
	got, err := f.filler().Run(f.ctx, fill.ID)
	if err != nil || got.Status != store.FillOK {
		t.Fatalf("fill %+v, %v", got, err)
	}
	var c store.FillCounts
	for _, ff := range got.Folders {
		c.Add(ff.Counts)
	}
	if c.Imported != 2 || c.Present != 1 {
		t.Errorf("counts %+v", c)
	}
	if locs := f.journalLocations(acc); len(locs) != 2 {
		t.Errorf("journal locations %+v", locs)
	}
	// No folder besides JOURNAL.
	conn, err := pgx.Connect(f.ctx, f.dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(f.ctx) }()
	var names []string
	rows, _ := conn.Query(f.ctx, "SELECT name FROM folders WHERE account_id = $1", acc.ID)
	if names, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil || !slices.Equal(names, []string{store.SMTPFolder}) {
		t.Errorf("folders %v, %v", names, err)
	}
}

// A fill from before #112 has a folder prefix: resumed, it goes on
// writing into its own folders.
func TestFillLegacyPrefix(t *testing.T) {
	f := newFixture(t, fillUser(t))
	acc := f.smtpAccount()
	fill := f.createFill(acc, "secret", "Spam", "Sent")
	conn, err := pgx.Connect(f.ctx, f.dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(f.ctx) }()
	if _, err := conn.Exec(f.ctx, "UPDATE smtp_fills SET folder_prefix = 'alice' WHERE id = $1", fill.ID); err != nil {
		t.Fatal(err)
	}
	got, err := f.filler().Run(f.ctx, fill.ID)
	if err != nil || got.Status != store.FillOK || got.FolderPrefix != "alice" {
		t.Fatalf("fill %+v, %v", got, err)
	}
	if inbox := folderOf(got, "INBOX"); inbox.FolderName != "alice/INBOX" || inbox.Counts.Imported != 5 {
		t.Errorf("INBOX %+v", inbox)
	}
	if locs := f.journalLocations(acc); len(locs) != 0 {
		t.Errorf("journal locations %+v", locs)
	}
	rows, _ := conn.Query(f.ctx, `SELECT l.uid FROM message_locations l JOIN folders fo ON fo.id = l.folder_id
		WHERE fo.account_id = $1 AND fo.name = 'alice/INBOX' AND l.fill_id = $2 ORDER BY l.uid`, acc.ID, fill.ID)
	uids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil || !slices.Equal(uids, []int64{1, 2, 3, 4, 5}) {
		t.Errorf("legacy folder UIDs %v, %v", uids, err)
	}
}

func TestFillResumeAfterCancel(t *testing.T) {
	u := fillUser(t)
	f := newFixture(t, u)
	acc := f.smtpAccount()
	fill := f.createFill(acc, "secret", "Spam", "Sent")
	filler := f.filler()
	batches := 0
	filler.Progress = func(store.FillFolder) {
		batches++
		if batches == 1 {
			if err := f.store.CancelFill(f.ctx, fill.ID); err != nil {
				t.Error(err)
			}
		}
	}
	got, err := filler.Run(f.ctx, fill.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.FillCancelled || got.PasswordEnc != nil {
		t.Fatalf("cancelled fill %+v", got)
	}
	if c := folderOf(got, "INBOX"); c.LastUID != 2 || c.Counts.Scanned != 2 || c.Done {
		t.Fatalf("progress after cancel %+v", c)
	}

	seal := func(id int64) ([]byte, error) { return archive.SealFillPassword(f.sealer, id, "secret") }
	if err := f.store.ResumeFill(f.ctx, fill.ID, seal); err != nil {
		t.Fatal(err)
	}
	f.srv.ResetCommands()
	filler.Progress = nil
	if got, err = filler.Run(f.ctx, fill.ID); err != nil {
		t.Fatal(err)
	}
	inbox := folderOf(got, "INBOX")
	if want := (store.FillCounts{Scanned: 6, Imported: 5, New: 5, AfterCutoff: 1}); got.Status != store.FillOK || inbox.Counts != want {
		t.Errorf("resumed %s %+v, want %+v", got.Status, inbox.Counts, want)
	}
	// Nothing committed before the cancel is fetched again.
	if b := f.srv.BodyFetches(); !slices.Equal(b, []string{"3", "5:6"}) {
		t.Errorf("body fetches after resume %v", b)
	}
	if locs, _ := f.stats(); locs["journal"] != 5 {
		t.Errorf("locations %v", locs)
	}
}

func TestFillRescansAfterUIDValidityChange(t *testing.T) {
	u := imapmemserver.NewUser("alice@example.com", "secret")
	imaptest.CreateMailboxes(t, u, "INBOX", "Box")
	for i := range 4 {
		imaptest.AppendAt(t, u, "Box", fillMessage(fmt.Sprintf("b%d", i)), fillCutoff.Add(-time.Hour))
	}
	f := newFixture(t, u)
	acc := f.smtpAccount()
	fill := f.createFill(acc, "secret")
	filler := f.filler()
	filler.Progress = func(ff store.FillFolder) {
		if ff.SourceName == "Box" && ff.LastUID == 2 {
			_ = f.store.CancelFill(f.ctx, fill.ID)
		}
	}
	if _, err := filler.Run(f.ctx, fill.ID); err != nil {
		t.Fatal(err)
	}
	// The server renumbers the folder: same messages, new UIDVALIDITY.
	if err := u.Delete("Box"); err != nil {
		t.Fatal(err)
	}
	imaptest.CreateMailboxes(t, u, "Box")
	for i := range 4 {
		imaptest.AppendAt(t, u, "Box", fillMessage(fmt.Sprintf("b%d", i)), fillCutoff.Add(-time.Hour))
	}
	seal := func(id int64) ([]byte, error) { return archive.SealFillPassword(f.sealer, id, "secret") }
	if err := f.store.ResumeFill(f.ctx, fill.ID, seal); err != nil {
		t.Fatal(err)
	}
	filler.Progress = nil
	got, err := filler.Run(f.ctx, fill.ID)
	if err != nil {
		t.Fatal(err)
	}
	box := folderOf(got, "Box")
	if box.Counts.Imported != 4 || box.Counts.Present != 2 || got.Status != store.FillOK {
		t.Errorf("after the renumbering %s %+v", got.Status, box.Counts)
	}
	if locs, _ := f.stats(); locs["journal"] != 4 {
		t.Errorf("locations %v", locs)
	}
}

func TestFillFailsAndWipesThePassword(t *testing.T) {
	f := newFixture(t, fillUser(t))
	acc := f.smtpAccount()
	fill := f.createFill(acc, "wrong password")
	got, err := f.filler().Run(f.ctx, fill.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.FillFailed || got.Error == "" || got.PasswordEnc != nil {
		t.Errorf("failed fill %+v", got)
	}
}

func TestFillHoldsTheSyncLock(t *testing.T) {
	f := newFixture(t, fillUser(t))
	acc := f.smtpAccount()
	fill := f.createFill(acc, "secret")
	unlock, ok, err := f.store.TryLockSyncForWrite(f.ctx, acc.ID)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if _, err := f.filler().Run(f.ctx, fill.ID); !errors.Is(err, archive.ErrFillBusy) {
		t.Errorf("run while locked: %v", err)
	}
	// Journaling goes on while the lock is held.
	f.journal(acc, "during")
	unlock()
}

// cleanupFixture is the setting of the fill-versus-cleanup tests: alice's
// IMAP account "work" with a 1-year rule, holding messages of 2010, and
// bob's smtp account without a rule, which a fill imports into from an
// IMAP mailbox whose first message (UID 1) is the target.
type cleanupFixture struct {
	*fixture
	work, journal *store.Account
	alice, bob    *store.User
	// cleaner is a second Store handle for the callbacks' cleanup, so it
	// needs no free connection in the fill's pool.
	cleaner *store.Store
	ctx     context.Context
}

const cleanupLockTimeout = 150 * time.Millisecond

func newCleanupFixture(t *testing.T) *cleanupFixture {
	t.Helper()
	u := imapmemserver.NewUser("alice@example.com", "secret")
	imaptest.CreateMailboxes(t, u, "INBOX")
	for _, id := range []string{"target", "second", "third"} {
		imaptest.AppendAt(t, u, "INBOX", fillMessage(id), fillCutoff.Add(-48*time.Hour))
	}
	f := newFixture(t, u)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	c := &cleanupFixture{fixture: f, ctx: ctx}
	var err error
	if c.alice, err = f.store.CreateUser(ctx, "alice", "h", false); err != nil {
		t.Fatal(err)
	}
	if c.bob, err = f.store.CreateUser(ctx, "bob", "h", false); err != nil {
		t.Fatal(err)
	}
	c.work = &store.Account{Name: "work", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, OwnerID: &c.alice.ID}
	if err := f.store.CreateAccount(ctx, c.work); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.AddRetentionRule(ctx, store.NewRetentionRule{AccountID: c.work.ID, Years: 1}); err != nil {
		t.Fatal(err)
	}
	c.journal = &store.Account{Name: "journal", Enabled: true, OwnerID: &c.bob.ID}
	if err := f.store.CreateSMTPAccount(ctx, c.journal, []string{"archive@archive.local"}); err != nil {
		t.Fatal(err)
	}
	if c.cleaner, err = store.Open(ctx, f.dbURL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.cleaner.Close)
	return c
}

// expired archives fillMessage(id) in work/INBOX with an internal date in
// 2010 and returns its SHA.
func (c *cleanupFixture) expired(id string, uid uint32) string {
	c.t.Helper()
	blob, created, err := c.blobs.Put(bytes.NewReader(fillMessage(id)))
	if err != nil {
		c.t.Fatal(err)
	}
	meta, err := archive.BuildMeta(c.ctx, c.store, c.blobs, blob, created)
	if err != nil {
		c.t.Fatal(err)
	}
	inbox, err := c.store.GetOrCreateFolder(c.ctx, c.work.ID, "INBOX")
	if err != nil {
		c.t.Fatal(err)
	}
	loc := store.Location{FolderID: inbox.ID, UID: uid, InternalDate: time.Date(2010, 3, 5, 10, 0, 0, 0, time.UTC)}
	if _, err := c.store.SaveBatch(c.ctx, inbox.ID, uid, []store.MessageMeta{meta}, []store.Location{loc}); err != nil {
		c.t.Fatal(err)
	}
	return blob.SHA256
}

func (c *cleanupFixture) confirmedRun() int64 {
	c.t.Helper()
	run, err := c.store.ProposeDeletion(c.ctx, nil)
	if err != nil {
		c.t.Fatal(err)
	}
	if err := c.store.ConfirmDeletionRun(c.ctx, run.ID, nil); err != nil {
		c.t.Fatal(err)
	}
	return run.ID
}

// runFill runs a fill into bob's journal and calls during once, from the
// Progress of the first batch that committed the target (INBOX, UID 1),
// while the fill holds the blob lock. It fails the test if during never
// ran.
func (c *cleanupFixture) runFill(during func()) {
	c.t.Helper()
	fill := c.createFill(c.journal, "secret")
	filler := c.filler()
	fired := false
	filler.Progress = func(ff store.FillFolder) {
		if !fired && ff.SourceName == "INBOX" && ff.LastUID >= 1 {
			fired = true
			during()
		}
	}
	got, err := filler.Run(c.ctx, fill.ID)
	if err != nil || got.Status != store.FillOK {
		c.t.Fatalf("fill %+v %v", got, err)
	}
	if !fired {
		c.t.Fatal("the callback never ran")
	}
	if inbox := folderOf(got, "INBOX"); inbox.Counts.Imported != 3 {
		c.t.Fatalf("INBOX %+v", inbox)
	}
}

// fileBytes returns the content of sha's file.
func (c *cleanupFixture) fileBytes(sha string) []byte {
	c.t.Helper()
	file, err := c.blobs.Open(blobstore.RelPath(sha))
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	b, err := io.ReadAll(file)
	if err != nil {
		c.t.Fatal(err)
	}
	return b
}

// assertLocations checks that alice's and bob's views of sha are their
// given accounts.
func (c *cleanupFixture) assertLocations(sha string, want map[*store.User]string) {
	c.t.Helper()
	for u, account := range want {
		d, err := c.store.GetMessageDetail(c.ctx, u.ID, sha)
		if err != nil {
			c.t.Errorf("%s: %v", u.Name, err)
			continue
		}
		if len(d.Locations) != 1 || d.Locations[0].Account != account {
			c.t.Errorf("%s's locations %+v", u.Name, d.Locations)
		}
	}
}

// A fill imports the bytes of a confirmed candidate into an unruled
// account. While it holds the blob lock, the deletion cannot run; after
// it, the new location keeps the candidate.
func TestFillAgainstDeletion(t *testing.T) {
	c := newCleanupFixture(t)
	target := c.expired("target", 1)
	id := c.confirmedRun()
	if got, err := c.store.PendingCandidates(c.ctx, id, "", 10); err != nil || !slices.Equal(got, []string{target}) {
		t.Fatalf("candidates %v %v", got, err)
	}

	var (
		duringErr error
		during    *store.DeletionRun
		pending   []string
	)
	c.runFill(func() {
		_, duringErr = archive.ExecuteDeletionRun(c.ctx, c.cleaner, id, archive.DeletionOptions{LockTimeout: cleanupLockTimeout})
		var err error
		if during, err = c.cleaner.DeletionRun(c.ctx, id); err != nil {
			t.Error(err)
		}
		if pending, err = c.cleaner.PendingCandidates(c.ctx, id, "", 10); err != nil {
			t.Error(err)
		}
	})
	if !errors.Is(duringErr, archive.ErrBlobsBusy) {
		t.Fatalf("deletion during the fill: %v", duringErr)
	}
	if during == nil || during.State != store.RunConfirmed || during.Deleted != 0 || during.Kept != 0 || !slices.Equal(pending, []string{target}) {
		t.Fatalf("run during the fill %+v, pending %v", during, pending)
	}

	run, err := archive.ExecuteDeletionRun(c.ctx, c.cleaner, id, archive.DeletionOptions{LockTimeout: cleanupLockTimeout})
	if err != nil || run.State != store.RunDone || run.Deleted != 0 || run.Kept != 1 {
		t.Fatalf("run after the fill %+v %v", run, err)
	}
	if !bytes.Equal(c.fileBytes(target), fillMessage("target")) {
		t.Error("the target's file changed")
	}
	c.assertLocations(target, map[*store.User]string{c.alice: "work", c.bob: "journal"})
}

// A fill archives a deleted message again, into an unruled account, while
// its file waits for the sweep. While the fill holds the blob lock, the
// sweep cannot run; after it, the sweep removes only the file of the
// control, which nothing archived again.
func TestFillAgainstSweep(t *testing.T) {
	c := newCleanupFixture(t)
	target, control := c.expired("target", 1), c.expired("control", 2)
	run, err := archive.ExecuteDeletionRun(c.ctx, c.cleaner, c.confirmedRun(), archive.DeletionOptions{})
	if err != nil || run.Deleted != 2 {
		t.Fatalf("run %+v %v", run, err)
	}
	if !c.blobs.Exists(target) || !c.blobs.Exists(control) {
		t.Fatal("files removed before the sweep")
	}
	if due, err := c.store.BlobRemovalDue(c.ctx); err != nil || !due {
		t.Fatalf("removals due %v %v", due, err)
	}

	var (
		duringErr     error
		duringRemoved int
		duringFiles   bool
	)
	c.runFill(func() {
		duringRemoved, duringErr = archive.SweepBlobs(c.ctx, c.cleaner, c.blobs, archive.SweepOptions{LockTimeout: cleanupLockTimeout})
		duringFiles = c.blobs.Exists(target) && c.blobs.Exists(control)
	})
	if !errors.Is(duringErr, archive.ErrBlobsBusy) || duringRemoved != 0 || !duringFiles {
		t.Fatalf("sweep during the fill: %d %v, files there %v", duringRemoved, duringErr, duringFiles)
	}

	n, err := archive.SweepBlobs(c.ctx, c.cleaner, c.blobs, archive.SweepOptions{LockTimeout: cleanupLockTimeout})
	if err != nil || n != 1 {
		t.Fatalf("sweep after the fill: %d %v", n, err)
	}
	if c.blobs.Exists(control) {
		t.Error("the control's file is there")
	}
	if !bytes.Equal(c.fileBytes(target), fillMessage("target")) {
		t.Error("the target's file is gone or changed")
	}
	c.assertLocations(target, map[*store.User]string{c.bob: "journal"})
	if _, err := c.store.GetMessageDetail(c.ctx, c.alice.ID, target); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("alice's deleted location: %v", err)
	}
	if rows, err := c.store.DueBlobRemovals(c.ctx, time.Time{}, "", 10); err != nil || len(rows) != 0 {
		t.Errorf("removals left %+v %v", rows, err)
	}
	if n, err := archive.SweepBlobs(c.ctx, c.cleaner, c.blobs, archive.SweepOptions{LockTimeout: cleanupLockTimeout}); err != nil || n != 0 {
		t.Errorf("second sweep: %d %v", n, err)
	}
}

// A message the blob store cannot persist fails the fill before its batch
// and the folder's progress commit.
func TestFillFailsWhenTheBlobStoreFails(t *testing.T) {
	f := newFixture(t, fillUser(t))
	acc := f.smtpAccount()
	fill := f.createFill(acc, "secret", "Spam", "Sent")
	if err := os.RemoveAll(filepath.Join(f.dataDir, "tmp")); err != nil {
		t.Fatal(err)
	}
	got, err := f.filler().Run(f.ctx, fill.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The folder fails; the fill ends partial.
	c := folderOf(got, "INBOX")
	if got.Status == store.FillOK || c.Error == "" {
		t.Fatalf("fill into a failing store: %s, folder %+v", got.Status, c)
	}
	if c.LastUID != 0 || c.Counts.Imported != 0 || c.Done {
		t.Errorf("progress without stored files %+v", c)
	}
	if locs, _ := f.stats(); locs["journal"] != 0 {
		t.Errorf("locations %v", locs)
	}
}
