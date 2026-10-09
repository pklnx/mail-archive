package notify_test

import (
	"mime"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/notify"
	"github.com/pklnx/mail-archive/internal/store"
)

// lose records a run of acc that lost lost of before messages.
func (e *env) lose(acc *store.Account, lost, before int, folders ...store.FolderLoss) *store.SyncRun {
	e.t.Helper()
	run, err := e.store.StartSyncRun(e.ctx, acc.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	run.Status, run.Health = "ok", store.HealthSuccess
	run.LossAlert = &store.LossAlert{Lost: lost, PresentBefore: before, Folders: folders}
	if err := e.store.FinishSyncRun(e.ctx, run); err != nil {
		e.t.Fatal(err)
	}
	return run
}

func TestLossAlert(t *testing.T) {
	e := newEnv(t)
	acc := e.addAccount("personal", "u", "p")
	run := e.lose(acc, 1234, 5000, store.FolderLoss{Name: "INBOX", Lost: 1200}, store.FolderLoss{Name: "Archive", Lost: 34})
	rcv := newReceiver(t)
	n := e.notifier(webhook(t, rcv.srv.URL, "", ""))
	e.deliver(n)
	e.deliver(n)
	got := rcv.got()
	if len(got) != 1 {
		t.Fatalf("%d requests, want 1", len(got))
	}
	j := got[0].json
	if j["event"] != "gone" || j["id"] != notify.LossEventID(run.ID) || got[0].header.Get("X-Mail-Archive-Id") != j["id"] ||
		j["title"] != "Mail archive: 1,234 messages deleted on the server (personal)" {
		t.Fatalf("message: %v", j)
	}
	want := `Account "personal" (owner -, ID ` // no owner in this env
	if msg := j["message"].(string); !strings.HasPrefix(msg, want) ||
		!strings.Contains(msg, "1,234 of 5,000 messages are no longer on the server. They stay in the archive.") ||
		!strings.Contains(msg, "Folders: INBOX 1,200, Archive 34.") {
		t.Fatalf("text: %s", msg)
	}
	ev := j["accounts"].([]any)[0].(map[string]any)
	folders := ev["folders"].([]any)
	if ev["event"] != "gone" || ev["lost"] != 1234.0 || ev["presentBefore"] != 5000.0 || len(folders) != 2 ||
		folders[0].(map[string]any)["name"] != "INBOX" || ev["failureStreak"] != 0.0 {
		t.Fatalf("account event: %v", ev)
	}
	if !strings.Contains(e.logs.String(), "loss alert sent") {
		t.Fatalf("audit log: %s", e.logs)
	}
}

// Failing syncs and losses go out in the same Deliver, as separate
// messages; several losses share one.
func TestLossAlertsBesideFailures(t *testing.T) {
	e := newEnv(t)
	broken := e.addAccount("broken", "u", "p")
	e.fail(broken, 3)
	a := e.addAccount("a", "u", "p")
	b := e.addAccount("b", "u", "p")
	e.lose(a, 100, 100000)
	e.lose(b, 12, 20)
	rcv := newReceiver(t)
	e.deliver(e.notifier(webhook(t, rcv.srv.URL, "ntfy", "")))
	got := rcv.got()
	if len(got) != 2 {
		t.Fatalf("%d requests, want 2", len(got))
	}
	if got[0].header.Get("Tags") != "warning" || got[1].header.Get("Tags") != "wastebasket" || got[1].header.Get("Priority") != "high" {
		t.Fatalf("headers: %v / %v", got[0].header, got[1].header)
	}
	title, err := new(mime.WordDecoder).DecodeHeader(got[1].header.Get("Title"))
	if err != nil || title != "Mail archive: 112 messages deleted on the server in 2 accounts" {
		t.Fatalf("title: %q, %v", title, err)
	}
	if body := string(got[1].body); !strings.Contains(body, `"a"`) || !strings.Contains(body, `"b"`) ||
		!strings.Contains(body, "100 of 100,000 messages") {
		t.Fatalf("body: %s", body)
	}
	if !strings.HasPrefix(got[1].header.Get("X-Mail-Archive-Id"), "batch-") {
		t.Fatalf("id: %s", got[1].header.Get("X-Mail-Archive-Id"))
	}
}

func TestLossAlertRetryAndGiveUp(t *testing.T) {
	e := newEnv(t)
	acc := e.addAccount("personal", "u", "p")
	e.lose(acc, 50, 60)
	rcv := newReceiver(t, http.StatusServiceUnavailable, http.StatusServiceUnavailable)
	n := e.notifier(webhook(t, rcv.srv.URL, "", ""))
	e.deliver(n)
	e.deliver(n) // the retry waits
	if len(rcv.got()) != 1 {
		t.Fatalf("retried too early: %d", len(rcv.got()))
	}
	e.exec("UPDATE loss_alerts SET notify_next_at = now() - interval '1 second'")
	e.deliver(n)
	got := rcv.got()
	if len(got) != 2 || got[0].json["id"] != got[1].json["id"] {
		t.Fatalf("retry: %d requests", len(got))
	}
	// A day after the run it is given up, even though it was never sent.
	e.exec("UPDATE loss_alerts SET notify_next_at = now(), created_at = now() - interval '23 hours 59 minutes'")
	n.Now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	rcv.mu.Lock()
	rcv.statuses = []int{http.StatusServiceUnavailable}
	rcv.mu.Unlock()
	e.deliver(n)
	var givenUp int
	if err := e.conn.QueryRow(e.ctx, "SELECT count(*) FROM loss_alerts WHERE given_up_at IS NOT NULL").Scan(&givenUp); err != nil || givenUp != 1 {
		t.Fatalf("given up: %d, %v", givenUp, err)
	}
	if !strings.Contains(e.logs.String(), "notification given up") {
		t.Fatalf("log: %s", e.logs)
	}
}

// An alert waiting while the receiver is down keeps its ID when sent.
func TestLossAlertCrashResendsTheSameID(t *testing.T) {
	e := newEnv(t)
	run := e.lose(e.addAccount("personal", "u", "p"), 50, 60)
	rcv := newReceiver(t)
	n := e.notifier(webhook(t, rcv.srv.URL, "", ""))
	e.deliver(n)
	// The process died after the request: the row is still unsent.
	e.exec("UPDATE loss_alerts SET sent_at = NULL, notify_lease = NULL, notify_lease_until = NULL")
	e.deliver(n)
	got := rcv.got()
	if len(got) != 2 || got[1].json["id"] != notify.LossEventID(run.ID) || got[0].json["id"] != got[1].json["id"] {
		t.Fatalf("requests: %d", len(got))
	}
}
