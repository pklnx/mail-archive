package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

func TestCreateSMTPImport(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, "owner", "h", true)
	if err != nil {
		t.Fatal(err)
	}
	journal := createSMTPAccount(t, st, &u.ID, "journal", "archive@example.com")
	imported := &store.Account{Kind: store.KindImport, Name: "piler", OwnerID: &u.ID}
	imap := &store.Account{Name: "mail", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", OwnerID: &u.ID}
	removed := createSMTPAccount(t, st, &u.ID, "gone", "gone@example.com")
	for _, a := range []*store.Account{imported, imap} {
		if err := st.CreateAccount(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.Exec(ctx, "UPDATE accounts SET removed_at = now() WHERE id = $1", removed.ID); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		acc  *store.Account
		want error
	}{{imported, store.ErrNotSMTP}, {imap, store.ErrNotSMTP}, {removed, store.ErrNotFound}, {&store.Account{ID: 999999}, store.ErrNotFound}} {
		imp := &store.SMTPImport{AccountID: c.acc.ID, Label: "x", Format: "eml", Source: "/import/x"}
		if err := st.CreateSMTPImport(ctx, imp); !errors.Is(err, c.want) || imp.ID != 0 {
			t.Errorf("%q: %v, want %v", c.acc.Name, err, c.want)
		}
	}
	for _, label := range []string{"", "a\x00b", "line\nbreak", strings.Repeat("ä", store.MaxImportLabel+1)} {
		if err := st.CreateSMTPImport(ctx, &store.SMTPImport{AccountID: journal.ID, Label: label, Format: "eml"}); !errors.Is(err, store.ErrImportLabel) {
			t.Errorf("label %q: %v", label, err)
		}
	}
	if err := store.CheckImportLabel(strings.Repeat("ä", store.MaxImportLabel)); err != nil {
		t.Errorf("200 characters: %v", err)
	}

	e := &store.AuditEntry{Actor: store.ActorCLI, Action: "data.import"}
	e.SetTarget(store.TargetAccount, strconv.FormatInt(journal.ID, 10), journal.Name)
	imp := &store.SMTPImport{AccountID: journal.ID, Label: "piler", Format: "eml", Source: "/import/piler-alice"}
	if err := st.CreateSMTPImport(store.WithAudit(ctx, e), imp); err != nil || imp.ID == 0 || imp.CreatedAt.IsZero() {
		t.Fatalf("create %+v, %v", imp, err)
	}
	var details []byte
	if err := conn.QueryRow(ctx, "SELECT details FROM audit_log WHERE action = 'data.import' AND target_id = $1",
		strconv.FormatInt(journal.ID, 10)).Scan(&details); err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	if err := json.Unmarshal(details, &d); err != nil || d["import"] != float64(imp.ID) {
		t.Errorf("audit details %s, %v", details, err)
	}
}

// TestSaveJournalImportBatch checks that an import of files numbers its
// locations after deliveries made meanwhile, names the import, counts a
// byte-identical delivered copy as present, adds nothing on a second call
// and leaves out a message deleted by retention.
func TestSaveJournalImportBatch(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, "owner", "h", true)
	if err != nil {
		t.Fatal(err)
	}
	a := createSMTPAccount(t, st, &u.ID, "journal", "archive@example.com")
	folderID, err := st.JournalFolderID(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	imp := &store.SMTPImport{AccountID: a.ID, Label: "piler", Format: "eml", Source: "/import/piler"}
	if err := st.CreateSMTPImport(ctx, imp); err != nil {
		t.Fatal(err)
	}
	// Delivered after the import's pre-check: 1 has the same bytes as a
	// message of the batch, 9 is another message.
	for _, sha := range []byte{1, 9} {
		if _, err := st.ReceiveMessage(ctx, a.ID, receivedMeta(sha), envelope("x@example.com"), present); err != nil {
			t.Fatal(err)
		}
	}
	date := time.Date(2019, 5, 1, 8, 0, 0, 0, time.UTC)
	metas := []store.MessageMeta{receivedMeta(3), receivedMeta(1), receivedMeta(2), receivedMeta(3)}
	locs := []store.Location{{InternalDate: date, Flags: []string{`\Seen`}}, {}, {UID: 1}, {}}
	res, present, err := st.SaveJournalImportBatch(ctx, folderID, imp.ID, metas, locs)
	if err != nil || res.New != 2 || res.Skipped != 0 || present != 2 {
		t.Fatalf("batch %+v, present %d, %v", res, present, err)
	}
	type row struct {
		sha            string
		uid            int64
		fill, imported *int64
		envelope       bool
	}
	rows := func() map[string]row {
		t.Helper()
		r, err := conn.Query(ctx, `SELECT l.message_sha256, l.uid, l.fill_id, l.import_id, e.location_id IS NOT NULL
			FROM message_locations l LEFT JOIN envelopes e ON e.location_id = l.id WHERE l.folder_id = $1`, folderID)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		out := map[string]row{}
		for r.Next() {
			var x row
			if err := r.Scan(&x.sha, &x.uid, &x.fill, &x.imported, &x.envelope); err != nil {
				t.Fatal(err)
			}
			if _, dup := out[x.sha]; dup {
				t.Errorf("two locations of %s", x.sha)
			}
			out[x.sha] = x
		}
		return out
	}
	got := rows()
	if len(got) != 4 {
		t.Fatalf("locations %+v", got)
	}
	uids := map[int64]bool{}
	for _, sha := range []byte{2, 3} {
		x := got[receivedMeta(sha).SHA256]
		if x.envelope || x.fill != nil || x.imported == nil || *x.imported != imp.ID || x.uid < 3 {
			t.Errorf("imported %d: %+v", sha, x)
		}
		uids[x.uid] = true
	}
	if len(uids) != 2 {
		t.Errorf("UIDs %v", uids)
	}
	if x := got[receivedMeta(1).SHA256]; !x.envelope || x.imported != nil {
		t.Errorf("delivered copy %+v", x)
	}
	var lastUID int64
	if err := conn.QueryRow(ctx, "SELECT last_uid FROM folders WHERE id = $1", folderID).Scan(&lastUID); err != nil || lastUID != 4 {
		t.Errorf("last UID %d, %v", lastUID, err)
	}
	d, err := st.GetMessageDetail(ctx, storetest.Viewer(t, st, u.ID), receivedMeta(3).SHA256)
	if err != nil || len(d.Locations) != 1 {
		t.Fatalf("detail %+v, %v", d, err)
	}
	if l := d.Locations[0]; !l.Filled || l.Fill != nil || l.Import == nil || l.Import.ID != imp.ID || l.Import.Label != "piler" ||
		l.Envelope != nil || l.InternalDate == nil || !l.InternalDate.Equal(date) {
		t.Errorf("location %+v", l)
	}
	if d, err := st.GetMessageDetail(ctx, storetest.Viewer(t, st, u.ID), receivedMeta(9).SHA256); err != nil || d.Locations[0].Import != nil {
		t.Errorf("delivered location %+v, %v", d, err)
	}

	// Again: everything is there.
	res, present, err = st.SaveJournalImportBatch(ctx, folderID, imp.ID, metas, locs)
	if err != nil || res.New != 0 || present != 4 || len(rows()) != 4 {
		t.Fatalf("again %+v, present %d, %v", res, present, err)
	}

	// Deleted by retention: a rule on JOURNAL has ended the message's
	// period, so it is not stored again.
	if _, err := st.AddRetentionRule(ctx, store.NewRetentionRule{AccountID: a.ID, Folder: store.SMTPFolder, Years: 1}); err != nil {
		t.Fatal(err)
	}
	var run int64
	if err := conn.QueryRow(ctx, `INSERT INTO deletion_runs (state, confirmed_at, finished_at) VALUES ('done', now(), now()) RETURNING id`).Scan(&run); err != nil {
		t.Fatal(err)
	}
	deleted := receivedMeta(5)
	sent := time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC)
	deleted.SentAt = &sent
	if _, err := conn.Exec(ctx, `INSERT INTO deleted_messages (sha256, size, message_seq, archived_at, retain_until, locations, run_id)
		VALUES ($1, 10, 1, '2010-01-02', '2011-01-02', 1, $2)`, deleted.SHA256, run); err != nil {
		t.Fatal(err)
	}
	res, present, err = st.SaveJournalImportBatch(ctx, folderID, imp.ID, []store.MessageMeta{deleted}, []store.Location{{InternalDate: sent}})
	if err != nil || res.Skipped != 1 || res.New != 0 || present != 0 || len(rows()) != 4 {
		t.Fatalf("deleted by retention %+v, present %d, %v", res, present, err)
	}
}

// TestSMTPImportsMigration checks 00029 down and up on a database with
// imported locations: the locations stay, and lose only the import.
func TestSMTPImportsMigration(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	ctx := context.Background()
	a := createSMTPAccount(t, st, nil, "journal", "archive@example.com")
	folderID, err := st.JournalFolderID(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	imp := &store.SMTPImport{AccountID: a.ID, Label: "piler", Format: "mbox", Source: "/import/piler"}
	if err := st.CreateSMTPImport(ctx, imp); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.SaveJournalImportBatch(ctx, folderID, imp.ID, []store.MessageMeta{receivedMeta(1)}, []store.Location{{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReceiveMessage(ctx, a.ID, receivedMeta(2), envelope("x@example.com"), present); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		t.Helper()
		var n int
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM message_locations WHERE folder_id = $1", folderID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if m, err := st.MigrateDown(ctx); err != nil || !strings.Contains(m, "00031_account_shares") {
		t.Fatalf("down the account shares: %q, %v", m, err)
	}
	if m, err := st.MigrateDown(ctx); err != nil || !strings.Contains(m, "00030_account_purges") {
		t.Fatalf("down the account purges: %q, %v", m, err)
	}
	if m, err := st.MigrateDown(ctx); err != nil || !strings.Contains(m, "00029_smtp_imports") {
		t.Fatalf("down: %q, %v", m, err)
	}
	var exists bool
	if err := conn.QueryRow(ctx, "SELECT to_regclass('smtp_imports') IS NOT NULL").Scan(&exists); err != nil || exists {
		t.Fatalf("smtp_imports after down: %v %v", exists, err)
	}
	if n := count(); n != 2 {
		t.Fatalf("%d locations after down", n)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 2 {
		t.Fatalf("%d locations after up", n)
	}
	var withImport int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM message_locations WHERE import_id IS NOT NULL").Scan(&withImport); err != nil || withImport != 0 {
		t.Fatalf("%d locations with an import, %v", withImport, err)
	}
	// The format is checked.
	if _, err := conn.Exec(ctx, "INSERT INTO smtp_imports (account_id, label, format, source) VALUES ($1, 'x', 'pst', 'y')", a.ID); err == nil {
		t.Error("format pst accepted")
	}
	// Deleting an account takes its imports along.
	empty := createSMTPAccount(t, st, nil, "empty", "empty@example.com")
	if err := st.CreateSMTPImport(ctx, &store.SMTPImport{AccountID: empty.ID, Label: "again", Format: "eml", Source: "/import/x"}); err != nil {
		t.Fatal(err)
	}
	if r, err := st.DeleteOrRemoveAccount(ctx, empty.Ref()); err != nil || r != store.AccountDeleted {
		t.Fatalf("delete: %v %v", r, err)
	}
	var left int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM smtp_imports WHERE account_id = $1", empty.ID).Scan(&left); err != nil || left != 0 {
		t.Errorf("%d imports left, %v", left, err)
	}
}
