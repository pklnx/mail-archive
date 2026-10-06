package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

func TestUsers(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()

	if n, err := st.CountUsers(ctx); err != nil || n != 0 {
		t.Fatalf("count: %d, %v", n, err)
	}
	admin, err := st.CreateUser(ctx, "admin", "h1", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(ctx, "admin", "h2", false); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	// The database only takes normalized names.
	if _, err := st.CreateUser(ctx, "Admin", "h2", false); err == nil {
		t.Fatal("uppercase name stored")
	}
	alice, err := st.CreateUser(ctx, "alice", "h3", false)
	if err != nil {
		t.Fatal(err)
	}

	// The last unlocked admin can be neither locked nor removed.
	if err := st.SetUserLocked(ctx, admin.ID, true); !errors.Is(err, store.ErrLastAdmin) {
		t.Fatalf("lock last admin: %v", err)
	}
	if err := st.DeleteUser(ctx, admin.ID); !errors.Is(err, store.ErrLastAdmin) {
		t.Fatalf("delete last admin: %v", err)
	}
	second, _ := st.CreateUser(ctx, "second", "h4", true)
	if err := st.SetUserLocked(ctx, admin.ID, true); err != nil {
		t.Fatalf("lock with a second admin: %v", err)
	}
	if err := st.DeleteUser(ctx, second.ID); !errors.Is(err, store.ErrLastAdmin) {
		t.Fatalf("delete the only unlocked admin: %v", err)
	}
	if err := st.SetUserLocked(ctx, admin.ID, false); err != nil {
		t.Fatal(err)
	}

	// Removing a user removes their sessions.
	sess := make([]byte, 32)
	sess[0] = 1
	if err := st.CreateSession(ctx, sess, alice.ID, time.Now().Add(time.Hour), "test"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteUser(ctx, alice.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetSession(ctx, sess, time.Now().Add(-time.Hour)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("session of removed user: %v", err)
	}
	if err := st.DeleteUser(ctx, alice.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}

	users, err := st.ListUsers(ctx)
	if err != nil || len(users) != 2 || users[0].Name != "admin" || users[1].Name != "second" {
		t.Fatalf("list: %v, %v", users, err)
	}
}
