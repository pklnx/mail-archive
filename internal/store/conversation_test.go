package store_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/db"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// thread builds the index data of a message with the given links; the
// thread ID follows archive.threadID.
func thread(inReplyTo string, refs ...string) store.IndexData {
	d := store.IndexData{InReplyTo: inReplyTo, ReferenceIDs: refs}
	switch {
	case len(refs) > 0:
		d.ThreadID = refs[0]
	case inReplyTo != "":
		d.ThreadID = inReplyTo
	}
	return d
}

// conversationFixture: an offer (a), the reply from Sent (b), the answer
// (c), a list copy of c with the same Message-ID but other bytes (dup), a
// reply to b with only In-Reply-To (d), a message that references itself
// (self) and an unrelated one (other).
func conversationFixture(t *testing.T) *searchFixture {
	f := newSearchFixture(t)
	own := func(id string) store.IndexData { return store.IndexData{ThreadID: id} }
	f.add("privat/INBOX", searchMsg{sha: "a", id: "a@x", subject: "Angebot Küche", from: "Studio <info@kueche.example>", sent: "2024-01-01T10:00:00Z", data: own("a@x")})
	f.add("privat/Sent", searchMsg{sha: "b", id: "b@x", subject: "Re: Angebot Küche", from: "Alice <alice@example.com>", sent: "2024-01-02T10:00:00Z", data: thread("a@x", "a@x")})
	f.add("privat/INBOX", searchMsg{sha: "c", id: "c@x", subject: "Re: Angebot Küche", from: "Studio <info@kueche.example>", sent: "2024-01-03T10:00:00Z", data: thread("b@x", "a@x", "b@x")})
	f.add("arbeit/INBOX", searchMsg{sha: "dup", id: "c@x", subject: "Re: Angebot Küche", from: "Studio <info@kueche.example>", sent: "2024-01-03T11:00:00Z", data: thread("b@x", "a@x", "b@x")})
	f.add("privat/INBOX", searchMsg{sha: "d", id: "d@x", subject: "Re: Angebot Küche", from: "Bob <bob@example.com>", sent: "2024-01-04T10:00:00Z", data: thread("b@x")})
	self := thread("s@x", "s@x")
	f.add("privat/INBOX", searchMsg{sha: "self", id: "s@x", subject: "Kaputt", from: "x@example.com", sent: "2024-01-05T10:00:00Z", data: self})
	f.add("privat/INBOX", searchMsg{sha: "other", id: "o@x", subject: "Newsletter", from: "news@example.com", sent: "2024-01-06T10:00:00Z", data: own("o@x")})
	return f
}

func (f *searchFixture) conversation(sha string) (*store.Conversation, []string) {
	f.t.Helper()
	c, err := f.st.GetConversation(f.ctx, f.owner, sha)
	if err != nil {
		f.t.Fatal(err)
	}
	var got []string
	for _, m := range c.Messages {
		got = append(got, strings.TrimSpace(m.SHA256)+":"+m.Relation)
	}
	return c, got
}

func TestConversation(t *testing.T) {
	f := conversationFixture(t)
	cases := map[string][]string{
		// Oldest first. The list copy shares the Message-ID and is listed too.
		"b": {"a:parent", "b:self", "c:reply", "dup:reply", "d:reply"},
		"a": {"a:self", "b:reply", "c:reply", "dup:reply"},
		"c": {"a:thread", "b:parent", "c:self", "dup:thread"},
		// d has its own thread ID (In-Reply-To only), but links to b.
		"d":     {"b:parent", "d:self"},
		"self":  {"self:self"},
		"other": {"other:self"},
	}
	for sha, want := range cases {
		c, got := f.conversation(sha)
		if !slices.Equal(got, want) || c.Total != int64(len(want)) || c.Truncated {
			t.Errorf("conversation(%s) = %v (total %d, truncated %v), want %v", sha, got, c.Total, c.Truncated, want)
		}
	}
	if _, err := f.st.GetConversation(f.ctx, f.owner, strings.Repeat("0", 64)); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown message: %v", err)
	}
}

func TestConversationLimit(t *testing.T) {
	f := newSearchFixture(t)
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	n := store.MaxConversation + 10
	for i := range n {
		id := fmt.Sprintf("m%03d@x", i)
		d := thread("m000@x", "m000@x")
		if i == 0 {
			d = store.IndexData{ThreadID: id}
		}
		f.add("privat/INBOX", searchMsg{sha: fmt.Sprintf("m%03d", i), id: id, subject: "s", sent: start.Add(time.Duration(i) * time.Hour).Format(time.RFC3339), data: d})
	}
	c, got := f.conversation("m000")
	if c.Total != int64(n) || !c.Truncated || len(got) != store.MaxConversation {
		t.Fatalf("total %d, truncated %v, %d listed", c.Total, c.Truncated, len(got))
	}
	// The newest messages, oldest first: the requested first message is
	// among the ones left out.
	if got[0] != "m010:reply" || got[len(got)-1] != fmt.Sprintf("m%03d:reply", n-1) {
		t.Errorf("listed %s … %s", got[0], got[len(got)-1])
	}
}

func TestSearchThreads(t *testing.T) {
	f := conversationFixture(t)
	threads := func(filter store.SearchFilter) []string {
		t.Helper()
		filter.Owner, filter.Limit = f.owner, 50
		rows, err := f.st.SearchThreads(f.ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range rows {
			out = append(out, fmt.Sprintf("%s:%d", strings.TrimSpace(r.SHA256), r.Count))
		}
		return out
	}
	cases := []struct {
		name   string
		filter store.SearchFilter
		want   []string
	}{
		{"all", store.SearchFilter{}, []string{"other:1", "self:1", "d:1", "dup:4"}},
		{"account and folder", store.SearchFilter{Account: "privat", Folder: "INBOX"}, []string{"other:1", "self:1", "d:1", "c:2"}},
		{"query", store.SearchFilter{Query: "Angebot", Before: date("2024-01-03")}, []string{"b:2"}},
		{"from", store.SearchFilter{From: "studio"}, []string{"dup:3"}},
		{"thread", store.SearchFilter{Thread: "a@x"}, []string{"dup:4"}},
		{"guessed thread", store.SearchFilter{Thread: "nobody@x"}, nil},
	}
	for _, c := range cases {
		if got := threads(c.filter); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// Grouping and expanding agree with the plain listing for every filter: a
// grouped row is the newest match of its thread, its count is the number of
// matches, expanding it lists exactly those, and the counts add up to the
// plain listing.
func TestSearchThreadsParity(t *testing.T) {
	f := conversationFixture(t)
	f.add("privat/INBOX", searchMsg{sha: "att", id: "att@x", subject: "Re: Angebot Küche", from: "Studio <info@kueche.example>", sent: "2024-01-07T10:00:00Z",
		data: store.IndexData{InReplyTo: "c@x", ReferenceIDs: []string{"a@x", "c@x"}, ThreadID: "a@x", To: "alice@example.com", HasAttachment: true, AttachmentNames: []string{"plan.pdf"}}})
	filters := []store.SearchFilter{
		{}, {Query: "Angebot"}, {Query: "kueche"}, {Account: "privat"}, {Account: "privat", Folder: "Sent"},
		{From: "studio"}, {To: "alice"}, {Attachment: "plan"}, {HasAttachment: true},
		{After: date("2024-01-02"), Before: date("2024-01-05")}, {Thread: "a@x"},
	}
	for _, filter := range filters {
		filter.Owner = f.owner
		filter.Limit = 100
		plain, err := f.st.SearchMessages(f.ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		grouped, err := f.st.SearchThreads(f.ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		var sum int64
		for _, g := range grouped {
			sum += g.Count
			sub := filter
			sub.Thread = g.Thread
			members, err := f.st.SearchMessages(f.ctx, sub)
			if err != nil {
				t.Fatal(err)
			}
			if int64(len(members)) != g.Count || len(members) == 0 || members[0].SHA256 != g.SHA256 {
				t.Errorf("%+v: thread %s has count %d, expands to %d", filter, g.Thread, g.Count, len(members))
			}
		}
		if sum != int64(len(plain)) {
			t.Errorf("%+v: counts add up to %d, plain listing has %d", filter, sum, len(plain))
		}
	}
}

func TestSearchThreadsPaging(t *testing.T) {
	f := newSearchFixture(t)
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	// 30 threads of 1 to 3 messages, interleaved in time.
	for i := range 60 {
		th := fmt.Sprintf("t%02d@x", i%30)
		f.add("privat/INBOX", searchMsg{sha: fmt.Sprintf("m%02d", i), id: fmt.Sprintf("m%02d@x", i), subject: "s",
			sent: start.Add(time.Duration(i) * time.Hour).Format(time.RFC3339), data: thread(th, th)})
	}
	all, err := f.st.SearchThreads(f.ctx, store.SearchFilter{Owner: f.owner, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var paged []store.MessageSummary
	filter := store.SearchFilter{Owner: f.owner, Limit: 7}
	for range 20 {
		page, err := f.st.SearchThreads(f.ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		paged = append(paged, page...)
		if len(page) < filter.Limit {
			break
		}
		last := page[len(page)-1]
		filter.CursorAt, filter.CursorSHA = &last.SortAt, strings.TrimSpace(last.SHA256)
	}
	if len(all) != 30 || len(paged) != len(all) {
		t.Fatalf("%d threads, %d paged", len(all), len(paged))
	}
	for i := range all {
		if all[i].SHA256 != paged[i].SHA256 || all[i].Count != 2 {
			t.Errorf("row %d: %s (count %d), paged %s", i, all[i].SHA256, all[i].Count, paged[i].SHA256)
		}
	}
}

// sqlc has no fragments, so the filter predicate of the listings exists
// once per query and alias. The copies must stay equal.
func TestSearchFilterCopies(t *testing.T) {
	src, err := os.ReadFile("queries/search.sql")
	if err != nil {
		t.Fatal(err)
	}
	alias := regexp.MustCompile(`\b[mnt]\.`)
	var copies []string
	for _, part := range strings.Split(string(src), "-- filters:begin")[1:] {
		body, _, ok := strings.Cut(part, "-- filters:end")
		if !ok {
			t.Fatal("filters:begin without filters:end")
		}
		var lines []string
		for _, l := range strings.Split(body, "\n") {
			lines = append(lines, strings.TrimSpace(l))
		}
		copies = append(copies, alias.ReplaceAllString(strings.Join(lines, "\n"), "x."))
	}
	if len(copies) != 4 {
		t.Fatalf("found %d copies, want 4 (SearchMessages, SearchThreads twice, CountThreadMatches)", len(copies))
	}
	for i, c := range copies[1:] {
		if c != copies[0] {
			t.Errorf("copy %d differs from the first:\n%s\n---\n%s", i+2, c, copies[0])
		}
	}
}

// The grouped listing and the conversation use the thread index on 10,000
// messages in threads of 5.
func TestConversationPlans(t *testing.T) {
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
INSERT INTO messages (sha256, size, stored_path, subject, from_addr, sent_at, message_id, in_reply_to,
                      reference_ids, thread_id, index_version)
SELECT encode(sha256(i::text::bytea), 'hex'), 1000, 'x', 'Betreff ' || (i / 5), 'sender@example.com',
       timestamptz '2020-01-01' + i * interval '1 hour', 'm' || i || '@x',
       CASE WHEN i % 5 <> 0 THEN 'm' || (i - 1) || '@x' END,
       CASE WHEN i % 5 <> 0 THEN ARRAY['m' || (i - i % 5) || '@x'] ELSE '{}' END,
       'm' || (i - i % 5) || '@x', 3
FROM generate_series(0, 9999) i;
INSERT INTO message_locations (message_sha256, folder_id, uidvalidity, uid)
SELECT encode(sha256(i::text::bytea), 'hex'), (SELECT id FROM folders), 1, i FROM generate_series(0, 9999) i;
ANALYZE;`
	if _, err := conn.Exec(ctx, seed); err != nil {
		t.Fatal(err)
	}
	var owner int64
	if err := conn.QueryRow(ctx, `SELECT id FROM users`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	sha := fmt.Sprintf("%x", sha256.Sum256([]byte("4242")))
	explain := func(t *testing.T, record func(q *db.Queries) error) string {
		t.Helper()
		var rec explainer
		if err := record(db.New(&rec)); !errors.Is(err, errRecorded) {
			t.Fatalf("record: %v", err)
		}
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
	cases := map[string]func(q *db.Queries) error{
		"grouped list": func(q *db.Queries) error {
			_, err := q.SearchThreads(ctx, db.SearchThreadsParams{Owner: owner, RowLimit: 51})
			return err
		},
		"conversation": func(q *db.Queries) error {
			_, err := q.ListConversation(ctx, db.ListConversationParams{Owner: owner, Sha256: sha, RowLimit: store.MaxConversation})
			return err
		},
	}
	for name, record := range cases {
		t.Run(name, func(t *testing.T) {
			if plan := explain(t, record); !strings.Contains(plan, "messages_thread_idx") || strings.Contains(plan, "Seq Scan on messages") {
				t.Errorf("plan does not use messages_thread_idx:\n%s", plan)
			}
		})
	}
}
