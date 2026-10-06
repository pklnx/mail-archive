package store_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// migrateDownThrough rolls back migrations until the named one is rolled
// back, and returns the error of the first failing step.
func migrateDownThrough(st *store.Store, name string) error {
	for {
		r, err := st.MigrateDown(context.Background())
		if err != nil {
			return err
		}
		if strings.HasSuffix(r, name) {
			return nil
		}
	}
}

func TestOwnerMigration(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	if err := migrateDownThrough(st, "00005_account_owner.sql"); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	exec := func(sql string) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	// An older user, but the admin gets the accounts.
	exec(`INSERT INTO users (name, password_hash, is_admin) VALUES ('kid', 'h', FALSE), ('patrick', 'h', TRUE), ('other', 'h', TRUE)`)
	exec(`INSERT INTO accounts (name, host, port, tls_mode, username, password_enc) VALUES ('personal', 'h', 993, 'tls', 'u', '\x01'), ('work', 'h', 993, 'tls', 'u', '\x01')`)
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	patrick, _ := st.GetUserByName(ctx, "patrick")
	accounts, _ := st.ListAccounts(ctx)
	for _, a := range accounts {
		if a.OwnerID == nil || *a.OwnerID != patrick.ID {
			t.Errorf("%s: owner %v, want %d", a.Name, a.OwnerID, patrick.ID)
		}
	}

	// Names are unique per owner only.
	other, _ := st.GetUserByName(ctx, "other")
	dup := &store.Account{Name: "personal", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, OwnerID: &other.ID}
	if err := st.CreateAccount(ctx, dup); err != nil {
		t.Fatalf("same name for another user: %v", err)
	}
	again := *dup
	if err := st.CreateAccount(ctx, &again); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("same name for the same user: %v", err)
	}

	// Rolling back needs globally unique names again.
	if err := migrateDownThrough(st, "00005_account_owner.sql"); err == nil || !strings.Contains(err.Error(), "same name") {
		t.Fatalf("down with duplicate names: %v", err)
	}
	exec(`UPDATE accounts SET name = 'personal-2' WHERE id = ` + itoa(dup.ID))
	if err := migrateDownThrough(st, "00005_account_owner.sql"); err != nil {
		t.Fatalf("down: %v", err)
	}
	exec(`INSERT INTO accounts (name, host, port, tls_mode, username, password_enc) VALUES ('fresh', 'h', 993, 'tls', 'u', '\x01')`)
	if _, err := conn.Exec(ctx, `INSERT INTO accounts (name, host, port, tls_mode, username, password_enc) VALUES ('fresh', 'h', 993, 'tls', 'u', '\x01')`); err == nil {
		t.Fatal("duplicate name accepted after rollback")
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestFirstUserAdoptsAccounts(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	orphan := createAccount(t, st, "before-users")
	if orphan.OwnerID != nil {
		t.Fatal("owner set without users")
	}
	first, err := st.CreateUser(ctx, "first", "h", true)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := st.CreateUser(ctx, "second", "h", false)
	a, _ := accountByName(st, "before-users")
	if a.OwnerID == nil || *a.OwnerID != first.ID {
		t.Fatalf("owner = %v, want %d", a.OwnerID, first.ID)
	}

	// Accounts created later without owner are not adopted by others.
	late := createAccount(t, st, "late")
	third, _ := st.CreateUser(ctx, "third", "h", false)
	if got, _ := accountByName(st, "late"); got.OwnerID != nil {
		t.Fatalf("adopted by a later user (%d)", third.ID)
	}

	// Moving between users, with name conflicts per owner.
	if err := st.SetAccountOwner(ctx, a.Ref(), second.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.ListOwnedAccounts(ctx, second.ID); len(got) != 1 || got[0].Name != "before-users" {
		t.Fatalf("second owns %v", got)
	}
	clash := &store.Account{Name: "before-users", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, OwnerID: &first.ID}
	if err := st.CreateAccount(ctx, clash); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAccountOwner(ctx, clash.Ref(), second.ID); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("move onto a taken name: %v", err)
	}
	if err := st.SetAccountOwner(ctx, late.Ref(), first.ID); err != nil {
		t.Fatal(err)
	}

	// Owners cannot be deleted.
	if err := st.DeleteUser(ctx, second.ID); !errors.Is(err, store.ErrOwnsAccounts) {
		t.Fatalf("delete an owner: %v", err)
	}
	a, _ = st.GetAccount(ctx, a.ID)
	if _, err := st.DeleteOrRemoveAccount(ctx, a.Ref()); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteUser(ctx, second.ID); err != nil {
		t.Fatalf("delete after the account was deleted: %v", err)
	}
	counts, _ := st.CountOwnedAccounts(ctx)
	if counts[first.ID] != 2 || counts[third.ID] != 0 {
		t.Fatalf("counts: %v", counts)
	}
}
