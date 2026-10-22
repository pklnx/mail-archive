package archive

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/attest"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// purgeSetup has alice's removed account "old" with m1 in INBOX and
// Archive, m2 in INBOX and in bob's "mail", m3 in Archive and in alice's
// "work". No rule covers anything.
type purgeSetup struct {
	st         *store.Store
	url        string
	blobs      *blobstore.Store
	alice, bob *store.User
	acc        *store.Account
	bobInbox   *store.Folder
	m1, m2, m3 string
	uid        uint32
}

func newPurgeSetup(t *testing.T) *purgeSetup {
	t.Helper()
	ctx := context.Background()
	st, dbURL := storetest.NewWithURL(t)
	blobs, err := blobstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := &purgeSetup{st: st, url: dbURL, blobs: blobs}
	if s.alice, err = st.CreateUser(ctx, "alice", "h", false); err != nil {
		t.Fatal(err)
	}
	if s.bob, err = st.CreateUser(ctx, "bob", "h", false); err != nil {
		t.Fatal(err)
	}
	inbox := s.folder(t, s.alice, "old", "INBOX")
	archive := s.folder(t, s.alice, "old", "Archive")
	s.bobInbox = s.folder(t, s.bob, "mail", "INBOX")
	work := s.folder(t, s.alice, "work", "INBOX")
	s.m1 = s.save(t, "m1", inbox, archive)
	s.m2 = s.save(t, "m2", inbox, s.bobInbox)
	s.m3 = s.save(t, "m3", archive, work)
	acc, err := st.GetOwnedAccount(ctx, s.alice.ID, "old")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeleteOrRemoveAccount(ctx, acc.Ref()); err != nil {
		t.Fatal(err)
	}
	if s.acc, err = st.GetAccount(ctx, acc.ID); err != nil {
		t.Fatal(err)
	}
	return s
}

func (s *purgeSetup) folder(t *testing.T, owner *store.User, account, name string) *store.Folder {
	t.Helper()
	ctx := context.Background()
	a, err := s.st.GetOwnedAccount(ctx, owner.ID, account)
	if errors.Is(err, store.ErrNotFound) {
		a = &store.Account{Name: account, Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, OwnerID: &owner.ID}
		err = s.st.CreateAccount(ctx, a)
	}
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.st.GetOrCreateFolder(ctx, a.ID, name)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// save stores a message file and archives it in each folder.
func (s *purgeSetup) save(t *testing.T, subject string, folders ...*store.Folder) string {
	t.Helper()
	b, _, err := s.blobs.Put(strings.NewReader("Subject: " + subject + "\r\n\r\nhi\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range folders {
		s.uid++
		meta := store.MessageMeta{SHA256: b.SHA256, Size: b.Size, StoredPath: b.Path, Subject: subject}
		if _, err := s.st.SaveBatch(context.Background(), f.ID, s.uid, []store.MessageMeta{meta}, []store.Location{{FolderID: f.ID, UID: s.uid}}); err != nil {
			t.Fatal(err)
		}
	}
	return b.SHA256
}

func (s *purgeSetup) confirm(t *testing.T) int64 {
	t.Helper()
	p, err := s.st.ConfirmPurge(context.Background(), s.acc.Ref(), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	return p.ID
}

func (s *purgeSetup) verifyClean(t *testing.T, pending int) {
	t.Helper()
	var found []Finding
	res, err := Verify(context.Background(), s.st, s.blobs, VerifyOptions{LockTimeout: time.Second, OnFinding: func(f Finding) { found = append(found, f) }})
	if err != nil || res.Problems() != 0 || len(found) != 0 || res.PendingRemovals != pending {
		t.Fatalf("verify: %+v %+v %v", res, found, err)
	}
}

func TestExecutePurge(t *testing.T) {
	ctx := context.Background()
	s := newPurgeSetup(t)
	id := s.confirm(t)
	var actions []string
	var progress []int64
	p, err := ExecutePurge(ctx, s.st, id, PurgeOptions{
		BatchSize: 1,
		Audit: func(action string) *store.AuditEntry {
			actions = append(actions, action)
			return &store.AuditEntry{Actor: store.ActorCLI, Action: action}
		},
		Progress: func(p *store.AccountPurge) { progress = append(progress, p.Locations) },
	})
	if err != nil || p.State != store.PurgeDone || p.Locations != 4 || p.Deleted != 1 || p.Kept != 2 {
		t.Fatalf("purge %+v %v", p, err)
	}
	if strings.Join(actions, ",") != "account.purge_batch,account.purge_batch,account.purge_batch,account.purge_finish" {
		t.Errorf("audit actions %v", actions)
	}
	if len(progress) != 3 || progress[2] != 4 {
		t.Errorf("progress %v", progress)
	}
	// The file of m1 waits out the grace period; verify is clean.
	for _, sha := range []string{s.m1, s.m2, s.m3} {
		if !s.blobs.Exists(sha) {
			t.Fatalf("file %s gone", sha)
		}
	}
	s.verifyClean(t, 1)
	if again, err := ExecutePurge(ctx, s.st, id, PurgeOptions{}); err != nil || again.State != store.PurgeDone {
		t.Fatalf("again: %+v %v", again, err)
	}
	// Grace 0: the sweep removes m1's file, the others stay.
	if n, err := SweepBlobs(ctx, s.st, s.blobs, SweepOptions{}); err != nil || n != 1 {
		t.Fatalf("sweep: %d %v", n, err)
	}
	if s.blobs.Exists(s.m1) || !s.blobs.Exists(s.m2) || !s.blobs.Exists(s.m3) {
		t.Errorf("files after the sweep: %v %v %v", s.blobs.Exists(s.m1), s.blobs.Exists(s.m2), s.blobs.Exists(s.m3))
	}
	s.verifyClean(t, 0)
	// m1 archived again into an unruled folder is stored again, and the
	// sweep keeps its file.
	if err := s.st.ScheduleBlobRemoval(ctx, s.m1, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if again := s.save(t, "m1", s.bobInbox); again != s.m1 {
		t.Fatal("other hash")
	}
	if d, err := s.st.GetMessageDetail(ctx, s.bob.ID, s.m1); err != nil || len(d.Locations) != 1 {
		t.Fatalf("re-archived: %+v %v", d, err)
	}
	if n, err := SweepBlobs(ctx, s.st, s.blobs, SweepOptions{}); err != nil || n != 0 || !s.blobs.Exists(s.m1) {
		t.Fatalf("sweep after the re-archive: %d %v", n, err)
	}
	s.verifyClean(t, 0)
}

// The next attestation covers a purge's deletion, with its reason.
func TestPurgeAttestation(t *testing.T) {
	ctx := context.Background()
	s := newPurgeSetup(t)
	dir := filepath.Join(t.TempDir(), "attest")
	keyFile := filepath.Join(dir, attest.DefaultKeyFile)
	if _, err := attest.Keygen(keyFile, dir); err != nil {
		t.Fatal(err)
	}
	key, err := attest.LoadSigner(keyFile, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Attest(ctx, s.st, dir, key); err != nil {
		t.Fatal(err)
	}
	if _, err := ExecutePurge(ctx, s.st, s.confirm(t), PurgeOptions{}); err != nil {
		t.Fatal(err)
	}
	var found []Finding
	res, err := CheckAttestations(ctx, s.st, AttestCheckOptions{Dir: dir, KeyExists: true, OnFinding: func(f Finding) { found = append(found, f) }})
	if err != nil || len(found) != 0 || len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "1 deletion(s) not yet covered") {
		t.Fatalf("after the purge: %+v %+v %v", found, res, err)
	}
	e, err := Attest(ctx, s.st, dir, key)
	if err != nil || e.Entry.DeletionsCount != 1 {
		t.Fatalf("next attestation %+v %v", e.Entry, err)
	}
	leaves := ""
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(path) //nolint:gosec // test files
			leaves += string(b)
		}
		return nil
	})
	if !strings.Contains(leaves, "account_purge") || !strings.Contains(leaves, s.m1) {
		t.Errorf("leaves lack the purge deletion")
	}
	found = nil
	if _, err := CheckAttestations(ctx, s.st, AttestCheckOptions{Dir: dir, KeyExists: true, OnFinding: func(f Finding) { found = append(found, f) }}); err != nil || len(found) != 0 {
		t.Fatalf("signed: %+v %v", found, err)
	}
}

// An interrupted purge resumes in the web server's retention job, and the
// totals of its end entry are the sums of all batches.
func TestPurgeResumesInRetentionJob(t *testing.T) {
	s := newPurgeSetup(t)
	id := s.confirm(t)
	ctx, cancel := context.WithCancel(context.Background())
	_, err := ExecutePurge(ctx, s.st, id, PurgeOptions{BatchSize: 1, Progress: func(*store.AccountPurge) { cancel() }})
	if err == nil {
		t.Fatal("the cancelled purge finished")
	}
	bg := context.Background()
	p, err := s.st.AccountPurge(bg, id)
	if err != nil || p.State != store.PurgeConfirmed || p.Deleted+p.Kept != 1 || p.Locations < 1 {
		t.Fatalf("interrupted %+v %v", p, err)
	}
	// Another process executes it: the job leaves it alone.
	unlock, ok, err := s.st.TryLockPurge(bg)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	job := &Retention{Store: s.st, Blobs: s.blobs}
	job.RunOnce(bg)
	if p, _ := s.st.AccountPurge(bg, id); p.State != store.PurgeConfirmed {
		t.Fatalf("executed under another process's lock: %+v", p)
	}
	if _, err := ExecutePurge(bg, s.st, id, PurgeOptions{}); !errors.Is(err, ErrPurgeRunning) {
		t.Errorf("second executor: %v", err)
	}
	unlock()
	job.RunOnce(bg)
	if p, _ := s.st.AccountPurge(bg, id); p.State != store.PurgeDone || p.Locations != 4 || p.Deleted != 1 || p.Kept != 2 {
		t.Fatalf("resumed %+v", p)
	}
	var finish map[string]any
	var batches []map[string]any
	if err := s.st.ExportAudit(bg, store.AuditFilter{Since: time.Now().Add(-time.Hour), Until: time.Now().Add(time.Hour)}, func(r store.AuditRecord) error {
		var d map[string]any
		if err := json.Unmarshal(r.Details, &d); err != nil {
			return err
		}
		switch r.Action {
		case "account.purge_finish":
			finish = d
			if r.TargetName != "old" || d["by"] != "web server" {
				t.Errorf("finish entry %+v %v", r, d)
			}
		case "account.purge_batch":
			batches = append(batches, d)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var locs, deleted, kept float64
	for _, b := range batches {
		locs += b["locations"].(float64)
		deleted += b["deleted"].(float64)
		kept += b["kept"].(float64)
	}
	if finish == nil || finish["locations"] != float64(4) || finish["deleted"] != float64(1) || finish["kept"] != float64(2) ||
		finish["owner"] != "alice" || finish["kind"] != "imap" {
		t.Errorf("finish %v", finish)
	}
	// The interrupted CLI's batch wrote no audit entry (no Audit option);
	// the job's batches add up to the rest.
	if int64(locs) != 4-p.Locations || int(deleted) != 1-p.Deleted || int(kept) != 2-p.Kept {
		t.Errorf("batches %v", batches)
	}
}

// A rule or hold added during a purge cancels it; also one added after
// the last batch, before the account is deleted.
func TestPurgeCancelledByBlocker(t *testing.T) {
	for _, tc := range []struct {
		name  string
		batch int
		hold  bool
		want  error
	}{
		{name: "rule between batches", batch: 1, want: store.ErrRetained},
		{name: "hold between batches", batch: 1, hold: true, want: store.ErrOnHold},
		{name: "rule after the last batch", batch: 10, want: store.ErrRetained},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s := newPurgeSetup(t)
			id := s.confirm(t)
			var actions []string
			var added int64
			_, err := ExecutePurge(ctx, s.st, id, PurgeOptions{
				BatchSize: tc.batch,
				Audit: func(action string) *store.AuditEntry {
					actions = append(actions, action)
					return &store.AuditEntry{Actor: store.ActorCLI, Action: action}
				},
				Progress: func(*store.AccountPurge) {
					if added != 0 {
						return
					}
					var err error
					if tc.hold {
						added, err = s.st.AddLegalHold(ctx, store.NewLegalHold{Name: "Case", OwnerID: &s.alice.ID})
					} else {
						added, err = s.st.AddRetentionRule(ctx, store.NewRetentionRule{AccountID: s.acc.ID, Years: 10})
					}
					if err != nil {
						t.Error(err)
					}
				},
			})
			var cancelled *PurgeCancelledError
			if !errors.As(err, &cancelled) || !errors.Is(err, tc.want) {
				t.Fatalf("purge: %v", err)
			}
			p, _ := s.st.AccountPurge(ctx, id)
			if p.State != store.PurgeCancelled || !strings.Contains(p.Note, "#") {
				t.Errorf("purge %+v", p)
			}
			if actions[len(actions)-1] != "account.purge_cancel" {
				t.Errorf("audit actions %v", actions)
			}
			acc, err := s.st.GetAccount(ctx, s.acc.ID)
			if err != nil || acc.RemovedAt == nil {
				t.Fatalf("account %+v %v", acc, err)
			}
			if !tc.hold {
				rules, _ := s.st.RetentionRules(ctx)
				if len(rules) != 1 || rules[0].ID != added {
					t.Errorf("rules %+v", rules)
				}
			}
			s.verifyClean(t, p.Deleted)
		})
	}
}

func TestPurgeWaitsForSync(t *testing.T) {
	ctx := context.Background()
	s := newPurgeSetup(t)
	id := s.confirm(t)
	unlock, ok, err := s.st.TryLockSync(ctx, s.acc.ID)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if _, err := ExecutePurge(ctx, s.st, id, PurgeOptions{}); !errors.Is(err, ErrSyncRunning) {
		t.Fatalf("while syncing: %v", err)
	}
	unlock()
	if p, _ := s.st.AccountPurge(ctx, id); p.State != store.PurgeConfirmed || p.Locations != 0 {
		t.Fatalf("purge %+v", p)
	}
	if p, err := ExecutePurge(ctx, s.st, id, PurgeOptions{}); err != nil || p.State != store.PurgeDone {
		t.Fatalf("after the sync: %+v %v", p, err)
	}
}

// TestPurgeWithWaitingWriter (review of #165): with a pool of four
// connections, a writer that waits for the blob lock while a batch holds
// it must not keep the batch from its connection. The deadline only
// guards against the deadlock.
func TestPurgeWithWaitingWriter(t *testing.T) {
	s := newPurgeSetup(t)
	id := s.confirm(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	small, err := store.Open(ctx, storetest.WithMaxConns(t, s.url, 4))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(small.Close)
	observer, err := pgx.Connect(ctx, s.url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close(context.Background()) })
	bobMail, err := small.GetOwnedAccount(ctx, s.bob.ID, "mail")
	if err != nil {
		t.Fatal(err)
	}
	writer := make(chan error, 1)
	started := false
	// The writer is collected before the small store closes, also when
	// the test fails: pgxpool.Close waits for every connection.
	t.Cleanup(func() {
		if started {
			<-writer
		}
	})
	p, err := ExecutePurge(ctx, small, id, PurgeOptions{
		BatchSize: 1,
		// Called with the blob lock held, before the batch.
		Audit: func(action string) *store.AuditEntry {
			if action == "account.purge_batch" && !started {
				started = true
				go func() {
					// A sync of bob's account: it holds a connection of the
					// pool while it waits for the blob lock.
					release, ok, err := small.TryLockSyncForWrite(ctx, bobMail.ID)
					if err == nil && !ok {
						err = errors.New("sync lock busy")
					}
					if ok {
						release()
					}
					writer <- err
				}()
				for ctx.Err() == nil {
					if waitingForBlobs(ctx, observer) != 0 {
						break
					}
				}
			}
			return &store.AuditEntry{Actor: store.ActorCLI, Action: action}
		},
	})
	if err != nil || p.State != store.PurgeDone {
		t.Fatalf("purge with a waiting writer: %+v %v", p, err)
	}
	err = <-writer
	started = false
	if err != nil {
		t.Fatalf("writer: %v", err)
	}
}

// A purge whose account moved after the confirmation fails at every
// check; the job's log says how to cancel it.
func TestPurgeStaleLog(t *testing.T) {
	ctx := context.Background()
	s := newPurgeSetup(t)
	id := s.confirm(t)
	if err := s.st.SetAccountOwner(ctx, s.acc.Ref(), s.bob.ID); err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	job := &Retention{Store: s.st, Blobs: s.blobs, Logger: slog.New(slog.NewTextHandler(&buf, nil))}
	job.RunOnce(ctx)
	if !strings.Contains(buf.String(), `cancel_with="./ma account purge old --user bob --cancel"`) {
		t.Errorf("log:\n%s", buf.String())
	}
	if p, _ := s.st.AccountPurge(ctx, id); p.State != store.PurgeConfirmed {
		t.Errorf("purge %+v", p)
	}
}
