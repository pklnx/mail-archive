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
