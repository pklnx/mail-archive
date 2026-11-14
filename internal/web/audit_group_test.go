package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/keepia/archive/internal/store"
)

// GET /api/audit?group= (#470, part 3): one customer group's entries, or
// with none those without a group (from before 1.4.0, or instance-level);
// it intersects with user and actor. Admins only, refused before any
// lookup; the group of each entry is in an admin's page only.
func TestAuditPageGroups(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	f.addUser("admin", keyPassword, true)
	alice := f.addUser("alice", keyPassword, false)
	bob := f.addUser("bob", keyPassword, false)
	carol := f.addUser("carol", keyPassword, false)
	def, err := f.st.GetGroupByName(ctx, "Default")
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.st.CreateGroup(ctx, "Meier GmbH")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.SetUserGroup(ctx, bob.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	self := func(u *store.User, action string) *store.AuditEntry {
		e := store.SelfEntry(store.ActorSession, action)
		e.SetUser(u.ID, u.Name)
		return e
	}
	// carol's read of alice's shared account: part of alice's own Log.
	shared := self(carol, "message.open")
	shared.Detail(store.DetailViaOwners, []int64{alice.ID})
	shared.Detail(store.DetailViaAccounts, []store.ViaAccount{{ID: 1, Owner: alice.ID, Key: "alice/work"}})
	bobKey := self(bob, "message.search")
	keyID := int64(1)
	bobKey.Actor, bobKey.APIKeyID, bobKey.APIKeyName = store.ActorAPIKey, &keyID, "script"
	for _, e := range []*store.AuditEntry{
		self(alice, "message.open"), // Default
		self(bob, "message.open"),   // Meier GmbH
		bobKey,                      // Meier GmbH
		shared,                      // Default
		store.InstanceEntry(store.ActorCLI, "data.backup"), // none
	} {
		if err := f.st.Audit(ctx, *e); err != nil {
			t.Fatal(err)
		}
	}
	// An entry of bob from before 1.4.0: no group.
	conn, err := pgx.Connect(ctx, f.dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, `INSERT INTO audit_log (actor, user_id, user_name, action, details) VALUES ('session', $1, 'bob', 'message.list', '{}')`, bob.ID); err != nil {
		t.Fatal(err)
	}
	a := f.as()
	a.loginOK("alice", keyPassword)
	f.loginOK("admin", keyPassword)

	// Sorted user/actor/action/group of each row of an admin's query.
	who := func(q string) string {
		t.Helper()
		var out []string
		for _, e := range auditEntries(t, f.expect("GET", "/api/audit?"+q, nil, 200)) {
			u, _ := e["user"].(string)
			g := "-"
			if gr, ok := e["group"].(map[string]any); ok {
				g = gr["name"].(string) + "#" + strconv.FormatFloat(gr["id"].(float64), 'f', 0, 64)
			}
			out = append(out, u+"/"+e["actor"].(string)+"/"+e["action"].(string)+"/"+g)
		}
		slices.Sort(out)
		return strings.Join(out, " ")
	}
	dg, bg := "Default#"+strconv.FormatInt(def.ID, 10), "Meier GmbH#"+strconv.FormatInt(b.ID, 10)
	bobRows := "bob/api_key/message.search/" + bg + " bob/session/message.open/" + bg
	noneRows := "/cli/data.backup/- bob/session/message.list/-"
	defRows := "admin/session/auth.login/" + dg + " alice/session/auth.login/" + dg + " alice/session/message.open/" + dg +
		" carol/session/message.open/" + dg
	gq := func(id int64) string { return "group=" + strconv.FormatInt(id, 10) }
	for q, want := range map[string]string{
		gq(b.ID):                                        bobRows,
		gq(def.ID):                                      defRows,
		"group=none":                                    noneRows,
		gq(b.ID) + "&user=alice":                        "",
		gq(b.ID) + "&user=bob":                          bobRows,
		gq(b.ID) + "&actor=api_key":                     "bob/api_key/message.search/" + bg,
		gq(b.ID) + "&user=alice&actor=cli":              "",
		gq(def.ID) + "&user=alice":                      "alice/session/auth.login/" + dg + " alice/session/message.open/" + dg,
		"group=none&user=bob":                           "bob/session/message.list/-",
		"group=none&actor=cli":                          "/cli/data.backup/-",
		"group=none&action=read":                        "",
		gq(b.ID) + "&action=read":                       "bob/session/message.open/" + bg,
		gq(b.ID) + "&since=2020-01-01&until=2020-01-02": "",
	} {
		if got := who(q); got != want {
			t.Errorf("%s:\n got  %s\n want %s", q, got, want)
		}
	}

	// Pages of 1 of one group return each of its entries once, newest first.
	var ids []float64
	cursor := ""
	for range 10 {
		out := f.expect("GET", "/api/audit?"+gq(b.ID)+"&limit=1&cursor="+url.QueryEscape(cursor), nil, 200)
		for _, e := range auditEntries(t, out) {
			ids = append(ids, e["id"].(float64))
		}
		next, _ := out["nextCursor"].(string)
		if next == "" {
			break
		}
		cursor = next
	}
	if len(ids) != 2 || ids[1] >= ids[0] {
		t.Fatalf("paged ids %v", ids)
	}

	// Admins: a malformed value is 400, and so is an id that neither a
	// group nor an entry has.
	for _, q := range []string{"group=abc", "group=0", "group=-1", "group=1.5", "group=NONE"} {
		if out := f.expect("GET", "/api/audit?"+q, nil, 400); out["error"] != "group must be a group id or none" {
			t.Errorf("%s: %v", q, out)
		}
	}
	if out := f.expect("GET", "/api/audit?group=999999", nil, 400); out["error"] != "unknown group" {
		t.Errorf("unknown group: %v", out)
	}
	// A group without entries yet is selectable: an empty page.
	fresh, err := f.st.CreateGroup(ctx, "Fresh AG")
	if err != nil {
		t.Fatal(err)
	}
	if rows := auditEntries(t, f.expect("GET", "/api/audit?"+gq(fresh.ID), nil, 200)); len(rows) != 0 {
		t.Errorf("new group: %v", rows)
	}
	// A removed group's entries keep its id: they stay selectable (an
	// offboarded customer's log), named as removed, without a name.
	gone, err := f.st.CreateGroup(ctx, "Gone GmbH")
	if err != nil {
		t.Fatal(err)
	}
	dave, err := f.st.CreateUserInGroup(ctx, store.NewUser{Name: "dave", PasswordHash: "h", GroupID: gone.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.Audit(ctx, *self(dave, "message.open")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.SetUserGroup(ctx, dave.ID, def.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.st.DeleteGroup(ctx, gone.ID); err != nil {
		t.Fatal(err)
	}
	goneRows := auditEntries(t, f.expect("GET", "/api/audit?"+gq(gone.ID), nil, 200))
	if len(goneRows) != 1 || goneRows[0]["user"] != "dave" || goneRows[0]["action"] != "message.open" {
		t.Fatalf("removed group: %v", goneRows)
	}
	if g := goneRows[0]["group"].(map[string]any); g["id"] != float64(gone.ID) || g["removed"] != true || g["name"] != nil {
		t.Errorf("removed group's entry: %v", g)
	}
	for _, e := range auditEntries(t, f.expect("GET", "/api/audit?"+gq(b.ID), nil, 200)) {
		if g := e["group"].(map[string]any); g["removed"] != nil {
			t.Errorf("a current group marked removed: %v", g)
		}
	}

	// Non-admins get the same 403 for every group value, before any
	// lookup: an existing group, an unknown one, none and nonsense.
	raw := func(c *authFixture, q string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest("GET", c.srv.URL+"/api/audit?"+q, http.NoBody)
		resp, err := c.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	status, want := raw(a, gq(b.ID))
	if status != 403 {
		t.Fatalf("alice with group: %d %s", status, want)
	}
	for _, q := range []string{gq(def.ID), gq(gone.ID), "group=999999", "group=none", "group=abc", gq(b.ID) + "&user=nobody"} {
		if status, body := raw(a, q); status != 403 || body != want {
			t.Errorf("alice %s: %d %s, want 403 %s", q, status, body, want)
		}
	}

	// alice's own page, with carol's read of her shared account: no row
	// has a group.
	mine := auditEntries(t, a.expect("GET", "/api/audit", nil, 200))
	viaShare := false
	for _, e := range mine {
		if _, ok := e["group"]; ok {
			t.Errorf("alice sees a group: %v", e)
		}
		viaShare = viaShare || e["viaShare"] == true
	}
	if len(mine) != 3 || !viaShare {
		t.Fatalf("alice's log %v", mine)
	}
	if _, body := raw(a, ""); strings.Contains(body, `"group"`) || strings.Contains(body, "Default") {
		t.Errorf("alice's page names a group: %s", body)
	}

	// The store gives the group only to an admin's page (All).
	day := func(n int) time.Time { return f.clock.Now().AddDate(0, 0, n) }
	for _, all := range []bool{false, true} {
		recs, err := f.st.ListAudit(ctx, store.AuditPage{Viewer: alice.ID, All: all, Since: day(-1), Until: day(1), Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		withGroup := slices.ContainsFunc(recs, func(r store.AuditRecord) bool { return r.GroupID != nil })
		if withGroup != all {
			t.Errorf("All=%v: records with a group: %v", all, withGroup)
		}
	}

	// The store refuses a group filter on a page that is not an admin's,
	// and a group together with none.
	for _, p := range []store.AuditPage{
		{Viewer: alice.ID, GroupID: &b.ID, Since: day(-1), Until: day(1), Limit: 10},
		{Viewer: alice.ID, NoGroup: true, Since: day(-1), Until: day(1), Limit: 10},
		{Viewer: alice.ID, All: true, GroupID: &b.ID, NoGroup: true, Since: day(-1), Until: day(1), Limit: 10},
	} {
		if _, err := f.st.ListAudit(ctx, p); !errors.Is(err, store.ErrAuditGroupFilter) {
			t.Errorf("ListAudit %+v: %v", p, err)
		}
	}
}
