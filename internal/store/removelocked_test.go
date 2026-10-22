package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// TestRemoveAccountLockedOneConnection (#176): the sync lock, the delete,
// a new user agent and the audit entry all run on one connection, so a
// pool of one connection is enough.
func TestRemoveAccountLockedOneConnection(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn := connect(t, url)
	empty := createAccount(t, st, "empty")
	full := createAccount(t, st, "full")
	folder, err := st.GetOrCreateFolder(ctx, full.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	meta := store.MessageMeta{SHA256: strings.Repeat("a", 64), Size: 1, StoredPath: "p"}
	if _, err := st.SaveBatch(ctx, folder.ID, 1, []store.MessageMeta{meta}, []store.Location{{FolderID: folder.ID, UIDValidity: 1, UID: 1}}); err != nil {
		t.Fatal(err)
	}
	busy := createAccount(t, st, "busy")
	small := sessionStore(t, ctx, url)

	noLocks := func() {
		t.Helper()
		var n int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory'
			AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("advisory locks left: %d %v", n, err)
		}
		// The connection is back in the pool.
		if _, err := small.CountUsers(ctx); err != nil {
			t.Fatalf("next query: %v", err)
		}
	}
	for _, c := range []struct {
		acc  *store.Account
		want store.RemoveResult
	}{{empty, store.AccountDeleted}, {full, store.AccountRemoved}} {
		e := agentEntry(t, "account.remove")
		e.UserAgent += " " + c.acc.Name
		res, ok, err := small.RemoveAccountLocked(store.WithAudit(ctx, e), c.acc.Ref())
		if err != nil || !ok || res != c.want {
			t.Fatalf("%s: %v %v %v (ctx %v)", c.acc.Name, res, ok, err, ctx.Err())
		}
		var n int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM audit_log l JOIN audit_user_agents ua ON ua.id = l.user_agent_id
			WHERE l.action = $1 AND ua.user_agent = $2 AND l.details->>'result' = $3`, e.Action, e.UserAgent, string(c.want)).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s: audit entries %d %v", c.acc.Name, n, err)
		}
		noLocks()
	}

	// A stale reference changes nothing and releases the lock.
	stale := busy.Ref()
	stale.Version--
	if _, ok, err := small.RemoveAccountLocked(ctx, stale); !ok || !errors.Is(err, store.ErrStale) {
		t.Fatalf("stale: %v %v", ok, err)
	}
	noLocks()

	// Busy: another session holds the account's sync lock.
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1, $2)", 0x6d61, busy.ID); err != nil {
		t.Fatal(err)
	}
	if res, ok, err := small.RemoveAccountLocked(ctx, busy.Ref()); err != nil || ok || res != "" {
		t.Fatalf("busy: %q %v %v", res, ok, err)
	}
	got, err := st.GetAccount(ctx, busy.ID)
	if err != nil || got.RemovedAt != nil || got.Version != busy.Version {
		t.Fatalf("busy account changed: %+v %v", got, err)
	}
	if _, err := small.CountUsers(ctx); err != nil {
		t.Fatalf("next query: %v", err)
	}
}
