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
)

// TestAccountRemoveBusy: "account remove" refuses while another session
// holds the account's sync lock, and works once it is released.
func TestAccountRemoveBusy(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	t.Setenv("MAIL_ARCHIVE_DATABASE_URL", url)
	key, _ := crypto.GenerateKey()
	t.Setenv("MAIL_ARCHIVE_SECRET_KEY", key)
	ctx := context.Background()
	a := &store.Account{Name: "work", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, Enabled: true}
	if err := st.CreateAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	obs, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = obs.Close(ctx) }()
	if _, err := obs.Exec(ctx, "SELECT pg_advisory_lock($1, $2)", 0x6d61, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := runCmd(t, newAccountCmd(), "", "remove", "work"); !errors.Is(err, archive.ErrSyncRunning) {
		t.Fatalf("remove while synced: %v", err)
	}
	if got, err := st.GetAccount(ctx, a.ID); err != nil || got.Version != a.Version {
		t.Fatalf("account changed: %+v %v", got, err)
	}
	if _, err := obs.Exec(ctx, "SELECT pg_advisory_unlock($1, $2)", 0x6d61, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := runCmd(t, newAccountCmd(), "", "remove", "work"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetAccount(ctx, a.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("account not deleted: %v", err)
	}
}
