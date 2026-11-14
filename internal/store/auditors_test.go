package store_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/keepia/archive/internal/store"
	"github.com/keepia/archive/internal/store/storetest"
)

// auditorFixture is a groupFixture (alice: info, private; bob: mine; uwe;
// root, an admin; all in "Default", the group a; kim with kb in group b)
// with three auditors, auda and aud2 in a and audb in b, and alice's
// accounts a1 (INBOX, Private) and a2 (INBOX) for the restriction matrix
// (#393, part 1):
//
//	1: a1/INBOX 2022-03-01       2: a1/INBOX 2021-06-01
//	3: a1/Private 2022-05-01     4: a1/INBOX and a1/Private 2022-07-01
//	5: a1/INBOX 2023-01-01 00:00 6: a1/INBOX 2022-01-01 00:00
//	7: a2/INBOX 2022-02-01
//
// 2, 3 and 4 answer 1.
type auditorFixture struct {
	*groupFixture
	auda, aud2, audb             *store.User
	a1Inbox, a1Private, a2Inbox  *store.Folder
	a1, a2                       int64
	period2022From, period2022To *time.Time
}

func newAuditorFixture(t *testing.T) *auditorFixture {
	t.Helper()
	f := &auditorFixture{groupFixture: newGroupFixture(t)}
	f.auda = f.auditor("auda", f.a)
	f.aud2 = f.auditor("aud2", f.a)
	f.audb = f.auditor("audb", f.b)
	f.a1Inbox = f.folder(f.alice, "a1", "INBOX")
	f.a1Private = f.folder(f.alice, "a1", "Private")
	f.a2Inbox = f.folder(f.alice, "a2", "INBOX")
	f.a1, f.a2 = f.a1Inbox.AccountID, f.a2Inbox.AccountID
	at := func(s string) *time.Time { return ts(s) }
	f.save(f.a1Inbox, hashOf('1'), "steuer 1", at("2022-03-01T10:00:00Z"), at("2022-03-01T10:00:00Z"))
	f.save(f.a1Inbox, hashOf('2'), "steuer 2", at("2021-06-01T10:00:00Z"), at("2021-06-01T10:00:00Z"))
	f.save(f.a1Private, hashOf('3'), "steuer 3", at("2022-05-01T10:00:00Z"), at("2022-05-01T10:00:00Z"))
	f.save(f.a1Inbox, hashOf('4'), "steuer 4", at("2022-07-01T10:00:00Z"), at("2022-07-01T10:00:00Z"))
	f.save(f.a1Private, hashOf('4'), "steuer 4", at("2022-07-01T10:00:00Z"), at("2022-07-01T10:00:00Z"))
	f.save(f.a1Inbox, hashOf('5'), "steuer 5", at("2023-01-01T00:00:00Z"), at("2023-01-01T00:00:00Z"))
	f.save(f.a1Inbox, hashOf('6'), "steuer 6", at("2022-01-01T00:00:00Z"), at("2022-01-01T00:00:00Z"))
	f.save(f.a2Inbox, hashOf('7'), "steuer 7", at("2022-02-01T10:00:00Z"), at("2022-02-01T10:00:00Z"))
	// Replies, not a thread: listings stay one row per message.
	if _, err := f.conn.Exec(f.ctx, `UPDATE messages SET message_id = 'm1@example' WHERE sha256 = $1`, hashOf('1')); err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.Exec(f.ctx, `UPDATE messages SET in_reply_to = 'm1@example' WHERE sha256 = ANY ($1)`,
		[]string{hashOf('2'), hashOf('3'), hashOf('4')}); err != nil {
		t.Fatal(err)
	}
	f.period2022From, f.period2022To = at("2022-01-01T00:00:00Z"), at("2023-01-01T00:00:00Z")
	return f
}

func (f *auditorFixture) auditor(name string, g *store.Group) *store.User {
	f.t.Helper()
	u, err := f.st.CreateUserInGroup(f.ctx, store.NewUser{Name: name, PasswordHash: "h", GroupID: g.ID, Auditor: true})
	if err != nil {
		f.t.Fatalf("auditor %s: %v", name, err)
	}
	return u
}

// grant creates a grant; zero fields default to alice's grant of a1 to
// auda for 28 days.
func (f *auditorFixture) grant(g store.NewGrant) int64 {
	f.t.Helper()
	id, err := f.st.CreateGrant(f.ctx, f.grantDefaults(g))
	if err != nil {
		f.t.Fatalf("grant: %v", err)
	}
	return id
}

func (f *auditorFixture) grantDefaults(g store.NewGrant) store.NewGrant {
	if g.OwnerID == 0 {
		g.OwnerID = f.alice.ID
	}
	if g.AuditorID == 0 {
		g.AuditorID = f.auda.ID
	}
	if g.Label == "" {
		g.Label = "Betriebsprüfung 2022"
	}
	if g.Accounts == nil {
		g.Accounts = []store.GrantAccount{{AccountID: f.a1}}
	}
	if g.EndsAt.IsZero() {
		g.EndsAt = time.Now().AddDate(0, 0, 28)
	}
	return g
}

// matrixGrant is the grant of the restriction matrix: a1's INBOX for 2022.
func (f *auditorFixture) matrixGrant() int64 {
	f.t.Helper()
	return f.grant(store.NewGrant{
		Accounts:   []store.GrantAccount{{AccountID: f.a1, Folders: []int64{f.a1Inbox.ID}}},
		PeriodFrom: f.period2022From, PeriodUntil: f.period2022To,
	})
}

func (f *auditorFixture) scope(u *store.User, grant int64) store.Scope {
	f.t.Helper()
	sc, err := f.st.AuditorScope(f.ctx, u.ID, grant)
	if err != nil {
		f.t.Fatal(err)
	}
	return sc
}

// session starts a session of u, bound to grant unless it is 0.
func (f *auditorFixture) session(u *store.User, grant int64) []byte {
	f.t.Helper()
	id := make([]byte, 32)
	if _, err := rand.Read(id); err != nil {
		f.t.Fatal(err)
	}
	if err := f.st.CreateSession(f.ctx, id, u.ID, time.Now().Add(12*time.Hour), "test"); err != nil {
		f.t.Fatal(err)
	}
	if grant != 0 {
		if err := f.st.SetSessionGrant(f.ctx, id, u.ID, grant); err != nil {
			f.t.Fatalf("bind the session to grant %d: %v", grant, err)
		}
	}
	return id
}

// sessionOK reports whether GetSession accepts the session.
func (f *auditorFixture) sessionOK(id []byte) bool {
	f.t.Helper()
	_, err := f.st.GetSession(f.ctx, id, time.Now().Add(-time.Hour))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		f.t.Fatal(err)
	}
	return err == nil
}

// grantState is a grant's row: its revocation reason ("" while
// unrevoked) and its auditor.
func (f *auditorFixture) grantState(id int64) (reason string, auditor *int64) {
	f.t.Helper()
	var r *string
	if err := f.conn.QueryRow(f.ctx, `SELECT revoke_reason, auditor_id FROM auditor_grants WHERE id = $1`, id).Scan(&r, &auditor); err != nil {
		f.t.Fatalf("grant %d: %v", id, err)
	}
	if r != nil {
		reason = *r
	}
	return reason, auditor
}

func (f *auditorFixture) count(sql string, args ...any) int {
	f.t.Helper()
	return countOf(f.t, f.conn, sql, args...)
}

// crossingGrants counts the unrevoked grants whose auditor and owner are
// in different groups: the invariant every group test keeps at zero.
func (f *auditorFixture) checkNoCrossingGrants() {
	f.t.Helper()
	if n := f.count(`SELECT count(*) FROM auditor_grants g JOIN users a ON a.id = g.auditor_id JOIN users o ON o.id = g.owner_id
		WHERE g.revoked_at IS NULL AND a.group_id <> o.group_id`); n != 0 {
		f.t.Fatalf("%d unrevoked grants cross groups", n)
	}
}

// conversation returns the first letters of a message's conversation in
// sc, nil for ErrNotFound.
func (f *auditorFixture) conversation(sc store.Scope, c byte) []string {
	f.t.Helper()
	conv, err := f.st.GetConversation(f.ctx, sc, hashOf(c))
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		f.t.Fatal(err)
	}
	out := []string{}
	for _, m := range conv.Messages {
		out = append(out, m.SHA256[:1])
	}
	if int(conv.Total) != len(out) {
		f.t.Fatalf("conversation total %d, %d messages", conv.Total, len(out))
	}
	slices.Sort(out)
	return out
}

// locations returns "account/folder" of a message's locations in sc, nil
// for ErrNotFound.
func (f *auditorFixture) locations(sc store.Scope, c byte) []string {
	f.t.Helper()
	d, err := f.st.GetMessageDetail(f.ctx, sc, hashOf(c))
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		f.t.Fatal(err)
	}
	out := []string{}
	for _, l := range d.Locations {
		out = append(out, l.Account+"/"+l.Folder)
	}
	slices.Sort(out)
	return out
}

// folders returns ListAccountFolders of sc as "account/folder:messages".
func (f *auditorFixture) folders(sc store.Scope) []string {
	f.t.Helper()
	list, err := f.st.ListAccountFolders(f.ctx, sc)
	if err != nil {
		f.t.Fatal(err)
	}
	out := []string{}
	for _, a := range list {
		out = append(out, a.Name)
		for _, fo := range a.Folders {
			out = append(out, fmt.Sprintf("%s/%s:%d", a.Name, fo.Name, fo.Messages))
		}
	}
	return out
}

// TestAuditorRestrictionMatrix (#393, store tests): a grant of a1's INBOX
// for 2022 opens exactly messages 1, 4 and 6 (period from inclusive,
// until exclusive), through every read path and every narrowing of the
// scope; a1/Private and a2 stay closed.
func TestAuditorRestrictionMatrix(t *testing.T) {
	f := newAuditorFixture(t)
	g := f.matrixGrant()
	sc := f.scope(f.auda, g)
	want := []string{"1", "4", "6"}
	for name, s := range map[string]store.Scope{
		"scope": sc, "named": sc.Named("alice/a1"), "only": sc.Only(f.a1), "shared": sc.Shared(),
		"restrict nil": sc.Restrict(nil, nil, nil),
	} {
		if got := f.listed(s); !slices.Equal(got, want) {
			t.Errorf("%s lists %v, want %v", name, got, want)
		}
	}
	if got := f.listed(sc.Named("alice/a2")); len(got) != 0 {
		t.Errorf("a2 lists %v", got)
	}
	if got := f.listed(sc.Own()); len(got) != 0 {
		t.Errorf("own lists %v", got)
	}
	// A query, and date filters clamped to the period.
	search := func(f2 store.SearchFilter) []string {
		t.Helper()
		f2.Scope, f2.Limit = sc, 50
		rows, err := f.st.SearchMessages(f.ctx, f2)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, r := range rows {
			out = append(out, r.SHA256[:1])
		}
		slices.Sort(out)
		return out
	}
	if got := search(store.SearchFilter{Query: "steuer"}); !slices.Equal(got, want) {
		t.Errorf("query lists %v", got)
	}
	if got := search(store.SearchFilter{After: ts("2020-01-01T00:00:00Z"), Before: ts("2030-01-01T00:00:00Z")}); !slices.Equal(got, want) {
		t.Errorf("a wider range lists %v", got)
	}
	if got := search(store.SearchFilter{Before: ts("2022-02-01T00:00:00Z")}); !slices.Equal(got, []string{"6"}) {
		t.Errorf("before February lists %v", got)
	}
	if got := search(store.SearchFilter{After: ts("2023-06-01T00:00:00Z")}); len(got) != 0 {
		t.Errorf("a disjoint range lists %v", got)
	}
	if got := search(store.SearchFilter{Folder: "Private"}); len(got) != 0 {
		t.Errorf("folder Private lists %v", got)
	}
	// Single messages: 404 outside the folders or the period; the
	// locations of a granted message only in granted folders.
	for _, c := range []byte("2357") {
		if got := f.locations(sc, c); got != nil {
			t.Errorf("message %c: %v, want not found", c, got)
		}
		if got := f.conversation(sc, c); got != nil {
			t.Errorf("conversation of %c: %v, want not found", c, got)
		}
	}
	if got := f.locations(sc, '4'); !slices.Equal(got, []string{"a1/INBOX"}) {
		t.Errorf("locations of 4: %v", got)
	}
	// Conversation members and their count: only those in scope.
	if got := f.conversation(sc, '1'); !slices.Equal(got, []string{"1", "4"}) {
		t.Errorf("conversation of 1: %v", got)
	}
	if got := f.conversation(storetest.Viewer(t, f.st, f.alice.ID), '1'); !slices.Equal(got, []string{"1", "2", "3", "4"}) {
		t.Errorf("alice's conversation of 1: %v", got)
	}
	// Folders and counts: only INBOX, with the period's messages.
	if got := f.folders(sc); !slices.Equal(got, []string{"a1", "a1/INBOX:3"}) {
		t.Errorf("folders %v", got)
	}
	// The gone flag only over the granted locations: 4 is gone from INBOX
	// but still in Private, so only for the auditor it is gone.
	if _, err := f.conn.Exec(f.ctx, `UPDATE message_locations SET gone_at = now() WHERE folder_id = $1 AND message_sha256 = $2`, f.a1Inbox.ID, hashOf('4')); err != nil {
		t.Fatal(err)
	}
	gone := func(s store.Scope) []string {
		t.Helper()
		rows, err := f.st.SearchMessages(f.ctx, store.SearchFilter{Scope: s, Gone: true, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, r := range rows {
			if !r.Gone {
				t.Errorf("row %s listed by the gone filter without the flag", r.SHA256[:1])
			}
			out = append(out, r.SHA256[:1])
		}
		return out
	}
	if got := gone(sc); !slices.Equal(got, []string{"4"}) {
		t.Errorf("auditor's gone %v", got)
	}
	if got := gone(storetest.OwnAccount(t, f.st, f.alice.ID, "a1")); len(got) != 0 {
		t.Errorf("alice's gone %v", got)
	}
	// The Log's subjects come from the viewer's scope as before.
	shares, err := f.st.ListShareOnlyLocations(f.ctx, sc, sc, []string{hashOf('1'), hashOf('3')})
	if err != nil {
		t.Fatal(err)
	}
	if len(shares) != 1 || shares[0].SHA256 != hashOf('1') || shares[0].AccountID != f.a1 {
		t.Errorf("share-only locations %+v", shares)
	}
}

// TestAuditorFoldersPerAccount (#393, B2): folders are granted per account
// by ID. a1 with INBOX only, a2 with all folders: a folder created later
// is visible in a2, not in a1, nor is a renamed folder of a1 (a new row);
// an empty folder list opens nothing.
func TestAuditorFoldersPerAccount(t *testing.T) {
	f := newAuditorFixture(t)
	g := f.grant(store.NewGrant{Accounts: []store.GrantAccount{{AccountID: f.a1, Folders: []int64{f.a1Inbox.ID}}, {AccountID: f.a2}}})
	sc := f.scope(f.auda, g)
	if got := f.listed(sc); !slices.Equal(got, []string{"1", "2", "4", "5", "6", "7"}) {
		t.Fatalf("lists %v", got)
	}
	if got := f.locations(sc, '3'); got != nil {
		t.Errorf("a1/Private's message: %v", got)
	}
	later := f.folder(f.alice, "a2", "Later")
	f.save(later, hashOf('8'), "later", ts("2022-08-01T00:00:00Z"), nil)
	renamed := f.folder(f.alice, "a1", "INBOX-renamed")
	f.save(renamed, hashOf('9'), "renamed", ts("2022-08-01T00:00:00Z"), nil)
	sc = f.scope(f.auda, g)
	if got := f.listed(sc); !slices.Equal(got, []string{"1", "2", "4", "5", "6", "7", "8"}) {
		t.Errorf("after the new folders: %v", got)
	}
	if got := f.folders(sc); !slices.Equal(got, []string{"a1", "a1/INBOX:5", "a2", "a2/INBOX:1", "a2/Later:1"}) {
		t.Errorf("folders %v", got)
	}
	// None at all.
	none := f.grant(store.NewGrant{Accounts: []store.GrantAccount{{AccountID: f.a1, Folders: []int64{}}}})
	sc = f.scope(f.auda, none)
	if len(sc.Accounts) != 1 || sc.Accounts[0].Folders == nil || len(sc.Accounts[0].Folders) != 0 {
		t.Fatalf("empty folder list: %+v", sc.Accounts)
	}
	if got := f.listed(sc); len(got) != 0 {
		t.Errorf("an empty folder list lists %v", got)
	}
	if got := f.folders(sc); !slices.Equal(got, []string{"a1"}) {
		t.Errorf("an empty folder list shows %v", got)
	}
}

// TestScopeRestrict (#393, B3): Restrict only narrows, a non-nil map drops
// the accounts it does not name, and the original scope's folder lists
// stay as they were. The IDs are abstract, each folder ID of an account
// (10-12 of account 1, 20 of account 2); a folder ID listed under another
// account is TestRestrictForeignFolder's case.
func TestScopeRestrict(t *testing.T) {
	jan, feb, mar := ts("2022-01-01T00:00:00Z"), ts("2022-02-01T00:00:00Z"), ts("2022-03-01T00:00:00Z")
	sc := store.Scope{Viewer: 9, Accounts: []store.ScopeAccount{{ID: 1, Folders: []int64{10, 11, 12}}, {ID: 2}, {ID: 3, Folders: []int64{}}}}
	r := sc.Restrict(nil, jan, mar)
	if len(r.Accounts) != 3 || !slices.Equal(r.Accounts[0].Folders, []int64{10, 11, 12}) || r.Accounts[1].Folders != nil {
		t.Fatalf("nil map: %+v", r.Accounts)
	}
	r = r.Restrict(map[int64][]int64{1: {11, 12, 99}, 2: {20}, 3: nil}, feb, ts("2023-01-01T00:00:00Z"))
	ids := func(s store.Scope) string { return fmt.Sprint(scopeIDs(s)) }
	if ids(r) != "[1 2 3]" || !slices.Equal(r.Accounts[0].Folders, []int64{11, 12}) || !slices.Equal(r.Accounts[1].Folders, []int64{20}) ||
		r.Accounts[2].Folders == nil || len(r.Accounts[2].Folders) != 0 {
		t.Fatalf("narrowed: %+v", r.Accounts)
	}
	if after, before := r.Period(); !after.Equal(*feb) || !before.Equal(*mar) {
		t.Fatalf("period %v %v, want the later start and the earlier end", after, before)
	}
	if after, before := r.Restrict(nil, nil, nil).Period(); !after.Equal(*feb) || !before.Equal(*mar) {
		t.Fatalf("nil bounds changed the period: %v %v", after, before)
	}
	if got := r.Restrict(map[int64][]int64{2: nil}, nil, nil); ids(got) != "[2]" {
		t.Fatalf("a map that names only 2: %v", ids(got))
	}
	if got := r.Restrict(map[int64][]int64{}, nil, nil); len(got.Accounts) != 0 {
		t.Fatalf("an empty map keeps %v", ids(got))
	}
	if !slices.Equal(sc.Accounts[0].Folders, []int64{10, 11, 12}) || sc.Accounts[1].Folders != nil {
		t.Fatalf("the original changed: %+v", sc.Accounts)
	}
	// Narrowing keeps the restriction.
	n := r.Only(1)
	if after, _ := n.Period(); len(n.Accounts) != 1 || !slices.Equal(n.Accounts[0].Folders, []int64{11, 12}) || after == nil {
		t.Fatalf("Only lost the restriction: %+v", n)
	}
}

// TestRestrictForeignFolder (#509 review, A2 = B1): a folder ID listed
// under an account it does not belong to opens nothing, neither in an
// auditor's scope (a2 with all folders, restricted to a1's Private) nor in
// an owner's scope (a1 and a2 restricted to each other's folders), and
// RestrictScope drops such IDs.
func TestRestrictForeignFolder(t *testing.T) {
	f := newAuditorFixture(t)
	g := f.grant(store.NewGrant{Accounts: []store.GrantAccount{{AccountID: f.a1, Folders: []int64{f.a1Inbox.ID}}, {AccountID: f.a2}}})
	sc := f.scope(f.auda, g)
	if got := f.locations(sc, '3'); got != nil {
		t.Fatalf("message 3 before Restrict: %v", got)
	}
	r := sc.Restrict(map[int64][]int64{f.a1: nil, f.a2: {f.a1Private.ID}}, nil, nil)
	if got := f.locations(r, '3'); got != nil {
		t.Errorf("auditor: Restrict widened the scope, message 3 at %v", got)
	}
	if got := f.listed(r); !slices.Equal(got, []string{"1", "2", "4", "5", "6"}) {
		t.Errorf("auditor lists %v, want a1/INBOX only", got)
	}
	if got := f.folders(r); !slices.Equal(got, []string{"a1", "a1/INBOX:5", "a2"}) {
		t.Errorf("auditor's folders %v", got)
	}
	owner, err := f.st.OwnerScope(f.ctx, f.alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	swapped := map[int64][]int64{f.a1: {f.a2Inbox.ID}, f.a2: {f.a1Private.ID}}
	o := owner.Restrict(swapped, nil, nil)
	if got := f.listed(o); len(got) != 0 {
		t.Errorf("owner's swapped folders list %v", got)
	}
	for _, c := range []byte("37") {
		if got := f.locations(o, c); got != nil {
			t.Errorf("owner: message %c at %v", c, got)
		}
	}
	// A folder ID listed under two accounts is wrong for one of them; it
	// opens neither.
	twice := owner.Restrict(map[int64][]int64{f.a1: {f.a1Private.ID}, f.a2: {f.a1Private.ID, f.a2Inbox.ID}}, nil, nil)
	if got := f.listed(twice); !slices.Equal(got, []string{"7"}) {
		t.Errorf("a folder ID under two accounts: lists %v, want a2/INBOX's 7 only", got)
	}
	rs, err := f.st.RestrictScope(f.ctx, owner, swapped, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Accounts) != 2 || len(rs.Accounts[0].Folders) != 0 || rs.Accounts[0].Folders == nil || len(rs.Accounts[1].Folders) != 0 {
		t.Errorf("RestrictScope kept foreign folders: %+v", rs.Accounts)
	}
	rs, err = f.st.RestrictScope(f.ctx, owner, map[int64][]int64{f.a1: {f.a1Private.ID, f.a2Inbox.ID}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Accounts) != 1 || !slices.Equal(rs.Accounts[0].Folders, []int64{f.a1Private.ID}) || !slices.Equal(f.listed(rs), []string{"3", "4"}) {
		t.Errorf("RestrictScope to a1/Private: %+v lists %v", rs.Accounts, f.listed(rs))
	}
}

// TestScopeParamsInEveryQuery (#393, decision 17): every query of the
// generated package that takes a scope's accounts also takes the folder
// restriction (as account and folder pairs) and the period, so a new read query cannot forget them.
// SearchMessages and its relatives take the period as After and Before,
// which searchParams clamps to the scope's. A bare accounts parameter (a
// query without Params struct) is refused.
func TestScopeParamsInEveryQuery(t *testing.T) {
	paths, err := filepath.Glob("db/*.sql.go")
	if err != nil || len(paths) == 0 {
		t.Fatalf("generated files: %v %v", paths, err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, p := range paths {
		file, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			switch d := d.(type) {
			case *ast.GenDecl:
				for _, s := range d.Specs {
					ts, ok := s.(*ast.TypeSpec)
					if !ok || !strings.HasSuffix(ts.Name.Name, "Params") {
						continue
					}
					st, ok := ts.Type.(*ast.StructType)
					if !ok {
						continue
					}
					fields := map[string]bool{}
					for _, fl := range st.Fields.List {
						for _, n := range fl.Names {
							fields[n.Name] = true
						}
					}
					if !fields["Accounts"] && !fields["SharedAccounts"] {
						continue
					}
					checked++
					period := fields["ScopeAfter"] && fields["ScopeBefore"] || fields["After"] && fields["Before"]
					if !fields["ScopeAllFolderAccounts"] || !fields["ScopeFolderAccounts"] || !fields["ScopeFolders"] || !period {
						t.Errorf("%s (%s) takes a scope's accounts without its folders and period", ts.Name.Name, filepath.Base(p))
					}
				}
			case *ast.FuncDecl:
				for _, fl := range d.Type.Params.List {
					arr, ok := fl.Type.(*ast.ArrayType)
					if !ok {
						continue
					}
					if id, ok := arr.Elt.(*ast.Ident); !ok || id.Name != "int64" {
						continue
					}
					for _, n := range fl.Names {
						if n.Name == "accounts" || n.Name == "sharedAccounts" {
							t.Errorf("%s (%s) takes a bare %s parameter", d.Name.Name, filepath.Base(p), n.Name)
						}
					}
				}
			}
		}
	}
	// SearchMessages, SearchThreads, CountThreadMatches, GetMessageSummary,
	// ListLocations, ListFolderCounts, ListShareOnlyLocations,
	// ListConversation, CountConversation, AttachmentMatches,
	// AttachmentSnippets, AuditSubjects.
	if checked < 12 {
		t.Fatalf("checked %d Params structs, want at least 12", checked)
	}
}

// TestGrantValidation (#393): what CreateGrant refuses, and that a folder
// stored under the wrong account opens nothing.
func TestGrantValidation(t *testing.T) {
	f := newAuditorFixture(t)
	journal := &store.Account{Name: "journal", Kind: store.KindSMTP, OwnerID: &f.alice.ID}
	if err := f.st.CreateAccount(f.ctx, journal); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		g    store.NewGrant
		want error
	}{
		"foreign account":         {store.NewGrant{Accounts: []store.GrantAccount{{AccountID: f.mineID}}}, store.ErrNotFound},
		"unknown account":         {store.NewGrant{Accounts: []store.GrantAccount{{AccountID: 999999}}}, store.ErrNotFound},
		"folder of another":       {store.NewGrant{Accounts: []store.GrantAccount{{AccountID: f.a1, Folders: []int64{f.a2Inbox.ID}}}}, store.ErrGrantInvalid},
		"account twice":           {store.NewGrant{Accounts: []store.GrantAccount{{AccountID: f.a1}, {AccountID: f.a1}}}, store.ErrGrantInvalid},
		"no account":              {store.NewGrant{Accounts: []store.GrantAccount{}}, store.ErrGrantInvalid},
		"smtp":                    {store.NewGrant{Accounts: []store.GrantAccount{{AccountID: journal.ID}}}, store.ErrGrantSMTP},
		"member as auditor":       {store.NewGrant{AuditorID: f.bob.ID}, store.ErrGrantNotAuditor},
		"owner as auditor":        {store.NewGrant{AuditorID: f.alice.ID}, store.ErrGrantNotAuditor},
		"auditor as owner":        {store.NewGrant{OwnerID: f.aud2.ID, Accounts: []store.GrantAccount{{AccountID: f.a1}}}, store.ErrNotFound},
		"unknown auditor":         {store.NewGrant{AuditorID: 999999}, store.ErrNotFound},
		"more than 365 days":      {store.NewGrant{EndsAt: time.Now().AddDate(0, 0, 366)}, store.ErrGrantInvalid},
		"ended":                   {store.NewGrant{EndsAt: time.Now().Add(-time.Minute)}, store.ErrGrantInvalid},
		"inverted period":         {store.NewGrant{PeriodFrom: f.period2022To, PeriodUntil: f.period2022From}, store.ErrGrantInvalid},
		"empty label":             {store.NewGrant{Label: " "}, store.ErrGrantInvalid},
		"label with outer spaces": {store.NewGrant{Label: " x"}, store.ErrGrantInvalid},
	} {
		if _, err := f.st.CreateGrant(f.ctx, f.grantDefaults(c.g)); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	if n := f.count(`SELECT count(*) FROM auditor_grants`); n != 0 {
		t.Fatalf("%d grants after refusals", n)
	}
	// A folder ID stored under the wrong account (past the store) opens
	// nothing: a1 gets a2's INBOX, which a2 itself does not grant.
	g := f.grant(store.NewGrant{Accounts: []store.GrantAccount{{AccountID: f.a1, Folders: []int64{}}}})
	if _, err := f.conn.Exec(f.ctx, `INSERT INTO auditor_grant_folders (grant_id, account_id, folder_id) VALUES ($1, $2, $3)`, g, f.a1, f.a2Inbox.ID); err != nil {
		t.Fatal(err)
	}
	sc := f.scope(f.auda, g)
	if got := f.listed(sc); len(got) != 0 {
		t.Errorf("a folder under the wrong account lists %v", got)
	}
	if len(sc.Accounts) != 1 || len(sc.Accounts[0].Folders) != 0 {
		t.Errorf("scope %+v", sc.Accounts)
	}
}

// TestGrantActivity (#393): only an active grant opens anything: not an
// expired, a future or a revoked one; revoking ends the bound sessions at
// once; locking the owner suspends the grant and unlocking restores it
// (maintainer decision M2). A session ends with its grant at the latest.
func TestGrantActivity(t *testing.T) {
	f := newAuditorFixture(t)
	active := f.grant(store.NewGrant{})
	expired := f.grant(store.NewGrant{})
	storetest.ShiftGrant(t, f.url, expired, -60*24*time.Hour)
	future := f.grant(store.NewGrant{StartsAt: ptr(time.Now().Add(24 * time.Hour)), EndsAt: time.Now().Add(48 * time.Hour)})
	revoked := f.grant(store.NewGrant{})
	if err := f.st.RevokeGrant(f.ctx, revoked, &f.alice.ID, &f.alice.ID); err != nil {
		t.Fatal(err)
	}
	for name, g := range map[string]int64{"expired": expired, "future": future, "revoked": revoked} {
		if sc := f.scope(f.auda, g); len(sc.Accounts) != 0 {
			t.Errorf("%s grant opens %v", name, scopeIDs(sc))
		}
		id := f.session(f.auda, 0)
		if err := f.st.SetSessionGrant(f.ctx, id, f.auda.ID, g); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("bind to the %s grant: %v", name, err)
		}
	}
	if got := scopeIDs(f.scope(f.auda, active)); !slices.Equal(got, []int64{f.a1}) {
		t.Fatalf("active grant opens %v", got)
	}
	// Another auditor's grant opens nothing for auda.
	other := f.grant(store.NewGrant{AuditorID: f.aud2.ID})
	if sc := f.scope(f.auda, other); len(sc.Accounts) != 0 {
		t.Errorf("aud2's grant opens %v for auda", scopeIDs(sc))
	}
	if err := f.st.SetSessionGrant(f.ctx, f.session(f.auda, 0), f.auda.ID, other); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("bind to aud2's grant: %v", err)
	}
	actives, err := f.st.ActiveGrants(f.ctx, f.auda.ID)
	if err != nil || len(actives) != 1 || actives[0].ID != active {
		t.Fatalf("active grants %+v, %v", actives, err)
	}

	// The session's expiry is capped at the grant's end.
	bound := f.session(f.auda, active)
	short := f.grant(store.NewGrant{EndsAt: time.Now().Add(time.Hour)})
	capped := f.session(f.auda, short)
	var expires, ends time.Time
	if err := f.conn.QueryRow(f.ctx, `SELECT s.expires_at, g.ends_at FROM sessions s JOIN auditor_grants g ON g.id = s.grant_id WHERE s.id = $1`, capped).Scan(&expires, &ends); err != nil {
		t.Fatal(err)
	}
	if !expires.Equal(ends) {
		t.Errorf("session expires %v, grant ends %v", expires, ends)
	}
	sess, err := f.st.GetSession(f.ctx, bound, time.Now().Add(-time.Hour))
	if err != nil || !sess.IsAuditor || sess.GrantID == nil || *sess.GrantID != active {
		t.Fatalf("auditor session %+v, %v", sess, err)
	}
	unbound := f.session(f.auda, 0)
	if !f.sessionOK(unbound) {
		t.Fatal("an unbound session with an active grant is refused")
	}

	// M2: a locked owner suspends the grant; unlocking restores it.
	if err := f.st.SetUserLocked(f.ctx, f.alice.ID, true); err != nil {
		t.Fatal(err)
	}
	if f.sessionOK(bound) || f.sessionOK(unbound) {
		t.Error("a locked owner's grant still opens a session")
	}
	if sc := f.scope(f.auda, active); len(sc.Accounts) != 0 {
		t.Errorf("a locked owner's grant opens %v", scopeIDs(sc))
	}
	if err := f.st.SetUserLocked(f.ctx, f.alice.ID, false); err != nil {
		t.Fatal(err)
	}
	if !f.sessionOK(bound) || len(f.scope(f.auda, active).Accounts) != 1 {
		t.Error("unlocking the owner did not restore the grant")
	}

	// Revocation ends the bound session at once; an unbound one ends when
	// no active grant is left.
	for _, g := range []int64{active, short, other} {
		if err := f.st.RevokeGrant(f.ctx, g, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.count(`SELECT count(*) FROM sessions WHERE id = $1 OR id = $2`, bound, capped); n != 0 {
		t.Errorf("%d bound sessions survived the revocation", n)
	}
	if f.sessionOK(unbound) {
		t.Error("a session of an auditor without active grant is accepted")
	}
	if err := f.st.RevokeGrant(f.ctx, active, nil, nil); !errors.Is(err, store.ErrGrantRevoked) {
		t.Errorf("revoke twice: %v", err)
	}
	if err := f.st.RevokeGrant(f.ctx, expired, &f.bob.ID, &f.bob.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("bob revokes alice's grant: %v", err)
	}
	if err := f.st.ExtendGrant(f.ctx, active, f.alice.ID, time.Now().AddDate(0, 0, 60)); !errors.Is(err, store.ErrGrantRevoked) {
		t.Errorf("extend a revoked grant: %v", err)
	}
	// A member's session is unchanged.
	if !f.sessionOK(f.session(f.bob, 0)) {
		t.Error("a member's session is refused")
	}
}

// TestExtendGrant (#393, decision 6): an extension moves the end later,
// at most 365 days from now; an expired grant can be extended, another
// owner's cannot.
func TestExtendGrant(t *testing.T) {
	f := newAuditorFixture(t)
	g := f.grant(store.NewGrant{})
	_, ends := f.grantWindow(g)
	if err := f.st.ExtendGrant(f.ctx, g, f.alice.ID, ends.Add(-time.Hour)); !errors.Is(err, store.ErrGrantInvalid) {
		t.Errorf("shorten: %v", err)
	}
	if err := f.st.ExtendGrant(f.ctx, g, f.alice.ID, time.Now().AddDate(0, 0, 366)); !errors.Is(err, store.ErrGrantInvalid) {
		t.Errorf("beyond 365 days: %v", err)
	}
	if err := f.st.ExtendGrant(f.ctx, g, f.bob.ID, ends.AddDate(0, 0, 1)); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("bob extends alice's grant: %v", err)
	}
	storetest.ShiftGrant(t, f.url, g, -60*24*time.Hour)
	if len(f.scope(f.auda, g).Accounts) != 0 {
		t.Fatal("the expired grant opens something")
	}
	to := time.Now().AddDate(0, 0, 300).Truncate(time.Microsecond)
	ctx := store.WithAudit(f.ctx, store.TargetedEntry(store.ActorSession, "auditor.extend"))
	if err := f.st.ExtendGrant(ctx, g, f.alice.ID, to); err != nil {
		t.Fatal(err)
	}
	if _, ends := f.grantWindow(g); !ends.Equal(to) {
		t.Fatalf("ends %v, want %v", ends, to)
	}
	if len(f.scope(f.auda, g).Accounts) != 1 {
		t.Fatal("the extended grant opens nothing")
	}
	if e := groupedEntries(t, f.conn, "auditor.extend"); len(e) != 1 || groupOf(e[0]) != fmt.Sprint(f.a.ID) {
		t.Fatalf("entries %+v", e)
	}
}

func (f *auditorFixture) grantWindow(id int64) (starts, ends time.Time) {
	f.t.Helper()
	if err := f.conn.QueryRow(f.ctx, `SELECT starts_at, ends_at FROM auditor_grants WHERE id = $1`, id).Scan(&starts, &ends); err != nil {
		f.t.Fatal(err)
	}
	return starts, ends
}

// TestAuditorRefusals (#393, decisions 1 and 8): an auditor gets no share,
// owns no account, holds no API key (not even one inserted past the
// store), and is never an admin; the role never changes.
func TestAuditorRefusals(t *testing.T) {
	f := newAuditorFixture(t)
	if _, err := f.st.ShareAccount(f.ctx, f.account(f.infoID).Ref(), f.auda.ID); !errors.Is(err, store.ErrShareAuditor) {
		t.Errorf("share with auda: %v", err)
	}
	acc := &store.Account{Name: "own", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, OwnerID: &f.auda.ID}
	if err := f.st.CreateAccount(f.ctx, acc); !errors.Is(err, store.ErrAuditorAccount) {
		t.Errorf("account owned by auda: %v", err)
	}
	if err := f.st.SetAccountOwner(f.ctx, f.account(f.infoID).Ref(), f.auda.ID, false); !errors.Is(err, store.ErrAuditorAccount) {
		t.Errorf("move info to auda: %v", err)
	}
	if _, err := f.st.CreateAPIKey(f.ctx, f.auda.ID, store.NewAPIKey{Hash: make([]byte, 32), Hint: "abcd", Name: "k", Scopes: []string{store.ScopeRead}}); !errors.Is(err, store.ErrAuditorKey) {
		t.Errorf("API key for auda: %v", err)
	}
	hash := make([]byte, 32)
	hash[0] = 7
	if _, err := f.conn.Exec(f.ctx, `INSERT INTO api_keys (user_id, key_hash, hint, name, scopes) VALUES ($1, $2, 'abcd', 'raw', '{read}')`, f.auda.ID, hash); err != nil {
		t.Fatal(err)
	}
	f.grant(store.NewGrant{})
	if _, _, err := f.st.APIKeyLogin(f.ctx, hash); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("login with auda's raw key: %v", err)
	}
	if _, err := f.st.CreateUserInGroup(f.ctx, store.NewUser{Name: "both", PasswordHash: "h", Admin: true, Auditor: true, GroupID: f.a.ID}); !errors.Is(err, store.ErrAuditorAdmin) {
		t.Errorf("admin and auditor: %v", err)
	}
	if err := f.st.SetUserAdmin(f.ctx, f.auda.ID, true); !errors.Is(err, store.ErrAuditorAdmin) {
		t.Errorf("make auda an admin: %v", err)
	}
	for sql, constraint := range map[string]string{
		`UPDATE users SET is_admin = true WHERE name = 'auda'`:    "users_auditor_not_admin",
		`UPDATE users SET is_auditor = false WHERE name = 'auda'`: "",
		`UPDATE users SET is_auditor = true WHERE name = 'bob'`:   "",
	} {
		_, err := f.conn.Exec(f.ctx, sql)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != constraint {
			t.Errorf("%s: %v, want a check violation %q", sql, err, constraint)
		}
	}
	if u, err := f.st.GetUserByID(f.ctx, f.auda.ID); err != nil || !u.IsAuditor || u.IsAdmin {
		t.Fatalf("auda %+v, %v", u, err)
	}
}

// TestFirstUserNotAuditor (#393): the first user cannot be an auditor, so
// accounts added before any user are never adopted by one.
func TestFirstUserNotAuditor(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	acc := &store.Account{Name: "early", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}}
	if err := st.CreateAccount(ctx, acc); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUserInGroup(ctx, store.NewUser{Name: "aud", PasswordHash: "h", Auditor: true}); !errors.Is(err, store.ErrFirstUserAuditor) {
		t.Fatalf("auditor as first user: %v", err)
	}
	c := connect(t, url)
	if n := countOf(t, c, `SELECT count(*) FROM users`); n != 0 {
		t.Fatalf("%d users", n)
	}
	if n := countOf(t, c, `SELECT count(*) FROM accounts WHERE owner_id IS NULL`); n != 1 {
		t.Fatalf("the early account was adopted (%d unowned)", n)
	}
	admin, err := st.CreateUser(ctx, "admin", "h", true)
	if err != nil {
		t.Fatal(err)
	}
	if a, err := st.GetAccount(ctx, acc.ID); err != nil || a.OwnerID == nil || *a.OwnerID != admin.ID {
		t.Fatalf("the first user does not own the early account: %+v %v", a, err)
	}
	if _, err := st.CreateUserInGroup(ctx, store.NewUser{Name: "aud", PasswordHash: "h", Auditor: true}); err != nil {
		t.Fatalf("auditor as second user: %v", err)
	}
}
