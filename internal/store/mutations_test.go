package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

func TestConcurrentUpdatesOneWins(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	a := createAccount(t, st, "a")
	for round := range 20 {
		ref := a.Ref()
		const writers = 8
		errs := make([]error, writers)
		var wg sync.WaitGroup
		for i := range writers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				name := fmt.Sprintf("r%d-w%d", round, i)
				enabled := i%2 == 0
				errs[i] = st.UpdateAccount(ctx, ref, store.AccountChange{Name: &name, Enabled: &enabled})
			}()
		}
		wg.Wait()
		winner := -1
		for i, err := range errs {
			switch {
			case err == nil:
				if winner >= 0 {
					t.Fatalf("round %d: writers %d and %d both won", round, winner, i)
				}
				winner = i
			case !errors.Is(err, store.ErrStale):
				t.Fatalf("round %d: writer %d: %v", round, i, err)
			}
		}
		if winner < 0 {
			t.Fatalf("round %d: nobody won", round)
		}
		got, err := st.GetAccount(ctx, a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Name != fmt.Sprintf("r%d-w%d", round, winner) || got.Enabled != (winner%2 == 0) || got.Version != ref.Version+1 {
			t.Fatalf("round %d: state %+v does not match winner %d", round, got, winner)
		}
		a = got
	}
}

func TestFailedUpdateChangesNothing(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	a := createAccount(t, st, "a")
	createAccount(t, st, "taken")
	name, off := "taken", false
	change := store.AccountChange{
		Name:    &name,
		Enabled: &off,
		Folders: &store.FolderFilters{Excluded: []string{"Spam"}},
		Connection: &store.Connection{
			Host: "new.example", Port: 143, TLSMode: store.TLSModeSTARTTLS, Username: "other", PasswordEnc: []byte{7},
		},
	}
	if err := st.UpdateAccount(ctx, a.Ref(), change); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	got, _ := st.GetAccount(ctx, a.ID)
	if got.Name != "a" || !got.Enabled || len(got.ExcludedFolders) != 0 || got.Host != "h" || got.Version != a.Version {
		t.Fatalf("partly applied: %+v", got)
	}

	// An invalid port is refused before anything is written.
	change.Name, change.Connection.Port = nil, 0
	if err := st.UpdateAccount(ctx, a.Ref(), change); err == nil {
		t.Fatal("port 0 accepted")
	}
	if got, _ := st.GetAccount(ctx, a.ID); got.Version != a.Version || !got.Enabled {
		t.Fatalf("partly applied: %+v", got)
	}

	// The same change without the conflict is applied completely.
	if err := st.UpdateAccount(ctx, a.Ref(), store.AccountChange{Enabled: &off, Folders: change.Folders}); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetAccount(ctx, a.ID)
	if got.Enabled || len(got.ExcludedFolders) != 1 || got.Version != a.Version+1 {
		t.Fatalf("not applied: %+v", got)
	}
	// The old reference is stale now, also for deleting.
	if err := st.UpdateAccount(ctx, a.Ref(), store.AccountChange{Enabled: &off}); !errors.Is(err, store.ErrStale) {
		t.Fatalf("stale update: %v", err)
	}
	if _, err := st.DeleteOrRemoveAccount(ctx, a.Ref()); !errors.Is(err, store.ErrStale) {
		t.Fatalf("stale delete: %v", err)
	}
}

func TestMoveAndMutationsRace(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice, _ := st.CreateUser(ctx, "alice", "h", true)
	bob, _ := st.CreateUser(ctx, "bob", "h", false)

	for round := range 20 {
		a := &store.Account{Name: fmt.Sprintf("acc%d", round), Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}, Enabled: true, OwnerID: &alice.ID}
		if err := st.CreateAccount(ctx, a); err != nil {
			t.Fatal(err)
		}
		ref := a.Ref()
		var moveErr, updateErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); moveErr = st.SetAccountOwner(ctx, ref, bob.ID) }()
		go func() {
			defer wg.Done()
			off := false
			updateErr = st.UpdateAccount(ctx, ref, store.AccountChange{Enabled: &off})
		}()
		wg.Wait()
		got, _ := st.GetAccount(ctx, a.ID)
		switch {
		// The owner is checked before the version: after the move, the old
		// owner's update finds no account at all.
		case moveErr == nil && errors.Is(updateErr, store.ErrNotFound):
			if *got.OwnerID != bob.ID || !got.Enabled {
				t.Fatalf("round %d: move won, state %+v", round, got)
			}
		case updateErr == nil && errors.Is(moveErr, store.ErrStale):
			if *got.OwnerID != alice.ID || got.Enabled {
				t.Fatalf("round %d: update won, state %+v", round, got)
			}
		default:
			t.Fatalf("round %d: move %v, update %v", round, moveErr, updateErr)
		}
	}

	// After a move, the old owner's reference finds nothing, even with the
	// current version.
	a, _ := accountByName(st, "acc0")
	if *a.OwnerID != bob.ID {
		if err := st.SetAccountOwner(ctx, a.Ref(), bob.ID); err != nil {
			t.Fatal(err)
		}
		a, _ = st.GetAccount(ctx, a.ID)
	}
	asAlice := a.Ref()
	asAlice.OwnerID = &alice.ID
	off := false
	if err := st.UpdateAccount(ctx, asAlice, store.AccountChange{Enabled: &off}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("update by the old owner: %v", err)
	}
	if _, err := st.DeleteOrRemoveAccount(ctx, asAlice); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete by the old owner: %v", err)
	}
	if err := st.SetAccountOwner(ctx, asAlice, alice.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("move back by the old owner: %v", err)
	}
}

func TestReplacePasswordCompareAndSwap(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	a := createAccount(t, st, "a") // password {1}
	if ok, err := st.ReplacePassword(ctx, a.ID, []byte{9}, []byte{2}); err != nil || ok {
		t.Fatalf("replace with a wrong old value: %v, %v", ok, err)
	}
	if ok, err := st.ReplacePassword(ctx, a.ID, []byte{1}, []byte{2}); err != nil || !ok {
		t.Fatalf("replace: %v, %v", ok, err)
	}
	got, _ := st.GetAccount(ctx, a.ID)
	if string(got.PasswordEnc) != "\x02" || got.Version != a.Version {
		t.Fatalf("after replace: %+v", got)
	}
}
