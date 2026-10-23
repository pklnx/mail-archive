package archive_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/imaptest"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/testwait"
)

// clock is a settable time for recurring fills.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func (f *fixture) createRecurringFill(acc *store.Account, password string, confirmed ...string) *store.Fill {
	f.t.Helper()
	fill := &store.Fill{
		AccountID: acc.ID, Host: f.host, Port: f.port, TLSMode: store.TLSModeNone, Username: "alice@example.com",
		KeepRunning: true, Confirmed: confirmed,
	}
	seal := func(id int64) ([]byte, error) { return archive.SealFillPassword(f.sealer, id, password) }
	if err := f.store.CreateFill(f.ctx, fill, seal); err != nil {
		f.t.Fatal(err)
	}
	return fill
}

// recurringFiller is the fixture's filler on clock c.
func (f *fixture) recurringFiller(c *clock) *archive.Filler {
	filler := f.filler()
	filler.Now = c.Now
	return filler
}

// runFill runs the fill (its next run, if it is between two) and fails
// the test on an error.
func runFill(t *testing.T, filler *archive.Filler, id int64) *store.Fill {
	t.Helper()
	got, err := filler.Run(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func totals(f *store.Fill) store.FillCounts {
	return archive.FillTotals(f)
}

// recurringUser has an INBOX with, in UID order, an old message, an old
// journaled one, a young one and another old one.
func recurringUser(t *testing.T, now time.Time) *imapmemserver.User {
	u := imapmemserver.NewUser("alice@example.com", "secret")
	imaptest.CreateMailboxes(t, u, "INBOX")
	imaptest.AppendAt(t, u, "INBOX", fillMessage("old1"), now.Add(-48*time.Hour))
	imaptest.AppendAt(t, u, "INBOX", fillMessage("journaled"), now.Add(-48*time.Hour))
	imaptest.AppendAt(t, u, "INBOX", fillMessage("young"), now.Add(-30*time.Minute))
	imaptest.AppendAt(t, u, "INBOX", fillMessage("old2"), now.Add(-24*time.Hour))
	return u
}

// TestRecurringFill: the first run imports old messages that were not
// journaled and leaves young ones waiting, with LastUID before the first
// of them; the next run, two hours later, imports them without fetching
// the rest again. A message that shows up later with an old date is
// imported by the next run, a journaled one is not.
func TestRecurringFill(t *testing.T) {
	c := &clock{now: time.Now()}
	u := recurringUser(t, c.Now())
	f := newFixture(t, u)
	acc := f.smtpAccount()
	f.journal(acc, "journaled")
	fill := f.createRecurringFill(acc, "secret")
	filler := f.recurringFiller(c)
	f.srv.ResetCommands()

	got := runFill(t, filler, fill.ID)
	inbox := folderOf(got, "INBOX")
	if got.Status != store.FillOK || got.PasswordEnc == nil || !got.KeepRunning {
		t.Fatalf("after the first run %+v", got)
	}
	if want := (store.FillCounts{Scanned: 4, Imported: 2, New: 2, Journaled: 1, Waiting: 1}); inbox.Counts != want || inbox.LastUID != 2 || !inbox.Done {
		t.Errorf("INBOX %+v, want %+v and LastUID 2", inbox, want)
	}
	if b := f.srv.BodyFetches(); !slices.Equal(b, []string{"1", "4"}) {
		t.Errorf("body fetches %v", b)
	}
	if h, _ := f.store.FillHealth(f.ctx, nil); h[fill.ID] == nil || h[fill.ID].LastSuccessAt == nil {
		t.Errorf("health %+v", h[fill.ID])
	}

	// Two hours later: the young message is old enough; the one after it
	// is found without its body.
	c.Add(2 * time.Hour)
	f.srv.ResetCommands()
	got = runFill(t, filler, fill.ID)
	inbox = folderOf(got, "INBOX")
	if want := (store.FillCounts{Scanned: 2, Imported: 1, New: 1, Present: 1}); inbox.Counts != want || inbox.LastUID != 4 {
		t.Errorf("second run INBOX %+v, want %+v", inbox, want)
	}
	if b := f.srv.BodyFetches(); !slices.Equal(b, []string{"3"}) {
		t.Errorf("body fetches of the second run %v", b)
	}

	// A message with an old date moved into the mailbox later, and one that
	// was journaled.
	imaptest.AppendAt(t, u, "INBOX", fillMessage("late"), c.Now().Add(-30*24*time.Hour))
	f.journal(acc, "late-journaled")
	imaptest.AppendAt(t, u, "INBOX", fillMessage("late-journaled"), c.Now().Add(-30*24*time.Hour))
	got = runFill(t, filler, fill.ID)
	inbox = folderOf(got, "INBOX")
	if want := (store.FillCounts{Scanned: 2, Imported: 1, New: 1, Journaled: 1}); inbox.Counts != want || inbox.LastUID != 6 {
		t.Errorf("third run INBOX %+v, want %+v", inbox, want)
	}
	// Two journaled and four imported messages, each once.
	locs := f.journalLocations(acc)
	filled := 0
	for _, l := range locs {
		if l.filled {
			filled++
		}
	}
	if len(locs) != 6 || filled != 4 {
		t.Errorf("journal locations %+v", locs)
	}
}

// TestRecurringFillMoves: a message moved to another IMAP folder, a
// renamed folder and a folder whose UIDVALIDITY changed cost no body and
// add no location; a folder deleted in the mailbox is no error.
func TestRecurringFillMoves(t *testing.T) {
	c := &clock{now: time.Now()}
	u := imapmemserver.NewUser("alice@example.com", "secret")
	imaptest.CreateMailboxes(t, u, "INBOX", "Box", "Gone")
	old := c.Now().Add(-48 * time.Hour)
	imaptest.AppendAt(t, u, "INBOX", fillMessage("a"), old)
	imaptest.AppendAt(t, u, "INBOX", fillMessage("b"), old)
	imaptest.AppendAt(t, u, "Box", fillMessage("c"), old)
	imaptest.AppendAt(t, u, "Gone", fillMessage("d"), old)
	f := newFixture(t, u)
	acc := f.smtpAccount()
	fill := f.createRecurringFill(acc, "secret")
	filler := f.recurringFiller(c)
	if got := runFill(t, filler, fill.ID); totals(got).Imported != 4 {
		t.Fatalf("first run %+v", totals(got))
	}

	// "b" moves to a new folder (same bytes and INTERNALDATE), "Box" is
	// renumbered, "Gone" is deleted.
	imaptest.CreateMailboxes(t, u, "Clients/Miller")
	imaptest.AppendAt(t, u, "Clients/Miller", fillMessage("b"), old)
	imaptest.Expunge(t, f.host, f.port, "alice@example.com", "secret", "INBOX", 2)
	if err := u.Delete("Box"); err != nil {
		t.Fatal(err)
	}
	imaptest.CreateMailboxes(t, u, "Box")
	imaptest.AppendAt(t, u, "Box", fillMessage("c"), old)
	if err := u.Delete("Gone"); err != nil {
		t.Fatal(err)
	}
	f.srv.ResetCommands()
	got := runFill(t, filler, fill.ID)
	if got.Status != store.FillOK || got.Error != "" {
		t.Fatalf("second run %+v", got)
	}
	if b := f.srv.BodyFetches(); len(b) != 0 {
		t.Errorf("body fetches %v", b)
	}
	if cnt := folderOf(got, "Clients/Miller").Counts; cnt != (store.FillCounts{Scanned: 1, Present: 1}) {
		t.Errorf("moved message %+v", cnt)
	}
	if cnt := folderOf(got, "Box").Counts; cnt != (store.FillCounts{Scanned: 1, Present: 1}) {
		t.Errorf("renumbered folder %+v", cnt)
	}
	// The deleted folder keeps its row, with the counts of its last run.
	if gone := folderOf(got, "Gone"); gone.SourceName != "Gone" || gone.Error != "" {
		t.Errorf("deleted folder %+v", gone)
	}
	if locs := f.journalLocations(acc); len(locs) != 4 {
		t.Errorf("journal locations %+v", locs)
	}
}

// TestRecurringFillNewFolders: new folders are picked up, but a new trash
// or spam folder only when it was confirmed.
func TestRecurringFillNewFolders(t *testing.T) {
	c := &clock{now: time.Now()}
	u := imapmemserver.NewUser("alice@example.com", "secret")
	imaptest.CreateMailboxes(t, u, "INBOX", "Junk")
	old := c.Now().Add(-48 * time.Hour)
	imaptest.AppendAt(t, u, "INBOX", fillMessage("a"), old)
	imaptest.AppendAt(t, u, "Junk", fillMessage("spam1"), old)
	f := newFixture(t, u)
	f.host, f.port = imaptest.StartWithRoles(t, map[string]imap.MailboxAttr{"INBOX": "", "Junk": imap.MailboxAttrJunk}, u)
	acc := f.smtpAccount()
	// The user confirmed Junk when starting the fill.
	fill := f.createRecurringFill(acc, "secret", "Junk")
	filler := f.recurringFiller(c)
	if got := runFill(t, filler, fill.ID); totals(got).Imported != 2 || len(got.Unconfirmed) != 0 {
		t.Fatalf("first run %+v, unconfirmed %v", totals(got), got.Unconfirmed)
	}

	// Later the mailbox has a new folder and a new trash folder.
	imaptest.CreateMailboxes(t, u, "Projects", "Deleted")
	imaptest.AppendAt(t, u, "Projects", fillMessage("p"), old)
	imaptest.AppendAt(t, u, "Deleted", fillMessage("trash"), old)
	imaptest.AppendAt(t, u, "Junk", fillMessage("spam2"), old)
	host, port := imaptest.StartWithRoles(t, map[string]imap.MailboxAttr{
		"INBOX": "", "Junk": imap.MailboxAttrJunk, "Projects": "", "Deleted": imap.MailboxAttrTrash,
	}, u)
	conn, err := pgx.Connect(f.ctx, f.dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(f.ctx) }()
	if _, err := conn.Exec(f.ctx, "UPDATE smtp_fills SET port = $1, host = $2 WHERE id = $3", port, host, fill.ID); err != nil {
		t.Fatal(err)
	}
	got := runFill(t, filler, fill.ID)
	if got.Status != store.FillOK || !slices.Equal(got.Unconfirmed, []string{"Deleted"}) {
		t.Fatalf("second run %s, unconfirmed %v", got.Status, got.Unconfirmed)
	}
	if folderOf(got, "Projects").Counts.Imported != 1 || folderOf(got, "Junk").Counts.Imported != 1 || folderOf(got, "Deleted").SourceName != "" {
		t.Errorf("folders %+v", got.Folders)
	}
	if !strings.Contains(f.logs.String(), "new trash or spam folder skipped") {
		t.Error("the skip is not logged")
	}
}

// TestRecurringFillStop: stopped between two batches, the fill ends
// cancelled without its password, and its health stays as it was.
func TestRecurringFillStop(t *testing.T) {
	c := &clock{now: time.Now()}
	u := recurringUser(t, c.Now())
	f := newFixture(t, u)
	acc := f.smtpAccount()
	fill := f.createRecurringFill(acc, "secret")
	filler := f.recurringFiller(c)
	runFill(t, filler, fill.ID)
	before, _ := f.store.FillHealth(f.ctx, nil)

	filler.Progress = func(store.FillFolder) {
		if err := f.store.CancelFill(f.ctx, fill.ID); err != nil {
			t.Error(err)
		}
	}
	c.Add(3 * time.Hour)
	got := runFill(t, filler, fill.ID)
	if got.Status != store.FillCancelled || got.PasswordEnc != nil || got.KeepRunning {
		t.Fatalf("stopped %+v", got)
	}
	var streak int
	var last time.Time
	if err := pgxRow(f, "SELECT failure_streak, last_success_at FROM smtp_fill_health WHERE fill_id = $1", fill.ID).Scan(&streak, &last); err != nil ||
		streak != 0 || !last.Equal(*before[fill.ID].LastSuccessAt) {
		t.Errorf("health after the stop: %d, %v, %v", streak, last, err)
	}
	if _, err := filler.Run(f.ctx, fill.ID); err == nil {
		t.Error("a stopped fill ran")
	}
}

func pgxRow(f *fixture, sql string, args ...any) pgx.Row {
	f.t.Helper()
	conn, err := pgx.Connect(f.ctx, f.dbURL)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn.QueryRow(f.ctx, sql, args...)
}

// TestRecurringFillRestart: a run interrupted by a shutdown stays running
// with its password, and the fill runner continues it.
func TestRecurringFillRestart(t *testing.T) {
	c := &clock{now: time.Now()}
	u := recurringUser(t, c.Now())
	f := newFixture(t, u)
	acc := f.smtpAccount()
	fill := f.createRecurringFill(acc, "secret")
	filler := f.recurringFiller(c)
	ctx, cancel := context.WithCancel(f.ctx)
	filler.Progress = func(store.FillFolder) { cancel() }
	if _, err := filler.Run(ctx, fill.ID); err == nil {
		t.Fatal("no error from the interrupted run")
	}
	got, _ := f.store.GetFill(f.ctx, fill.ID)
	if got.Status != store.FillRunning || got.PasswordEnc == nil {
		t.Fatalf("after the shutdown %+v", got)
	}
	if h, _ := f.store.FillHealth(f.ctx, nil); h[fill.ID].LastSuccessAt != nil || h[fill.ID].FailureStreak != 0 {
		t.Errorf("health after the shutdown %+v", h[fill.ID])
	}

	filler.Progress = nil
	var ran testwait.Signal
	r := &archive.FillRunner{Filler: filler, CheckEvery: 20 * time.Millisecond, Now: c.Now, AfterRun: ran.Notify}
	rctx, rcancel := context.WithCancel(f.ctx)
	done := make(chan struct{})
	go func() { r.Run(rctx); close(done) }()
	t.Cleanup(func() { rcancel(); <-done })
	got = waitFill(t, f, &ran, fill.ID, store.FillOK)
	if totals(got).Imported != 3 || !got.KeepRunning {
		t.Errorf("continued %+v", totals(got))
	}
}

// TestRecurringFillConversion: a one-time fill from before #112 that is
// switched on copies its locations into JOURNAL without new files, then
// imports what its cut-off left out.
func TestRecurringFillConversion(t *testing.T) {
	f := newFixture(t, fillUser(t))
	acc := f.smtpAccount()
	f.journal(acc, "m2")
	f.journal(acc, "m5")
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
	if err != nil || got.Status != store.FillOK || folderOf(got, "INBOX").FolderName != "alice/INBOX" {
		t.Fatalf("legacy fill %+v, %v", got, err)
	}
	// m1, m3 and the message without an ID; m4 is after the cut-off.
	if c := folderOf(got, "INBOX").Counts; c.Imported != 3 || c.AfterCutoff != 1 {
		t.Fatalf("legacy counts %+v", c)
	}
	blobs := f.countBlobFiles()

	seal := func(id int64) ([]byte, error) { return archive.SealFillPassword(f.sealer, id, "secret") }
	if err := f.store.SetFillRecurring(f.ctx, fill.ID, nil, nil, seal); err != nil {
		t.Fatal(err)
	}
	c := &clock{now: fillCutoff.Add(30 * 24 * time.Hour)}
	f.srv.ResetCommands()
	got = runFill(t, f.recurringFiller(c), fill.ID)
	inbox := folderOf(got, "INBOX")
	if got.Status != store.FillOK || inbox.FolderName != store.SMTPFolder {
		t.Fatalf("first recurring run %+v", got)
	}
	// Only m4, which the cut-off left out, is downloaded; the others are
	// found by the pre-check (the message without an ID by its bytes).
	if want := (store.FillCounts{Scanned: 6, Imported: 1, New: 1, Journaled: 2, Present: 3}); inbox.Counts != want {
		t.Errorf("INBOX %+v, want %+v", inbox.Counts, want)
	}
	if b := f.srv.BodyFetches(); !slices.Equal(b, []string{"4", "6"}) {
		t.Errorf("body fetches %v", b)
	}
	if n := f.countBlobFiles(); n != blobs+1 {
		t.Errorf("blob files %d, was %d", n, blobs)
	}
	locs := f.journalLocations(acc)
	if len(locs) != 6 {
		t.Fatalf("journal locations %+v", locs)
	}
	for i, l := range locs {
		if l.uid != int64(i+1) || l.filled != (i >= 2) || l.filled != (l.fillID == fill.ID) {
			t.Errorf("location %d: %+v", i, l)
		}
	}
	if !strings.Contains(f.logs.String(), "fill's folders copied into the journal folder") {
		t.Error("the copy is not logged")
	}
}

// TestRecurringFillHealth: runs with a wrong password fail and extend the
// streak; stopped and resumed with the right one, the streak ends.
func TestRecurringFillHealth(t *testing.T) {
	c := &clock{now: time.Now()}
	f := newFixture(t, recurringUser(t, c.Now()))
	acc := f.smtpAccount()
	fill := f.createRecurringFill(acc, "wrong")
	filler := f.recurringFiller(c)
	for i := range 3 {
		got := runFill(t, filler, fill.ID)
		if got.Status != store.FillFailed || got.Error == "" || got.PasswordEnc == nil {
			t.Fatalf("run %d: %+v", i, got)
		}
	}
	h, _ := f.store.FillHealth(f.ctx, nil)
	if h[fill.ID].FailureStreak != 3 || h[fill.ID].State(3, 6*time.Hour, nil, time.Now()) != store.HealthFailing {
		t.Fatalf("health %+v", h[fill.ID])
	}
	if err := f.store.CancelFill(f.ctx, fill.ID); err != nil {
		t.Fatal(err)
	}
	seal := func(id int64) ([]byte, error) { return archive.SealFillPassword(f.sealer, id, "secret") }
	if err := f.store.ResumeFill(f.ctx, fill.ID, seal); err != nil {
		t.Fatal(err)
	}
	if got := runFill(t, filler, fill.ID); got.Status != store.FillOK {
		t.Fatalf("after the resume %+v", got)
	}
	h, _ = f.store.FillHealth(f.ctx, nil)
	if h[fill.ID].FailureStreak != 0 || h[fill.ID].LastSuccessAt == nil {
		t.Errorf("health after the resume %+v", h[fill.ID])
	}
}

// TestFillRunnerRecurring: the runner starts due recurring fills on the
// schedule, not before the hold after its start, not for disabled
// accounts, and not with the schedule off.
func TestFillRunnerRecurring(t *testing.T) {
	c := &clock{now: time.Now()}
	f := newFixture(t, recurringUser(t, c.Now()))
	acc := f.smtpAccount()
	fill := f.createRecurringFill(acc, "secret")
	filler := f.recurringFiller(c)
	runFill(t, filler, fill.ID)
	// The runner reads the clock through its own counter (the Filler and
	// the test read c directly), so the test can wait until the runner has
	// looked at the schedule: checks waits for twice the reads of one loop
	// iteration in the runner's configuration, so at least one whole
	// iteration read the clock after the change before it.
	var runs, reads atomic.Int64
	var ran, read testwait.Signal
	start := func(r *archive.FillRunner) func() {
		r.Filler, r.CheckEvery = filler, 10*time.Millisecond
		r.Now = func() time.Time {
			reads.Add(1)
			read.Notify()
			return c.Now()
		}
		r.AfterRun = func() { runs.Add(1); ran.Notify() }
		ctx, cancel := context.WithCancel(f.ctx)
		done := make(chan struct{})
		go func() { r.Run(ctx); close(done) }()
		return func() { cancel(); <-done }
	}
	checks := func(perIteration int64, what string) {
		t.Helper()
		from := reads.Load()
		testwait.Until(t, &read, what, func() bool { return reads.Load() >= from+2*perIteration })
	}
	lastStart := func() time.Time {
		got, _ := f.store.GetFill(f.ctx, fill.ID)
		return *got.RunStartedAt
	}
	first := lastStart()
	sched := archive.Schedule{Sync: 6 * time.Hour, SyncMin: 5 * time.Minute}

	// Not due yet; then due, but held after the start. Held, an iteration
	// reads the clock twice (held() for the running and for the due fills).
	stop := start(&archive.FillRunner{Schedule: sched, HoldUntil: c.Now().Add(7 * time.Hour)})
	checks(2, "the schedule checks before it is due")
	c.Add(6*time.Hour + time.Minute)
	checks(2, "the schedule checks during the hold")
	if !lastStart().Equal(first) {
		t.Fatal("ran before it was due or during the hold")
	}
	// After the hold it runs once.
	c.Add(time.Hour)
	testwait.Until(t, &ran, "the run after the hold", func() bool { return runs.Load() >= 1 })
	stop()
	got, err := f.store.GetFill(f.ctx, fill.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.FillOK || got.RunStartedAt.Equal(first) || runs.Load() != 1 {
		t.Fatalf("after the hold: %s, %v, %d runs", got.Status, got.RunStartedAt, runs.Load())
	}

	// The schedule off, or the account disabled: no run.
	second := lastStart()
	c.Add(7 * time.Hour)
	// The schedule off, without a hold: one read per iteration (held() for
	// the running fills).
	stop = start(&archive.FillRunner{})
	checks(1, "the checks with the schedule off")
	stop()
	off := false
	if err := f.store.UpdateAccount(f.ctx, acc.Ref(), store.AccountChange{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	// The schedule on, without a hold: three reads per iteration (held()
	// twice, dueFills once).
	stop = start(&archive.FillRunner{Schedule: sched})
	checks(3, "the schedule checks with the account disabled")
	stop()
	if !lastStart().Equal(second) {
		t.Error("ran with the schedule off or the account disabled")
	}
	// A disabled account's fill is skipped without a change, also when
	// queued directly.
	if _, err := filler.Run(f.ctx, fill.ID); !errors.Is(err, archive.ErrFillSkipped) {
		t.Errorf("run of a disabled account's fill: %v", err)
	}
}

// TestRecurringFillSwitchedOn: a one-time fill into JOURNAL, switched on,
// scans its folders again and imports what its cut-off left out, without
// downloading what it imported before.
func TestRecurringFillSwitchedOn(t *testing.T) {
	f := newFixture(t, fillUser(t))
	acc := f.smtpAccount()
	f.journal(acc, "m2")
	f.journal(acc, "m5")
	fill := f.createFill(acc, "secret", "Spam", "Sent")
	got, err := f.filler().Run(f.ctx, fill.ID)
	if err != nil || got.Status != store.FillOK {
		t.Fatalf("one-time fill %+v, %v", got, err)
	}
	// m1, m3 and the message without an ID; m4 is after the cut-off.
	if c := folderOf(got, "INBOX"); c.Counts.Imported != 3 || c.Counts.AfterCutoff != 1 || c.LastUID != 6 {
		t.Fatalf("one-time INBOX %+v", c)
	}
	seal := func(id int64) ([]byte, error) { return archive.SealFillPassword(f.sealer, id, "secret") }
	if err := f.store.SetFillRecurring(f.ctx, fill.ID, nil, nil, seal); err != nil {
		t.Fatal(err)
	}
	c := &clock{now: fillCutoff.Add(30 * 24 * time.Hour)}
	f.srv.ResetCommands()
	got = runFill(t, f.recurringFiller(c), fill.ID)
	inbox := folderOf(got, "INBOX")
	if want := (store.FillCounts{Scanned: 6, Imported: 1, New: 1, Journaled: 2, Present: 3}); got.Status != store.FillOK || inbox.Counts != want || inbox.LastUID != 6 {
		t.Errorf("first recurring run %s %+v, want %+v", got.Status, inbox, want)
	}
	if b := f.srv.BodyFetches(); !slices.Equal(b, []string{"4", "6"}) {
		t.Errorf("body fetches %v", b)
	}
	if locs := f.journalLocations(acc); len(locs) != 6 {
		t.Errorf("journal locations %+v", locs)
	}
}

// TestRecurringFillDisabledAccount: a recurring fill whose account is
// disabled does not run, also when its run was already started: no state
// change, no failure, no health.
func TestRecurringFillDisabledAccount(t *testing.T) {
	c := &clock{now: time.Now()}
	f := newFixture(t, recurringUser(t, c.Now()))
	acc := f.smtpAccount()
	off := false
	if err := f.store.UpdateAccount(f.ctx, acc.Ref(), store.AccountChange{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	fill := f.createRecurringFill(acc, "secret")
	filler := f.recurringFiller(c)
	if _, err := filler.Run(f.ctx, fill.ID); !errors.Is(err, archive.ErrFillSkipped) {
		t.Fatalf("run: %v", err)
	}
	got, _ := f.store.GetFill(f.ctx, fill.ID)
	if got.Status != store.FillRunning || got.PasswordEnc == nil || len(got.Folders) != 0 {
		t.Errorf("after the skipped run %+v", got)
	}
	if h, _ := f.store.FillHealth(f.ctx, nil); h[fill.ID].LastSuccessAt != nil || h[fill.ID].FailureStreak != 0 {
		t.Errorf("health %+v", h[fill.ID])
	}
	// Enabled again, it runs.
	on := true
	acc, _ = f.store.GetAccount(f.ctx, acc.ID)
	if err := f.store.UpdateAccount(f.ctx, acc.Ref(), store.AccountChange{Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	if got := runFill(t, filler, fill.ID); got.Status != store.FillOK {
		t.Errorf("after enabling %+v", got)
	}
}
