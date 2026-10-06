package web

import (
	"context"
	"net/http"
	"sort"
	"testing"

	"github.com/pklnx/mail-archive/internal/store"
)

// In the API fixture, the fixture's user owns "alice" (invoice, meeting,
// rich) and "bob" (invoice, newsletter). Handing "bob" to a second user
// gives both users the invoice, and one message only each otherwise.
func TestUsersSeeOnlyTheirOwnMail(t *testing.T) {
	f := newAPIFixture(t)
	ctx := context.Background()
	other, token := sessionFor(t, f.st, "other")
	bob, err := accountByName(f.st, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetAccountOwner(ctx, bob.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	g := *f
	g.cookie = token

	sorted := func(l []string) []string { sort.Strings(l); return l }
	if got := sorted(f.list("")); !equal(got, []string{"invoice", "meeting", "rich"}) {
		t.Errorf("first user lists %v", got)
	}
	if got := sorted(g.list("")); !equal(got, []string{"invoice", "newsletter"}) {
		t.Errorf("second user lists %v", got)
	}
	// Search and filters only cover the user's own accounts.
	if got := g.list("q=Rechnung"); !equal(got, []string{"invoice"}) {
		t.Errorf("search: %v", got)
	}
	if got := g.list("q=roadmap"); len(got) != 0 {
		t.Errorf("search found another user's mail: %v", got)
	}
	if got := f.list("account=bob"); len(got) != 0 {
		t.Errorf("filter by another user's account: %v", got)
	}

	// Shared message: only the user's own locations.
	var d struct {
		Locations []locationJSON `json:"locations"`
	}
	g.getJSON("/api/messages/"+f.ids["invoice"], 200, &d)
	if len(d.Locations) != 1 || d.Locations[0].Account != "bob" {
		t.Errorf("second user's locations: %+v", d.Locations)
	}
	f.getJSON("/api/messages/"+f.ids["invoice"], 200, &d)
	if len(d.Locations) != 1 || d.Locations[0].Account != "alice" {
		t.Errorf("first user's locations: %+v", d.Locations)
	}

	// Guessing another user's message ID finds nothing, in every form.
	for _, p := range []string{"", "/html", "/raw", "/parts/1"} {
		if resp := g.get("/api/messages/" + f.ids["rich"] + p); resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET rich%s as second user: %d", p, resp.StatusCode)
		}
		if resp := f.get("/api/messages/" + f.ids["newsletter"] + p); resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET newsletter%s as first user: %d", p, resp.StatusCode)
		}
	}

	// Navigation and status: own accounts, own counts.
	var accounts accountsResponse
	g.getJSON("/api/accounts", 200, &accounts)
	if len(accounts.Accounts) != 1 || accounts.Accounts[0].Name != "bob" {
		t.Errorf("second user's accounts: %+v", accounts.Accounts)
	}
	var status struct {
		Unique   int64           `json:"uniqueMessages"`
		Accounts []statusAccount `json:"accounts"`
	}
	f.getJSON("/api/status", 200, &status)
	if status.Unique != 3 || len(status.Accounts) != 1 || status.Accounts[0].Name != "alice" {
		t.Errorf("first user's status: %+v", status)
	}
	g.getJSON("/api/status", 200, &status)
	if status.Unique != 2 || len(status.Accounts) != 1 || status.Accounts[0].Name != "bob" {
		t.Errorf("second user's status: %+v", status)
	}
}

func TestAccountEndpointsPerUser(t *testing.T) {
	f := newManageFixture(t, testUser(t))
	_, token := sessionFor(t, f.st, "other")
	g := *f
	g.cookie = token

	f.do("POST", "/api/accounts", withConfirm(f.newAccount("private", "alice", "secret")), 201)
	f.waitIdle("private")

	// The other user sees nothing and cannot touch the account.
	if got := g.accounts().Accounts; len(got) != 0 {
		t.Fatalf("other user sees %+v", got)
	}
	g.do("PATCH", "/api/accounts/private", map[string]any{"enabled": false}, 404)
	g.do("GET", "/api/accounts/private/server-folders", nil, 404)
	g.do("POST", "/api/accounts/private/sync", nil, 404)
	g.do("DELETE", "/api/accounts/private", nil, 404)
	if out := g.do("POST", "/api/sync", nil, 202); string(out) != "{\"queued\":0}\n" {
		t.Fatalf("sync all as other user: %s", out)
	}

	// The same name is free for the other user.
	g.do("POST", "/api/accounts", withConfirm(g.newAccount("private", "alice", "secret")), 201)
	g.waitIdle("private")
	if got := f.accounts().Accounts; len(got) != 1 {
		t.Fatalf("first user sees %d accounts", len(got))
	}
	if a := f.account("private"); !a.Enabled {
		t.Fatal("first user's account changed")
	}
}

func withConfirm(m map[string]any) map[string]any {
	m["confirmFolders"] = true
	return m
}

// accountByName returns the only account with this name, of any owner.
func accountByName(st *store.Store, name string) (*store.Account, error) {
	list, err := st.ListAccountsByName(context.Background(), name)
	if err != nil {
		return nil, err
	}
	if len(list) != 1 {
		return nil, store.ErrNotFound
	}
	return list[0], nil
}
