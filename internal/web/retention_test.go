package web

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/store"
)

func TestRetentionNeedsAdmin(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("admin", keyPassword, true)
	f.addUser("kim", keyPassword, false)
	f.loginOK("kim", keyPassword)
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/retention/rules", nil},
		{"POST", "/api/retention/rules", map[string]any{"accountId": 1, "years": 10}},
		{"DELETE", "/api/retention/rules/1", nil},
		{"GET", "/api/legal-holds", nil},
		{"POST", "/api/legal-holds", map[string]any{"name": "x", "reason": "y", "all": true}},
		{"POST", "/api/legal-holds/1/release", map[string]any{"note": "z"}},
	} {
		f.expect(c.method, c.path, c.body, 403)
	}
}

func TestRetentionRulesAPI(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	f.addUser("admin", keyPassword, true)
	alice := f.addUser("alice", keyPassword, false)
	acc := f.imapAccount("work", alice)
	if _, err := f.st.GetOrCreateFolder(ctx, acc.ID, "INBOX"); err != nil {
		t.Fatal(err)
	}
	f.loginOK("admin", keyPassword)

	list := f.expect("GET", "/api/retention/rules", nil, 200)
	if s := toJSON(list); !strings.Contains(s, `"rules":[]`) || !strings.Contains(s, `"name":"work"`) ||
		!strings.Contains(s, `"folders":["INBOX"]`) || !strings.Contains(s, `"owner":"alice"`) || list["minYears"] != float64(6) {
		t.Fatalf("empty list %s", s)
	}

	// Under 6 years needs force; a bad time zone and an unknown account
	// are refused.
	f.expect("POST", "/api/retention/rules", map[string]any{"accountId": acc.ID, "years": 2}, 400)
	f.expect("POST", "/api/retention/rules", map[string]any{"accountId": acc.ID, "years": 8, "timeZone": "Mars/Olympus"}, 400)
	f.expect("POST", "/api/retention/rules", map[string]any{"accountId": 9999, "years": 8}, 400)
	created := f.expect("POST", "/api/retention/rules", map[string]any{"accountId": acc.ID, "years": 8, "note": "§147 AO"}, 201)
	f.expect("POST", "/api/retention/rules", map[string]any{"accountId": acc.ID, "years": 10}, 409)
	f.expect("POST", "/api/retention/rules", map[string]any{"accountId": acc.ID, "folder": "News", "years": 1, "force": true}, 201)

	s := toJSON(f.expect("GET", "/api/retention/rules", nil, 200))
	if !strings.Contains(s, `"years":8`) || !strings.Contains(s, `"note":"§147 AO"`) || !strings.Contains(s, `"createdBy":"admin"`) ||
		!strings.Contains(s, `"timeZone":"Europe/Berlin"`) || !strings.Contains(s, `"folder":"News"`) {
		t.Fatalf("list %s", s)
	}
	id := itoa(int64(created["id"].(float64)))
	f.expect("DELETE", "/api/retention/rules/"+id, nil, 204)
	f.expect("DELETE", "/api/retention/rules/"+id, nil, 404)

	entries, err := f.st.ListAudit(ctx, store.AuditPage{
		All: true, Actions: []string{"retention"}, Since: time.Now().Add(-time.Hour), Until: time.Now().Add(time.Hour), Limit: 10,
	})
	if err != nil || len(entries) != 3 || entries[0].Action != "retention.rule_remove" || entries[2].TargetName != "work" {
		t.Fatalf("audit %+v %v", entries, err)
	}
}

func TestLegalHoldsAPI(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	f.addUser("admin", keyPassword, true)
	alice := f.addUser("alice", keyPassword, false)
	acc := f.imapAccount("work", alice)
	folder, err := f.st.GetOrCreateFolder(ctx, acc.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("a", 64)
	if _, err := f.st.SaveBatch(ctx, folder.ID, 1, []store.MessageMeta{{SHA256: sha, Size: 1, StoredPath: "x", Subject: "Falcon contract"}},
		[]store.Location{{FolderID: folder.ID, UID: 1}}); err != nil {
		t.Fatal(err)
	}
	f.loginOK("admin", keyPassword)

	// Without any condition, a hold needs all.
	f.expect("POST", "/api/legal-holds", map[string]any{"name": "everything", "reason": "audit"}, 400)
	f.expect("POST", "/api/legal-holds", map[string]any{"name": "", "reason": "audit", "all": true}, 400)
	f.expect("POST", "/api/legal-holds", map[string]any{"name": "x", "reason": "audit", "user": "nobody"}, 400)
	all := f.expect("POST", "/api/legal-holds", map[string]any{"name": "everything", "reason": "audit", "all": true}, 201)
	if _, ok := all["matched"]; ok {
		t.Errorf("matched without a filter: %v", all)
	}
	filtered := f.expect("POST", "/api/legal-holds", map[string]any{
		"name": "Falcon", "reason": "lawsuit", "user": "alice", "accountId": acc.ID,
		"filter": map[string]any{"q": "falcon"}, "since": "2019-01-01T00:00:00Z",
	}, 201)
	if filtered["matched"] != float64(1) {
		t.Fatalf("filter hold %v", filtered)
	}

	s := toJSON(f.expect("GET", "/api/legal-holds", nil, 200))
	if !strings.Contains(s, `"name":"Falcon"`) || !strings.Contains(s, `"user":"alice"`) || !strings.Contains(s, `"account":"work"`) ||
		!strings.Contains(s, `"filter":{"q":"falcon"}`) || !strings.Contains(s, `"matched":1`) {
		t.Fatalf("list %s", s)
	}

	id := itoa(int64(all["id"].(float64)))
	f.expect("POST", "/api/legal-holds/"+id+"/release", map[string]any{"note": ""}, 400)
	f.expect("POST", "/api/legal-holds/"+id+"/release", map[string]any{"note": "closed"}, 204)
	f.expect("POST", "/api/legal-holds/"+id+"/release", map[string]any{"note": "again"}, 409)
	f.expect("POST", "/api/legal-holds/999/release", map[string]any{"note": "x"}, 404)
	if s := toJSON(f.expect("GET", "/api/legal-holds", nil, 200)); strings.Contains(s, `"everything"`) {
		t.Fatalf("released hold listed %s", s)
	}
	if s := toJSON(f.expect("GET", "/api/legal-holds?released=1", nil, 200)); !strings.Contains(s, `"releaseNote":"closed"`) ||
		!strings.Contains(s, `"releasedBy":"admin"`) {
		t.Fatalf("with released %s", s)
	}

	// A user named by a hold cannot be removed, even without accounts.
	f.addUser("carol", keyPassword, false)
	f.expect("POST", "/api/legal-holds", map[string]any{"name": "carol", "reason": "audit", "user": "carol"}, 201)
	body := f.expect("DELETE", "/api/users/carol", nil, 409)
	if !strings.Contains(body["error"].(string), "legal hold") {
		t.Fatalf("remove held user: %v", body)
	}
}

func TestDeletionRunsAPI(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	f.addUser("admin", keyPassword, true)
	alice := f.addUser("alice", keyPassword, false)
	acc := f.imapAccount("work", alice)
	folder, err := f.st.GetOrCreateFolder(ctx, acc.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	sent := time.Date(2010, 3, 5, 10, 0, 0, 0, time.UTC)
	sha := strings.Repeat("a", 64)
	if _, err := f.st.SaveBatch(ctx, folder.ID, 1, []store.MessageMeta{{SHA256: sha, Size: 5, StoredPath: "x", Subject: "secret", SentAt: &sent}},
		[]store.Location{{FolderID: folder.ID, UID: 1, InternalDate: sent}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.AddRetentionRule(ctx, store.NewRetentionRule{AccountID: acc.ID, Years: 1}); err != nil {
		t.Fatal(err)
	}
	k := f.as()
	k.loginOK("alice", keyPassword)
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/retention/runs"}, {"POST", "/api/retention/runs"}, {"GET", "/api/retention/runs/1"},
		{"POST", "/api/retention/runs/1/confirm"}, {"POST", "/api/retention/runs/1/cancel"},
	} {
		k.expect(c.method, c.path, map[string]any{}, 403)
	}
	f.loginOK("admin", keyPassword)

	run := f.expect("POST", "/api/retention/runs", nil, 201)
	if run["state"] != "proposed" || run["candidates"] != float64(1) || run["createdBy"] != "admin" {
		t.Fatalf("proposed %v", run)
	}
	f.expect("POST", "/api/retention/runs", nil, 409)
	id := itoa(int64(run["id"].(float64)))
	review := toJSON(f.expect("GET", "/api/retention/runs/"+id, nil, 200))
	if !strings.Contains(review, `"account":"work"`) || !strings.Contains(review, `"year":2011`) || !strings.Contains(review, sha) ||
		strings.Contains(review, "secret") {
		t.Fatalf("review %s", review)
	}
	f.expect("POST", "/api/retention/runs/"+id+"/confirm", map[string]any{"count": 2}, 400)
	f.expect("POST", "/api/retention/runs/"+id+"/confirm", map[string]any{"count": 1}, 202)
	f.expect("POST", "/api/retention/runs/"+id+"/cancel", nil, 409)
	f.expect("GET", "/api/retention/runs/999", nil, 404)
	if s := toJSON(f.expect("GET", "/api/retention/runs", nil, 200)); !strings.Contains(s, `"state":"confirmed"`) || !strings.Contains(s, `"confirmedBy":"admin"`) {
		t.Fatalf("list %s", s)
	}
	entries, err := f.st.ListAudit(ctx, store.AuditPage{
		All: true, Actions: []string{"retention"}, Since: time.Now().Add(-time.Hour), Until: time.Now().Add(time.Hour), Limit: 10,
	})
	if err != nil || len(entries) != 2 || entries[0].Action != "retention.confirm" || entries[1].Action != "retention.preview" {
		t.Fatalf("audit %+v %v", entries, err)
	}
}
