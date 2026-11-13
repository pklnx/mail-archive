package archive_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/imaptest"
	"github.com/pklnx/mail-archive/internal/store"
)

// The same work for a message new to the customer group (#539, part 3):
// a message that another group already archived is parsed, and its parsed
// fields go to the insert, as for a message new to the archive; within the
// group nothing is parsed (residual).

// groupRaw has an attachment, so that the parse shows in has_attachment.
const groupRaw = "Message-ID: <grant@example.com>\r\nFrom: a@example.com\r\nSubject: Foerderbescheid\r\n" +
	"Date: Fri, 5 Mar 2010 10:00:00 +0000\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n" +
	"--b\r\nContent-Type: text/plain\r\n\r\nDer Bescheid liegt bei.\r\n" +
	"--b\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"Bescheid.pdf\"\r\n\r\nJVBE\r\n--b--\r\n"

func groupSHA() string {
	sum := sha256.Sum256([]byte(groupRaw))
	return hex.EncodeToString(sum[:])
}

// probed is what an insert into messages got: the subject (empty for
// NULL) and has_attachment.
type probed struct {
	sha, subject string
	attachment   bool
}

// insertProbe records every insert into messages with a BEFORE INSERT
// trigger, also one that ON CONFLICT DO NOTHING then discards: what the
// save passed to InsertMessage.
type insertProbe struct {
	t    *testing.T
	conn *pgx.Conn
}

func newInsertProbe(t *testing.T, dbURL string) *insertProbe {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	if _, err := conn.Exec(ctx, `
CREATE TABLE test_insert_probe (sha256 text, subject text, has_attachment boolean);
CREATE FUNCTION test_insert_probe() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO test_insert_probe (sha256, subject, has_attachment) VALUES (NEW.sha256, NEW.subject, NEW.has_attachment);
    RETURN NEW;
END $$;
CREATE TRIGGER test_insert_probe BEFORE INSERT ON messages FOR EACH ROW EXECUTE FUNCTION test_insert_probe();`); err != nil {
		t.Fatal(err)
	}
	return &insertProbe{t: t, conn: conn}
}

// take returns the inserts recorded since the last call.
func (p *insertProbe) take() []probed {
	p.t.Helper()
	ctx := context.Background()
	rows, err := p.conn.Query(ctx, `DELETE FROM test_insert_probe RETURNING sha256, coalesce(subject, ''), has_attachment`)
	if err != nil {
		p.t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (probed, error) {
		var x probed
		err := r.Scan(&x.sha, &x.subject, &x.attachment)
		return x, err
	})
	if err != nil {
		p.t.Fatal(err)
	}
	return got
}

// tempSyncs records the hashes whose temporary file a blob store
// synchronized (blobstore.OnTempSync).
type tempSyncs struct {
	mu     sync.Mutex
	hashes []string
}

func recordTempSyncs(t *testing.T, blobs *blobstore.Store) *tempSyncs {
	r := &tempSyncs{}
	blobs.OnTempSync(func(hash string) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.hashes = append(r.hashes, hash)
	})
	t.Cleanup(func() { blobs.OnTempSync(nil) })
	return r
}

// take returns the hashes recorded since the last call.
func (r *tempSyncs) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	got := r.hashes
	r.hashes = nil
	return got
}

// expect checks that exactly one insert of groupRaw was recorded, parsed
// or not, and, with syncs, that its temporary file was synchronized
// exactly when it was parsed.
func (p *insertProbe) expect(step string, parsed bool, syncs ...*tempSyncs) {
	p.t.Helper()
	for _, r := range syncs {
		if got := r.take(); len(got) != map[bool]int{true: 1, false: 0}[parsed] {
			p.t.Errorf("%s: temporary file synchronized for %v, want parsed=%v", step, got, parsed)
		}
	}
	want := probed{sha: groupSHA()}
	if parsed {
		want.subject, want.attachment = "Foerderbescheid", true
	}
	if got := p.take(); len(got) != 1 || got[0] != want {
		p.t.Errorf("%s: inserts %+v, want [%+v]", step, got, want)
	}
}

// TestNewToGroupWork: alice (group A) syncs M first, new to the archive.
// Carol's sync (group B) and a fill into dave's journal (group C) then
// parse M, synchronize the temporary file and pass the fields to the
// insert, as for a new message; on main they do neither. Bob's sync and
// fill (group A) parse and synchronize nothing.
func TestNewToGroupWork(t *testing.T) {
	at := time.Date(2010, 3, 5, 10, 0, 0, 0, time.UTC)
	alice := imapmemserver.NewUser("alice@example.com", "secret")
	imaptest.CreateMailboxes(t, alice, "INBOX")
	imaptest.AppendAt(t, alice, "INBOX", []byte(groupRaw), at)
	f := newFixture(t, alice)
	probe := newInsertProbe(t, f.dbURL)
	synced := recordTempSyncs(t, f.blobs)

	user := func(name string, group bool) *store.User {
		t.Helper()
		if !group {
			u, err := f.store.CreateUser(f.ctx, name, "h", false)
			if err != nil {
				t.Fatal(err)
			}
			return u
		}
		g, err := f.store.CreateGroup(f.ctx, "group-"+name)
		if err != nil {
			t.Fatal(err)
		}
		u, err := f.store.CreateUserInGroup(f.ctx, store.NewUser{Name: name, PasswordHash: "h", GroupID: g.ID})
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	userA, bob, carol, dave := user("alice", false), user("bob", false), user("carol", true), user("dave", true)
	sync := func(name string, owner *store.User) {
		t.Helper()
		a := &store.Account{Name: name, Host: f.host, Port: f.port, TLSMode: store.TLSModeNone, Username: "alice@example.com", Enabled: true, OwnerID: &owner.ID}
		seal := func(id int64) ([]byte, error) { return archive.SealPassword(f.sealer, id, "secret") }
		if err := f.store.CreateAccountSealed(f.ctx, a, seal); err != nil {
			t.Fatal(err)
		}
		if r := f.syncer.SyncAccount(f.ctx, a); r.Err != nil || r.New != 1 {
			t.Fatalf("sync %s: %+v", name, r)
		}
	}
	fill := func(owner *store.User, addr string) {
		t.Helper()
		j := &store.Account{Name: "journal", Enabled: true, OwnerID: &owner.ID}
		if err := f.store.CreateSMTPAccount(f.ctx, j, []string{addr}); err != nil {
			t.Fatal(err)
		}
		got, err := f.filler().Run(f.ctx, f.createFill(j, "secret").ID)
		if err != nil {
			t.Fatal(err)
		}
		if c := folderOf(got, "INBOX").Counts; c.Imported != 1 {
			t.Fatalf("fill for %s: %+v", owner.Name, c)
		}
	}

	sync("work", userA)
	probe.expect("alice's sync, new to the archive", true, synced)
	sync("bob", bob)
	probe.expect("bob's sync, the group holds M", false, synced)
	sync("carol", carol)
	probe.expect("carol's sync, new to group B", true, synced)
	fill(bob, "bob@archive.local")
	probe.expect("bob's fill, the group holds M", false, synced)
	fill(dave, "dave@archive.local")
	probe.expect("dave's fill, new to group C", true, synced)
}

// TestImportAlwaysFullWork: ./ma import does the full work without a
// group lookup (#539, decision 6848822508 point 4): a second import
// account without an owner (group 0, which holds M through the first)
// parses M again, and so does an import into the journal folder of a
// second smtp account of a group. On main neither parses.
func TestImportAlwaysFullWork(t *testing.T) {
	f := newFixture(t)
	probe := newInsertProbe(t, f.dbURL)
	importInto := func(name string) {
		t.Helper()
		dir := t.TempDir()
		writeEML(t, dir, map[string]string{"export/1.eml": groupRaw}, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
		acc := &store.Account{Kind: store.KindImport, Name: name}
		if err := f.store.CreateAccount(f.ctx, acc); err != nil {
			t.Fatal(err)
		}
		folders, _, err := archive.ImportFolders("eml", dir, "")
		if err != nil {
			t.Fatal(err)
		}
		im := &archive.Importer{Store: f.store, Blobs: f.blobs, Logger: quiet}
		if res, err := im.Import(f.ctx, acc, folders); err != nil || res.Total().New != 1 {
			t.Fatalf("import %s: %v %+v", name, err, res)
		}
	}
	importInto("first")
	probe.expect("first import", true)
	importInto("second")
	probe.expect("second import, group 0 holds M", true)

	// An import into the journal folders of two smtp accounts of one
	// group: the second parses M too.
	owner, err := f.store.CreateUser(f.ctx, "alice", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	journalInto := func(name string) {
		t.Helper()
		acc := &store.Account{Name: name, Enabled: true, OwnerID: &owner.ID}
		if err := f.store.CreateSMTPAccount(f.ctx, acc, []string{name + "@archive.test"}); err != nil {
			t.Fatal(err)
		}
		imp := &store.SMTPImport{AccountID: acc.ID, Label: "piler", Format: "eml", Source: "/import/piler"}
		if err := f.store.CreateSMTPImport(f.ctx, imp); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		writeEML(t, dir, map[string]string{"1.eml": groupRaw}, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
		folders, _, err := archive.JournalImportFolders("eml", dir)
		if err != nil {
			t.Fatal(err)
		}
		im := &archive.Importer{Store: f.store, Blobs: f.blobs, Logger: quiet}
		if res, err := im.ImportJournal(f.ctx, acc, imp, folders); err != nil || res.Total().New != 1 {
			t.Fatalf("journal import %s: %v %+v", name, err, res)
		}
	}
	journalInto("journal-1")
	probe.expect("first journal import", true)
	journalInto("journal-2")
	probe.expect("second journal import, the group holds M", true)
}

// TestBuildMetaNewToGroup: for a message the database knows, BuildMeta
// parses headers and body only when it is new to the group, and keeps
// Known either way, so the row is not written again.
func TestBuildMetaNewToGroup(t *testing.T) {
	f := newFixture(t)
	blob, created, err := f.blobs.Put(strings.NewReader(groupRaw))
	if err != nil {
		t.Fatal(err)
	}
	acc := &store.Account{Kind: store.KindImport, Name: "first"}
	if err := f.store.CreateAccount(f.ctx, acc); err != nil {
		t.Fatal(err)
	}
	folder, err := f.store.GetOrCreateFolder(f.ctx, acc.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	meta, err := archive.BuildMeta(f.ctx, f.store, f.blobs, blob, created, false)
	if err != nil || meta.Known || meta.Subject != "Foerderbescheid" {
		t.Fatalf("new to the archive: %+v %v", meta, err)
	}
	if _, err := f.store.SaveBatch(f.ctx, folder.ID, 1, []store.MessageMeta{meta}, []store.Location{{FolderID: folder.ID, UID: 1}}); err != nil {
		t.Fatal(err)
	}

	same, err := archive.BuildMeta(f.ctx, f.store, f.blobs, blob, false, false)
	if err != nil || !same.Known || same.Subject != "" || same.BodyText != "" || same.HasAttachment {
		t.Errorf("known, the group holds it: %+v %v", same, err)
	}
	other, err := archive.BuildMeta(f.ctx, f.store, f.blobs, blob, false, true)
	if err != nil || !other.Known || other.Subject != "Foerderbescheid" || other.MessageID != "grant@example.com" ||
		!strings.Contains(other.BodyText, "Bescheid liegt bei") || !other.HasAttachment ||
		len(other.AttachmentNames) != 1 || other.AttachmentNames[0] != "Bescheid.pdf" || other.SentAt == nil {
		t.Errorf("known, new to the group: %+v %v", other, err)
	}
}

// TestNewToGroupAfterGroupPurged (review 6853087249): group H archived M,
// then H's only account was purged; M's row and location are gone and its
// file waits out the grace period. A save of M into alice's account
// (group A, now the only group with accounts) finds the file, but counts M
// as new to A: the temporary file is synchronized and M is parsed, as
// while H existed. A shortcut for one-group instances answered "held"
// here, so the work depended on what H held.
func TestNewToGroupAfterGroupPurged(t *testing.T) {
	f := newFixture(t)
	alice, err := f.store.CreateUser(f.ctx, "alice", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	h, err := f.store.CreateGroup(f.ctx, "H")
	if err != nil {
		t.Fatal(err)
	}
	carol, err := f.store.CreateUserInGroup(f.ctx, store.NewUser{Name: "carol", PasswordHash: "h", GroupID: h.ID})
	if err != nil {
		t.Fatal(err)
	}
	account := func(owner *store.User, name string) (*store.Account, *store.Folder) {
		t.Helper()
		a := &store.Account{Name: name, Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, OwnerID: &owner.ID}
		if err := f.store.CreateAccount(f.ctx, a); err != nil {
			t.Fatal(err)
		}
		fo, err := f.store.GetOrCreateFolder(f.ctx, a.ID, "INBOX")
		if err != nil {
			t.Fatal(err)
		}
		return a, fo
	}
	aliceAcc, _ := account(alice, "work")
	carolAcc, carolInbox := account(carol, "old")
	put := func(a *store.Account) archive.Stored {
		t.Helper()
		s, err := archive.PutFor(f.ctx, f.blobs, strings.NewReader(groupRaw), f.store.NewGroupLookup(a.ID))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	stored := put(carolAcc)
	meta, err := archive.BuildMeta(f.ctx, f.store, f.blobs, stored.Blob, stored.Created, stored.NewToGroup)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.SaveBatch(f.ctx, carolInbox.ID, 1, []store.MessageMeta{meta}, []store.Location{{FolderID: carolInbox.ID, UID: 1}}); err != nil {
		t.Fatal(err)
	}
	synced := recordTempSyncs(t, f.blobs)
	if s := put(aliceAcc); s.Created || !s.NewToGroup || len(synced.take()) != 1 {
		t.Fatalf("while H holds M: %+v", s)
	}

	if _, err := f.store.DeleteOrRemoveAccount(f.ctx, carolAcc.Ref()); err != nil {
		t.Fatal(err)
	}
	removed, err := f.store.GetAccount(f.ctx, carolAcc.ID)
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.store.ConfirmPurge(f.ctx, removed.Ref(), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done, err := archive.ExecutePurge(f.ctx, f.store, p.ID, archive.PurgeOptions{}); err != nil || done.State != store.PurgeDone {
		t.Fatalf("purge %+v %v", done, err)
	}
	if exists, err := f.store.MessageExists(f.ctx, stored.Blob.SHA256); err != nil || exists || !f.blobs.Exists(stored.Blob.SHA256) {
		t.Fatalf("after the purge: row %v (%v), file %v; want no row and the file in its grace period", exists, err, f.blobs.Exists(stored.Blob.SHA256))
	}
	if s := put(aliceAcc); s.Created || !s.NewToGroup {
		t.Errorf("after H's purge: %+v, want an existing file new to the group", s)
	}
	if got := synced.take(); len(got) != 1 {
		t.Errorf("after H's purge: temporary file synchronized %d times, want 1", len(got))
	}
}
