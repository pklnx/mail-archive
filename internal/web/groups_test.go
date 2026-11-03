package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store"
)

// groupWorld is a shareWorld with two customer groups: a, b and the admin
// x in "Default", k in "B". a owns info, k owns kb.
type groupWorld struct {
	*shareWorld
	def, b *store.Group
}

func newGroupWorld(t *testing.T) *groupWorld {
	t.Helper()
	w := &groupWorld{shareWorld: newShareWorld(t)}
	w.user("a", false)
	w.user("b", false)
	w.user("x", true)
	k := w.user("k", false)
	w.account("a", "info")
	w.account("k", "kb")
	ctx := context.Background()
	var err error
	if w.def, err = w.st.GetGroupByName(ctx, "Default"); err != nil {
		t.Fatal(err)
	}
	if w.b, err = w.st.CreateGroup(ctx, "B"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.st.SetUserGroup(ctx, k.ID, w.b.ID); err != nil {
		t.Fatal(err)
	}
	return w
}

// sendJSON is send with a JSON body.
func (w *groupWorld) sendJSON(c credential, method, path string, body any) (int, []byte) {
	w.t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, w.srv.URL+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", w.srv.URL)
	if c.cookie != "" {
		req.AddCookie(&http.Cookie{Name: cookieName, Value: c.cookie})
	}
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// groupColumn returns the group_id of the newest entry of action, "NULL"
// without one.
func (w *groupWorld) groupColumn(action string) string {
	w.t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, w.dbURL)
	if err != nil {
		w.t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var g *int64
	if err := conn.QueryRow(ctx, `SELECT group_id FROM audit_log WHERE action = $1 ORDER BY at DESC, id DESC LIMIT 1`, action).Scan(&g); err != nil {
		w.t.Fatalf("entry %s: %v", action, err)
	}
	if g == nil {
		return "NULL"
	}
	return fmt.Sprint(*g)
}

// TestShareProbeAcrossGroups (#476, #470 test 2): a share with a name that
// does not exist and one with a user of another group get the same answer,
// byte for byte, and neither shares nor logs anything. Before customer
// groups the second one shared the account (204).
func TestShareProbeAcrossGroups(t *testing.T) {
	w := newGroupWorld(t)
	ca := w.creds["a"]
	n := len(auditLog(t, w.st))
	s1, unknown := w.send(ca, "PUT", "/api/accounts/info/shares/nosuchuser")
	s2, other := w.send(ca, "PUT", "/api/accounts/info/shares/k")
	if s1 != http.StatusNotFound || s2 != http.StatusNotFound || !bytes.Equal(unknown, other) {
		t.Fatalf("unknown: %d %s; other group: %d %s", s1, unknown, s2, other)
	}
	if !strings.Contains(string(unknown), "no such user in your group") {
		t.Errorf("answer %s", unknown)
	}
	if got := newEntries(t, w.st, n); len(got) != 0 {
		t.Errorf("logged %+v", got)
	}
	shares, err := w.st.AccountShares(context.Background(), w.current("a/info").ID)
	if err != nil || len(shares) != 0 {
		t.Fatalf("shares %v, %v", shares, err)
	}
	if names, _ := w.list(w.creds["k"], ""); len(names) != 0 {
		t.Errorf("k lists %v", names)
	}
	// Inside the group it works as before.
	if status, body := w.send(ca, "PUT", "/api/accounts/info/shares/b"); status != http.StatusNoContent {
		t.Fatalf("share with b: %d %s", status, body)
	}
}

// TestUnshareProbe (#476, #470 test 1): an unshare with a name that does
// not exist and one with an existing user without a share get the same
// answer, byte for byte, and log nothing. Before, the first answered "no
// such user".
func TestUnshareProbe(t *testing.T) {
	w := newGroupWorld(t)
	ca := w.creds["a"]
	n := len(auditLog(t, w.st))
	var bodies [][]byte
	for _, name := range []string{"nosuchuser", "b", "k"} {
		status, body := w.send(ca, "DELETE", "/api/accounts/info/shares/"+name)
		if status != http.StatusNotFound {
			t.Fatalf("unshare %s: %d %s", name, status, body)
		}
		bodies = append(bodies, body)
	}
	if !bytes.Equal(bodies[0], bodies[1]) || !bytes.Equal(bodies[0], bodies[2]) {
		t.Fatalf("answers differ: %s / %s / %s", bodies[0], bodies[1], bodies[2])
	}
	if got := newEntries(t, w.st, n); len(got) != 0 {
		t.Errorf("logged %+v", got)
	}
	// An existing share is found by its name, in any case, and ends.
	w.share("a/info", "b")
	if status, body := w.send(ca, "DELETE", "/api/accounts/info/shares/B"); status != http.StatusNoContent {
		t.Fatalf("unshare b: %d %s", status, body)
	}
	if e := w.lastEntry(n, "unshare"); e.Action != "account.unshare" || details(t, e)["user"] != "b" ||
		details(t, e)["user_id"] != float64(w.users["b"].ID) {
		t.Errorf("entry %+v", e)
	}
	if names, _ := w.list(w.creds["b"], ""); len(names) != 0 {
		t.Errorf("b lists %v after the unshare", names)
	}
}

// TestSharedWithSameGroupOnly (#470, test 14b): a crossing share planted
// past the triggers shows neither in the owner's sharedWith nor as a
// shared account of its reader.
func TestSharedWithSameGroupOnly(t *testing.T) {
	w := newGroupWorld(t)
	w.share("a/info", "b")
	info := w.current("a/info")
	w.sql(fmt.Sprintf(`DO $$ BEGIN
		ALTER TABLE account_shares DISABLE TRIGGER account_shares_same_group;
		INSERT INTO account_shares (account_id, user_id) VALUES (%d, %d);
		ALTER TABLE account_shares ENABLE TRIGGER account_shares_same_group;
	END $$`, info.ID, w.users["k"].ID))
	var ra accountsResponse
	w.fetchJSON(w.creds["a"], "/api/accounts", &ra)
	if len(ra.Accounts) != 1 || len(ra.Accounts[0].SharedWith) != 1 || ra.Accounts[0].SharedWith[0].User != "b" {
		t.Fatalf("a's accounts %+v", ra.Accounts)
	}
	var rk accountsResponse
	w.fetchJSON(w.creds["k"], "/api/accounts", &rk)
	if len(rk.Shared) != 0 {
		t.Fatalf("k sees %+v", rk.Shared)
	}
	if names, _ := w.list(w.creds["k"], "account=a/info"); len(names) != 0 {
		t.Fatalf("k lists %v", names)
	}
}

// TestCreateUserGroupAPI (#470, test 14a): POST /api/users needs a group
// while several exist and puts the user into it.
func TestCreateUserGroupAPI(t *testing.T) {
	w := newGroupWorld(t)
	cx := w.creds["x"]
	n := len(auditLog(t, w.st))
	status, body := w.sendJSON(cx, "POST", "/api/users", map[string]any{"name": "nogroup"})
	if status != http.StatusBadRequest || !strings.Contains(string(body), "choose a group") {
		t.Fatalf("without a group: %d %s", status, body)
	}
	for _, g := range []int64{w.b.ID + 100, 0, -1} {
		status, body = w.sendJSON(cx, "POST", "/api/users", map[string]any{"name": "lost", "group": g})
		if status != http.StatusBadRequest || !strings.Contains(string(body), "unknown group") {
			t.Fatalf("group %d: %d %s", g, status, body)
		}
	}
	if got := newEntries(t, w.st, n); len(got) != 0 {
		t.Errorf("refused creates logged %+v", got)
	}
	status, body = w.sendJSON(cx, "POST", "/api/users", map[string]any{"name": "inb", "group": w.b.ID})
	if status != http.StatusCreated {
		t.Fatalf("in B: %d %s", status, body)
	}
	u, err := w.st.GetUserByName(context.Background(), "inb")
	if err != nil || u.GroupID != w.b.ID {
		t.Fatalf("inb %+v, %v", u, err)
	}
	if got := w.groupColumn("user.add"); got != fmt.Sprint(w.b.ID) {
		t.Errorf("user.add recorded under %s", got)
	}
	// With one group, no group is needed.
	if _, err := w.st.SetUserGroup(context.Background(), w.users["k"].ID, w.def.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.st.SetUserGroup(context.Background(), u.ID, w.def.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.st.DeleteGroup(context.Background(), w.b.ID); err != nil {
		t.Fatal(err)
	}
	if status, body := w.sendJSON(cx, "POST", "/api/users", map[string]any{"name": "single"}); status != http.StatusCreated {
		t.Fatalf("with one group: %d %s", status, body)
	}
	if u, err := w.st.GetUserByName(context.Background(), "single"); err != nil || u.GroupID != w.def.ID {
		t.Fatalf("single %+v, %v", u, err)
	}
}

// TestAuditGroupsOfAdminActions (#470, test 12): the admin x sits in
// group B. Instance-level work (a hold without user or account, its
// release, a deletion run, a failed login of an unknown name) has no
// group; work on a user or account of Default is recorded under Default,
// and x's own login under B.
func TestAuditGroupsOfAdminActions(t *testing.T) {
	w := newGroupWorld(t)
	if _, err := w.st.SetUserGroup(context.Background(), w.users["x"].ID, w.b.ID); err != nil {
		t.Fatal(err)
	}
	cx := w.creds["x"]
	def, b := fmt.Sprint(w.def.ID), fmt.Sprint(w.b.ID)
	hold := func(body map[string]any) int64 {
		t.Helper()
		status, out := w.sendJSON(cx, "POST", "/api/legal-holds", body)
		if status != http.StatusCreated {
			t.Fatalf("hold %v: %d %s", body, status, out)
		}
		var r struct{ ID int64 }
		_ = json.Unmarshal(out, &r)
		return r.ID
	}
	release := func(id int64) {
		t.Helper()
		if status, out := w.sendJSON(cx, "POST", fmt.Sprintf("/api/legal-holds/%d/release", id), map[string]any{"note": "done"}); status != http.StatusNoContent && status != http.StatusOK {
			t.Fatalf("release %d: %d %s", id, status, out)
		}
	}
	all := hold(map[string]any{"name": "everything", "reason": "audit", "all": true})
	if got := w.groupColumn("hold.add"); got != "NULL" {
		t.Errorf("hold over the instance: group %s", got)
	}
	release(all)
	if got := w.groupColumn("hold.release"); got != "NULL" {
		t.Errorf("release of the instance hold: group %s", got)
	}
	ofA := hold(map[string]any{"name": "a's mail", "reason": "case", "user": "a"})
	if got := w.groupColumn("hold.add"); got != def {
		t.Errorf("hold on a: group %s, want %s", got, def)
	}
	release(ofA)
	if got := w.groupColumn("hold.release"); got != def {
		t.Errorf("release of a's hold: group %s, want %s", got, def)
	}
	kb := w.current("k/kb")
	hold(map[string]any{"name": "kb", "reason": "case", "accountId": kb.ID})
	if got := w.groupColumn("hold.add"); got != b {
		t.Errorf("hold on kb: group %s, want %s", got, b)
	}
	info := w.current("a/info")
	if status, out := w.sendJSON(cx, "POST", "/api/retention/rules", map[string]any{"accountId": info.ID, "years": 10}); status != http.StatusCreated {
		t.Fatalf("rule: %d %s", status, out)
	}
	if got := w.groupColumn("retention.rule_add"); got != def {
		t.Errorf("rule on info: group %s, want %s", got, def)
	}
	if status, out := w.sendJSON(cx, "POST", "/api/retention/runs", nil); status >= 300 {
		t.Fatalf("deletion run: %d %s", status, out)
	}
	if got := w.groupColumn("retention.preview"); got != "NULL" {
		t.Errorf("deletion run: group %s", got)
	}
	if status, _ := w.sendJSON(credential{}, "POST", "/api/session", map[string]any{"username": "nosuchuser", "password": "wrong password"}); status != http.StatusUnauthorized {
		t.Fatalf("failed login: %d", status)
	}
	if got := w.groupColumn("auth.login_failed"); got != "NULL" {
		t.Errorf("failed login of an unknown name: group %s", got)
	}
	// A targeted entry takes the target's group, not the admin's.
	if status, out := w.sendJSON(cx, "POST", "/api/users", map[string]any{"name": "newa", "group": w.def.ID}); status != http.StatusCreated {
		t.Fatalf("user in Default: %d %s", status, out)
	}
	if got := w.groupColumn("user.add"); got != def {
		t.Errorf("user.add of newa: group %s, want %s", got, def)
	}
}

// TestGroupRoutesAdminOnly (#470, part 2): a user who is not an admin, and
// an admin's API key, get 403 on every group route before anything is
// looked up: an existing and an unknown id get the same answer, byte for
// byte, and nothing changes or is logged.
func TestGroupRoutesAdminOnly(t *testing.T) {
	w := newGroupWorld(t)
	n := len(auditLog(t, w.st))
	for _, c := range []credential{w.creds["a"], w.creds["a-key"], w.creds["x-key"]} {
		if status, body := w.sendJSON(c, "GET", "/api/groups", nil); status != http.StatusForbidden {
			t.Errorf("GET as %s: %d %s", c.name, status, body)
		}
		if status, body := w.sendJSON(c, "POST", "/api/groups", map[string]any{"name": "C"}); status != http.StatusForbidden {
			t.Errorf("POST as %s: %d %s", c.name, status, body)
		}
		for _, method := range []string{"PATCH", "DELETE"} {
			s1, existing := w.sendJSON(c, method, fmt.Sprintf("/api/groups/%d", w.b.ID), map[string]any{"name": "C"})
			s2, unknown := w.sendJSON(c, method, fmt.Sprintf("/api/groups/%d", w.b.ID+100), map[string]any{"name": "C"})
			if s1 != http.StatusForbidden || s2 != http.StatusForbidden || !bytes.Equal(existing, unknown) {
				t.Errorf("%s as %s: existing %d %s; unknown %d %s", method, c.name, s1, existing, s2, unknown)
			}
		}
		// The group change of PATCH /api/users is admin-only too.
		if status, body := w.sendJSON(c, "PATCH", "/api/users/b", map[string]any{"group": w.b.ID}); status != http.StatusForbidden {
			t.Errorf("PATCH user as %s: %d %s", c.name, status, body)
		}
	}
	if got := newEntries(t, w.st, n); len(got) != 0 {
		t.Errorf("logged %+v", got)
	}
	groups, err := w.st.ListGroups(context.Background())
	if err != nil || len(groups) != 2 || groups[0].Name != "B" || groups[1].Name != "Default" {
		t.Fatalf("groups %+v, %v", groups, err)
	}
	if u, err := w.st.GetUserByName(context.Background(), "b"); err != nil || u.GroupID != w.def.ID {
		t.Fatalf("b %+v, %v", u, err)
	}
}

// TestGroupRoutes (#470, part 2): the admin lists, adds, renames and
// removes groups (the fixture's tester owns two accounts in Default); a
// duplicate name (in any case) is 409, a group with
// users cannot be removed, and each change is logged under its group.
func TestGroupRoutes(t *testing.T) {
	w := newGroupWorld(t)
	cx := w.creds["x"]
	list := func() []groupJSON {
		t.Helper()
		var r struct{ Groups []groupJSON }
		w.fetchJSON(cx, "/api/groups", &r)
		return r.Groups
	}
	got := list()
	if len(got) != 2 || got[0].Name != "B" || got[0].Users != 1 || got[0].Accounts != 1 ||
		got[1].Name != "Default" || got[1].Users != 4 || got[1].Accounts != 3 || got[1].ID != w.def.ID || got[1].CreatedAt.IsZero() {
		t.Fatalf("groups %+v", got)
	}

	status, body := w.sendJSON(cx, "POST", "/api/groups", map[string]any{"name": "  Steuerbüro Meier "})
	var c groupJSON
	if status != http.StatusCreated || json.Unmarshal(body, &c) != nil || c.Name != "Steuerbüro Meier" || c.ID == 0 {
		t.Fatalf("add: %d %s", status, body)
	}
	if g := w.groupColumn("group.add"); g != fmt.Sprint(c.ID) {
		t.Errorf("group.add recorded under %s, want %d", g, c.ID)
	}
	n := len(auditLog(t, w.st))
	for _, name := range []string{"steuerbüro meier", "b"} {
		if status, body := w.sendJSON(cx, "POST", "/api/groups", map[string]any{"name": name}); status != http.StatusConflict {
			t.Errorf("add %q: %d %s", name, status, body)
		}
	}
	for _, in := range []map[string]any{{"name": ""}, {"name": "   "}, {"name": strings.Repeat("x", 101)}, {"name": "a\tb"}, {"title": "C"}} {
		if status, body := w.sendJSON(cx, "POST", "/api/groups", in); status != http.StatusBadRequest {
			t.Errorf("add %v: %d %s", in, status, body)
		}
	}
	path := fmt.Sprintf("/api/groups/%d", c.ID)
	if status, body := w.sendJSON(cx, "PATCH", path, map[string]any{"name": "B"}); status != http.StatusConflict {
		t.Errorf("rename to B: %d %s", status, body)
	}
	for _, p := range []string{fmt.Sprintf("/api/groups/%d", w.b.ID+100), "/api/groups/0", "/api/groups/x"} {
		if status, body := w.sendJSON(cx, "PATCH", p, map[string]any{"name": "D"}); status != http.StatusNotFound {
			t.Errorf("rename %s: %d %s", p, status, body)
		}
		if status, body := w.sendJSON(cx, "DELETE", p, nil); status != http.StatusNotFound {
			t.Errorf("remove %s: %d %s", p, status, body)
		}
	}
	if status, body := w.sendJSON(cx, "DELETE", fmt.Sprintf("/api/groups/%d", w.b.ID), nil); status != http.StatusConflict ||
		!strings.Contains(string(body), "still has users") {
		t.Errorf("remove B with k: %d %s", status, body)
	}
	if got := newEntries(t, w.st, n); len(got) != 0 {
		t.Errorf("refused changes logged %+v", got)
	}

	if status, body := w.sendJSON(cx, "PATCH", path, map[string]any{"name": "Meier"}); status != http.StatusNoContent {
		t.Fatalf("rename: %d %s", status, body)
	}
	if e := w.lastEntry(n, "rename"); e.Action != "group.rename" || details(t, e)["group"] != "Meier" || details(t, e)["from"] != "Steuerbüro Meier" {
		t.Errorf("rename entry %+v", e)
	}
	if g := w.groupColumn("group.rename"); g != fmt.Sprint(c.ID) {
		t.Errorf("group.rename recorded under %s", g)
	}
	if status, body := w.sendJSON(cx, "DELETE", path, nil); status != http.StatusNoContent {
		t.Fatalf("remove: %d %s", status, body)
	}
	if g := w.groupColumn("group.remove"); g != fmt.Sprint(c.ID) {
		t.Errorf("group.remove recorded under %s", g)
	}
	if got := list(); len(got) != 2 {
		t.Errorf("after the remove: %+v", got)
	}
}
