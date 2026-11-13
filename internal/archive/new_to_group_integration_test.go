package archive_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/archive"
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

// expect checks that exactly one insert of groupRaw was recorded, parsed
// or not.
func (p *insertProbe) expect(step string, parsed bool) {
	p.t.Helper()
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
// parse M and pass the fields to the insert, as for a new message; on
// main they pass none. Bob's sync and fill (group A) parse nothing.
func TestNewToGroupWork(t *testing.T) {
	at := time.Date(2010, 3, 5, 10, 0, 0, 0, time.UTC)
	alice := imapmemserver.NewUser("alice@example.com", "secret")
	imaptest.CreateMailboxes(t, alice, "INBOX")
	imaptest.AppendAt(t, alice, "INBOX", []byte(groupRaw), at)
	f := newFixture(t, alice)
	probe := newInsertProbe(t, f.dbURL)

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
	probe.expect("alice's sync, new to the archive", true)
	sync("bob", bob)
	probe.expect("bob's sync, the group holds M", false)
	sync("carol", carol)
	probe.expect("carol's sync, new to group B", true)
	fill(bob, "bob@archive.local")
	probe.expect("bob's fill, the group holds M", false)
	fill(dave, "dave@archive.local")
	probe.expect("dave's fill, new to group C", true)
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

// TestNewToGroupOneGroup: while accounts of one customer group only exist,
// a save does the work it did before 1.6.0 (no lookup, a known message is
// not parsed). The condition is read for every message: when the save of
// the first message of a sync creates an account of a second group (a
// trigger stands in for an admin), the second message of the same run is
// looked up and parsed. Both messages are known to the archive (rows
// without locations) and new to the group.
func TestNewToGroupOneGroup(t *testing.T) {
	at := time.Date(2010, 3, 5, 10, 0, 0, 0, time.UTC)
	first := groupRaw
	second := strings.Replace(groupRaw, "Message-ID: <grant@", "Message-ID: <second@", 1)
	alice := imapmemserver.NewUser("alice@example.com", "secret")
	imaptest.CreateMailboxes(t, alice, "INBOX")
	imaptest.AppendAt(t, alice, "INBOX", []byte(first), at)
	imaptest.AppendAt(t, alice, "INBOX", []byte(second), at)
	f := newFixture(t, alice)
	f.syncer.BatchSize = 1 // the first message commits before the second is fetched
	probe := newInsertProbe(t, f.dbURL)
	var shas []string
	for _, raw := range []string{first, second} {
		blob, _, err := f.blobs.Put(strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		shas = append(shas, blob.SHA256)
		if _, err := probe.conn.Exec(f.ctx, `INSERT INTO messages (sha256, size, stored_path) VALUES ($1, $2, $3)`, blob.SHA256, blob.Size, blob.Path); err != nil {
			t.Fatal(err)
		}
	}
	probe.take()
	if _, err := probe.conn.Exec(f.ctx, `
CREATE FUNCTION test_second_group() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.sha256 = '`+shas[0]+`' THEN
        INSERT INTO customer_groups (name) VALUES ('second');
        INSERT INTO users (name, password_hash, group_id) SELECT 'carol', 'x', id FROM customer_groups WHERE name = 'second';
        INSERT INTO accounts (name, host, port, tls_mode, username, password_enc, owner_id)
        SELECT 'carol', 'h', 993, 'tls', 'u', '\x00', id FROM users WHERE name = 'carol';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER test_second_group BEFORE INSERT ON messages FOR EACH ROW EXECUTE FUNCTION test_second_group();`); err != nil {
		t.Fatal(err)
	}
	owner, err := f.store.CreateUser(f.ctx, "alice", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	a := &store.Account{Name: "work", Host: f.host, Port: f.port, TLSMode: store.TLSModeNone, Username: "alice@example.com", Enabled: true, OwnerID: &owner.ID}
	seal := func(id int64) ([]byte, error) { return archive.SealPassword(f.sealer, id, "secret") }
	if err := f.store.CreateAccountSealed(f.ctx, a, seal); err != nil {
		t.Fatal(err)
	}
	if r := f.syncer.SyncAccount(f.ctx, a); r.Err != nil || r.New != 2 {
		t.Fatalf("sync: %+v", r)
	}
	got := probe.take()
	want := []probed{{sha: shas[0]}, {sha: shas[1], subject: "Foerderbescheid", attachment: true}}
	slices.SortFunc(got, func(x, y probed) int { return slices.Index(shas, x.sha) - slices.Index(shas, y.sha) })
	if !slices.Equal(got, want) {
		t.Errorf("inserts %+v, want %+v (one group: main's work; then the second group's account exists)", got, want)
	}
}
