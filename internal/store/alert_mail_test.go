package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// downAlertMailMigration rolls back 00045_alert_mail.
func downAlertMailMigration(t *testing.T, st *store.Store) {
	t.Helper()
	if m, err := st.MigrateDown(context.Background()); err != nil || !strings.Contains(m, "00045_alert_mail") {
		t.Fatalf("down: %q %v", m, err)
	}
}

// confirmAddress gives a user a confirmed notification address.
func confirmAddress(t *testing.T, st *store.Store, userID int64, email string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	if err := st.SetNotifyEmail(ctx, userID, store.NotifyCodeRequest{Email: email, CodeHash: store.NotifyCodeHash(userID, "ABCDEF"), Now: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.ConfirmNotifyEmail(ctx, userID, store.NotifyCodeHash(userID, "ABCDEF"), now); err != nil {
		t.Fatal(err)
	}
}

// TestAlertMailMigration: Up adds the columns; Down drops them with the
// queued alert mail; Up again works.
func TestAlertMailMigration(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	ctx := context.Background()
	admin, err := st.CreateUser(ctx, "admin", "h", true)
	if err != nil {
		t.Fatal(err)
	}
	confirmAddress(t, st, admin.ID, "admin@example.com")
	if _, err := st.EnqueueJobs(ctx, []store.NewJob{{Kind: store.JobNotifyMail, Key: "e:1", MaxAttempts: 1}}, false); err != nil {
		t.Fatal(err)
	}
	downAlertMailMigration(t, st)
	var cols, jobs int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'user_notify_emails' OR (column_name = 'mail_enqueued_event' AND table_name <> 'storage_states')
			OR column_name IN ('mailed_state', 'mailed_seq')`).Scan(&cols); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM jobs WHERE kind = 'notify_mail'").Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if cols != 0 || jobs != 0 {
		t.Fatalf("after down: %d columns, %d jobs", cols, jobs)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if s, err := st.NotifyEmail(ctx, admin.ID); err != nil || s.Email != "" {
		t.Fatalf("after re-up: %+v %v", s, err)
	}
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE column_name IN ('mailed_state', 'mailed_seq')`).Scan(&cols); err != nil || cols != 3 {
		t.Fatalf("after re-up: %d mail state columns, want 3: %v", cols, err)
	}
	if _, err := conn.Exec(ctx, "INSERT INTO user_notify_emails (user_id, notify_email_confirmed_at) VALUES ($1, now())", admin.ID); err == nil {
		t.Fatal("a confirmed time without an address was accepted")
	}
	// The users rows keep their columns: commands that read them work
	// while the migration is pending (TestStartUpgrade).
	var userCols int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM information_schema.columns WHERE table_name = 'users' AND column_name LIKE 'notify%'").Scan(&userCols); err != nil || userCols != 0 {
		t.Fatalf("users has %d notify columns: %v", userCols, err)
	}
}

// TestNotifyEmailCode: set, wrong code, expiry, the tries per code, the
// codes per hour, and only admins.
func TestNotifyEmailCode(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	admin, err := st.CreateUser(ctx, "admin", "h", true)
	if err != nil {
		t.Fatal(err)
	}
	member, err := st.CreateUser(ctx, "member", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	code := func(c string) []byte { return store.NotifyCodeHash(admin.ID, c) }
	set := func(at time.Time, c string) error {
		return st.SetNotifyEmail(ctx, admin.ID, store.NotifyCodeRequest{Email: "admin@example.com", CodeHash: code(c), Language: "de", Now: at})
	}

	if err := st.SetNotifyEmail(ctx, member.ID, store.NotifyCodeRequest{Email: "m@example.com", CodeHash: code("X"), Now: now}); !errors.Is(err, store.ErrNotifyAdminOnly) {
		t.Fatalf("member: %v", err)
	}
	if err := set(now, "AAAAAA"); err != nil {
		t.Fatal(err)
	}
	if err := st.ConfirmNotifyEmail(ctx, admin.ID, code("BBBBBB"), now); !errors.Is(err, store.ErrNotifyWrongCode) {
		t.Fatalf("wrong code: %v", err)
	}
	if err := st.ConfirmNotifyEmail(ctx, admin.ID, code("AAAAAA"), now.Add(store.NotifyCodeTTL)); !errors.Is(err, store.ErrNotifyNoCode) {
		t.Fatalf("expired: %v", err)
	}
	// The code of another user does not match.
	if err := st.ConfirmNotifyEmail(ctx, admin.ID, store.NotifyCodeHash(member.ID, "AAAAAA"), now); !errors.Is(err, store.ErrNotifyWrongCode) {
		t.Fatalf("another user's hash: %v", err)
	}
	// Two wrong tries counted so far (and one expired): two more wrong,
	// the fifth spends the code, and the right one no longer counts.
	for i, want := range []error{store.ErrNotifyWrongCode, store.ErrNotifyCodeTries, store.ErrNotifyCodeTries} {
		if err := st.ConfirmNotifyEmail(ctx, admin.ID, code("CCCCCC"), now); !errors.Is(err, want) {
			t.Fatalf("try %d: %v, want %v", i+4, err, want)
		}
	}
	if err := st.ConfirmNotifyEmail(ctx, admin.ID, code("AAAAAA"), now); !errors.Is(err, store.ErrNotifyCodeTries) {
		t.Fatalf("right code after the tries: %v", err)
	}
	if s, _ := st.NotifyEmail(ctx, admin.ID); s.ConfirmedAt != nil || s.CodeTries != store.NotifyCodeMaxTries {
		t.Fatalf("state %+v", s)
	}

	// Three codes per hour: the second and third within the hour pass,
	// the fourth is refused until the hour of the first is over.
	if err := set(now.Add(10*time.Minute), "DDDDDD"); err != nil {
		t.Fatal(err)
	}
	if err := set(now.Add(20*time.Minute), "EEEEEE"); err != nil {
		t.Fatal(err)
	}
	var limit *store.NotifyCodeLimitError
	if err := set(now.Add(30*time.Minute), "FFFFFF"); !errors.As(err, &limit) || !limit.RetryAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("fourth code: %v", err)
	}
	// The refused request changed nothing: the third code still works.
	if err := st.ConfirmNotifyEmail(ctx, admin.ID, code("EEEEEE"), now.Add(30*time.Minute)); err != nil {
		t.Fatalf("third code: %v", err)
	}
	s, err := st.NotifyEmail(ctx, admin.ID)
	if err != nil || s.Email != "admin@example.com" || s.ConfirmedAt == nil || s.CodeExpiresAt != nil {
		t.Fatalf("confirmed: %+v %v", s, err)
	}
	r, err := st.AlertMailRecipient(ctx, admin.ID)
	if err != nil || r.Email != "admin@example.com" || r.NotifyLanguage != "de" {
		t.Fatalf("recipient %+v %v", r, err)
	}
	if err := set(now.Add(61*time.Minute), "GGGGGG"); err != nil {
		t.Fatalf("a new hour: %v", err)
	}
	if _, err := st.AlertMailRecipient(ctx, admin.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a new code unconfirms: %v", err)
	}
	if err := st.RemoveNotifyEmail(ctx, admin.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.RemoveNotifyEmail(ctx, admin.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second remove: %v", err)
	}
}

// TestAlertMailEnqueue: a claim enqueues one job per recipient (admins
// with a confirmed address who are not locked), once per event: the same
// event claimed again enqueues nothing, also after the job is done, and a
// new event enqueues again.
func TestAlertMailEnqueue(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	ctx := context.Background()
	var ids []int64
	for _, u := range []struct {
		name          string
		admin, locked bool
		confirmed     bool
	}{{"a1", true, false, true}, {"a2", true, false, true}, {"a3", true, true, true}, {"a4", true, false, false}, {"m1", false, false, true}} {
		usr, err := st.CreateUser(ctx, u.name, "h", u.admin)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, usr.ID)
		if u.confirmed && u.admin {
			confirmAddress(t, st, usr.ID, u.name+"@example.com")
		}
		if u.confirmed && !u.admin {
			// A member never gets an address through the store; a row
			// set by hand still gets no mail.
			if _, err := conn.Exec(ctx, "INSERT INTO user_notify_emails (user_id, notify_email, notify_email_confirmed_at) VALUES ($1, 'm1@example.com', now())", usr.ID); err != nil {
				t.Fatal(err)
			}
		}
		if u.locked {
			if _, err := conn.Exec(ctx, "UPDATE users SET locked_at = now() WHERE id = $1", usr.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := st.SetNotifyEmail(ctx, ids[3], store.NotifyCodeRequest{Email: "a4@example.com", CodeHash: store.NotifyCodeHash(ids[3], "X"), Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
	mail := func(seq *int64) store.AlertMail[store.PendingStorageNotification] {
		return func(p store.PendingStorageNotification) *store.AlertMailEvent {
			*seq = p.EventSeq
			return &store.AlertMailEvent{ID: "storage:instance:" + itoa(p.EventSeq), Payload: store.AlertMailPayload{Kind: store.AlertMailStorage, Level: p.State}}
		}
	}
	transition := func(state string) {
		if _, err := conn.Exec(ctx, "UPDATE storage_states SET state = $1, event_seq = event_seq + 1 WHERE group_id IS NULL", state); err != nil {
			t.Fatal(err)
		}
	}
	jobs := func() map[string]string {
		rows, err := conn.Query(ctx, "SELECT key, state, payload::text FROM jobs WHERE kind = 'notify_mail' ORDER BY key")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var k, s, p string
			if err := rows.Scan(&k, &s, &p); err != nil {
				t.Fatal(err)
			}
			var pl store.AlertMailPayload
			if err := json.Unmarshal([]byte(p), &pl); err != nil || pl.Event+":"+itoa(pl.User) != k {
				t.Fatalf("payload %s of %s: %v", p, k, err)
			}
			out[k] = s
		}
		return out
	}
	claim := func(seq *int64) int {
		t.Helper()
		got, err := st.ClaimStorageNotifications(ctx, []byte("lease-x"), time.Minute, 10, mail(seq))
		if err != nil {
			t.Fatal(err)
		}
		// Released without finishing: as after a failed webhook request.
		if _, err := conn.Exec(ctx, "UPDATE storage_states SET notify_lease = NULL, notify_lease_until = NULL"); err != nil {
			t.Fatal(err)
		}
		return len(got)
	}

	var seq int64
	transition("warn")
	for range 3 {
		if n := claim(&seq); n != 1 {
			t.Fatalf("claimed %d", n)
		}
	}
	want := map[string]string{"storage:instance:1:" + itoa(ids[0]): "queued", "storage:instance:1:" + itoa(ids[1]): "queued"}
	if got := jobs(); !sameJobs(got, want) {
		t.Fatalf("jobs after three claims %v, want %v", got, want)
	}
	// A done job is not queued again by another claim of the event, also
	// when the outbox forgot the event.
	if _, err := conn.Exec(ctx, "UPDATE jobs SET state = 'done', finished_at = now() WHERE kind = 'notify_mail'"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "UPDATE storage_states SET mail_enqueued_event = NULL"); err != nil {
		t.Fatal(err)
	}
	claim(&seq)
	for k, s := range jobs() {
		if s != "done" {
			t.Fatalf("job %s %s after a repeated claim", k, s)
		}
	}
	transition("ok")
	claim(&seq)
	transition("warn")
	claim(&seq)
	if got := jobs(); len(got) != 6 || got["storage:instance:3:"+itoa(ids[1])] != "queued" {
		t.Fatalf("warn, ok, warn: %v", got)
	}
	// Without mail, nothing is enqueued.
	transition("full")
	if _, err := st.ClaimStorageNotifications(ctx, []byte("lease-y"), time.Minute, 10, nil); err != nil {
		t.Fatal(err)
	}
	if got := jobs(); len(got) != 6 {
		t.Fatalf("a claim without mail enqueued: %v", got)
	}
}

func sameJobs(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// TestAlertMailEnqueuedEvent: mail_enqueued_event alone keeps a webhook
// retry from mailing an event again where the job key cannot: to an admin
// who confirmed an address after the first claim, and after the event's
// done jobs were purged. The mail's own state (mailed_seq) agrees: the
// event is not due as mail either. A new transition is mailed to both.
func TestAlertMailEnqueuedEvent(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	ctx := context.Background()
	a1, err := st.CreateUser(ctx, "a1", "h", true)
	if err != nil {
		t.Fatal(err)
	}
	a2, err := st.CreateUser(ctx, "a2", "h", true)
	if err != nil {
		t.Fatal(err)
	}
	confirmAddress(t, st, a1.ID, "a1@example.com")
	mail := func(p store.PendingStorageNotification) *store.AlertMailEvent {
		return &store.AlertMailEvent{ID: "storage:instance:" + itoa(p.EventSeq), Payload: store.AlertMailPayload{Kind: store.AlertMailStorage, Level: p.State}}
	}
	transition := func(state string) {
		t.Helper()
		if _, err := conn.Exec(ctx, "UPDATE storage_states SET state = $1, event_seq = event_seq + 1 WHERE group_id IS NULL", state); err != nil {
			t.Fatal(err)
		}
	}
	// claim claims as a notifier whose webhook request then fails: the
	// lease is released and the transition stays due.
	claim := func() {
		t.Helper()
		got, err := st.ClaimStorageNotifications(ctx, []byte("lease-x"), time.Minute, 10, mail)
		if err != nil || len(got) != 1 {
			t.Fatalf("claimed %d: %v", len(got), err)
		}
		if _, err := conn.Exec(ctx, "UPDATE storage_states SET notify_lease = NULL, notify_lease_until = NULL"); err != nil {
			t.Fatal(err)
		}
	}
	jobs := func(user int64) int {
		t.Helper()
		var n int
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM jobs WHERE kind = 'notify_mail' AND key LIKE $1", "%:"+itoa(user)).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	notDue := func() {
		t.Helper()
		n, err := st.EnqueueDueStorageMail(ctx, 10, func(p store.PendingStorageNotification) *store.AlertMailEvent {
			t.Errorf("storage transition %d due as mail", p.EventSeq)
			return nil
		})
		if err != nil || n != 0 {
			t.Fatalf("due as mail: %d %v", n, err)
		}
	}

	transition("warn")
	claim()
	if jobs(a1.ID) != 1 || jobs(a2.ID) != 0 {
		t.Fatalf("first claim: a1 %d, a2 %d jobs", jobs(a1.ID), jobs(a2.ID))
	}
	confirmAddress(t, st, a2.ID, "a2@example.com")
	claim()
	if n := jobs(a2.ID); n != 0 {
		t.Fatalf("admin confirmed after the first claim got %d jobs for the same event", n)
	}
	notDue()

	// The done job is purged while the webhook still retries the event.
	if _, err := conn.Exec(ctx, "UPDATE jobs SET state = 'done', finished_at = now() - interval '2 hours' WHERE kind = 'notify_mail'"); err != nil {
		t.Fatal(err)
	}
	if n, err := st.DeleteDoneJobs(ctx, time.Hour); err != nil || n != 1 {
		t.Fatalf("purged %d: %v", n, err)
	}
	claim()
	if jobs(a1.ID) != 0 || jobs(a2.ID) != 0 {
		t.Fatalf("after the purge: a1 %d, a2 %d jobs for the same event", jobs(a1.ID), jobs(a2.ID))
	}
	notDue()

	transition("full")
	claim()
	if jobs(a1.ID) != 1 || jobs(a2.ID) != 1 {
		t.Fatalf("new transition: a1 %d, a2 %d jobs", jobs(a1.ID), jobs(a2.ID))
	}
	notDue()
}

// TestAlertMailDueByOwnState: the mail that is due is decided by the
// mail's own state, not the webhook's: a transition the webhook never
// claimed is due as mail, once; a scope back at ok is due only after an
// earlier mail of it.
func TestAlertMailDueByOwnState(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	ctx := context.Background()
	admin, err := st.CreateUser(ctx, "admin", "h", true)
	if err != nil {
		t.Fatal(err)
	}
	confirmAddress(t, st, admin.ID, "admin@example.com")
	var seen []string
	mail := func(p store.PendingStorageNotification) *store.AlertMailEvent {
		seen = append(seen, p.State)
		return &store.AlertMailEvent{ID: "storage:instance:" + itoa(p.EventSeq), Payload: store.AlertMailPayload{Kind: store.AlertMailStorage, Level: p.State}}
	}
	set := func(sql string) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	due := func() int {
		t.Helper()
		n, err := st.EnqueueDueStorageMail(ctx, 10, mail)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	// A transition back to ok that was never mailed is no news.
	set("UPDATE storage_states SET state = 'ok', event_seq = 2 WHERE group_id IS NULL")
	if n := due(); n != 0 {
		t.Fatalf("ok without an earlier mail: %d due", n)
	}
	// The webhook waits for its retry an hour from now, and holds a lease.
	set(`UPDATE storage_states SET state = 'warn', event_seq = 3, notify_next_at = now() + interval '1 hour',
		notify_lease = 'x', notify_lease_until = now() + interval '1 hour' WHERE group_id IS NULL`)
	if n := due(); n != 1 {
		t.Fatalf("warn during the webhook's backoff: %d due", n)
	}
	if n := due(); n != 0 {
		t.Fatalf("warn again: %d due", n)
	}
	set("UPDATE storage_states SET state = 'ok', event_seq = 4 WHERE group_id IS NULL")
	if n := due(); n != 1 {
		t.Fatalf("ok after a mail: %d due", n)
	}
	var keys []string
	rows, err := conn.Query(ctx, "SELECT key FROM jobs WHERE kind = 'notify_mail' ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k)
	}
	want := "storage:instance:3:" + itoa(admin.ID) + " storage:instance:4:" + itoa(admin.ID)
	if strings.Join(keys, " ") != want || strings.Join(seen, " ") != "warn ok" {
		t.Fatalf("jobs %v (states %v), want %s", keys, seen, want)
	}
	// A due row whose event was enqueued already enqueues nothing but
	// still records its mail state, so it does not stay due: a batch of
	// such rows would make the notifier's mail pass loop.
	set("UPDATE storage_states SET state = 'full', event_seq = 5, mail_enqueued_event = 'storage:instance:5' WHERE group_id IS NULL")
	if n := due(); n != 1 {
		t.Fatalf("enqueued event: %d due", n)
	}
	if n := due(); n != 0 {
		t.Fatalf("enqueued event stays due: %d", n)
	}
	var jobs int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM jobs WHERE kind = 'notify_mail'").Scan(&jobs); err != nil || jobs != 2 {
		t.Fatalf("%d jobs after an enqueued event: %v", jobs, err)
	}
}
