// Package gobdtest builds the archive the GoBD export tests run on (#394,
// part 2): two customer groups, an account without an owner and a message
// that two customers received, in a throwaway database.
package gobdtest

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/keepia/archive/internal/blobstore"
	"github.com/keepia/archive/internal/store"
	"github.com/keepia/archive/internal/store/storetest"
)

// Fixture is the archive:
//
//	group A "Meier GmbH": anna (account work: INBOX, Archiv), ben (work: INBOX)
//	group B "Schulz KG":  kim (mail: INBOX)
//	no group:             the account old-imap (INBOX), without an owner
//
// Messages: Shared (anna/work/INBOX and kim/mail/INBOX, stored once), Anna
// (anna/work/INBOX and anna/work/Archiv), Ben (ben/work/INBOX), Kim
// (kim/mail/INBOX), Old (old-imap/INBOX).
type Fixture struct {
	St      *store.Store
	URL     string
	Conn    *pgx.Conn
	Blobs   *blobstore.Store
	DataDir string
	Ctx     context.Context

	A, B            *store.Group
	Anna, Ben, Kim  *store.User
	AnnaWork        *store.Account
	BenWork         *store.Account
	KimMail         *store.Account
	OldIMAP         *store.Account
	Folders         map[string]int64 // "anna/work/INBOX" → folder ID
	Shared, AnnaMsg string
	BenMsg, KimMsg  string
	OldMsg          string
	uids            map[int64]uint32
	extra           map[string]*store.Account // "owner/name" → an account of Account
}

// New builds the fixture; the test is skipped without TEST_DATABASE_URL.
func New(t *testing.T) *Fixture {
	t.Helper()
	ctx := context.Background()
	st, url := storetest.NewWithURL(t)
	dataDir := t.TempDir()
	blobs, err := blobstore.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	f := &Fixture{St: st, URL: url, Conn: conn, Blobs: blobs, DataDir: dataDir, Ctx: ctx,
		Folders: map[string]int64{}, uids: map[int64]uint32{}, extra: map[string]*store.Account{}}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	f.A, err = st.GetGroupByName(ctx, "default")
	must(err)
	must(st.RenameGroup(ctx, f.A.ID, "Meier GmbH"))
	f.A.Name = "Meier GmbH"
	f.B, err = st.CreateGroup(ctx, "Schulz KG")
	must(err)
	f.Anna, err = st.CreateUserInGroup(ctx, store.NewUser{Name: "anna", PasswordHash: "h", Admin: true, GroupID: f.A.ID})
	must(err)
	f.Ben, err = st.CreateUserInGroup(ctx, store.NewUser{Name: "ben", PasswordHash: "h", GroupID: f.A.ID})
	must(err)
	f.Kim, err = st.CreateUserInGroup(ctx, store.NewUser{Name: "kim", PasswordHash: "h", GroupID: f.B.ID})
	must(err)
	f.AnnaWork = f.account(t, f.Anna, "work")
	f.BenWork = f.account(t, f.Ben, "work")
	f.KimMail = f.account(t, f.Kim, "mail")
	f.OldIMAP = f.account(t, f.Anna, "old-imap")
	// An account without an owner: the store API never creates one.
	_, err = conn.Exec(ctx, "UPDATE accounts SET owner_id = NULL WHERE id = $1", f.OldIMAP.ID)
	must(err)
	f.OldIMAP.OwnerID = nil

	day := func(y, m, d int) time.Time { return time.Date(y, time.Month(m), d, 9, 0, 0, 0, time.UTC) }
	f.Shared = f.Add(t, "anna/work/INBOX", "Rechnung 2024-17", day(2024, 3, 5))
	f.Add(t, "kim/mail/INBOX", "Rechnung 2024-17", day(2024, 3, 5))
	f.AnnaMsg = f.Add(t, "anna/work/INBOX", "Angebot", day(2024, 12, 31).Add(14*time.Hour+30*time.Minute))
	f.Add(t, "anna/work/Archiv", "Angebot", day(2024, 12, 31).Add(14*time.Hour+30*time.Minute))
	f.BenMsg = f.Add(t, "ben/work/INBOX", "Lieferschein", day(2025, 2, 1))
	f.KimMsg = f.Add(t, "kim/mail/INBOX", "Schulz intern", day(2025, 2, 2))
	f.OldMsg = f.Add(t, "old-imap/INBOX", "Altbestand", day(2019, 6, 1))
	return f
}

// Account adds an IMAP account of owner to the archive, for a test that
// needs one more than the fixture has; Folder and Add then take
// "owner/name/folder".
func (f *Fixture) Account(t *testing.T, owner *store.User, name string) *store.Account {
	t.Helper()
	acc := f.account(t, owner, name)
	f.extra[owner.Name+"/"+name] = acc
	return acc
}

func (f *Fixture) account(t *testing.T, owner *store.User, name string) *store.Account {
	t.Helper()
	acc := &store.Account{Name: name, Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, OwnerID: &owner.ID}
	if err := f.St.CreateAccount(f.Ctx, acc); err != nil {
		t.Fatal(err)
	}
	return acc
}

// Folder returns the ID of "account/folder" (or "owner/account/folder"),
// creating the folder.
func (f *Fixture) Folder(t *testing.T, key string) int64 {
	t.Helper()
	if id, ok := f.Folders[key]; ok {
		return id
	}
	parts := strings.Split(key, "/")
	var acc *store.Account
	switch strings.Join(parts[:len(parts)-1], "/") {
	case "anna/work":
		acc = f.AnnaWork
	case "ben/work":
		acc = f.BenWork
	case "kim/mail":
		acc = f.KimMail
	case "old-imap":
		acc = f.OldIMAP
	default:
		if acc = f.extra[strings.Join(parts[:len(parts)-1], "/")]; acc == nil {
			t.Fatalf("unknown account in %q", key)
		}
	}
	fo, err := f.St.GetOrCreateFolder(f.Ctx, acc.ID, parts[len(parts)-1])
	if err != nil {
		t.Fatal(err)
	}
	f.Folders[key] = fo.ID
	return fo.ID
}

// Body is the message the fixture stores for a subject and date.
func Body(subject string, at time.Time) string {
	return fmt.Sprintf("From: kunde@example.org\r\nTo: buchhaltung@example.org\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%s@example.org>\r\n\r\nHallo,\r\n%s\r\n",
		subject, at.Format(time.RFC1123Z), strings.ReplaceAll(subject, " ", "."), subject)
}

// Add stores a message in the folder "account/folder" and returns its
// SHA-256. The same subject and date give the same message.
func (f *Fixture) Add(t *testing.T, folder, subject string, at time.Time) string {
	t.Helper()
	return f.AddBody(t, folder, Body(subject, at), subject, at)
}

// AddBody stores body in the folder with the given metadata.
func (f *Fixture) AddBody(t *testing.T, folder, body, subject string, at time.Time) string {
	t.Helper()
	id := f.Folder(t, folder)
	b, _, err := f.Blobs.Put(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	f.uids[id]++
	uid := f.uids[id]
	sent := at
	meta := store.MessageMeta{SHA256: b.SHA256, Size: b.Size, StoredPath: b.Path, Subject: subject,
		From: "kunde@example.org", MessageID: "<" + strings.ReplaceAll(subject, " ", ".") + "@example.org>", SentAt: &sent}
	meta.To = "buchhaltung@example.org"
	loc := store.Location{FolderID: id, UIDValidity: 1, UID: uid, Flags: []string{`\Seen`}, InternalDate: at.Add(time.Minute)}
	if _, err := f.St.SaveBatch(f.Ctx, id, uid, []store.MessageMeta{meta}, []store.Location{loc}); err != nil {
		t.Fatal(err)
	}
	return b.SHA256
}
