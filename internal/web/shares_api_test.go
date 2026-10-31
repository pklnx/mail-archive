package web

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/pklnx/mail-archive/internal/store"
)

// PUT and DELETE /api/accounts/{name}/shares/{user} (#389): only the
// owner, only with a session; each change is audited in its transaction;
// sharing again writes nothing; access ends with the unshare at once.
func TestShareRoutes(t *testing.T) {
	w := newShareWorld(t)
	a := w.user("a", false)
	b := w.user("b", false)
	w.user("x", true)
	w.account("a", "info")
	w.put("a/info", "INBOX", "M", mail("m", "Hello", "Mon, 5 Oct 2026 10:00:00 +0000", "shared mail"))
	smtpOwner := a.ID
	smtp := &store.Account{Name: "journal", OwnerID: &smtpOwner}
	if err := w.st.CreateSMTPAccount(context.Background(), smtp, []string{"j@example.com"}); err != nil {
		t.Fatal(err)
	}
	ca, cb := w.creds["a"], w.creds["b"]
	lists := func(c credential) []string { names, _ := w.list(c, ""); return names }

	for _, c := range []struct {
		name   string
		cred   credential
		method string
		path   string
		want   int
	}{
		{"self", ca, "PUT", "/api/accounts/info/shares/a", http.StatusBadRequest},
		{"unknown user", ca, "PUT", "/api/accounts/info/shares/nobody", http.StatusNotFound},
		{"unknown account", ca, "PUT", "/api/accounts/other/shares/b", http.StatusNotFound},
		{"smtp", ca, "PUT", "/api/accounts/journal/shares/b", http.StatusConflict},
		{"admin on a's account", w.creds["x"], "PUT", "/api/accounts/info/shares/x", http.StatusNotFound},
		{"a's read key", w.creds["a-key"], "PUT", "/api/accounts/info/shares/b", http.StatusForbidden},
		{"unshare without share", ca, "DELETE", "/api/accounts/info/shares/b", http.StatusNotFound},
	} {
		n := len(auditLog(t, w.st))
		if status, body := w.send(c.cred, c.method, c.path); status != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, status, body, c.want)
		}
		if got := newEntries(t, w.st, n); len(got) != 0 {
			t.Errorf("%s: logged %+v", c.name, got)
		}
	}
	if got := lists(cb); len(got) != 0 {
		t.Fatalf("b lists %v before any share", got)
	}

	n := len(auditLog(t, w.st))
	if status, body := w.send(ca, "PUT", "/api/accounts/info/shares/B"); status != http.StatusNoContent {
		t.Fatalf("share: %d %s", status, body)
	}
	e := w.lastEntry(n, "share")
	if d := details(t, e); e.Action != "account.share" || e.TargetType != store.TargetAccount || e.TargetName != "info" ||
		d["user"] != "b" || d["user_id"] != float64(b.ID) || e.UserName != "a" {
		t.Errorf("share entry %+v %v", e, d)
	}
	if got := lists(cb); !slices.Equal(got, []string{"M"}) {
		t.Errorf("b lists %v", got)
	}
	n = len(auditLog(t, w.st))
	if status, _ := w.send(ca, "PUT", "/api/accounts/info/shares/b"); status != http.StatusNoContent {
		t.Errorf("share again: %d", status)
	}
	if got := newEntries(t, w.st, n); len(got) != 0 {
		t.Errorf("sharing again logged %+v", got)
	}
	// b cannot share or unshare a's account.
	for _, m := range []string{"PUT", "DELETE"} {
		if status, _ := w.send(cb, m, "/api/accounts/info/shares/b"); status != http.StatusNotFound {
			t.Errorf("%s as b: %d", m, status)
		}
	}

	// GET /api/accounts: a sees whom info is shared with, b sees a/info
	// with its folders only; the same with b's read key.
	var ra accountsResponse
	w.fetchJSON(ca, "/api/accounts", &ra)
	if len(ra.Accounts) != 2 || len(ra.Shared) != 0 {
		t.Fatalf("a's accounts %+v", ra)
	}
	for _, acc := range ra.Accounts {
		want := 0
		if acc.Name == "info" {
			want = 1
		}
		if len(acc.SharedWith) != want || want == 1 && (acc.SharedWith[0].User != "b" || acc.SharedWith[0].Since.IsZero()) {
			t.Errorf("a's %s shared with %+v", acc.Name, acc.SharedWith)
		}
	}
	for _, c := range []credential{cb, w.creds["b-key"]} {
		var rb accountsResponse
		body := w.fetchJSON(c, "/api/accounts", &rb)
		if len(rb.Accounts) != 0 || len(rb.Shared) != 1 {
			t.Fatalf("%s: accounts %s", c.name, body)
		}
		s := rb.Shared[0]
		if s.Key != "a/info" || s.Name != "info" || s.Owner != "a" || s.Kind != "imap" || !s.Enabled || s.Removed ||
			len(s.Folders) != 1 || s.Folders[0].Name != "INBOX" || s.Folders[0].Messages != 1 {
			t.Errorf("%s: shared %+v", c.name, s)
		}
		// Nothing of the account's setup or of other shared users.
		for _, leak := range []string{`"host"`, `"username"`, `"sharedWith"`, `"sync"`} {
			if strings.Contains(string(body[strings.Index(string(body), `"shared"`):]), leak) {
				t.Errorf("%s: shared account shows %s: %s", c.name, leak, body)
			}
		}
	}

	// The unshare ends the access at once, also for a removed account.
	n = len(auditLog(t, w.st))
	if status, body := w.send(ca, "DELETE", "/api/accounts/info/shares/b"); status != http.StatusNoContent {
		t.Fatalf("unshare: %d %s", status, body)
	}
	if e := w.lastEntry(n, "unshare"); e.Action != "account.unshare" || details(t, e)["user"] != "b" {
		t.Errorf("unshare entry %+v", e)
	}
	if got := lists(cb); len(got) != 0 {
		t.Errorf("b lists %v after the unshare", got)
	}
	if status, _ := w.fetch(cb, "/api/messages/"+w.ids["M"]); status != http.StatusNotFound {
		t.Errorf("b opens M after the unshare: %d", status)
	}
	w.share("a/info", "b")
	if _, _, err := w.st.RemoveAccountLocked(context.Background(), w.current("a/info").Ref()); err != nil {
		t.Fatal(err)
	}
	if status, _ := w.send(ca, "PUT", "/api/accounts/info/shares/x"); status != http.StatusConflict {
		t.Errorf("share a removed account: %d", status)
	}
	if got := lists(cb); !slices.Equal(got, []string{"M"}) {
		t.Errorf("b lists %v of the removed account", got)
	}
	if status, _ := w.send(ca, "DELETE", "/api/accounts/info/shares/b"); status != http.StatusNoContent {
		t.Errorf("unshare a removed account: %d", status)
	}

	// The share and its entry are saved together or not at all.
	breakAudit(t, w.dbURL)
	if status, _ := w.send(ca, "PUT", "/api/accounts/journal2/shares/b"); status != http.StatusNotFound {
		t.Errorf("unknown account: %d", status)
	}
	w.account("a", "second")
	if status, _ := w.send(ca, "PUT", "/api/accounts/second/shares/b"); status != http.StatusServiceUnavailable {
		t.Errorf("share without audit: %d", status)
	}
	if shares, err := w.st.AccountShares(context.Background(), w.accounts["a/second"].ID); err != nil || len(shares) != 0 {
		t.Errorf("share saved without its entry: %v %v", shares, err)
	}
}

// account=owner/name addresses a shared account; a plain name stays the
// viewer's own, also when both are called info (#389, test 5).
func TestShareNameCollision(t *testing.T) {
	w := newShareWorld(t)
	w.user("a", false)
	w.user("b", false)
	w.account("a", "info")
	w.account("b", "info")
	w.put("a/info", "INBOX", "A", mail("a", "Of a", "Mon, 5 Oct 2026 10:00:00 +0000", "a's"))
	w.put("b/info", "Sent", "B", mail("b", "Of b", "Tue, 6 Oct 2026 10:00:00 +0000", "b's"))
	w.share("a/info", "b")
	for _, c := range []credential{w.creds["b"], w.creds["b-key"]} {
		for q, want := range map[string][]string{
			"account=info": {"B"}, "account=b/info": {"B"}, "account=a/info": {"A"}, "": {"B", "A"},
			"account=a/other": {}, "account=c/info": {}, "account=a/info&folder=Sent": {},
		} {
			if got, _ := w.list(c, q); !slices.Equal(got, want) {
				t.Errorf("%s: %s lists %v, want %v", c.name, q, got, want)
			}
		}
		var r accountsResponse
		w.fetchJSON(c, "/api/accounts", &r)
		if len(r.Accounts) != 1 || len(r.Accounts[0].Folders) != 1 || r.Accounts[0].Folders[0].Name != "Sent" ||
			len(r.Shared) != 1 || len(r.Shared[0].Folders) != 1 || r.Shared[0].Folders[0].Name != "INBOX" {
			t.Errorf("%s: accounts %+v", c.name, r)
		}
		var m messageJSON
		w.fetchJSON(c, "/api/messages/"+w.ids["A"], &m)
		if len(m.Locations) != 1 || m.Locations[0].Owner == nil || *m.Locations[0].Owner != "a" {
			t.Errorf("%s: locations of A %+v", c.name, m.Locations)
		}
		w.fetchJSON(c, "/api/messages/"+w.ids["B"], &m)
		if len(m.Locations) != 1 || m.Locations[0].Owner != nil {
			t.Errorf("%s: locations of B %+v", c.name, m.Locations)
		}
	}
	// a's own view is unchanged.
	if got, _ := w.list(w.creds["a"], "account=info"); !slices.Equal(got, []string{"A"}) {
		t.Errorf("a lists %v", got)
	}
	// Locking b suspends b's key, shared accounts included (#389, test 8);
	// a locked owner's shares keep working (decision 7).
	if err := w.st.SetUserLocked(context.Background(), w.users["b"].ID, true); err != nil {
		t.Fatal(err)
	}
	if status, _ := w.fetch(w.creds["b-key"], "/api/messages?account=a/info"); status != http.StatusUnauthorized {
		t.Errorf("locked b's key: %d", status)
	}
	if err := w.st.SetUserLocked(context.Background(), w.users["b"].ID, false); err != nil {
		t.Fatal(err)
	}
	if err := w.st.SetUserLocked(context.Background(), w.users["a"].ID, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.list(w.creds["b-key"], "account=a/info"); !slices.Equal(got, []string{"A"}) {
		t.Errorf("b lists %v of locked a's account", got)
	}
}

// Decision 12 (#389, revision 2 R5): onHold only on the viewer's own
// locations, also for admins; retainUntil on every location.
func TestShareHidesHolds(t *testing.T) {
	w := newShareWorld(t)
	ctx := context.Background()
	a := w.user("a", false)
	x := w.user("x", true)
	w.user("b", false)
	w.account("a", "info")
	w.account("x", "own")
	msg := mail("h", "Held", "Mon, 5 Oct 2026 10:00:00 +0000", "held mail")
	w.put("a/info", "INBOX", "H", msg)
	w.share("a/info", "x")
	w.share("a/info", "b")
	if _, err := w.st.AddRetentionRule(ctx, store.NewRetentionRule{AccountID: w.accounts["a/info"].ID, Years: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.st.AddLegalHold(ctx, store.NewLegalHold{Name: "case", OwnerID: &a.ID}); err != nil {
		t.Fatal(err)
	}
	get := func(c credential) (messageJSON, string) {
		var m messageJSON
		body := w.fetchJSON(c, "/api/messages/"+w.ids["H"], &m)
		return m, string(body)
	}
	m, body := get(w.creds["x"])
	if len(m.Locations) != 1 || m.Locations[0].OnHold != nil || m.Locations[0].RetainUntil == nil ||
		m.OnHold == nil || *m.OnHold || m.RetainUntil == nil || strings.Count(body, "onHold") != 1 {
		t.Errorf("x, shared only: %s", body)
	}
	// a, the owner, is no admin and sees no hold either; a's admin view
	// of holds is the holds page.
	w.put("x/own", "INBOX", "H", msg)
	if _, err := w.st.AddLegalHold(ctx, store.NewLegalHold{Name: "x case", OwnerID: &x.ID}); err != nil {
		t.Fatal(err)
	}
	m, body = get(w.creds["x"])
	if len(m.Locations) != 2 || m.OnHold == nil || !*m.OnHold {
		t.Fatalf("x, also own: %s", body)
	}
	for _, l := range m.Locations {
		own := l.Account == "own"
		if own != (l.OnHold != nil && *l.OnHold) || !own && l.OnHold != nil || own != (l.Owner == nil) {
			t.Errorf("x's location %+v", l)
		}
	}
	for _, c := range []string{"b", "a"} {
		if _, body := get(w.creds[c]); strings.Contains(body, "onHold") {
			t.Errorf("%s sees holds: %s", c, body)
		}
	}
}
