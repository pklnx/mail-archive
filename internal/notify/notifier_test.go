package notify_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/imaptest"
	"github.com/pklnx/mail-archive/internal/notify"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// receiver records webhook requests and answers with the given statuses
// in turn (200 once they run out).
type receiver struct {
	t        *testing.T
	mu       sync.Mutex
	statuses []int
	requests []received
	srv      *httptest.Server
}

type received struct {
	header http.Header
	body   []byte
	json   map[string]any
}

func newReceiver(t *testing.T, statuses ...int) *receiver {
	r := &receiver{t: t, statuses: statuses}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		rec := received{header: req.Header.Clone(), body: body}
		_ = json.Unmarshal(body, &rec.json)
		r.mu.Lock()
		r.requests = append(r.requests, rec)
		status := http.StatusOK
		if len(r.statuses) > 0 {
			status, r.statuses = r.statuses[0], r.statuses[1:]
		}
		r.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) got() []received {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]received(nil), r.requests...)
}

func webhook(t *testing.T, rawURL, format, auth string) *notify.Webhook {
	t.Helper()
	cfg, err := config.ParseWebhook(rawURL, format, auth)
	if err != nil {
		t.Fatal(err)
	}
	return notify.NewWebhook(cfg)
}

type env struct {
	t      *testing.T
	ctx    context.Context
	store  *store.Store
	conn   *pgx.Conn
	sealer *crypto.Sealer
	syncer *archive.Syncer
	host   string
	port   int
	logs   *bytes.Buffer
}

func newEnv(t *testing.T, users ...*imapmemserver.User) *env {
	st, dbURL := storetest.NewWithURL(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	blobs, err := blobstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keyStr, _ := crypto.GenerateKey()
	key, _ := crypto.ParseKey(keyStr)
	sealer, err := crypto.NewSealer(key)
	if err != nil {
		t.Fatal(err)
	}
	host, port := imaptest.Start(t, users...)
	return &env{
		t: t, ctx: ctx, store: st, conn: conn, sealer: sealer, host: host, port: port, logs: &bytes.Buffer{},
		syncer: &archive.Syncer{Store: st, Blobs: blobs, Sealer: sealer, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
	}
}

func (e *env) addAccount(name, user, password string) *store.Account {
	e.t.Helper()
	acc := &store.Account{Name: name, Host: e.host, Port: e.port, TLSMode: store.TLSModeNone, Username: user, Enabled: true}
	seal := func(id int64) ([]byte, error) { return archive.SealPassword(e.sealer, id, password) }
	if err := e.store.CreateAccountSealed(e.ctx, acc, seal); err != nil {
		e.t.Fatal(err)
	}
	return acc
}

func (e *env) setPassword(acc *store.Account, password string) {
	e.t.Helper()
	fresh, err := e.store.GetAccount(e.ctx, acc.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	enc, err := archive.SealPassword(e.sealer, acc.ID, password)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.store.UpdateAccount(e.ctx, fresh.Ref(), store.AccountChange{PasswordEnc: enc}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) sync(acc *store.Account) archive.AccountResult {
	e.t.Helper()
	fresh, err := e.store.GetAccount(e.ctx, acc.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	return e.syncer.SyncAccount(e.ctx, fresh)
}

// fail records a failed run without an IMAP server.
func (e *env) fail(acc *store.Account, times int) {
	e.t.Helper()
	for range times {
		run, err := e.store.StartSyncRun(e.ctx, acc.ID)
		if err != nil {
			e.t.Fatal(err)
		}
		run.Status, run.Error, run.Health = "failed", "LOGIN failed", store.HealthFailure
		if err := e.store.FinishSyncRun(e.ctx, run); err != nil {
			e.t.Fatal(err)
		}
	}
}

func (e *env) succeed(acc *store.Account) {
	e.t.Helper()
	run, err := e.store.StartSyncRun(e.ctx, acc.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	run.Status, run.Health = "ok", store.HealthSuccess
	if err := e.store.FinishSyncRun(e.ctx, run); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) notifier(w *notify.Webhook) *notify.Notifier {
	return &notify.Notifier{
		Store: e.store, Webhook: w, AlertAfter: 3,
		Logger: slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
}

func (e *env) deliver(n *notify.Notifier) {
	e.t.Helper()
	if err := n.Deliver(e.ctx); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.conn.Exec(e.ctx, sql, args...); err != nil {
		e.t.Fatal(err)
	}
}

func TestAlertAndRecoveryWithIMAP(t *testing.T) {
	user := imapmemserver.NewUser("alice", "right")
	imaptest.CreateMailboxes(t, user, "INBOX")
	e := newEnv(t, user)
	acc := e.addAccount("work", "alice", "wrong")
	rcv := newReceiver(t)
	n := e.notifier(webhook(t, rcv.srv.URL+"/hook", "", ""))

	for i := range 5 {
		if res := e.sync(acc); res.Err == nil {
			t.Fatalf("sync %d with the wrong password succeeded", i)
		}
		e.deliver(n)
		if want := map[bool]int{true: 0, false: 1}[i < 2]; len(rcv.got()) != want {
			t.Fatalf("after %d failures: %d messages, want %d", i+1, len(rcv.got()), want)
		}
	}
	alert := rcv.got()[0].json
	if alert["event"] != "failing" || !strings.HasPrefix(alert["id"].(string), fmt.Sprintf("%d-", acc.ID)) ||
		!strings.Contains(alert["message"].(string), `"work"`) || !strings.Contains(alert["text"].(string), "3 syncs in a row") {
		t.Fatalf("alert: %v", alert)
	}
	accounts := alert["accounts"].([]any)
	if len(accounts) != 1 || accounts[0].(map[string]any)["lastError"] == "" {
		t.Fatalf("alert accounts: %v", accounts)
	}

	e.setPassword(acc, "right")
	if res := e.sync(acc); res.Err != nil {
		t.Fatal(res.Err)
	}
	e.deliver(n)
	e.deliver(n)
	got := rcv.got()
	if len(got) != 2 || got[1].json["event"] != "recovered" {
		t.Fatalf("recovery: %d messages, last %v", len(got), got[len(got)-1].json)
	}
	if !strings.HasSuffix(got[1].json["id"].(string), "-recovered") {
		t.Fatalf("recovery id: %v", got[1].json["id"])
	}
	if !strings.Contains(e.logs.String(), "alert sent") || !strings.Contains(e.logs.String(), "recovery sent") {
		t.Fatalf("audit log: %s", e.logs)
	}
}

func TestRecoveryBeforeDeliverySendsNothing(t *testing.T) {
	e := newEnv(t)
	acc := e.addAccount("work", "u", "p")
	rcv := newReceiver(t)
	n := e.notifier(webhook(t, rcv.srv.URL, "", ""))
	e.fail(acc, 3)
	e.succeed(acc)
	e.deliver(n)
	if len(rcv.got()) != 0 {
		t.Fatalf("%d messages", len(rcv.got()))
	}
}

func TestRetryKeepsTheID(t *testing.T) {
	e := newEnv(t)
	acc := e.addAccount("work", "u", "p")
	rcv := newReceiver(t, http.StatusInternalServerError)
	n := e.notifier(webhook(t, rcv.srv.URL, "", ""))
	e.fail(acc, 3)
	e.deliver(n)
	// The retry waits; pretend its time has come.
	e.deliver(n)
	if len(rcv.got()) != 1 {
		t.Fatalf("retried too early: %d requests", len(rcv.got()))
	}
	e.exec("UPDATE account_sync_health SET notify_next_at = now() - interval '1 second'")
	e.deliver(n)
	e.deliver(n)
	got := rcv.got()
	if len(got) != 2 || got[0].json["id"] != got[1].json["id"] || got[0].header.Get("X-Mail-Archive-Id") != got[0].json["id"] {
		t.Fatalf("requests: %d, ids %v %v", len(got), got[0].json["id"], got[len(got)-1].json["id"])
	}
}

func TestPermanentErrorIsGivenUp(t *testing.T) {
	e := newEnv(t)
	acc := e.addAccount("work", "u", "p")
	rcv := newReceiver(t, http.StatusBadRequest, http.StatusBadRequest)
	n := e.notifier(webhook(t, rcv.srv.URL, "", ""))
	e.fail(acc, 3)
	e.deliver(n)
	var next time.Time
	if err := e.conn.QueryRow(e.ctx, "SELECT notify_next_at FROM account_sync_health").Scan(&next); err != nil {
		t.Fatal(err)
	}
	if d := time.Until(next); d < 59*time.Minute {
		t.Fatalf("a 400 is retried after %v, want an hour", d)
	}
	if !strings.Contains(e.logs.String(), "level=ERROR") {
		t.Fatalf("400 not logged as an error: %s", e.logs)
	}
	// A day later it is given up and not sent again.
	e.exec("UPDATE account_sync_health SET notify_next_at = now(), notify_first_error_at = now() - interval '25 hours'")
	e.deliver(n)
	e.fail(acc, 2)
	e.exec("UPDATE account_sync_health SET notify_next_at = NULL")
	e.deliver(n)
	if len(rcv.got()) != 2 {
		t.Fatalf("%d requests, want 2", len(rcv.got()))
	}
	if !strings.Contains(e.logs.String(), "notification given up") {
		t.Fatalf("give-up not logged: %s", e.logs)
	}
	// Its recovery is announced normally.
	e.succeed(acc)
	e.deliver(n)
	if got := rcv.got(); len(got) != 3 || got[2].json["event"] != "recovered" {
		t.Fatalf("recovery after give-up: %d", len(got))
	}
}

func TestBatches(t *testing.T) {
	e := newEnv(t)
	for i := range notify.BatchSize + 1 {
		e.fail(e.addAccount(fmt.Sprintf("acc%02d", i), "u", "p"), 3)
	}
	rcv := newReceiver(t)
	e.deliver(e.notifier(webhook(t, rcv.srv.URL, "", "")))
	got := rcv.got()
	if len(got) != 2 {
		t.Fatalf("%d requests, want 2", len(got))
	}
	if n := len(got[0].json["accounts"].([]any)); n != notify.BatchSize {
		t.Fatalf("first batch: %d accounts", n)
	}
	if n := len(got[1].json["accounts"].([]any)); n != 1 {
		t.Fatalf("second batch: %d accounts", n)
	}
	if !strings.HasPrefix(got[0].json["id"].(string), "batch-") || !strings.Contains(got[0].json["title"].(string), "50 accounts") {
		t.Fatalf("batch: %v %v", got[0].json["id"], got[0].json["title"])
	}
}

func TestParallelNotifiersSendOnce(t *testing.T) {
	e := newEnv(t)
	e.fail(e.addAccount("work", "u", "p"), 3)
	rcv := newReceiver(t)
	w := webhook(t, rcv.srv.URL, "", "")
	var wg sync.WaitGroup
	for range 6 {
		n := e.notifier(w)
		wg.Go(func() {
			if err := n.Deliver(e.ctx); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if len(rcv.got()) != 1 {
		t.Fatalf("%d requests, want 1", len(rcv.got()))
	}
}

func TestCrashAfterSendResendsTheSameID(t *testing.T) {
	e := newEnv(t)
	acc := e.addAccount("work", "u", "p")
	e.fail(acc, 3)
	rcv := newReceiver(t)
	w := webhook(t, rcv.srv.URL, "", "")
	// A notifier claims and sends, then dies before writing the state.
	pending, err := e.store.ClaimNotifications(e.ctx, []byte("dead"), notify.LeaseDuration, 3, notify.BatchSize)
	if err != nil || len(pending) != 1 {
		t.Fatalf("claim: %v %v", pending, err)
	}
	if _, err := w.Send(e.ctx, notify.BuildMessage(pending)); err != nil {
		t.Fatal(err)
	}
	n := e.notifier(w)
	e.deliver(n)
	if len(rcv.got()) != 1 {
		t.Fatal("sent while the lease was held")
	}
	e.exec("UPDATE account_sync_health SET notify_lease_until = now() - interval '1 second'")
	e.deliver(n)
	got := rcv.got()
	if len(got) != 2 || got[0].json["id"] != got[1].json["id"] {
		t.Fatalf("resend: %d requests", len(got))
	}
}

func TestDisabledAccountIsSkipped(t *testing.T) {
	e := newEnv(t)
	acc := e.addAccount("work", "u", "p")
	e.fail(acc, 3)
	e.exec("UPDATE accounts SET enabled = false")
	rcv := newReceiver(t)
	n := e.notifier(webhook(t, rcv.srv.URL, "", ""))
	e.deliver(n)
	if len(rcv.got()) != 0 {
		t.Fatal("disabled account announced")
	}
	// Enabled again, still failing: the alert follows.
	e.exec("UPDATE accounts SET enabled = true")
	e.deliver(n)
	if len(rcv.got()) != 1 {
		t.Fatalf("%d requests after enabling", len(rcv.got()))
	}
}

func TestLogsHideSecrets(t *testing.T) {
	e := newEnv(t)
	e.fail(e.addAccount("work", "u", "p"), 3)
	rcv := newReceiver(t, http.StatusServiceUnavailable)
	const token, path = "Bearer tk_supersecret", "/topic-secret-path"
	n := e.notifier(webhook(t, rcv.srv.URL+path+"?auth=querysecret", "ntfy", token))
	e.deliver(n)
	// A connection error too: *url.Error would print the whole URL.
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()
	e.exec("UPDATE account_sync_health SET notify_next_at = NULL")
	n.Webhook = webhook(t, closedURL+path+"?auth=querysecret", "", token)
	e.deliver(n)
	logs := e.logs.String()
	for _, secret := range []string{"tk_supersecret", "topic-secret-path", "querysecret", "LOGIN failed"} {
		if strings.Contains(logs, secret) {
			t.Errorf("log contains %q:\n%s", secret, logs)
		}
	}
	if !strings.Contains(logs, "notification failed") || !strings.Contains(logs, "127.0.0.1") {
		t.Fatalf("failure not logged with its host: %s", logs)
	}
	var stored string
	if err := e.conn.QueryRow(e.ctx, "SELECT notify_error FROM account_sync_health").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "querysecret") || strings.Contains(stored, "topic-secret") {
		t.Fatalf("stored error contains the URL: %s", stored)
	}
}

func TestNtfyFormat(t *testing.T) {
	e := newEnv(t)
	e.fail(e.addAccount("Büro", "u", "p"), 3)
	rcv := newReceiver(t)
	e.deliver(e.notifier(webhook(t, rcv.srv.URL, "ntfy", "Bearer tk_x")))
	got := rcv.got()
	if len(got) != 1 {
		t.Fatalf("%d requests", len(got))
	}
	h := got[0].header
	if h.Get("Authorization") != "Bearer tk_x" || h.Get("Priority") != "high" || h.Get("Tags") != "warning" ||
		!strings.HasPrefix(h.Get("Content-Type"), "text/plain") || !strings.HasPrefix(h.Get("Title"), "=?UTF-8?") {
		t.Fatalf("headers: %v", h)
	}
	if !strings.Contains(string(got[0].body), "Büro") || got[0].json != nil {
		t.Fatalf("body: %s", got[0].body)
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	var leaked sync.Map
	other := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		leaked.Store("hit", r.Header.Get("Authorization"))
	}))
	defer other.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer first.Close()
	w := webhook(t, first.URL, "", "Bearer secret")
	status, err := w.Send(context.Background(), notify.TestMessage(time.Now()))
	var de *notify.DeliveryError
	if status != http.StatusTemporaryRedirect || err == nil || !errors.As(err, &de) || !de.Permanent() {
		t.Fatalf("redirect: %d %v", status, err)
	}
	if _, hit := leaked.Load("hit"); hit {
		t.Fatal("redirect followed")
	}
}

func TestRetryDelay(t *testing.T) {
	for _, c := range []struct {
		previous int
		de       *notify.DeliveryError
		want     time.Duration
	}{
		{0, &notify.DeliveryError{Status: 500}, 30 * time.Second},
		{1, &notify.DeliveryError{Status: 500}, time.Minute},
		{2, nil, 2 * time.Minute},
		{7, nil, time.Hour},
		{60, nil, time.Hour},
		{0, &notify.DeliveryError{Status: 429, RetryAfter: 10 * time.Minute}, 10 * time.Minute},
		{0, &notify.DeliveryError{Status: 429, RetryAfter: 5 * time.Hour}, time.Hour},
		{3, &notify.DeliveryError{Status: 503, RetryAfter: time.Second}, 4 * time.Minute},
		{0, &notify.DeliveryError{Status: 404}, time.Hour},
	} {
		if got := notify.RetryDelay(c.previous, c.de); got != c.want {
			t.Errorf("RetryDelay(%d, %+v) = %v, want %v", c.previous, c.de, got, c.want)
		}
	}
}
