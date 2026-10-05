package store_test

import (
	"context"
	"testing"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

func TestMigrateDownAndUp(t *testing.T) {
	st := storetest.New(t) // already migrated up
	ctx := context.Background()

	assertStatus := func(wantApplied bool) {
		t.Helper()
		statuses, err := st.MigrationStatus(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(statuses) == 0 {
			t.Fatal("no migrations found")
		}
		for _, s := range statuses {
			if s.Applied != wantApplied {
				t.Fatalf("%s: applied=%v, want %v", s.Path, s.Applied, wantApplied)
			}
		}
	}
	assertStatus(true)

	// Roll back every migration, one at a time.
	for {
		statuses, err := st.MigrationStatus(ctx)
		if err != nil {
			t.Fatal(err)
		}
		applied := 0
		for _, s := range statuses {
			if s.Applied {
				applied++
			}
		}
		if applied == 0 {
			break
		}
		if _, err := st.MigrateDown(ctx); err != nil {
			t.Fatalf("down: %v", err)
		}
	}
	assertStatus(false)
	if _, err := st.ListAccounts(ctx); err == nil {
		t.Fatal("accounts table should be gone after rolling back")
	}
	if _, err := st.MigrateDown(ctx); err == nil {
		t.Fatal("expected error when nothing is left to roll back")
	}

	// Up again restores a working schema.
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	assertStatus(true)
	if err := st.CreateAccount(ctx, &store.Account{
		Name: "x", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1},
	}); err != nil {
		t.Fatal(err)
	}
}
