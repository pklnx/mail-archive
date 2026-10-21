package archive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/attest"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

type retentionSetup struct {
	st     *store.Store
	url    string
	blobs  *blobstore.Store
	user   *store.User
	acc    *store.Account
	folder *store.Folder
	shas   []string
}

// newRetentionSetup archives n messages from 2010 into alice's account
// "work", which has a 1-year retention rule, so all of them may go.
func newRetentionSetup(t *testing.T, n int) *retentionSetup {
	t.Helper()
	ctx := context.Background()
	st, url := storetest.NewWithURL(t)
	blobs, err := blobstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.CreateUser(ctx, "alice", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	acc := &store.Account{Name: "work", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, OwnerID: &u.ID}
	if err := st.CreateAccount(ctx, acc); err != nil {
		t.Fatal(err)
	}
	f, err := st.GetOrCreateFolder(ctx, acc.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddRetentionRule(ctx, store.NewRetentionRule{AccountID: acc.ID, Years: 1}); err != nil {
		t.Fatal(err)
	}
	s := &retentionSetup{st: st, url: url, blobs: blobs, user: u, acc: acc, folder: f}
	sent := time.Date(2010, 3, 5, 10, 0, 0, 0, time.UTC)
	for i := range n {
		b, _, err := blobs.Put(strings.NewReader(fmt.Sprintf("Subject: old %d\r\nDate: Fri, 05 Mar 2010 10:00:00 +0000\r\n\r\nhi\r\n", i)))
		if err != nil {
			t.Fatal(err)
		}
		meta := store.MessageMeta{SHA256: b.SHA256, Size: b.Size, StoredPath: b.Path, Subject: fmt.Sprint("old ", i), SentAt: &sent}
		loc := store.Location{FolderID: f.ID, UID: uint32(i + 1), InternalDate: sent}
		if _, err := st.SaveBatch(ctx, f.ID, uint32(i+1), []store.MessageMeta{meta}, []store.Location{loc}); err != nil {
			t.Fatal(err)
		}
		s.shas = append(s.shas, b.SHA256)
	}
	return s
}

func (s *retentionSetup) confirmedRun(t *testing.T) int64 {
	t.Helper()
	ctx := context.Background()
	run, err := s.st.ProposeDeletion(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.st.ConfirmDeletionRun(ctx, run.ID, nil); err != nil {
		t.Fatal(err)
	}
	return run.ID
}

func (s *retentionSetup) files() int {
	n := 0
	for _, sha := range s.shas {
		if s.blobs.Exists(sha) {
			n++
		}
	}
	return n
}

func TestExecuteDeletionRun(t *testing.T) {
	ctx := context.Background()
	s := newRetentionSetup(t, 3)
	id := s.confirmedRun(t)

	var actions []string
	run, err := ExecuteDeletionRun(ctx, s.st, id, DeletionOptions{
		BatchSize: 2,
		Audit: func(action string) *store.AuditEntry {
			actions = append(actions, action)
			return &store.AuditEntry{Actor: store.ActorCLI, Action: action}
		},
	})
	if err != nil || run.State != store.RunDone || run.Deleted != 3 || run.Kept != 0 {
		t.Fatalf("run %+v %v", run, err)
	}
	if strings.Join(actions, ",") != "retention.batch,retention.batch,retention.finish" {
		t.Errorf("audit actions %v", actions)
	}
	// The files stay for the grace period; verify does not call them
	// orphans.
	if n := s.files(); n != 3 {
		t.Fatalf("%d files after the run", n)
	}
	res, err := Verify(ctx, s.st, s.blobs, VerifyOptions{LockTimeout: time.Second})
	if err != nil || res.Problems() != 0 || res.PendingRemovals != 3 {
		t.Fatalf("verify within the grace: %+v %v", res, err)
	}
	// A done run is returned as it is.
	if again, err := ExecuteDeletionRun(ctx, s.st, id, DeletionOptions{}); err != nil || again.Deleted != 3 {
		t.Fatalf("again: %+v %v", again, err)
	}

	// The removals were scheduled with no grace (Grace 0): the sweep
	// removes the files.
	n, err := SweepBlobs(ctx, s.st, s.blobs, SweepOptions{})
	if err != nil || n != 3 || s.files() != 0 {
		t.Fatalf("sweep: %d %v, %d files left", n, err, s.files())
	}
	if n, err := SweepBlobs(ctx, s.st, s.blobs, SweepOptions{}); err != nil || n != 0 {
		t.Fatalf("second sweep: %d %v", n, err)
	}
}

// A removal that is not durable leaves its cleanup record pending, and the
// retry completes it although the file is gone already.
func TestSweepKeepsRemovalsUntilDurable(t *testing.T) {
	ctx := context.Background()
	s := newRetentionSetup(t, 1)
	id := s.confirmedRun(t)
	if _, err := ExecuteDeletionRun(ctx, s.st, id, DeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	// The store root is unavailable: the absence of the file cannot be
	// made durable.
	root := s.blobs.Root()
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if n, err := SweepBlobs(ctx, s.st, s.blobs, SweepOptions{}); err == nil {
		t.Fatalf("sweep without the store root: %d removed, no error", n)
	}
	if due, err := s.st.BlobRemovalDue(ctx); err != nil || !due {
		t.Fatalf("removal forgotten after the failure: due=%v %v", due, err)
	}

	// Back, but without the file: the retry finishes the removal.
	if err := os.MkdirAll(filepath.Join(root, "messages"), 0o700); err != nil {
		t.Fatal(err)
	}
	if n, err := SweepBlobs(ctx, s.st, s.blobs, SweepOptions{}); err != nil || n != 1 {
		t.Fatalf("retry: %d %v", n, err)
	}
	if due, err := s.st.BlobRemovalDue(ctx); err != nil || due {
		t.Fatalf("removal still due after the retry: due=%v %v", due, err)
	}
}

func TestDeletionRunResumes(t *testing.T) {
	s := newRetentionSetup(t, 3)
	id := s.confirmedRun(t)
	ctx, cancel := context.WithCancel(context.Background())
	_, err := ExecuteDeletionRun(ctx, s.st, id, DeletionOptions{BatchSize: 1, Progress: func(int, int) { cancel() }})
	if err == nil {
		t.Fatal("the cancelled run finished")
	}
	run, err := s.st.DeletionRun(context.Background(), id)
	if err != nil || run.State != store.RunConfirmed || run.Deleted != 1 {
		t.Fatalf("interrupted run %+v %v", run, err)
	}
	run, err = ExecuteDeletionRun(context.Background(), s.st, id, DeletionOptions{BatchSize: 1})
	if err != nil || run.State != store.RunDone || run.Deleted != 3 {
		t.Fatalf("resumed run %+v %v", run, err)
	}
}

// A sync writing files holds the blob lock shared; a batch waits for it,
// and gives up after the lock timeout.
func TestDeletionWaitsForSyncs(t *testing.T) {
	ctx := context.Background()
	s := newRetentionSetup(t, 1)
	id := s.confirmedRun(t)
	unlock, ok, err := s.st.TryLockSyncForWrite(ctx, s.acc.ID)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if _, err := ExecuteDeletionRun(ctx, s.st, id, DeletionOptions{LockTimeout: 200 * time.Millisecond}); !errors.Is(err, ErrBlobsBusy) {
		t.Fatalf("with a sync running: %v", err)
	}
	unlock()
	run, err := ExecuteDeletionRun(ctx, s.st, id, DeletionOptions{})
	if err != nil || run.Deleted != 1 {
		t.Fatalf("after the sync: %+v %v", run, err)
	}
}

func TestVerifyDeletedFiles(t *testing.T) {
	ctx := context.Background()
	s := newRetentionSetup(t, 1)
	id := s.confirmedRun(t)
	if _, err := ExecuteDeletionRun(ctx, s.st, id, DeletionOptions{Grace: time.Hour}); err != nil {
		t.Fatal(err)
	}
	// The removal record is lost: verify reports the file, and
	// --fix-orphans schedules it instead of moving deleted mail aside.
	c, err := pgx.Connect(ctx, s.url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close(ctx) }()
	if _, err := c.Exec(ctx, "DELETE FROM blob_removals"); err != nil {
		t.Fatal(err)
	}
	var found []Finding
	res, err := Verify(ctx, s.st, s.blobs, VerifyOptions{LockTimeout: time.Second, OnFinding: func(f Finding) { found = append(found, f) }})
	if err != nil || len(found) != 1 || found[0].Kind != FindingDeleted || found[0].SHA256 != s.shas[0] {
		t.Fatalf("lost removal: %+v %+v %v", found, res, err)
	}
	res, err = Verify(ctx, s.st, s.blobs, VerifyOptions{LockTimeout: time.Second, FixOrphans: true})
	if err != nil || res.Scheduled != 1 || res.Moved != 0 {
		t.Fatalf("fix: %+v %v", res, err)
	}
	if n, err := SweepBlobs(ctx, s.st, s.blobs, SweepOptions{}); err != nil || n != 1 {
		t.Fatalf("sweep: %d %v", n, err)
	}
}

// Deletions by retention are recorded for the attestation chain: the
// check reports nothing, and the next attestation signs them.
func TestDeletionAndAttestations(t *testing.T) {
	ctx := context.Background()
	s := newRetentionSetup(t, 2)
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
	if _, err := ExecuteDeletionRun(ctx, s.st, s.confirmedRun(t), DeletionOptions{}); err != nil {
		t.Fatal(err)
	}
	var found []Finding
	res, err := CheckAttestations(ctx, s.st, AttestCheckOptions{Dir: dir, KeyExists: true, OnFinding: func(f Finding) { found = append(found, f) }})
	if err != nil || len(found) != 0 || len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "2 deletion(s) not yet covered") {
		t.Fatalf("after the deletion: %+v %+v %v", found, res, err)
	}
	e, err := Attest(ctx, s.st, dir, key)
	if err != nil || e.Entry.DeletionsCount != 2 || e.Entry.Total != 0 {
		t.Fatalf("next attestation %+v %v", e.Entry, err)
	}
	found = nil
	if _, err := CheckAttestations(ctx, s.st, AttestCheckOptions{Dir: dir, KeyExists: true, OnFinding: func(f Finding) { found = append(found, f) }}); err != nil || len(found) != 0 {
		t.Fatalf("signed: %+v %v", found, err)
	}
}

// oldMessage is message i of newRetentionSetup.
func oldMessage(i int) string {
	return fmt.Sprintf("Subject: old %d\r\nDate: Fri, 05 Mar 2010 10:00:00 +0000\r\n\r\nhi\r\n", i)
}

// fileBytes returns the content of sha's file.
func (s *retentionSetup) fileBytes(t *testing.T, sha string) string {
	t.Helper()
	f, err := s.blobs.Open(blobstore.RelPath(sha))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// count returns the rows of table (deleted_messages, message_deletions or
// blob_removals) for sha.
func (s *retentionSetup) count(t *testing.T, table, sha string) int {
	t.Helper()
	ctx := context.Background()
	c, err := pgx.Connect(ctx, s.url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close(ctx) }()
	var n int
	if err := c.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{table}.Sanitize()+" WHERE sha256 = $1", sha).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A message shared with bob stays, file included, while bob's location is
// unexpired or on bob's hold: whether that was so at the preview, or came
// after it. Message 0 is shared; message 1, wholly expired, goes.
func TestDeletionKeepsProtectedSharedMessage(t *testing.T) {
	for _, tc := range []struct {
		name string
		hold bool // bob's location is expired too, but bob has a hold
		late bool // the protection comes after the confirmation
	}{
		{name: "unexpired before the preview"},
		{name: "hold before the preview", hold: true},
		{name: "unexpired after the preview", late: true},
		{name: "hold after the preview", hold: true, late: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s := newRetentionSetup(t, 2)
			shared, control := s.shas[0], s.shas[1]
			bob, err := s.st.CreateUser(ctx, "bob", "h", false)
			if err != nil {
				t.Fatal(err)
			}
			acc := &store.Account{Name: "private", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, OwnerID: &bob.ID}
			if err := s.st.CreateAccount(ctx, acc); err != nil {
				t.Fatal(err)
			}
			folder, err := s.st.GetOrCreateFolder(ctx, acc.ID, "INBOX")
			if err != nil {
				t.Fatal(err)
			}
			// A normal rule: bob's location expires by its own dates.
			if _, err := s.st.AddRetentionRule(ctx, store.NewRetentionRule{AccountID: acc.ID, Years: 1}); err != nil {
				t.Fatal(err)
			}
			sent := time.Date(2010, 3, 5, 10, 0, 0, 0, time.UTC)
			addBobLocation := func() {
				t.Helper()
				internal := time.Now() // unexpired
				if tc.hold {
					internal = sent
				}
				meta := store.MessageMeta{SHA256: shared, Size: int64(len(oldMessage(0))), StoredPath: blobstore.RelPath(shared), Subject: "old 0", SentAt: &sent}
				loc := store.Location{FolderID: folder.ID, UID: 1, InternalDate: internal}
				if _, err := s.st.SaveBatch(ctx, folder.ID, 1, []store.MessageMeta{meta}, []store.Location{loc}); err != nil {
					t.Fatal(err)
				}
			}
			addHold := func() {
				t.Helper()
				if _, err := s.st.AddLegalHold(ctx, store.NewLegalHold{Name: "bob", OwnerID: &bob.ID}); err != nil {
					t.Fatal(err)
				}
			}
			// Bob's expired location for a hold is there before the preview
			// in both hold cases: the SHA is already shared.
			if !tc.late || tc.hold {
				addBobLocation()
			}
			if tc.hold && !tc.late {
				addHold()
			}

			id := s.confirmedRun(t)
			cands, err := s.st.PendingCandidates(ctx, id, "", 10)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{control}
			if tc.late {
				want = []string{shared, control}
				slices.Sort(want)
			}
			if !slices.Equal(cands, want) {
				t.Fatalf("candidates %v, want %v", cands, want)
			}
			if run, err := s.st.DeletionRun(ctx, id); err != nil || run.Candidates != len(want) {
				t.Fatalf("run %+v %v", run, err)
			}

			if tc.late {
				if tc.hold {
					addHold()
				} else {
					addBobLocation()
				}
			}
			run, err := ExecuteDeletionRun(ctx, s.st, id, DeletionOptions{})
			wantKept := 0
			if tc.late {
				wantKept = 1
			}
			if err != nil || run.State != store.RunDone || run.Deleted != 1 || run.Kept != wantKept {
				t.Fatalf("run %+v %v, want 1 deleted, %d kept", run, err, wantKept)
			}
			if n, err := SweepBlobs(ctx, s.st, s.blobs, SweepOptions{}); err != nil || n != 1 {
				t.Fatalf("sweep: %d %v", n, err)
			}

			// Both owners still read the shared message and its file.
			for _, o := range []struct {
				user    *store.User
				account string
			}{{s.user, "work"}, {bob, "private"}} {
				d, err := s.st.GetMessageDetail(ctx, o.user.ID, shared)
				if err != nil {
					t.Fatalf("%s: %v", o.user.Name, err)
				}
				if len(d.Locations) != 1 || d.Locations[0].Account != o.account {
					t.Errorf("%s's locations %+v", o.user.Name, d.Locations)
				}
			}
			if !s.blobs.Exists(shared) || s.fileBytes(t, shared) != oldMessage(0) {
				t.Error("the shared file is gone or changed")
			}
			for _, table := range []string{"deleted_messages", "message_deletions", "blob_removals"} {
				if n := s.count(t, table, shared); n != 0 {
					t.Errorf("%d %s rows for the shared message", n, table)
				}
			}
			// The control is gone, row and file; its removal is done.
			if _, err := s.st.GetMessageDetail(ctx, s.user.ID, control); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("control: %v", err)
			}
			if s.blobs.Exists(control) {
				t.Error("the control's file is still there")
			}
			if s.count(t, "deleted_messages", control) != 1 || s.count(t, "blob_removals", control) != 0 {
				t.Error("the control's tombstone or removal")
			}
		})
	}
}
