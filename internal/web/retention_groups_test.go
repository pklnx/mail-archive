package web

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store"
)

// TestRetentionGroupsAPI (#470, part 4): rules, accounts, holds and the
// deletion review carry the customer group; a hold can name a group, which
// must exist and match its user or account; its entries are recorded under
// that group.
func TestRetentionGroupsAPI(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	f.addUser("admin", keyPassword, true)
	alice := f.addUser("alice", keyPassword, false)
	kim := f.addUser("kim", keyPassword, false)
	def, err := f.st.GetGroupByName(ctx, "Default")
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.st.CreateGroup(ctx, "B")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.SetUserGroup(ctx, kim.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	f.imapAccount("work", alice)
	kb := f.imapAccount("kb", kim)
	folder, err := f.st.GetOrCreateFolder(ctx, kb.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	sent := time.Date(2010, 3, 5, 10, 0, 0, 0, time.UTC)
	if _, err := f.st.SaveBatch(ctx, folder.ID, 1, []store.MessageMeta{{SHA256: strings.Repeat("k", 64), Size: 5, StoredPath: "x", SentAt: &sent}},
		[]store.Location{{FolderID: folder.ID, UID: 1, InternalDate: sent}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.AddRetentionRule(ctx, store.NewRetentionRule{AccountID: kb.ID, Years: 1}); err != nil {
		t.Fatal(err)
	}
	f.loginOK("admin", keyPassword)
	refB := fmt.Sprintf(`{"id":%d,"name":"B"}`, b.ID)
	refDef := fmt.Sprintf(`{"id":%d,"name":"Default"}`, def.ID)

	// The group of each item, by the value of its key field.
	groupsOf := func(list any, key string, fields ...string) map[string]string {
		t.Helper()
		out := map[string]string{}
		for _, it := range list.([]any) {
			m := it.(map[string]any)
			for _, fl := range fields {
				out[fmt.Sprint(m[key])+"."+fl] = toJSON(m[fl])
			}
		}
		return out
	}
	check := func(what string, got, want map[string]string) {
		t.Helper()
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: %v, want %v", what, got, want)
		}
	}
	rules := f.expect("GET", "/api/retention/rules", nil, 200)
	if got := toJSON(rules["groups"]); got != "["+refB+","+refDef+"]" {
		t.Errorf("groups %s", got)
	}
	check("rules", groupsOf(rules["rules"], "account", "group"), map[string]string{"kb.group": refB})
	check("accounts", groupsOf(rules["accounts"], "name", "group"), map[string]string{"work.group": refDef, "kb.group": refB})

	// The group must exist and match the hold's user or account.
	if e := f.expect("POST", "/api/legal-holds", map[string]any{"name": "x", "reason": "y", "group": 9999}, 400); e["error"] != "unknown group" {
		t.Errorf("unknown group: %v", e)
	}
	if e := f.expect("POST", "/api/legal-holds", map[string]any{"name": "x", "reason": "y", "group": b.ID, "user": "alice"}, 400); !strings.Contains(fmt.Sprint(e["error"]), "not in the hold's group") {
		t.Errorf("alice in group B: %v", e)
	}
	// A group alone is a scope: no "all" needed.
	held := f.expect("POST", "/api/legal-holds", map[string]any{"name": "Meier audit", "reason": "Betriebsprüfung", "group": b.ID}, 201)
	f.expect("POST", "/api/legal-holds", map[string]any{"name": "alice", "reason": "y", "user": "alice"}, 201)
	check("holds", groupsOf(f.expect("GET", "/api/legal-holds", nil, 200)["holds"], "name", "group", "coveredGroup"), map[string]string{
		"Meier audit.group": refB, "Meier audit.coveredGroup": refB, "alice.group": "null", "alice.coveredGroup": refDef,
	})
	id := itoa(int64(held["id"].(float64)))
	f.expect("POST", "/api/legal-holds/"+id+"/release", map[string]any{"note": "closed"}, 204)

	// The entries of the group hold are recorded under B.
	conn, err := pgx.Connect(ctx, f.dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	for _, action := range []string{"hold.add", "hold.release"} {
		var group *int64
		if err := conn.QueryRow(ctx, `SELECT group_id FROM audit_log WHERE action = $1 AND details->>'name' = 'Meier audit'`, action).Scan(&group); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		if group == nil || *group != b.ID {
			t.Errorf("%s recorded under group %v, want %d", action, group, b.ID)
		}
	}

	// The group stays while a hold names it, also a released one.
	if e := f.expect("DELETE", fmt.Sprintf("/api/groups/%d", b.ID), nil, 409); e["error"] != "the group still has users; move them to another group first" {
		t.Errorf("remove B with kim: %v", e)
	}
	c, err := f.st.CreateGroup(ctx, "C")
	if err != nil {
		t.Fatal(err)
	}
	cHold := f.expect("POST", "/api/legal-holds", map[string]any{"name": "c", "reason": "y", "group": c.ID}, 201)
	f.expect("POST", "/api/legal-holds/"+itoa(int64(cHold["id"].(float64)))+"/release", map[string]any{"note": "closed"}, 204)
	if e := f.expect("DELETE", fmt.Sprintf("/api/groups/%d", c.ID), nil, 409); e["error"] != "a legal hold names the group, so it stays" {
		t.Errorf("remove C named by a released hold: %v", e)
	}

	// The review of a deletion run.
	run := f.expect("POST", "/api/retention/runs", nil, 201)
	review := f.expect("GET", "/api/retention/runs/"+itoa(int64(run["id"].(float64))), nil, 200)
	check("review", groupsOf(review["groups"], "account", "group"), map[string]string{"kb.group": refB})
}
