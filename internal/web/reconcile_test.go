package web

import (
	"context"
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
	if a := f.waitIdle("private"); a.Sync.LastRun.ReconciledFolders != 0 {
		t.Fatalf("plain sync reconciled: %+v", a.Sync.LastRun)
	}
	if r := f.accounts(); r.ReconcileInterval != "" {
		t.Errorf("reconcileInterval = %q, want off", r.ReconcileInterval)
	}

	f.do("POST", "/api/accounts/private/sync", map[string]any{"reconcile": true}, http.StatusAccepted)
	first := waitNewRun(f, "private", "")
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
// after ("" for any). The request queued it before answering, so the
// account is not idle until it finished.
func waitNewRun(f *manageFixture, name, after string) accountJSON {
	f.t.Helper()
	a := f.waitIdle(name)
	if a.Sync.LastRun.StartedAt.String() == after {
		f.t.Fatalf("no new run of %s", name)
	}
	return a
}
