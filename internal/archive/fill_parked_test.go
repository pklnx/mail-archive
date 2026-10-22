package archive_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/store"
)

// fillOutcome names the error a fill run ended with.
func fillOutcome(err error) string {
	for _, e := range []error{store.ErrFillNotRunning, archive.ErrFillSkipped, archive.ErrFillBusy, store.ErrNotFound} {
		if errors.Is(err, e) {
			return e.Error()
		}
	}
	if err != nil {
		return "other: " + err.Error()
	}
	return "ok"
}

// TestFillChangedWhileParked (#178): while another process holds the blob
// lock, Filler.Run waits for it without the account's lock. A recurring
// fill stopped, or its account removed, meanwhile is handled as by a Run
// that starts after the change: Run reads the fill again under the lock.
func TestFillChangedWhileParked(t *testing.T) {
	for _, change := range []string{"stopped", "account removed"} {
		t.Run(change, func(t *testing.T) {
			c := &clock{now: time.Now()}
			f := newFixture(t, recurringUser(t, c.Now()))
			acc := f.smtpAccount()
			fill := f.createRecurringFill(acc, "secret")
			filler := f.recurringFiller(c)
			runFill(t, filler, fill.ID)
			c.Add(3 * time.Hour)
			other := &store.Account{Name: "other", Enabled: true}
			if err := f.store.CreateSMTPAccount(f.ctx, other, []string{"other@archive.local"}); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(f.ctx, 30*time.Second)
			defer cancel()
			observer, err := pgx.Connect(ctx, f.dbURL)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = observer.Close(context.Background()) })
			waiting := func() int {
				var n int
				if err := observer.QueryRow(ctx, `SELECT count(*) FROM pg_locks
					WHERE locktype = 'advisory' AND classid = $1 AND objid = 0 AND NOT granted
					  AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`, 0x6d62).Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n + f.store.BlobWaiters()
			}
			// Stands in for a CLI verify.
			if _, err := observer.Exec(ctx, "SELECT pg_advisory_lock($1, 0)", 0x6d62); err != nil {
				t.Fatal(err)
			}
			// A delivery takes the wait slot, so that Run waits without a
			// connection.
			delivered := make(chan error, 1)
			go func() {
				h := strings.Repeat("ab", 32)
				meta := store.MessageMeta{SHA256: h, Size: 10, StoredPath: h[:2] + "/" + h}
				env := store.Envelope{MailFrom: "a@example.com", RcptTo: []string{"other@archive.local"}, ReceivedAt: time.Now(), HELO: "mx"}
				_, err := f.store.ReceiveMessage(ctx, other.ID, meta, env, func() bool { return true })
				delivered <- err
			}()
			for waiting() < 1 {
				if ctx.Err() != nil {
					t.Fatal("the delivery does not wait")
				}
			}
			type result struct {
				fill *store.Fill
				err  error
			}
			parked := make(chan result, 1)
			go func() {
				got, err := filler.Run(ctx, fill.ID)
				parked <- result{got, err}
			}()
			for waiting() < 2 {
				if ctx.Err() != nil {
					t.Fatal("Run does not wait")
				}
			}

			switch change {
			case "stopped":
				if err := f.store.CancelFill(ctx, fill.ID); err != nil {
					t.Fatal(err)
				}
			case "account removed":
				unlock, ok, err := f.store.TryLockSync(ctx, acc.ID)
				if err != nil || !ok {
					t.Fatalf("the waiting Run holds the account's lock: %v %v", ok, err)
				}
				res, err := f.store.DeleteOrRemoveAccount(ctx, acc.Ref())
				unlock()
				if err != nil || res != store.AccountRemoved {
					t.Fatalf("remove: %v %v", res, err)
				}
			}

			if _, err := observer.Exec(ctx, "SELECT pg_advisory_unlock($1, 0)", 0x6d62); err != nil {
				t.Fatal(err)
			}
			if err := <-delivered; err != nil {
				t.Fatalf("delivery: %v", err)
			}
			got := <-parked
			after, afterErr := filler.Run(ctx, fill.ID)
			if fillOutcome(got.err) != fillOutcome(afterErr) {
				t.Errorf("waiting Run: %v, a Run after the change: %v", got.err, afterErr)
			}
			if got.fill == nil || after == nil {
				t.Fatalf("waiting Run returned fill %v, a Run after the change %v", got.fill != nil, after != nil)
			}
			if got.fill.Status != after.Status || got.fill.KeepRunning != after.KeepRunning {
				t.Errorf("waiting Run returned a fill %s (keep running %v), a Run after the change %s (%v)",
					got.fill.Status, got.fill.KeepRunning, after.Status, after.KeepRunning)
			}
			if after.Status == store.FillRunning {
				t.Error("the fill runs again")
			}
		})
	}
}
