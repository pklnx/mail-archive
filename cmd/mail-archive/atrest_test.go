package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/blobcrypt"
	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/testwait"
)

// enableForTest records encryption at rest in state with a new keyring in
// the data directory, as part 3's commands do, and returns the key.
func enableForTest(t *testing.T, st *store.Store, state string) []byte {
	t.Helper()
	kek, err := blobcrypt.GenerateKEK()
	if err != nil {
		t.Fatal(err)
	}
	ring, err := blobcrypt.NewKeyring(kek, "archive key", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	dataDir := config.DataDirFromEnv()
	if err := blobcrypt.WriteKeyringFile(blobcrypt.KeyringPath(dataDir), ring); err != nil {
		t.Fatal(err)
	}
	data, err := ring.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetBlobEncryption(context.Background(), state, store.KeyringCopy{
		Keyring: data, ID: ring.ArchiveID(), Generation: int64(ring.Generation()), CurrentKey: ring.CurrentKey(),
	}); err != nil {
		t.Fatal(err)
	}
	return kek
}

// The writer commands go through the start-up checks: during an unfinished
// conversion they refuse, and with encryption on they need the key.
func TestWriterCommandsCheckEncryption(t *testing.T) {
	st, _ := jobsEnv(t)
	secret, _ := crypto.GenerateKey()
	t.Setenv(config.EnvSecretKey, secret)
	u, err := st.CreateUser(context.Background(), "anna", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateAccount(context.Background(), &store.Account{Kind: store.KindImport, Name: "old", OwnerID: &u.ID}); err != nil {
		t.Fatal(err)
	}
	kek := enableForTest(t, st, store.BlobEncrypting)
	for name, cmd := range map[string]func() error{
		"sync":    func() error { return runCmd(t, newSyncCmd(), "") },
		"reindex": func() error { return runCmd(t, newReindexCmd(), "") },
		"worker":  func() error { t.Setenv(config.EnvJobWorkers, "1"); return runCmd(t, newWorkerCmd(), "") },
	} {
		if err := cmd(); !errors.Is(err, archive.ErrConversionInProgress) {
			t.Errorf("%s during a conversion: %v", name, err)
		}
	}
	if err := st.SetBlobEncryption(context.Background(), store.BlobOn, store.KeyringCopy{}); err != nil {
		t.Fatal(err)
	}
	if err := runCmd(t, newSyncCmd(), ""); !errors.Is(err, archive.ErrNoArchiveKey) {
		t.Errorf("sync without the key: %v", err)
	}
	if err := runCmd(t, newExportCmd(), "", "--format", "mbox", "--out", t.TempDir()+"/x", "--user", u.Name); !errors.Is(err, archive.ErrNoArchiveKey) {
		t.Errorf("export without the key: %v", err)
	}
	t.Setenv(config.EnvArchiveKey, blobcrypt.EncodeKEK(kek))
	if err := runCmd(t, newSyncCmd(), ""); err != nil {
		t.Errorf("sync with the key: %v", err)
	}
}

// A writer whose lock connection dies stops the process with a cause that
// main turns into a non-zero exit.
func TestWriterLockLossStopsProcess(t *testing.T) {
	_, conn := jobsEnv(t)
	tick := make(chan time.Time)
	causes := make(chan error, 2)
	oldTick, oldStop := writersTick, stopProcess
	writersTick, stopProcess = tick, func(err error) { causes <- err }
	t.Cleanup(func() { writersTick, stopProcess = oldTick, oldStop })

	ctx := context.Background()
	a, err := openApp(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	_, ar, err := openArchiveBlobs(ctx, a, "serve", archive.RoleWriter)
	if err != nil {
		t.Fatal(err)
	}
	defer ar.Close()
	var ok bool
	if err := conn.QueryRow(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name = $1`,
		store.WriterAppName("serve")).Scan(&ok); err != nil || !ok {
		t.Fatalf("terminate: %v %v", ok, err)
	}
	tick <- time.Now()
	select {
	case err := <-causes:
		var ws *writerStopped
		if !errors.As(err, &ws) || !errors.Is(err, store.ErrWritersLockLost) || !strings.Contains(err.Error(), "writers lock") {
			t.Fatalf("cause %v", err)
		}
	case <-time.After(testwait.Limit(t)):
		t.Fatal("the process was not stopped")
	}
}

// main exits non-zero after a writer stop, although serve returns nil on
// its cancelled context.
func TestExitStatusAfterWriterStop(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	if code, msg := exitStatus(ctx, nil); code != 0 || msg != "" {
		t.Fatalf("clean exit: %d %q", code, msg)
	}
	cancel(&writerStopped{err: store.ErrBlobStateChanged})
	if code, msg := exitStatus(ctx, nil); code != 1 || !strings.Contains(msg, "encryption at rest changed") {
		t.Fatalf("after a writer stop: %d %q", code, msg)
	}
	if code, _ := exitStatus(context.Background(), &exitError{msg: "x", code: 3}); code != 3 {
		t.Fatalf("exit error: %d", code)
	}
}
