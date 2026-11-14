package web

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/keepia/archive/internal/store"
)

// grantBody is a POST /api/auditor-grants body of a's for auditor.
func grantBody(auditor string) map[string]any {
	return map[string]any{
		"auditor": auditor, "label": "Betriebsprüfung 2026",
		"accounts":   []map[string]any{{"name": "info", "folders": []string{"INBOX"}}},
		"periodFrom": "2026-01-01T00:00:00Z", "periodUntil": "2027-01-01T00:00:00Z",
		"endsAt": time.Now().AddDate(0, 0, 10).UTC().Format(time.RFC3339), "currentPassword": auditorTestPassword,
	}
}

func decodeGrant(t *testing.T, body []byte) grantJSON {
	t.Helper()
	var g grantJSON
	if err := json.Unmarshal(body, &g); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return g
}

func (w *auditorWorld) grants(c credential) []grantJSON {
	w.t.Helper()
	var out struct {
		Grants []grantJSON `json:"grants"`
	}
	w.fetchJSON(c, "/api/auditor-grants", &out)
	return out.Grants
}

func (w *auditorWorld) countGrants() int {
	w.t.Helper()
	var n int
	w.queryRow(&n, `SELECT count(*) FROM auditor_grants`)
	return n
}

// TestAuditorGrantAPI: an owner creates a grant by the auditor's name with
// a step-up, lists it, extends it (step-up again) and revokes it; each
// change writes its entry in the grant's group. Without the password both
// changes are refused and nothing is stored.
func TestAuditorGrantAPI(t *testing.T) {
	w := newAuditorWorld(t)
	a := w.creds["a"]
	n, grants := len(auditLog(t, w.st)), w.countGrants()
	body := grantBody("auda")
	delete(body, "currentPassword")
	if status, out := w.sendJSON(a, "POST", "/api/auditor-grants", body); status != http.StatusUnauthorized {
		t.Errorf("without the password: %d %s", status, out)
	}
	bad := grantBody("auda")
	bad["accounts"] = []map[string]any{{"name": "info", "folders": []string{"Nope"}}}
	if status, out := w.sendJSON(a, "POST", "/api/auditor-grants", bad); status != http.StatusBadRequest {
		t.Errorf("unknown folder: %d %s", status, out)
	}
	if got := newEntries(t, w.st, n); len(got) != 0 || w.countGrants() != grants {
		t.Errorf("refused creations stored something: %+v", got)
	}
	status, out := w.sendJSON(a, "POST", "/api/auditor-grants", grantBody("auda"))
	g := decodeGrant(t, out)
	if status != http.StatusCreated || g.State != "active" || g.Auditor == nil || *g.Auditor != "auda" || g.Owner != "a" ||
		len(g.Accounts) != 1 || g.Accounts[0].Name != "info" || !slices.Equal(g.Accounts[0].Folders, []string{"INBOX"}) {
		t.Fatalf("create: %d %s", status, out)
	}
	e := w.lastEntry(n, "grant")
	if e.Action != store.ActionAuditorGrant || e.TargetName != "auda" || w.groupColumn(store.ActionAuditorGrant) != strconv.FormatInt(w.def.ID, 10) {
		t.Errorf("grant entry %+v", e)
	}
	if d := details(t, e); d["label"] != "Betriebsprüfung 2026" || d[store.DetailAuditorGrant] != float64(g.ID) {
		t.Errorf("grant details %v", d)
	}
	if list := w.grants(a); len(list) != 2 || list[0].ID != g.ID {
		t.Errorf("a's list %+v", list)
	}
	id := "/api/auditor-grants/" + strconv.FormatInt(g.ID, 10)
	if status, out := w.send(a, "GET", id); status != http.StatusOK || decodeGrant(t, out).ID != g.ID {
		t.Errorf("get: %d %s", status, out)
	}
	later := time.Now().AddDate(0, 0, 40).UTC().Truncate(time.Second)
	n = len(auditLog(t, w.st))
	if status, out := w.sendJSON(a, "PATCH", id, map[string]any{"endsAt": later}); status != http.StatusUnauthorized {
		t.Errorf("extend without the password: %d %s", status, out)
	}
	status, out = w.sendJSON(a, "PATCH", id, map[string]any{"endsAt": later, "currentPassword": auditorTestPassword})
	if status != http.StatusOK || !decodeGrant(t, out).EndsAt.Equal(later) {
		t.Fatalf("extend: %d %s", status, out)
	}
	if e := w.lastEntry(n, "extend"); e.Action != store.ActionAuditorExtend {
		t.Errorf("extend entry %+v", e)
	}
	n = len(auditLog(t, w.st))
	if status, out := w.send(a, "DELETE", id); status != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", status, out)
	}
	if e := w.lastEntry(n, "revoke"); e.Action != store.ActionAuditorRevoke || details(t, e)["reason"] != store.RevokeOwner {
		t.Errorf("revoke entry %+v", e)
	}
	if status, out := w.send(a, "DELETE", id); status != http.StatusConflict {
		t.Errorf("revoke twice: %d %s", status, out)
	}
	if status, out := w.sendJSON(a, "PATCH", id, map[string]any{"endsAt": later, "currentPassword": auditorTestPassword}); status != http.StatusConflict {
		t.Errorf("extend a revoked grant: %d %s", status, out)
	}
	if list := w.grants(a); list[0].State != "revoked" || list[0].RevokeReason == nil || *list[0].RevokeReason != store.RevokeOwner {
		t.Errorf("after the revocation %+v", list[0])
	}
}

// TestAuditorNameProbe (group test 1, part 2): a grant to a name that does
// not exist, to b (a member of a's group), to x (an admin of a's group),
// to audb (an auditor of group B) and to k (a member of B) all answer the
// same 404, byte for byte, and store and log nothing.
func TestAuditorNameProbe(t *testing.T) {
	w := newAuditorWorld(t)
	n, grants := len(auditLog(t, w.st)), w.countGrants()
	var first []byte
	for _, name := range []string{"nosuch", "b", "x", "audb", "k"} {
		status, out := w.sendJSON(w.creds["a"], "POST", "/api/auditor-grants", grantBody(name))
		if status != http.StatusNotFound || string(out) != `{"error":"`+errNoAuditorInGroup+`"}`+"\n" {
			t.Errorf("%s: %d %s", name, status, out)
		}
		if first == nil {
			first = out
		} else if string(out) != string(first) {
			t.Errorf("%s: %s differs from %s", name, out, first)
		}
	}
	if got := newEntries(t, w.st, n); len(got) != 0 || w.countGrants() != grants {
		t.Errorf("probes stored or logged something: %+v", got)
	}
	// An account of another owner, in the group or not, is no account.
	for _, acc := range []string{"kb", "nosuch"} {
		body := grantBody("auda")
		body["accounts"] = []map[string]any{{"name": acc}}
		if status, out := w.sendJSON(w.creds["a"], "POST", "/api/auditor-grants", body); status != http.StatusNotFound || !strings.Contains(string(out), errNoSuchAccount) {
			t.Errorf("account %s: %d %s", acc, status, out)
		}
	}
}

// TestAuditorIDProbes (group test 10): the grant-ID routes answer an
// unknown ID and k's grant (group B) with the same 404; so do ?grant=
// from b for a's grant and for an unknown ID, and auda's choice of a
// revoked, a foreign and an unknown grant.
func TestAuditorIDProbes(t *testing.T) {
	w := newAuditorWorld(t)
	kg := w.makeGrant("k", "audb", "kb", nil)
	pair := func(what string, call func(id int64) (int, []byte), ids ...int64) {
		t.Helper()
		var first string
		for i, id := range ids {
			status, out := call(id)
			if status != http.StatusNotFound || !strings.Contains(string(out), errNoSuchGrant) {
				t.Errorf("%s %d: %d %s", what, id, status, out)
			}
			if i == 0 {
				first = string(out)
			} else if string(out) != first {
				t.Errorf("%s: %s differs from %s", what, out, first)
			}
		}
	}
	path := func(id int64) string { return "/api/auditor-grants/" + strconv.FormatInt(id, 10) }
	a := w.creds["a"]
	pair("get", func(id int64) (int, []byte) { return w.send(a, "GET", path(id)) }, 999999, kg)
	pair("extend", func(id int64) (int, []byte) {
		return w.sendJSON(a, "PATCH", path(id), map[string]any{"endsAt": time.Now().AddDate(0, 0, 60), "currentPassword": auditorTestPassword})
	}, 999999, kg)
	pair("revoke", func(id int64) (int, []byte) { return w.send(a, "DELETE", path(id)) }, 999999, kg)
	pair("grant filter", func(id int64) (int, []byte) {
		return w.send(w.creds["b"], "GET", "/api/audit?grant="+strconv.FormatInt(id, 10))
	}, w.grant, 999999, kg)
	revoked := w.makeGrant("a", "auda", "info", nil)
	if err := w.st.RevokeGrant(t.Context(), revoked, nil, nil); err != nil {
		t.Fatal(err)
	}
	pair("choice", func(id int64) (int, []byte) {
		return w.sendJSON(w.creds["auda"], "PUT", "/api/session/grant", map[string]any{"id": id})
	}, revoked, kg, 999999)
	var revokedBy int
	w.queryRow(&revokedBy, `SELECT count(*) FROM auditor_grants WHERE id = $1 AND revoked_at IS NULL`, kg)
	if revokedBy != 1 {
		t.Error("a's probe revoked k's grant")
	}
}

// TestAuditorOwnerView (group test 9): a sees auda's reads and searches
// with their terms, without address or browser, and an admin's revocation
// with "operator" as actor; b (a's group) and k (group B) see none of it;
// the admin sees everything. After auda moved to group B, a's grant list
// no longer names auda, and a's older Log entries keep the name.
func TestAuditorOwnerView(t *testing.T) {
	w := newAuditorWorld(t)
	auda := w.creds["auda"]
	w.list(auda, "q=mail")
	w.fetch(auda, "/api/messages/"+w.ids["M"])
	g2 := w.makeGrant("a", "auda", "info", nil)
	if status, _ := w.send(w.creds["x"], "DELETE", "/api/auditor-grants/"+strconv.FormatInt(g2, 10)); status != http.StatusNoContent {
		t.Fatal("admin revocation")
	}
	entries, raw := w.log(w.creds["a"], "")
	var search, open, revoke *auditEntryJSON
	for i := range entries {
		e := &entries[i]
		switch e.Action {
		case "message.search":
			search = e
		case "message.open":
			open = e
		case store.ActionAuditorRevoke:
			revoke = e
		}
	}
	if search == nil || open == nil || revoke == nil {
		t.Fatalf("a's Log: %s", raw)
	}
	for _, e := range []*auditEntryJSON{search, open} {
		if e.User != "auda" || !e.ViaShare || !e.ViaAuditor || e.ClientIP != "" || e.UserAgent != "" {
			t.Errorf("auda's %s in a's Log: %+v", e.Action, e)
		}
		if _, ok := detailMap(t, e.Details)[store.DetailViaOwners]; ok {
			t.Errorf("%s keeps via_owners: %s", e.Action, e.Details)
		}
	}
	if d := detailMap(t, search.Details); d["q"] != "mail" || d[store.DetailAuditorGrant] != float64(w.grant) {
		t.Errorf("search details %v", d)
	}
	if revoke.User != store.AuditOperator || revoke.ClientIP != "" || !revoke.ViaAuditor || strings.Contains(raw, `"x"`) {
		t.Errorf("admin's revocation in a's Log: %+v", revoke)
	}
	// The grant filter keeps one grant's entries.
	if g, _ := w.log(w.creds["a"], "grant="+strconv.FormatInt(g2, 10)); len(g) != 1 || g[0].Action != store.ActionAuditorRevoke {
		t.Errorf("a's entries of grant %d: %+v", g2, g)
	}
	for _, other := range []string{"b", "k"} {
		_, raw := w.log(w.creds[other], "")
		if strings.Contains(raw, "auda") || strings.Contains(raw, "auditor") {
			t.Errorf("%s's Log shows the case: %s", other, raw)
		}
	}
	admin, _ := w.log(w.creds["x"], "grant="+strconv.FormatInt(w.grant, 10))
	if len(admin) == 0 || slices.ContainsFunc(admin, func(e auditEntryJSON) bool { return e.ViaAuditor || e.User == "auda" && e.ClientIP == "" }) {
		t.Errorf("admin's grant page %+v", admin)
	}
	if status, out := w.send(w.creds["x"], "GET", "/api/audit?grant=999999"); status != http.StatusBadRequest {
		t.Errorf("admin's unknown grant: %d %s", status, out)
	}
	// auda moves to group B: the grant is revoked, a's list no longer
	// names auda, the older entries keep the name.
	status, out := w.sendJSON(w.creds["x"], "PATCH", "/api/users/auda", map[string]any{"group": w.b.ID})
	if status != http.StatusOK || string(out) != `{"grantsRevoked":1,"sharesEnded":0}`+"\n" {
		t.Fatalf("move auda: %d %s", status, out)
	}
	if status, _ := w.fetch(auda, "/api/messages"); status != http.StatusUnauthorized {
		t.Errorf("auda after the move: %d", status)
	}
	list := w.grants(w.creds["a"])
	i := slices.IndexFunc(list, func(g grantJSON) bool { return g.ID == w.grant })
	if i < 0 || list[i].Auditor != nil || list[i].RevokeReason == nil || *list[i].RevokeReason != store.RevokeGroupChange {
		t.Errorf("a's grant after the move %+v", list)
	}
	if _, raw := w.log(w.creds["a"], "action=search"); !strings.Contains(raw, `"user":"auda"`) {
		t.Errorf("a's older entries lost auda's name: %s", raw)
	}
}

// TestAuditorUsersAPI (group test 11, part 2): with two groups an auditor
// needs a group; it is created into it as an auditor; admin and auditor
// together are refused; an auditor cannot become an admin; removing an
// auditor names the revoked grants, which stay in the owner's list.
func TestAuditorUsersAPI(t *testing.T) {
	w := newAuditorWorld(t)
	x := w.creds["x"]
	if status, out := w.sendJSON(x, "POST", "/api/users", map[string]any{"name": "stb", "auditor": true}); status != http.StatusBadRequest || !strings.Contains(string(out), "choose a group") {
		t.Errorf("without a group: %d %s", status, out)
	}
	if status, out := w.sendJSON(x, "POST", "/api/users", map[string]any{"name": "stb", "auditor": true, "admin": true, "group": w.def.ID}); status != http.StatusBadRequest {
		t.Errorf("admin and auditor: %d %s", status, out)
	}
	n := len(auditLog(t, w.st))
	if status, out := w.sendJSON(x, "POST", "/api/users", map[string]any{"name": "stb", "auditor": true, "group": w.def.ID}); status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, out)
	}
	if d := details(t, w.lastEntry(n, "user.add")); d["auditor"] != true {
		t.Errorf("user.add details %v", d)
	}
	var users struct {
		Users []adminUserJSON `json:"users"`
	}
	w.fetchJSON(x, "/api/users", &users)
	i := slices.IndexFunc(users.Users, func(u adminUserJSON) bool { return u.Name == "stb" })
	if i < 0 || !users.Users[i].Auditor || users.Users[i].Admin || users.Users[i].Group.ID != w.def.ID {
		t.Errorf("users %+v", users.Users)
	}
	if status, out := w.sendJSON(x, "PATCH", "/api/users/stb", map[string]any{"admin": true}); status != http.StatusConflict {
		t.Errorf("auditor to admin: %d %s", status, out)
	}
	status, out := w.send(x, "DELETE", "/api/users/auda")
	var removed struct {
		GrantsRevoked []store.RevokedGrant `json:"grantsRevoked"`
	}
	if err := json.Unmarshal(out, &removed); status != http.StatusOK || err != nil || len(removed.GrantsRevoked) != 1 || removed.GrantsRevoked[0].ID != w.grant {
		t.Fatalf("remove auda: %d %s", status, out)
	}
	list := w.grants(w.creds["a"])
	if len(list) != 1 || list[0].Auditor != nil || list[0].RevokeReason == nil || *list[0].RevokeReason != store.RevokeUserRemoved {
		t.Errorf("a's list after the removal %+v", list)
	}
	if status, _ := w.send(x, "DELETE", "/api/users/b"); status != http.StatusNoContent {
		t.Errorf("removing a member: %d", status)
	}
}

// TestAuditorNewRoutesByRole: the new routes as the owner a, the member b,
// k of group B, the admin x and without a login, raw against the route
// table (#393 part 2, risky: access widening); and their answers carry the
// same security headers as every other API answer.
func TestAuditorNewRoutesByRole(t *testing.T) {
	w := newAuditorWorld(t)
	id := "/api/auditor-grants/" + strconv.FormatInt(w.grant, 10)
	for _, c := range []struct {
		who, method, path string
		body              any
		want              int
	}{
		{"nobody", "GET", "/api/auditor-grants", nil, 401},
		{"nobody", "POST", "/api/auditor-grants", map[string]any{}, 401},
		{"nobody", "GET", id, nil, 401},
		{"nobody", "PATCH", id, map[string]any{}, 401},
		{"nobody", "DELETE", id, nil, 401},
		{"nobody", "PUT", "/api/session/grant", map[string]any{"id": w.grant}, 401},
		{"a", "GET", id, nil, 200},
		{"a", "PUT", "/api/session/grant", map[string]any{"id": w.grant}, 404},
		{"b", "GET", id, nil, 404},
		{"b", "DELETE", id, nil, 404},
		{"b", "PUT", "/api/session/grant", map[string]any{"id": w.grant}, 404},
		{"k", "GET", id, nil, 404},
		{"k", "DELETE", id, nil, 404},
		{"k", "PUT", "/api/session/grant", map[string]any{"id": w.grant}, 404},
		{"x", "GET", id, nil, 404},
		{"x", "PUT", "/api/session/grant", map[string]any{"id": w.grant}, 404},
		{"a-key", "GET", "/api/auditor-grants", nil, 403},
		{"a-key", "DELETE", id, nil, 403},
		{"auda", "GET", id, nil, 403},
		{"auda", "PUT", "/api/session/grant", map[string]any{"id": w.grant}, 200},
	} {
		var status int
		var out []byte
		if c.who == "nobody" {
			status, out = w.sendBare(c.method, c.path, c.body)
		} else {
			status, out = w.sendJSON(w.creds[c.who], c.method, c.path, c.body)
		}
		if status != c.want {
			t.Errorf("%s %s as %s: %d %s, want %d", c.method, c.path, c.who, status, out, c.want)
		}
	}
	for _, who := range []string{"b", "k", "x"} {
		if list := w.grants(w.creds[who]); len(list) != 0 {
			t.Errorf("%s lists %+v", who, list)
		}
	}
	if list := w.grants(w.creds["auda"]); len(list) != 1 || list[0].ID != w.grant || len(list[0].Accounts) != 1 {
		t.Errorf("auda lists %+v", list)
	}
	// The grant is still active: nobody but its owner and an admin may end it.
	var active bool
	w.queryRow(&active, `SELECT revoked_at IS NULL FROM auditor_grants WHERE id = $1`, w.grant)
	if !active {
		t.Error("the grant was revoked by someone else")
	}
	headers := func(path string) http.Header {
		req, _ := http.NewRequest("GET", w.srv.URL+path, nil)
		req.AddCookie(&http.Cookie{Name: cookieName, Value: w.creds["a"].cookie})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		h := resp.Header.Clone()
		h.Del("Date")
		h.Del("Content-Length")
		return h
	}
	if got, want := headers("/api/auditor-grants"), headers("/api/accounts"); !headersEqual(got, want) {
		t.Errorf("headers of the grant list\n got %v\nwant %v", got, want)
	}
}

// sendBare sends a request like sendJSON without any login: to the
// server's handler without the fixture's cookie.
func (w *auditorWorld) sendBare(method, path string, body any) (int, []byte) {
	w.t.Helper()
	srv := httptest.NewServer(w.bare)
	defer srv.Close()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, srv.URL+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", srv.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func headersEqual(a, b http.Header) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if !slices.Equal(v, b[k]) {
			return false
		}
	}
	return true
}
