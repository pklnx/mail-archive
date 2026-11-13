package store_test

import (
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// The same work for a message new to the customer group (#539, part 3).

// TestGroupLookup: the lookup that sync, fill and SMTP ask before a save
// counts every account of the saving account's group, this account
// included, and group 0 (accounts without an owner) as a group of its own;
// copies in other groups never count, also not in a folder whose account
// left the group after the lookup read the group's folders, and not for
// an account that moved to another group during its folder run.
func TestGroupLookup(t *testing.T) {
	f := newMarksFixture(t)
	unowned := func(name string) *store.Folder {
		a := &store.Account{Name: name, Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}}
		if err := f.st.CreateAccount(f.ctx, a); err != nil {
			t.Fatal(err)
		}
		fo, err := f.st.GetOrCreateFolder(f.ctx, a.ID, "INBOX")
		if err != nil {
			t.Fatal(err)
		}
		return fo
	}
	aliceInbox := f.folder(f.alice, "work", "INBOX")
	aliceOther := f.folder(f.alice, "work", "Archive")
	bobInbox := f.folder(f.bob, "bob", "INBOX")
	carolInbox := f.folder(f.carol, "carol", "INBOX")
	u1, u2 := unowned("import-1"), unowned("import-2")
	m, n, k := hashOf('m'), hashOf('n'), hashOf('k')
	f.save(aliceOther, m, "m", old, old)
	f.save(u1, n, "n", old, old)
	f.save(bobInbox, k, "k", old, old)

	lookups := map[int64]*store.GroupLookup{}
	holds := func(fo *store.Folder, sha string) bool {
		t.Helper()
		l := lookups[fo.AccountID]
		if l == nil {
			l = f.st.NewGroupLookup(fo.AccountID)
			lookups[fo.AccountID] = l
		}
		held, err := l.Holds(f.ctx, sha)
		if err != nil {
			t.Fatal(err)
		}
		return held
	}
	for _, c := range []struct {
		name string
		fo   *store.Folder
		sha  string
		want bool
	}{
		{"the account itself, in another folder", aliceInbox, m, true},
		{"another account of the group", bobInbox, m, true},
		{"another group", carolInbox, m, false},
		{"group 0 for a group's message", u2, m, false},
		{"group 0, another account without an owner", u2, n, true},
		{"a group for group 0's message", aliceInbox, n, false},
		{"a message nobody holds", aliceInbox, hashOf('x'), false},
		{"bob's message, before bob's account leaves the group", aliceInbox, k, true},
	} {
		if got := holds(c.fo, c.sha); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	// Bob's account moves to carol (group B). Alice's lookup read A's
	// folders before: bob's folder no longer counts for A, and carol's
	// lookup, also read before, sees the message from the next folder run.
	bobAccount, err := f.st.GetOwnedAccount(f.ctx, f.bob.ID, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetAccountOwner(f.ctx, bobAccount.Ref(), f.carol.ID, true); err != nil {
		t.Fatal(err)
	}
	if holds(aliceInbox, k) {
		t.Error("a folder that left the group still counts")
	}
	// Bob's own lookup, opened while his account was in A, follows the
	// account into B: A's copy of m no longer counts, his own k does.
	if holds(bobInbox, m) {
		t.Error("after bob's account moved to group B, group A's copy still counts")
	}
	if !holds(bobInbox, k) {
		t.Error("after the move, bob's own copy does not count")
	}
	if holds(carolInbox, k) {
		t.Error("a folder that joined the group counts before the next folder run")
	}
	delete(lookups, carolInbox.AccountID) // the next folder run
	if !holds(carolInbox, k) {
		t.Error("a new lookup of B does not count the folder that joined it")
	}
	// A gone account holds nothing; the save then fails as without the
	// lookup.
	if held, err := f.st.NewGroupLookup(1<<40).Holds(f.ctx, m); err != nil || held {
		t.Errorf("gone account: %v %v", held, err)
	}
}

// TestSaveBatchKnownWithParsedFields: a Known meta with the fields parsed
// for a message new to the group (BuildMeta) goes to InsertMessage with
// those fields; the existing row stays byte-equal and the location is
// stored.
func TestSaveBatchKnownWithParsedFields(t *testing.T) {
	f := newMarksFixture(t)
	aliceInbox := f.folder(f.alice, "work", "INBOX")
	carolInbox := f.folder(f.carol, "carol", "INBOX")
	m := hashOf('m')
	f.save(aliceInbox, m, "first subject", old, old)
	row := func() string {
		t.Helper()
		var s string
		if err := f.conn.QueryRow(f.ctx, `SELECT to_jsonb(m)::text FROM messages m WHERE sha256 = $1`, m).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	before := row()
	meta := store.MessageMeta{
		SHA256: m, Size: 1, StoredPath: "x", Known: true, Subject: "other subject", From: "a@example.com", SentAt: old,
		IndexData: store.IndexData{BodyText: "other body", HasAttachment: true, AttachmentNames: []string{"x.pdf"}},
	}
	f.uid++
	res, err := f.st.SaveBatch(f.ctx, carolInbox.ID, f.uid, []store.MessageMeta{meta}, []store.Location{{FolderID: carolInbox.ID, UID: f.uid, InternalDate: *old}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 || f.locations(carolInbox, m) != 1 {
		t.Errorf("save %+v, locations %d", res, f.locations(carolInbox, m))
	}
	if after := row(); after != before {
		t.Errorf("row changed:\n before %s\n after  %s", before, after)
	}
}

// TestInsertConflictComputesGeneratedColumns is a probe of PostgreSQL that
// part 3 relies on: INSERT ... ON CONFLICT DO NOTHING computes the stored
// generated columns of the new row before it finds the conflict, so that
// an insert of a known message with its parsed fields computes the search
// vector (messages.search) as an insert of a new one does, and then
// writes nothing. The generated column calls a function that raises a
// NOTICE; the conflicting insert must deliver it. It runs on the
// PostgreSQL of CI.
func TestInsertConflictComputesGeneratedColumns(t *testing.T) {
	cfg, err := pgx.ParseConfig(storetest.NewUnmigrated(t))
	if err != nil {
		t.Fatal(err)
	}
	var (
		mu      sync.Mutex
		notices []string
	)
	cfg.OnNotice = func(_ *pgconn.PgConn, n *pgconn.Notice) {
		mu.Lock()
		defer mu.Unlock()
		notices = append(notices, n.Message)
	}
	ctx := context.Background()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, `
CREATE FUNCTION pg_temp.probe_vector(t text) RETURNS tsvector LANGUAGE plpgsql IMMUTABLE AS $$
BEGIN
    RAISE NOTICE 'computed %', t;
    RETURN to_tsvector('simple', t);
END $$;
CREATE TEMP TABLE probe (k int PRIMARY KEY, body text,
    search tsvector GENERATED ALWAYS AS (pg_temp.probe_vector(coalesce(body, ''))) STORED);`); err != nil {
		t.Fatal(err)
	}
	take := func() []string {
		mu.Lock()
		defer mu.Unlock()
		got := notices
		notices = nil
		return got
	}
	insert := func(body string) int64 {
		t.Helper()
		tag, err := conn.Exec(ctx, `INSERT INTO probe (k, body) VALUES (1, $1) ON CONFLICT (k) DO NOTHING`, body)
		if err != nil {
			t.Fatal(err)
		}
		return tag.RowsAffected()
	}
	if n := insert("first"); n != 1 {
		t.Fatalf("first insert: %d rows", n)
	}
	if got := take(); len(got) != 1 || got[0] != "computed first" {
		t.Fatalf("notices of the first insert: %q", got)
	}
	if n := insert("second"); n != 0 {
		t.Fatalf("conflicting insert: %d rows", n)
	}
	if got := take(); len(got) != 1 || got[0] != "computed second" {
		t.Errorf("the conflicting insert did not compute the generated column: notices %q", got)
	}
	var body string
	if err := conn.QueryRow(ctx, `SELECT body FROM probe WHERE k = 1`).Scan(&body); err != nil || body != "first" {
		t.Errorf("row after the conflict: %q %v", body, err)
	}
}
