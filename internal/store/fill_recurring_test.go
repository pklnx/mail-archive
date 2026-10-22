package store_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

func newRecurringFill(a *store.Account, username string) *store.Fill {
	f := newFill(a, username)
	f.Cutoff, f.KeepRunning = nil, true
	return f
}

// saveFilled stores messages as a fill's batch in ff's folder.
func saveFilled(t *testing.T, st *store.Store, ff *store.FillFolder, metas ...store.MessageMeta) {
	t.Helper()
	ff.Counts.Imported += len(metas)
	locs := make([]store.Location, len(metas))
	for i := range locs {
		locs[i] = store.Location{InternalDate: time.Date(2026, 5, 1, 8, 0, i, 0, time.UTC)}
	}
	if _, err := st.SaveFillBatch(context.Background(), ff, metas, locs); err != nil {
		t.Fatal(err)
	}
}

func TestRecurringFillLifecycle(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	ctx := context.Background()
	a := createSMTPAccount(t, st, nil, "journal", "archive@example.com")

	// A fill has either a cut-off or keeps running.
	both := newFill(a, "alice@example.com")
	both.KeepRunning = true
	neither := newRecurringFill(a, "alice@example.com")
	neither.KeepRunning = false
	interval := time.Hour
	oneTimeInterval := newFill(a, "alice@example.com")
	oneTimeInterval.Interval = &interval
	for _, f := range []*store.Fill{both, neither, oneTimeInterval} {
		if err := st.CreateFill(ctx, f, seal); !errors.Is(err, store.ErrFillRule) {
			t.Errorf("fill %+v: %v", f, err)
		}
	}

	f := newRecurringFill(a, "alice@example.com")
	f.Interval = &interval
	f.Confirmed = []string{"Junk"}
	if err := st.CreateFill(ctx, f, seal); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetFill(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.FillRunning || !got.KeepRunning || got.Cutoff != nil || got.RecurringSince == nil ||
		got.RunStartedAt == nil || got.Interval == nil || *got.Interval != time.Hour || !got.IntoJournal() {
		t.Fatalf("recurring fill %+v", got)
	}
	// The confirmed trash folder has a row from the start.
	if len(got.Folders) != 1 || got.Folders[0].SourceName != "Junk" || got.Folders[0].FolderName != store.SMTPFolder {
		t.Errorf("folders %+v", got.Folders)
	}
	ff, err := st.FillFolder(ctx, got, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	ff.Counts.Waiting, ff.Done = 2, true
	saveFilled(t, st, ff, receivedMeta(1))

	// The end of a run keeps the password and counts in the health.
	if err := st.FinishFill(ctx, f.ID, store.FillOK, ""); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetFill(ctx, f.ID)
	if got.Status != store.FillOK || got.PasswordEnc == nil || got.FinishedAt == nil || !got.KeepRunning {
		t.Fatalf("after a run %+v", got)
	}
	health, err := st.FillHealth(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if h := health[f.ID]; h == nil || h.LastSuccessAt == nil || h.FailureStreak != 0 || h.Mailbox != "alice@example.com" ||
		h.Account != "journal" || h.Running || h.Interval == nil {
		t.Fatalf("health %+v", h)
	}
	// Between runs it is due for the runner.
	if due, err := st.RecurringFills(ctx); err != nil || len(due) != 1 || due[0].ID != f.ID {
		t.Errorf("recurring fills %+v, %v", due, err)
	}

	// A one-time fill that runs blocks the next run.
	once := newFill(a, "bob@example.com")
	if err := st.CreateFill(ctx, once, seal); err != nil {
		t.Fatal(err)
	}
	if err := st.StartFillRun(ctx, f.ID, time.Now()); !errors.Is(err, store.ErrFillRunning) {
		t.Errorf("run while another fill runs: %v", err)
	}
	if err := st.FinishFill(ctx, once.ID, store.FillOK, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.StartFillRun(ctx, f.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.StartFillRun(ctx, f.ID, time.Now()); !errors.Is(err, store.ErrFillNotRunning) {
		t.Errorf("start a running fill: %v", err)
	}
	// A run starts with fresh counts and every folder to visit.
	got, _ = st.GetFill(ctx, f.ID)
	if inbox := got.Folders[0]; inbox.SourceName != "INBOX" || inbox.Done || inbox.Counts != (store.FillCounts{}) || inbox.LastUID != ff.LastUID {
		t.Errorf("folder at the run's start %+v", inbox)
	}
	if got.Status != store.FillRunning || got.FinishedAt != nil || !got.RunStartedAt.After(*got.RecurringSince) {
		t.Errorf("running %+v", got)
	}
	if err := st.FinishFill(ctx, f.ID, store.FillFailed, "login failed"); err != nil {
		t.Fatal(err)
	}
	health, _ = st.FillHealth(ctx, nil)
	if h := health[f.ID]; h.FailureStreak != 1 || h.FailingSince == nil {
		t.Errorf("health after a failed run %+v", h)
	}

	// The database keeps no password of a stopped fill.
	if _, err := conn.Exec(ctx, "UPDATE smtp_fills SET keep_running = false WHERE id = $1", f.ID); err == nil {
		t.Error("a stopped fill kept its password")
	}
	if _, err := conn.Exec(ctx, "UPDATE smtp_fills SET status = 'cancelled' WHERE id = $1", f.ID); err == nil {
		t.Error("a cancelled fill keeps running")
	}

	if err := st.SetFillInterval(ctx, f.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.SetFillInterval(ctx, once.ID, nil); !errors.Is(err, store.ErrFillNotRecurring) {
		t.Errorf("interval of a one-time fill: %v", err)
	}
	if err := st.SetFillRecurring(ctx, f.ID, nil, nil, seal); !errors.Is(err, store.ErrFillKeepsRunning) {
		t.Errorf("switch on twice: %v", err)
	}
	if err := st.ResumeFill(ctx, f.ID, seal); !errors.Is(err, store.ErrFillNotResumable) {
		t.Errorf("resume a fill that keeps running: %v", err)
	}

	// Stop between runs: cancelled, no password, health unchanged.
	if err := st.CancelFill(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetFill(ctx, f.ID)
	if got.Status != store.FillCancelled || got.KeepRunning || got.PasswordEnc != nil || got.FinishedAt == nil || got.Interval != nil {
		t.Fatalf("stopped %+v", got)
	}
	if err := st.CancelFill(ctx, f.ID); !errors.Is(err, store.ErrFillNotRunning) {
		t.Errorf("stop twice: %v", err)
	}
	if h, _ := st.FillHealth(ctx, nil); h[f.ID] != nil {
		t.Errorf("health of a stopped fill %+v", h[f.ID])
	}
	// Resumed, it keeps running again and starts a run.
	if err := st.ResumeFill(ctx, f.ID, seal); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetFill(ctx, f.ID)
	if got.Status != store.FillRunning || !got.KeepRunning || got.PasswordEnc == nil || got.Folders[0].Counts != (store.FillCounts{}) {
		t.Errorf("resumed %+v", got)
	}
	if h, _ := st.FillHealth(ctx, nil); h[f.ID] == nil || h[f.ID].FailureStreak != 1 || !h[f.ID].Running {
		t.Errorf("health after the resume %+v", h[f.ID])
	}

	// A disabled account's fills are not due.
	if err := st.FinishFill(ctx, f.ID, store.FillOK, ""); err != nil {
		t.Fatal(err)
	}
	off := false
	if err := st.UpdateAccount(ctx, a.Ref(), store.AccountChange{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if due, err := st.RecurringFills(ctx); err != nil || len(due) != 0 {
		t.Errorf("recurring fills of a disabled account %+v, %v", due, err)
	}
}

func TestSetFillRecurring(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, "owner", "h", true)
	if err != nil {
		t.Fatal(err)
	}
	a := createSMTPAccount(t, st, &u.ID, "journal", "archive@example.com")
	f := newFill(a, "alice@example.com")
	if err := st.CreateFill(ctx, f, seal); err != nil {
		t.Fatal(err)
	}
	if err := st.SetFillRecurring(ctx, f.ID, nil, nil, seal); !errors.Is(err, store.ErrFillRunning) {
		t.Errorf("switch on a running fill: %v", err)
	}
	// The cut-off moved the folder's last UID past messages it left out.
	ff, err := st.FillFolder(ctx, f, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	ff.LastUID, ff.Done = 9, true
	if err := st.UpdateFillFolder(ctx, ff); err != nil {
		t.Fatal(err)
	}
	if err := st.CancelFill(ctx, f.ID); err != nil {
		t.Fatal(err)
	}
	day := 24 * time.Hour
	if err := st.SetFillRecurring(ctx, f.ID, &day, []string{"Trash"}, seal); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetFill(ctx, f.ID)
	if got.Status != store.FillRunning || !got.KeepRunning || got.Cutoff == nil || got.RecurringSince == nil ||
		*got.Interval != day || got.PasswordEnc == nil || len(got.Folders) != 2 || got.Folders[1].SourceName != "Trash" {
		t.Errorf("switched on %+v", got)
	}
	// Its folders are scanned again from the start.
	if inbox := got.Folders[0]; inbox.SourceName != "INBOX" || inbox.LastUID != 0 || inbox.Done {
		t.Errorf("folder after switching on %+v", inbox)
	}
	if err := st.SetFillRecurring(ctx, 9999, nil, nil, seal); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing fill: %v", err)
	}

	// The card shows every recurring fill and the newest fill.
	if err := st.FinishFill(ctx, f.ID, store.FillOK, ""); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		once := newFill(a, "bob@example.com")
		if err := st.CreateFill(ctx, once, seal); err != nil {
			t.Fatal(err)
		}
		if err := st.FinishFill(ctx, once.ID, store.FillOK, ""); err != nil {
			t.Fatal(err)
		}
	}
	all, err := st.AccountFills(ctx, a.ID)
	if err != nil || len(all) != 3 {
		t.Fatalf("fills %+v, %v", all, err)
	}
	owned, err := st.OwnerFills(ctx, u.ID)
	if err != nil || len(owned[a.ID]) != 2 || owned[a.ID][0].ID != all[0].ID || owned[a.ID][1].ID != f.ID {
		t.Errorf("owner fills %+v, %v", owned, err)
	}
}

// TestRemoveAccountStopsRecurringFills: removing an account stops its
// recurring fills between runs too and wipes their passwords.
func TestRemoveAccountStopsRecurringFills(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	a := createSMTPAccount(t, st, nil, "journal", "archive@example.com")
	f := newRecurringFill(a, "alice@example.com")
	if err := st.CreateFill(ctx, f, seal); err != nil {
		t.Fatal(err)
	}
	// With mail, the account is removed rather than deleted.
	ff, err := st.FillFolder(ctx, f, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	saveFilled(t, st, ff, receivedMeta(1))
	if err := st.FinishFill(ctx, f.ID, store.FillOK, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetFill(ctx, f.ID); got.PasswordEnc == nil {
		t.Fatal("idle recurring fill without password")
	}
	a, _ = st.GetAccount(ctx, a.ID)
	if res, err := st.DeleteOrRemoveAccount(ctx, a.Ref()); err != nil || res != store.AccountRemoved {
		t.Fatalf("remove: %v, %v", res, err)
	}
	got, _ := st.GetFill(ctx, f.ID)
	if got.Status != store.FillCancelled || got.KeepRunning || got.PasswordEnc != nil || got.Error != "the account was removed" {
		t.Errorf("fill of a removed account %+v", got)
	}
	if err := st.SetFillRecurring(ctx, f.ID, nil, nil, seal); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("switch on a fill of a removed account: %v", err)
	}
}

func TestFillImported(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	a := createSMTPAccount(t, st, nil, "journal", "archive@example.com")
	f := newRecurringFill(a, "alice@example.com")
	if err := st.CreateFill(ctx, f, seal); err != nil {
		t.Fatal(err)
	}
	ff, err := st.FillFolder(ctx, f, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	m1 := receivedMeta(1)
	m1.MessageID = "one@example.com"
	m2 := receivedMeta(2)
	m2.MessageID = "two@example.com"
	saveFilled(t, st, ff, m1, m2)
	// A journaled message with a Message-ID is not this fill's.
	m3 := receivedMeta(3)
	m3.MessageID = "three@example.com"
	if _, err := st.ReceiveMessage(ctx, a.ID, m3, envelope("x@example.com"), present); err != nil {
		t.Fatal(err)
	}
	got, err := st.FillImported(ctx, f.ID, []string{"one@example.com", "three@example.com", ""})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	if len(got) != 1 || !got[store.NewImportedKey("one@example.com", at, m1.Size)] {
		t.Errorf("imported %v", got)
	}
	// The key needs the same date and size.
	if got[store.NewImportedKey("one@example.com", at.Add(time.Second), m1.Size)] || got[store.NewImportedKey("one@example.com", at, m1.Size+1)] {
		t.Error("matched another date or size")
	}
	if got, err := st.FillImported(ctx, f.ID, nil); err != nil || len(got) != 0 {
		t.Errorf("no IDs: %v, %v", got, err)
	}
}

// TestConvertLegacyFill copies a fill from before #112 into JOURNAL: one
// location per message, with JOURNAL's UIDVALIDITY and UIDs after its
// last, none for a message JOURNAL holds; the fill's folders then fill
// JOURNAL from the start.
func TestConvertLegacyFill(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	ctx := context.Background()
	a := createSMTPAccount(t, st, nil, "journal", "archive@example.com")
	// Two journaled messages, UIDs 1 and 2; message 2 is also in the
	// mailbox with the same bytes.
	for _, sha := range []byte{1, 2} {
		if _, err := st.ReceiveMessage(ctx, a.ID, receivedMeta(sha), envelope("x@example.com"), present); err != nil {
			t.Fatal(err)
		}
	}
	f := newFill(a, "alice@example.com")
	if err := st.CreateFill(ctx, f, seal); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "UPDATE smtp_fills SET folder_prefix = 'alice' WHERE id = $1", f.ID); err != nil {
		t.Fatal(err)
	}
	f, _ = st.GetFill(ctx, f.ID)
	inbox, err := st.FillFolder(ctx, f, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	sent, err := st.FillFolder(ctx, f, "Sent")
	if err != nil {
		t.Fatal(err)
	}
	if inbox.FolderName != "alice/INBOX" || sent.FolderName != "alice/Sent" {
		t.Fatalf("legacy folders %+v %+v", inbox, sent)
	}
	inbox.LastUID, sent.LastUID = 40, 7
	saveFilled(t, st, inbox, receivedMeta(2), receivedMeta(10), receivedMeta(11))
	// Message 11 is filed in both folders.
	saveFilled(t, st, sent, receivedMeta(11), receivedMeta(12))
	if err := st.FinishFill(ctx, f.ID, store.FillOK, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.SetFillRecurring(ctx, f.ID, nil, nil, seal); err != nil {
		t.Fatal(err)
	}
	f, _ = st.GetFill(ctx, f.ID)
	n, err := st.ConvertLegacyFill(ctx, f)
	if err != nil || n != 3 {
		t.Fatalf("converted %d, %v", n, err)
	}
	type loc struct {
		sha                    string
		uidvalidity, uid, fill int64
	}
	rows, err := conn.Query(ctx, `SELECT trim(l.message_sha256), l.uidvalidity, l.uid, coalesce(l.fill_id, 0)
		FROM message_locations l JOIN folders fo ON fo.id = l.folder_id
		WHERE fo.account_id = $1 AND fo.name = $2 ORDER BY l.uid`, a.ID, store.SMTPFolder)
	if err != nil {
		t.Fatal(err)
	}
	locs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (loc, error) {
		var l loc
		err := r.Scan(&l.sha, &l.uidvalidity, &l.uid, &l.fill)
		return l, err
	})
	if err != nil || len(locs) != 5 {
		t.Fatalf("journal locations %+v, %v", locs, err)
	}
	for i, l := range locs {
		if l.uid != int64(i+1) || l.uidvalidity != locs[0].uidvalidity || (i >= 2) != (l.fill == f.ID) {
			t.Errorf("location %d: %+v", i, l)
		}
	}
	if !slices.Equal([]string{locs[2].sha[:2], locs[3].sha[:2], locs[4].sha[:2]}, []string{"0a", "0b", "0c"}) {
		t.Errorf("copied %+v", locs[2:])
	}
	var lastUID int64
	if err := conn.QueryRow(ctx, "SELECT last_uid FROM folders WHERE account_id = $1 AND name = $2", a.ID, store.SMTPFolder).Scan(&lastUID); err != nil || lastUID != 5 {
		t.Errorf("last UID %d, %v", lastUID, err)
	}
	// The IMAP folders now fill JOURNAL, from the start; the old folders
	// keep their mail.
	f, _ = st.GetFill(ctx, f.ID)
	for _, ff := range f.Folders {
		if ff.FolderName != store.SMTPFolder || ff.LastUID != 0 || ff.Done {
			t.Errorf("folder after the conversion %+v", ff)
		}
	}
	var legacy int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM message_locations l JOIN folders fo ON fo.id = l.folder_id
		WHERE fo.name LIKE 'alice/%'`).Scan(&legacy); err != nil || legacy != 5 {
		t.Errorf("legacy locations %d, %v", legacy, err)
	}
	// Nothing is left to copy.
	if n, err := st.ConvertLegacyFill(ctx, f); err != nil || n != 0 {
		t.Errorf("second conversion %d, %v", n, err)
	}
}

func TestFillHealthState(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	for _, c := range []struct {
		name string
		h    store.FillHealth
		busy *time.Time
		want string
	}{
		{"new", store.FillHealth{Enabled: true, RecurringSince: *ago(time.Hour)}, nil, store.HealthOK},
		{"never succeeded", store.FillHealth{Enabled: true, RecurringSince: *ago(13 * time.Hour)}, nil, store.HealthStale},
		{"recent success", store.FillHealth{Enabled: true, RecurringSince: *ago(48 * time.Hour), LastSuccessAt: ago(time.Hour)}, nil, store.HealthOK},
		// Switched on again after a long stop: counts from then.
		{"old success, switched on again", store.FillHealth{Enabled: true, RecurringSince: *ago(time.Hour), LastSuccessAt: ago(48 * time.Hour)}, nil, store.HealthOK},
		{"running", store.FillHealth{Enabled: true, RecurringSince: *ago(48 * time.Hour), LastSuccessAt: ago(13 * time.Hour)}, ago(time.Hour), store.HealthOK},
		{"failing", store.FillHealth{Enabled: true, RecurringSince: *ago(time.Hour), FailureStreak: 3}, nil, store.HealthFailing},
		{"disabled", store.FillHealth{RecurringSince: *ago(48 * time.Hour), FailureStreak: 9}, nil, store.HealthOK},
	} {
		if got := c.h.State(3, 6*time.Hour, c.busy, now); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	// HealthState is what both use.
	if got := store.HealthState(true, 0, now.Add(-13*time.Hour), 3, 6*time.Hour, nil, now); got != store.HealthStale {
		t.Errorf("HealthState %s", got)
	}
	if got := store.HealthState(true, 0, now.Add(-13*time.Hour), 3, 0, nil, now); got != store.HealthOK {
		t.Errorf("HealthState without schedule %s", got)
	}
}

func TestFillNotifications(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, "owner", "h", true)
	if err != nil {
		t.Fatal(err)
	}
	a := createSMTPAccount(t, st, &u.ID, "journal", "archive@example.com")
	f := newRecurringFill(a, "alice@example.com")
	if err := st.CreateFill(ctx, f, seal); err != nil {
		t.Fatal(err)
	}
	fail := func() {
		t.Helper()
		if err := st.FinishFill(ctx, f.ID, store.FillFailed, "login failed"); err != nil {
			t.Fatal(err)
		}
		if err := st.StartFillRun(ctx, f.ID, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	fail()
	fail()
	lease := []byte("lease-1")
	if got, err := st.ClaimFillNotifications(ctx, lease, time.Minute, 3, 10); err != nil || len(got) != 0 {
		t.Fatalf("claimed below the threshold %+v, %v", got, err)
	}
	if err := st.FinishFill(ctx, f.ID, store.FillFailed, "login failed"); err != nil {
		t.Fatal(err)
	}
	if n, err := st.CountFailingByOwner(ctx, 3); err != nil || n[u.ID] != 1 {
		t.Errorf("failing by owner %v, %v", n, err)
	}
	got, err := st.ClaimFillNotifications(ctx, lease, time.Minute, 3, 10)
	if err != nil || len(got) != 1 {
		t.Fatalf("claimed %+v, %v", got, err)
	}
	if p := got[0]; p.FillID != f.ID || p.AccountID != a.ID || p.Mailbox != "alice@example.com" || p.Owner != "owner" ||
		p.FailureStreak != 3 || p.LastError != "login failed" || p.NotifiedState != store.NotifiedOK {
		t.Errorf("pending %+v", p)
	}
	if err := st.FinishFillNotification(ctx, f.ID, []byte("other"), store.NotifiedFailing, ""); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("finish with another lease: %v", err)
	}
	if err := st.FinishFillNotification(ctx, f.ID, lease, store.NotifiedFailing, ""); err != nil {
		t.Fatal(err)
	}
	// A success makes a recovery due.
	if err := st.StartFillRun(ctx, f.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishFill(ctx, f.ID, store.FillPartial, "INBOX: oops"); err != nil {
		t.Fatal(err)
	}
	got, err = st.ClaimFillNotifications(ctx, lease, time.Minute, 3, 10)
	if err != nil || len(got) != 1 || got[0].NotifiedState != store.NotifiedFailing || got[0].FailureStreak != 0 {
		t.Fatalf("recovery %+v, %v", got, err)
	}
	if err := st.DeferFillNotification(ctx, f.ID, lease, time.Now().Add(time.Hour), "503"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.ClaimFillNotifications(ctx, lease, time.Minute, 3, 10); len(got) != 0 {
		t.Errorf("claimed before the retry %+v", got)
	}
}

func TestRecurringFillMigration(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	ctx := context.Background()
	var names []string
	rows, err := conn.Query(ctx, `SELECT conname FROM pg_constraint WHERE conrelid = 'smtp_fills'::regclass ORDER BY conname`)
	if err != nil {
		t.Fatal(err)
	}
	if names, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"smtp_fills_keep_running_recurring", "smtp_fills_keep_running_not_cancelled", "smtp_fills_rule", "smtp_fills_password_check"} {
		if !slices.Contains(names, want) {
			t.Errorf("constraint %s missing in %v", want, names)
		}
	}
	if slices.Contains(names, "smtp_fills_check1") {
		t.Errorf("the old password check is still there: %v", names)
	}

	// Down: a recurring fill stops and loses its password, gets a cut-off;
	// nothing archived is lost. Up again works.
	a := createSMTPAccount(t, st, nil, "journal", "archive@example.com")
	f := newRecurringFill(a, "alice@example.com")
	if err := st.CreateFill(ctx, f, seal); err != nil {
		t.Fatal(err)
	}
	ff, err := st.FillFolder(ctx, f, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	saveFilled(t, st, ff, receivedMeta(1))
	if err := st.FinishFill(ctx, f.ID, store.FillOK, ""); err != nil {
		t.Fatal(err)
	}
	if m, err := st.MigrateDown(ctx); err != nil || !strings.Contains(m, "00029_smtp_imports") {
		t.Fatalf("down the smtp imports: %q, %v", m, err)
	}
	if m, err := st.MigrateDown(ctx); err != nil || !strings.Contains(m, "00028_recurring_fills") {
		t.Fatalf("down: %q, %v", m, err)
	}
	var hasPassword bool
	var cutoff *time.Time
	if err := conn.QueryRow(ctx, "SELECT password_enc IS NOT NULL, cutoff FROM smtp_fills WHERE id = $1", f.ID).Scan(&hasPassword, &cutoff); err != nil ||
		hasPassword || cutoff == nil {
		t.Errorf("after down: password %v, cutoff %v, %v", hasPassword, cutoff, err)
	}
	var locations int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM message_locations WHERE fill_id = $1", f.ID).Scan(&locations); err != nil || locations != 1 {
		t.Errorf("locations after down %d, %v", locations, err)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetFill(ctx, f.ID)
	if err != nil || got.KeepRunning || got.Recurring() || got.Status != store.FillOK {
		t.Errorf("after up %+v, %v", got, err)
	}
}
