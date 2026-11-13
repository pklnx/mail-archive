package store_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/db"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// Attachment text per location (#539, part 2): a location shows its
// message's attachment text only when attachment_text_ok is set.

// downAttachmentTextPerLocationMigration rolls back
// 00042_attachment_text_per_location.
func downAttachmentTextPerLocationMigration(t *testing.T, st *store.Store) {
	t.Helper()
	if m, err := st.MigrateDown(context.Background()); err != nil || !strings.Contains(m, "00042_attachment_text_per_location") {
		t.Fatalf("down: %q %v", m, err)
	}
}

// attMsg is message i with a PDF attachment, new to the archive, whose
// body says body; thread groups it with others.
func attMsg(i int, body, thread string) store.MessageMeta {
	m := usageMsg(i, 100)
	m.Subject = fmt.Sprintf("Nachricht %d", i)
	m.IndexData = store.IndexData{BodyText: body, HasAttachment: true, AttachmentNames: []string{"Rechnung.pdf"}, ThreadID: thread}
	return m
}

// known is m as sync passes a message whose row exists: only the hash,
// size and path.
func known(m store.MessageMeta) store.MessageMeta {
	return store.MessageMeta{SHA256: m.SHA256, Size: m.Size, StoredPath: m.StoredPath, Known: true}
}

// flags returns the attachment_text_ok of m's locations in fo.
func (f *usageFixture) flags(fo *store.Folder, sha string) []bool {
	f.t.Helper()
	rows, err := f.conn.Query(f.ctx, "SELECT attachment_text_ok FROM message_locations WHERE folder_id = $1 AND message_sha256 = $2 ORDER BY id", fo.ID, sha)
	if err != nil {
		f.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[bool])
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

// textJob returns the state and priority of sha's attachment_text job, or
// "" when it has none.
func (f *usageFixture) textJob(sha string) (string, int) {
	f.t.Helper()
	var state string
	var prio int
	err := f.conn.QueryRow(f.ctx, "SELECT state, priority FROM jobs WHERE kind = $1 AND key = $2", store.JobAttachmentText, sha).Scan(&state, &prio)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0
	}
	if err != nil {
		f.t.Fatal(err)
	}
	return state, prio
}

// extracted stores texts for sha as its job does: the locations that do
// not show the text when it starts show it afterwards.
func (f *usageFixture) extracted(sha string, texts ...store.AttachmentText) {
	f.t.Helper()
	needed, err := f.st.AttachmentTextNeeded(f.ctx, sha)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.st.SaveAttachmentTexts(f.ctx, sha, texts, needed); err != nil {
		f.t.Fatal(err)
	}
	f.exec("UPDATE jobs SET state = 'done', finished_at = now() WHERE kind = $1 AND key = $2", store.JobAttachmentText, sha)
}

// TestAttachmentTextMigration00042: existing locations keep showing their
// text (true), new ones default to false, jobs.requeue is false; down
// drops both columns and the index; down and up turns false flags true.
func TestAttachmentTextMigration00042(t *testing.T) {
	f := newUsageFixture(t)
	fo := f.folder(nil, "alt")
	downAttachmentTextPerLocationMigration(t, f.st)
	f.exec(`INSERT INTO messages (sha256, size, stored_path, has_attachment) VALUES (lpad('1', 64, '0'), 1, 'x', true)`)
	f.exec(`INSERT INTO message_locations (message_sha256, folder_id, uidvalidity, uid) VALUES (lpad('1', 64, '0'), $1, 1, 1)`, fo.ID)
	f.exec(`INSERT INTO jobs (kind, key, priority, max_attempts, payload) VALUES ('attachment_text', 'k', 0, 3, '{}')`)
	if _, err := f.st.Migrate(f.ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.int64Of("SELECT count(*) FROM message_locations WHERE attachment_text_ok"); n != 1 {
		t.Errorf("existing locations with the flag: %d", n)
	}
	if n := f.int64Of("SELECT count(*) FROM jobs WHERE requeue"); n != 0 {
		t.Errorf("jobs with requeue: %d", n)
	}
	f.exec(`INSERT INTO message_locations (message_sha256, folder_id, uidvalidity, uid) VALUES (lpad('1', 64, '0'), $1, 1, 2)`, fo.ID)
	if n := f.int64Of("SELECT count(*) FROM message_locations WHERE uid = 2 AND NOT attachment_text_ok"); n != 1 {
		t.Error("a location written without the column (as by 1.5.x) is not false")
	}
	if n := f.int64Of("SELECT count(*) FROM pg_indexes WHERE indexname = 'message_locations_attachment_pending_idx'"); n != 1 {
		t.Error("no partial index")
	}
	downAttachmentTextPerLocationMigration(t, f.st)
	if n := f.int64Of(`SELECT count(*) FROM information_schema.columns WHERE (table_name, column_name) IN
		(('message_locations', 'attachment_text_ok'), ('jobs', 'requeue'))`); n != 0 {
		t.Errorf("%d columns left after down", n)
	}
	if _, err := f.st.Migrate(f.ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.int64Of("SELECT count(*) FROM message_locations WHERE NOT attachment_text_ok"); n != 0 {
		t.Errorf("down and up left %d false flags", n)
	}
}

// TestAttachmentTextInsert: the flag of a new location in all four
// insert paths. A message without an attachment is true and queues
// nothing; one new to the archive is false and queued at PriorityNew; a
// known one is true only when the location's group already has a location
// of it that shows the text (also an account without an owner, group 0),
// else false and its done job is queued again.
func TestAttachmentTextInsert(t *testing.T) {
	f := newUsageFixture(t)
	gB := f.group("B")
	alice := f.user("alice", f.defaultGroup())
	carol := f.user("carol", f.defaultGroup())
	bob := f.user("bob", gB)
	fa, fa2 := f.folder(&alice.ID, "alice"), f.folder(&alice.ID, "alice2")
	fc, fb := f.folder(&carol.ID, "carol"), f.folder(&bob.ID, "bob")
	u1, u2 := f.folder(nil, "unowned1"), f.folder(nil, "unowned2")

	plain := usageMsg(1, 10)
	f.save(fa, plain)
	if got := f.flags(fa, plain.SHA256); !slices.Equal(got, []bool{true}) {
		t.Errorf("without attachment: %v", got)
	}
	if state, _ := f.textJob(plain.SHA256); state != "" {
		t.Errorf("without attachment: job %s", state)
	}
	f.save(fb, known(plain))
	if got := f.flags(fb, plain.SHA256); !slices.Equal(got, []bool{true}) {
		t.Errorf("known without attachment: %v", got)
	}

	m := attMsg(2, "x", "")
	f.save(fa, m)
	if got := f.flags(fa, m.SHA256); !slices.Equal(got, []bool{false}) {
		t.Errorf("new with attachment: %v", got)
	}
	if state, prio := f.textJob(m.SHA256); state != store.JobQueued || prio != store.PriorityNew {
		t.Errorf("new with attachment: job %s %d", state, prio)
	}
	// Another account of the group before the job ran: false too.
	f.save(fc, known(m))
	if got := f.flags(fc, m.SHA256); !slices.Equal(got, []bool{false}) {
		t.Errorf("same group before the run: %v", got)
	}
	f.extracted(m.SHA256, pdfText(1, "Zahlungserinnerung"))
	// After it: the group shows it, in another account and in a folder of
	// the same account.
	f.save(fa2, known(m))
	if got := f.flags(fa2, m.SHA256); !slices.Equal(got, []bool{true}) {
		t.Errorf("same group after the run: %v", got)
	}
	if state, _ := f.textJob(m.SHA256); state != store.JobDone {
		t.Errorf("same group: job %s", state)
	}
	// Another group: false, and the done job is queued again for it.
	f.save(fb, known(m))
	if got := f.flags(fb, m.SHA256); !slices.Equal(got, []bool{false}) {
		t.Errorf("other group: %v", got)
	}
	if state, prio := f.textJob(m.SHA256); state != store.JobQueued || prio != store.PriorityNew {
		t.Errorf("other group: job %s %d", state, prio)
	}
	f.extracted(m.SHA256)

	// Group 0, accounts without an owner: a group like any other.
	f.save(u1, known(m))
	if got := f.flags(u1, m.SHA256); !slices.Equal(got, []bool{false}) {
		t.Errorf("group 0 first: %v", got)
	}
	f.extracted(m.SHA256)
	f.save(u2, known(m))
	if got := f.flags(u2, m.SHA256); !slices.Equal(got, []bool{true}) {
		t.Errorf("group 0 second: %v", got)
	}

	// Delivery over SMTP (ReceiveMessage) and a fill (saveIntoJournal) in
	// another group: false and queued.
	gC := f.group("C")
	dave := f.user("dave", gC)
	smtpAcc := createSMTPAccount(t, f.st, &dave.ID, "journal", "journal@example.com")
	f.extracted(m.SHA256)
	if _, err := f.st.ReceiveMessage(f.ctx, smtpAcc.ID, known(m), store.Envelope{MailFrom: "a@example.com", RcptTo: []string{"b@example.com"}, ReceivedAt: time.Now()},
		func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	journal := f.int64Of("SELECT id FROM folders WHERE account_id = $1", smtpAcc.ID)
	if n := f.int64Of("SELECT count(*) FROM message_locations WHERE folder_id = $1 AND NOT attachment_text_ok", journal); n != 1 {
		t.Errorf("delivery into another group: %d false locations", n)
	}
	if state, _ := f.textJob(m.SHA256); state != store.JobQueued {
		t.Errorf("delivery: job %s", state)
	}
	f.extracted(m.SHA256)
	// The same message imported into the journal folder again (a second
	// location): the group shows it now.
	if _, err := f.st.ReceiveMessage(f.ctx, smtpAcc.ID, known(m), store.Envelope{MailFrom: "a@example.com", RcptTo: []string{"c@example.com"}, ReceivedAt: time.Now()},
		func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	if n := f.int64Of("SELECT count(*) FROM message_locations WHERE folder_id = $1 AND NOT attachment_text_ok", journal); n != 0 {
		t.Errorf("after the run: %d false locations", n)
	}
	m3 := attMsg(3, "y", "")
	f.save(fa, m3)
	f.extracted(m3.SHA256)
	imp := &store.SMTPImport{AccountID: smtpAcc.ID, Label: "test", Format: "eml", Source: "test"}
	if err := f.st.CreateSMTPImport(f.ctx, imp); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.st.SaveJournalImportBatch(f.ctx, journal, imp.ID, []store.MessageMeta{known(m3)}, []store.Location{{}}); err != nil {
		t.Fatal(err)
	}
	if n := f.int64Of("SELECT count(*) FROM message_locations WHERE folder_id = $1 AND message_sha256 = $2 AND NOT attachment_text_ok", journal, m3.SHA256); n != 1 {
		t.Errorf("journal import into another group: %d false locations", n)
	}
	if state, prio := f.textJob(m3.SHA256); state != store.JobQueued || prio != store.PriorityNew {
		t.Errorf("journal import: job %s %d", state, prio)
	}
}

// TestRequeueIfRunning: the opt-in rerun of a running job. Only a running
// job asked for it gets requeue, with the lower priority and nothing else
// changed; FinishJob then queues it again at once with attempts 0 and
// wakes the pools; a retry, a lost lease, a release and a continue drop
// the request; a failed job is never queued by it; without the option a
// running job is left alone.
func TestRequeueIfRunning(t *testing.T) {
	f := newUsageFixture(t)
	ctx := f.ctx
	newJob := func(key string, prio int, requeue bool) store.NewJob {
		return store.NewJob{Kind: "requeue_test", Key: key, Priority: prio, MaxAttempts: 3, RequeueIfRunning: requeue}
	}
	enqueue := func(j store.NewJob) int {
		t.Helper()
		n, err := f.st.EnqueueJobs(ctx, []store.NewJob{j}, false)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	type row struct {
		state                    string
		prio, attempts           int
		requeue                  bool
		payload                  string
		startedAt, leaseUntil    string
		runAfterPast, maxAttempt bool
	}
	read := func(key string) row {
		t.Helper()
		var r row
		if err := f.conn.QueryRow(ctx, `SELECT state, priority, attempts, requeue, payload::text, coalesce(started_at::text, ''),
			coalesce(lease_until::text, ''), run_after <= now(), max_attempts = 3
			FROM jobs WHERE kind = 'requeue_test' AND key = $1`, key).
			Scan(&r.state, &r.prio, &r.attempts, &r.requeue, &r.payload, &r.startedAt, &r.leaseUntil, &r.runAfterPast, &r.maxAttempt); err != nil {
			t.Fatal(err)
		}
		return r
	}
	claim := func(lease string) store.ClaimedJob {
		t.Helper()
		c, err := f.st.ClaimJobs(ctx, []string{"requeue_test"}, []byte(lease), time.Minute, 1)
		if err != nil || len(c) != 1 {
			t.Fatalf("claim: %v %v", c, err)
		}
		return c[0]
	}

	enqueue(newJob("a", store.PriorityBackfill, false))
	j := claim("l1")
	before := read("a")
	// Without the option: unchanged.
	if n := enqueue(newJob("a", store.PriorityNew, false)); n != 0 {
		t.Errorf("queued %d", n)
	}
	if got := read("a"); got != before {
		t.Errorf("running job changed without the option: %+v", got)
	}
	// With it: requeue and the lower priority only; not counted.
	if n := enqueue(newJob("a", store.PriorityNew, true)); n != 0 {
		t.Errorf("counted %d", n)
	}
	want := before
	want.requeue, want.prio = true, store.PriorityNew
	if got := read("a"); got != want {
		t.Errorf("after the request: %+v, want %+v", got, want)
	}
	listen := connect(t, f.url)
	if _, err := listen.Exec(ctx, "LISTEN "+store.JobsChannel); err != nil {
		t.Fatal(err)
	}
	if err := f.st.FinishJob(ctx, j.ID, j.Lease); err != nil {
		t.Fatal(err)
	}
	if got := read("a"); got.state != store.JobQueued || got.attempts != 0 || got.requeue || got.prio != store.PriorityNew ||
		got.startedAt != "" || got.leaseUntil != "" || !got.runAfterPast {
		t.Errorf("after FinishJob: %+v", got)
	}
	wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if n, err := listen.WaitForNotification(wctx); err != nil || n.Payload != "requeue_test" {
		t.Errorf("notification: %v %v", n, err)
	}
	// The rerun finishes done.
	j = claim("l2")
	if err := f.st.FinishJob(ctx, j.ID, j.Lease); err != nil {
		t.Fatal(err)
	}
	if got := read("a"); got.state != store.JobDone || got.requeue {
		t.Errorf("rerun: %+v", got)
	}

	// Retry, lost lease (reap), release and continue drop the request.
	backoff := store.JobBackoff{First: time.Millisecond, Cap: time.Millisecond}
	for _, c := range []struct {
		name string
		end  func(j store.ClaimedJob) error
	}{
		{"retry", func(j store.ClaimedJob) error {
			_, _, err := f.st.RetryJob(ctx, j.ID, j.Lease, "x", false, backoff)
			return err
		}},
		{"reap", func(j store.ClaimedJob) error {
			f.exec("UPDATE jobs SET lease_until = now() - interval '1 second' WHERE id = $1", j.ID)
			_, err := f.st.ReapExpiredJobs(ctx, "requeue_test", backoff, nil, 10)
			return err
		}},
		{"release", func(j store.ClaimedJob) error { return f.st.ReleaseJob(ctx, j.ID, j.Lease) }},
		{"continue", func(j store.ClaimedJob) error { return f.st.ContinueJob(ctx, j.ID, j.Lease, nil) }},
	} {
		key := "b-" + c.name
		enqueue(newJob(key, store.PriorityBackfill, false))
		f.exec("UPDATE jobs SET state = 'done' WHERE kind = 'requeue_test' AND key <> $1", key)
		j := claim("l-" + c.name)
		enqueue(newJob(key, store.PriorityNew, true))
		if err := c.end(j); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := read(key); got.requeue || got.state != store.JobQueued {
			t.Errorf("%s: %+v", c.name, got)
		}
		f.exec("UPDATE jobs SET state = 'done' WHERE kind = 'requeue_test' AND key = $1", key)
	}

	// A failed job stays failed.
	enqueue(newJob("c", store.PriorityBackfill, false))
	f.exec("UPDATE jobs SET state = 'failed' WHERE kind = 'requeue_test' AND key = 'c'")
	enqueue(newJob("c", store.PriorityNew, true))
	if got := read("c"); got.state != store.JobFailed || got.requeue || got.prio != store.PriorityBackfill {
		t.Errorf("failed: %+v", got)
	}
}

// attachmentScope is the scope the search entry points take: the user's
// accounts.
func attachmentScope(t *testing.T, st *store.Store, user int64) store.Scope {
	return storetest.Viewer(t, st, user)
}

// TestAttachmentTextGate: every search entry point finds a message by its
// attachment text only through a location of the scope that shows it:
// SearchMessages with att_shas and with the walk, SearchThreads (both
// arms), CountThreadMatches, AttachmentMatches, AttachmentSnippets and a
// filter hold. alice's location shows M's PDF text, bob's (another group)
// does not until his extraction; a user moved into bob's group keeps
// hers, and a save after the move in that group shows it at once. Both
// can search Y, so that both searches walk with a limit of 0.
func TestAttachmentTextGate(t *testing.T) {
	f := newUsageFixture(t)
	gB := f.group("B")
	alice := f.user("alice", f.defaultGroup())
	bob := f.user("bob", gB)
	fa, fb := f.folder(&alice.ID, "alice"), f.folder(&bob.ID, "bob")
	// M matches only by its PDF; X, in M's thread, by its body.
	m := attMsg(1, "Siehe Anhang", "t1")
	x := attMsg(2, "Die Zahlungserinnerung kam an", "t1")
	f.save(fa, m, x)
	f.extracted(m.SHA256, pdfText(1, "Zahlungserinnerung Kundennummer"))
	f.extracted(x.SHA256)
	f.save(fb, known(m), known(x))
	// Y, which both can search, so that the walk runs for both: it starts
	// only when AttachmentMatches finds more than its limit.
	y := attMsg(3, "Hallo", "t2")
	f.save(fa, y)
	f.extracted(y.SHA256, pdfText(1, "Kundennummer Lieferschein"))
	f.save(fb, known(y))
	f.extracted(y.SHA256)
	const word = "Kundennummer"

	type view struct {
		messages, walk, threads, threadsWalk []string
		count                                int64
		matches, snippets                    int
		hold                                 int64
	}
	look := func(user int64) view {
		t.Helper()
		var v view
		sc := attachmentScope(t, f.st, user)
		names := func(rows []store.MessageSummary) []string {
			out := []string{}
			for _, r := range rows {
				out = append(out, strings.TrimSpace(r.SHA256))
			}
			slices.Sort(out)
			return out
		}
		search := func() []string {
			rows, err := f.st.SearchMessages(f.ctx, store.SearchFilter{Scope: sc, Query: word, Limit: 10})
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range rows {
				if r.MatchedAttachment == nil {
					t.Errorf("user %d: %s without its matched attachment", user, r.SHA256)
				}
			}
			return names(rows)
		}
		threads := func(q string) ([]string, int64) {
			rows, err := f.st.SearchThreads(f.ctx, store.SearchFilter{Scope: sc, Query: q, Limit: 10})
			if err != nil {
				t.Fatal(err)
			}
			var c int64
			for _, r := range rows {
				c += r.Count
			}
			return names(rows), c
		}
		v.messages = search()
		v.threads, _ = threads(word)
		_, v.count = threads("Zahlungserinnerung")
		restore := store.SetMaxAttachmentMatches(0)
		v.walk = search()
		v.threadsWalk, _ = threads(word)
		restore()
		var accounts []int64
		for _, a := range sc.Accounts {
			accounts = append(accounts, a.ID)
		}
		q := db.New(f.conn)
		shas, err := q.AttachmentMatches(f.ctx, db.AttachmentMatchesParams{Query: word, Accounts: accounts, RowLimit: 10,
			ScopeAllFolderAccounts: accounts, ScopeFolderAccounts: []int64{}, ScopeFolders: []int64{}})
		if err != nil {
			t.Fatal(err)
		}
		v.matches = len(shas)
		snips, err := q.AttachmentSnippets(f.ctx, db.AttachmentSnippetsParams{Query: word, Shas: []string{m.SHA256}, Accounts: accounts,
			ScopeAllFolderAccounts: accounts, ScopeFolderAccounts: []int64{}, ScopeFolders: []int64{}})
		if err != nil {
			t.Fatal(err)
		}
		v.snippets = len(snips)
		id, err := f.st.AddLegalHold(f.ctx, store.NewLegalHold{Name: fmt.Sprintf("h%d-%d", user, time.Now().UnixNano()), Reason: "r", OwnerID: &user,
			Filter: &store.HoldFilter{Query: word}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.st.MatchLegalHold(f.ctx, id); err != nil {
			t.Fatal(err)
		}
		if v.hold, err = f.st.LegalHoldMatched(f.ctx, id); err != nil {
			t.Fatal(err)
		}
		return v
	}
	sees := func(name string, v view, yes bool) {
		t.Helper()
		want := view{messages: []string{y.SHA256}, walk: []string{y.SHA256}, threads: []string{y.SHA256}, threadsWalk: []string{y.SHA256},
			count: 1, matches: 1, hold: 1}
		if yes {
			both := []string{m.SHA256, y.SHA256}
			want = view{messages: both, walk: both, threads: both, threadsWalk: both, count: 2, matches: 2, snippets: 1, hold: 2}
		}
		if !slices.Equal(v.messages, want.messages) || !slices.Equal(v.walk, want.walk) || !slices.Equal(v.threads, want.threads) ||
			!slices.Equal(v.threadsWalk, want.threadsWalk) || v.count != want.count || v.matches != want.matches ||
			v.snippets != want.snippets || v.hold != want.hold {
			t.Errorf("%s:\n got  %+v\n want %+v", name, v, want)
		}
	}
	sees("alice", look(alice.ID), true)
	sees("bob before his extraction", look(bob.ID), false)

	// alice moves into bob's group: she keeps her text, bob still has none.
	if _, err := f.st.SetUserGroup(f.ctx, alice.ID, gB); err != nil {
		t.Fatal(err)
	}
	sees("alice after the move", look(alice.ID), true)
	sees("bob after alice's move", look(bob.ID), false)
	// From then on, a user of the group who saves M shows it at once.
	carol := f.user("carol", gB)
	fc := f.folder(&carol.ID, "carol")
	f.save(fc, known(m))
	if got := f.flags(fc, m.SHA256); !slices.Equal(got, []bool{true}) {
		t.Errorf("carol after the move: %v", got)
	}

	f.extracted(m.SHA256)
	sees("bob after his extraction", look(bob.ID), true)
}

// TestGroupAttachmentTextOKPlan: the insert's group lookup probes
// message_locations_folder_sha_idx from the group's own folders and never
// looks a hash up over the whole archive (message_locations_sha_idx), in
// the custom and in the generic plan, on a group of 400 folders where
// every hash has few copies: there the planner flattens an unfenced probe
// into a lookup by hash.
func TestGroupAttachmentTextOKPlan(t *testing.T) {
	f := newUsageFixture(t)
	g := f.defaultGroup()
	const users, folders = 20, 20
	for u := range users {
		usr := f.user(fmt.Sprintf("u%d", u), g)
		acc := &store.Account{Kind: store.KindImport, Name: fmt.Sprintf("a%d", u), OwnerID: &usr.ID}
		if err := f.st.CreateAccount(f.ctx, acc); err != nil {
			t.Fatal(err)
		}
		f.exec(`INSERT INTO folders (account_id, name) SELECT $1, 'f' || i FROM generate_series(1, $2::int) i`, acc.ID, folders)
	}
	// 20,000 messages, two copies each, spread over the folders.
	f.exec(`INSERT INTO messages (sha256, size, stored_path, has_attachment)
		SELECT lpad(to_hex(i), 64, '0'), 1, 'x', true FROM generate_series(1, 20000) i`)
	f.exec(`INSERT INTO message_locations (message_sha256, folder_id, uidvalidity, uid, attachment_text_ok)
		SELECT lpad(to_hex(i), 64, '0'), fo.id, 1, i * 2 + c, i % 2 = 0
		FROM generate_series(1, 20000) i, generate_series(0, 1) c,
		     LATERAL (SELECT id FROM folders ORDER BY id OFFSET (i * 7 + c) % 400 LIMIT 1) fo`)
	f.exec("VACUUM ANALYZE folders")
	f.exec("VACUUM ANALYZE message_locations")
	if n := f.int64Of("SELECT count(*) FROM folders"); n < 400 {
		t.Fatalf("%d folders", n)
	}
	explain := func(sql string, args ...any) string {
		t.Helper()
		rows, err := f.conn.Query(f.ctx, "EXPLAIN "+sql, args...)
		if err != nil {
			t.Fatal(err)
		}
		lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(lines, "\n")
	}
	probeOK := func(plan string) bool {
		return strings.Contains(plan, "message_locations_folder_sha_idx") &&
			!strings.Contains(plan, "message_locations_sha_idx") && !strings.Contains(plan, "Seq Scan on message_locations")
	}
	shas := make([]string, 100)
	for i := range shas {
		shas[i] = lpadHex(i*97 + 1)
	}
	var rec explainer
	if _, err := db.New(&rec).GroupAttachmentTextOK(f.ctx, db.GroupAttachmentTextOKParams{Shas: shas, GroupID: g}); !errors.Is(err, errRecorded) {
		t.Fatalf("record: %v", err)
	}
	if plan := explain(rec.sql, rec.args...); !probeOK(plan) {
		t.Errorf("custom plan does not probe message_locations_folder_sha_idx only:\n%s", plan)
	}
	f.exec("PREPARE group_text_ok(bpchar[], bigint) AS " + rec.sql)
	f.exec("SET plan_cache_mode = force_generic_plan")
	defer f.exec("RESET plan_cache_mode")
	generic := explain(fmt.Sprintf("EXECUTE group_text_ok(ARRAY['%s']::bpchar[], %d)", strings.Join(shas, "','"), g))
	if !strings.Contains(generic, "$2") && !strings.Contains(generic, "$1") {
		t.Errorf("not a generic plan:\n%s", generic)
	}
	if !probeOK(generic) {
		t.Errorf("generic plan does not probe message_locations_folder_sha_idx only:\n%s", generic)
	}
	// The answer itself: the even hashes have their flag.
	got, err := db.New(f.conn).GroupAttachmentTextOK(f.ctx, db.GroupAttachmentTextOKParams{Shas: []string{lpadHex(2), lpadHex(3)}, GroupID: g})
	if err != nil || !slices.Equal(got, []string{lpadHex(2)}) {
		t.Errorf("answer: %v %v", got, err)
	}
	if got, err := db.New(f.conn).GroupAttachmentTextOK(f.ctx, db.GroupAttachmentTextOKParams{Shas: []string{lpadHex(2)}, GroupID: 0}); err != nil || len(got) != 0 {
		t.Errorf("group 0: %v %v", got, err)
	}
}

// TestRepairAttachmentText: the repair queues the job of every done
// message with a false location and no queued, running or failed job (a
// location inserted with the column's default, as 1.5.x does; a job ended
// without setting flags, as an old worker does; a done job removed), sets
// the flag on false locations of messages without attachments, leaves
// messages that are not done to the backfill, and a second pass queues
// nothing.
func TestRepairAttachmentText(t *testing.T) {
	f := newUsageFixture(t)
	alice := f.user("alice", f.defaultGroup())
	fo := f.folder(&alice.ID, "alice")
	oldInsert, oldJob, pruned, failed, running, notDone, plain := attMsg(1, "", ""), attMsg(2, "", ""), attMsg(3, "", ""),
		attMsg(4, "", ""), attMsg(5, "", ""), attMsg(6, "", ""), usageMsg(7, 1)
	f.save(fo, oldInsert, oldJob, pruned, failed, running, notDone, plain)
	for _, m := range []store.MessageMeta{oldInsert, oldJob, pruned, failed, running} {
		f.extracted(m.SHA256, pdfText(1, "x"))
	}
	// As 1.5.x: a location without the column, a message without
	// attachments with a false location, a job ended done without flags.
	f.exec(`INSERT INTO message_locations (message_sha256, folder_id, uidvalidity, uid) VALUES ($1, $2, 1, 1000), ($3, $2, 1, 1001)`,
		oldInsert.SHA256, fo.ID, plain.SHA256)
	f.exec(`INSERT INTO message_locations (message_sha256, folder_id, uidvalidity, uid) VALUES ($1, $2, 1, 1002), ($3, $2, 1, 1003), ($4, $2, 1, 1004),
		($5, $2, 1, 1005)`, oldJob.SHA256, fo.ID, pruned.SHA256, failed.SHA256, running.SHA256)
	f.exec("DELETE FROM jobs WHERE key = $1", pruned.SHA256)
	f.exec("UPDATE jobs SET state = 'failed' WHERE key = $1", failed.SHA256)
	f.exec("UPDATE jobs SET state = 'running', lease_token = '\\x01', lease_until = now() + interval '1 hour' WHERE key = $1", running.SHA256)
	f.exec("UPDATE jobs SET state = 'done' WHERE key = $1", notDone.SHA256)
	res, err := f.st.RepairAttachmentText(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Queued != 3 || res.Cleared != 1 {
		t.Errorf("first pass: %+v", res)
	}
	for _, m := range []store.MessageMeta{oldInsert, oldJob, pruned} {
		if state, prio := f.textJob(m.SHA256); state != store.JobQueued || prio != store.PriorityBackfill {
			t.Errorf("%s: job %s %d", m.SHA256[60:], state, prio)
		}
	}
	for m, want := range map[string]string{failed.SHA256: store.JobFailed, running.SHA256: store.JobRunning, notDone.SHA256: store.JobDone} {
		if state, _ := f.textJob(m); state != want {
			t.Errorf("%s: job %s, want %s", m[60:], state, want)
		}
	}
	if got := f.flags(fo, plain.SHA256); !slices.Equal(got, []bool{true, true}) {
		t.Errorf("without attachment: %v", got)
	}
	res, err = f.st.RepairAttachmentText(f.ctx)
	if err != nil || res.Queued != 0 || res.Cleared != 0 {
		t.Errorf("second pass: %+v %v", res, err)
	}
}

// TestAttachmentTextUnreadable: a file that cannot be read sets the flags
// of a message that is not done (marked done without text) and of a done
// one without searchable text; a done one with searchable text changes
// nothing and reports ErrAttachmentTextUnreadable.
func TestAttachmentTextUnreadable(t *testing.T) {
	f := newUsageFixture(t)
	alice := f.user("alice", f.defaultGroup())
	fo := f.folder(&alice.ID, "alice")
	notDone, doneEmpty, doneText := attMsg(1, "", ""), attMsg(2, "", ""), attMsg(3, "", "")
	f.save(fo, notDone, doneEmpty, doneText)
	f.extracted(doneEmpty.SHA256, store.AttachmentText{Part: 1, Filename: "scan.pdf", Status: "no_text"})
	f.extracted(doneText.SHA256, pdfText(1, "Text"))
	// Locations a process before 1.6.0 inserted afterwards.
	f.exec(`INSERT INTO message_locations (message_sha256, folder_id, uidvalidity, uid) VALUES ($1, $3, 1, 1000), ($2, $3, 1, 1001)`,
		doneEmpty.SHA256, doneText.SHA256, fo.ID)
	for _, c := range []struct {
		m       store.MessageMeta
		wantErr error
		want    []bool
	}{
		{notDone, nil, []bool{true}},
		{doneEmpty, nil, []bool{true, true}},
		{doneText, store.ErrAttachmentTextUnreadable, []bool{true, false}},
	} {
		needed, err := f.st.AttachmentTextNeeded(f.ctx, c.m.SHA256)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.st.AttachmentTextUnreadable(f.ctx, c.m.SHA256, needed); !errors.Is(err, c.wantErr) {
			t.Errorf("%s: %v", c.m.SHA256[60:], err)
		}
		if got := f.flags(fo, c.m.SHA256); !slices.Equal(got, c.want) {
			t.Errorf("%s: flags %v, want %v", c.m.SHA256[60:], got, c.want)
		}
	}
	if done, err := f.st.AttachmentIndexed(f.ctx, notDone.SHA256); !done || err != nil {
		t.Errorf("not done: %v %v", done, err)
	}
	if n := f.int64Of("SELECT count(*) FROM attachment_texts WHERE message_sha256 = $1 AND body_text IS NOT NULL", doneText.SHA256); n != 1 {
		t.Errorf("texts of the unreadable done message: %d", n)
	}
}
