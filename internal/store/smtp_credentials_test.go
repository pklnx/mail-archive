package store_test

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
	"github.com/pklnx/mail-archive/internal/testwait"
)

// smtpSecret creates a secret for a with grace at now and returns its
// hash, the credential and what it superseded.
func smtpSecret(t *testing.T, st *store.Store, a *store.Account, grace time.Duration, now time.Time) ([]byte, *store.SMTPCredential, []store.SupersededSMTPCredential) {
	t.Helper()
	_, hash, hint, err := auth.NewSMTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	c, sup, err := st.CreateSMTPCredential(context.Background(), a.Ref(), store.NewSMTPCredential{Hash: hash, Hint: hint, Grace: grace, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if c.Hint != hint || c.AccountID != a.ID || c.CreatedAt.Sub(now).Abs() > time.Microsecond {
		t.Fatalf("created %+v", c)
	}
	return hash, c, sup
}

func credentialStates(t *testing.T, st *store.Store, accountID int64, now time.Time) map[int64]string {
	t.Helper()
	list, err := st.SMTPCredentials(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[int64]string{}
	for _, c := range list {
		out[c.ID] = c.State(now)
	}
	return out
}

func TestSMTPCredentialCreate(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, "alice", "h", true)
	if err != nil {
		t.Fatal(err)
	}
	a := createSMTPAccount(t, st, &u.ID, "journal", "journal@example.com")
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	_, hash, hint, _ := auth.NewSMTPSecret()
	e := store.TargetedEntry(store.ActorCLI, "smtp_credential.create")
	e.SetTarget(store.TargetAccount, itoa64(a.ID), a.Name)
	c, sup, err := st.CreateSMTPCredential(store.WithAudit(ctx, e), a.Ref(), store.NewSMTPCredential{Hash: hash, Hint: hint, CreatedBy: &u.ID, Grace: 7 * 24 * time.Hour, Now: now})
	if err != nil || len(sup) != 0 {
		t.Fatalf("create: %+v %v %v", c, sup, err)
	}
	if got := credentialStates(t, st, a.ID, now); !maps1(got, c.ID, store.SMTPCredentialActive) {
		t.Fatalf("states: %v", got)
	}
	rows := auditRows(t, connect(t, url))
	last := rows[len(rows)-1]
	if last.Action != "smtp_credential.create" || last.Details["credential_id"] != float64(c.ID) || len(last.Details["superseded"].([]any)) != 0 {
		t.Fatalf("audit: %+v", last)
	}
	for _, v := range last.Details {
		if s, ok := v.(string); ok && strings.Contains(s, string(hash)) {
			t.Fatal("hash in the audit entry")
		}
	}

	// An IMAP account has no SMTP credentials; a removed one gets none.
	imap := createAccount(t, st, "imap")
	if _, _, err := st.CreateSMTPCredential(ctx, imap.Ref(), store.NewSMTPCredential{Hash: hash32(1), Hint: "abcd", Now: now}); !errors.Is(err, store.ErrNotSMTP) {
		t.Errorf("imap: %v", err)
	}
	if _, err := st.ReceiveMessage(ctx, a.ID, receivedMeta(1), envelope("journal@example.com"), present); err != nil {
		t.Fatal(err)
	}
	a, _ = st.GetAccount(ctx, a.ID)
	if res, err := st.DeleteOrRemoveAccount(ctx, a.Ref()); err != nil || res != store.AccountRemoved {
		t.Fatalf("remove: %v %v", res, err)
	}
	a, _ = st.GetAccount(ctx, a.ID)
	if _, _, err := st.CreateSMTPCredential(ctx, a.Ref(), store.NewSMTPCredential{Hash: hash32(2), Hint: "abcd", Now: now}); !errors.Is(err, store.ErrAccountRemoved) {
		t.Errorf("removed: %v", err)
	}
	// A stale or foreign reference and a grace out of range change nothing.
	b := createSMTPAccount(t, st, &u.ID, "other", "other@example.com")
	stale := b.Ref()
	stale.Version--
	other := b.Ref()
	other.OwnerID = nil
	for _, c := range []struct {
		ref   store.AccountRef
		grace time.Duration
		want  error
	}{
		{stale, 0, store.ErrStale},
		{other, 0, store.ErrNotFound},
		{b.Ref(), -time.Second, store.ErrSMTPGrace},
		{b.Ref(), store.MaxSMTPGrace + time.Second, store.ErrSMTPGrace},
	} {
		if _, _, err := st.CreateSMTPCredential(ctx, c.ref, store.NewSMTPCredential{Hash: hash32(3), Hint: "abcd", Grace: c.grace, Now: now}); !errors.Is(err, c.want) {
			t.Errorf("%+v: %v", c, err)
		}
	}
	if got := credentialStates(t, st, b.ID, now); len(got) != 0 {
		t.Errorf("refused creates stored %v", got)
	}
}

// Rotation keeps the current secret for the grace period, so two are
// usable; the next rotation revokes the one that was expiring; a grace of
// 0 revokes the current one at once.
func TestSMTPCredentialRotation(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	a := createSMTPAccount(t, st, nil, "journal", "journal@example.com")
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	week := 7 * 24 * time.Hour

	h1, c1, _ := smtpSecret(t, st, a, week, now)
	now = now.Add(time.Hour)
	h2, c2, sup := smtpSecret(t, st, a, week, now)
	if len(sup) != 1 || sup[0].ID != c1.ID || sup[0].RevokedAt != nil || !sup[0].ExpiresAt.Equal(now.Add(week)) {
		t.Fatalf("superseded: %+v", sup)
	}
	for _, h := range [][]byte{h1, h2} {
		if _, err := st.SMTPLogin(ctx, h, now); err != nil {
			t.Fatalf("login during the grace period: %v", err)
		}
	}
	if n, err := st.UsableSMTPCredentials(ctx, a.ID, now); err != nil || n != 2 {
		t.Fatalf("usable: %d %v", n, err)
	}
	if got := credentialStates(t, st, a.ID, now); got[c1.ID] != store.SMTPCredentialExpiring || got[c2.ID] != store.SMTPCredentialActive {
		t.Fatalf("states: %v", got)
	}

	// A third secret revokes the expiring one and supersedes the current.
	now = now.Add(time.Hour)
	h3, c3, sup := smtpSecret(t, st, a, week, now)
	slices.SortFunc(sup, func(x, y store.SupersededSMTPCredential) int { return int(x.ID - y.ID) })
	if len(sup) != 2 || sup[0].ID != c1.ID || !sup[0].RevokedAt.Equal(now) || sup[1].ID != c2.ID || !sup[1].ExpiresAt.Equal(now.Add(week)) {
		t.Fatalf("superseded: %+v", sup)
	}
	if _, err := st.SMTPLogin(ctx, h1, now); !errors.Is(err, store.ErrSMTPCredentialRevoked) {
		t.Errorf("first: %v", err)
	}
	if n, _ := st.UsableSMTPCredentials(ctx, a.ID, now); n != 2 {
		t.Errorf("usable after the third: %d", n)
	}

	// After the grace period the old one is expired: the clock is the
	// caller's.
	later := now.Add(week)
	if l, err := st.SMTPLogin(ctx, h2, later); !errors.Is(err, store.ErrSMTPCredentialExpired) || l == nil || l.CredentialID != c2.ID || !l.ExpiresAt.Equal(later) {
		t.Errorf("expired: %+v %v", l, err)
	}
	if _, err := st.SMTPLogin(ctx, h2, later.Add(-time.Second)); err != nil {
		t.Errorf("a second before the end: %v", err)
	}
	if err := st.SMTPCredentialUsable(ctx, c2.ID, later); !errors.Is(err, store.ErrSMTPCredentialExpired) {
		t.Errorf("usable after the end: %v", err)
	}
	// A rotation after the end leaves the expired one as it is.
	_, c4, sup := smtpSecret(t, st, a, 0, later)
	if len(sup) != 1 || sup[0].ID != c3.ID || !sup[0].RevokedAt.Equal(later) {
		t.Fatalf("grace 0: %+v", sup)
	}
	if _, err := st.SMTPLogin(ctx, h3, later); !errors.Is(err, store.ErrSMTPCredentialRevoked) {
		t.Errorf("revoked at once: %v", err)
	}
	got := credentialStates(t, st, a.ID, later)
	want := map[int64]string{c1.ID: store.SMTPCredentialRevoked, c2.ID: store.SMTPCredentialExpired, c3.ID: store.SMTPCredentialRevoked, c4.ID: store.SMTPCredentialActive}
	if len(got) != len(want) {
		t.Fatalf("states %v", got)
	}
	for id, s := range want {
		if got[id] != s {
			t.Errorf("credential %d: %s, want %s", id, got[id], s)
		}
	}
	if n, _ := st.UsableSMTPCredentials(ctx, a.ID, later); n != 1 {
		t.Errorf("usable at the end: %d", n)
	}
}

// Concurrent creates for one account are serialized by the account's row
// lock: each supersedes the one before it, so one secret stays current and
// at most two are usable.
func TestSMTPCredentialConcurrentCreates(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	a := createSMTPAccount(t, st, nil, "journal", "journal@example.com")
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	const n = 8
	errs := make(chan error, n)
	start := make(chan struct{})
	for range n {
		go func() {
			<-start
			_, hash, hint, err := auth.NewSMTPSecret()
			if err == nil {
				_, _, err = st.CreateSMTPCredential(ctx, a.Ref(), store.NewSMTPCredential{Hash: hash, Hint: hint, Grace: 7 * 24 * time.Hour, Now: now})
			}
			errs <- err
		}()
	}
	close(start)
	limit := time.After(testwait.Limit(t))
	for range n {
		select {
		case err := <-errs:
			if err != nil {
				t.Errorf("create: %v", err)
			}
		case <-limit:
			t.Fatal("concurrent creates did not finish")
		}
	}
	counts := map[string]int{}
	for _, state := range credentialStates(t, st, a.ID, now) {
		counts[state]++
	}
	if counts[store.SMTPCredentialActive] != 1 || counts[store.SMTPCredentialExpiring] != 1 || counts[store.SMTPCredentialRevoked] != n-2 {
		t.Fatalf("states after %d concurrent creates: %v", n, counts)
	}
	if u, err := st.UsableSMTPCredentials(ctx, a.ID, now); err != nil || u != 2 {
		t.Fatalf("usable: %d %v", u, err)
	}
}

func TestSMTPCredentialRevoke(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	u, _ := st.CreateUser(ctx, "alice", "h", true)
	a := createSMTPAccount(t, st, &u.ID, "journal", "journal@example.com")
	b := createSMTPAccount(t, st, &u.ID, "other", "other@example.com")
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	h, c, _ := smtpSecret(t, st, a, 0, now)

	// Only a secret of the named account; nil is for admins.
	if _, err := st.RevokeSMTPCredential(ctx, &b.ID, c.ID, &u.ID, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other account: %v", err)
	}
	e := store.TargetedEntry(store.ActorCLI, "smtp_credential.revoke")
	r, err := st.RevokeSMTPCredential(store.WithAudit(ctx, e), &a.ID, c.ID, &u.ID, now)
	if err != nil || r.AccountName != "journal" || r.Hint != c.Hint {
		t.Fatalf("revoke: %+v %v", r, err)
	}
	if _, err := st.SMTPLogin(ctx, h, now); !errors.Is(err, store.ErrSMTPCredentialRevoked) {
		t.Errorf("login: %v", err)
	}
	if err := st.SMTPCredentialUsable(ctx, c.ID, now); !errors.Is(err, store.ErrSMTPCredentialRevoked) {
		t.Errorf("usable: %v", err)
	}
	if _, err := st.RevokeSMTPCredential(ctx, nil, c.ID, nil, now); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("twice: %v", err)
	}
	rows := auditRows(t, connect(t, url))
	last := rows[len(rows)-1]
	if last.Action != "smtp_credential.revoke" || *last.TargetType != store.TargetAccount || *last.TargetID != itoa64(a.ID) ||
		*last.TargetName != "journal" || last.Details["credential_id"] != float64(c.ID) || last.Details["hint"] != c.Hint {
		t.Fatalf("audit %+v", last)
	}
	_, c2, _ := smtpSecret(t, st, b, 0, now)
	if _, err := st.RevokeSMTPCredential(ctx, nil, c2.ID, nil, now); err != nil {
		t.Errorf("admin: %v", err)
	}
	all, err := st.AllSMTPCredentials(ctx)
	if err != nil || len(all) != 2 || all[0].AccountName != "journal" || all[1].AccountName != "other" || all[0].OwnerName != "alice" || all[1].RevokedAt == nil {
		t.Fatalf("all: %+v %v", all, err)
	}
}

func TestSMTPLogin(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	u, _ := st.CreateUser(ctx, "alice", "h", true)
	a := createSMTPAccount(t, st, &u.ID, "journal", "journal@example.com")
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	h, c, _ := smtpSecret(t, st, a, 0, now)

	l, err := st.SMTPLogin(ctx, h, now)
	if err != nil || l.CredentialID != c.ID || l.AccountID != a.ID || l.ExpiresAt != nil || l.RevokedAt != nil {
		t.Fatalf("login: %+v %v", l, err)
	}
	if _, err := st.SMTPLogin(ctx, hash32(9), now); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
	if err := st.SMTPCredentialUsable(ctx, c.ID+100, now); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown id: %v", err)
	}

	// The time of use is recorded at most once a minute.
	used := func() time.Time {
		t.Helper()
		list, _ := st.SMTPCredentials(ctx, a.ID)
		if list[0].LastUsedAt == nil {
			t.Fatal("no last use")
		}
		return *list[0].LastUsedAt
	}
	if !used().Equal(now) {
		t.Fatalf("first use %v", used())
	}
	if _, err := st.SMTPLogin(ctx, h, now.Add(59*time.Second)); err != nil || !used().Equal(now) {
		t.Errorf("within a minute: %v %v", used(), err)
	}
	if _, err := st.SMTPLogin(ctx, h, now.Add(61*time.Second)); err != nil || !used().Equal(now.Add(61*time.Second)) {
		t.Errorf("after a minute: %v %v", used(), err)
	}

	// A disabled account logs in (its mail is refused later and stays
	// queued); a removed one does not.
	a, _ = st.GetAccount(ctx, a.ID)
	off := false
	if err := st.UpdateAccount(ctx, a.Ref(), store.AccountChange{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SMTPLogin(ctx, h, now); err != nil {
		t.Errorf("disabled: %v", err)
	}
	if _, err := st.ReceiveMessage(ctx, a.ID, receivedMeta(1), envelope("journal@example.com"), present); !errors.Is(err, store.ErrNotReceiving) {
		t.Fatalf("receive while disabled: %v", err)
	}
	on := true
	a, _ = st.GetAccount(ctx, a.ID)
	if err := st.UpdateAccount(ctx, a.Ref(), store.AccountChange{Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReceiveMessage(ctx, a.ID, receivedMeta(1), envelope("journal@example.com"), present); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.UsableSMTPCredentialsByOwner(ctx, u.ID, now); len(got) != 1 || got[0].AccountName != "journal" || got[0].N != 1 {
		t.Errorf("by owner: %+v", got)
	}
	a, _ = st.GetAccount(ctx, a.ID)
	if res, err := st.DeleteOrRemoveAccount(ctx, a.Ref()); err != nil || res != store.AccountRemoved {
		t.Fatalf("remove: %v %v", res, err)
	}
	if l, err := st.SMTPLogin(ctx, h, now); !errors.Is(err, store.ErrAccountRemoved) || l.AccountID != a.ID {
		t.Errorf("removed: %+v %v", l, err)
	}
	if err := st.SMTPCredentialUsable(ctx, c.ID, now); !errors.Is(err, store.ErrAccountRemoved) {
		t.Errorf("usable when removed: %v", err)
	}
	if got, _ := st.UsableSMTPCredentialsByOwner(ctx, u.ID, now); len(got) != 0 {
		t.Errorf("by owner after removal: %+v", got)
	}

	// A secret on an account of another kind (only possible past the
	// store) does not log in.
	imap := createAccount(t, st, "imap")
	h2 := hash32(7)
	if _, err := connect(t, url).Exec(ctx, "INSERT INTO smtp_credentials (account_id, secret_hash, hint) VALUES ($1, $2, 'abcd')", imap.ID, h2); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SMTPLogin(ctx, h2, now); !errors.Is(err, store.ErrNotSMTP) {
		t.Errorf("imap: %v", err)
	}
}

// Deleting an account deletes its secrets; housekeeping deletes secrets
// revoked or expired more than 30 days ago.
func TestSMTPCredentialCleanup(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	a := createSMTPAccount(t, st, nil, "journal", "journal@example.com")
	b := createSMTPAccount(t, st, nil, "other", "other@example.com")
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	week, day := 7*24*time.Hour, 24*time.Hour

	// first is revoked at now, second expires at now+week, third stays.
	_, first, _ := smtpSecret(t, st, a, 0, now)
	_, second, _ := smtpSecret(t, st, a, 0, now)
	_, third, _ := smtpSecret(t, st, a, week, now)
	if got := credentialStates(t, st, a.ID, now); got[first.ID] != store.SMTPCredentialRevoked || got[second.ID] != store.SMTPCredentialExpiring {
		t.Fatalf("states: %v", got)
	}
	for _, c := range []struct {
		at   time.Time
		want int64
	}{
		{now.Add(30 * day), 0},
		{now.Add(30*day + time.Second), 1},
		{now.Add(week + 30*day), 0},
		{now.Add(week + 30*day + time.Second), 1},
	} {
		if n, err := st.DeleteOldSMTPCredentials(ctx, c.at); err != nil || n != c.want {
			t.Fatalf("at %v: %d %v", c.at, n, err)
		}
	}
	if got := credentialStates(t, st, a.ID, now); len(got) != 1 || got[third.ID] != store.SMTPCredentialActive {
		t.Fatalf("left: %v", got)
	}

	smtpSecret(t, st, b, 0, now)
	if res, err := st.DeleteOrRemoveAccount(ctx, b.Ref()); err != nil || res != store.AccountDeleted {
		t.Fatalf("delete: %v %v", res, err)
	}
	var n int
	if err := st.QueryRowForTest(ctx, "SELECT count(*) FROM smtp_credentials WHERE account_id = $1", b.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("after delete: %d %v", n, err)
	}
}

// 00040 goes down and up again with secrets stored.
func TestSMTPCredentialsMigration(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	a := createSMTPAccount(t, st, nil, "journal", "journal@example.com")
	smtpSecret(t, st, a, 0, time.Now())
	downSMTPCredentialsMigration(t, st)
	var n int
	if err := st.QueryRowForTest(ctx, "SELECT count(*) FROM information_schema.tables WHERE table_name = 'smtp_credentials'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("table after down: %d %v", n, err)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	h, c, _ := smtpSecret(t, st, a, 0, time.Now())
	if l, err := st.SMTPLogin(ctx, h, time.Now()); err != nil || l.CredentialID != c.ID {
		t.Fatalf("after up: %+v %v", l, err)
	}
	// The database keeps one secret without an end per account.
	if _, err := connect(t, url).Exec(ctx, "INSERT INTO smtp_credentials (account_id, secret_hash, hint) VALUES ($1, $2, 'abcd')", a.ID, hash32(5)); err == nil ||
		!strings.Contains(err.Error(), "smtp_credentials_current_idx") {
		t.Fatalf("second current secret: %v", err)
	}
}

// downSMTPCredentialsMigration rolls back 00042_metrics_key_scope,
// 00041_storage_usage and
// 00040_smtp_credentials.
func downSMTPCredentialsMigration(t *testing.T, st *store.Store) {
	t.Helper()
	downStorageUsageMigration(t, st)
	if m, err := st.MigrateDown(context.Background()); err != nil || !strings.Contains(m, "00040_smtp_credentials") {
		t.Fatalf("down: %q %v", m, err)
	}
}

func hash32(b byte) []byte {
	h := make([]byte, 32)
	for i := range h {
		h[i] = b
	}
	return h
}

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

func maps1(m map[int64]string, id int64, state string) bool { return len(m) == 1 && m[id] == state }
