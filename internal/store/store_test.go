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

func TestStatsWithAndWithoutSyncRuns(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	for _, name := range []string{"fresh", "synced"} {
		if err := st.CreateAccount(ctx, &store.Account{
			Name: name, Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1},
		}); err != nil {
			t.Fatal(err)
		}
	}
	synced, err := accountByName(st, "synced")
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"failed", "ok"} { // the latest run must win
		run, err := st.StartSyncRun(ctx, synced.ID)
		if err != nil {
			t.Fatal(err)
		}
		run.Status = status
		if err := st.FinishSyncRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}

	stats, unique, err := st.Stats(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if unique != 0 || len(stats) != 2 {
		t.Fatalf("unique=%d stats=%+v", unique, stats)
	}
	fresh, done := stats[0], stats[1]
	if fresh.Account != "fresh" || fresh.LastRunAt != nil || fresh.LastStatus != nil {
		t.Errorf("fresh account: %+v", fresh)
	}
	if done.Account != "synced" || done.LastRunAt == nil || done.LastStatus == nil || *done.LastStatus != "ok" {
		t.Errorf("synced account: %+v", done)
	}
}
