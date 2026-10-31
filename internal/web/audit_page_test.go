package web

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/pklnx/mail-archive/internal/store"
)

func auditEntries(t *testing.T, out map[string]any) []map[string]any {
	t.Helper()
	list, _ := out["entries"].([]any)
	res := make([]map[string]any, 0, len(list))
	for _, e := range list {
		res = append(res, e.(map[string]any))
	}
	return res
}

func TestAuditPage(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	f.addUser("admin", keyPassword, true)
	alice := f.addUser("alice", keyPassword, false)

	// A message only alice has, opened by alice.
	acc := f.imapAccount("mail", alice)
	folder, err := f.st.GetOrCreateFolder(ctx, acc.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("a", 64)
	if _, err := f.st.SaveBatch(ctx, folder.ID, 1, []store.MessageMeta{{SHA256: sha, Size: 1, StoredPath: "x", Subject: "Invoice 42"}},
		[]store.Location{{FolderID: folder.ID, UID: 1}}); err != nil {
		t.Fatal(err)
	}
	open := store.AuditEntry{Actor: store.ActorSession, Action: "message.open"}
	open.SetUser(alice.ID, "alice")
	open.SetTarget(store.TargetMessage, sha, "")
	if err := f.st.Audit(ctx, open); err != nil {
		t.Fatal(err)
	}
	if err := f.st.Audit(ctx, store.AuditEntry{Actor: store.ActorCLI, Action: "data.backup"}); err != nil {
		t.Fatal(err)
	}
	a := f.as()
	a.loginOK("alice", keyPassword)
	f.loginOK("admin", keyPassword)

	// alice sees her own rows: the login and the opened message, with
	// its subject.
	mine := auditEntries(t, a.expect("GET", "/api/audit", nil, 200))
	if len(mine) != 2 || mine[0]["action"] != "auth.login" || mine[1]["action"] != "message.open" {
		t.Fatalf("alice's log %v", mine)
	}
	target := mine[1]["target"].(map[string]any)
	if target["subject"] != "Invoice 42" || target["id"] != sha {
		t.Fatalf("alice's target %v", target)
	}
	if mine[0]["user"] != "alice" || mine[0]["clientIp"] == nil || mine[0]["userAgent"] == nil {
		t.Fatalf("alice's login row %v", mine[0])
	}
	// Reading the log writes nothing.
	if again := auditEntries(t, a.expect("GET", "/api/audit", nil, 200)); len(again) != 2 {
		t.Fatalf("reading wrote rows: %d", len(again))
	}
	a.expect("GET", "/api/audit?user=admin", nil, 403)
	a.expect("GET", "/api/audit?actor=cli", nil, 403)

	// The admin sees everyone, but not the subject of a message only
	// alice has.
	all := auditEntries(t, f.expect("GET", "/api/audit", nil, 200))
	if len(all) != 4 {
		t.Fatalf("admin sees %d rows: %v", len(all), all)
	}
	for _, e := range all {
		if e["action"] == "message.open" {
			if tg := e["target"].(map[string]any); tg["subject"] != nil || tg["id"] != sha {
				t.Fatalf("admin's view of alice's message %v", tg)
			}
		}
	}
	if rows := auditEntries(t, f.expect("GET", "/api/audit?user=alice", nil, 200)); len(rows) != 2 {
		t.Fatalf("user=alice: %v", rows)
	}
	if rows := auditEntries(t, f.expect("GET", "/api/audit?actor=cli", nil, 200)); len(rows) != 1 || rows[0]["action"] != "data.backup" || rows[0]["actor"] != "cli" {
		t.Fatalf("actor=cli: %v", rows)
	}
	if rows := auditEntries(t, f.expect("GET", "/api/audit?action=read&action=export", nil, 200)); len(rows) != 2 {
		t.Fatalf("action groups: %v", rows)
	}
	if rows := auditEntries(t, f.expect("GET", "/api/audit?action=auth.login", nil, 200)); len(rows) != 2 {
		t.Fatalf("exact action: %v", rows)
	}
	f.expect("GET", "/api/audit?user=nobody", nil, 404)

	// Pages of 1 add up to everything, newest first, without repeats.
	var ids []float64
	cursor := ""
	for range 10 {
		out := f.expect("GET", "/api/audit?limit=1&cursor="+url.QueryEscape(cursor), nil, 200)
		for _, e := range auditEntries(t, out) {
			ids = append(ids, e["id"].(float64))
		}
		next, _ := out["nextCursor"].(string)
		if next == "" {
			break
		}
		cursor = next
	}
	if len(ids) != 4 {
		t.Fatalf("paged ids %v", ids)
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] >= ids[i-1] {
			t.Fatalf("not newest first: %v", ids)
		}
	}

	// A period without entries, and bad parameters.
	if rows := auditEntries(t, f.expect("GET", "/api/audit?since=2020-01-01&until=2020-01-31", nil, 200)); len(rows) != 0 {
		t.Fatalf("old period: %v", rows)
	}
	for _, q := range []string{"since=yesterday", "until=2020-13-01", "since=2026-02-01&until=2026-01-01", "action=everything", "actor=root", "cursor=%21", "limit=501"} {
		f.expect("GET", "/api/audit?"+q, nil, 400)
	}
}

// user and actor repeat; an entry matches if its user is one of user or its
// actor is one of actor. The checks run in the order 403, cap, actor, user
// lookups, so a non-admin learns nothing about names.
func TestAuditPageWho(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	f.addUser("admin", keyPassword, true)
	alice := f.addUser("alice", keyPassword, false)
	bob := f.addUser("bob", keyPassword, false)
	keyID := int64(1)
	for _, e := range []store.AuditEntry{
		{Actor: store.ActorSession, Action: "message.open", UserID: &alice.ID, UserName: "alice"},
		{Actor: store.ActorSession, Action: "message.open", UserID: &bob.ID, UserName: "bob"},
		{Actor: store.ActorAPIKey, Action: "message.search", UserID: &bob.ID, UserName: "bob", APIKeyID: &keyID, APIKeyName: "script"},
		{Actor: store.ActorCLI, Action: "data.backup"},
	} {
		if err := f.st.Audit(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	a := f.as()
	a.loginOK("alice", keyPassword)
	f.loginOK("admin", keyPassword)

	// Sorted user/actor/action of each row of an admin's query.
	who := func(q string) string {
		t.Helper()
		var out []string
		for _, e := range auditEntries(t, f.expect("GET", "/api/audit?"+q, nil, 200)) {
			u, _ := e["user"].(string)
			out = append(out, u+"/"+e["actor"].(string)+"/"+e["action"].(string))
		}
		slices.Sort(out)
		return strings.Join(out, " ")
	}
	const (
		aliceRows = "alice/session/auth.login alice/session/message.open"
		bobRows   = "bob/api_key/message.search bob/session/message.open"
		cliRow    = "/cli/data.backup"
	)
	for q, want := range map[string]string{
		"user=alice":                           aliceRows,
		"actor=cli":                            cliRow,
		"user=alice&user=bob":                  aliceRows + " " + bobRows,
		"user=alice&user=ALICE&user=":          aliceRows,
		"user=alice&actor=cli":                 cliRow + " " + aliceRows,
		"user=bob&actor=api_key":               bobRows,
		"actor=cli&actor=api_key&actor=cli":    cliRow + " bob/api_key/message.search",
		"user=alice&actor=api_key&action=read": "alice/session/message.open",
		"user=&actor=":                         cliRow + " admin/session/auth.login " + aliceRows + " " + bobRows,
	} {
		if got := who(q); got != want {
			t.Errorf("%s:\n got  %s\n want %s", q, got, want)
		}
	}

	// Pages of 1 under a multi-value filter return every match once.
	var ids []float64
	cursor := ""
	for range 10 {
		out := f.expect("GET", "/api/audit?user=alice&user=bob&actor=cli&limit=1&cursor="+url.QueryEscape(cursor), nil, 200)
		for _, e := range auditEntries(t, out) {
			ids = append(ids, e["id"].(float64))
		}
		next, _ := out["nextCursor"].(string)
		if next == "" {
			break
		}
		cursor = next
	}
	if len(ids) != 5 {
		t.Fatalf("paged ids %v", ids)
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] >= ids[i-1] {
			t.Fatalf("not newest first or repeated: %v", ids)
		}
	}

	names := func(n int) string {
		vs := url.Values{}
		for i := range n {
			vs.Add("user", fmt.Sprintf("nobody%d", i))
		}
		return vs.Encode()
	}
	// Admins: an unknown name among known ones, the cap before any lookup,
	// the actor check before any lookup.
	f.expect("GET", "/api/audit?user=alice&user=nobody", nil, 404)
	f.expect("GET", "/api/audit?"+names(100), nil, 404)
	f.expect("GET", "/api/audit?"+names(101), nil, 400)
	f.expect("GET", "/api/audit?actor=cli&actor=root", nil, 400)
	f.expect("GET", "/api/audit?actor=root&user=nobody", nil, 400)
	// Non-admins get 403 before anything else; empty values are ignored.
	for _, q := range []string{"user=nobody", "user=alice&user=bob", "actor=root", names(101)} {
		a.expect("GET", "/api/audit?"+q, nil, 403)
	}
	if rows := auditEntries(t, a.expect("GET", "/api/audit?user=&actor=", nil, 200)); len(rows) != 2 {
		t.Fatalf("alice with empty values: %v", rows)
	}
}
