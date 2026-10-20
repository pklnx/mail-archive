package notify_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/notify"
	"github.com/pklnx/mail-archive/internal/store"
)

func TestBuildFillMessage(t *testing.T) {
	since := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	failing := store.PendingNotification{
		AccountID: 4, Account: "journal", Owner: "kim", FillID: 7, Mailbox: "ak@example.com",
		FailureStreak: 3, FailingSince: &since, NotifiedState: store.NotifiedOK, LastError: "LOGIN failed",
	}
	m := notify.BuildFillMessage([]store.PendingNotification{failing})
	ev := m.Accounts[0]
	if m.Event != notify.EventFailing || m.Title != "Mail archive: import of ak@example.com into journal keeps failing" ||
		m.ID != "fill-7-1792483200-failing" || m.ID != notify.FillEventID(7, &since, notify.EventFailing) ||
		ev.FillID != 7 || ev.Mailbox != "ak@example.com" || ev.AccountID != 4 || ev.LastError != "LOGIN failed" {
		t.Fatalf("alert %+v", m)
	}
	if !strings.Contains(m.Body, "failed 3 runs in a row since 2026-10-20 08:00 UTC") || !strings.Contains(m.Body, "Last error: LOGIN failed") ||
		!strings.Contains(m.Body, `account "journal" (owner kim, fill 7)`) {
		t.Errorf("body %q", m.Body)
	}

	recovered := failing
	recovered.NotifiedState, recovered.FailureStreak = store.NotifiedFailing, 0
	m = notify.BuildFillMessage([]store.PendingNotification{recovered})
	if m.Event != notify.EventRecovered || m.Title != "Mail archive: ak@example.com imports into journal again" ||
		m.Accounts[0].LastError != "" || !strings.HasSuffix(m.ID, "-recovered") || !strings.Contains(m.Body, "ran successfully again") {
		t.Errorf("recovery %+v", m)
	}

	other := failing
	other.FillID, other.Mailbox = 8, "bob@example.com"
	m = notify.BuildFillMessage([]store.PendingNotification{failing, other, recovered})
	if m.Event != notify.EventMixed || m.Title != "Mail archive: 2 imports failing, 1 recovered" || !strings.HasPrefix(m.ID, "batch-") {
		t.Errorf("mixed %+v", m)
	}
	m = notify.BuildFillMessage([]store.PendingNotification{failing, other})
	if m.Title != "Mail archive: imports of 2 mailboxes keep failing" {
		t.Errorf("two failing %q", m.Title)
	}
}

// TestFillAlertAndRecovery: a recurring fill whose runs keep failing is
// announced once, after the account alerts, with fillId and mailbox, and
// its recovery once.
func TestFillAlertAndRecovery(t *testing.T) {
	e := newEnv(t)
	acc := &store.Account{Name: "journal", Enabled: true}
	if err := e.store.CreateSMTPAccount(e.ctx, acc, []string{"archive@archive.test"}); err != nil {
		t.Fatal(err)
	}
	fill := &store.Fill{AccountID: acc.ID, Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "ak@example.com", KeepRunning: true}
	if err := e.store.CreateFill(e.ctx, fill, func(int64) ([]byte, error) { return []byte("x"), nil }); err != nil {
		t.Fatal(err)
	}
	// A run ends with status; the next one starts first, except for the
	// fill's first run.
	runs := 0
	finish := func(status string) {
		t.Helper()
		if runs++; runs > 1 {
			if err := e.store.StartFillRun(e.ctx, fill.ID, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
		if err := e.store.FinishFill(e.ctx, fill.ID, status, map[string]string{store.FillFailed: "LOGIN failed"}[status]); err != nil {
			t.Fatal(err)
		}
	}
	imapAcc := e.addAccount("work", "u", "p")
	rcv := newReceiver(t, http.StatusInternalServerError)
	n := e.notifier(webhook(t, rcv.srv.URL, "", ""))
	e.fail(imapAcc, 3)
	for range 3 {
		finish(store.FillFailed)
	}
	// The first request (the account's alert) fails: the fill waits too.
	e.deliver(n)
	if got := rcv.got(); len(got) != 1 || got[0].json["accounts"].([]any)[0].(map[string]any)["fillId"] != nil {
		t.Fatalf("first request %+v", got)
	}
	e.exec("UPDATE account_sync_health SET notify_next_at = now() - interval '1 second'")
	e.deliver(n)
	got := rcv.got()
	if len(got) != 3 {
		t.Fatalf("%d requests", len(got))
	}
	alert := got[2].json
	ev := alert["accounts"].([]any)[0].(map[string]any)
	if alert["event"] != "failing" || !strings.HasPrefix(alert["id"].(string), "fill-") || ev["fillId"] != float64(fill.ID) ||
		ev["mailbox"] != "ak@example.com" || ev["account"] != "journal" || ev["lastError"] != "LOGIN failed" {
		t.Fatalf("fill alert %v", alert)
	}
	e.deliver(n)
	if len(rcv.got()) != 3 {
		t.Fatalf("alert sent twice: %d", len(rcv.got()))
	}

	finish(store.FillOK)
	e.deliver(n)
	got = rcv.got()
	if len(got) != 4 || got[3].json["event"] != "recovered" || !strings.HasSuffix(got[3].json["id"].(string), "-recovered") {
		t.Fatalf("recovery %d %v", len(got), got[len(got)-1].json)
	}
}
