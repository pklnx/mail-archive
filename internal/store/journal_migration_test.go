package store_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// TestJournalFolderMigration checks 00027: the journal folder INBOX of smtp
// accounts becomes JOURNAL with its retention rules, a dead rule on JOURNAL
// goes, and the locations of earlier fills name their fill.
func TestJournalFolderMigration(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, "owner", "h", true)
	if err != nil {
		t.Fatal(err)
	}
	journal := createSMTPAccount(t, st, &u.ID, "journal", "archive@example.com")
	imap := &store.Account{Name: "mail", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", Enabled: true, OwnerID: &u.ID}
	if err := st.CreateAccount(ctx, imap); err != nil {
		t.Fatal(err)
	}
	downJobsMigration(t, st)
	if m, err := st.MigrateDown(ctx); err != nil || !strings.Contains(m, "00031_account_shares") {
		t.Fatalf("down the account shares: %q, %v", m, err)
	}
	if m, err := st.MigrateDown(ctx); err != nil || !strings.Contains(m, "00030_account_purges") {
		t.Fatalf("down the account purges: %q, %v", m, err)
	}
	if m, err := st.MigrateDown(ctx); err != nil || !strings.Contains(m, "00029_smtp_imports") {
		t.Fatalf("down the smtp imports: %q, %v", m, err)
	}
	if m, err := st.MigrateDown(ctx); err != nil || !strings.Contains(m, "00028_recurring_fills") {
		t.Fatalf("down the recurring fills: %q, %v", m, err)
	}
	if m, err := st.MigrateDown(ctx); err != nil || !strings.Contains(m, "00027_smtp_journal_folder") {
		t.Fatalf("down the journal folder: %q, %v", m, err)
	}

	// The schema of 00026: the journal folder is INBOX, fills write into
	// "<prefix>/<IMAP folder>".
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	id := func(sql string, args ...any) int64 {
		t.Helper()
		var v int64
		if err := conn.QueryRow(ctx, sql, args...).Scan(&v); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return v
	}
	inbox := id("SELECT id FROM folders WHERE account_id = $1", journal.ID)
	exec("INSERT INTO messages (sha256, size, stored_path, sent_at) SELECT lpad(to_hex(i), 64, '0'), 1, 'x', '2020-03-01' FROM generate_series(1, 3) i")
	received := id(`INSERT INTO message_locations (message_sha256, folder_id, uidvalidity, uid, internal_date)
		SELECT lpad('1', 64, '0'), id, uidvalidity, 1, '2020-03-01' FROM folders WHERE id = $1 RETURNING id`, inbox)
	exec("UPDATE folders SET last_uid = 1 WHERE id = $1", inbox)
	exec("INSERT INTO envelopes (location_id, mail_from, rcpt_to, rcpt_text) VALUES ($1, 'a@example.com', '{b@example.com}', 'b@example.com')", received)
	kept := id("INSERT INTO retention_rules (account_id, folder, years) VALUES ($1, 'INBOX', 10) RETURNING id", journal.ID)
	dead := id("INSERT INTO retention_rules (account_id, folder, years) VALUES ($1, 'JOURNAL', 2) RETURNING id", journal.ID)
	imapRule := id("INSERT INTO retention_rules (account_id, folder, years) VALUES ($1, 'INBOX', 5) RETURNING id", imap.ID)
	imapInbox := id("INSERT INTO folders (account_id, name, uidvalidity) VALUES ($1, 'INBOX', 1) RETURNING id", imap.ID)
	fill := id(`INSERT INTO smtp_fills (account_id, host, port, tls_mode, username, folder_prefix, cutoff, status, finished_at)
		VALUES ($1, 'imap.example.com', 993, 'tls', 'alice@example.com', 'alice', now(), 'ok', now()) RETURNING id`, journal.ID)
	legacy := id("INSERT INTO folders (account_id, name, uidvalidity) VALUES ($1, 'alice/INBOX', 1) RETURNING id", journal.ID)
	exec("INSERT INTO smtp_fill_folders (fill_id, source_name, folder_id) VALUES ($1, 'INBOX', $2)", fill, legacy)
	filled := id("INSERT INTO message_locations (message_sha256, folder_id, uidvalidity, uid) VALUES (lpad('2', 64, '0'), $1, 1, 1) RETURNING id", legacy)
	imapLoc := id("INSERT INTO message_locations (message_sha256, folder_id, uidvalidity, uid) VALUES (lpad('3', 64, '0'), $1, 1, 1) RETURNING id", imapInbox)
	retainUntil := func(loc int64) time.Time {
		t.Helper()
		var until time.Time
		if err := conn.QueryRow(ctx, `SELECT location_retain_until(l.folder_id, m.sent_at, l.internal_date, m.created_at)
			FROM message_locations l JOIN messages m ON m.sha256 = l.message_sha256 WHERE l.id = $1`, loc).Scan(&until); err != nil {
			t.Fatal(err)
		}
		return until
	}
	before := retainUntil(received)

	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	folderName := func(id int64) string {
		t.Helper()
		var name string
		if err := conn.QueryRow(ctx, "SELECT name FROM folders WHERE id = $1", id).Scan(&name); err != nil {
			t.Fatal(err)
		}
		return name
	}
	if a, b, c := folderName(inbox), folderName(imapInbox), folderName(legacy); a != "JOURNAL" || b != "INBOX" || c != "alice/INBOX" {
		t.Errorf("folders %q %q %q", a, b, c)
	}
	rules := map[int64]string{}
	rows, _ := conn.Query(ctx, "SELECT id, folder FROM retention_rules")
	for rows.Next() {
		var id int64
		var folder string
		if err := rows.Scan(&id, &folder); err != nil {
			t.Fatal(err)
		}
		rules[id] = folder
	}
	if len(rules) != 2 || rules[kept] != "JOURNAL" || rules[imapRule] != "INBOX" {
		t.Errorf("rules %v (dead %d)", rules, dead)
	}
	if got := retainUntil(received); !got.Equal(before) {
		t.Errorf("retention end %v, was %v", got, before)
	}
	fillOf := func(loc int64) *int64 {
		t.Helper()
		var f *int64
		if err := conn.QueryRow(ctx, "SELECT fill_id FROM message_locations WHERE id = $1", loc).Scan(&f); err != nil {
			t.Fatal(err)
		}
		return f
	}
	if f := fillOf(filled); f == nil || *f != fill {
		t.Errorf("fill of the legacy location %v", f)
	}
	if fillOf(received) != nil || fillOf(imapLoc) != nil {
		t.Error("fill on other locations")
	}
	// The removed rule and the rename are in the audit log.
	type entry struct {
		Action, TargetID, TargetName string
		Details                      map[string]any
	}
	auditBy := func(by string) []entry {
		t.Helper()
		rows, _ := conn.Query(ctx, "SELECT action, target_id, target_name, details FROM audit_log WHERE details->>'by' = $1 ORDER BY id", by)
		entries, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (entry, error) {
			var e entry
			var raw []byte
			if err := r.Scan(&e.Action, &e.TargetID, &e.TargetName, &raw); err != nil {
				return e, err
			}
			return e, json.Unmarshal(raw, &e.Details)
		})
		if err != nil {
			t.Fatal(err)
		}
		return entries
	}
	entries := auditBy("migration 00027")
	if len(entries) != 2 {
		t.Fatalf("audit %+v", entries)
	}
	if e := entries[0]; e.Action != "retention.rule_remove" || e.TargetName != "journal" || e.Details["by"] != "migration 00027" ||
		e.Details["rule_id"] != float64(dead) || e.Details["folder"] != "JOURNAL" || e.Details["years"] != float64(2) {
		t.Errorf("rule removal %+v", e)
	}
	if e := entries[1]; e.Action != "account.change" || e.TargetName != "journal" || e.Details["by"] != "migration 00027" ||
		mustJSON(t, e.Details["folder"]) != `{"from":"INBOX","to":"JOURNAL"}` || mustJSON(t, e.Details["retention_rules"]) != mustJSON(t, []int64{kept}) {
		t.Errorf("rename %+v", e)
	}
	// A new message in the renamed folder is received as before.
	if res, err := st.ReceiveMessage(ctx, journal.ID, receivedMeta(9), envelope("x@example.com"), present); err != nil || res.UID != 2 {
		t.Fatalf("receive after the migration: %+v, %v", res, err)
	}

	// A fill into JOURNAL, set up while the schema is the current one.
	f := newFill(journal, "bob@example.com")
	if err := st.CreateFill(ctx, f, seal); err != nil {
		t.Fatal(err)
	}
	ff, err := st.FillFolder(ctx, f, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	ff.Counts.Imported = 1
	if _, err := st.SaveFillBatch(ctx, ff, []store.MessageMeta{receivedMeta(10)}, []store.Location{{}}); err != nil {
		t.Fatal(err)
	}
	downJobsMigration(t, st)
	if m, err := st.MigrateDown(ctx); err != nil || !strings.Contains(m, "00031_account_shares") {
		t.Fatalf("down the account shares: %q, %v", m, err)
	}
	if m, err := st.MigrateDown(ctx); err != nil || !strings.Contains(m, "00030_account_purges") {
		t.Fatalf("down the account purges: %q, %v", m, err)
	}
	if m, err := st.MigrateDown(ctx); err != nil || !strings.Contains(m, "00029_smtp_imports") {
		t.Fatalf("down the smtp imports: %q, %v", m, err)
	}
	if m, err := st.MigrateDown(ctx); err != nil || !strings.Contains(m, "00028_recurring_fills") {
		t.Fatalf("down the recurring fills: %q, %v", m, err)
	}

	// Down refuses while a rule names INBOX of an smtp account...
	rule, err := st.AddRetentionRule(ctx, store.NewRetentionRule{AccountID: journal.ID, Folder: "INBOX", Years: 3})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.MigrateDown(ctx); err == nil || !strings.Contains(err.Error(), "retention rule on the folder INBOX") {
		t.Fatalf("down with an INBOX rule: %v", err)
	}
	if err := st.RemoveRetentionRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	// ... while JOURNAL holds imported mail, and while a fill into it runs.
	if _, err := st.MigrateDown(ctx); err == nil || !strings.Contains(err.Error(), "imported by a fill") {
		t.Fatalf("down with imported mail: %v", err)
	}
	// Locations are kept, so the test gives the imported one an envelope.
	exec(`INSERT INTO envelopes (location_id, mail_from, rcpt_to, rcpt_text)
		SELECT id, 'a@example.com', '{b@example.com}', 'b@example.com' FROM message_locations WHERE fill_id = $1`, f.ID)
	if _, err := st.MigrateDown(ctx); err == nil || !strings.Contains(err.Error(), "is running") {
		t.Fatalf("down with a running fill: %v", err)
	}
	exec("UPDATE smtp_fills SET status = 'ok', finished_at = now(), password_enc = NULL WHERE id = $1", f.ID)
	// ... and while the prefix the fill would get is another fill's.
	exec("UPDATE smtp_fills SET folder_prefix = $1 WHERE id = $2", "fill-"+strconv.FormatInt(f.ID, 10), fill)
	if _, err := st.MigrateDown(ctx); err == nil || !strings.Contains(err.Error(), "already uses the prefix") {
		t.Fatalf("down with a prefix clash: %v", err)
	}
	exec("UPDATE smtp_fills SET folder_prefix = 'alice' WHERE id = $1", fill)
	downBefore := len(auditBy("migration 00027 down"))
	if m, err := st.MigrateDown(ctx); err != nil || !strings.Contains(m, "00027_smtp_journal_folder") {
		t.Fatalf("down: %q, %v", m, err)
	}
	if name := folderName(inbox); name != "INBOX" {
		t.Errorf("folder after down %q", name)
	}
	var prefix string
	if err := conn.QueryRow(ctx, "SELECT folder_prefix FROM smtp_fills WHERE id = $1", f.ID).Scan(&prefix); err != nil || prefix != "fill-"+strconv.FormatInt(f.ID, 10) {
		t.Errorf("prefix after down %q, %v", prefix, err)
	}
	// The generated prefix and the reverse rename are in the audit log.
	down := auditBy("migration 00027 down")[downBefore:]
	if len(down) != 2 {
		t.Fatalf("audit of the down %+v", down)
	}
	if e := down[0]; e.Action != "account.change" || e.TargetName != "journal" || e.Details["fill"] != float64(f.ID) ||
		e.Details["prefix"] != prefix || mustJSON(t, e.Details["fields"]) != `["fill_prefix"]` {
		t.Errorf("prefix entry %+v", e)
	}
	if e := down[1]; e.Action != "account.change" || e.TargetName != "journal" ||
		mustJSON(t, e.Details["folder"]) != `{"from":"JOURNAL","to":"INBOX"}` || mustJSON(t, e.Details["retention_rules"]) != mustJSON(t, []int64{kept}) {
		t.Errorf("rename entry %+v", e)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
