package web

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/store"
)

// auditorAllowlist pins the routes an auditor may call (#393, Security,
// "Auditor route allowlist", and revision 1 point 8). A route added to or
// dropped from the allowlist fails here until this list says so.
var auditorAllowlist = []string{
	"GET /api/session", "POST /api/session", "POST /api/session/2fa",
	"POST " + passkeyLoginBeginPath, "POST " + passkeyLoginFinishPath, "DELETE /api/session",
	"PUT /api/session/grant",
	"PUT " + profilePasswordPath, "PATCH /api/profile",
	"GET /api/profile/2fa", "POST /api/profile/2fa/setup", "POST /api/profile/2fa/confirm",
	"GET /api/profile/passkeys", "POST /api/profile/passkeys/begin", "POST /api/profile/passkeys/finish",
	"DELETE /api/profile/passkeys/{id}",
	"GET /api/auditor-grants",
	"GET /api/audit", "GET /api/status", "GET /api/accounts",
	"GET /api/messages", "GET /api/messages/{sha}", "GET /api/messages/{sha}/conversation",
	"GET /api/messages/{sha}/html", "GET /api/messages/{sha}/raw", "GET /api/messages/{sha}/parts/{n}",
	"/api/",
}

// TestAuditorAllowlistPinned: the route table's auditor routes are exactly
// the pinned list; 2FA removal and new recovery codes stay denied (an
// admin resets an auditor's 2FA), and so do the owner's grant routes other
// than the list.
func TestAuditorAllowlistPinned(t *testing.T) {
	var got []string
	for _, rt := range (&Server{}).routes() {
		if rt.auditor {
			got = append(got, rt.pattern)
		}
	}
	want := slices.Clone(auditorAllowlist)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("auditor routes\n got %q\nwant %q", got, want)
	}
	for _, p := range []string{"DELETE /api/profile/2fa", "POST /api/profile/2fa/recovery-codes", "POST /api/auditor-grants",
		"GET /api/auditor-grants/{id}", "PATCH /api/auditor-grants/{id}", "DELETE /api/auditor-grants/{id}", "POST /api/profile/api-keys"} {
		if slices.Contains(got, p) {
			t.Errorf("%s is allowed to auditors", p)
		}
	}
}

// matrixPath fills a route pattern's parameters for the route matrix.
func matrixPath(pattern, sha string) (method, path string) {
	method, path, ok := strings.Cut(pattern, " ")
	if !ok { // the catch-all
		return http.MethodGet, "/api/no-such-route"
	}
	r := strings.NewReplacer("{name}", "info", "{user}", "b", "{id}", "999999", "{sha}", sha, "{n}", "1")
	return method, r.Replace(path)
}

// auditorReadActions are the entries the allowlisted read routes write,
// each before anything is sent.
var auditorReadActions = map[string]string{
	"GET /api/accounts":                    store.ActionAccountList,
	"GET /api/messages":                    "message.list",
	"GET /api/messages/{sha}":              "message.open",
	"GET /api/messages/{sha}/conversation": "message.conversation",
	"GET /api/messages/{sha}/html":         "message.html",
	"GET /api/messages/{sha}/raw":          "message.raw",
	"GET /api/messages/{sha}/parts/{n}":    "message.part",
}

// TestAuditorRouteMatrix (#393 part 2, "route matrix over routes()"):
// every route of the table that needs a login, called by auda through the
// grant: an allowlisted route answers (no denial, no 5xx other than the
// 503 of a server without a secret key or public URL), and each read writes its entry with the
// grant and its owner, which put it into a's Log; every other route
// answers 403 "not available to auditors", writes auditor.denied with
// method and route (also in a's Log) and leaves the store as it was.
func TestAuditorRouteMatrix(t *testing.T) {
	w := newAuditorWorld(t)
	sha := w.ids["M"]
	denied := `{"error":"` + errNotForAuditors + `"}` + "\n"
	for _, rt := range (&Server{}).routes() {
		if rt.ui || rt.access == accessPublic {
			continue
		}
		c := w.creds["auda"]
		if rt.pattern == "DELETE /api/session" {
			c = w.auditorSession("auda", w.grant) // a logout of its own
		}
		method, path := matrixPath(rt.pattern, sha)
		n := len(auditLog(t, w.st))
		before := w.fingerprint()
		status, body := w.sendJSON(c, method, path, map[string]any{})
		entries := newEntries(t, w.st, n)
		if !rt.auditor {
			if status != http.StatusForbidden || string(body) != denied {
				t.Errorf("%s: %d %s, want the denial", rt.pattern, status, body)
			}
			if len(entries) != 1 || entries[0].Action != store.ActionAuditorDenied {
				t.Errorf("%s: entries %+v, want one auditor.denied", rt.pattern, entries)
				continue
			}
			d := details(t, entries[0])
			if d["route"] != rt.pattern || d["method"] != method {
				t.Errorf("%s: denial details %v", rt.pattern, d)
			}
			if err := w.auditorEntry(entries[0], "auda", w.grant, "a"); err != nil {
				t.Errorf("%s: %v", rt.pattern, err)
			}
			if after := w.fingerprint(); after != before {
				t.Errorf("%s: the store changed:\n%s\n%s", rt.pattern, before, after)
			}
			continue
		}
		if status == http.StatusForbidden && string(body) == denied {
			t.Errorf("%s: an allowlisted route is denied", rt.pattern)
		}
		// The fixture's server has no secret key and no public URL: 2FA
		// setup and passkeys answer 503 for everyone.
		if status >= 500 && (status != http.StatusServiceUnavailable || !strings.Contains(string(body), " on the server")) {
			t.Errorf("%s: %d %s", rt.pattern, status, body)
		}
		action, read := auditorReadActions[rt.pattern]
		if !read {
			continue
		}
		if status != http.StatusOK {
			t.Errorf("%s: %d %s", rt.pattern, status, body)
		}
		i := slices.IndexFunc(entries, func(e store.AuditRecord) bool { return e.Action == action })
		if i < 0 {
			t.Errorf("%s: no %s entry in %+v", rt.pattern, action, entries)
			continue
		}
		if err := w.auditorEntry(entries[i], "auda", w.grant, "a"); err != nil {
			t.Errorf("%s: %v", rt.pattern, err)
		}
	}
	for p := range auditorReadActions {
		if !slices.Contains(auditorAllowlist, p) {
			t.Errorf("read route %s is not allowlisted", p)
		}
	}
}

// TestAuditorKeyRefused: an API key inserted for an auditor directly (the
// store refuses to create one) opens nothing, on an allowlisted read route
// too.
func TestAuditorKeyRefused(t *testing.T) {
	w := newAuditorWorld(t)
	key, hash, hint, err := auth.NewAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	w.sql(`INSERT INTO api_keys (user_id, name, key_hash, hint, scopes) VALUES ($1, 'planted', $2, $3, ARRAY['read'])`,
		w.users["auda"].ID, hash, hint)
	// Two layers: the key login finds no key of an auditor (401), and
	// authenticateKey refuses an auditor's key it is given (403).
	for _, path := range []string{"/api/messages", "/api/accounts", "/api/messages/" + w.ids["M"]} {
		status, body := w.fetch(credential{name: "auda's key", key: key}, path)
		refused := status == http.StatusUnauthorized || status == http.StatusForbidden && string(body) == `{"error":"`+errNotForAuditors+`"}`+"\n"
		if !refused || strings.Contains(string(body), "Matrix") || strings.Contains(string(body), "info") {
			t.Errorf("%s with auda's key: %d %s", path, status, body)
		}
	}
}

// TestAuditorGrantGate: with two active grants (both of a) an unbound
// session may only pick one; a route outside the
// allowlist is still denied, and the denial goes to every grant owner's
// Log. After the choice the session reads through the chosen grant.
func TestAuditorGrantGate(t *testing.T) {
	w := newAuditorWorld(t)
	other := w.makeGrant("a", "auda", "second", nil)
	c := w.auditorSession("auda", 0)
	status, body := w.fetch(c, "/api/session")
	s := decodeSession(t, body)
	if status != http.StatusOK || !s.GrantSelectionRequired || s.Grant != nil || len(s.Grants) != 2 || !s.User.Auditor || !s.User.TwoFactorRequired {
		t.Fatalf("session: %d %s", status, body)
	}
	if status, body := w.fetch(c, "/api/messages"); status != http.StatusForbidden || !strings.Contains(string(body), `"grantSelectionRequired":true`) {
		t.Errorf("gated list: %d %s", status, body)
	}
	n := len(auditLog(t, w.st))
	if status, _ := w.fetch(c, "/api/users"); status != http.StatusForbidden {
		t.Errorf("users: %d", status)
	}
	got := newEntries(t, w.st, n)
	if len(got) != 1 || got[0].Action != store.ActionAuditorDenied || !slices.Equal(viaOwners(t, got[0]), []int64{w.users["a"].ID}) {
		t.Errorf("denial before the choice: %+v", got)
	} else if _, ok := details(t, got[0])[store.DetailAuditorGrant]; ok {
		t.Errorf("denial before the choice names a grant: %s", got[0].Details)
	}
	n = len(auditLog(t, w.st))
	status, body = w.sendJSON(c, "PUT", "/api/session/grant", map[string]any{"id": other})
	s = decodeSession(t, body)
	if status != http.StatusOK || s.GrantSelectionRequired || s.Grant == nil || s.Grant.ID != other {
		t.Fatalf("select: %d %s", status, body)
	}
	got = newEntries(t, w.st, n)
	if len(got) != 1 || got[0].Action != store.ActionAuditorSelect {
		t.Fatalf("select entries %+v", got)
	}
	if err := w.auditorEntry(got[0], "auda", other, "a"); err != nil {
		t.Error(err)
	}
	names, _ := w.list(c, "")
	if !slices.Equal(names, []string{"S"}) {
		t.Errorf("after choosing the second grant auda lists %v", names)
	}
	// Switching back works too; the list follows the grant.
	if status, body := w.sendJSON(c, "PUT", "/api/session/grant", map[string]any{"id": w.grant}); status != http.StatusOK {
		t.Fatalf("switch: %d %s", status, body)
	}
	if names, _ := w.list(c, ""); !slices.Equal(names, []string{"M"}) {
		t.Errorf("after switching back auda lists %v", names)
	}
}

// TestAuditorReadsTheGrantOnly (#393 part 2, "GET /api/messages?account=
// owner/name as an auditor, with folders and period checked", and "GET
// /api/accounts shows granted accounts and folders only, without
// counts"): auda sees M only: not Old (outside the period), not P
// (Private is not granted), not S (a/second is not granted), not K.
func TestAuditorReadsTheGrantOnly(t *testing.T) {
	w := newAuditorWorld(t)
	c := w.creds["auda"]
	for _, q := range []string{"", "account=a/info", "account=a/info&folder=INBOX", "q=mail", "after=2020-01-01"} {
		if names, _ := w.list(c, q); !slices.Equal(names, []string{"M"}) {
			t.Errorf("list %q: %v", q, names)
		}
	}
	for _, q := range []string{"account=a/second", "account=a/info&folder=Private", "account=k/kb", "account=info", "before=2025-12-31", "gone=1"} {
		if names, _ := w.list(c, q); len(names) != 0 {
			t.Errorf("list %q: %v", q, names)
		}
	}
	for _, name := range []string{"Old", "P", "S", "K"} {
		for _, suffix := range []string{"", "/conversation", "/html", "/raw", "/parts/0"} {
			if status, body := w.fetch(c, "/api/messages/"+w.ids[name]+suffix); status != http.StatusNotFound {
				t.Errorf("%s%s: %d %s", name, suffix, status, body)
			}
		}
	}
	var m messageJSON
	w.fetchJSON(c, "/api/messages/"+w.ids["M"], &m)
	if len(m.Locations) != 1 || m.Locations[0].Folder != "INBOX" || m.Locations[0].Owner == nil || *m.Locations[0].Owner != "a" || m.OnHold != nil {
		t.Errorf("locations %+v", m.Locations)
	}
	n := len(auditLog(t, w.st))
	_, raw := w.fetch(c, "/api/accounts")
	want := `{"accounts":[],"shared":[{"key":"a/info","name":"info","owner":"a","kind":"imap","removed":false,"folders":[{"name":"INBOX","delimiter":null}]}],"manage":false}` + "\n"
	if string(raw) != want {
		t.Errorf("accounts\n got %s\nwant %s", raw, want)
	}
	got := newEntries(t, w.st, n)
	if len(got) != 1 || got[0].Action != store.ActionAccountList {
		t.Fatalf("accounts entries %+v", got)
	}
	if err := w.auditorEntry(got[0], "auda", w.grant, "a"); err != nil {
		t.Error(err)
	}
	if v := viaKeys(details(t, got[0])); !slices.Equal(v, []string{"a/info"}) {
		t.Errorf("account.list via %v", v)
	}
	// Every page is logged, plain folder listings and empty pages too.
	for _, q := range []string{"account=a/info&folder=INBOX", "account=a/second"} {
		n := len(auditLog(t, w.st))
		w.list(c, q)
		if got := newEntries(t, w.st, n); len(got) != 1 || got[0].Action != "message.list" || w.auditorEntry(got[0], "auda", w.grant, "a") != nil {
			t.Errorf("list %q: entries %+v", q, got)
		}
	}
	// The status is an empty own view.
	status, body := w.fetch(c, "/api/status")
	if status != http.StatusOK || string(body) != `{"accounts":[],"uniqueMessages":0}`+"\n" {
		t.Errorf("status: %d %s", status, body)
	}
}

// TestAuditorReadFailsClosed: when the audit log cannot be written, an
// auditor's read answers 503 and sends nothing.
func TestAuditorReadFailsClosed(t *testing.T) {
	w := newAuditorWorld(t)
	breakAudit(t, w.dbURL)
	for _, path := range []string{"/api/messages", "/api/accounts", "/api/messages/" + w.ids["M"], "/api/messages/" + w.ids["M"] + "/raw"} {
		status, body := w.fetch(w.creds["auda"], path)
		if status != http.StatusServiceUnavailable || strings.Contains(string(body), "Matrix") || strings.Contains(string(body), "info") {
			t.Errorf("%s: %d %s", path, status, body)
		}
	}
}

// TestAuditorAccessEnds: an expired grant, the owner's revocation and an
// admin's revocation each end the auditor's access with the next request
// (401); the auditor can neither extend nor revoke.
func TestAuditorAccessEnds(t *testing.T) {
	w := newAuditorWorld(t)
	c := w.creds["auda"]
	id := strconv.FormatInt(w.grant, 10)
	for _, rq := range []struct{ method, path string }{{"PATCH", "/api/auditor-grants/" + id}, {"DELETE", "/api/auditor-grants/" + id}} {
		if status, _ := w.sendJSON(c, rq.method, rq.path, map[string]any{"endsAt": "2027-01-01T00:00:00Z"}); status != http.StatusForbidden {
			t.Errorf("auda %s: %d", rq.method, status)
		}
	}
	if status, _ := w.fetch(c, "/api/messages"); status != http.StatusOK {
		t.Fatalf("before: %d", status)
	}
	// Expiry.
	expired := w.makeGrant("a", "auda", "info", nil)
	ce := w.auditorSession("auda", expired)
	w.shiftGrant(expired, -29*24*time.Hour)
	if status, _ := w.fetch(ce, "/api/messages"); status != http.StatusUnauthorized {
		t.Errorf("expired grant: %d", status)
	}
	// The owner revokes.
	if status, body := w.send(w.creds["a"], "DELETE", "/api/auditor-grants/"+id); status != http.StatusNoContent {
		t.Fatalf("owner revokes: %d %s", status, body)
	}
	if status, _ := w.fetch(c, "/api/messages"); status != http.StatusUnauthorized {
		t.Errorf("after the owner's revocation: %d", status)
	}
	// An admin revokes a's other grant.
	g2 := w.makeGrant("a", "auda", "info", nil)
	c2 := w.auditorSession("auda", g2)
	if status, _ := w.fetch(c2, "/api/messages"); status != http.StatusOK {
		t.Fatalf("second grant: %d", status)
	}
	if status, body := w.send(w.creds["x"], "DELETE", "/api/auditor-grants/"+strconv.FormatInt(g2, 10)); status != http.StatusNoContent {
		t.Fatalf("admin revokes: %d %s", status, body)
	}
	if status, _ := w.fetch(c2, "/api/messages"); status != http.StatusUnauthorized {
		t.Errorf("after the admin's revocation: %d", status)
	}
	var reason string
	w.queryRow(&reason, `SELECT revoke_reason FROM auditor_grants WHERE id = $1`, g2)
	if reason != store.RevokeOperator {
		t.Errorf("admin's revocation reason %q", reason)
	}
}
