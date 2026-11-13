package store_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/db"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

type retentionFixture struct {
	t     *testing.T
	ctx   context.Context
	st    *store.Store
	url   string
	alice *store.User
	bob   *store.User
	uid   uint32
}

func newRetentionFixture(t *testing.T) *retentionFixture {
	t.Helper()
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	alice, err := st.CreateUser(ctx, "alice", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := st.CreateUser(ctx, "bob", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	return &retentionFixture{t: t, ctx: ctx, st: st, url: url, alice: alice, bob: bob}
}

// folder returns folder name of owner's IMAP account, creating both.
func (f *retentionFixture) folder(owner *store.User, account, name string) *store.Folder {
	f.t.Helper()
	a, err := f.st.GetOwnedAccount(f.ctx, owner.ID, account)
	if errors.Is(err, store.ErrNotFound) {
		a = &store.Account{Name: account, Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, OwnerID: &owner.ID}
		err = f.st.CreateAccount(f.ctx, a)
	}
	if err != nil {
		f.t.Fatal(err)
	}
	fo, err := f.st.GetOrCreateFolder(f.ctx, a.ID, name)
	if err != nil {
		f.t.Fatal(err)
	}
	return fo
}

// save archives message hash in fo with a Date header and an internal date.
func (f *retentionFixture) save(fo *store.Folder, hash, subject string, sent, internal *time.Time) {
	f.t.Helper()
	f.uid++
	meta := store.MessageMeta{SHA256: hash, Size: 1, StoredPath: "x", Subject: subject, SentAt: sent}
	loc := store.Location{FolderID: fo.ID, UID: f.uid}
	if internal != nil {
		loc.InternalDate = *internal
	}
	if _, err := f.st.SaveBatch(f.ctx, fo.ID, f.uid, []store.MessageMeta{meta}, []store.Location{loc}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *retentionFixture) rule(fo *store.Folder, folder string, years int) int64 {
	f.t.Helper()
	id, err := f.st.AddRetentionRule(f.ctx, store.NewRetentionRule{AccountID: fo.AccountID, Folder: folder, Years: years})
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *retentionFixture) detail(owner *store.User, hash string) *store.MessageDetail {
	f.t.Helper()
	d, err := f.st.GetMessageDetail(f.ctx, storetest.Viewer(f.t, f.st, owner.ID), hash)
	if err != nil {
		f.t.Fatal(err)
	}
	return d
}

func (f *retentionFixture) hold(h store.NewLegalHold) int64 {
	f.t.Helper()
	if h.Name == "" {
		h.Name = "audit"
	}
	id, err := f.st.AddLegalHold(f.ctx, h)
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

func ts(s string) *time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return &t
}

func TestRetentionEnd(t *testing.T) {
	_, url := storetest.NewWithURL(t)
	ctx := context.Background()
	c, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close(ctx) }()
	archived := "2026-06-01T00:00:00Z"
	for _, tc := range []struct {
		name               string
		sent, internal, at *time.Time
		years              int
		want               string
	}{
		{"received 5 March 2019, 6 years", ts("2019-03-05T10:00:00Z"), ts("2019-03-05T10:00:00Z"), ts(archived), 6, "2025-12-31T23:00:00Z"},
		{"8 years", ts("2019-03-05T10:00:00Z"), nil, ts(archived), 8, "2027-12-31T23:00:00Z"},
		{"10 years", ts("2019-03-05T10:00:00Z"), nil, ts(archived), 10, "2029-12-31T23:00:00Z"},
		// Received 15 July 2016: the period runs 1 January 2017 to 31 December
		// 2026, so the mail may be deleted from 1 January 2027 (Berlin).
		{"mid-year mail counts from the year end", ts("2016-07-15T10:00:00Z"), ts("2016-07-15T10:00:00Z"), ts(archived), 10, "2026-12-31T23:00:00Z"},
		// 00:30 on 1 January in Berlin is 2020, though it is 2019 in UTC.
		{"Berlin new year", ts("2019-12-31T23:30:00Z"), nil, ts(archived), 6, "2026-12-31T23:00:00Z"},
		{"a forged old Date header", ts("2001-01-01T00:00:00Z"), ts("2024-05-01T00:00:00Z"), ts(archived), 6, "2030-12-31T23:00:00Z"},
		{"a future Date header is capped", ts("2040-01-01T00:00:00Z"), nil, ts(archived), 6, "2032-12-31T23:00:00Z"},
		{"no dates: when it was archived", nil, nil, ts(archived), 6, "2032-12-31T23:00:00Z"},
	} {
		var got time.Time
		if err := c.QueryRow(ctx, "SELECT retention_end($1, $2, $3, $4, 'Europe/Berlin')", tc.sent, tc.internal, tc.at, tc.years).Scan(&got); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if want := *ts(tc.want); !got.Equal(want) {
			t.Errorf("%s: %s, want %s", tc.name, got.UTC().Format(time.RFC3339), tc.want)
		}
	}
}

func TestRetentionRules(t *testing.T) {
	f := newRetentionFixture(t)
	inbox := f.folder(f.alice, "work", "INBOX")
	news := f.folder(f.alice, "work", "News")
	sent := ts("2019-03-05T10:00:00Z")
	f.save(inbox, hashOf('a'), "invoice", sent, sent)
	f.save(news, hashOf('b'), "newsletter", sent, sent)

	// No rule: kept for ever.
	if d := f.detail(f.alice, hashOf('a')); d.RetainUntil != nil || d.Locations[0].RetainUntil != nil {
		t.Fatalf("no rule: %+v", d.RetainUntil)
	}

	accountRule := f.rule(inbox, "", 6)
	if d := f.detail(f.alice, hashOf('a')); d.RetainUntil == nil || !d.RetainUntil.Equal(*ts("2025-12-31T23:00:00Z")) {
		t.Fatalf("account rule: %v", d.RetainUntil)
	}
	// A folder rule cannot shorten the account rule: the longest wins.
	f.rule(news, "News", 1)
	if d := f.detail(f.alice, hashOf('b')); !d.RetainUntil.Equal(*ts("2025-12-31T23:00:00Z")) {
		t.Fatalf("shorter folder rule: %v", d.RetainUntil)
	}
	f.rule(inbox, "INBOX", 10)
	if d := f.detail(f.alice, hashOf('a')); !d.RetainUntil.Equal(*ts("2029-12-31T23:00:00Z")) {
		t.Fatalf("longer folder rule: %v", d.RetainUntil)
	}

	// The same message in an unruled account of another user: the message
	// is kept for ever, though alice's own view shows her end.
	other := f.folder(f.bob, "private", "INBOX")
	f.save(other, hashOf('a'), "invoice", sent, sent)
	if d := f.detail(f.bob, hashOf('a')); d.RetainUntil != nil {
		t.Fatalf("bob's unruled copy: %v", d.RetainUntil)
	}

	if _, err := f.st.AddRetentionRule(f.ctx, store.NewRetentionRule{AccountID: inbox.AccountID, Years: 8}); !errors.Is(err, store.ErrConflict) {
		t.Errorf("second account rule: %v", err)
	}
	if _, err := f.st.AddRetentionRule(f.ctx, store.NewRetentionRule{AccountID: inbox.AccountID, Folder: "x", Years: 8, TimeZone: "Mars/Olympus"}); !errors.Is(err, store.ErrInvalidTimeZone) {
		t.Errorf("bad time zone: %v", err)
	}
	if _, err := f.st.AddRetentionRule(f.ctx, store.NewRetentionRule{AccountID: inbox.AccountID, Folder: "y", Years: 0}); err == nil {
		t.Error("0 years accepted")
	}

	rules, err := f.st.RetentionRules(f.ctx)
	if err != nil || len(rules) != 3 || rules[0].Owner != "alice" || rules[0].Folder != "" || rules[0].TimeZone != store.DefaultRetentionTimeZone {
		t.Fatalf("rules: %+v %v", rules, err)
	}
	if err := f.st.RemoveRetentionRule(f.ctx, accountRule); err != nil {
		t.Fatal(err)
	}
	if err := f.st.RemoveRetentionRule(f.ctx, accountRule); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("second remove: %v", err)
	}
	if d := f.detail(f.alice, hashOf('b')); !d.RetainUntil.Equal(*ts("2020-12-31T23:00:00Z")) {
		t.Fatalf("folder rule alone: %v", d.RetainUntil)
	}
	if c, err := f.st.RetentionCounts(f.ctx); err != nil || c.Rules != 2 || c.ActiveHolds != 0 {
		t.Fatalf("counts: %+v %v", c, err)
	}
}

func TestLegalHolds(t *testing.T) {
	f := newRetentionFixture(t)
	work := f.folder(f.alice, "work", "INBOX")
	priv := f.folder(f.alice, "private", "INBOX")
	bobs := f.folder(f.bob, "mail", "INBOX")
	f.save(work, hashOf('a'), "Project Falcon contract", ts("2019-03-05T10:00:00Z"), ts("2019-03-05T10:00:00Z"))
	f.save(work, hashOf('b'), "lunch", ts("2023-03-05T10:00:00Z"), nil)
	f.save(priv, hashOf('c'), "holiday", nil, nil)
	f.save(bobs, hashOf('d'), "Falcon notes", ts("2021-01-01T10:00:00Z"), nil)

	held := func(owner *store.User, hash string) bool {
		t.Helper()
		return f.detail(owner, hash).Locations[0].OnHold
	}
	check := func(name string, want map[string]bool) {
		t.Helper()
		for key, w := range want {
			owner := f.alice
			if key == hashOf('d') {
				owner = f.bob
			}
			if got := held(owner, key); got != w {
				t.Errorf("%s: %c held %v, want %v", name, key[0], got, w)
			}
		}
	}
	all := func(v bool) map[string]bool {
		return map[string]bool{hashOf('a'): v, hashOf('b'): v, hashOf('c'): v, hashOf('d'): v}
	}
	check("no hold", all(false))

	// A user hold covers all of the user's accounts.
	userHold := f.hold(store.NewLegalHold{OwnerID: &f.alice.ID})
	check("user hold", map[string]bool{hashOf('a'): true, hashOf('b'): true, hashOf('c'): true, hashOf('d'): false})
	if err := f.st.ReleaseLegalHold(f.ctx, userHold, nil, ""); err == nil {
		t.Error("release without a note")
	}
	if err := f.st.ReleaseLegalHold(f.ctx, userHold, nil, "audit closed"); err != nil {
		t.Fatal(err)
	}
	if err := f.st.ReleaseLegalHold(f.ctx, userHold, nil, "again"); !errors.Is(err, store.ErrConflict) {
		t.Errorf("second release: %v", err)
	}
	if err := f.st.ReleaseLegalHold(f.ctx, 999, nil, "x"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown hold: %v", err)
	}
	check("released", all(false))

	// An account hold, limited to a period: a message without dates counts
	// as inside.
	acc := f.hold(store.NewLegalHold{AccountID: &work.AccountID, PeriodStart: ts("2019-01-01T00:00:00Z"), PeriodEnd: ts("2020-01-01T00:00:00Z")})
	check("account and period", map[string]bool{hashOf('a'): true, hashOf('b'): false, hashOf('c'): false, hashOf('d'): false})
	_ = f.st.ReleaseLegalHold(f.ctx, acc, nil, "done")
	period := f.hold(store.NewLegalHold{PeriodStart: ts("2022-01-01T00:00:00Z")})
	check("period only", map[string]bool{hashOf('a'): false, hashOf('b'): true, hashOf('c'): true, hashOf('d'): false})
	_ = f.st.ReleaseLegalHold(f.ctx, period, nil, "done")

	// A filter hold covers its whole scope until it is matched, then only
	// the matches, across users.
	filter := f.hold(store.NewLegalHold{Filter: &store.HoldFilter{Query: "falcon"}})
	check("filter before matching", all(true))
	n, err := f.st.MatchLegalHolds(f.ctx)
	if err != nil || n != 2 {
		t.Fatalf("matching: %d %v", n, err)
	}
	check("filter after matching", map[string]bool{hashOf('a'): true, hashOf('b'): false, hashOf('c'): false, hashOf('d'): true})
	if n, err := f.st.MatchLegalHolds(f.ctx); err != nil || n != 0 {
		t.Fatalf("matching again: %d %v", n, err)
	}
	holds, err := f.st.LegalHolds(f.ctx, false)
	if err != nil || len(holds) != 1 || holds[0].ID != filter || holds[0].Matched != 2 || holds[0].MatchedAt == nil ||
		holds[0].Filter == nil || holds[0].Filter.Query != "falcon" {
		t.Fatalf("active holds: %+v %v", holds, err)
	}
	if holds, err := f.st.LegalHolds(f.ctx, true); err != nil || len(holds) != 4 || holds[0].ReleaseNote != "audit closed" {
		t.Fatalf("all holds: %+v %v", holds, err)
	}

	if _, err := f.st.AddLegalHold(f.ctx, store.NewLegalHold{Name: " "}); err == nil {
		t.Error("hold without a name")
	}
	if _, err := f.st.AddLegalHold(f.ctx, store.NewLegalHold{Name: "x", PeriodStart: ts("2022-01-01T00:00:00Z"), PeriodEnd: ts("2021-01-01T00:00:00Z")}); err == nil {
		t.Error("period ending before it starts")
	}
}

// A hold's filter never searches smtp envelopes: to: matches the To and
// Cc headers only, for holds on an smtp account, on another account of its
// owner and on the whole instance. Matches stored before stay.
func TestHoldFilterIgnoresEnvelopes(t *testing.T) {
	f := newRetentionFixture(t)
	imap := f.folder(f.alice, "work", "INBOX")
	journal := createSMTPAccount(t, f.st, &f.alice.ID, "journal", "journal@example.com")
	meta := receivedMeta(0x42)
	meta.Subject, meta.To = "budget", "team@x.example"
	f.uid++
	if _, err := f.st.SaveBatch(f.ctx, imap.ID, f.uid, []store.MessageMeta{meta}, []store.Location{{FolderID: imap.ID, UID: f.uid}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.ReceiveMessage(f.ctx, journal.ID, meta, envelope("hidden@bcc.example"), present); err != nil {
		t.Fatal(err)
	}
	// bob has his own copy of the same message, without an envelope.
	bobs := f.folder(f.bob, "mail", "INBOX")
	f.uid++
	if _, err := f.st.SaveBatch(f.ctx, bobs.ID, f.uid, []store.MessageMeta{meta}, []store.Location{{FolderID: bobs.ID, UID: f.uid}}); err != nil {
		t.Fatal(err)
	}
	// The search itself still finds alice's envelope.
	if rows, err := f.st.SearchMessages(f.ctx, store.SearchFilter{Scope: storetest.Viewer(t, f.st, f.alice.ID), To: "hidden@bcc", Limit: 10}); err != nil || len(rows) != 1 {
		t.Fatalf("search to:hidden@bcc: %d %v", len(rows), err)
	}

	filter := func(to string) *store.HoldFilter { return &store.HoldFilter{To: to} }
	onJournal := f.hold(store.NewLegalHold{AccountID: &journal.ID, Filter: filter("hidden@bcc")})
	onIMAP := f.hold(store.NewLegalHold{AccountID: &imap.AccountID, Filter: filter("hidden@bcc")})
	instance := f.hold(store.NewLegalHold{Filter: filter("hidden@bcc")})
	kept := f.hold(store.NewLegalHold{AccountID: &journal.ID, Filter: filter("hidden@bcc")})
	if _, err := connect(t, f.url).Exec(f.ctx, `INSERT INTO legal_hold_messages (hold_id, message_sha256) VALUES ($1, $2)`, kept, meta.SHA256); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.MatchLegalHolds(f.ctx); err != nil {
		t.Fatal(err)
	}
	matched := func() map[int64]int64 {
		t.Helper()
		holds, err := f.st.LegalHolds(f.ctx, false)
		if err != nil {
			t.Fatal(err)
		}
		out := map[int64]int64{}
		for _, h := range holds {
			out[h.ID] = h.Matched
		}
		return out
	}
	m := matched()
	for name, c := range map[string]struct {
		id   int64
		want int64
	}{"smtp account": {onJournal, 0}, "imap account": {onIMAP, 0}, "instance": {instance, 0}, "matched before": {kept, 1}} {
		if m[c.id] != c.want {
			t.Errorf("%s hold to:hidden@bcc matched %d, want %d", name, m[c.id], c.want)
		}
	}
	// No hold covers bob's copy through alice's envelope.
	if l := f.detail(f.bob, meta.SHA256).Locations; len(l) != 1 || l[0].OnHold {
		t.Errorf("bob's copy through alice's envelope: %+v", l)
	}

	// The To header still matches, in every scope.
	headerJournal := f.hold(store.NewLegalHold{AccountID: &journal.ID, Filter: filter("team@x")})
	headerIMAP := f.hold(store.NewLegalHold{AccountID: &imap.AccountID, Filter: filter("team@x")})
	headerInstance := f.hold(store.NewLegalHold{Filter: filter("team@x")})
	if _, err := f.st.MatchLegalHolds(f.ctx); err != nil {
		t.Fatal(err)
	}
	m = matched()
	for name, id := range map[string]int64{"smtp account": headerJournal, "imap account": headerIMAP, "instance": headerInstance} {
		if m[id] != 1 {
			t.Errorf("%s hold to:team@x matched %d, want 1", name, m[id])
		}
	}
	if l := f.detail(f.bob, meta.SHA256).Locations; len(l) != 1 || !l[0].OnHold {
		t.Errorf("bob's copy under the instance hold on the To header: %+v", l)
	}
}

// checkHoldError checks that err is a *store.HoldError with the IDs, for
// the web answer, and that its text is unchanged, for the command line.
func checkHoldError(t *testing.T, err error, text string, ids ...int64) {
	t.Helper()
	var held *store.HoldError
	if !errors.As(err, &held) || !slices.Equal(held.IDs, ids) {
		t.Errorf("%v: not a HoldError with IDs %v: %#v", err, ids, held)
	}
	if err == nil || err.Error() != text {
		t.Errorf("text %q, want %q", err, text)
	}
}

// Changes to users and accounts must not release held mail or remove what
// a hold names.
func TestHoldsGuardUsersAndAccounts(t *testing.T) {
	f := newRetentionFixture(t)
	work := f.folder(f.alice, "work", "INBOX")
	f.save(work, hashOf('a'), "x", nil, nil)
	hold := f.hold(store.NewLegalHold{OwnerID: &f.alice.ID})

	acc, err := f.st.GetOwnedAccount(f.ctx, f.alice.ID, "work")
	if err != nil {
		t.Fatal(err)
	}
	err = f.st.SetAccountOwner(f.ctx, acc.Ref(), f.bob.ID, false)
	if !errors.Is(err, store.ErrOnHold) {
		t.Errorf("moving an account away from a held user: %v", err)
	}
	checkHoldError(t, err, fmt.Sprintf("named by a legal hold: the account's owner or their group is on legal hold #%d", hold), hold)

	// A user named by a hold is never deleted, even after the release.
	carol, err := f.st.CreateUser(f.ctx, "carol", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	carolHold := f.hold(store.NewLegalHold{OwnerID: &carol.ID})
	_ = f.st.ReleaseLegalHold(f.ctx, carolHold, nil, "done")
	err = f.st.DeleteUser(f.ctx, carol.ID)
	if !errors.Is(err, store.ErrOnHold) {
		t.Errorf("deleting a user named by a hold: %v", err)
	}
	checkHoldError(t, err, fmt.Sprintf("named by a legal hold #%d; lock the user instead", carolHold), carolHold)

	// An empty account named by a hold is kept as removed.
	empty := f.folder(f.bob, "empty", "INBOX")
	emptyAcc, err := f.st.GetOwnedAccount(f.ctx, f.bob.ID, "empty")
	if err != nil {
		t.Fatal(err)
	}
	f.hold(store.NewLegalHold{AccountID: &empty.AccountID})
	if res, err := f.st.DeleteOrRemoveAccount(f.ctx, emptyAcc.Ref()); err != nil || res != store.AccountRemoved {
		t.Errorf("removing an empty held account: %s %v", res, err)
	}

	// After the release, the account can move.
	if err := f.st.ReleaseLegalHold(f.ctx, hold, nil, "done"); err != nil {
		t.Fatal(err)
	}
	acc, _ = f.st.GetOwnedAccount(f.ctx, f.alice.ID, "work")
	if err := f.st.SetAccountOwner(f.ctx, acc.Ref(), f.bob.ID, false); err != nil {
		t.Errorf("moving after the release: %v", err)
	}
}

func TestRetentionMigrationDown(t *testing.T) {
	f := newRetentionFixture(t)
	f.hold(store.NewLegalHold{OwnerID: &f.alice.ID})
	downJobsMigration(t, f.st)
	if m, err := f.st.MigrateDown(f.ctx); err != nil || !strings.Contains(m, "00031_account_shares") {
		t.Fatalf("down the account shares: %q, %v", m, err)
	}
	if m, err := f.st.MigrateDown(f.ctx); err != nil || !strings.Contains(m, "00030_account_purges") {
		t.Fatalf("down the account purges: %q, %v", m, err)
	}
	if m, err := f.st.MigrateDown(f.ctx); err != nil || !strings.Contains(m, "00029_smtp_imports") {
		t.Fatalf("down the smtp imports: %q %v", m, err)
	}
	if m, err := f.st.MigrateDown(f.ctx); err != nil || !strings.Contains(m, "00028_recurring_fills") {
		t.Fatalf("down the recurring fills: %q %v", m, err)
	}
	if m, err := f.st.MigrateDown(f.ctx); err != nil || !strings.Contains(m, "00027_smtp_journal_folder") {
		t.Fatalf("down the journal folder: %q %v", m, err)
	}
	if m, err := f.st.MigrateDown(f.ctx); err != nil || !strings.Contains(m, "00026_smtp_fills") {
		t.Fatalf("down the smtp fills: %q %v", m, err)
	}
	if m, err := f.st.MigrateDown(f.ctx); err != nil || !strings.Contains(m, "00025_user_settings") {
		t.Fatalf("down the user settings: %q %v", m, err)
	}
	if m, err := f.st.MigrateDown(f.ctx); err != nil || !strings.Contains(m, "00024_account_sync_interval") {
		t.Fatalf("down the sync interval: %q %v", m, err)
	}
	if m, err := f.st.MigrateDown(f.ctx); err != nil || !strings.Contains(m, "00023_folder_delimiter") {
		t.Fatalf("down the folder delimiter: %q %v", m, err)
	}
	if m, err := f.st.MigrateDown(f.ctx); err != nil || !strings.Contains(m, "00022_deletion_runs") {
		t.Fatalf("down the deletion runs: %q %v", m, err)
	}
	if _, err := f.st.MigrateDown(f.ctx); err == nil || !strings.Contains(err.Error(), "legal holds exist") {
		t.Fatalf("down with a hold: %v", err)
	}
}

// The web UI matches only the hold an admin adds, and its list counts only
// the caller's holds (#538): neither the request's answer nor its time
// depends on what other holds' filters match in mail the caller cannot
// read. The command line and deletion runs still match and count every
// hold.
func TestLegalHoldsOfCaller(t *testing.T) {
	f := newRetentionFixture(t)
	work := f.folder(f.alice, "work", "INBOX")
	bobs := f.folder(f.bob, "mail", "INBOX")
	f.save(work, hashOf('a'), "Falcon contract", nil, nil)
	f.save(bobs, hashOf('b'), "Falcon notes", nil, nil)
	falcon := &store.HoldFilter{Query: "falcon"}
	aliceUser := f.hold(store.NewLegalHold{Name: "alice", OwnerID: &f.alice.ID, Filter: falcon})
	aliceAcc := f.hold(store.NewLegalHold{Name: "alice's account", AccountID: &work.AccountID, Filter: falcon})
	bobAcc := f.hold(store.NewLegalHold{Name: "bob's account", AccountID: &bobs.AccountID, Filter: falcon})
	instance := f.hold(store.NewLegalHold{Name: "instance", Filter: falcon})
	plain := f.hold(store.NewLegalHold{Name: "plain", OwnerID: &f.alice.ID})
	released := f.hold(store.NewLegalHold{Name: "released", AccountID: &work.AccountID, Filter: falcon})
	if err := f.st.ReleaseLegalHold(f.ctx, released, nil, "done"); err != nil {
		t.Fatal(err)
	}

	type state struct {
		matched int64
		at      bool
	}
	list := func(holds []store.LegalHold, err error) map[int64]state {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		out := map[int64]state{}
		for _, h := range holds {
			out[h.ID] = state{h.Matched, h.MatchedAt != nil}
		}
		return out
	}
	all := func() map[int64]state { return list(f.st.LegalHolds(f.ctx, false)) }

	// One hold is matched, the others are not touched.
	if n, err := f.st.MatchLegalHold(f.ctx, aliceAcc); err != nil || n != 1 {
		t.Fatalf("matching alice's account hold: %d %v", n, err)
	}
	if got, want := all(), map[int64]state{aliceUser: {}, aliceAcc: {1, true}, bobAcc: {}, instance: {}, plain: {}}; !maps.Equal(got, want) {
		t.Errorf("after matching one hold %v, want %v", got, want)
	}
	for name, id := range map[string]int64{"unknown": 999, "released": released, "without a filter": plain} {
		if n, err := f.st.MatchLegalHold(f.ctx, id); !errors.Is(err, store.ErrNotFound) || n != 0 {
			t.Errorf("matching a hold %s: %d %v", name, n, err)
		}
	}

	if _, err := f.st.MatchLegalHolds(f.ctx); err != nil {
		t.Fatal(err)
	}
	every := map[int64]state{aliceUser: {1, true}, aliceAcc: {1, true}, bobAcc: {1, true}, instance: {2, true}, plain: {}}
	if got := all(); !maps.Equal(got, every) {
		t.Errorf("command line list %v, want %v", got, every)
	}
	// alice's holds name her, or name no user and her account; bob's
	// count and the instance's are 0 for her.
	if got, want := list(f.st.LegalHoldsCountingFor(f.ctx, false, f.alice.ID)),
		map[int64]state{aliceUser: {1, true}, aliceAcc: {1, true}, bobAcc: {0, true}, instance: {0, true}, plain: {}}; !maps.Equal(got, want) {
		t.Errorf("alice's list %v, want %v", got, want)
	}
	if got, want := list(f.st.LegalHoldsCountingFor(f.ctx, false, f.bob.ID)),
		map[int64]state{aliceUser: {0, true}, aliceAcc: {0, true}, bobAcc: {1, true}, instance: {0, true}, plain: {}}; !maps.Equal(got, want) {
		t.Errorf("bob's list %v, want %v", got, want)
	}

	// The count runs only for the caller's holds, as EXPLAIN ANALYZE shows
	// by the loops of its subplan: once per listed hold for the command
	// line, once per hold naming alice or her account for her (the hold
	// without a filter names her too), once for bob.
	conn, err := pgx.Connect(f.ctx, f.url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(f.ctx) }()
	loops := regexp.MustCompile(`SubPlan \d+\s*\n\s*->  Aggregate \(actual rows=\S+ loops=(\d+)\)`)
	for _, c := range []struct {
		name     string
		countFor *int64
		want     string
	}{{"command line", nil, "5"}, {"alice", &f.alice.ID, "3"}, {"bob", &f.bob.ID, "1"}} {
		var rec explainer
		if _, err := db.New(&rec).ListLegalHolds(f.ctx, db.ListLegalHoldsParams{CountFor: c.countFor}); !errors.Is(err, errRecorded) {
			t.Fatalf("record: %v", err)
		}
		rows, err := conn.Query(f.ctx, "EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, SUMMARY OFF) "+rec.sql, rec.args...)
		if err != nil {
			t.Fatal(err)
		}
		lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		plan := strings.Join(lines, "\n")
		if m := loops.FindStringSubmatch(plan); m == nil || m[1] != c.want {
			t.Errorf("%s: the count runs %v times, want %s:\n%s", c.name, m, c.want, plan)
		}
	}
}
