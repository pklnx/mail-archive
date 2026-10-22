package store_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// purgeFixture has alice's account "old" (A) with m1 in INBOX and Archive
// (two locations), m2 in INBOX, also in bob's "mail", and m3 in Archive,
// also in alice's "work". A is removed and has no rule.
type purgeFixture struct {
	*retentionFixture
	acc                   *store.Account
	inbox, archive        *store.Folder
	bobInbox, aliceWork   *store.Folder
	m1, m2, m3, unrelated string
}

func newPurgeFixture(t *testing.T) *purgeFixture {
	t.Helper()
	f := &purgeFixture{retentionFixture: newRetentionFixture(t)}
	f.m1, f.m2, f.m3, f.unrelated = hashOf('1'), hashOf('2'), hashOf('3'), hashOf('4')
	f.inbox = f.folder(f.alice, "old", "INBOX")
	f.archive = f.folder(f.alice, "old", "Archive")
	f.bobInbox = f.folder(f.bob, "mail", "INBOX")
	f.aliceWork = f.folder(f.alice, "work", "INBOX")
	f.save(f.inbox, f.m1, "zebrafish report", old, old)
	f.save(f.archive, f.m1, "zebrafish report", old, old)
	f.save(f.inbox, f.m2, "shared with bob", old, old)
	f.save(f.bobInbox, f.m2, "shared with bob", old, old)
	f.save(f.archive, f.m3, "also at work", old, old)
	f.save(f.aliceWork, f.m3, "also at work", old, old)
	f.save(f.aliceWork, f.unrelated, "unrelated", old, old)
	f.acc = f.account(f.alice, "old")
	if _, err := f.st.DeleteOrRemoveAccount(f.ctx, f.acc.Ref()); err != nil {
		t.Fatal(err)
	}
	f.acc = f.account(f.alice, "old")
	if f.acc.RemovedAt == nil {
		t.Fatal("not removed")
	}
	return f
}

func (f *purgeFixture) account(owner *store.User, name string) *store.Account {
	f.t.Helper()
	a, err := f.st.GetOwnedAccount(f.ctx, owner.ID, name)
	if err != nil {
		f.t.Fatal(err)
	}
	return a
}

func (f *purgeFixture) preview() *store.PurgePreview {
	f.t.Helper()
	p, err := f.st.PurgePreview(f.ctx, f.acc.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return p
}

func (f *purgeFixture) confirm(expected int64) *store.AccountPurge {
	f.t.Helper()
	p, err := f.st.ConfirmPurge(f.ctx, f.acc.Ref(), expected, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	return p
}

// run executes a purge with the store's steps, as archive.ExecutePurge
// does, without its locks.
func (f *purgeFixture) run(id int64, batch int, grace time.Duration) (*store.AccountPurge, error) {
	f.t.Helper()
	if err := f.st.CheckPurgeCount(f.ctx, id); err != nil {
		return nil, err
	}
	for {
		res, err := f.st.PurgeBatch(f.ctx, id, batch, grace)
		if err != nil {
			return nil, err
		}
		if res.Done {
			break
		}
	}
	return f.st.FinishPurge(f.ctx, id)
}

// rows returns the rows of a query as text, for before/after comparisons.
func rowsOf(t *testing.T, c *pgx.Conn, sql string, args ...any) []string {
	t.Helper()
	rows, err := c.Query(context.Background(), "SELECT row_to_json(x)::text FROM ("+sql+") x ORDER BY 1", args...)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func countOf(t *testing.T, c *pgx.Conn, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := c.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPurgePreview(t *testing.T) {
	f := newPurgeFixture(t)
	p := f.preview()
	if p.Locations != 4 || p.Messages != 3 || p.Deleted != 1 || p.DeletedBytes != 1 || p.Kept != 2 || p.Owner != "alice" ||
		p.Account.Name != "old" || p.Blockers.Any() || p.Open != nil {
		t.Fatalf("preview %+v", p)
	}
	want := []store.PurgeKept{{Owner: "alice", Account: "work", Messages: 1}, {Owner: "bob", Account: "mail", Messages: 1}}
	if !slices.Equal(p.KeptIn, want) {
		t.Errorf("kept in %+v", p.KeptIn)
	}
	// Nothing changed.
	if q := f.preview(); q.Locations != 4 {
		t.Errorf("second preview %+v", q)
	}
}

// TestPurge is the main flow: shared messages stay unchanged, the message
// whose last location was in the account is deleted like a retention
// deletion, and the account is gone everywhere.
func TestPurge(t *testing.T) {
	f := newPurgeFixture(t)
	c := connect(t, f.url)
	shared := `SELECT l.* FROM message_locations l JOIN folders fo ON fo.id = l.folder_id
	           WHERE l.message_sha256 IN ($1, $2) AND fo.account_id <> $3`
	sharedMsgs := `SELECT * FROM messages WHERE sha256 IN ($1, $2)`
	beforeLocs := rowsOf(t, c, shared, f.m2, f.m3, f.acc.ID)
	beforeMsgs := rowsOf(t, c, sharedMsgs, f.m2, f.m3)
	if len(beforeLocs) != 2 || len(beforeMsgs) != 2 {
		t.Fatalf("fixture %v %v", beforeLocs, beforeMsgs)
	}

	e := &store.AuditEntry{Actor: store.ActorCLI, Action: "account.purge"}
	p, err := f.st.ConfirmPurge(store.WithAudit(f.ctx, e), f.acc.Ref(), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.State != store.PurgeConfirmed || p.Expected != 1 || p.Account != "old" || p.Owner != "alice" || p.AccountVersion != f.acc.Version {
		t.Fatalf("confirmed %+v", p)
	}
	if e.TargetType != store.TargetAccount || e.TargetName != "old" || e.Details["deleted"] != int64(1) || e.Details["kept"] != int64(2) {
		t.Errorf("audit %+v", e)
	}
	if q := f.preview(); q.Open == nil || q.Open.ID != p.ID {
		t.Errorf("open purge not in the preview: %+v", q)
	}
	start := time.Now()
	done, err := f.run(p.ID, 500, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if done.State != store.PurgeDone || done.Locations != 4 || done.Deleted != 1 || done.Kept != 2 || done.FinishedAt == nil {
		t.Fatalf("done %+v", done)
	}

	// Shared messages: byte for byte unchanged, still visible to their
	// owners, no tombstone and no file removal.
	if got := rowsOf(t, c, shared, f.m2, f.m3, f.acc.ID); !slices.Equal(got, beforeLocs) {
		t.Errorf("shared locations changed:\n%v\n%v", beforeLocs, got)
	}
	if got := rowsOf(t, c, sharedMsgs, f.m2, f.m3); !slices.Equal(got, beforeMsgs) {
		t.Errorf("shared messages changed")
	}
	if d := f.detail(f.bob, f.m2); len(d.Locations) != 1 {
		t.Errorf("m2 for bob %+v", d.Locations)
	}
	if d := f.detail(f.alice, f.m3); len(d.Locations) != 1 || d.Locations[0].Account != "work" {
		t.Errorf("m3 for alice %+v", d.Locations)
	}
	if n := countOf(t, c, "SELECT count(*) FROM deleted_messages WHERE sha256 IN ($1, $2)", f.m2, f.m3) +
		countOf(t, c, "SELECT count(*) FROM blob_removals WHERE sha256 IN ($1, $2)", f.m2, f.m3) +
		countOf(t, c, "SELECT count(*) FROM message_deletions WHERE sha256 IN ($1, $2)", f.m2, f.m3); n != 0 {
		t.Errorf("records for shared messages: %d", n)
	}

	// m1: deleted like a retention deletion.
	var (
		locations         int
		purgeID           *int64
		runID             *int64
		retainUntil       *time.Time
		size, seq, delSeq int64
		reason            string
		due               time.Time
	)
	if err := c.QueryRow(f.ctx, `SELECT locations, purge_id, run_id, retain_until, size, message_seq FROM deleted_messages WHERE sha256 = $1`,
		f.m1).Scan(&locations, &purgeID, &runID, &retainUntil, &size, &seq); err != nil {
		t.Fatal(err)
	}
	if locations != 2 || purgeID == nil || *purgeID != p.ID || runID != nil || retainUntil != nil || size != 1 || seq == 0 {
		t.Errorf("tombstone %d %v %v %v %d", locations, purgeID, runID, retainUntil, size)
	}
	if err := c.QueryRow(f.ctx, `SELECT reason, message_seq FROM message_deletions WHERE sha256 = $1`, f.m1).Scan(&reason, &delSeq); err != nil ||
		reason != "account_purge" || delSeq != seq {
		t.Errorf("deletion record %q %d %v", reason, delSeq, err)
	}
	if err := c.QueryRow(f.ctx, `SELECT due_at FROM blob_removals WHERE sha256 = $1`, f.m1).Scan(&due); err != nil ||
		due.Before(start.Add(time.Hour-time.Minute)) || due.After(time.Now().Add(time.Hour)) {
		t.Errorf("file removal due %v %v", due, err)
	}
	if n := countOf(t, c, "SELECT count(*) FROM messages WHERE sha256 = $1", f.m1); n != 0 {
		t.Error("m1 still there")
	}
	if ok, err := f.st.IsTombstoned(f.ctx, f.m1); err != nil || !ok {
		t.Errorf("tombstoned %v %v", ok, err)
	}

	// The account is gone, its name is free.
	if _, err := f.st.GetAccount(f.ctx, f.acc.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("account: %v", err)
	}
	if n := countOf(t, c, "SELECT count(*) FROM folders WHERE account_id = $1", f.acc.ID); n != 0 {
		t.Errorf("%d folders left", n)
	}
	again := &store.Account{Name: "old", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, OwnerID: &f.alice.ID}
	if err := f.st.CreateAccount(f.ctx, again); err != nil {
		t.Errorf("name not free: %v", err)
	}
	// The record stays.
	if _, err := c.Exec(f.ctx, "DELETE FROM account_purges"); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Errorf("delete a purge: %v", err)
	}
	if _, err := f.st.ConfirmPurge(f.ctx, f.acc.Ref(), 0, nil); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("confirm a purged account: %v", err)
	}
}

// TestPurgeRefused: the purge of an account that is not removed, has a
// rule, or is named or covered by a hold is refused, names the reason
// and changes nothing.
func TestPurgeRefused(t *testing.T) {
	f := newPurgeFixture(t)
	c := connect(t, f.url)
	state := func() string {
		return fmt.Sprint(countOf(t, c, "SELECT count(*) FROM message_locations"), countOf(t, c, "SELECT count(*) FROM messages"),
			countOf(t, c, "SELECT count(*) FROM deleted_messages"), countOf(t, c, "SELECT count(*) FROM account_purges"),
			countOf(t, c, "SELECT count(*) FROM folders"), countOf(t, c, "SELECT count(*) FROM accounts"))
	}
	before := state()
	refused := func(name string, ref store.AccountRef, sentinel error, words ...string) {
		t.Helper()
		_, err := f.st.ConfirmPurge(f.ctx, ref, 1, nil)
		if !errors.Is(err, sentinel) {
			t.Errorf("%s: %v, want %v", name, err, sentinel)
			return
		}
		for _, w := range words {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: %q lacks %q", name, err, w)
			}
		}
		if s := state(); s != before {
			t.Errorf("%s changed something: %s -> %s", name, before, s)
		}
	}

	// Not removed: active and disabled accounts.
	work := f.account(f.alice, "work")
	refused("active", work.Ref(), store.ErrNotRemoved, `account "work" is not removed`)
	if err := f.st.UpdateAccount(f.ctx, work.Ref(), store.AccountChange{Enabled: new(bool)}); err != nil {
		t.Fatal(err)
	}
	work = f.account(f.alice, "work")
	refused("disabled", work.Ref(), store.ErrNotRemoved, "not removed")
	if p, _ := f.st.PurgePreview(f.ctx, work.ID); !p.Blockers.NotRemoved {
		t.Error("preview of an active account")
	}

	// Rules: of the account, of a folder, of a folder it does not have.
	for _, folder := range []string{"", "INBOX", "Elsewhere"} {
		id := f.rule(f.inbox, folder, 10)
		want := "all folders"
		if folder != "" {
			want = fmt.Sprintf("folder %q", folder)
		}
		refused("rule "+folder, f.acc.Ref(), store.ErrRetained, fmt.Sprintf("#%d (%s, 10 years)", id, want))
		if p := f.preview(); len(p.Blockers.Rules) != 1 || p.Blockers.Rules[0].ID != id {
			t.Errorf("preview rules %+v", p.Blockers.Rules)
		}
		if err := f.st.RemoveRetentionRule(f.ctx, id); err != nil {
			t.Fatal(err)
		}
	}

	// Active holds covering the account's mail.
	period := ts("2010-01-01T00:00:00Z")
	periodEnd := ts("2011-01-01T00:00:00Z")
	for _, c := range []struct {
		name string
		hold store.NewLegalHold
	}{
		{"owner", store.NewLegalHold{OwnerID: &f.alice.ID}},
		{"everything", store.NewLegalHold{}},
		{"period", store.NewLegalHold{PeriodStart: period, PeriodEnd: periodEnd}},
		{"filter on m1", store.NewLegalHold{OwnerID: &f.alice.ID, Filter: &store.HoldFilter{Query: "zebrafish"}}},
		// Only the location in this account counts: m2 is held in it.
		{"filter on a shared message", store.NewLegalHold{OwnerID: &f.alice.ID, Filter: &store.HoldFilter{Query: "bob"}}},
	} {
		c.hold.Name = "Case " + c.name
		id := f.hold(c.hold)
		refused(c.name, f.acc.Ref(), store.ErrOnHold, fmt.Sprintf(`legal hold #%d "Case %s" covers mail of account "old"`, id, c.name))
		if p := f.preview(); len(p.Blockers.Holds) != 1 || p.Blockers.Holds[0].ID != id || p.Blockers.Holds[0].Released {
			t.Errorf("%s: preview holds %+v", c.name, p.Blockers.Holds)
		}
		if err := f.st.ReleaseLegalHold(f.ctx, id, nil, "done"); err != nil {
			t.Fatal(err)
		}
	}

	// Not refused: a filter hold matching nothing in the account, a hold
	// on bob that covers only his copy of m2, a period that does not
	// match.
	nothing := f.hold(store.NewLegalHold{OwnerID: &f.alice.ID, Filter: &store.HoldFilter{Query: "unrelated"}})
	bobs := f.hold(store.NewLegalHold{OwnerID: &f.bob.ID})
	later := f.hold(store.NewLegalHold{PeriodStart: ts("2020-01-01T00:00:00Z")})
	if _, err := f.st.MatchLegalHolds(f.ctx); err != nil {
		t.Fatal(err)
	}
	if p := f.preview(); p.Blockers.Any() {
		t.Errorf("blocked by holds that do not cover the account: %+v", p.Blockers)
	}
	for _, id := range []int64{nothing, bobs, later} {
		if err := f.st.ReleaseLegalHold(f.ctx, id, nil, "done"); err != nil {
			t.Fatal(err)
		}
	}

	// A hold naming the account keeps it, also once released (maintainer
	// decision on #155).
	named := f.hold(store.NewLegalHold{Name: "Case A", AccountID: &f.acc.ID})
	refused("named", f.acc.Ref(), store.ErrOnHold, fmt.Sprintf(`legal hold #%d "Case A"`, named))
	if err := f.st.ReleaseLegalHold(f.ctx, named, nil, "closed"); err != nil {
		t.Fatal(err)
	}
	refused("released", f.acc.Ref(), store.ErrOnHold,
		fmt.Sprintf(`released legal hold #%d "Case A" names account "old"; its record keeps the account`, named))
	if p := f.preview(); len(p.Blockers.Holds) != 1 || !p.Blockers.Holds[0].Released || !p.Blockers.Holds[0].NamesAccount {
		t.Errorf("preview released hold %+v", p.Blockers.Holds)
	}
}

func TestPurgeBusyAndOpen(t *testing.T) {
	f := newPurgeFixture(t)
	unlock, ok, err := f.st.TryLockSync(f.ctx, f.acc.ID)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if p := f.preview(); !p.Blockers.Syncing {
		t.Error("preview while syncing")
	}
	if _, err := f.st.ConfirmPurge(f.ctx, f.acc.Ref(), 1, nil); !errors.Is(err, store.ErrAccountBusy) ||
		!strings.Contains(err.Error(), "being synced, filled or imported into") {
		t.Errorf("confirm while syncing: %v", err)
	}
	unlock()

	// A second purge while one is open: of the same account, and of
	// another one.
	p := f.confirm(1)
	if _, err := f.st.ConfirmPurge(f.ctx, f.acc.Ref(), 1, nil); !errors.Is(err, store.ErrPurgeOpen) {
		t.Errorf("confirm twice: %v", err)
	}
	other := f.folder(f.bob, "other", "INBOX")
	f.save(other, hashOf('9'), "x", old, old)
	acc := f.account(f.bob, "other")
	if _, err := f.st.DeleteOrRemoveAccount(f.ctx, acc.Ref()); err != nil {
		t.Fatal(err)
	}
	acc = f.account(f.bob, "other")
	if _, err := f.st.ConfirmPurge(f.ctx, acc.Ref(), 1, nil); !errors.Is(err, store.ErrPurgeOpen) ||
		!strings.Contains(err.Error(), fmt.Sprintf(`purge %d of account "old" is open`, p.ID)) {
		t.Errorf("confirm another account: %v", err)
	}
	// The purge lock lets one process execute.
	unlockPurge, ok, err := f.st.TryLockPurge(f.ctx)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if _, ok, _ := f.st.TryLockPurge(f.ctx); ok {
		t.Error("purge lock taken twice")
	}
	unlockPurge()
}

// TestPurgeCount: the confirmed count must match, and a message that got a
// location elsewhere after the confirmation is kept.
func TestPurgeCount(t *testing.T) {
	f := newPurgeFixture(t)
	var changed *store.PurgeChangedError
	if _, err := f.st.ConfirmPurge(f.ctx, f.acc.Ref(), 2, nil); !errors.As(err, &changed) || changed.Now != 1 || changed.Expected != 2 ||
		!strings.Contains(err.Error(), "is now 1, not 2") {
		t.Fatalf("stale count: %v", err)
	}
	if open, err := f.st.OpenAccountPurge(f.ctx); err != nil || open != nil {
		t.Errorf("open purge after a refusal: %+v %v", open, err)
	}
	p := f.confirm(1)
	// m1 is archived again in bob's account meanwhile.
	f.save(f.bobInbox, f.m1, "zebrafish report", old, old)
	done, err := f.run(p.ID, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if done.Deleted != 0 || done.Kept != 3 || done.Locations != 4 {
		t.Errorf("done %+v", done)
	}
	if d := f.detail(f.bob, f.m1); len(d.Locations) != 1 {
		t.Errorf("m1 for bob %+v", d.Locations)
	}
}

// TestPurgeCountGrew: more messages to delete than confirmed stop the
// purge before and in a batch.
func TestPurgeCountGrew(t *testing.T) {
	f := newPurgeFixture(t)
	c := connect(t, f.url)
	p := f.confirm(1)
	if _, err := c.Exec(f.ctx, "UPDATE account_purges SET expected = 0 WHERE id = $1", p.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.st.CheckPurgeCount(f.ctx, p.ID); !errors.Is(err, store.ErrPurgeChanged) {
		t.Errorf("check: %v", err)
	}
	// Batches in hash order: m1 comes first and would exceed 0.
	if _, err := f.st.PurgeBatch(f.ctx, p.ID, 1, 0); !errors.Is(err, store.ErrPurgeChanged) {
		t.Errorf("batch: %v", err)
	}
	if n := countOf(t, c, "SELECT count(*) FROM message_locations l JOIN folders fo ON fo.id = l.folder_id WHERE fo.account_id = $1", f.acc.ID); n != 4 {
		t.Errorf("%d locations left", n)
	}
}

// checkInvariants: every message has a location, every deleted one a
// tombstone and a deletion record.
func checkInvariants(t *testing.T, c *pgx.Conn) {
	t.Helper()
	if n := countOf(t, c, "SELECT count(*) FROM messages m WHERE NOT EXISTS (SELECT 1 FROM message_locations l WHERE l.message_sha256 = m.sha256)"); n != 0 {
		t.Errorf("%d messages without a location", n)
	}
	if n := countOf(t, c, `SELECT count(*) FROM deleted_messages d WHERE NOT EXISTS (SELECT 1 FROM message_deletions x WHERE x.sha256 = d.sha256)
	                       OR EXISTS (SELECT 1 FROM messages m WHERE m.sha256 = d.sha256)`); n != 0 {
		t.Errorf("%d tombstones without a deletion record or with a message", n)
	}
}

// TestPurgeBlockedMidway: a rule or hold added between batches, or before
// the end, stops the purge; what was done stays consistent.
func TestPurgeBlockedMidway(t *testing.T) {
	for _, c := range []struct {
		name  string
		block func(f *purgeFixture) int64
		err   error
	}{
		{"rule", func(f *purgeFixture) int64 { return f.rule(f.inbox, "", 10) }, store.ErrRetained},
		{"hold", func(f *purgeFixture) int64 { return f.hold(store.NewLegalHold{OwnerID: &f.alice.ID}) }, store.ErrOnHold},
		{"hold naming", func(f *purgeFixture) int64 { return f.hold(store.NewLegalHold{AccountID: &f.acc.ID}) }, store.ErrOnHold},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newPurgeFixture(t)
			conn := connect(t, f.url)
			p := f.confirm(1)
			if _, err := f.st.PurgeBatch(f.ctx, p.ID, 1, 0); err != nil {
				t.Fatal(err)
			}
			id := c.block(f)
			_, err := f.st.PurgeBatch(f.ctx, p.ID, 1, 0)
			var blocked *store.PurgeBlockedError
			if !errors.Is(err, c.err) || !errors.As(err, &blocked) || !strings.Contains(err.Error(), fmt.Sprintf("#%d", id)) {
				t.Fatalf("batch: %v", err)
			}
			if err := f.st.CancelPurge(f.ctx, p.ID, err.Error()); err != nil {
				t.Fatal(err)
			}
			got, _ := f.st.AccountPurge(f.ctx, p.ID)
			if got.State != store.PurgeCancelled || !strings.Contains(got.Note, fmt.Sprintf("#%d", id)) || got.Deleted != 1 || got.FinishedAt == nil {
				t.Errorf("cancelled %+v", got)
			}
			if a := f.account(f.alice, "old"); a.RemovedAt == nil {
				t.Error("account not removed")
			}
			checkInvariants(t, conn)
			if _, err := f.st.PurgeBatch(f.ctx, p.ID, 1, 0); !errors.Is(err, store.ErrPurgeState) {
				t.Errorf("batch of a cancelled purge: %v", err)
			}
			if err := f.st.CancelPurge(f.ctx, p.ID, ""); !errors.Is(err, store.ErrPurgeState) {
				t.Errorf("cancel twice: %v", err)
			}
		})
	}
}

// TestFinishPurgeRechecks (plan review finding 1): a rule added after the
// last batch is not dropped with the account.
func TestFinishPurgeRechecks(t *testing.T) {
	f := newPurgeFixture(t)
	c := connect(t, f.url)
	p := f.confirm(1)
	if res, err := f.st.PurgeBatch(f.ctx, p.ID, 100, 0); err != nil || !res.Done || res.Locations != 4 {
		t.Fatalf("batch %+v %v", res, err)
	}
	rule := f.rule(f.inbox, "", 10)
	if _, err := f.st.FinishPurge(f.ctx, p.ID); !errors.Is(err, store.ErrRetained) || !strings.Contains(err.Error(), fmt.Sprintf("#%d", rule)) {
		t.Fatalf("finish with a rule: %v", err)
	}
	if n := countOf(t, c, "SELECT count(*) FROM retention_rules WHERE id = $1", rule); n != 1 {
		t.Error("rule dropped")
	}
	if a := f.account(f.alice, "old"); a.RemovedAt == nil {
		t.Error("account not removed")
	}
	if err := f.st.RemoveRetentionRule(f.ctx, rule); err != nil {
		t.Fatal(err)
	}
	hold := f.hold(store.NewLegalHold{AccountID: &f.acc.ID})
	_ = f.st.ReleaseLegalHold(f.ctx, hold, nil, "done")
	if _, err := f.st.FinishPurge(f.ctx, p.ID); !errors.Is(err, store.ErrOnHold) {
		t.Fatalf("finish with a released hold naming the account: %v", err)
	}
}

// TestPurgeEmptyBatchNotAudited: a batch with nothing left to purge (a
// resume after the last batch) writes no entry; the entry stays for the
// next transaction.
func TestPurgeEmptyBatchNotAudited(t *testing.T) {
	f := newPurgeFixture(t)
	c := connect(t, f.url)
	p := f.confirm(1)
	if res, err := f.st.PurgeBatch(f.ctx, p.ID, 100, 0); err != nil || !res.Done {
		t.Fatalf("batch %+v %v", res, err)
	}
	e := &store.AuditEntry{Actor: store.ActorCLI, Action: "account.purge_batch"}
	actx := store.WithAudit(f.ctx, e)
	if res, err := f.st.PurgeBatch(actx, p.ID, 100, 0); err != nil || !res.Done || res.Locations != 0 {
		t.Fatalf("empty batch %+v %v", res, err)
	}
	if n := countOf(t, c, "SELECT count(*) FROM audit_log WHERE action = 'account.purge_batch'"); n != 0 {
		t.Errorf("empty batch wrote %d entries", n)
	}
	e.Action = "account.purge_finish"
	if _, err := f.st.FinishPurge(actx, p.ID); err != nil {
		t.Fatal(err)
	}
	if n := countOf(t, c, "SELECT count(*) FROM audit_log WHERE action = 'account.purge_finish'"); n != 1 {
		t.Errorf("finish wrote %d entries", n)
	}
}

// TestPurgeStale (plan review finding 4): a purge whose account moved to
// another user stops, and can be cancelled.
func TestPurgeStale(t *testing.T) {
	f := newPurgeFixture(t)
	p := f.confirm(1)
	if err := f.st.SetAccountOwner(f.ctx, f.acc.Ref(), f.bob.ID); err != nil {
		t.Fatal(err)
	}
	_, err := f.st.PurgeBatch(f.ctx, p.ID, 10, 0)
	var stale *store.PurgeStaleError
	if !errors.Is(err, store.ErrStale) || !errors.As(err, &stale) || !strings.Contains(err.Error(), "moved to another user") {
		t.Fatalf("batch after a move: %v", err)
	}
	if _, err := f.st.FinishPurge(f.ctx, p.ID); !errors.Is(err, store.ErrStale) {
		t.Errorf("finish after a move: %v", err)
	}
	if err := f.st.CancelPurge(f.ctx, p.ID, "moved"); err != nil {
		t.Fatal(err)
	}
	// A new purge, of the account as it is now.
	moved := f.account(f.bob, "old")
	if _, err := f.st.ConfirmPurge(f.ctx, f.acc.Ref(), 1, nil); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("confirm with the old ref: %v", err)
	}
	q, err := f.st.ConfirmPurge(f.ctx, moved.Ref(), 1, nil)
	if err != nil || q.Owner != "bob" {
		t.Fatalf("confirm again %+v %v", q, err)
	}
	f.acc = moved
	if _, err := f.run(q.ID, 10, 0); err != nil {
		t.Fatal(err)
	}
}

// TestPurgeGuard: the trigger lets a purge delete unruled locations only
// for its removed account with a confirmed purge, never a held one.
func TestPurgeGuard(t *testing.T) {
	f := newPurgeFixture(t)
	c := connect(t, f.url)
	work := f.account(f.alice, "work")
	try := func(name, setting, sha string, folder int64, wantOK bool) {
		t.Helper()
		tx, err := c.Begin(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(f.ctx) }()
		if setting != "" {
			if _, err := tx.Exec(f.ctx, "SELECT set_config('mail_archive.purge_account', $1, true)", setting); err != nil {
				t.Fatal(err)
			}
		}
		_, err = tx.Exec(f.ctx, "DELETE FROM message_locations WHERE message_sha256 = $1 AND folder_id = $2", sha, folder)
		switch {
		case wantOK && err != nil:
			t.Errorf("%s: %v", name, err)
		case !wantOK && (err == nil || !strings.Contains(err.Error(), "still retained or on legal hold")):
			t.Errorf("%s: %v", name, err)
		}
	}
	own := fmt.Sprint(f.acc.ID)
	try("no setting", "", f.m1, f.inbox.ID, false)
	try("no confirmed purge", own, f.m1, f.inbox.ID, false)
	// An account that is not removed, even with a confirmed purge row
	// (which only a hand-made row can give it).
	func() {
		tx, err := c.Begin(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(f.ctx) }()
		if _, err := tx.Exec(f.ctx, `INSERT INTO account_purges (account_id, account_name, account_kind, account_version, expected)
		                            VALUES ($1, 'work', 'imap', $2, 0)`, work.ID, work.Version); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(f.ctx, "SELECT set_config('mail_archive.purge_account', $1, true)", fmt.Sprint(work.ID)); err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(f.ctx, "DELETE FROM message_locations WHERE message_sha256 = $1 AND folder_id = $2", f.m3, f.aliceWork.ID)
		if err == nil || !strings.Contains(err.Error(), "still retained or on legal hold") {
			t.Errorf("an active account with a confirmed purge: %v", err)
		}
	}()
	p := f.confirm(1)
	try("no setting, purge confirmed", "", f.m1, f.inbox.ID, false)
	try("another account", fmt.Sprint(work.ID), f.m1, f.inbox.ID, false)
	try("an active account", fmt.Sprint(work.ID), f.m3, f.aliceWork.ID, false)
	try("not a number", "x", f.m1, f.inbox.ID, false)
	try("the purged account", own, f.m1, f.inbox.ID, true)
	rule := f.rule(f.inbox, "Archive", 100)
	try("a ruled location", own, f.m1, f.archive.ID, false)
	try("an unruled one beside it", own, f.m1, f.inbox.ID, true)
	_ = f.st.RemoveRetentionRule(f.ctx, rule)
	hold := f.hold(store.NewLegalHold{OwnerID: &f.alice.ID})
	try("a held location", own, f.m1, f.inbox.ID, false)
	_ = f.st.ReleaseLegalHold(f.ctx, hold, nil, "done")
	_ = f.st.CancelPurge(f.ctx, p.ID, "test")
	try("a cancelled purge", own, f.m1, f.inbox.ID, false)
}

// TestPurgeSettingOnPooledConnection (plan review finding 2): after a
// purge batch, the setting reads as ” on its connection, and a retention
// deletion on that connection still works.
func TestPurgeSettingOnPooledConnection(t *testing.T) {
	f := newPurgeFixture(t)
	st, err := store.Open(f.ctx, storetest.WithMaxConns(t, f.url, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// bob's m9 from 2010 under a 1-year rule: deletable.
	expired := f.folder(f.bob, "expired", "INBOX")
	f.save(expired, hashOf('9'), "old", old, old)
	f.rule(expired, "", 1)

	p, err := st.ConfirmPurge(f.ctx, f.acc.Ref(), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := st.PurgeBatch(f.ctx, p.ID, 1, 0); err != nil || res.Deleted != 1 {
		t.Fatalf("batch %+v %v", res, err)
	}
	run, err := st.ProposeDeletion(f.ctx, nil)
	if err != nil || run.Candidates != 1 {
		t.Fatalf("propose %+v %v", run, err)
	}
	if err := st.ConfirmDeletionRun(f.ctx, run.ID, nil); err != nil {
		t.Fatal(err)
	}
	if d, _, err := st.DeleteBatch(f.ctx, run.ID, []string{hashOf('9')}, 0); err != nil || d != 1 {
		t.Fatalf("retention deletion after a purge batch on the same connection: %d %v", d, err)
	}
	// The setting really is '' on that connection now.
	var setting *string
	if err := st.QueryRowForTest(f.ctx, "SELECT current_setting('mail_archive.purge_account', true)").Scan(&setting); err != nil ||
		setting == nil || *setting != "" {
		t.Errorf("setting %v %v", setting, err)
	}
}

// TestPurgeSMTPAccount: an smtp account with deliveries (envelopes), a
// fill and an import is gone with all of it; shared messages stay.
func TestPurgeSMTPAccount(t *testing.T) {
	f := newRetentionFixture(t)
	c := connect(t, f.url)
	journal := createSMTPAccount(t, f.st, &f.alice.ID, "journal", "archive@example.com")
	work := f.folder(f.alice, "work", "INBOX")
	if _, err := f.st.ReceiveMessage(f.ctx, journal.ID, receivedMeta(1), envelope("a@example.com"), present); err != nil {
		t.Fatal(err)
	}
	shared := receivedMeta(2)
	if _, err := f.st.ReceiveMessage(f.ctx, journal.ID, shared, envelope("a@example.com"), present); err != nil {
		t.Fatal(err)
	}
	f.uid++
	if _, err := f.st.SaveBatch(f.ctx, work.ID, f.uid, []store.MessageMeta{shared}, []store.Location{{FolderID: work.ID, UID: f.uid}}); err != nil {
		t.Fatal(err)
	}
	fill := newFill(journal, "alice@example.com")
	if err := f.st.CreateFill(f.ctx, fill, seal); err != nil {
		t.Fatal(err)
	}
	ff, err := f.st.FillFolder(f.ctx, fill, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.SaveFillBatch(f.ctx, ff, []store.MessageMeta{receivedMeta(3)}, []store.Location{{}}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.FinishFill(f.ctx, fill.ID, store.FillOK, ""); err != nil {
		t.Fatal(err)
	}
	imp := &store.SMTPImport{AccountID: journal.ID, Label: "piler", Format: "eml", Source: "/import/x"}
	if err := f.st.CreateSMTPImport(f.ctx, imp); err != nil {
		t.Fatal(err)
	}
	jf, err := f.st.JournalFolderID(f.ctx, journal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.st.SaveJournalImportBatch(f.ctx, jf, imp.ID, []store.MessageMeta{receivedMeta(4)}, []store.Location{{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.DeleteOrRemoveAccount(f.ctx, journal.Ref()); err != nil {
		t.Fatal(err)
	}
	removed, _ := f.st.GetAccount(f.ctx, journal.ID)
	pf := &purgeFixture{retentionFixture: f, acc: removed}
	pre := pf.preview()
	if pre.Locations != 4 || pre.Deleted != 3 || pre.Kept != 1 || pre.Blockers.Any() {
		t.Fatalf("preview %+v", pre)
	}
	beforeShared := rowsOf(t, c, "SELECT * FROM message_locations WHERE folder_id = $1", work.ID)
	p := pf.confirm(3)
	if _, err := pf.run(p.ID, 2, 0); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"folders", "smtp_addresses", "smtp_fills", "smtp_imports", "sync_runs", "loss_alerts", "account_sync_health"} {
		if n := countOf(t, c, "SELECT count(*) FROM "+table+" WHERE account_id = $1", journal.ID); n != 0 {
			t.Errorf("%s: %d rows left", table, n)
		}
	}
	for _, table := range []string{"envelopes", "smtp_fill_folders", "smtp_fill_health"} {
		if n := countOf(t, c, "SELECT count(*) FROM "+table); n != 0 {
			t.Errorf("%s: %d rows left", table, n)
		}
	}
	if got := rowsOf(t, c, "SELECT * FROM message_locations WHERE folder_id = $1", work.ID); !slices.Equal(got, beforeShared) {
		t.Error("shared location changed")
	}
	if n := countOf(t, c, "SELECT count(*) FROM messages"); n != 1 {
		t.Errorf("%d messages left", n)
	}
	checkInvariants(t, c)
}

// TestPurgeMigration checks 00030 down and up, and that down refuses
// once a purge is recorded.
func TestPurgeMigration(t *testing.T) {
	f := newPurgeFixture(t)
	ctx, st := f.ctx, f.st
	c := connect(t, f.url)
	if m, err := st.MigrateDown(ctx); err != nil || !strings.Contains(m, "00030_account_purges") {
		t.Fatalf("down: %q %v", m, err)
	}
	var exists bool
	if err := c.QueryRow(ctx, "SELECT to_regclass('account_purges') IS NOT NULL OR to_regproc('location_holds') IS NOT NULL").Scan(&exists); err != nil || exists {
		t.Fatalf("still there: %v %v", exists, err)
	}
	if _, err := c.Exec(ctx, `INSERT INTO message_deletions (message_seq, sha256, reason) VALUES (1, $1, 'account_purge')`, hashOf('a')); err == nil {
		t.Error("account_purge reason before 00030")
	}
	// The old location_held is back.
	hold := f.hold(store.NewLegalHold{OwnerID: &f.alice.ID})
	var held bool
	if err := c.QueryRow(ctx, "SELECT location_held(NULL, $1, NULL, NULL)", f.inbox.ID).Scan(&held); err != nil || !held {
		t.Errorf("location_held after down: %v %v", held, err)
	}
	if err := st.ReleaseLegalHold(ctx, hold, nil, "done"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	f.confirm(1)
	if _, err := st.MigrateDown(ctx); err == nil || !strings.Contains(err.Error(), "purged accounts are recorded") {
		t.Errorf("down with a purge: %v", err)
	}
	if err := c.QueryRow(ctx, "SELECT to_regclass('account_purges') IS NOT NULL").Scan(&exists); err != nil || !exists {
		t.Errorf("table dropped: %v %v", exists, err)
	}
}
