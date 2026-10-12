package archive

import (
	"context"
	"errors"
	"fmt"
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
