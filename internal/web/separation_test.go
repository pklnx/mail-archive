package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/auth"
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
	if err := f.st.SetAccountOwner(ctx, bob.Ref(), other.ID); err != nil {
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
	for _, q := range []string{"to=alice@example.com", "attachment=Vertrag", "has=attachment", "from=Bob"} {
		if got := g.list(q); len(got) != 0 {
			t.Errorf("%s found another user's mail: %v", q, got)
		}
		if got := f.list(q); !equal(got, []string{"rich"}) {
			t.Errorf("%s as owner: %v", q, got)
		}
	}
	// The shared invoice is found through each user's own accounts.
	if got := g.list("to=buchhaltung&account=bob"); !equal(got, []string{"invoice"}) {
		t.Errorf("second user, to: %v", got)
	}
	if got := f.list("to=buchhaltung&account=bob"); len(got) != 0 {
		t.Errorf("first user found the invoice in the second user's account: %v", got)
	}
	if got := f.list("to=buchhaltung&account=alice"); !equal(got, []string{"invoice"}) {
		t.Errorf("first user, to: %v", got)
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

func TestAccountChangesWhileSyncing(t *testing.T) {
	f := newManageFixture(t, testUser(t))
	f.do("POST", "/api/accounts", withConfirm(f.newAccount("private", "alice", "secret")), 201)
	f.waitIdle("private")
	acc, err := accountByName(f.st, "private")
	if err != nil {
		t.Fatal(err)
	}

	// Hold the sync lock like a running sync does.
	unlock, ok, err := f.st.TryLockSync(context.Background(), acc.ID)
	if err != nil || !ok {
		t.Fatal("lock", ok, err)
	}
	f.do("DELETE", "/api/accounts/private", nil, 409)
	// Everything else works during the sync and is saved at once.
	f.do("PATCH", "/api/accounts/private", map[string]any{"name": "renamed", "enabled": false, "excludedFolders": []string{"Spam"}}, 204)
	unlock()

	a := f.account("renamed")
	if a.Enabled || len(a.ExcludedFolders) != 1 {
		t.Fatalf("after PATCH: %+v", a)
	}
	f.do("DELETE", "/api/accounts/renamed", nil, 200)
}

func TestOldOwnerCannotChangeMovedAccount(t *testing.T) {
	f := newManageFixture(t, testUser(t))
	other, token := sessionFor(t, f.st, "other")
	g := *f
	g.cookie = token
	f.do("POST", "/api/accounts", withConfirm(f.newAccount("private", "alice", "secret")), 201)
	f.waitIdle("private")
	acc, _ := accountByName(f.st, "private")
	if err := f.st.SetAccountOwner(context.Background(), acc.Ref(), other.ID); err != nil {
		t.Fatal(err)
	}
	f.do("PATCH", "/api/accounts/private", map[string]any{"enabled": false}, 404)
	f.do("DELETE", "/api/accounts/private", nil, 404)
	if a := g.account("private"); !a.Enabled {
		t.Fatal("the old owner changed the account")
	}
}

// credential is how a test request authenticates: a session token or an
// API key. Neither means the API fixture's own user, "tester".
type credential struct {
	name   string // for messages
	cookie string
	key    string
}

// readKey issues an API key with the read scope for u, like the profile
// page does, and returns it.
func readKey(t *testing.T, st *store.Store, u *store.User) string {
	t.Helper()
	key, hash, hint, err := auth.NewAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAPIKey(context.Background(), u.ID, store.NewAPIKey{
		Hash: hash, Hint: hint, Name: "read", Scopes: []string{store.ScopeRead},
	}); err != nil {
		t.Fatal(err)
	}
	return key
}

// fetch sends GET path with c and returns the status and the whole body,
// bytes included.
//
// The API fixture's loggedIn wrapper adds tester's session cookie to every
// request without one, so a key request also carries tester's cookie. The
// key's user counts, not the cookie's (TestAPIKeyNoCookieFallback); a
// fallback to the cookie would turn another user's 404s into 200s, so the
// tests below would catch it.
func (f *apiFixture) fetch(c credential, path string) (int, []byte) {
	f.t.Helper()
	req, _ := http.NewRequest("GET", f.srv.URL+path, nil)
	if c.cookie != "" {
		req.AddCookie(&http.Cookie{Name: cookieName, Value: c.cookie})
	}
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	return resp.StatusCode, body
}

// fetchJSON is fetch for a 200 JSON answer; it returns the body as well.
func (f *apiFixture) fetchJSON(c credential, path string, v any) []byte {
	f.t.Helper()
	status, body := f.fetch(c, path)
	if status != http.StatusOK {
		f.t.Fatalf("GET %s as %s: status %d: %s", path, c.name, status, body)
	}
	if err := json.Unmarshal(body, v); err != nil {
		f.t.Fatalf("GET %s as %s: %v: %s", path, c.name, err, body)
	}
	return body
}

// listAs is list with c; grouped also asks for the grouped list.
func (f *apiFixture) listAs(c credential, query string, grouped bool) []string {
	f.t.Helper()
	if grouped {
		query = "group=1&" + query
	}
	var r listResp
	f.fetchJSON(c, "/api/messages?"+query, &r)
	names := []string{}
	for _, m := range r.Messages {
		names = append(names, f.name(m.ID))
	}
	return names
}

// sharedLocation is what the shared-message test compares of a location.
type sharedLocation struct {
	Account, Folder string
	Filled          bool
	Envelope        *envelopeJSON
}

// The same bytes are received over SMTP for one user and synced over IMAP
// for another; a third user knows the SHA. Each sees exactly their own
// locations, finds the message only through their own accounts, and the
// third user gets nothing at all, with a session or an API key.
//
// "Alice" is the fixture's user tester: it already holds the rich message
// in alice/INBOX (IMAP) and gets an SMTP copy in alice-smtp. The fixture's
// bob account stays with tester and does not hold the message.
func TestSharedMessageAcrossOwners(t *testing.T) {
	f := newAPIFixture(t)
	ctx := context.Background()
	rich := f.ids["rich"]
	tester, err := f.st.GetUserByName(ctx, "tester")
	if err != nil {
		t.Fatal(err)
	}

	// tester: the same bytes over SMTP, with a BCC recipient that is in no
	// header.
	if strings.Contains(msgRich, "hidden-bcc") {
		t.Fatal("the BCC recipient is in the message")
	}
	smtpAcc := &store.Account{Name: "alice-smtp", Enabled: true, OwnerID: &tester.ID}
	if err := f.st.CreateSMTPAccount(ctx, smtpAcc, []string{"alice-archive@archive.test"}); err != nil {
		t.Fatal(err)
	}
	blob, created, err := f.blobs.Put(strings.NewReader(msgRich))
	if err != nil {
		t.Fatal(err)
	}
	meta, err := archive.BuildMeta(ctx, f.st, f.blobs, blob, created)
	if err != nil || meta.SHA256 != rich {
		t.Fatalf("smtp copy %s, %v", meta.SHA256, err)
	}
	env := store.Envelope{MailFrom: "bob@example.com", RcptTo: []string{"alice@example.com", "hidden-bcc@example.net"},
		ReceivedAt: time.Now(), ClientIP: netip.MustParseAddr("192.0.2.25"), HELO: "mx.example.org"}
	if _, err := f.st.ReceiveMessage(ctx, smtpAcc.ID, meta, env, func() bool { return true }); err != nil {
		t.Fatal(err)
	}

	// bob: the same bytes over IMAP in his own account.
	bob, bobToken := sessionFor(t, f.st, "bob")
	if err := f.st.CreateAccount(ctx, &store.Account{Name: "bob-imap", Host: "h", Port: 993, TLSMode: store.TLSModeTLS,
		Username: "u", PasswordEnc: []byte{1}, Enabled: true, OwnerID: &bob.ID}); err != nil {
		t.Fatal(err)
	}
	f.addMessage("bob-imap", "INBOX", "rich", msgRich, 1)
	if f.ids["rich"] != rich {
		t.Fatalf("bob's copy is %s, not %s", f.ids["rich"], rich)
	}

	// outsider: no account at all.
	outsider, outsiderToken := sessionFor(t, f.st, "outsider")

	asTester := credential{name: "tester"}
	asBob := credential{name: "bob", cookie: bobToken}
	bobKey := credential{name: "bob's key", key: readKey(t, f.st, bob)}
	asOutsider := credential{name: "outsider", cookie: outsiderToken}
	outsiderKey := credential{name: "outsider's key", key: readKey(t, f.st, outsider)}

	// Exactly the user's own locations; only the SMTP one has the envelope.
	locations := func(c credential) ([]sharedLocation, []byte) {
		t.Helper()
		var d messageJSON
		body := f.fetchJSON(c, "/api/messages/"+rich, &d)
		out := []sharedLocation{}
		for _, l := range d.Locations {
			if l.Envelope != nil {
				if l.Envelope.ReceivedAt.IsZero() {
					t.Errorf("%s: envelope without receivedAt", c.name)
				}
				l.Envelope.ReceivedAt = time.Time{}
			}
			out = append(out, sharedLocation{Account: l.Account, Folder: l.Folder, Filled: l.Filled, Envelope: l.Envelope})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Account < out[j].Account })
		return out, body
	}
	got, _ := locations(asTester)
	want := []sharedLocation{
		{Account: "alice", Folder: "INBOX"},
		{Account: "alice-smtp", Folder: store.SMTPFolder, Envelope: &envelopeJSON{
			MailFrom: "bob@example.com", RcptTo: []string{"alice@example.com", "hidden-bcc@example.net"},
			ClientIP: "192.0.2.25", HELO: "mx.example.org",
		}},
	}
	if !sameLocations(got, want) {
		t.Errorf("tester's locations %s, want %s", toJSON(got), toJSON(want))
	}
	for _, c := range []credential{asBob, bobKey} {
		got, body := locations(c)
		if want := []sharedLocation{{Account: "bob-imap", Folder: "INBOX"}}; !sameLocations(got, want) {
			t.Errorf("%s's locations %s, want %s", c.name, toJSON(got), toJSON(want))
		}
		if bytes.Contains(body, []byte("hidden-bcc")) {
			t.Errorf("%s sees the other owner's envelope: %s", c.name, body)
		}
	}

	// Search: per owner and per whole message. The account filter and the
	// envelope recipient are separate conditions over the owner's own
	// locations, so tester finds the BCC recipient also within alice.
	search := []struct {
		c     credential
		query string
		want  []string
	}{
		{asTester, "to=alice%40example.com", []string{"rich"}},
		{asTester, "to=alice%40example.com&account=alice", []string{"rich"}},
		{asTester, "to=alice%40example.com&account=alice-smtp", []string{"rich"}},
		{asTester, "to=hidden-bcc%40example.net", []string{"rich"}},
		{asTester, "to=hidden-bcc%40example.net&account=alice-smtp", []string{"rich"}},
		{asTester, "to=hidden-bcc%40example.net&account=alice", []string{"rich"}},
		{asTester, "account=bob-imap", []string{}},
		{asTester, "to=alice%40example.com&account=bob-imap", []string{}},
		{asBob, "to=alice%40example.com", []string{"rich"}},
		{asBob, "to=alice%40example.com&account=bob-imap", []string{"rich"}},
		{asBob, "to=hidden-bcc%40example.net", []string{}},
		{asBob, "account=alice-smtp", []string{}},
		{asBob, "to=hidden-bcc%40example.net&account=alice-smtp", []string{}},
		{bobKey, "to=alice%40example.com", []string{"rich"}},
		{bobKey, "to=hidden-bcc%40example.net", []string{}},
	}
	for _, s := range search {
		for _, grouped := range []bool{false, true} {
			if got := f.listAs(s.c, s.query, grouped); !slices.Equal(got, s.want) {
				t.Errorf("%s: %s (grouped %v) = %v, want %v", s.c.name, s.query, grouped, got, s.want)
			}
		}
	}

	// The owners read the message, its bytes and its attachment, so a
	// missing file or part cannot make a denial below pass.
	const pdf = "%PDF-1.4\n"
	objects := []string{"", "/html", "/raw", "/parts/3", "/conversation"}
	for _, c := range []credential{asTester, asBob, bobKey} {
		for _, p := range objects {
			status, body := f.fetch(c, "/api/messages/"+rich+p)
			if status != http.StatusOK {
				t.Errorf("GET rich%s as %s: %d %s", p, c.name, status, body)
			}
			switch {
			case p == "/raw" && string(body) != msgRich:
				t.Errorf("raw as %s: %q", c.name, body)
			case p == "/parts/3" && string(body) != pdf:
				t.Errorf("Vertrag.pdf as %s: %q", c.name, body)
			}
		}
	}

	// The outsider knows the SHA and finds nothing, in every form.
	leaks := []string{"Fotos", "Vertrag", "Hier das Logo", "hidden-bcc", "bob@example.com", "alice@example.com", pdf}
	for _, c := range []credential{asOutsider, outsiderKey} {
		for _, p := range objects {
			status, body := f.fetch(c, "/api/messages/"+rich+p)
			if status != http.StatusNotFound {
				t.Errorf("GET rich%s as %s: %d, want 404", p, c.name, status)
			}
			for _, l := range leaks {
				if bytes.Contains(body, []byte(l)) {
					t.Errorf("GET rich%s as %s: the answer contains %q: %s", p, c.name, l, body)
				}
			}
		}
		for _, q := range []string{"", "to=alice%40example.com", "q=Vertrag", "to=hidden-bcc%40example.net"} {
			for _, query := range []string{q, "thread=" + url.QueryEscape("rich@x") + "&" + q} {
				for _, grouped := range []bool{false, true} {
					if got := f.listAs(c, query, grouped); len(got) != 0 {
						t.Errorf("%s: %s (grouped %v) found %v", c.name, query, grouped, got)
					}
				}
			}
		}
	}
}

func sameLocations(a, b []sharedLocation) bool { return toJSON(a) == toJSON(b) }
