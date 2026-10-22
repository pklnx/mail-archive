package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store"
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
	d, err := f.st.GetMessageDetail(f.ctx, owner.ID, hash)
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
	if err := f.st.SetAccountOwner(f.ctx, acc.Ref(), f.bob.ID); !errors.Is(err, store.ErrOnHold) {
		t.Errorf("moving an account away from a held user: %v", err)
	}

	// A user named by a hold is never deleted, even after the release.
	carol, err := f.st.CreateUser(f.ctx, "carol", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	carolHold := f.hold(store.NewLegalHold{OwnerID: &carol.ID})
	_ = f.st.ReleaseLegalHold(f.ctx, carolHold, nil, "done")
	if err := f.st.DeleteUser(f.ctx, carol.ID); !errors.Is(err, store.ErrOnHold) {
		t.Errorf("deleting a user named by a hold: %v", err)
	}

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
	if err := f.st.SetAccountOwner(f.ctx, acc.Ref(), f.bob.ID); err != nil {
		t.Errorf("moving after the release: %v", err)
	}
}

func TestRetentionMigrationDown(t *testing.T) {
	f := newRetentionFixture(t)
	f.hold(store.NewLegalHold{OwnerID: &f.alice.ID})
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
