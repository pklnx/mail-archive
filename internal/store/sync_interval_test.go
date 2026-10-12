package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

func TestAccountSyncInterval(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	a := createAccount(t, st, "work")
	if a.SyncInterval != nil {
		t.Fatalf("new account: own interval %v", *a.SyncInterval)
	}
	set := func(d *time.Duration) *store.Account {
		t.Helper()
		cur, err := st.GetAccount(ctx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.UpdateAccount(ctx, cur.Ref(), store.AccountChange{SyncInterval: &store.OwnInterval{D: d}}); err != nil {
			t.Fatal(err)
		}
		got, err := st.GetAccount(ctx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Version != cur.Version+1 {
			t.Errorf("version %d after %d", got.Version, cur.Version)
		}
		return got
	}
	half, zero := 30*time.Minute, time.Duration(0)
	if got := set(&half); got.SyncInterval == nil || *got.SyncInterval != half {
		t.Errorf("set 30m: %v", got.SyncInterval)
	}
	// Other changes keep it.
	cur, _ := st.GetAccount(ctx, a.ID)
	off := false
	if err := st.UpdateAccount(ctx, cur.Ref(), store.AccountChange{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetAccount(ctx, a.ID); got.SyncInterval == nil || *got.SyncInterval != half {
		t.Errorf("after disabling: %v", got.SyncInterval)
	}
	if got := set(&zero); got.SyncInterval == nil || *got.SyncInterval != 0 {
		t.Errorf("set manual: %v", got.SyncInterval)
	}
	if got := set(nil); got.SyncInterval != nil {
		t.Errorf("reset to default: %v", *got.SyncInterval)
	}
	set(&half)

	// Stats and SyncHealth carry it.
	stats, _, err := st.Stats(ctx, nil)
	if err != nil || len(stats) != 1 || stats[0].SyncInterval == nil || *stats[0].SyncInterval != half ||
		stats[0].CreatedAt.IsZero() || stats[0].RemovedAt != nil {
		t.Errorf("stats: %+v, %v", stats, err)
	}
	health, err := st.SyncHealth(ctx, nil)
	if err != nil || health[a.ID] == nil || health[a.ID].SyncInterval == nil || *health[a.ID].SyncInterval != half {
		t.Errorf("health: %+v, %v", health[a.ID], err)
	}

	// Only IMAP accounts have one.
	smtp := createSMTPAccount(t, st, nil, "journal", "archive@example.com")
	if err := st.UpdateAccount(ctx, smtp.Ref(), store.AccountChange{SyncInterval: &store.OwnInterval{D: &half}}); !errors.Is(err, store.ErrNoServer) {
		t.Errorf("smtp account: %v", err)
	}
	imp := &store.Account{Kind: store.KindImport, Name: "old", SyncInterval: &half}
	if err := st.CreateAccount(ctx, imp); !errors.Is(err, store.ErrNoServer) {
		t.Errorf("import account: %v", err)
	}

	// The database refuses what the API refuses.
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	for _, sql := range []string{
		"UPDATE accounts SET sync_interval_seconds = 60 WHERE kind = 'imap'",
		"UPDATE accounts SET sync_interval_seconds = 1830 WHERE kind = 'imap'",
		"UPDATE accounts SET sync_interval_seconds = 2592060 WHERE kind = 'imap'",
		"UPDATE accounts SET sync_interval_seconds = 1800 WHERE kind = 'smtp'",
	} {
		if _, err := conn.Exec(ctx, sql); err == nil {
			t.Errorf("accepted: %s", sql)
		}
	}
	for _, sql := range []string{
		"UPDATE accounts SET sync_interval_seconds = 0 WHERE kind = 'imap'",
		"UPDATE accounts SET sync_interval_seconds = 2592000 WHERE kind = 'imap'",
	} {
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Errorf("%s: %v", sql, err)
		}
	}
	if err := migrateDownThrough(st, "00024_account_sync_interval.sql"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetAccount(ctx, a.ID); got.SyncInterval != nil {
		t.Errorf("after down and up: %v", *got.SyncInterval)
	}
}
