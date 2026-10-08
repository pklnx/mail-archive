package store_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/db"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

type searchMsg struct {
	sha     string
	id      string // Message-ID
	subject string
	from    string
	sent    string // RFC 3339
	body    string
	data    store.IndexData
}

// searchFixture stores messages in two accounts of one user: "privat"
// (INBOX, Sent) and "arbeit" (INBOX).
type searchFixture struct {
	t       *testing.T
	ctx     context.Context
	st      *store.Store
	owner   int64
	folders map[string]int64 // "account/folder"
	uid     uint32
}

func newSearchFixture(t *testing.T) *searchFixture {
	t.Helper()
	f := &searchFixture{t: t, ctx: context.Background(), st: storetest.New(t), folders: map[string]int64{}}
	u, err := f.st.CreateUser(f.ctx, "alice", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	f.owner = u.ID
	accounts := map[string]int64{}
	for _, path := range []string{"privat/INBOX", "privat/Sent", "arbeit/INBOX"} {
		account, folder, _ := strings.Cut(path, "/")
		if _, ok := accounts[account]; !ok {
			acc := &store.Account{Name: account, Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, OwnerID: &u.ID}
			if err := f.st.CreateAccount(f.ctx, acc); err != nil {
				t.Fatal(err)
			}
			accounts[account] = acc.ID
		}
		fo, err := f.st.GetOrCreateFolder(f.ctx, accounts[account], folder)
		if err != nil {
			t.Fatal(err)
		}
		f.folders[path] = fo.ID
	}
	return f
}

func (f *searchFixture) add(path string, m searchMsg) {
	f.t.Helper()
	sent, err := time.Parse(time.RFC3339, m.sent)
	if err != nil {
		f.t.Fatal(err)
	}
	m.data.BodyText = m.body
	meta := store.MessageMeta{
		SHA256: m.sha, Size: 1, StoredPath: m.sha, MessageID: m.id, Subject: m.subject, From: m.from, SentAt: &sent, IndexData: m.data,
	}
	f.uid++
	folder := f.folders[path]
	loc := store.Location{FolderID: folder, UIDValidity: 1, UID: f.uid}
	if _, err := f.st.SaveBatch(f.ctx, folder, f.uid, []store.MessageMeta{meta}, []store.Location{loc}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *searchFixture) search(filter store.SearchFilter) []string {
	f.t.Helper()
	filter.Owner = f.owner
	if filter.Limit == 0 {
		filter.Limit = 50
	}
	rows, err := f.st.SearchMessages(f.ctx, filter)
	if err != nil {
		f.t.Fatal(err)
	}
	out := []string{}
	for _, r := range rows {
		out = append(out, strings.TrimSpace(r.SHA256)) // CHAR(64) pads short test hashes
	}
	return out
}

func date(s string) *time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return &t
}

func TestSearchFilters(t *testing.T) {
	f := newSearchFixture(t)
	f.add("privat/Sent", searchMsg{
		sha: "steuer", subject: "Einspruch Steuerbescheid", from: "Alice <alice@example.com>", sent: "2024-03-10T09:00:00Z",
		body: "Sehr geehrte Damen und Herren, anbei der Einspruch.",
		data: store.IndexData{
			To: "Finanzamt Köln <poststelle@fa-koeln.example>", Cc: "Steuerberater Weber <weber@kanzlei.example>",
			AttachmentNames: []string{"Einspruch.pdf", "Belege 2023.zip"}, HasAttachment: true,
		},
	})
	f.add("privat/INBOX", searchMsg{
		sha: "bescheid", subject: "Ihr Bescheid", from: "Finanzamt Köln <poststelle@fa-koeln.example>", sent: "2024-01-15T09:00:00Z",
		body: "Den Bescheid finden Sie im Anhang.",
		data: store.IndexData{To: "alice@example.com", AttachmentNames: []string{"Bescheid_2023.pdf"}, HasAttachment: true},
	})
	f.add("privat/INBOX", searchMsg{
		sha: "rabatt", subject: "100% Rabatt", from: "shop@example.com", sent: "2023-06-01T09:00:00Z",
		body: "Nur heute.",
		data: store.IndexData{To: "alice+100%@example.com"},
	})
	f.add("arbeit/INBOX", searchMsg{
		sha: "meeting", subject: "Termin", from: "Bob <bob@firma.example>", sent: "2023-01-20T09:00:00Z",
		body: "Termin mit dem Finanzamt am Montag.",
		data: store.IndexData{To: "alice@firma.example", Cc: "team_lead@firma.example", HasAttachment: true}, // unnamed attachment
	})

	cases := []struct {
		name   string
		filter store.SearchFilter
		want   []string
	}{
		{"all", store.SearchFilter{}, []string{"steuer", "bescheid", "rabatt", "meeting"}},
		{"to name", store.SearchFilter{To: "finanzamt"}, []string{"steuer"}},
		{"to address", store.SearchFilter{To: "poststelle@FA-koeln"}, []string{"steuer"}},
		{"to matches cc", store.SearchFilter{To: "weber"}, []string{"steuer"}},
		{"to escapes %", store.SearchFilter{To: "+100%@"}, []string{"rabatt"}},
		{"to escapes _", store.SearchFilter{To: "team_lead"}, []string{"meeting"}},
		{"to _ is literal", store.SearchFilter{To: "team_"}, []string{"meeting"}},
		{"to % is literal", store.SearchFilter{To: "alice%"}, []string{}},
		{"from", store.SearchFilter{From: "Finanzamt"}, []string{"bescheid"}},
		{"from address", store.SearchFilter{From: "bob@"}, []string{"meeting"}},
		{"attachment", store.SearchFilter{Attachment: "einspruch"}, []string{"steuer"}},
		{"attachment second name", store.SearchFilter{Attachment: "belege"}, []string{"steuer"}},
		{"has attachment", store.SearchFilter{HasAttachment: true}, []string{"steuer", "bescheid", "meeting"}},
		{"after inclusive", store.SearchFilter{After: date("2024-01-15")}, []string{"steuer", "bescheid"}},
		{"before exclusive", store.SearchFilter{Before: date("2024-01-15")}, []string{"rabatt", "meeting"}},
		{"date range", store.SearchFilter{After: date("2023-06-01"), Before: date("2024-03-10")}, []string{"bescheid", "rabatt"}},
		{"to and query", store.SearchFilter{To: "finanzamt", Query: "Einspruch"}, []string{"steuer"}},
		{"to and other query", store.SearchFilter{To: "finanzamt", Query: "Termin"}, []string{}},
		{"account", store.SearchFilter{HasAttachment: true, Account: "privat"}, []string{"steuer", "bescheid"}},
		{"folder", store.SearchFilter{HasAttachment: true, Account: "privat", Folder: "Sent"}, []string{"steuer"}},
		{"to and dates", store.SearchFilter{To: "alice", After: date("2023-01-01"), Before: date("2024-01-01")}, []string{"rabatt", "meeting"}},
		{"from and attachment", store.SearchFilter{From: "finanzamt", Attachment: "bescheid"}, []string{"bescheid"}},
		// Free text stays on subject, sender and body.
		{"query skips recipients", store.SearchFilter{Query: "Weber"}, []string{}},
		{"query skips recipient address", store.SearchFilter{Query: "poststelle@fa-koeln.example"}, []string{"bescheid"}},
		{"query skips attachment names", store.SearchFilter{Query: "Belege"}, []string{}},
		{"query body", store.SearchFilter{Query: "Finanzamt"}, []string{"bescheid", "meeting"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := f.search(c.filter); !slices.Equal(got, c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}

	// The cursor continues a filtered listing.
	rows, err := f.st.SearchMessages(f.ctx, store.SearchFilter{Owner: f.owner, HasAttachment: true, Limit: 1})
	if err != nil || len(rows) != 1 || !rows[0].HasAttachment {
		t.Fatalf("first page = %+v, %v", rows, err)
	}
	next := f.search(store.SearchFilter{HasAttachment: true, CursorAt: &rows[0].SortAt, CursorSHA: strings.TrimSpace(rows[0].SHA256)})
	if !slices.Equal(next, []string{"bescheid", "meeting"}) {
		t.Errorf("second page = %v", next)
	}
	all, err := f.st.SearchMessages(f.ctx, store.SearchFilter{Owner: f.owner, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range all {
		if r.HasAttachment != (strings.TrimSpace(r.SHA256) != "rabatt") {
			t.Errorf("%s: HasAttachment = %v", r.SHA256, r.HasAttachment)
		}
	}
}

// explainer is a db.DBTX that records the query instead of running it.
type explainer struct {
	sql  string
	args []any
}

var errRecorded = errors.New("recorded")

func (e *explainer) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errRecorded
}

func (e *explainer) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	e.sql, e.args = sql, args
	return nil, errRecorded
}

func (e *explainer) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

// The filters and reindex use their indexes on an archive of 50,000
// messages, a quarter of them with attachments. The plans
// are those of the generated query with the given parameters.
func TestSearchFilterPlans(t *testing.T) {
	_, url := storetest.NewWithURL(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	seed := `
INSERT INTO users (name, password_hash) VALUES ('plan', 'x');
INSERT INTO accounts (name, host, port, tls_mode, username, password_enc, owner_id)
VALUES ('a', 'h', 993, 'tls', 'u', '\x00', (SELECT id FROM users));
INSERT INTO folders (account_id, name, uidvalidity) SELECT id, 'INBOX', 1 FROM accounts;
INSERT INTO messages (sha256, size, stored_path, subject, from_addr, sent_at, to_addr, cc_addr,
                      attachment_names, has_attachment, index_version)
SELECT encode(sha256(i::text::bytea), 'hex'), 1000, 'x', 'Betreff ' || i, 'sender' || (i % 500) || '@example.com',
       timestamptz '2020-01-01' + i * interval '1 hour',
       CASE WHEN i % 5000 = 0 THEN 'Finanzamt <poststelle@finanzamt.example>' ELSE 'kunde' || i || '@example.com' END,
       CASE WHEN i % 3 = 0 THEN 'kopie' || (i % 100) || '@example.com' END,
       CASE WHEN i % 4 = 0 THEN 'rechnung-' || i || '.pdf' END,
       i % 4 = 0, 2
FROM generate_series(1, 50000) i;
INSERT INTO message_locations (message_sha256, folder_id, uidvalidity, uid)
SELECT encode(sha256(i::text::bytea), 'hex'), (SELECT id FROM folders), 1, i FROM generate_series(1, 50000) i;
ANALYZE;`
	if _, err := conn.Exec(ctx, seed); err != nil {
		t.Fatal(err)
	}
	var owner int64
	if err := conn.QueryRow(ctx, `SELECT id FROM users`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	str := func(s string) *string { return &s }

	cases := []struct {
		name  string
		p     db.SearchMessagesParams
		index string
	}{
		{"to", db.SearchMessagesParams{ToPattern: str("%finanzamt%")}, "messages_rcpt_trgm_idx"},
		{"attachment", db.SearchMessagesParams{AttachmentPattern: str("%rechnung-4200%"), HasAttachment: true}, "messages_attachment_trgm_idx"},
		{"has attachment", db.SearchMessagesParams{HasAttachment: true}, "messages_attachment_sort_idx"},
	}
	explain := func(t *testing.T, rec *explainer) string {
		t.Helper()
		rows, err := conn.Query(ctx, "EXPLAIN "+rec.sql, rec.args...)
		if err != nil {
			t.Fatal(err)
		}
		lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(lines, "\n")
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.p.Owner, c.p.RowLimit = owner, 51
			var rec explainer
			if _, err := db.New(&rec).SearchMessages(ctx, c.p); !errors.Is(err, errRecorded) {
				t.Fatalf("record: %v", err)
			}
			if plan := explain(t, &rec); !strings.Contains(plan, c.index) {
				t.Errorf("plan does not use %s:\n%s", c.index, plan)
			}
		})
	}

	// Each reindex batch seeks to its start in the primary key instead of
	// reading from the first row again. Right after the upgrade, every row
	// is pending.
	t.Run("reindex batch", func(t *testing.T) {
		if _, err := conn.Exec(ctx, `UPDATE messages SET index_version = 0; ANALYZE messages`); err != nil {
			t.Fatal(err)
		}
		var rec explainer
		p := db.ListUnindexedParams{IndexVersion: store.IndexVersion, After: strings.Repeat("8", 64), RowLimit: 200}
		if _, err := db.New(&rec).ListUnindexed(ctx, p); !errors.Is(err, errRecorded) {
			t.Fatalf("record: %v", err)
		}
		if plan := explain(t, &rec); !strings.Contains(plan, "Index Cond: (sha256 >") {
			t.Errorf("plan does not seek in the primary key:\n%s", plan)
		}
	})
}
