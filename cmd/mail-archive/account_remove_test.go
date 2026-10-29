package main

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
	"github.com/pklnx/mail-archive/internal/testwait"
)

// TestAccountRemoveBusy: "account remove" refuses while another session
// holds the account's sync lock, and works once it is released. Neither
// run leaves an advisory lock behind.
func TestAccountRemoveBusy(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	t.Setenv("MAIL_ARCHIVE_DATABASE_URL", url)
	key, _ := crypto.GenerateKey()
	t.Setenv("MAIL_ARCHIVE_SECRET_KEY", key)
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	a := &store.Account{Name: "work", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, Enabled: true}
	if err := st.CreateAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	obs, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = obs.Close(context.Background()) }()
	// othersLocks counts the advisory locks of the test's database that
	// other sessions than the observer hold or wait for.
	othersLocks := func() int {
		t.Helper()
		var n int
		if err := obs.QueryRow(ctx, `SELECT count(*) FROM pg_locks
			WHERE locktype = 'advisory' AND pid <> pg_backend_pid()
			  AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if _, err := obs.Exec(ctx, "SELECT pg_advisory_lock($1, $2)", 0x6d61, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := runCmd(t, newAccountCmd(), "", "remove", "work"); !errors.Is(err, archive.ErrSyncRunning) {
		t.Fatalf("remove while synced: %v", err)
	}
	if got, err := st.GetAccount(ctx, a.ID); err != nil || got.Version != a.Version {
		t.Fatalf("account changed: %+v %v", got, err)
	}
	if n := othersLocks(); n != 0 {
		t.Fatalf("the refused remove left %d advisory locks", n)
	}
	var unlocked bool
	if err := obs.QueryRow(ctx, "SELECT pg_advisory_unlock($1, $2)", 0x6d61, a.ID).Scan(&unlocked); err != nil || !unlocked {
		t.Fatalf("unlock the sync lock: %v %v", unlocked, err)
	}
	if err := runCmd(t, newAccountCmd(), "", "remove", "work"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetAccount(ctx, a.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("account not deleted: %v", err)
	}
	if n := othersLocks(); n != 0 {
		t.Fatalf("the remove left %d advisory locks", n)
	}
}
