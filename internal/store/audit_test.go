package store_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

func connect(t *testing.T, url string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func partitions(t *testing.T, conn *pgx.Conn) []string {
	t.Helper()
	rows, err := conn.Query(context.Background(),
		`SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
		 WHERE i.inhparent = 'audit_log'::regclass ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func partitionName(months int) string {
	now := time.Now().UTC()
	m := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, months, 0)
	return fmt.Sprintf("audit_log_%04d_%02d", m.Year(), int(m.Month()))
}

type auditRow struct {
	Actor, Action                   string
	UserID                          *int64
	UserName, TargetType, TargetID  *string
	TargetName, ClientIP, UserAgent *string
	Details                         map[string]any
}

func auditRows(t *testing.T, conn *pgx.Conn) []auditRow {
	t.Helper()
	rows, err := conn.Query(context.Background(),
		`SELECT l.actor, l.action, l.user_id, l.user_name, l.target_type, l.target_id, l.target_name,
		        host(l.client_ip), ua.user_agent, l.details
		 FROM audit_log l LEFT JOIN audit_user_agents ua ON ua.id = l.user_agent_id ORDER BY l.at, l.id`)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (auditRow, error) {
		var a auditRow
		err := r.Scan(&a.Actor, &a.Action, &a.UserID, &a.UserName, &a.TargetType, &a.TargetID, &a.TargetName,
			&a.ClientIP, &a.UserAgent, &a.Details)
		return a, err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAuditPartitions(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	ctx := context.Background()
	want := []string{partitionName(0), partitionName(1), partitionName(2), partitionName(3)}
	if got := partitions(t, conn); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("after migrate: %v, want %v", got, want)
	}
	// Idempotent and safe in parallel: two calls create months 4 and 5 once.
	var wg sync.WaitGroup
	created := make([]int, 2)
	for i := range 2 {
		wg.Go(func() {
			n, err := st.EnsureAuditPartitions(ctx, 5)
			if err != nil {
				t.Error(err)
			}
			created[i] = n
		})
	}
	wg.Wait()
	if created[0]+created[1] != 2 || len(partitions(t, conn)) != 6 {
		t.Fatalf("created %v, partitions %v", created, partitions(t, conn))
	}
	if n, err := st.EnsureAuditPartitions(ctx, 5); err != nil || n != 0 {
		t.Fatalf("again: %d, %v", n, err)
	}
}

func TestAuditAppendOnly(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	e := store.AuditEntry{Actor: store.ActorSession, Action: "auth.login", UserAgent: "test-agent", ClientIP: netip.MustParseAddr("192.0.2.1")}
	if err := st.Audit(ctx, e); err != nil {
		t.Fatal(err)
	}
	part := partitionName(0)
	statements := []string{
		"UPDATE audit_log SET action = 'x.y'",
		"DELETE FROM audit_log",
		"TRUNCATE audit_log",
		"UPDATE " + part + " SET action = 'x.y'",
		"DELETE FROM " + part,
		"TRUNCATE " + part,
		"UPDATE audit_user_agents SET user_agent = 'x'",
		"DELETE FROM audit_user_agents",
		"TRUNCATE audit_user_agents CASCADE",
	}
	for _, replica := range []bool{false, true} {
		conn := connect(t, url)
		if replica {
			if _, err := conn.Exec(ctx, "SET session_replication_role = replica"); err != nil {
				t.Fatal(err)
			}
		}
		for _, sql := range statements {
			if _, err := conn.Exec(ctx, sql); err == nil {
				t.Errorf("replica %v: %s succeeded", replica, sql)
			}
		}
	}
	if rows := auditRows(t, connect(t, url)); len(rows) != 1 || rows[0].Action != "auth.login" {
		t.Fatalf("rows changed: %+v", rows)
	}
}

// TestAuditAppendOnlyOwner checks the privileges of a non-superuser owner,
// which a superuser (as in CI) does not have.
func TestAuditAppendOnlyOwner(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	if err := st.Audit(ctx, store.AuditEntry{Actor: store.ActorCLI, Action: "user.add"}); err != nil {
		t.Fatal(err)
	}
	conn := connect(t, url)
	var canCreate bool
	if err := conn.QueryRow(ctx, "SELECT rolcreaterole OR rolsuper FROM pg_roles WHERE rolname = current_user").Scan(&canCreate); err != nil {
		t.Fatal(err)
	}
	if !canCreate {
		t.Skip("the test role cannot create roles")
	}
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	role := "audit_owner_" + hex.EncodeToString(suffix)
	ident := pgx.Identifier{role}.Sanitize()
	if _, err := conn.Exec(ctx, "CREATE ROLE "+ident); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), url)
		if err != nil {
			return
		}
		defer func() { _ = c.Close(context.Background()) }()
		_, _ = c.Exec(context.Background(), "REASSIGN OWNED BY "+ident+" TO CURRENT_USER")
		_, _ = c.Exec(context.Background(), "DROP OWNED BY "+ident)
		_, _ = c.Exec(context.Background(), "DROP ROLE "+ident)
	})
	// Owner changes carry the owner's (revoked) privileges over.
	for _, tbl := range append([]string{"audit_log", "audit_user_agents"}, partitions(t, conn)...) {
		if _, err := conn.Exec(ctx, "ALTER TABLE "+tbl+" OWNER TO "+ident); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.Exec(ctx, "SET ROLE "+ident); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		"UPDATE audit_log SET action = 'x.y'",
		"DELETE FROM audit_log",
		"TRUNCATE audit_log",
		"TRUNCATE " + partitionName(0),
		"DELETE FROM audit_user_agents",
	} {
		_, err := conn.Exec(ctx, sql)
		if err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%s: %v", sql, err)
		}
	}
	// Inserting still works for the owner.
	if _, err := conn.Exec(ctx, "INSERT INTO audit_log (actor, action) VALUES ('cli', 'user.add')"); err != nil {
		t.Fatalf("insert as owner: %v", err)
	}
}

func TestAuditMigrationDown(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	if err := st.Audit(ctx, store.AuditEntry{Actor: store.ActorCLI, Action: "user.add"}); err != nil {
		t.Fatal(err)
	}
	// First the smtp imports, the recurring fills, the journal folder, the smtp fills, the user settings, the sync intervals, the folder delimiters and the attestations, of which there are none.
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
	if m, err := st.MigrateDown(ctx); err != nil || !strings.Contains(m, "00026_smtp_fills") {
		t.Fatalf("down the smtp fills: %q, %v", m, err)
	}
	if m, err := st.MigrateDown(ctx); err != nil || !strings.Contains(m, "00025_user_settings") {
		t.Fatalf("down the user settings: %q, %v", m, err)
	}
	if m, err := st.MigrateDown(ctx); err != nil || !strings.Contains(m, "00024_account_sync_interval") {
		t.Fatalf("down the sync interval: %q, %v", m, err)
	}
	if m, err := st.MigrateDown(ctx); err != nil || !strings.Contains(m, "00023_folder_delimiter") {
		t.Fatalf("down the folder delimiter: %q, %v", m, err)
	}
	if m, err := st.MigrateDown(ctx); err != nil || !strings.Contains(m, "00022_deletion_runs") {
		t.Fatalf("down the deletion runs: %q, %v", m, err)
	}
	if m, err := st.MigrateDown(ctx); err != nil || !strings.Contains(m, "00021_retention_rules") {
		t.Fatalf("down the retention rules: %q, %v", m, err)
	}
	if m, err := st.MigrateDown(ctx); err != nil || !strings.Contains(m, "00020_api_keys") {
		t.Fatalf("down the API keys: %q, %v", m, err)
	}
	if m, err := st.MigrateDown(ctx); err != nil || !strings.Contains(m, "00019_attestations") {
		t.Fatalf("down the attestations: %q, %v", m, err)
	}
	if _, err := st.MigrateDown(ctx); err == nil || !strings.Contains(err.Error(), "audit log has entries") {
		t.Fatalf("down with entries: %v", err)
	}
	empty := storetest.New(t)
	downJobsMigration(t, empty)
	if m, err := empty.MigrateDown(ctx); err != nil || !strings.Contains(m, "00031_account_shares") {
		t.Fatalf("down the account shares: %q, %v", m, err)
	}
	if m, err := empty.MigrateDown(ctx); err != nil || !strings.Contains(m, "00030_account_purges") {
		t.Fatalf("down the account purges: %q, %v", m, err)
	}
	if m, err := empty.MigrateDown(ctx); err != nil || !strings.Contains(m, "00029_smtp_imports") {
		t.Fatalf("down the smtp imports: %q, %v", m, err)
	}
	if m, err := empty.MigrateDown(ctx); err != nil || !strings.Contains(m, "00028_recurring_fills") {
		t.Fatalf("down the recurring fills: %q, %v", m, err)
	}
	if m, err := empty.MigrateDown(ctx); err != nil || !strings.Contains(m, "00027_smtp_journal_folder") {
		t.Fatalf("down the journal folder: %q, %v", m, err)
	}
	if m, err := empty.MigrateDown(ctx); err != nil || !strings.Contains(m, "00026_smtp_fills") {
		t.Fatalf("down the smtp fills: %q, %v", m, err)
	}
	if m, err := empty.MigrateDown(ctx); err != nil || !strings.Contains(m, "00025_user_settings") {
		t.Fatalf("down the user settings: %q, %v", m, err)
	}
	if m, err := empty.MigrateDown(ctx); err != nil || !strings.Contains(m, "00024_account_sync_interval") {
		t.Fatalf("down the sync interval: %q, %v", m, err)
	}
	if m, err := empty.MigrateDown(ctx); err != nil || !strings.Contains(m, "00023_folder_delimiter") {
		t.Fatalf("down the folder delimiter: %q, %v", m, err)
	}
	if m, err := empty.MigrateDown(ctx); err != nil || !strings.Contains(m, "00022_deletion_runs") {
		t.Fatalf("down the deletion runs: %q, %v", m, err)
	}
	if m, err := empty.MigrateDown(ctx); err != nil || !strings.Contains(m, "00021_retention_rules") {
		t.Fatalf("down the retention rules: %q, %v", m, err)
	}
	if m, err := empty.MigrateDown(ctx); err != nil || !strings.Contains(m, "00020_api_keys") {
		t.Fatalf("down the API keys: %q, %v", m, err)
	}
	if m, err := empty.MigrateDown(ctx); err != nil || !strings.Contains(m, "00019_attestations") {
		t.Fatalf("down the attestations: %q, %v", m, err)
	}
	if m, err := empty.MigrateDown(ctx); err != nil || !strings.Contains(m, "00018_audit_log") {
		t.Fatalf("down without entries: %q, %v", m, err)
	}
}

func TestAuditWithChange(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	ctx := context.Background()
	e := &store.AuditEntry{Actor: store.ActorCLI, Action: "user.add"}
	e.SetTarget(store.TargetUser, "", "alice")
	actx := store.WithAudit(ctx, e)
	u, err := st.CreateUser(actx, "alice", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	// A second transaction with the same context writes nothing.
	if err := st.SetUserAdmin(actx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	rows := auditRows(t, conn)
	if len(rows) != 1 || *rows[0].TargetID != fmt.Sprint(u.ID) || *rows[0].TargetName != "alice" {
		t.Fatalf("rows %+v", rows)
	}
	// A failed change writes no entry.
	if _, err := st.CreateUser(store.WithAudit(ctx, &store.AuditEntry{Actor: store.ActorCLI, Action: "user.add"}), "alice", "h", false); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	if n := len(auditRows(t, conn)); n != 1 {
		t.Fatalf("%d rows after a failed change", n)
	}
	// An entry that cannot be written rolls the change back.
	bad := &store.AuditEntry{Actor: store.ActorCLI, Action: "not valid"}
	if _, err := st.CreateUser(store.WithAudit(ctx, bad), "bob", "h", false); !errors.Is(err, store.ErrAuditUnavailable) {
		t.Fatalf("invalid entry: %v", err)
	}
	if _, err := st.GetUserByName(ctx, "bob"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("bob was created: %v", err)
	}
}

// A user with log rows can be deleted; the rows keep the ID and name.
func TestAuditUserRemoval(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, "alice", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	e := store.AuditEntry{Actor: store.ActorSession, Action: "auth.login"}
	e.SetUser(u.ID, u.Name)
	if err := st.Audit(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	rows := auditRows(t, connect(t, url))
	if len(rows) != 1 || rows[0].UserID == nil || *rows[0].UserID != u.ID || *rows[0].UserName != "alice" {
		t.Fatalf("rows %+v", rows)
	}
}

// detachCurrentMonth takes the current month's partition away, as an
// archival would, and renames it so that it can be created again.
func detachCurrentMonth(t *testing.T, conn *pgx.Conn) {
	t.Helper()
	ctx := context.Background()
	part := partitionName(0)
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	for _, sql := range []string{
		"ALTER TABLE audit_log DETACH PARTITION " + part,
		"ALTER TABLE " + part + " RENAME TO detached_" + hex.EncodeToString(suffix),
	} {
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAuditMissingPartition(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	ctx := context.Background()
	detachCurrentMonth(t, conn)
	// A single insert creates the partition and tries again.
	if err := st.Audit(ctx, store.AuditEntry{Actor: store.ActorCLI, Action: "data.export"}); err != nil {
		t.Fatalf("audit without partition: %v", err)
	}
	if n := len(auditRows(t, conn)); n != 1 {
		t.Fatalf("%d rows", n)
	}
	// In a transaction it fails once; the next attempt works.
	detachCurrentMonth(t, conn)
	e := &store.AuditEntry{Actor: store.ActorCLI, Action: "user.add"}
	if _, err := st.CreateUser(store.WithAudit(ctx, e), "alice", "h", false); !errors.Is(err, store.ErrAuditUnavailable) {
		t.Fatalf("in a transaction: %v", err)
	}
	if _, err := st.CreateUser(store.WithAudit(ctx, &store.AuditEntry{Actor: store.ActorCLI, Action: "user.add"}), "alice", "h", false); err != nil {
		t.Fatalf("next attempt: %v", err)
	}
	if n := len(auditRows(t, conn)); n != 1 {
		t.Fatalf("%d rows in the new partition", n)
	}
}

func TestAuditCleansInput(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	// A multi-byte character across byte 256 of the user agent.
	ua := strings.Repeat("a", 255) + "é" + strings.Repeat("b", 10)
	e := store.AuditEntry{Actor: store.ActorSession, Action: "message.search", UserAgent: ua + "\x00\xff"}
	e.Detail("q", "nul\x00 and \xff invalid")
	e.Detail("list", []string{"x\x00"})
	if err := st.Audit(ctx, e); err != nil {
		t.Fatalf("audit: %v", err)
	}
	rows := auditRows(t, connect(t, url))
	got := *rows[0].UserAgent
	if !utf8.ValidString(got) || len(got) > 256 || got != strings.Repeat("a", 255) {
		t.Errorf("user agent %q (%d bytes)", got, len(got))
	}
	if q := rows[0].Details["q"]; q != "nul� and � invalid" {
		t.Errorf("q %q", q)
	}
}

// Parallel inserts of the same new user agent get one id, also from two
// stores with separate caches.
func TestAuditUserAgentRace(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	other, err := store.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := range 20 {
		s := st
		if i%2 == 1 {
			s = other
		}
		wg.Go(func() {
			if err := s.Audit(ctx, store.AuditEntry{Actor: store.ActorSession, Action: "auth.login", UserAgent: "same agent"}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	var agents, rows int
	conn := connect(t, url)
	if err := conn.QueryRow(ctx, "SELECT (SELECT count(*) FROM audit_user_agents), (SELECT count(DISTINCT user_agent_id) FROM audit_log)").Scan(&agents, &rows); err != nil {
		t.Fatal(err)
	}
	if agents != 1 || rows != 1 {
		t.Fatalf("%d user agents, %d distinct ids", agents, rows)
	}
}

// TestAuditUserAgentFewConnections runs more transactions than the pool has
// connections, all waiting for one row lock, each with an audit entry
// whose user agent is new. The user agent must be looked up before the
// transaction starts: the lock holder would otherwise wait for a
// connection that only the transactions waiting for its lock hold.
func TestAuditUserAgentFewConnections(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	u, err := st.CreateUser(context.Background(), "alice", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	small, err := store.Open(context.Background(), url+"&pool_max_conns=2")
	if err != nil {
		t.Fatal(err)
	}
	defer small.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := range 6 {
		wg.Go(func() {
			e := &store.AuditEntry{Actor: store.ActorSession, Action: "user.reset_2fa", UserAgent: fmt.Sprintf("agent %d", i)}
			if err := small.ResetTwoFactor(store.WithAudit(ctx, e), u.ID); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}

func TestExportAudit(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice, err := st.CreateUser(ctx, "alice", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{"auth.login", "message.open", "user.2fa_reset", "message.search"} {
		e := store.AuditEntry{Actor: store.ActorSession, Action: a}
		if a != "user.2fa_reset" {
			e.SetUser(alice.ID, alice.Name)
		}
		if err := st.Audit(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	collect := func(f store.AuditFilter) []string {
		var out []string
		if err := st.ExportAudit(ctx, f, func(r store.AuditRecord) error {
			out = append(out, r.Action)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return strings.Fields(strings.Join(out, " "))
	}
	all := store.AuditFilter{Since: time.Now().Add(-time.Hour), Until: time.Now().Add(time.Hour)}
	if got := collect(all); strings.Join(got, " ") != "auth.login message.open user.2fa_reset message.search" {
		t.Errorf("all: %v", got)
	}
	f := all
	f.UserID = &alice.ID
	f.Actions = []string{"read", "login"}
	if got := collect(f); strings.Join(got, " ") != "auth.login message.open" {
		t.Errorf("alice, read and login: %v", got)
	}
	f = all
	f.Actions = []string{"user.2fa_reset"}
	if got := collect(f); strings.Join(got, " ") != "user.2fa_reset" {
		t.Errorf("exact: %v", got)
	}
	f.Until = time.Now().Add(-time.Minute)
	if got := collect(f); len(got) != 0 {
		t.Errorf("before: %v", got)
	}
	if _, err := store.AuditActionPatterns([]string{"nonsense"}); err == nil {
		t.Error("unknown action accepted")
	}
	if p, _ := store.AuditActionPatterns([]string{"user.2fa_reset"}); p[0] != `user.2fa\_reset` {
		t.Errorf("underscore not escaped: %v", p)
	}
}

// Admins narrow the Log page to several users and actors: an entry matches
// if its user is one of them or its actor is (a union), and the actions
// still apply to every match.
func TestListAuditWho(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	users := map[string]int64{}
	for _, name := range []string{"admin", "alice", "bob", "carol"} {
		u, err := st.CreateUser(ctx, name, "h", name == "admin")
		if err != nil {
			t.Fatal(err)
		}
		users[name] = u.ID
	}
	for _, e := range []struct{ user, actor, action string }{
		{"alice", store.ActorSession, "message.open"},
		{"alice", store.ActorSession, "auth.login"},
		{"bob", store.ActorSession, "message.open"},
		{"bob", store.ActorAPIKey, "message.search"},
		{"carol", store.ActorSession, "message.open"},
		{"", store.ActorCLI, "data.backup"},
	} {
		entry := store.AuditEntry{Actor: e.actor, Action: e.action}
		if e.user != "" {
			entry.SetUser(users[e.user], e.user)
		}
		if e.actor == store.ActorAPIKey {
			keyID := int64(1) // no foreign key: entries outlive their keys
			entry.APIKeyID, entry.APIKeyName = &keyID, "script"
		}
		if err := st.Audit(ctx, entry); err != nil {
			t.Fatal(err)
		}
	}
	list := func(p store.AuditPage) string {
		t.Helper()
		p.Viewer, p.All, p.Since, p.Until, p.Limit = users["admin"], true, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), 100
		recs, err := st.ListAudit(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range recs {
			out = append(out, r.UserName+"/"+r.Actor+"/"+r.Action)
		}
		slices.Sort(out)
		return strings.Join(out, " ")
	}
	id := func(names ...string) []int64 {
		var out []int64
		for _, n := range names {
			out = append(out, users[n])
		}
		return out
	}
	all := "/cli/data.backup alice/session/auth.login alice/session/message.open bob/api_key/message.search bob/session/message.open carol/session/message.open"
	for _, c := range []struct {
		name string
		p    store.AuditPage
		want string
	}{
		{"nil means all", store.AuditPage{}, all},
		{"empty means all", store.AuditPage{UserIDs: []int64{}, Actors: []string{}}, all},
		{"one user", store.AuditPage{UserIDs: id("alice")}, "alice/session/auth.login alice/session/message.open"},
		{"two users", store.AuditPage{UserIDs: id("alice", "bob")},
			"alice/session/auth.login alice/session/message.open bob/api_key/message.search bob/session/message.open"},
		{"cli without a user", store.AuditPage{Actors: []string{store.ActorCLI}}, "/cli/data.backup"},
		{"two actors", store.AuditPage{Actors: []string{store.ActorCLI, store.ActorAPIKey}}, "/cli/data.backup bob/api_key/message.search"},
		{"user or actor", store.AuditPage{UserIDs: id("alice"), Actors: []string{store.ActorCLI}},
			"/cli/data.backup alice/session/auth.login alice/session/message.open"},
		{"overlap once", store.AuditPage{UserIDs: id("bob"), Actors: []string{store.ActorAPIKey}},
			"bob/api_key/message.search bob/session/message.open"},
		{"with actions", store.AuditPage{UserIDs: id("alice"), Actors: []string{store.ActorAPIKey}, Actions: []string{"read", "search"}},
			"alice/session/message.open bob/api_key/message.search"},
	} {
		if got := list(c.p); got != c.want {
			t.Errorf("%s:\n got  %s\n want %s", c.name, got, c.want)
		}
	}
}

// TestAuditSize measures the storage per typical row for the docs.
func TestAuditSize(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	if err := st.Audit(ctx, store.AuditEntry{Actor: store.ActorSession, Action: "auth.login",
		UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"}); err != nil {
		t.Fatal(err)
	}
	conn := connect(t, url)
	const n = 10000
	// A mix like a busy user's day: opened messages, searches, parts.
	if _, err := conn.Exec(ctx, `
		INSERT INTO audit_log (actor, user_id, user_name, action, target_type, target_id, client_ip, user_agent_id, details)
		SELECT 'session', 1, 'alice',
		       CASE i % 4 WHEN 0 THEN 'message.search' WHEN 3 THEN 'message.part' ELSE 'message.open' END,
		       CASE i % 4 WHEN 0 THEN NULL ELSE 'message' END,
		       CASE i % 4 WHEN 0 THEN NULL ELSE encode(sha256(i::text::bytea), 'hex') END,
		       '192.0.2.10', 1,
		       CASE i % 4 WHEN 0 THEN '{"q": "invoice 2026", "account": "work"}'::jsonb
		                  WHEN 3 THEN '{"index": 2, "inline": true, "content_type": "image/png"}'::jsonb
		                  ELSE '{}'::jsonb END
		FROM generate_series(1, $1) i`, n); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "VACUUM ANALYZE audit_log"); err != nil {
		t.Fatal(err)
	}
	var size int64
	if err := conn.QueryRow(ctx, `SELECT sum(pg_total_relation_size(inhrelid)) FROM pg_inherits WHERE inhparent = 'audit_log'::regclass`).Scan(&size); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d bytes per row, including indexes", size/(n+1))
}
