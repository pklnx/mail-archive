package web

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"testing"

	"github.com/pklnx/mail-archive/internal/store"
)

// Gone is per user: Alice's and Bob's accounts hold the same invoice. Only
// Alice's copy is gone from her server; Bob learns nothing about it.
func TestGoneMessagesPerUser(t *testing.T) {
	f := newAPIFixture(t)
	ctx := context.Background()
	other, token := sessionFor(t, f.st, "other")
	bob, err := accountByName(f.st, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetAccountOwner(ctx, bob.Ref(), other.ID); err != nil {
		t.Fatal(err)
	}
	g := *f
	g.cookie = token
	alice, err := accountByName(f.st, "alice")
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := f.st.GetOrCreateFolder(ctx, alice.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	// The server lists meeting (2) and rich (3), not the invoice (1).
	if _, err := f.st.ReconcileFolder(ctx, inbox.ID, 1, []int64{2, 3}, []string{`\Seen`, `\Seen`}); err != nil {
		t.Fatal(err)
	}
	// A message only in an import account was never on a server.
	imported := &store.Account{Kind: store.KindImport, Name: "old", OwnerID: alice.OwnerID}
	if err := f.st.CreateAccount(ctx, imported); err != nil {
		t.Fatal(err)
	}
	f.addMessage("old", "Archive", "imported", msgNewsletter, 1)

	if got := f.list("gone=1"); !equal(got, []string{"invoice"}) {
		t.Errorf("alice's gone messages: %v", got)
	}
	if got := f.list("gone=true&account=alice&q=Rechnung"); !equal(got, []string{"invoice"}) {
		t.Errorf("combined with account and query: %v", got)
	}
	if got := f.list("gone=1&account=old"); len(got) != 0 {
		t.Errorf("gone in the import account: %v", got)
	}
	if got := g.list("gone=1"); len(got) != 0 {
		t.Errorf("bob's gone messages: %v", got)
	}

	var detail struct {
		Locations []locationJSON `json:"locations"`
	}
	f.getJSON("/api/messages/"+f.ids["invoice"], 200, &detail)
	if len(detail.Locations) != 1 || detail.Locations[0].GoneAt == nil || detail.Locations[0].LastSeenAt.IsZero() {
		t.Errorf("alice's locations: %+v", detail.Locations)
	}
	g.getJSON("/api/messages/"+f.ids["invoice"], 200, &detail)
	if len(detail.Locations) != 1 || detail.Locations[0].GoneAt != nil {
		t.Errorf("bob's locations: %+v", detail.Locations)
	}

	var status struct {
		Accounts []statusAccount `json:"accounts"`
	}
	f.getJSON("/api/status", 200, &status)
	gone := map[string]int64{}
	for _, a := range status.Accounts {
		gone[a.Name] = a.GoneMessages
	}
	if gone["alice"] != 1 || gone["old"] != 0 || len(gone) != 2 {
		t.Errorf("alice's status: %v", gone)
	}
	g.getJSON("/api/status", 200, &status)
	if len(status.Accounts) != 1 || status.Accounts[0].GoneMessages != 0 {
		t.Errorf("bob's status: %+v", status.Accounts)
	}
	var accounts accountsResponse
	f.getJSON("/api/accounts", 200, &accounts)
	for _, a := range accounts.Accounts {
		if a.Name == "alice" && (a.GoneMessages != 1 || a.LastReconciledAt == nil || a.Folders[0].LastReconciledAt == nil) {
			t.Errorf("alice in /api/accounts: %+v", a)
		}
	}

	if resp := f.get("/api/messages?gone=maybe"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("gone=maybe: %d", resp.StatusCode)
	}
}

func TestReconcileButton(t *testing.T) {
	f := newManageFixture(t, testUser(t))
	f.do("POST", "/api/accounts", f.newAccount("private", "alice", "secret"), http.StatusCreated)
	plain := f.waitIdle("private")
	if plain.Sync.LastRun.ReconciledFolders != 0 {
		t.Fatalf("plain sync reconciled: %+v", plain.Sync.LastRun)
	}
	if r := f.accounts(); r.ReconcileInterval != "" {
		t.Errorf("reconcileInterval = %q, want off", r.ReconcileInterval)
	}

	f.do("POST", "/api/accounts/private/sync", map[string]any{"reconcile": true}, http.StatusAccepted)
	first := waitNewRun(f, "private", plain.Sync.LastRun.StartedAt.String())
	if first.Sync.LastRun.ReconciledFolders != 2 || first.LastReconciledAt == nil {
		t.Fatalf("after the button: %+v", first)
	}
	// Folders reconciled in the last minutes are skipped.
	f.do("POST", "/api/accounts/private/sync", map[string]any{"reconcile": true}, http.StatusAccepted)
	if a := waitNewRun(f, "private", first.Sync.LastRun.StartedAt.String()); a.Sync.LastRun.ReconciledFolders != 0 {
		t.Fatalf("repeated click reconciled again: %+v", a.Sync.LastRun)
	}

	f.do("POST", "/api/accounts/private/sync", map[string]any{"reconcile": "yes"}, http.StatusBadRequest)
	f.do("POST", "/api/accounts/private/sync", map[string]any{"other": true}, http.StatusBadRequest)
	f.do("POST", "/api/accounts/nobody/sync", map[string]any{"reconcile": true}, http.StatusNotFound)

	imported := &store.Account{Kind: store.KindImport, Name: "old"}
	if err := f.st.CreateAccount(context.Background(), imported); err != nil {
		t.Fatal(err)
	}
	user, err := f.st.GetUserByName(context.Background(), "tester")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetAccountOwner(context.Background(), imported.Ref(), user.ID); err != nil {
		t.Fatal(err)
	}
	f.do("POST", "/api/accounts/old/sync", map[string]any{"reconcile": true}, http.StatusConflict)
}

// waitNewRun waits for a finished run other than the one that started at
// after. Since #187 the account no longer shows idle with the previous run
// after the request queued a new one (TestSyncStateNeverIdle); the check
// for a new run stays as a guard.
func waitNewRun(f *manageFixture, name, after string) accountJSON {
	f.t.Helper()
	var a accountJSON
	f.until(fmt.Sprintf("a new run of %s after %s", name, after), func() bool {
		a = f.account(name)
		return a.Sync.State == "idle" && a.Sync.LastRun != nil && a.Sync.LastRun.FinishedAt != nil &&
			a.Sync.LastRun.StartedAt.String() != after
	}, func() any { return a.Sync })
	return a
}

// A listing marks each row that is only in the archive with "gone": true,
// in the listing's account, and leaves the field out otherwise; the
// message detail never has it (#310).
func TestGoneFlagInListJSON(t *testing.T) {
	f := newAPIFixture(t)
	ctx := context.Background()
	alice, err := accountByName(f.st, "alice")
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := f.st.GetOrCreateFolder(ctx, alice.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	// The server lists meeting (2) and rich (3), not the invoice (1), which
	// is still in bob/INBOX of the same user.
	if _, err := f.st.ReconcileFolder(ctx, inbox.ID, 1, []int64{2, 3}, []string{`\Seen`, `\Seen`}); err != nil {
		t.Fatal(err)
	}
	// flags returns, per message name, the raw "gone" value of each row,
	// or "absent".
	flags := func(path string) map[string]string {
		t.Helper()
		var r struct {
			Messages []map[string]json.RawMessage `json:"messages"`
		}
		f.getJSON(path, 200, &r)
		out := map[string]string{}
		for _, m := range r.Messages {
			var id string
			if err := json.Unmarshal(m["id"], &id); err != nil {
				t.Fatal(err)
			}
			v, ok := m["gone"]
			if !ok {
				out[f.name(id)] = "absent"
			} else {
				out[f.name(id)] = string(v)
			}
		}
		return out
	}
	inAlice := map[string]string{"invoice": "true", "meeting": "absent", "rich": "absent"}
	for _, path := range []string{"/api/messages?account=alice", "/api/messages?account=alice&folder=INBOX", "/api/messages?account=alice&group=1"} {
		if got := flags(path); !maps.Equal(got, inAlice) {
			t.Errorf("%s: %v, want %v", path, got, inAlice)
		}
	}
	// In all mail, bob's copy keeps the invoice on a server.
	all := map[string]string{"invoice": "absent", "meeting": "absent", "rich": "absent", "newsletter": "absent"}
	for _, path := range []string{"/api/messages", "/api/messages?group=1"} {
		if got := flags(path); !maps.Equal(got, all) {
			t.Errorf("%s: %v, want %v", path, got, all)
		}
	}
	var detail map[string]json.RawMessage
	f.getJSON("/api/messages/"+f.ids["invoice"], 200, &detail)
	if v, ok := detail["gone"]; ok {
		t.Errorf("the message detail has gone: %s", v)
	}
}
