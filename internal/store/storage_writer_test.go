package store_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
	"github.com/pklnx/mail-archive/internal/testwait"
)

// serve's storage state writer lock is held outside the pool: with the
// default pool of 4 (2 cores), a sync and a fill holding their locks and
// a deletion session waiting for the blob lock use 3 connections, and the
// sync's next query still gets the fourth (review of #573, round 2).
func TestStorageWriterOutsidePool(t *testing.T) {
	_, url := storetest.NewWithURL(t)
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.WithMaxConns(t, url, 4))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a, b := createAccount(t, st, "sync"), createAccount(t, st, "fill")
	for _, acc := range []*store.Account{a, b} {
		unlock, ok, err := st.TryLockSyncForWrite(ctx, acc.ID)
		if err != nil || !ok {
			t.Fatalf("sync lock of %s: %v, %v", acc.Name, ok, err)
		}
		defer unlock()
	}
	sess, ok, err := st.StartDeletionSession(ctx)
	if err != nil || !ok {
		t.Fatalf("deletion session: %v, %v", ok, err)
	}
	defer sess.Close(ctx)
	if got, err := sess.TryLockBlobs(ctx); err != nil || got {
		t.Fatalf("the deletion's blob lock while syncs hold it shared: %v, %v", got, err)
	}
	w := st.StorageWriter(true)
	defer w.Close()
	if _, ok, err := w.TryLock(ctx); err != nil || !ok {
		t.Fatalf("writer lock: %v, %v", ok, err)
	}
	qctx, cancel := context.WithTimeout(ctx, testwait.Limit(t))
	defer cancel()
	if _, err := st.GetAccount(qctx, a.ID); err != nil {
		t.Fatalf("the sync's next query with the writer lock held: %v", err)
	}
}

// serve notices when its lock's session ended (a PostgreSQL restart, a
// terminated backend) and takes the lock again; while another process
// holds it meanwhile, serve does not have it.
func TestStorageWriterRetakesLostLock(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(ctx) }()
	serve := st.StorageWriter(true)
	defer serve.Close()
	if _, ok, err := serve.TryLock(ctx); err != nil || !ok {
		t.Fatalf("serve's lock: %v, %v", ok, err)
	}
	terminate := func() {
		t.Helper()
		var n int
		if err := admin.QueryRow(ctx, `SELECT count(pg_terminate_backend(l.pid)) FROM pg_locks l
			JOIN pg_stat_activity a ON a.pid = l.pid
			WHERE a.datname = current_database() AND l.locktype = 'advisory' AND l.classid = 28019 AND l.granted`).Scan(&n); err != nil || n != 1 {
			t.Fatalf("terminated %d holders: %v", n, err)
		}
	}
	terminate()
	// A command now gets the lock for its read; serve does not.
	command := st.StorageWriter(false)
	unlock, ok, err := command.TryLock(ctx)
	if err != nil || !ok {
		t.Fatalf("command after serve's session ended: %v, %v", ok, err)
	}
	if _, ok, _ := serve.TryLock(ctx); ok {
		t.Fatal("serve believes it holds the lock while the command holds it")
	}
	unlock()
	if _, ok, err := serve.TryLock(ctx); err != nil || !ok || serve.Lost() != 1 {
		t.Fatalf("serve took the lock again: %v, %v, lost %d", ok, err, serve.Lost())
	}
	if _, ok, _ := command.TryLock(ctx); ok {
		t.Fatal("a command took the lock while serve holds it again")
	}
}
