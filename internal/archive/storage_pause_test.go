package archive_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/quota"
	"github.com/pklnx/mail-archive/internal/store"
)

// pauseFixture adds two customer groups to a fixture, user "ann" in
// group A (the default) and "ben" in group B, and a storage gate on the
// store with a clock the test moves. Group figures count (a snapshot was
// taken); the gate reads them again only when the clock moves.
type pauseFixture struct {
	*fixture
	db       *pgx.Conn
	ann, ben *store.User
	groupA   int64
	groupB   int64
	now      time.Time
	mu       sync.Mutex
}

func newPauseFixture(t *testing.T, users ...*imapmemserver.User) *pauseFixture {
	t.Helper()
	p := &pauseFixture{fixture: newFixture(t, users...), now: time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)}
	conn, err := pgx.Connect(p.ctx, p.dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	p.db = conn
	if p.ann, err = p.store.CreateUser(p.ctx, "ann", "h", false); err != nil {
		t.Fatal(err)
	}
	if p.ben, err = p.store.CreateUser(p.ctx, "ben", "h", false); err != nil {
		t.Fatal(err)
	}
	gb, err := p.store.CreateGroup(p.ctx, "B")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.store.SetUserGroup(p.ctx, p.ben.ID, gb.ID); err != nil {
		t.Fatal(err)
	}
	p.groupA, p.groupB = p.ann.GroupID, gb.ID
	if _, err := conn.Exec(p.ctx, `UPDATE storage_snapshot SET computed_at = now()`); err != nil {
		t.Fatal(err)
	}
	return p
}

func (p *pauseFixture) clock() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.now
}

// later moves the clock past the gate's interval: the next check reads
// the figures again.
func (p *pauseFixture) later() {
	p.mu.Lock()
	p.now = p.now.Add(time.Hour)
	p.mu.Unlock()
}

// gate is a gate on the fixture's store; every save tells it what it
// stored.
func (p *pauseFixture) gate(instanceQuota int64) *quota.Gate {
	g := quota.New(quota.Config{InstanceQuota: instanceQuota, Thresholds: quota.Thresholds{WarnPercent: 80, OverflowPercent: 10},
		Interval: time.Minute}, p.store, nil, p.clock, slog.New(slog.NewTextHandler(io.Discard, nil)))
	p.store.SetWriteObserver(g.Wrote)
	return g
}

// setQuota sets group A's quota: 1 byte makes it full after the first
// batch that stores anything (the gate counts the batch's bytes at once).
func (p *pauseFixture) setQuota(group int64, q *int64) {
	p.t.Helper()
	if _, err := p.store.SetGroupQuota(p.ctx, group, q); err != nil {
		p.t.Fatal(err)
	}
}

func (p *pauseFixture) ownedAccount(name, user string, owner *store.User) *store.Account {
	p.t.Helper()
	acc := &store.Account{
		Name: name, Host: p.host, Port: p.port, TLSMode: store.TLSModeNone, Username: user, Enabled: true, OwnerID: &owner.ID,
	}
	seal := func(id int64) ([]byte, error) { return archive.SealPassword(p.sealer, id, "pw") }
	if err := p.store.CreateAccountSealed(p.ctx, acc, seal); err != nil {
		p.t.Fatal(err)
	}
	return acc
}

func (p *pauseFixture) int(sql string, args ...any) int64 {
	p.t.Helper()
	var n int64
	if err := p.db.QueryRow(p.ctx, sql, args...).Scan(&n); err != nil {
		p.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func (p *pauseFixture) locations(account int64) int64 {
	return p.int(`SELECT count(*) FROM message_locations l JOIN folders f ON f.id = l.folder_id WHERE f.account_id = $1`, account)
}

func one(v int64) *int64 { return &v }

// A sync of group A pauses mid-folder once the group is full: what came
// before is saved and the folder's last UID covers it, the run stops
// before the next folder and is partial with storage_full, the streak is
// unchanged. Group B keeps syncing. While paused, no run starts. With the
// quota raised, the next run continues from the last UID. A full instance
// pauses every group.
func TestSyncPausesPerGroup(t *testing.T) {
	ua := imapmemserver.NewUser("a", "pw")
	ub := imapmemserver.NewUser("b", "pw")
	createMailboxes(t, ua, "INBOX", "Later")
	createMailboxes(t, ub, "INBOX")
	for i := range 5 {
		appendMsg(t, ua, "INBOX", rawMessage(fmt.Sprintf("a%d@example.com", i), "a"))
	}
	appendMsg(t, ua, "Later", rawMessage("later@example.com", "later"))
	for i := range 3 {
		appendMsg(t, ub, "INBOX", rawMessage(fmt.Sprintf("b%d@example.com", i), "b"))
	}
	p := newPauseFixture(t, ua, ub)
	accA := p.ownedAccount("a", "a", p.ann)
	accB := p.ownedAccount("b", "b", p.ben)
	p.setQuota(p.groupA, one(1))
	p.syncer.Storage = p.gate(0)

	res := p.sync()
	if !res["a"].IsPaused() || res["a"].Fetched != 2 {
		t.Fatalf("a: %+v", res["a"])
	}
	var fe *quota.FullError
	if !errors.As(res["a"].Err, &fe) || fe.Scope != quota.ScopeGroup {
		t.Fatalf("a: err %v", res["a"].Err)
	}
	if res["b"].Err != nil || res["b"].Fetched != 3 {
		t.Fatalf("b: %+v", res["b"])
	}
	if n := p.locations(accA.ID); n != 2 {
		t.Fatalf("a: %d locations, want the first batch", n)
	}
	if last := p.int(`SELECT last_uid FROM folders WHERE account_id = $1 AND name = 'INBOX'`, accA.ID); last != 2 {
		t.Fatalf("a: INBOX last UID %d", last)
	}
	if n := p.int(`SELECT count(*) FROM folders WHERE account_id = $1 AND name = 'Later'`, accA.ID); n != 0 {
		t.Fatal("the run went on to the next folder")
	}
	runs, err := p.store.LastRuns(p.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r := runs[accA.ID]; r.Status != "partial" || r.Error != quota.PausedText {
		t.Fatalf("a: run %+v", r)
	}
	health, err := p.store.SyncHealth(p.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if h := health[accA.ID]; h.FailureStreak != 0 || h.LastSuccessAt != nil || h.GroupID == nil || *h.GroupID != p.groupA {
		t.Fatalf("a: health %+v", h)
	}
	if h := health[accB.ID]; h.LastSuccessAt == nil {
		t.Fatalf("b: health %+v", h)
	}
	if strings.Contains(runs[accA.ID].Error, "group") || strings.Contains(runs[accA.ID].Error, "instance") {
		t.Fatal("the run's error names the limit")
	}

	// Paused: no run starts.
	before := p.int(`SELECT count(*) FROM sync_runs WHERE account_id = $1`, accA.ID)
	p.later()
	if res := p.sync(); !res["a"].IsPaused() || res["a"].Fetched != 0 {
		t.Fatalf("a while paused: %+v", res["a"])
	}
	if n := p.int(`SELECT count(*) FROM sync_runs WHERE account_id = $1`, accA.ID); n != before {
		t.Fatalf("a while paused: %d runs, before %d", n, before)
	}

	// The quota raised: the next run continues from the last UID.
	p.setQuota(p.groupA, nil)
	p.later()
	res = p.sync()
	if res["a"].Err != nil || res["a"].Fetched != 4 {
		t.Fatalf("a after the quota was raised: %+v", res["a"])
	}
	if n := p.locations(accA.ID); n != 6 {
		t.Fatalf("a: %d locations, want 6", n)
	}

	// A full instance pauses every group, whatever their quotas.
	appendMsg(t, ua, "INBOX", rawMessage("a9@example.com", "a"))
	appendMsg(t, ub, "INBOX", rawMessage("b9@example.com", "b"))
	p.syncer.Storage = p.gate(1)
	res = p.sync()
	for _, name := range []string{"a", "b"} {
		if !errors.As(res[name].Err, &fe) || fe.Scope != quota.ScopeInstance || res[name].Fetched != 0 {
			t.Fatalf("%s with the instance full: %+v", name, res[name])
		}
	}
}

// writeMbox writes an mbox file of n messages and returns its directory.
func writeMbox(t *testing.T, prefix string, n int) string {
	t.Helper()
	dir := t.TempDir()
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "From sender@example.com Fri Mar  5 10:00:00 2010\nMessage-ID: <%s%d@example.com>\nFrom: a@example.com\n"+
			"Subject: %s %d\nDate: Fri, 5 Mar 2010 10:00:00 +0000\n\nBody %d\n\n", prefix, i, prefix, i, i)
	}
	if err := os.WriteFile(filepath.Join(dir, "Imported"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "Imported")
}

// An import pauses once its group is full: what it read is saved, the run
// is partial with storage_full, and running it again goes on from there.
// A paused group's import does not start.
func TestImportPauses(t *testing.T) {
	p := newPauseFixture(t)
	acc := &store.Account{Kind: store.KindImport, Name: "old", OwnerID: &p.ann.ID}
	if err := p.store.CreateAccount(p.ctx, acc); err != nil {
		t.Fatal(err)
	}
	p.setQuota(p.groupA, one(1))
	im := &archive.Importer{Store: p.store, Blobs: p.blobs, BatchSize: 2, Storage: p.gate(0),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	folders, _, err := archive.ImportFolders("mbox", writeMbox(t, "imp", 5), "")
	if err != nil {
		t.Fatal(err)
	}
	acc, err = p.store.GetAccount(p.ctx, acc.ID)
	if err != nil {
		t.Fatal(err)
	}
	res, err := im.Import(p.ctx, acc, folders)
	if !errors.Is(err, quota.ErrStorageFull) || res == nil || res.Status != "partial" || res.Total().Added != 2 {
		t.Fatalf("import: %+v, %v", res, err)
	}
	if n := p.locations(acc.ID); n != 2 {
		t.Fatalf("%d locations after the pause", n)
	}
	runs, _ := p.store.LastRuns(p.ctx)
	if runs[acc.ID].Error != quota.PausedText {
		t.Fatalf("run %+v", runs[acc.ID])
	}
	p.later()
	if res, err := im.Import(p.ctx, acc, folders); !errors.Is(err, quota.ErrStorageFull) || res != nil {
		t.Fatalf("paused import started: %+v, %v", res, err)
	}
	p.setQuota(p.groupA, nil)
	p.later()
	res, err = im.Import(p.ctx, acc, folders)
	if err != nil || res.Status != "ok" || res.Total().Added != 3 || res.Total().Present != 2 {
		t.Fatalf("rerun: %+v, %v", res, err)
	}
	if n := p.locations(acc.ID); n != 5 {
		t.Fatalf("%d locations after the rerun", n)
	}
}

// An import into an smtp account's journal folder pauses the same way.
func TestJournalImportPauses(t *testing.T) {
	p := newPauseFixture(t)
	acc := &store.Account{Name: "journal", Enabled: true, OwnerID: &p.ann.ID}
	if err := p.store.CreateSMTPAccount(p.ctx, acc, []string{"archive@archive.local"}); err != nil {
		t.Fatal(err)
	}
	p.setQuota(p.groupA, one(1))
	im := &archive.Importer{Store: p.store, Blobs: p.blobs, BatchSize: 2, Storage: p.gate(0),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	folders, _, err := archive.JournalImportFolders("mbox", writeMbox(t, "jnl", 5))
	if err != nil {
		t.Fatal(err)
	}
	importRun := func() (*archive.ImportResult, error) {
		imp := &store.SMTPImport{AccountID: acc.ID, Label: "old", Format: "mbox", Source: "x"}
		if err := p.store.CreateSMTPImport(p.ctx, imp); err != nil {
			t.Fatal(err)
		}
		return im.ImportJournal(p.ctx, acc, imp, folders)
	}
	res, err := importRun()
	if !errors.Is(err, quota.ErrStorageFull) || res.Status != "partial" || res.Total().Added != 2 {
		t.Fatalf("import: %+v, %v", res, err)
	}
	p.setQuota(p.groupA, nil)
	p.later()
	res, err = importRun()
	if err != nil || res.Total().Added != 3 || res.Total().Present != 2 {
		t.Fatalf("rerun: %+v, %v", res, err)
	}
	if n := p.locations(acc.ID); n != 5 {
		t.Fatalf("%d locations", n)
	}
}

// A fill pauses once its group is full: the batch before is saved, the
// fill stays running (nothing ends, its health is unchanged), a run of a
// paused fill does not start, and once space is freed the fill goes on
// where it stopped without fetching anything twice.
func TestFillPauses(t *testing.T) {
	p := newPauseFixture(t, fillUser(t))
	acc := &store.Account{Name: "journal", Enabled: true, OwnerID: &p.ann.ID}
	if err := p.store.CreateSMTPAccount(p.ctx, acc, []string{"archive@archive.local"}); err != nil {
		t.Fatal(err)
	}
	fill := p.createFill(acc, "secret", "Spam")
	p.setQuota(p.groupA, one(1))
	filler := p.filler()
	filler.Storage = p.gate(0)
	p.srv.ResetCommands()
	got, err := filler.Run(p.ctx, fill.ID)
	if !errors.Is(err, quota.ErrStorageFull) || got.Status != store.FillRunning {
		t.Fatalf("paused fill: %+v, %v", got, err)
	}
	if n := p.locations(acc.ID); n != 2 {
		t.Fatalf("%d locations after the pause, want the first batch's two", n)
	}
	if got, err := filler.Run(p.ctx, fill.ID); !errors.Is(err, quota.ErrStorageFull) || got.Status != store.FillRunning {
		t.Fatalf("run while paused: %+v, %v", got, err)
	}
	p.setQuota(p.groupA, nil)
	p.later()
	got, err = filler.Run(p.ctx, fill.ID)
	if err != nil || got.Status != store.FillOK {
		t.Fatalf("after the quota was raised: %+v, %v", got, err)
	}
	inbox := folderOf(got, "INBOX")
	// m1, m2, m3, m5 and the one without a Message-ID (nothing journaled
	// here), and s1 of Sent; each body fetched once.
	if inbox.Counts.Imported != 5 || p.locations(acc.ID) != 6 {
		t.Fatalf("INBOX %+v, %d locations", inbox.Counts, p.locations(acc.ID))
	}
}

// A pause with messages queued in the folder's batch (here the disk
// reserve, reached while the third message comes in): the queued ones are
// saved and the folder's last UID covers exactly them.
func TestSyncPauseSavesTheBatch(t *testing.T) {
	ua := imapmemserver.NewUser("a", "pw")
	createMailboxes(t, ua, "INBOX")
	for i := range 5 {
		appendMsg(t, ua, "INBOX", rawMessage(fmt.Sprintf("q%d@example.com", i), "q"))
	}
	p := newPauseFixture(t, ua)
	acc := p.ownedAccount("a", "a", p.ann)
	p.syncer.BatchSize = 10 // nothing is saved before the pause
	gate := p.diskGate(4)
	p.syncer.Storage = gate
	res := p.sync()
	var fe *quota.FullError
	if !errors.As(res["a"].Err, &fe) || fe.Scope != quota.ScopeDisk || res["a"].New != 2 {
		t.Fatalf("a: %+v", res["a"])
	}
	if n := p.locations(acc.ID); n != 2 {
		t.Fatalf("%d locations, want the two queued before the pause", n)
	}
	if last := p.int(`SELECT last_uid FROM folders WHERE account_id = $1 AND name = 'INBOX'`, acc.ID); last != 2 {
		t.Fatalf("INBOX last UID %d", last)
	}
}

// diskGate is a gate that reads the figures at every check (its clock
// moves an hour each time) and finds the disk below its reserve from the
// n-th read of the free space on: with n = 4, at the third message after
// the check at the start.
func (p *pauseFixture) diskGate(n int) *quota.Gate {
	var (
		mu    sync.Mutex
		calls int
	)
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		p.now = p.now.Add(time.Hour)
		return p.now
	}
	free := func() (int64, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls >= n {
			return 1, nil
		}
		return 1 << 40, nil
	}
	g := quota.New(quota.Config{Thresholds: quota.Thresholds{WarnPercent: 80, OverflowPercent: 10}, MinFreeDisk: 1 << 30,
		Interval: time.Minute}, p.store, free, clock, slog.New(slog.NewTextHandler(io.Discard, nil)))
	p.store.SetWriteObserver(g.Wrote)
	return g
}

// An import and an import into the journal folder that pause with
// messages read but not saved yet save them first.
func TestImportPauseSavesTheBatch(t *testing.T) {
	p := newPauseFixture(t)
	imp := &store.Account{Kind: store.KindImport, Name: "old", OwnerID: &p.ann.ID}
	if err := p.store.CreateAccount(p.ctx, imp); err != nil {
		t.Fatal(err)
	}
	jnl := &store.Account{Name: "journal", Enabled: true, OwnerID: &p.ann.ID}
	if err := p.store.CreateSMTPAccount(p.ctx, jnl, []string{"archive@archive.local"}); err != nil {
		t.Fatal(err)
	}
	newImporter := func(n int) *archive.Importer {
		return &archive.Importer{Store: p.store, Blobs: p.blobs, BatchSize: 10, Storage: p.diskGate(n),
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	}
	folders, _, err := archive.ImportFolders("mbox", writeMbox(t, "bat", 5), "")
	if err != nil {
		t.Fatal(err)
	}
	acc, err := p.store.GetAccount(p.ctx, imp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := newImporter(4).Import(p.ctx, acc, folders); !errors.Is(err, quota.ErrStorageFull) || res.Total().Added != 2 {
		t.Fatalf("import: %+v, %v", res, err)
	}
	if n := p.locations(imp.ID); n != 2 {
		t.Fatalf("import: %d locations, want the two read before the pause", n)
	}
	jfolders, _, err := archive.JournalImportFolders("mbox", writeMbox(t, "jbat", 5))
	if err != nil {
		t.Fatal(err)
	}
	si := &store.SMTPImport{AccountID: jnl.ID, Label: "old", Format: "mbox", Source: "x"}
	if err := p.store.CreateSMTPImport(p.ctx, si); err != nil {
		t.Fatal(err)
	}
	// The first read of this gate also ends the disk_low the first one
	// persisted, with a second statfs under the row lock: one read more.
	if res, err := newImporter(5).ImportJournal(p.ctx, jnl, si, jfolders); !errors.Is(err, quota.ErrStorageFull) || res.Total().Added != 2 {
		t.Fatalf("journal import: %+v, %v", res, err)
	}
	if n := p.locations(jnl.ID); n != 2 {
		t.Fatalf("journal import: %d locations", n)
	}
}
