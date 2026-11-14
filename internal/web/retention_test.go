package web

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/keepia/archive/internal/store"
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
		{"POST", "/api/legal-holds", map[string]any{"name": "x", "reason": "y", "group": 1}},
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
	admin := f.addUser("admin", keyPassword, true)
	alice := f.addUser("alice", keyPassword, false)
	acc := f.imapAccount("work", alice)
	own := f.imapAccount("personal", admin)
	save := func(a *store.Account, uid uint32, sha, subject string) {
		t.Helper()
		folder, err := f.st.GetOrCreateFolder(ctx, a.ID, "INBOX")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.st.SaveBatch(ctx, folder.ID, uid, []store.MessageMeta{{SHA256: sha, Size: 1, StoredPath: "x", Subject: subject}},
			[]store.Location{{FolderID: folder.ID, UID: uid}}); err != nil {
			t.Fatal(err)
		}
	}
	save(acc, 1, strings.Repeat("a", 64), "Falcon contract")
	f.loginOK("admin", keyPassword)

	// Without any condition, a hold needs all.
	f.expect("POST", "/api/legal-holds", map[string]any{"name": "everything", "reason": "audit"}, 400)
	f.expect("POST", "/api/legal-holds", map[string]any{"name": "", "reason": "audit", "all": true}, 400)
	f.expect("POST", "/api/legal-holds", map[string]any{"name": "x", "reason": "audit", "user": "nobody"}, 400)
	all := f.expect("POST", "/api/legal-holds", map[string]any{"name": "everything", "reason": "audit", "all": true}, 201)
	if _, ok := all["matched"]; ok {
		t.Errorf("matched without a filter: %v", all)
	}

	// A filter over mail that is not the admin's is the operator's, on the
	// command line (#538): refused with a code, nothing stored or
	// recorded. Holds without a filter on the same scopes still work.
	g, err := f.st.GetGroupByName(ctx, "Default")
	if err != nil {
		t.Fatal(err)
	}
	nobody := f.imapAccount("shared", nil)
	falcon := map[string]any{"q": "falcon"}
	for name, scope := range map[string]map[string]any{
		"alice and her account": {"user": "alice", "accountId": acc.ID, "since": "2019-01-01T00:00:00Z"},
		"alice":                 {"user": "alice"},
		"alice's account":       {"accountId": acc.ID},
		"account without owner": {"accountId": nobody.ID},
		"group":                 {"group": g.ID},
		"instance":              {},
		"instance, confirmed":   {"all": true},
	} {
		body := map[string]any{"name": "Falcon " + name, "reason": "lawsuit", "filter": falcon}
		for k, v := range scope {
			body[k] = v
		}
		if out := f.expect("POST", "/api/legal-holds", body, 403); out["code"] != "filter_hold_cli" ||
			out["error"] != "A hold with a search filter over other users' mail is added by the operator: ./ma hold add --query" {
			t.Errorf("filter hold on %s: %v", name, out)
		}
		// The filter needs only one of its fields to count.
		body["filter"] = map[string]any{"hasAttachment": true}
		if out := f.expect("POST", "/api/legal-holds", body, 403); out["code"] != "filter_hold_cli" {
			t.Errorf("attachment filter hold on %s: %v", name, out)
		}
		delete(body, "filter")
		body["name"] = "plain " + name
		body["all"] = true
		f.expect("POST", "/api/legal-holds", body, 201)
	}
	holds, err := f.st.LegalHolds(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range holds {
		if h.Filter != nil || strings.HasPrefix(h.Name, "Falcon") {
			t.Errorf("refused hold stored: %+v", h)
		}
	}
	entries, err := f.st.ListAudit(ctx, store.AuditPage{
		All: true, Actions: []string{"hold.add"}, Since: time.Now().Add(-time.Hour), Until: time.Now().Add(time.Hour), Limit: 100,
	})
	if err != nil || len(entries) != len(holds) {
		t.Fatalf("hold.add entries %d for %d holds: %v", len(entries), len(holds), err)
	}
	for _, e := range entries {
		if strings.Contains(string(e.Details), `"Falcon`) {
			t.Errorf("refused hold recorded: %+v", e)
		}
	}

	// The operator's filter hold on alice's account, added on the command
	// line and not matched yet: the web UI matches only the hold it adds,
	// never this one, and never counts it.
	save(acc, 2, strings.Repeat("c", 64), "Falcon appendix")
	cli, err := f.st.AddLegalHold(ctx, store.NewLegalHold{Name: "operator", AccountID: &acc.ID, Filter: &store.HoldFilter{Query: "falcon"}})
	if err != nil {
		t.Fatal(err)
	}
	matchedAt := func(id int64) *time.Time {
		t.Helper()
		holds, err := f.st.LegalHolds(ctx, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range holds {
			if h.ID == id {
				return h.MatchedAt
			}
		}
		t.Fatalf("no active hold %d", id)
		return nil
	}

	// The count of a new hold on the admin's own account is its own. The
	// hold names the admin too, so it covers the account only while the
	// admin owns it.
	save(own, 1, strings.Repeat("b", 64), "Falcon minutes")
	ownHold := f.expect("POST", "/api/legal-holds", map[string]any{"name": "own account", "reason": "r", "accountId": own.ID, "filter": falcon}, 201)
	if ownHold["matched"] != float64(1) {
		t.Errorf("hold on the admin's own account %v", ownHold)
	}
	if at := matchedAt(cli); at != nil {
		t.Errorf("adding a hold in the web UI matched the operator's hold at %v", at)
	}
	ownUser := f.expect("POST", "/api/legal-holds", map[string]any{"name": "own user", "reason": "r", "user": "admin", "filter": falcon}, 201)
	if ownUser["matched"] != float64(1) {
		t.Errorf("hold on the admin %v", ownUser)
	}
	ownID := int64(ownHold["id"].(float64))
	scope, err := f.st.LegalHoldScope(ctx, ownID)
	if err != nil || scope.OwnerID == nil || *scope.OwnerID != admin.ID || scope.AccountID == nil || *scope.AccountID != own.ID {
		t.Fatalf("own account filter hold names %+v (%v), want the admin and the account", scope, err)
	}
	// So the operator cannot move the account to alice while it is active
	// (./ma account move): the admin's filter never runs over her mail.
	ownAcc, err := f.st.GetAccount(ctx, own.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetAccountOwner(ctx, ownAcc.Ref(), alice.ID, false); !errors.Is(err, store.ErrOnHold) {
		t.Fatalf("moved the account under the admin's filter hold: %v", err)
	}

	matched := map[string]string{}
	for _, h := range f.expect("GET", "/api/legal-holds", nil, 200)["holds"].([]any) {
		m := h.(map[string]any)
		v, ok := m["matched"]
		if !ok {
			t.Errorf("hold %v without matched", m["name"])
		}
		if v != nil {
			matched[m["name"].(string)] = toJSON(v)
		}
	}
	if want := map[string]string{"own account": "1", "own user": "1"}; toJSON(matched) != toJSON(want) {
		t.Errorf("matched in the list %v, want %v", matched, want)
	}

	// Released, the holds no longer keep the account with the admin.
	for _, id := range []int64{ownID, int64(ownUser["id"].(float64))} {
		f.expect("POST", "/api/legal-holds/"+itoa(id)+"/release", map[string]any{"note": "moving"}, 204)
	}
	if ownAcc, err = f.st.GetAccount(ctx, own.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetAccountOwner(ctx, ownAcc.Ref(), alice.ID, false); err != nil {
		t.Fatalf("move after the release: %v", err)
	}

	id := itoa(int64(all["id"].(float64)))
	f.expect("POST", "/api/legal-holds/"+id+"/release", map[string]any{"note": ""}, 400)
	f.expect("POST", "/api/legal-holds/"+id+"/release", map[string]any{"note": "closed"}, 204)
	// Releasing it again answers a fixed text and a code, not the store's.
	if again := f.expect("POST", "/api/legal-holds/"+id+"/release", map[string]any{"note": "again"}, 409); again["error"] != "this legal hold is already released" ||
		again["code"] != "released" {
		t.Fatalf("release twice: %v", again)
	}
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
	carolHold := f.expect("POST", "/api/legal-holds", map[string]any{"name": "carol", "reason": "audit", "user": "carol"}, 201)
	body := f.expect("DELETE", "/api/users/carol", nil, 409)
	if !strings.Contains(body["error"].(string), "legal hold") || body["code"] != "on_hold" ||
		toJSON(body["holdIds"]) != toJSON([]any{carolHold["id"]}) || strings.Contains(body["error"].(string), "lock the user instead") {
		t.Fatalf("remove held user: %v", body)
	}

	// Another hold that cannot be matched no longer withholds the new
	// hold's count: only the new hold is matched. A filter of the wrong
	// type passes the database's check and fails when it is read.
	mine := f.imapAccount("mine", admin)
	save(mine, 1, strings.Repeat("d", 64), "Falcon draft")
	f.exec(`INSERT INTO legal_holds (name, reason, filter) VALUES ('broken', 'r', '{"q": 5}')`)
	if h := f.expect("POST", "/api/legal-holds", map[string]any{"name": "own again", "reason": "r", "accountId": mine.ID, "filter": falcon}, 201); h["matched"] != float64(1) {
		t.Errorf("hold beside a broken one %v", h)
	}
	f.exec(`UPDATE legal_holds SET released_at = now(), release_note = 'broken' WHERE name = 'broken'`)
	// When matching the new hold fails, it is not matched yet and covers
	// its whole scope: no count, not even a partial one.
	f.exec(`CREATE FUNCTION refuse_match() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'refused'; END $$`)
	f.exec(`CREATE TRIGGER refuse_match BEFORE INSERT ON legal_hold_messages FOR EACH ROW EXECUTE FUNCTION refuse_match()`)
	h := f.expect("POST", "/api/legal-holds", map[string]any{"name": "own failing", "reason": "r", "accountId": mine.ID, "filter": falcon}, 201)
	if h["id"] == nil || h["matched"] != nil {
		t.Errorf("hold when matching fails %v", h)
	}
	if at := matchedAt(int64(h["id"].(float64))); at != nil {
		t.Errorf("hold whose matching failed is matched at %v", at)
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
