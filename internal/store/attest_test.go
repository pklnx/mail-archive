package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// fenceClass is the attestation fence's lock class (attestFenceClass).
const fenceClass = 0x6d64

func shaOf(i int) string { return fmt.Sprintf("%064x", i) }

// insertRaw inserts a message by SQL on conn, as a writer that holds the
// fence would.
func insertRaw(t *testing.T, ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, i int,
) int64 {
	t.Helper()
	var seq int64
	if err := q.QueryRow(ctx, "INSERT INTO messages (sha256, size, stored_path) VALUES ($1, 1, 'x') RETURNING seq",
		shaOf(i)).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	return seq
}

func TestAttestMigration(t *testing.T) {
	ctx := context.Background()
	st, url := storetest.NewWithURL(t)
	conn := connect(t, url)
	for _, want := range []string{"00026_smtp_fills", "00025_user_settings", "00024_account_sync_interval", "00023_folder_delimiter", "00022_deletion_runs", "00021_retention_rules", "00020_api_keys", "00019_attestations"} {
		if m, err := st.MigrateDown(ctx); err != nil || !strings.Contains(m, want) {
			t.Fatalf("down: %q %v", m, err)
		}
	}
	if _, err := conn.Exec(ctx, "INSERT INTO messages (sha256, size, stored_path) SELECT lpad(to_hex(i), 64, '0'), 1, 'x' FROM generate_series(1, 5) i"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var n, distinct int
	if err := conn.QueryRow(ctx, "SELECT count(seq), count(DISTINCT seq) FROM messages").Scan(&n, &distinct); err != nil || n != 5 || distinct != 5 {
		t.Fatalf("seq of existing rows: %d %d %v", n, distinct, err)
	}
	// New rows get larger values; an explicit value is refused.
	if seq := insertRaw(t, ctx, conn, 100); seq <= 5 {
		t.Errorf("new seq %d", seq)
	}
	if _, err := conn.Exec(ctx, "INSERT INTO messages (sha256, size, stored_path, seq) VALUES ($1, 1, 'x', 1000)", shaOf(101)); err == nil {
		t.Error("explicit seq accepted")
	}
	if _, err := conn.Exec(ctx, "UPDATE messages SET seq = 1000 WHERE sha256 = $1", shaOf(100)); err == nil {
		t.Error("update of seq accepted")
	}

	// Both tables only grow.
	if _, err := conn.Exec(ctx, `INSERT INTO message_deletions (message_seq, sha256, reason) VALUES (999, $1, 'retention')`, shaOf(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO attestations VALUES (1, now(), 0, 0, 0, 0, 0, 0, 0, $1, $1, 'k', $1, 's')`, shaOf(0)); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		"UPDATE attestations SET total = 1", "DELETE FROM attestations", "TRUNCATE attestations",
		"UPDATE message_deletions SET reason = 'retention'", "DELETE FROM message_deletions", "TRUNCATE message_deletions",
	} {
		if _, err := conn.Exec(ctx, sql); err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Errorf("%s: %v", sql, err)
		}
	}
	for _, want := range []string{"00026_smtp_fills", "00025_user_settings", "00024_account_sync_interval", "00023_folder_delimiter", "00022_deletion_runs", "00021_retention_rules", "00020_api_keys"} {
		if m, err := st.MigrateDown(ctx); err != nil || !strings.Contains(m, want) {
			t.Fatalf("down: %q %v", m, err)
		}
	}
	if _, err := st.MigrateDown(ctx); err == nil || !strings.Contains(err.Error(), "attestations exist") {
		t.Errorf("down with attestations: %v", err)
	}
}

// TestAttestFence checks the ordering point between writers and the
// snapshot of an attestation: a writer in progress is waited for and its
// row is in the snapshot; a writer that starts after the snapshot does
// not wait for the attestation to finish and gets a larger seq.
func TestAttestFence(t *testing.T) {
	ctx := context.Background()
	st, url := storetest.NewWithURL(t)
	writer := connect(t, url)

	tx, err := writer.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared($1, 0)", fenceClass); err != nil {
		t.Fatal(err)
	}
	seq := insertRaw(t, ctx, tx, 1)

	unlock, ok, err := st.TryLockAttest(ctx)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	defer unlock()
	got := make(chan store.AttestBounds, 1)
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- st.WithAttestSnapshot(ctx, func(snap *store.AttestSnapshot) error {
			got <- snap.Bounds
			<-release
			return nil
		})
	}()
	select {
	case b := <-got:
		t.Fatalf("snapshot taken while a writer was in progress: %+v", b)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	b := <-got
	if b.MessagesUpto != seq || b.Total != 1 {
		t.Errorf("bounds %+v, want upto %d and total 1", b, seq)
	}

	// The fence is released while the attestation still runs.
	later := make(chan int64, 1)
	go func() {
		c := connect(t, url)
		tx, err := c.Begin(ctx)
		if err != nil {
			t.Error(err)
			later <- 0
			return
		}
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared($1, 0)", fenceClass); err != nil {
			t.Error(err)
		}
		var s int64
		if err := tx.QueryRow(ctx, "INSERT INTO messages (sha256, size, stored_path) VALUES ($1, 1, 'x') RETURNING seq", shaOf(2)).Scan(&s); err != nil {
			t.Error(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Error(err)
		}
		later <- s
	}()
	select {
	case s := <-later:
		if s <= b.MessagesUpto {
			t.Errorf("later writer got seq %d, not above %d", s, b.MessagesUpto)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a writer waited for the whole attestation")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// TestWritesTakeFence checks that the write paths wait while an
// attestation holds the fence.
func TestWritesTakeFence(t *testing.T) {
	ctx := context.Background()
	st, url := storetest.NewWithURL(t)
	smtp := createSMTPAccount(t, st, nil, "journal", "archive@example.com")
	acc := &store.Account{Name: "imap", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}}
	if err := st.CreateAccount(ctx, acc); err != nil {
		t.Fatal(err)
	}
	f, err := st.GetOrCreateFolder(ctx, acc.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}

	writes := map[string]func() error{
		"SaveBatch": func() error {
			_, err := st.SaveBatch(ctx, f.ID, 1, []store.MessageMeta{receivedMeta(1)}, []store.Location{{FolderID: f.ID, UID: 1}})
			return err
		},
		"ReceiveMessage": func() error {
			_, err := st.ReceiveMessage(ctx, smtp.ID, receivedMeta(2), envelope("archive@example.com"), present)
			return err
		},
	}
	for name, write := range writes {
		fence := connect(t, url)
		if _, err := fence.Exec(ctx, "SELECT pg_advisory_lock($1, 0)", fenceClass); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- write() }()
		select {
		case err := <-done:
			t.Errorf("%s did not wait for the fence: %v", name, err)
		case <-time.After(300 * time.Millisecond):
		}
		if _, err := fence.Exec(ctx, "SELECT pg_advisory_unlock($1, 0)", fenceClass); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestAttestSnapshotLockTimeoutReleases(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	st, url := storetest.NewWithURL(t)
	writer := connect(t, url)
	tx, err := writer.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared($1, 0)", fenceClass); err != nil {
		t.Fatal(err)
	}
	// A cancelled wait for the fence leaves no lock behind.
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	if err := st.WithAttestSnapshot(ctx, func(*store.AttestSnapshot) error { return nil }); err == nil {
		t.Fatal("snapshot taken while a writer held the fence")
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The closed connection's backend exits shortly after.
	var left int
	for range 50 {
		if err := writer.QueryRow(context.Background(),
			"SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND classid = $1", fenceClass).Scan(&left); err != nil {
			t.Fatal(err)
		}
		if left == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if left != 0 {
		t.Errorf("fence locks left: %d", left)
	}
	failing := errors.New("fail")
	if err := st.WithAttestSnapshot(context.Background(), func(*store.AttestSnapshot) error { return failing }); !errors.Is(err, failing) {
		t.Errorf("fn error: %v", err)
	}
}
