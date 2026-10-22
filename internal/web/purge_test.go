package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/store"
)

// purgeFixture: the admin's removed account "old" with m1 in INBOX and
// Archive (only there) and m2 in INBOX and in alice's "work"; the admin's
// active account "live"; alice's removed account "hers".
type purgeFixture struct {
	*authFixture
	ret          *archive.Retention
	admin, alice *store.User
	old          *store.Account
	m1, m2       string
	uid          uint32
}

func newPurgeFixture(t *testing.T) *purgeFixture {
	t.Helper()
	ret := &archive.Retention{Grace: 48 * time.Hour, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	f := &purgeFixture{authFixture: newAuthFixtureOpts(t, Options{Retention: ret}), ret: ret}
	f.admin = f.addUser("admin", keyPassword, true)
	f.alice = f.addUser("alice", keyPassword, false)
	f.addUser("kim", keyPassword, false)
	f.old = f.imapAccount("old", f.admin)
	inbox, archiveFolder := f.folder(f.old, "INBOX"), f.folder(f.old, "Archive")
	work := f.folder(f.imapAccount("work", f.alice), "INBOX")
	f.m1 = f.save(strings.Repeat("1", 64), 100, inbox, archiveFolder)
	f.m2 = f.save(strings.Repeat("2", 64), 50, inbox, work)
	f.old = f.remove(f.old)
	f.imapAccount("live", f.admin)
	hers := f.imapAccount("hers", f.alice)
	f.save(strings.Repeat("3", 64), 10, f.folder(hers, "INBOX"))
	f.remove(hers)
	return f
}

func (f *purgeFixture) folder(a *store.Account, name string) *store.Folder {
	f.t.Helper()
	folder, err := f.st.GetOrCreateFolder(context.Background(), a.ID, name)
	if err != nil {
		f.t.Fatal(err)
	}
	return folder
}

func (f *purgeFixture) save(sha string, size int64, folders ...*store.Folder) string {
	f.t.Helper()
	for _, folder := range folders {
		f.uid++
		meta := store.MessageMeta{SHA256: sha, Size: size, StoredPath: "x", Subject: "s"}
		if _, err := f.st.SaveBatch(context.Background(), folder.ID, f.uid, []store.MessageMeta{meta},
			[]store.Location{{FolderID: folder.ID, UID: f.uid}}); err != nil {
			f.t.Fatal(err)
		}
	}
	return sha
}

func (f *purgeFixture) remove(a *store.Account) *store.Account {
	f.t.Helper()
	ctx := context.Background()
	if _, err := f.st.DeleteOrRemoveAccount(ctx, a.Ref()); err != nil {
		f.t.Fatal(err)
	}
	a, err := f.st.GetAccount(ctx, a.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return a
}

func (f *purgeFixture) preview(name string) purgePreviewJSON {
	f.t.Helper()
	var p purgePreviewJSON
	if err := json.Unmarshal([]byte(toJSON(f.expect("GET", "/api/accounts/"+name+"/purge", nil, 200))), &p); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func (f *purgeFixture) listed() map[string]accountJSON {
	f.t.Helper()
	var r accountsResponse
	if err := json.Unmarshal([]byte(toJSON(f.expect("GET", "/api/accounts", nil, 200))), &r); err != nil {
		f.t.Fatal(err)
	}
	out := map[string]accountJSON{}
	for _, a := range r.Accounts {
		out[a.Name] = a
	}
	return out
}

func TestPurgeNeedsAdmin(t *testing.T) {
	f := newPurgeFixture(t)
	k := f.as()
	k.loginOK("alice", keyPassword)
	// Also the user's own removed account.
	for _, name := range []string{"hers", "old"} {
		body := k.expect("GET", "/api/accounts/"+name+"/purge", nil, 403)
		if body["error"] != adminOnlyPurge {
			t.Fatalf("GET %s: %v", name, body)
		}
		k.expect("POST", "/api/accounts/"+name+"/purge", map[string]any{"count": 1}, 403)
	}
	if _, err := f.st.GetAccount(context.Background(), f.old.ID); err != nil {
		t.Fatal(err)
	}
}

func TestPurgeOwnAccountsOnly(t *testing.T) {
	f := newPurgeFixture(t)
	f.loginOK("admin", keyPassword)
	// alice's removed account is not found, like a name that does not exist.
	f.expect("GET", "/api/accounts/hers/purge", nil, 404)
	f.expect("POST", "/api/accounts/hers/purge", map[string]any{"count": 1}, 404)
	f.expect("GET", "/api/accounts/nothing/purge", nil, 404)
	if p := f.preview("old"); p.Account != "old" {
		t.Fatalf("own account: %+v", p)
	}
}

func TestPurgeNotRemoved(t *testing.T) {
	f := newPurgeFixture(t)
	f.loginOK("admin", keyPassword)
	p := f.preview("live")
	if !p.Blockers.NotRemoved || len(p.Blockers.Messages) != 1 || !strings.Contains(p.Blockers.Messages[0], "not removed") {
		t.Fatalf("preview of an active account: %+v", p.Blockers)
	}
	body := f.expect("POST", "/api/accounts/live/purge", map[string]any{"count": 0}, 409)
	if !strings.Contains(body["error"].(string), `account "live" is not removed`) {
		t.Fatalf("confirm of an active account: %v", body)
	}
	if !f.listed()["live"].Enabled {
		t.Fatal("active account changed")
	}
}

func TestPurgePreviewAndConfirm(t *testing.T) {
	f := newPurgeFixture(t)
	ctx := context.Background()
	f.loginOK("admin", keyPassword)

	p := f.preview("old")
	if p.Kind != "imap" || p.Locations != 3 || p.Messages != 2 || p.Deleted != 1 || p.DeletedBytes != 100 || p.Kept != 1 ||
		len(p.KeptIn) != 1 || p.KeptIn[0] != (purgeKeptJSON{Owner: "alice", Account: "work", Messages: 1}) ||
		p.Grace != "48h0m0s" || p.Purge != nil {
		t.Fatalf("preview %+v", p)
	}
	if b := p.Blockers; b.NotRemoved || b.Syncing || b.PurgeOpen || len(b.Rules) != 0 || len(b.Holds) != 0 || len(b.Messages) != 0 {
		t.Fatalf("blockers %+v", b)
	}
	if o := f.listed()["old"]; o.Purging || o.Purge != nil {
		t.Fatal("purging before the confirmation")
	}

	// The count is required and must be the number of messages to delete.
	f.expect("POST", "/api/accounts/old/purge", map[string]any{}, 400)
	f.expect("POST", "/api/accounts/old/purge", map[string]any{"count": "1"}, 400)
	body := f.expect("POST", "/api/accounts/old/purge", map[string]any{"count": 2}, 409)
	if body["deleted"] != float64(1) || !strings.Contains(body["error"].(string), "now 1") {
		t.Fatalf("wrong count: %v", body)
	}
	if open, err := f.st.OpenAccountPurge(ctx); err != nil || open != nil {
		t.Fatalf("confirmed with a wrong count: %+v %v", open, err)
	}

	confirmed := f.expect("POST", "/api/accounts/old/purge", map[string]any{"count": 1}, 202)
	id := int64(confirmed["purge"].(float64))
	open, err := f.st.OpenAccountPurge(ctx)
	if err != nil || open == nil || open.ID != id || open.AccountID != f.old.ID || open.Expected != 1 || open.CreatedBy != "admin" {
		t.Fatalf("open purge %+v %v", open, err)
	}
	// Nothing is deleted by the request: the retention job does it.
	if _, err := f.st.GetAccount(ctx, f.old.ID); err != nil {
		t.Fatalf("deleted by the request: %v", err)
	}
	listed := f.listed()
	if o := listed["old"]; !o.Purging || o.Purge == nil || *o.Purge != (accountPurgeJSON{ID: id, Expected: 1}) {
		t.Fatalf("purging not listed: %+v", o.Purge)
	}
	if l := listed["live"]; l.Purging || l.Purge != nil {
		t.Fatalf("live purging: %+v", l)
	}
	if p := f.preview("old"); p.Purge == nil || p.Purge.ID != id || p.Purge.State != store.PurgeConfirmed || p.Purge.Expected != 1 {
		t.Fatalf("preview while purging %+v", p.Purge)
	}
	body = f.expect("POST", "/api/accounts/old/purge", map[string]any{"count": 1}, 409)
	if !strings.Contains(body["error"].(string), "open") {
		t.Fatalf("second confirmation: %v", body)
	}

	f.ret.RunOnce(ctx)
	if _, ok := f.listed()["old"]; ok {
		t.Fatal("old still listed")
	}
	f.expect("GET", "/api/accounts/old/purge", nil, 404)
	done, err := f.st.AccountPurge(ctx, id)
	if err != nil || done.State != store.PurgeDone || done.Locations != 3 || done.Deleted != 1 || done.Kept != 1 {
		t.Fatalf("purge %+v %v", done, err)
	}
	if ok, err := f.st.IsTombstoned(ctx, f.m1); err != nil || !ok {
		t.Fatalf("m1 tombstoned %v %v", ok, err)
	}
	if ok, err := f.st.IsTombstoned(ctx, f.m2); err != nil || ok {
		t.Fatalf("m2 tombstoned %v %v", ok, err)
	}

	entries, err := f.st.ListAudit(ctx, store.AuditPage{
		All: true, Actions: []string{"account.purge"}, Since: time.Now().Add(-time.Hour), Until: time.Now().Add(time.Hour), Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	var confirm *store.AuditRecord
	for i := range entries {
		if entries[i].Action == "account.purge" {
			confirm = &entries[i]
		}
	}
	var details map[string]any
	if confirm != nil {
		_ = json.Unmarshal(confirm.Details, &details)
	}
	if confirm == nil || confirm.UserName != "admin" || confirm.TargetName != "old" || details["deleted"] != float64(1) ||
		details["kept"] != float64(1) || details["purge_id"] != float64(id) {
		t.Fatalf("audit %+v", entries)
	}
}

func TestPurgeBlockedByRuleAndHold(t *testing.T) {
	f := newPurgeFixture(t)
	ctx := context.Background()
	f.loginOK("admin", keyPassword)
	rule, err := f.st.AddRetentionRule(ctx, store.NewRetentionRule{AccountID: f.old.ID, Years: 10})
	if err != nil {
		t.Fatal(err)
	}
	p := f.preview("old")
	if len(p.Blockers.Rules) != 1 || p.Blockers.Rules[0] != (purgeRuleJSON{ID: rule, Years: 10}) ||
		!strings.Contains(strings.Join(p.Blockers.Messages, ";"), "#"+itoa(rule)+" (all folders, 10 years)") {
		t.Fatalf("rule blocker %+v", p.Blockers)
	}
	body := f.expect("POST", "/api/accounts/old/purge", map[string]any{"count": 1}, 409)
	if !strings.Contains(body["error"].(string), "retention rule #"+itoa(rule)) {
		t.Fatalf("confirm with a rule: %v", body)
	}
	if err := f.st.RemoveRetentionRule(ctx, rule); err != nil {
		t.Fatal(err)
	}

	hold, err := f.st.AddLegalHold(ctx, store.NewLegalHold{Name: "Case A", Reason: "lawsuit", AccountID: &f.old.ID})
	if err != nil {
		t.Fatal(err)
	}
	p = f.preview("old")
	if len(p.Blockers.Rules) != 0 || len(p.Blockers.Holds) != 1 ||
		p.Blockers.Holds[0] != (purgeHoldJSON{ID: hold, Name: "Case A", NamesAccount: true}) {
		t.Fatalf("hold blocker %+v", p.Blockers)
	}
	body = f.expect("POST", "/api/accounts/old/purge", map[string]any{"count": 1}, 409)
	if !strings.Contains(body["error"].(string), `legal hold #`+itoa(hold)+` "Case A"`) {
		t.Fatalf("confirm with a hold: %v", body)
	}
	// A released hold that names the account still keeps it.
	if err := f.st.ReleaseLegalHold(ctx, hold, nil, "closed"); err != nil {
		t.Fatal(err)
	}
	if p := f.preview("old"); len(p.Blockers.Holds) != 1 || !p.Blockers.Holds[0].Released {
		t.Fatalf("released hold %+v", p.Blockers)
	}
	body = f.expect("POST", "/api/accounts/old/purge", map[string]any{"count": 1}, 409)
	if !strings.Contains(body["error"].(string), `released legal hold #`+itoa(hold)) {
		t.Fatalf("confirm with a released hold: %v", body)
	}
	if open, err := f.st.OpenAccountPurge(ctx); err != nil || open != nil {
		t.Fatalf("confirmed despite blockers: %+v %v", open, err)
	}
}

func TestPurgeWithoutRetentionJob(t *testing.T) {
	f := newAuthFixture(t)
	admin := f.addUser("admin", keyPassword, true)
	pf := &purgeFixture{authFixture: f}
	a := f.imapAccount("old", admin)
	pf.save(strings.Repeat("1", 64), 1, pf.folder(a, "INBOX"))
	pf.remove(a)
	f.loginOK("admin", keyPassword)
	if s := toJSON(f.expect("GET", "/api/accounts/old/purge", nil, 200)); !strings.Contains(s, `"grace":""`) {
		t.Fatalf("preview %s", s)
	}
	f.expect("POST", "/api/accounts/old/purge", map[string]any{"count": 0}, 503)
}

func TestPurgeStaleAccount(t *testing.T) {
	f := newPurgeFixture(t)
	ctx := context.Background()
	f.loginOK("admin", keyPassword)
	// The account changes between loading it and confirming the purge.
	// It runs in the handler's goroutine: report errors, never Fatal.
	f.server.beforeConfirmPurge = func() {
		conn, err := pgx.Connect(ctx, f.dbURL)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.Close(ctx) }()
		if _, err := conn.Exec(ctx, "UPDATE accounts SET version = version + 1 WHERE id = $1", f.old.ID); err != nil {
			t.Error(err)
		}
	}
	body := f.expect("POST", "/api/accounts/old/purge", map[string]any{"count": 1}, 409)
	if body["error"] != store.ErrStale.Error() {
		t.Fatalf("stale account: %v", body)
	}
	if open, err := f.st.OpenAccountPurge(ctx); err != nil || open != nil {
		t.Fatalf("confirmed a stale account: %+v %v", open, err)
	}
}
