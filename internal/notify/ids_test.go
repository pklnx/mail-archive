package notify_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/keepia/archive/internal/notify"
	"github.com/keepia/archive/internal/store"
)

// leakMarkers are put into every name, host, mailbox, folder, subject,
// address and error text that an alert could be built from.
var leakMarkers = []string{
	"zqownerzq", "zqaccountzq", "zqhost.example", "zqimapuserzq", "zqfillaccountzq", "zqmailboxzq",
	"zqerrorzq", "zqfolderzq", "zqsubjectzq", "zqfrom@zq.example", "zqto@zq.example", "zqsmtp@zq.example", "zqgroupzq",
}

// leakEnv seeds a failing account, a failing recurring fill, a loss alert
// and a storage transition of a group, every one of them with markers.
func leakEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	ctx := context.Background()
	g := e.group("zqgroupzq")
	owner, err := e.store.CreateUserInGroup(ctx, store.NewUser{Name: "zqownerzq", PasswordHash: "x", GroupID: g})
	if err != nil {
		t.Fatal(err)
	}
	acc := &store.Account{Name: "zqaccountzq", Host: "zqhost.example", Port: 993, TLSMode: store.TLSModeTLS,
		Username: "zqimapuserzq", PasswordEnc: []byte{1}, Enabled: true, OwnerID: &owner.ID}
	if err := e.store.CreateAccount(ctx, acc); err != nil {
		t.Fatal(err)
	}
	folder, err := e.store.GetOrCreateFolder(ctx, acc.ID, "zqfolderzq")
	if err != nil {
		t.Fatal(err)
	}
	meta := store.MessageMeta{SHA256: strings.Repeat("a", 64), Size: 10, StoredPath: "x", Subject: "zqsubjectzq", From: "zqfrom@zq.example",
		IndexData: store.IndexData{To: "zqto@zq.example"}}
	if _, err := e.store.SaveBatch(ctx, folder.ID, 1, []store.MessageMeta{meta}, []store.Location{{FolderID: folder.ID, UID: 1}}); err != nil {
		t.Fatal(err)
	}
	// The loss comes from a successful run, before the failing ones.
	e.lose(acc, 40, 100, store.FolderLoss{Name: "zqfolderzq", Lost: 40})
	for range 3 {
		run, err := e.store.StartSyncRun(ctx, acc.ID)
		if err != nil {
			t.Fatal(err)
		}
		run.Status, run.Error, run.Health = "failed", "zqerrorzq", store.HealthFailure
		if err := e.store.FinishSyncRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}

	smtpAcc := &store.Account{Name: "zqfillaccountzq", Enabled: true, OwnerID: &owner.ID}
	if err := e.store.CreateSMTPAccount(ctx, smtpAcc, []string{"zqsmtp@zq.example"}); err != nil {
		t.Fatal(err)
	}
	fill := &store.Fill{AccountID: smtpAcc.ID, Host: "zqhost.example", Port: 993, TLSMode: store.TLSModeTLS, Username: "zqmailboxzq", KeepRunning: true}
	if err := e.store.CreateFill(ctx, fill, func(int64) ([]byte, error) { return []byte("x"), nil }); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if i > 0 {
			if err := e.store.StartFillRun(ctx, fill.ID, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
		if err := e.store.FinishFill(ctx, fill.ID, store.FillFailed, "zqerrorzq"); err != nil {
			t.Fatal(err)
		}
	}
	e.exec("UPDATE customer_groups SET quota_bytes = 100 WHERE id = $1", g)
	e.transition(g, "full")
	return e
}

// TestIDsDetailLeak: in the default detail mode, no marker reaches the
// webhook, in the JSON format or in ntfy's (body and Title header), for
// failing syncs, failing fills, losses and storage. The same seeds with
// full detail do reach it, so the seeds are where an alert reads them.
func TestIDsDetailLeak(t *testing.T) {
	for _, format := range []string{"json", "ntfy"} {
		t.Run(format, func(t *testing.T) {
			e := leakEnv(t)
			rcv := newReceiver(t)
			e.deliver(e.notifier(idsWebhook(t, rcv.srv.URL, format)))
			got := rcv.got()
			events := map[string]bool{}
			for _, r := range got {
				events[r.header.Get("X-Mail-Archive-Id")[:4]] = true
				all := string(r.body) + "\n" + decodeTitle(t, r.header.Get("Title"))
				for name, vals := range r.header {
					all += "\n" + name + ": " + strings.Join(vals, ", ")
				}
				for _, m := range leakMarkers {
					if strings.Contains(all, m) {
						t.Errorf("%s request %s contains %q:\n%s", format, r.header.Get("X-Mail-Archive-Id"), m, all)
					}
				}
			}
			// A sync alert, a fill alert, a loss alert and a storage alert.
			if len(got) != 4 || !events["fill"] || !events["gone"] || !events["stor"] {
				t.Fatalf("%d requests: %v", len(got), events)
			}
		})
	}

	// The control: with full detail the same seeds show.
	e := leakEnv(t)
	rcv := newReceiver(t)
	e.deliver(e.notifier(webhook(t, rcv.srv.URL, "", "")))
	var all strings.Builder
	for _, r := range rcv.got() {
		all.Write(r.body)
	}
	for _, m := range []string{"zqownerzq", "zqaccountzq", "zqerrorzq", "zqfolderzq", "zqmailboxzq", "zqfillaccountzq"} {
		if !strings.Contains(all.String(), m) {
			t.Errorf("full detail lacks %q", m)
		}
	}
}

// TestIDsDetailMessages: the ids messages are fixed text with IDs, and
// the structured accounts carry no field for a name.
func TestIDsDetailMessages(t *testing.T) {
	e := newEnv(t)
	acc := e.addAccount("work", "u", "p")
	e.fail(acc, 3)
	rcv := newReceiver(t, http.StatusOK)
	n := e.notifier(idsWebhook(t, rcv.srv.URL, ""))
	e.deliver(n)
	e.succeed(acc)
	e.deliver(n)
	e.lose(acc, 2, 10, store.FolderLoss{Name: "INBOX", Lost: 2})
	e.deliver(n)
	got := rcv.got()
	if len(got) != 3 {
		t.Fatalf("%d requests", len(got))
	}
	id := itoa(acc.ID)
	for i, want := range []struct{ title, message string }{
		{"Mail archive: sync of account " + id + " keeps failing", "Account " + id + " failed 3 syncs in a row since "},
		{"Mail archive: account " + id + " syncs again", "Account " + id + " synced successfully again after failing since "},
		{"Mail archive: 2 messages deleted on the server (account " + id + ")", "Account " + id + ": 2 of 10 messages are no longer on the server. They stay in the archive."},
	} {
		j := got[i].json
		if j["title"] != want.title || !strings.HasPrefix(j["message"].(string), want.message) {
			t.Errorf("request %d: %q / %q", i, j["title"], j["message"])
		}
		ev := j["accounts"].([]any)[0].(map[string]any)
		for _, field := range []string{"account", "owner", "lastError", "mailbox", "folders", "moreFolders"} {
			if _, ok := ev[field]; ok {
				t.Errorf("request %d has %q: %v", i, field, ev)
			}
		}
		if ev["accountId"] != float64(acc.ID) {
			t.Errorf("request %d: %v", i, ev)
		}
	}
	// The builders leave the values out, whatever the webhook does.
	m := notify.BuildMessage([]store.PendingNotification{{AccountID: 5, Account: "zqaccountzq", Owner: "zqownerzq", FailureStreak: 3,
		NotifiedState: store.NotifiedOK, LastError: "zqerrorzq"}}, false)
	if s := m.Title + m.Body + m.Slack + m.Discord; strings.Contains(s, "zq") || m.Accounts[0].Account != "" || m.Accounts[0].LastError != "" {
		t.Errorf("ids message %+v", m)
	}
}
