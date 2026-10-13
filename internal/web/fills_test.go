package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/imaptest"
	"github.com/pklnx/mail-archive/internal/store"
)

// fillFixture is a manage fixture with an IMAP mailbox (INBOX and a Spam
// folder the server marks as junk) and an smtp account "journal" of the
// fixture's user.
func fillFixture(t *testing.T) (*manageFixture, *store.Account) {
	t.Helper()
	roles := map[string]imap.MailboxAttr{"INBOX": "", "Spam": imap.MailboxAttrJunk}
	u := imapmemserver.NewUser("alice@example.com", "secret")
	imaptest.CreateMailboxes(t, u, "INBOX", "Spam")
	imaptest.Append(t, u, "INBOX", []byte(msgInvoice))
	imaptest.Append(t, u, "Spam", []byte(msgNewsletter))
	f := newManageFixtureWithRoles(t, roles, u)
	ctx := context.Background()
	tester, err := f.st.GetUserByName(ctx, "tester")
	if err != nil {
		t.Fatal(err)
	}
	acc := &store.Account{Name: "journal", Enabled: true, OwnerID: &tester.ID}
	if err := f.st.CreateSMTPAccount(ctx, acc, []string{"archive@archive.test"}); err != nil {
		t.Fatal(err)
	}
	return f, acc
}

func (f *manageFixture) fillBody(password string) map[string]any {
	return map[string]any{"host": f.host, "port": f.port, "tls": "none", "username": "alice@example.com", "password": password}
}

// waitFill waits until the account's latest fill has the status.
func (f *manageFixture) waitFill(name, status string) accountJSON {
	f.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		a := f.account(name)
		if a.Fill != nil && a.Fill.Status == status && a.Sync.State == "idle" {
			return a
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.t.Fatalf("fill of %q did not reach %s: %+v", name, status, f.account(name).Fill)
	return accountJSON{}
}

func auditDetails(t *testing.T, r store.AuditRecord) map[string]any {
	t.Helper()
	var d map[string]any
	if err := json.Unmarshal(r.Details, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestFillAPI(t *testing.T) {
	f, acc := fillFixture(t)
	ctx := context.Background()
	if a := f.account("journal"); a.Fill != nil || a.FirstReceived != nil {
		t.Fatalf("before any fill: %+v", a)
	}
	journaled := receive(t, f, acc, "Subject: journaled\r\n")
	if a := f.account("journal"); a.FirstReceived == nil || a.FirstReceived.Subject != "journaled" {
		t.Fatalf("first received: %+v", a.FirstReceived)
	}

	// The folder list, before the fill exists.
	f.do("POST", "/api/accounts/journal/fills/folders", f.fillBody("wrong"), http.StatusUnprocessableEntity)
	var list struct {
		Folders             []serverFolderJSON `json:"folders"`
		SuggestedExclusions []string           `json:"suggestedExclusions"`
	}
	if err := json.Unmarshal(f.do("POST", "/api/accounts/journal/fills/folders", f.fillBody("secret"), 200), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Folders) != 2 || !slices.Equal(list.SuggestedExclusions, []string{"Spam"}) {
		t.Fatalf("folders %+v", list)
	}

	// Validation before the login, the login before anything is saved.
	for _, bad := range []map[string]any{
		{"host": f.host, "username": "alice@example.com"},
		{"host": "", "username": "alice@example.com", "password": "secret"},
		{"host": f.host, "port": f.port, "tls": "none", "username": "alice@example.com", "password": "secret", "folderPrefix": "INBOX"},
		{"host": f.host, "port": f.port, "tls": "none", "username": "alice@example.com", "password": "secret", "folderPrefix": "/x"},
		{"host": f.host, "tls": "ssl", "username": "alice@example.com", "password": "secret"},
	} {
		f.do("POST", "/api/accounts/journal/fills", bad, http.StatusBadRequest)
	}
	f.do("POST", "/api/accounts/journal/fills", f.fillBody("wrong"), http.StatusUnprocessableEntity)
	var c folderConfirmation
	if err := json.Unmarshal(f.do("POST", "/api/accounts/journal/fills", f.fillBody("secret"), http.StatusConflict), &c); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.SuggestedExclusions, []string{"Spam"}) {
		t.Fatalf("confirmation %+v", c)
	}
	if a := f.account("journal"); a.Fill != nil {
		t.Fatalf("fill saved without confirmation: %+v", a.Fill)
	}

	n := len(auditLog(t, f.st))
	body := f.fillBody("secret")
	body["excludedFolders"] = []string{"Spam"}
	body["confirmFolders"] = true
	var started struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(f.do("POST", "/api/accounts/journal/fills", body, http.StatusAccepted), &started); err != nil {
		t.Fatal(err)
	}
	a := f.waitFill("journal", store.FillOK)
	fill := a.Fill
	if fill.ID != started.ID || fill.FolderPrefix != "alice@example.com" || fill.Host != f.host || fill.TLS != "none" ||
		!slices.Equal(fill.ExcludedFolders, []string{"Spam"}) || fill.FinishedAt == nil || len(fill.Folders) != 1 {
		t.Fatalf("fill %+v", fill)
	}
	if ff := fill.Folders[0]; ff.Source != "INBOX" || ff.Folder != "alice@example.com/INBOX" || !ff.Done ||
		ff.Scanned != 1 || ff.Imported != 1 || ff.New != 1 {
		t.Errorf("fill folder %+v", ff)
	}
	if a.FirstReceived == nil || a.FirstReceived.Subject != "journaled" {
		t.Errorf("first received after the fill: %+v", a.FirstReceived)
	}
	// The response never carries the password.
	if raw := string(f.do("GET", "/api/accounts", nil, 200)); strings.Contains(raw, "secret") || strings.Contains(raw, "password") {
		t.Errorf("accounts response mentions the password: %s", raw)
	}

	// Started by the user, ended by the web server.
	var actions []string
	for _, e := range newEntries(t, f.st, n) {
		actions = append(actions, e.Action)
		d := auditDetails(t, e)
		switch e.Action {
		case "data.fill":
			if e.TargetName != "journal" || d["host"] != f.host || d["username"] != "alice@example.com" ||
				d["prefix"] != "alice@example.com" || d["fill"] != float64(started.ID) || d["before"] == nil {
				t.Errorf("data.fill %+v %s", e, e.Details)
			}
			if fmt.Sprint(d["folders"]) != "map[excluded:[Spam] included:[]]" {
				t.Errorf("data.fill folders %v", d["folders"])
			}
		case "data.fill_done":
			if e.UserName != "" || d["by"] != "web server" || d["status"] != "ok" || d["imported"] != float64(1) {
				t.Errorf("data.fill_done %+v %s", e, e.Details)
			}
		}
	}
	if !slices.Equal(actions, []string{"data.fill", "data.fill_done"}) {
		t.Errorf("audit %v", actions)
	}

	// The copied location has no envelope and says so; the journaled one
	// does not.
	var msgs struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(f.do("GET", "/api/messages", nil, 200), &msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs.Messages) != 2 {
		t.Fatalf("messages %+v", msgs)
	}
	for _, m := range msgs.Messages {
		var msg struct {
			Locations []locationJSON `json:"locations"`
		}
		if err := json.Unmarshal(f.do("GET", "/api/messages/"+m.ID, nil, 200), &msg); err != nil {
			t.Fatal(err)
		}
		l := msg.Locations[0]
		if m.ID == journaled {
			if l.Filled || l.Envelope == nil {
				t.Errorf("journaled location %+v", l)
			}
		} else if !l.Filled || l.Envelope != nil || l.Folder != "alice@example.com/INBOX" {
			t.Errorf("filled location %+v", l)
		}
	}

	// A fill that ended well is neither resumed nor cancelled; the prefix
	// stays its own.
	id := fmt.Sprint(started.ID)
	f.do("POST", "/api/accounts/journal/fills/"+id+"/resume", map[string]any{"password": "secret"}, http.StatusConflict)
	f.do("POST", "/api/accounts/journal/fills/"+id+"/cancel", nil, http.StatusConflict)
	f.do("POST", "/api/accounts/journal/fills", body, http.StatusConflict)
	f.do("POST", "/api/accounts/journal/fills/999999/cancel", nil, http.StatusNotFound)
	f.do("POST", "/api/accounts/journal/fills/x/cancel", nil, http.StatusNotFound)

	// Only smtp accounts are filled.
	if err := f.st.CreateAccount(ctx, &store.Account{Name: "imap", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", OwnerID: acc.OwnerID}); err != nil {
		t.Fatal(err)
	}
	f.do("POST", "/api/accounts/imap/fills", body, http.StatusConflict)
	f.do("POST", "/api/accounts/imap/fills/folders", f.fillBody("secret"), http.StatusConflict)
	f.do("POST", "/api/accounts/nope/fills", body, http.StatusNotFound)
}

// receive journals a message into the smtp account and returns its ID.
func receive(t *testing.T, f *manageFixture, acc *store.Account, headers string) string {
	t.Helper()
	ctx := context.Background()
	blob, created, err := f.blobs.Put(strings.NewReader("From: a@example.com\r\nTo: b@example.com\r\n" + headers + "\r\nHello.\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	meta, err := archive.BuildMeta(ctx, f.st, f.blobs, blob, created)
	if err != nil {
		t.Fatal(err)
	}
	env := store.Envelope{MailFrom: "a@example.com", RcptTo: []string{"b@example.com"}, ReceivedAt: time.Now(), HELO: "mx"}
	if _, err := f.st.ReceiveMessage(ctx, acc.ID, meta, env, func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	return meta.SHA256
}

func TestFillCancelAndResume(t *testing.T) {
	f, acc := fillFixture(t)
	ctx := context.Background()
	// While the account's lock is held (as by the command line), the fill
	// waits and shows as running.
	unlock, ok, err := f.st.TryLockSyncForWrite(ctx, acc.ID)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	defer func() { unlock() }()
	body := f.fillBody("secret")
	body["confirmFolders"] = true
	var started struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(f.do("POST", "/api/accounts/journal/fills", body, http.StatusAccepted), &started); err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprint(started.ID)
	if a := f.account("journal"); a.Fill == nil || a.Fill.Status != store.FillRunning || a.Sync.State == "idle" {
		t.Fatalf("waiting fill %+v %+v", a.Fill, a.Sync)
	}
	// One fill per account at a time.
	body["folderPrefix"] = "other"
	f.do("POST", "/api/accounts/journal/fills", body, http.StatusConflict)
	f.do("POST", "/api/accounts/journal/fills/"+id+"/resume", map[string]any{"password": "secret"}, http.StatusConflict)
	// The account is not removed under a fill.
	if raw := f.do("DELETE", "/api/accounts/journal", nil, http.StatusConflict); !strings.Contains(string(raw), "synced or filled") {
		t.Errorf("delete: %s", raw)
	}

	n := len(auditLog(t, f.st))
	f.do("POST", "/api/accounts/journal/fills/"+id+"/cancel", nil, http.StatusNoContent)
	f.do("POST", "/api/accounts/journal/fills/"+id+"/cancel", nil, http.StatusConflict)
	if a := f.account("journal"); a.Fill.Status != store.FillCancelled {
		t.Fatalf("after cancel %+v", a.Fill)
	}
	if got, err := f.st.GetFill(ctx, started.ID); err != nil || got.PasswordEnc != nil {
		t.Fatalf("cancelled fill keeps its password: %v", err)
	}

	// Resuming asks for the password again and checks it.
	f.do("POST", "/api/accounts/journal/fills/"+id+"/resume", map[string]any{}, http.StatusBadRequest)
	f.do("POST", "/api/accounts/journal/fills/"+id+"/resume", map[string]any{"password": "wrong"}, http.StatusUnprocessableEntity)
	f.do("POST", "/api/accounts/journal/fills/"+id+"/resume", map[string]any{"password": "secret"}, http.StatusAccepted)
	unlock()
	unlock = func() {}
	a := f.waitFill("journal", store.FillOK)
	if len(a.Fill.Folders) != 2 {
		t.Errorf("resumed fill %+v", a.Fill)
	}
	var actions []string
	for _, e := range newEntries(t, f.st, n) {
		actions = append(actions, e.Action)
		if d := auditDetails(t, e); d["fill"] != float64(started.ID) || e.TargetName != "journal" {
			t.Errorf("%s: %s", e.Action, e.Details)
		}
	}
	if !slices.Equal(actions, []string{"data.fill_cancel", "data.fill_resume", "data.fill_done"}) {
		t.Errorf("audit %v", actions)
	}
}

func TestFillsNeedTheKey(t *testing.T) {
	f := newAPIFixture(t) // no syncer: browse only
	for _, path := range []string{"/api/accounts/journal/fills", "/api/accounts/journal/fills/folders", "/api/accounts/journal/fills/1/cancel"} {
		req, _ := http.NewRequest("POST", f.srv.URL+path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", f.srv.URL)
		if f.cookie != "" {
			req.AddCookie(&http.Cookie{Name: cookieName, Value: f.cookie})
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s: status %d", path, resp.StatusCode)
		}
	}
}
