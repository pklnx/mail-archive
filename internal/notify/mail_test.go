package notify_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"mime"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/keepia/archive/internal/alertmail"
	"github.com/keepia/archive/internal/alertmail/mailtest"
	"github.com/keepia/archive/internal/jobs"
	"github.com/keepia/archive/internal/notify"
	"github.com/keepia/archive/internal/store"
	"github.com/keepia/archive/internal/testwait"
)

// Alert mail (#397, part 5): the notifier's mail pass enqueues one
// notify_mail job per admin with a confirmed address, once per transition;
// the job pool delivers them through an in-process relay.

// admin creates an admin with a confirmed notification address.
func (e *env) admin(name, email string) *store.User {
	e.t.Helper()
	var group int64
	if err := e.conn.QueryRow(e.ctx, "SELECT min(id) FROM customer_groups").Scan(&group); err != nil {
		e.t.Fatal(err)
	}
	u, err := e.store.CreateUserInGroup(e.ctx, store.NewUser{Name: name, PasswordHash: "h", Admin: true, GroupID: group})
	if err != nil {
		e.t.Fatal(err)
	}
	e.confirm(u.ID, email)
	return u
}

// confirm sets and confirms a user's address.
func (e *env) confirm(userID int64, email string) {
	e.t.Helper()
	now := time.Now()
	if err := e.store.SetNotifyEmail(e.ctx, userID, store.NotifyCodeRequest{Email: email, CodeHash: store.NotifyCodeHash(userID, "ABCDEF"), Now: now}); err != nil {
		e.t.Fatal(err)
	}
	if err := e.store.ConfirmNotifyEmail(e.ctx, userID, store.NotifyCodeHash(userID, "ABCDEF"), now); err != nil {
		e.t.Fatal(err)
	}
}

// mailNotifier is a notifier with alert mail and the webhook w (nil for
// none).
func (e *env) mailNotifier(w *notify.Webhook) *notify.Notifier {
	n := e.notifier(w)
	n.Mail = true
	return n
}

// sendMail runs the due notify_mail jobs through relay until none is due.
func (e *env) sendMail(relay *mailtest.Server) {
	e.t.Helper()
	svc := &alertmail.Service{
		Store: e.store, Mailer: relay.Sender(), From: "Mail Archive <archive@relay.test>", PublicURL: "https://archive.example.com",
		Logger: slog.New(slog.NewTextHandler(e.logs, nil)),
	}
	kinds := &jobs.Registry{}
	if err := kinds.Register(svc.Kind()); err != nil {
		e.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(e.ctx, testwait.Limit(e.t))
	defer cancel()
	if err := (&jobs.Pool{Store: e.store, Kinds: kinds, Workers: 1, UntilIdle: true, Logger: slog.New(slog.NewTextHandler(e.logs, nil))}).Run(ctx); err != nil {
		e.t.Fatal(err)
	}
}

// due makes every deferred announcement due now, as after its retry
// delay.
func (e *env) due() {
	e.t.Helper()
	for _, table := range []string{"account_sync_health", "smtp_fill_health", "loss_alerts", "storage_states"} {
		e.exec("UPDATE " + table + " SET notify_next_at = now() WHERE notify_next_at IS NOT NULL")
	}
}

// mailJobs returns the state and last_error of every notify_mail job by key.
func (e *env) mailJobs() map[string][2]string {
	e.t.Helper()
	rows, err := e.conn.Query(e.ctx, "SELECT key, state, coalesce(last_error, '') FROM jobs WHERE kind = 'notify_mail'")
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string][2]string{}
	for rows.Next() {
		var k, s, le string
		if err := rows.Scan(&k, &s, &le); err != nil {
			e.t.Fatal(err)
		}
		out[k] = [2]string{s, le}
	}
	return out
}

// decoded is a received mail's recipient, subject and body.
type decoded struct {
	to, subject, body, header string
}

func decode(t *testing.T, m mailtest.Mail) decoded {
	t.Helper()
	msg, err := mail.ReadMessage(bytes.NewReader(m.Data))
	if err != nil {
		t.Fatal(err)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(quotedprintable.NewReader(msg.Body))
	if err != nil {
		t.Fatal(err)
	}
	head, _, _ := strings.Cut(string(m.Data), "\r\n\r\n")
	return decoded{to: strings.Join(m.To, ","), subject: subject, body: string(body), header: head}
}

func decodeAll(t *testing.T, relay *mailtest.Server) []decoded {
	t.Helper()
	var out []decoded
	for _, m := range relay.Mails() {
		out = append(out, decode(t, m))
	}
	return out
}

// TestMailOncePerTransition: a webhook that fails three times gives one
// mail per recipient, not one per claim; failing, recovered and failing
// again give three; members, locked admins and admins without a confirmed
// address get none.
func TestMailOncePerTransition(t *testing.T) {
	e := newEnv(t)
	e.admin("boss", "boss@example.com")
	e.admin("second", "second@example.com")
	locked := e.admin("gone", "gone@example.com")
	if err := e.store.SetUserLocked(e.ctx, locked.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.CreateUser(e.ctx, "plain", "h", true); err != nil {
		t.Fatal(err)
	}
	acc := e.addAccount("büro", "u", "p")
	rcv := newReceiver(t, 500, 500, 500)
	n := e.mailNotifier(idsWebhook(t, rcv.srv.URL, ""))
	relay := mailtest.Start(t, mailtest.StartTLS)

	e.fail(acc, 3)
	// The streak began an hour ago: event IDs count in seconds, and the
	// next streak below must start in another second, as a real one does.
	e.exec("UPDATE account_sync_health SET failing_since = failing_since - interval '1 hour'")
	for range 4 {
		e.deliver(n)
		e.due()
	}
	if got := rcv.got(); len(got) != 4 {
		t.Fatalf("%d webhook requests, want 4 (three failed)", len(got))
	}
	if got := e.mailJobs(); len(got) != 2 {
		t.Fatalf("mail jobs after four claims: %v", got)
	}
	e.sendMail(relay)
	got := decodeAll(t, relay)
	if len(got) != 2 || got[0].to == got[1].to || !strings.Contains(got[0].subject, `syncs of account "büro" keep failing`) {
		t.Fatalf("mails %+v", got)
	}
	// Another claim of the delivered event sends nothing more.
	e.exec("UPDATE account_sync_health SET notified_state = 'ok'")
	e.deliver(n)
	e.sendMail(relay)
	if len(relay.Mails()) != 2 {
		t.Fatalf("%d mails after a repeated claim", len(relay.Mails()))
	}

	e.succeed(acc)
	e.deliver(n)
	e.fail(acc, 3)
	e.deliver(n)
	e.sendMail(relay)
	got = decodeAll(t, relay)
	if len(got) != 6 {
		t.Fatalf("failing, recovered, failing: %d mails, want 6; jobs %v", len(got), e.mailJobs())
	}
	var recovered int
	for _, m := range got {
		if strings.Contains(m.subject, "works again") {
			recovered++
		}
		if strings.Contains(m.to, "gone@") {
			t.Fatalf("a locked admin got mail")
		}
	}
	if recovered != 2 {
		t.Fatalf("%d recovery mails", recovered)
	}
}

// TestMailWithoutWebhook: without a webhook the claim is finished at once,
// after the mail is enqueued.
func TestMailWithoutWebhook(t *testing.T) {
	e := newEnv(t)
	e.admin("boss", "boss@example.com")
	relay := mailtest.Start(t, mailtest.Implicit)
	n := e.mailNotifier(nil)
	e.exec("UPDATE storage_counter_base SET bytes = 2000")
	n.StorageQuota = 1000
	e.transition(0, "full")
	e.deliver(n)
	var notified int64
	if err := e.conn.QueryRow(e.ctx, "SELECT notified_seq FROM storage_states WHERE group_id IS NULL").Scan(&notified); err != nil || notified != 1 {
		t.Fatalf("notified_seq %d %v", notified, err)
	}
	e.sendMail(relay)
	got := decodeAll(t, relay)
	if len(got) != 1 || got[0].subject != "Mail archive: storage full" || !strings.Contains(got[0].body, "1000 B") {
		t.Fatalf("mails %+v", got)
	}
}

// TestWebhookAndMailIndependent: a failing relay does not hold the webhook
// back, and a failing webhook does not hold the mail back.
func TestWebhookAndMailIndependent(t *testing.T) {
	e := newEnv(t)
	boss := e.admin("boss", "boss@example.com")
	acc := e.addAccount("büro", "u", "p")
	relay := mailtest.Start(t, mailtest.StartTLS)
	relay.On("rcpt", func(string) error { return mailtest.Error(451, 4, 3, 0, "try later") })
	rcv := newReceiver(t)
	n := e.mailNotifier(idsWebhook(t, rcv.srv.URL, ""))
	e.fail(acc, 3)
	e.deliver(n)
	e.sendMail(relay)
	if len(rcv.got()) != 1 || len(relay.Mails()) != 0 {
		t.Fatalf("relay failing: %d webhook requests, %d mails", len(rcv.got()), len(relay.Mails()))
	}
	key := notify.EventID(acc.ID, failingSince(e, acc.ID), notify.EventFailing) + ":" + itoa(boss.ID)
	if j := e.mailJobs()[key]; j[0] != store.JobQueued || j[1] != "smtp 451 4.3.0" {
		t.Fatalf("job %s: %v (%v)", key, j, e.mailJobs())
	}

	// The webhook now fails; the next transition's mail goes out anyway.
	e2 := newEnv(t)
	e2.admin("boss", "boss@example.com")
	acc2 := e2.addAccount("büro", "u", "p")
	relay2 := mailtest.Start(t, mailtest.StartTLS)
	rcv2 := newReceiver(t, http.StatusServiceUnavailable)
	n2 := e2.mailNotifier(idsWebhook(t, rcv2.srv.URL, ""))
	e2.fail(acc2, 3)
	e2.deliver(n2)
	e2.sendMail(relay2)
	if len(rcv2.got()) != 1 || len(relay2.Mails()) != 1 {
		t.Fatalf("webhook failing: %d webhook requests, %d mails", len(rcv2.got()), len(relay2.Mails()))
	}
}

func failingSince(e *env, accountID int64) *time.Time {
	e.t.Helper()
	var since *time.Time
	if err := e.conn.QueryRow(e.ctx, "SELECT failing_since FROM account_sync_health WHERE account_id = $1", accountID).Scan(&since); err != nil {
		e.t.Fatal(err)
	}
	return since
}

// TestStorageMailGroup: warn, ok and warn again of a group give three
// mails, each naming the group.
func TestStorageMailGroup(t *testing.T) {
	e := newEnv(t)
	e.admin("boss", "boss@example.com")
	g := e.group("Müller GmbH")
	e.exec("UPDATE customer_groups SET quota_bytes = 1000 WHERE id = $1", g)
	relay := mailtest.Start(t, mailtest.StartTLS)
	n := e.mailNotifier(nil)
	for _, state := range []string{"warn", "ok", "warn"} {
		e.transition(g, state)
		e.deliver(n)
	}
	e.sendMail(relay)
	got := decodeAll(t, relay)
	if len(got) != 3 {
		t.Fatalf("%d mails, want 3", len(got))
	}
	for i, m := range got {
		if !strings.Contains(m.subject, `group "Müller GmbH"`) || !strings.Contains(m.body, "Customer group: Müller GmbH") ||
			!strings.Contains(m.header, "Auto-Submitted: auto-generated") {
			t.Errorf("mail %d: %+v", i, m)
		}
	}
}

// TestMailLeak: of the leak markers, alert mail shows only the names of
// accounts, owners and groups: no error text, folder, IMAP user, host,
// subject or address from mail, for every kind of event. The jobs' payloads
// hold no marker at all, and the logs no address.
func TestMailLeak(t *testing.T) {
	e := leakEnv(t)
	e.admin("boss", "boss@example.com")
	relay := mailtest.Start(t, mailtest.StartTLS)
	n := e.mailNotifier(idsWebhook(t, newReceiver(t).srv.URL, ""))
	e.deliver(n)

	var payloads string
	if err := e.conn.QueryRow(e.ctx, "SELECT string_agg(payload::text, ' ') FROM jobs WHERE kind = 'notify_mail'").Scan(&payloads); err != nil {
		t.Fatal(err)
	}
	for _, m := range leakMarkers {
		if strings.Contains(payloads, m) {
			t.Errorf("payload holds %q: %s", m, payloads)
		}
	}
	e.sendMail(relay)
	got := decodeAll(t, relay)
	if len(got) != 4 {
		t.Fatalf("%d mails, want 4 (sync, fill, loss, storage)", len(got))
	}
	allowed := map[string]bool{"zqaccountzq": true, "zqownerzq": true, "zqfillaccountzq": true, "zqgroupzq": true}
	seen := map[string]bool{}
	for _, m := range got {
		all := m.header + "\n" + m.subject + "\n" + m.body
		for _, mk := range leakMarkers {
			if strings.Contains(all, mk) {
				seen[mk] = true
				if !allowed[mk] {
					t.Errorf("mail %q shows %q", m.subject, mk)
				}
			}
		}
		if strings.Contains(m.subject, "storage") && !strings.Contains(all, "zqgroupzq") {
			t.Errorf("storage mail without the group name: %+v", m)
		}
	}
	for mk := range allowed {
		if !seen[mk] {
			t.Errorf("no mail names %q", mk)
		}
	}
	if logs := e.logs.String(); strings.Contains(logs, "boss@example.com") || strings.Contains(logs, "zqerrorzq") {
		t.Errorf("logs show an address or error text:\n%s", logs)
	}
}

// TestMailRecipientCheckedAtDelivery: between the claim and the delivery,
// a demoted, locked or removed admin and one whose new address is not
// confirmed get nothing (done, recipient_gone); a new confirmed address
// gets the mail.
func TestMailRecipientCheckedAtDelivery(t *testing.T) {
	e := newEnv(t)
	e.admin("keeper", "keeper@example.com") // keeps an admin, so the others may change
	demoted := e.admin("demoted", "demoted@example.com")
	locked := e.admin("locked", "locked@example.com")
	removed := e.admin("removed", "removed@example.com")
	moved := e.admin("moved", "old@example.com")
	reconfirmed := e.admin("reconfirmed", "before@example.com")
	relay := mailtest.Start(t, mailtest.StartTLS)
	n := e.mailNotifier(nil)
	e.transition(0, "warn")
	e.deliver(n)
	if got := e.mailJobs(); len(got) != 6 {
		t.Fatalf("%d jobs", len(got))
	}
	if err := e.store.SetUserAdmin(e.ctx, demoted.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := e.store.SetUserLocked(e.ctx, locked.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := e.store.DeleteUser(e.ctx, removed.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.store.SetNotifyEmail(e.ctx, moved.ID, store.NotifyCodeRequest{Email: "new@example.com", CodeHash: store.NotifyCodeHash(moved.ID, "X"), Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
	e.confirm(reconfirmed.ID, "after@example.com")
	e.sendMail(relay)
	var to []string
	for _, m := range relay.Mails() {
		to = append(to, m.To...)
	}
	if strings.Join(to, ",") != "keeper@example.com,after@example.com" && strings.Join(to, ",") != "after@example.com,keeper@example.com" {
		t.Fatalf("mail went to %v", to)
	}
	gone := 0
	for k, j := range e.mailJobs() {
		if j[0] != store.JobDone {
			t.Errorf("job %s %v", k, j)
		}
		if j[1] == alertmail.CodeRecipientGone {
			gone++
		}
	}
	if gone != 4 {
		t.Fatalf("%d jobs recipient_gone, want 4: %v", gone, e.mailJobs())
	}
}

// TestMailReplies: 5.1.1 at RCPT unconfirms the address and fails the job;
// a relay that answers 5.7.1 to everything fails the job and keeps the
// address confirmed; the logs carry codes, never the reply text.
func TestMailReplies(t *testing.T) {
	for _, c := range []struct {
		reply       error
		phase       string
		unconfirmed bool
		code        string
	}{
		{mailtest.Error(550, 5, 1, 1, "zqreplyzq no such user"), "rcpt", true, "smtp 550 5.1.1"},
		{mailtest.Error(554, 5, 7, 1, "zqreplyzq relay denied"), "mail", false, "smtp 554 5.7.1"},
		{mailtest.Error(554, 5, 7, 1, "zqreplyzq relay denied"), "rcpt", false, "smtp 554 5.7.1"},
	} {
		e := newEnv(t)
		boss := e.admin("boss", "boss@example.com")
		relay := mailtest.Start(t, mailtest.StartTLS)
		relay.On(c.phase, func(string) error { return c.reply })
		n := e.mailNotifier(nil)
		e.transition(0, "warn")
		e.deliver(n)
		e.sendMail(relay)
		j := e.mailJobs()["storage:instance:1:"+itoa(boss.ID)]
		if j[0] != store.JobFailed || j[1] != c.code {
			t.Errorf("%s %s: job %v", c.phase, c.code, j)
		}
		st, err := e.store.NotifyEmail(e.ctx, boss.ID)
		if err != nil || (st.ConfirmedAt == nil) != c.unconfirmed || (st.BouncedAt != nil) != c.unconfirmed || st.Email != "boss@example.com" {
			t.Errorf("%s %s: address %+v %v", c.phase, c.code, st, err)
		}
		if logs := e.logs.String(); strings.Contains(logs, "zqreplyzq") || strings.Contains(logs, "boss@example.com") || !strings.Contains(logs, strings.TrimPrefix(c.code, "smtp ")) {
			t.Errorf("%s %s: logs\n%s", c.phase, c.code, logs)
		}
	}
}

// brokenWebhooks are the webhook cases of the independence tests: none,
// one that answers 503 to every request (retried with a growing delay),
// and one that answers 401 (a configuration error: an hour between tries).
var brokenWebhooks = []struct {
	name   string
	status int
}{{"no webhook", 0}, {"503", http.StatusServiceUnavailable}, {"401", http.StatusUnauthorized}}

// brokenWebhook returns a webhook that answers status to every request,
// or nil for status 0.
func brokenWebhook(t *testing.T, status int) (*notify.Webhook, *receiver) {
	t.Helper()
	if status == 0 {
		return nil, nil
	}
	statuses := make([]int, 100)
	for i := range statuses {
		statuses[i] = status
	}
	rcv := newReceiver(t, statuses...)
	return idsWebhook(t, rcv.srv.URL, ""), rcv
}

// subjects returns the subjects of the relay's mails.
func subjects(t *testing.T, relay *mailtest.Server) []string {
	t.Helper()
	var out []string
	for _, m := range decodeAll(t, relay) {
		out = append(out, m.subject)
	}
	return out
}

// recurringFill creates an smtp account with a recurring fill and returns
// a function that records one finished run with status.
func (e *env) recurringFill() func(status string) {
	e.t.Helper()
	acc := &store.Account{Name: "journal", Enabled: true}
	if err := e.store.CreateSMTPAccount(e.ctx, acc, []string{"archive@archive.test"}); err != nil {
		e.t.Fatal(err)
	}
	fill := &store.Fill{AccountID: acc.ID, Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "ak@example.com", KeepRunning: true}
	if err := e.store.CreateFill(e.ctx, fill, func(int64) ([]byte, error) { return []byte("x"), nil }); err != nil {
		e.t.Fatal(err)
	}
	runs := 0
	return func(status string) {
		e.t.Helper()
		if runs++; runs > 1 {
			if err := e.store.StartFillRun(e.ctx, fill.ID, time.Now()); err != nil {
				e.t.Fatal(err)
			}
		}
		if err := e.store.FinishFill(e.ctx, fill.ID, status, map[string]string{store.FillFailed: "LOGIN failed"}[status]); err != nil {
			e.t.Fatal(err)
		}
	}
}

// TestRecoveryMailWhileWebhookFails: an account and a recurring fill that
// fail and recover while the webhook keeps failing (503, or 401 for good)
// get a failing and a recovery mail each, as without a webhook; the
// webhook's own state stays at its first, undelivered alert. A later
// Deliver sends nothing more.
func TestRecoveryMailWhileWebhookFails(t *testing.T) {
	for _, c := range brokenWebhooks {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.admin("boss", "boss@example.com")
			relay := mailtest.Start(t, mailtest.StartTLS)
			w, rcv := brokenWebhook(t, c.status)
			n := e.mailNotifier(w)
			acc := e.addAccount("büro", "u", "p")
			finish := e.recurringFill()

			e.fail(acc, 3)
			for range 3 {
				finish(store.FillFailed)
			}
			e.deliver(n)
			e.succeed(acc)
			finish(store.FillOK)
			e.deliver(n)
			e.deliver(n)
			e.sendMail(relay)

			got := subjects(t, relay)
			want := map[string]bool{
				`Mail archive: syncs of account "büro" keep failing`:                true,
				`Mail archive: account "büro" works again`:                          true,
				`Mail archive: recurring imports of account "journal" keep failing`: true,
				`Mail archive: account "journal" works again`:                       true,
			}
			if len(got) != len(want) {
				t.Fatalf("mails %q, want %d; jobs %v", got, len(want), e.mailJobs())
			}
			for _, s := range got {
				if !want[s] {
					t.Errorf("unexpected mail %q of %q", s, got)
				}
				delete(want, s)
			}
			if rcv != nil {
				// The webhook was tried and never delivered: its state is
				// still the alert it could not send.
				if len(rcv.got()) == 0 {
					t.Fatal("the webhook was never tried")
				}
				var notified string
				if err := e.conn.QueryRow(e.ctx, "SELECT notified_state FROM account_sync_health WHERE account_id = $1", acc.ID).Scan(&notified); err != nil || notified != store.NotifiedOK {
					t.Fatalf("webhook state %q %v", notified, err)
				}
			}
			e.deliver(n)
			e.sendMail(relay)
			if len(relay.Mails()) != 4 {
				t.Fatalf("%d mails after another Deliver", len(relay.Mails()))
			}
			// Nothing stays due as mail.
			none := func(p store.PendingNotification) *store.AlertMailEvent {
				t.Errorf("still due as mail: %+v", p)
				return nil
			}
			if k, err := e.store.EnqueueDueSyncMail(e.ctx, 3, 10, none); err != nil || k != 0 {
				t.Fatalf("syncs due as mail: %d %v", k, err)
			}
			if k, err := e.store.EnqueueDueFillMail(e.ctx, 3, 10, none); err != nil || k != 0 {
				t.Fatalf("fills due as mail: %d %v", k, err)
			}
		})
	}
}

// TestLossMailWhileWebhookFails: a loss alert is mailed in the Deliver
// that finds it, also when the webhook's request for another account's
// alert fails first and the webhook claims no loss alert.
func TestLossMailWhileWebhookFails(t *testing.T) {
	for _, c := range brokenWebhooks {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.admin("boss", "boss@example.com")
			relay := mailtest.Start(t, mailtest.StartTLS)
			w, _ := brokenWebhook(t, c.status)
			n := e.mailNotifier(w)
			failing := e.addAccount("büro", "u", "p")
			losing := e.addAccount("archiv", "u", "p")
			e.fail(failing, 3)
			e.lose(losing, 200, 1000)
			e.deliver(n)
			e.sendMail(relay)
			got := subjects(t, relay)
			if len(got) != 2 || !strings.Contains(strings.Join(got, "\n"), `200 messages deleted on the server (account "archiv")`) {
				t.Fatalf("mails %q, want the sync and the loss mail", got)
			}
			e.deliver(n)
			e.sendMail(relay)
			if len(relay.Mails()) != 2 {
				t.Fatalf("%d mails after another Deliver", len(relay.Mails()))
			}
		})
	}
}

// TestStorageMailDuringWebhookBackoff: a storage transition while the
// webhook waits for its retry (503: 30 s, 401: an hour) is mailed at
// once, as without a webhook.
func TestStorageMailDuringWebhookBackoff(t *testing.T) {
	for _, c := range brokenWebhooks {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.admin("boss", "boss@example.com")
			g := e.group("Müller GmbH")
			e.exec("UPDATE customer_groups SET quota_bytes = 1000 WHERE id = $1", g)
			relay := mailtest.Start(t, mailtest.StartTLS)
			w, rcv := brokenWebhook(t, c.status)
			n := e.mailNotifier(w)

			e.transition(g, "warn")
			e.deliver(n)
			if rcv != nil {
				var waiting bool
				if err := e.conn.QueryRow(e.ctx, "SELECT notify_next_at > now() FROM storage_states WHERE group_id = $1", g).Scan(&waiting); err != nil || !waiting {
					t.Fatalf("the webhook does not wait for its retry: %v %v", waiting, err)
				}
			}
			e.transition(g, "full")
			e.deliver(n)
			e.sendMail(relay)
			got := subjects(t, relay)
			if len(got) != 2 || !strings.Contains(got[1], "full") || got[0] == got[1] {
				t.Fatalf("mails %q, want the warn and the full mail; jobs %v", got, e.mailJobs())
			}
			if rcv != nil && len(rcv.got()) != 1 {
				t.Fatalf("%d webhook requests, want 1 (the retry waits)", len(rcv.got()))
			}
		})
	}
}

// TestNoMailForShortFailureAfterRecovery: the webhook delivered the
// failing alert, mail sent the failing and the recovery mail, and the
// webhook now answers 401 (or 503) to the recovery. A short failure below
// the threshold starts a new streak and clears the webhook's retry time,
// so the webhook claims a recovery of the new streak at once: mail sends
// nothing for it, for syncs and for recurring fills.
func TestNoMailForShortFailureAfterRecovery(t *testing.T) {
	for _, kind := range []string{"sync", "fill"} {
		for _, status := range []int{http.StatusUnauthorized, http.StatusServiceUnavailable} {
			t.Run(kind+" "+strconv.Itoa(status), func(t *testing.T) {
				e := newEnv(t)
				e.admin("boss", "boss@example.com")
				relay := mailtest.Start(t, mailtest.StartTLS)
				statuses := []int{http.StatusOK}
				for range 20 {
					statuses = append(statuses, status)
				}
				rcv := newReceiver(t, statuses...)
				n := e.mailNotifier(idsWebhook(t, rcv.srv.URL, ""))
				var fail, ok func()
				table := "account_sync_health"
				if kind == "sync" {
					acc := e.addAccount("büro", "u", "p")
					fail, ok = func() { e.fail(acc, 1) }, func() { e.succeed(acc) }
				} else {
					finish := e.recurringFill()
					fail, ok = func() { finish(store.FillFailed) }, func() { finish(store.FillOK) }
					table = "smtp_fill_health"
				}
				for range 3 {
					fail()
				}
				// The next streak must start in another second: event IDs
				// count in seconds.
				e.exec("UPDATE " + table + " SET failing_since = failing_since - interval '1 hour'")
				e.deliver(n)
				ok()
				e.deliver(n)
				e.sendMail(relay)
				if got := subjects(t, relay); len(got) != 2 {
					t.Fatalf("failing and recovered: mails %q", got)
				}
				var notified string
				if err := e.conn.QueryRow(e.ctx, "SELECT notified_state FROM "+table).Scan(&notified); err != nil || notified != store.NotifiedFailing {
					t.Fatalf("the webhook delivered the alert but not the recovery: %q %v", notified, err)
				}

				fail()
				ok()
				before := len(rcv.got())
				e.deliver(n)
				if len(rcv.got()) != before+1 {
					t.Fatalf("the webhook did not claim the recovery of the short streak at once: %d requests, want %d", len(rcv.got()), before+1)
				}
				e.due()
				e.deliver(n)
				e.sendMail(relay)
				if got := subjects(t, relay); len(got) != 2 {
					t.Fatalf("mails after a short failure %q, want only the first 2; jobs %v", got, e.mailJobs())
				}
			})
		}
	}
}

// TestNoMailForUnseenStorageChange: warn and back to ok before any pass
// sees it sends nothing, with and without a webhook; the webhook still
// announces the state it claims.
func TestNoMailForUnseenStorageChange(t *testing.T) {
	for _, withWebhook := range []bool{false, true} {
		t.Run(map[bool]string{false: "no webhook", true: "webhook"}[withWebhook], func(t *testing.T) {
			e := newEnv(t)
			e.admin("boss", "boss@example.com")
			g := e.group("Müller GmbH")
			relay := mailtest.Start(t, mailtest.StartTLS)
			var w *notify.Webhook
			var rcv *receiver
			if withWebhook {
				rcv = newReceiver(t)
				w = idsWebhook(t, rcv.srv.URL, "")
			}
			n := e.mailNotifier(w)
			e.transition(g, "warn")
			e.transition(g, "ok")
			e.deliver(n)
			e.deliver(n)
			e.sendMail(relay)
			if got := subjects(t, relay); len(got) != 0 {
				t.Fatalf("mails %q for a change no pass saw", got)
			}
			if rcv != nil && len(rcv.got()) != 1 {
				t.Fatalf("%d webhook requests, want 1", len(rcv.got()))
			}
		})
	}
}

// TestMailOncePerEvent: what keeps an event from being mailed twice is the
// row's mail state, which the enqueue moves on in the same transaction:
// mailed_state for syncs and fills, mailed_seq for storage, and
// mail_enqueued_event for loss alerts. The jobs table does not decide it:
// after the done jobs are purged and a second admin confirmed an address,
// another round mails nothing.
func TestMailOncePerEvent(t *testing.T) {
	e := newEnv(t)
	e.admin("boss", "boss@example.com")
	relay := mailtest.Start(t, mailtest.StartTLS)
	n := e.mailNotifier(nil)
	acc := e.addAccount("büro", "u", "p")
	finish := e.recurringFill()
	e.fail(acc, 3)
	for range 3 {
		finish(store.FillFailed)
	}
	e.lose(e.addAccount("archiv", "u", "p"), 200, 1000)
	e.transition(0, "warn")
	e.deliver(n)
	if got := e.mailJobs(); len(got) != 4 {
		t.Fatalf("jobs %v, want sync, fill, loss and storage", got)
	}
	e.sendMail(relay)

	e.admin("second", "second@example.com")
	e.exec("UPDATE jobs SET finished_at = now() - interval '2 hours' WHERE kind = 'notify_mail'")
	if purged, err := e.store.DeleteDoneJobs(e.ctx, time.Hour); err != nil || purged != 4 {
		t.Fatalf("purged %d: %v", purged, err)
	}
	e.deliver(n)
	if got := e.mailJobs(); len(got) != 0 {
		t.Fatalf("the same events enqueued again: %v", got)
	}
}

// TestMailTurnedOn: the first round with mail announces the current state
// once: a failing account, a group still at warn and a loss the webhook
// already sent, but not a group back at ok. While no admin has a confirmed
// address, rounds record the state and send nothing, so the first admin
// to confirm gets only later changes.
func TestMailTurnedOn(t *testing.T) {
	t.Run("current state", func(t *testing.T) {
		e := newEnv(t)
		e.admin("boss", "boss@example.com")
		rcv := newReceiver(t)
		w := idsWebhook(t, rcv.srv.URL, "")
		before := e.notifier(w)
		acc := e.addAccount("büro", "u", "p")
		e.fail(acc, 3)
		e.lose(e.addAccount("archiv", "u", "p"), 200, 1000)
		warn, back := e.group("Müller GmbH"), e.group("Lindqvist Design")
		e.transition(warn, "warn")
		e.transition(back, "warn")
		e.deliver(before)
		e.transition(back, "ok")
		e.deliver(before)
		if len(rcv.got()) == 0 || len(e.mailJobs()) != 0 {
			t.Fatalf("before mail: %d webhook requests, jobs %v", len(rcv.got()), e.mailJobs())
		}

		e.deliver(e.mailNotifier(w))
		got := e.mailJobs()
		want := []string{
			notify.EventID(acc.ID, failingSince(e, acc.ID), notify.EventFailing),
			"storage:" + itoa(warn) + ":1",
		}
		if len(got) != 3 {
			t.Fatalf("jobs %v, want the failing account, the loss and the group at warn", got)
		}
		gone := 0
		for k := range got {
			if strings.HasPrefix(k, "gone-") {
				gone++
			}
		}
		for _, w := range want {
			if _, ok := got[w+":"+itoa(e.userID("boss"))]; !ok {
				t.Errorf("no job for %s: %v", w, got)
			}
		}
		if gone != 1 {
			t.Errorf("%d loss jobs: %v", gone, got)
		}
	})
	t.Run("no address yet", func(t *testing.T) {
		e := newEnv(t)
		n := e.mailNotifier(nil)
		acc := e.addAccount("büro", "u", "p")
		g := e.group("Müller GmbH")
		e.fail(acc, 3)
		e.transition(g, "warn")
		e.deliver(n)
		e.admin("boss", "boss@example.com")
		e.deliver(n)
		if got := e.mailJobs(); len(got) != 0 {
			t.Fatalf("jobs %v for changes before the first address", got)
		}
		e.transition(g, "full")
		e.deliver(n)
		if got := e.mailJobs(); len(got) != 1 {
			t.Fatalf("jobs %v, want the change after the address", got)
		}
	})
}

// userID returns the ID of the user name.
func (e *env) userID(name string) int64 {
	e.t.Helper()
	var id int64
	if err := e.conn.QueryRow(e.ctx, "SELECT id FROM users WHERE name = $1", name).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}
