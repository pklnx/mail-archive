package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// accountByName returns the only account with this name, of any owner.
func accountByName(st *store.Store, name string) (*store.Account, error) {
	list, err := st.ListAccountsByName(context.Background(), name)
	if err != nil {
		return nil, err
	}
	if len(list) != 1 {
		return nil, store.ErrNotFound
	}
	return list[0], nil
}

func createAccount(t *testing.T, st *store.Store, name string) *store.Account {
	t.Helper()
	a := &store.Account{Name: name, Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, Enabled: true}
	if err := st.CreateAccount(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestDeleteOrRemoveAccount(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()

	// Without archived mail the account is deleted completely.
	empty := createAccount(t, st, "empty")
	if _, err := st.GetOrCreateFolder(ctx, empty.ID, "INBOX"); err != nil {
		t.Fatal(err)
	}
	if res, err := st.DeleteOrRemoveAccount(ctx, empty.Ref()); err != nil || res != store.AccountDeleted {
		t.Fatalf("delete empty: %v, %v", res, err)
	}
	if _, err := accountByName(st, "empty"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("empty account still there: %v", err)
	}

	// With archived mail it is only marked as removed.
	full := createAccount(t, st, "full")
	folder, err := st.GetOrCreateFolder(ctx, full.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	meta := store.MessageMeta{SHA256: strings.Repeat("a", 64), Size: 1, StoredPath: "p"}
	if _, err := st.SaveBatch(ctx, folder.ID, 1, []store.MessageMeta{meta}, []store.Location{{FolderID: folder.ID, UIDValidity: 1, UID: 1}}); err != nil {
		t.Fatal(err)
	}
	if res, err := st.DeleteOrRemoveAccount(ctx, full.Ref()); err != nil || res != store.AccountRemoved {
		t.Fatalf("remove full: %v, %v", res, err)
	}
	got, err := accountByName(st, "full")
	if err != nil {
		t.Fatal(err)
	}
	if got.RemovedAt == nil || got.Enabled || len(got.PasswordEnc) != 0 {
		t.Fatalf("removed account: %+v", got)
	}
	// Removed accounts cannot be changed or removed again.
	on := true
	if err := st.UpdateAccount(ctx, got.Ref(), store.AccountChange{Enabled: &on}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("enable removed: %v", err)
	}
	if err := st.UpdateAccount(ctx, got.Ref(), store.AccountChange{PasswordEnc: []byte{2}}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("set password on removed: %v", err)
	}
	if _, err := st.DeleteOrRemoveAccount(ctx, got.Ref()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("remove twice: %v", err)
	}
	// Its mail is still listed under the account, for its owner: the first
	// user gets the accounts created before any user existed.
	owner, err := st.CreateUser(ctx, "owner", "h", true)
	if err != nil {
		t.Fatal(err)
	}
	list, err := st.ListAccountFolders(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || !list[0].Removed || len(list[0].Folders) != 1 || list[0].Folders[0].Messages != 1 {
		t.Fatalf("account folders: %+v", list)
	}
}

func TestSyncLock(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	a := createAccount(t, st, "a")
	b := createAccount(t, st, "b")

	unlock, ok, err := st.TryLockSync(ctx, a.ID)
	if err != nil || !ok {
		t.Fatalf("first lock: %v, %v", ok, err)
	}
	if _, ok, err := st.TryLockSync(ctx, a.ID); err != nil || ok {
		t.Fatalf("second lock on the same account: %v, %v", ok, err)
	}
	unlockB, ok, err := st.TryLockSync(ctx, b.ID)
	if err != nil || !ok {
		t.Fatalf("lock on another account: %v, %v", ok, err)
	}
	syncing, err := st.SyncingAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !syncing[a.ID] || !syncing[b.ID] || len(syncing) != 2 {
		t.Fatalf("syncing = %v", syncing)
	}
	unlock()
	unlockB()
	if syncing, _ := st.SyncingAccounts(ctx); len(syncing) != 0 {
		t.Fatalf("after unlock: %v", syncing)
	}
	unlock, ok, err = st.TryLockSync(ctx, a.ID)
	if err != nil || !ok {
		t.Fatalf("lock after unlock: %v, %v", ok, err)
	}
	unlock()
}

func TestSyncRunProgressAndStaleRuns(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()
	a := createAccount(t, st, "a")

	stale, err := st.StartSyncRun(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	stale.MessagesFetched, stale.MessagesNew = 5, 3
	if err := st.UpdateSyncRunProgress(ctx, stale); err != nil {
		t.Fatal(err)
	}
	runs, err := st.LastRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r := runs[a.ID]; r.Status != "running" || r.FinishedAt != nil || r.MessagesFetched != 5 || r.MessagesNew != 3 {
		t.Fatalf("running run: %+v", r)
	}

	// A new run closes the one left open by a crashed process.
	if _, err := st.StartSyncRun(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var status, msg string
	if err := conn.QueryRow(ctx, "SELECT status, error FROM sync_runs WHERE id = $1", stale.ID).Scan(&status, &msg); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || msg != "interrupted" {
		t.Fatalf("stale run: %s %q", status, msg)
	}
}

func TestRenameAccount(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	a := createAccount(t, st, "old")
	createAccount(t, st, "taken")
	gone := createAccount(t, st, "gone")
	folder, err := st.GetOrCreateFolder(ctx, gone.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	meta := store.MessageMeta{SHA256: strings.Repeat("b", 64), Size: 1, StoredPath: "p"}
	if _, err := st.SaveBatch(ctx, folder.ID, 1, []store.MessageMeta{meta}, []store.Location{{FolderID: folder.ID, UIDValidity: 1, UID: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeleteOrRemoveAccount(ctx, gone.Ref()); err != nil {
		t.Fatal(err)
	}
	rename := func(a *store.Account, name string) error {
		return st.UpdateAccount(ctx, a.Ref(), store.AccountChange{Name: &name})
	}

	if err := rename(a, "new"); err != nil {
		t.Fatal(err)
	}
	got, err := accountByName(st, "new")
	if err != nil || got.ID != a.ID || got.Version != a.Version+1 {
		t.Fatalf("renamed account: %+v, %v", got, err)
	}
	for _, name := range []string{"taken", "gone"} {
		if err := rename(got, name); !errors.Is(err, store.ErrConflict) {
			t.Errorf("rename to %q: %v, want ErrConflict", name, err)
		}
	}
	if err := rename(gone, "revived"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("rename removed account: %v, want ErrNotFound", err)
	}
}
