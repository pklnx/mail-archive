package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/imaptest"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

type manageFixture struct {
	t          *testing.T
	st         *store.Store
	srv        *httptest.Server
	cookie     string // session token; empty means the fixture's own user
	host       string
	port       int
	runnerDone chan struct{}
}

func newManageFixture(t *testing.T, users ...*imapmemserver.User) *manageFixture {
	t.Helper()
	return newManageFixtureWithRoles(t, nil, users...)
}

// newManageFixtureWithRoles serves fixed mailboxes with special-use
// attributes (see imaptest.StartWithRoles); nil serves the users' mailboxes.
func newManageFixtureWithRoles(t *testing.T, roles map[string]imap.MailboxAttr, users ...*imapmemserver.User) *manageFixture {
	t.Helper()
	st := storetest.New(t)
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
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	syncer := &archive.Syncer{Store: st, Blobs: blobs, Sealer: sealer, Logger: log}
	runner := &archive.Runner{Syncer: syncer, CheckEvery: 10 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	f := &manageFixture{t: t, st: st, runnerDone: make(chan struct{})}
	go func() { runner.Run(ctx); close(f.runnerDone) }()
	t.Cleanup(func() { cancel(); <-f.runnerDone })
	if roles != nil {
		f.host, f.port = imaptest.StartWithRoles(t, roles, users...)
	} else {
		f.host, f.port = imaptest.Start(t, users...)
	}
	s := New(st, blobs, log, Options{AllowedHosts: []string{"127.0.0.1"}, Syncer: syncer, Runner: runner})
	f.srv = httptest.NewServer(loggedIn(t, st, s.Handler()))
	t.Cleanup(f.srv.Close)
	return f
}

// do sends a same-origin JSON request like the UI does.
func (f *manageFixture) do(method, path string, body any, want int) []byte {
	f.t.Helper()
	var r io.Reader = http.NoBody
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, f.srv.URL+path, r)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", f.srv.URL)
	if f.cookie != "" {
		req.AddCookie(&http.Cookie{Name: cookieName, Value: f.cookie})
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		f.t.Fatalf("%s %s: status %d, want %d: %s", method, path, resp.StatusCode, want, out)
	}
	return out
}

func (f *manageFixture) accounts() accountsResponse {
	f.t.Helper()
	var r accountsResponse
	if err := json.Unmarshal(f.do("GET", "/api/accounts", nil, 200), &r); err != nil {
		f.t.Fatal(err)
	}
	return r
}

func (f *manageFixture) account(name string) accountJSON {
	f.t.Helper()
	for _, a := range f.accounts().Accounts {
		if a.Name == name {
			return a
		}
	}
	f.t.Fatalf("account %q not listed", name)
	return accountJSON{}
}

// waitIdle waits until the account's sync finished and returns it.
func (f *manageFixture) waitIdle(name string) accountJSON {
	f.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		a := f.account(name)
		if a.Sync.State == "idle" && a.Sync.LastRun != nil && a.Sync.LastRun.FinishedAt != nil {
			return a
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.t.Fatalf("sync of %q did not finish", name)
	return accountJSON{}
}

func (f *manageFixture) newAccount(name, user, password string) map[string]any {
	return map[string]any{"name": name, "host": f.host, "port": f.port, "tls": "none", "username": user, "password": password}
}

func testUser(t *testing.T) *imapmemserver.User {
	u := imapmemserver.NewUser("alice", "secret")
	imaptest.CreateMailboxes(t, u, "INBOX", "Spam")
	imaptest.Append(t, u, "INBOX", []byte(msgInvoice))
	imaptest.Append(t, u, "Spam", []byte(msgNewsletter))
	return u
}

func TestCreateAccountAndSync(t *testing.T) {
	f := newManageFixture(t, testUser(t))

	// The login is checked before anything is saved.
	body := f.do("POST", "/api/accounts", f.newAccount("private", "alice", "wrong"), http.StatusUnprocessableEntity)
	if !strings.Contains(string(body), "login failed") {
		t.Errorf("wrong password: %s", body)
	}
	if n := len(f.accounts().Accounts); n != 0 {
		t.Fatalf("%d accounts after failed login", n)
	}

	// A new account is synced right away.
	f.do("POST", "/api/accounts", f.newAccount("private", "alice", "secret"), http.StatusCreated)
	a := f.waitIdle("private")
	if a.Sync.LastRun.Status != "ok" || a.Sync.LastRun.Fetched != 2 || len(a.Folders) != 2 {
		t.Fatalf("after first sync: %+v", a)
	}
	if a.Host != f.host || a.TLS != "none" || a.Username != "alice" {
		t.Errorf("connection: %+v", a)
	}
	raw := f.do("GET", "/api/accounts", nil, 200)
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "password") {
		t.Errorf("account list leaks the password: %s", raw)
	}

	f.do("POST", "/api/accounts", f.newAccount("private", "alice", "secret"), http.StatusConflict)
	for _, bad := range []map[string]any{
		{"name": "a/b", "host": "h", "username": "u", "password": "p"},
		{"name": "x", "host": "", "username": "u", "password": "p"},
		{"name": "x", "host": "h", "username": "u", "password": ""},
		{"name": "x", "host": "h", "tls": "ssl", "username": "u", "password": "p"},
		{"name": "x", "host": "h", "port": 70000, "username": "u", "password": "p"},
		{"name": "x", "host": "h", "username": "u", "password": "p", "unknown": 1},
	} {
		f.do("POST", "/api/accounts", bad, http.StatusBadRequest)
	}
}

func TestUpdateAccount(t *testing.T) {
	f := newManageFixture(t, testUser(t))
	f.do("POST", "/api/accounts", f.newAccount("private", "alice", "secret"), http.StatusCreated)
	f.waitIdle("private")

	// Live folder list with the current selection.
	var folders struct {
		Folders []serverFolderJSON `json:"folders"`
	}
	if err := json.Unmarshal(f.do("GET", "/api/accounts/private/server-folders", nil, 200), &folders); err != nil {
		t.Fatal(err)
	}
	if len(folders.Folders) != 2 || !folders.Folders[0].Selected || !folders.Folders[1].Selected {
		t.Fatalf("server folders: %+v", folders)
	}

	f.do("PATCH", "/api/accounts/private", map[string]any{"excludedFolders": []string{"Spam"}, "enabled": false}, http.StatusNoContent)
	a := f.account("private")
	if a.Enabled || len(a.ExcludedFolders) != 1 || a.ExcludedFolders[0] != "Spam" {
		t.Fatalf("after update: %+v", a)
	}

	// A wrong new password is rejected and the old one stays.
	f.do("PATCH", "/api/accounts/private", map[string]any{"password": "wrong"}, http.StatusUnprocessableEntity)
	f.do("PATCH", "/api/accounts/private", map[string]any{"username": "alice"}, http.StatusNoContent)
	f.do("PATCH", "/api/accounts/missing", map[string]any{"enabled": true}, http.StatusNotFound)

	// Manual sync also works for a disabled account.
	f.do("POST", "/api/accounts/private/sync", nil, http.StatusAccepted)
	f.waitIdle("private")
	var all struct {
		Queued int `json:"queued"`
	}
	if err := json.Unmarshal(f.do("POST", "/api/sync", nil, http.StatusAccepted), &all); err != nil || all.Queued != 0 {
		t.Fatalf("sync all queued %d (disabled accounts are skipped): %v", all.Queued, err)
	}
}

func TestRemoveAccount(t *testing.T) {
	f := newManageFixture(t, testUser(t))
	f.do("POST", "/api/accounts", f.newAccount("private", "alice", "secret"), http.StatusCreated)
	f.waitIdle("private")

	// With archived mail: removed, mail kept.
	var res struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(f.do("DELETE", "/api/accounts/private", nil, 200), &res); err != nil || res.Result != "removed" {
		t.Fatalf("remove: %+v %v", res, err)
	}
	a := f.account("private")
	if !a.Removed || a.Enabled || len(a.Folders) != 2 {
		t.Fatalf("removed account: %+v", a)
	}
	f.do("PATCH", "/api/accounts/private", map[string]any{"enabled": true}, http.StatusConflict)
	f.do("POST", "/api/accounts/private/sync", nil, http.StatusConflict)
	f.do("DELETE", "/api/accounts/private", nil, http.StatusConflict)
	f.do("POST", "/api/accounts", f.newAccount("private", "alice", "secret"), http.StatusConflict)

	// Without archived mail: deleted.
	f.do("POST", "/api/accounts", map[string]any{
		"name": "empty", "host": f.host, "port": f.port, "tls": "none", "username": "alice", "password": "secret", "enabled": false,
	}, http.StatusCreated)
	if err := json.Unmarshal(f.do("DELETE", "/api/accounts/empty", nil, 200), &res); err != nil || res.Result != "deleted" {
		t.Fatalf("delete: %+v %v", res, err)
	}
	if n := len(f.accounts().Accounts); n != 1 {
		t.Fatalf("%d accounts left, want 1", n)
	}
}

func TestManagementNeedsSecretKey(t *testing.T) {
	f := newAPIFixture(t) // no syncer: browse only
	var r accountsResponse
	f.getJSON("/api/accounts", 200, &r)
	if r.Manage || len(r.Accounts) != 2 {
		t.Fatalf("accounts: %+v", r)
	}
	req, _ := http.NewRequest("POST", f.srv.URL+"/api/sync", http.NoBody)
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
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestRenameAccountAPI(t *testing.T) {
	f := newManageFixture(t, testUser(t))
	f.do("POST", "/api/accounts", f.newAccount("alice@example.com", "alice", "secret"), http.StatusCreated)
	f.waitIdle("alice@example.com")
	f.do("POST", "/api/accounts", map[string]any{
		"name": "taken", "host": f.host, "port": f.port, "tls": "none", "username": "alice", "password": "secret", "enabled": false,
	}, http.StatusCreated)

	f.do("PATCH", "/api/accounts/alice@example.com", map[string]any{"name": "a/b"}, http.StatusBadRequest)
	f.do("PATCH", "/api/accounts/alice@example.com", map[string]any{"name": "taken"}, http.StatusConflict)
	// A failed login in the same request leaves the name unchanged.
	f.do("PATCH", "/api/accounts/alice@example.com", map[string]any{"name": "example", "password": "wrong"}, http.StatusUnprocessableEntity)
	f.account("alice@example.com")

	f.do("PATCH", "/api/accounts/alice@example.com", map[string]any{"name": "example"}, http.StatusNoContent)
	a := f.account("example")
	if len(a.Folders) != 2 || a.Folders[0].Messages+a.Folders[1].Messages != 2 {
		t.Fatalf("renamed account lost its mail: %+v", a.Folders)
	}
	// The re-encrypted password still works: a sync and a connection check succeed.
	f.do("POST", "/api/accounts/example/sync", nil, http.StatusAccepted)
	if run := f.waitIdle("example").Sync.LastRun; run.Status != "ok" {
		t.Fatalf("sync after rename: %+v", run)
	}
	f.do("PATCH", "/api/accounts/example", map[string]any{"username": "alice"}, http.StatusNoContent)
	f.do("PATCH", "/api/accounts/alice@example.com", map[string]any{"enabled": true}, http.StatusNotFound)
}

func TestCreateAccountConfirmsTrashAndSpam(t *testing.T) {
	roles := map[string]imap.MailboxAttr{"INBOX": "", "Spam": imap.MailboxAttrJunk, "Papierkorb": imap.MailboxAttrTrash}
	u := imapmemserver.NewUser("alice", "secret")
	imaptest.CreateMailboxes(t, u, "INBOX", "Spam", "Papierkorb")
	imaptest.Append(t, u, "INBOX", []byte(msgInvoice))
	imaptest.Append(t, u, "Spam", []byte(msgNewsletter))
	imaptest.Append(t, u, "Papierkorb", []byte(msgMeeting))
	f := newManageFixtureWithRoles(t, roles, u)

	// Without confirmation nothing is saved and the folders are named.
	var c folderConfirmation
	if err := json.Unmarshal(f.do("POST", "/api/accounts", f.newAccount("a", "alice", "secret"), http.StatusConflict), &c); err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.SuggestedExclusions, ",") != "Papierkorb,Spam" {
		t.Fatalf("suggestions: %+v", c)
	}
	if n := len(f.accounts().Accounts); n != 0 {
		t.Fatalf("%d accounts saved without confirmation", n)
	}
	// A wrong password is still reported as such, not as a folder question.
	f.do("POST", "/api/accounts", f.newAccount("a", "alice", "wrong"), http.StatusUnprocessableEntity)

	// "Save without them": only INBOX is archived.
	without := f.newAccount("without", "alice", "secret")
	without["confirmFolders"] = true
	without["excludedFolders"] = c.SuggestedExclusions
	f.do("POST", "/api/accounts", without, http.StatusCreated)
	if a := f.waitIdle("without"); len(a.Folders) != 1 || a.Folders[0].Name != "INBOX" {
		t.Fatalf("without trash and spam: %+v", a.Folders)
	}

	// "Save with all folders".
	all := f.newAccount("all", "alice", "secret")
	all["confirmFolders"] = true
	f.do("POST", "/api/accounts", all, http.StatusCreated)
	if a := f.waitIdle("all"); len(a.Folders) != 3 {
		t.Fatalf("with all folders: %+v", a.Folders)
	}

	// Already excluded: no question.
	excluded := f.newAccount("excluded", "alice", "secret")
	excluded["excludedFolders"] = []string{"spam", "papierkorb"}
	f.do("POST", "/api/accounts", excluded, http.StatusCreated)
}
