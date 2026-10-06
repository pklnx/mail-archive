package archive

import (
	"context"
	"testing"

	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

func testSealer(t *testing.T) *crypto.Sealer {
	t.Helper()
	keyStr, _ := crypto.GenerateKey()
	key, _ := crypto.ParseKey(keyStr)
	s, err := crypto.NewSealer(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPasswordBoundToAccountID(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	sealer := testSealer(t)
	alice, _ := st.CreateUser(ctx, "alice", "h", true)
	bob, _ := st.CreateUser(ctx, "bob", "h", false)

	// Two users, same account name, different passwords.
	add := func(owner int64, pw string) *store.Account {
		a := &store.Account{Name: "personal", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", OwnerID: &owner}
		if err := st.CreateAccountSealed(ctx, a, func(id int64) ([]byte, error) { return SealPassword(sealer, id, pw) }); err != nil {
			t.Fatal(err)
		}
		return a
	}
	a, b := add(alice.ID, "alice-secret"), add(bob.ID, "bob-secret")
	if pw, err := OpenPassword(sealer, a); err != nil || pw != "alice-secret" {
		t.Fatalf("open: %q, %v", pw, err)
	}

	// A ciphertext copied to the other row does not decrypt there.
	swapped := *b
	swapped.PasswordEnc = a.PasswordEnc
	if _, err := OpenPassword(sealer, &swapped); err == nil {
		t.Fatal("password of another account decrypted")
	}

	// Renaming needs no new encryption.
	if err := st.RenameAccount(ctx, a.ID, "renamed"); err != nil {
		t.Fatal(err)
	}
	a.Name = "renamed"
	if pw, err := OpenPassword(sealer, a); err != nil || pw != "alice-secret" {
		t.Fatalf("after rename: %q, %v", pw, err)
	}
}

func TestUpgradePasswords(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	sealer := testSealer(t)

	// An account from before: password bound to the name.
	old, _ := sealer.Seal([]byte("old-secret"), legacyPasswordContext("legacy"))
	legacy := &store.Account{Name: "legacy", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: old}
	if err := st.CreateAccount(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	current := &store.Account{Name: "current", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u"}
	if err := st.CreateAccountSealed(ctx, current, func(id int64) ([]byte, error) { return SealPassword(sealer, id, "new-secret") }); err != nil {
		t.Fatal(err)
	}
	removed := &store.Account{Name: "removed", Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u", PasswordEnc: []byte{1}}
	if err := st.CreateAccount(ctx, removed); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeleteOrRemoveAccount(ctx, removed.ID); err != nil {
		t.Fatal(err)
	}

	// Still readable before the upgrade.
	if pw, err := OpenPassword(sealer, legacy); err != nil || pw != "old-secret" {
		t.Fatalf("legacy before upgrade: %q, %v", pw, err)
	}
	if n, err := UpgradePasswords(ctx, st, sealer); err != nil || n != 1 {
		t.Fatalf("upgrade: %d, %v", n, err)
	}
	got, _ := st.GetAccount(ctx, legacy.ID)
	if _, err := sealer.Open(got.PasswordEnc, passwordContext(legacy.ID)); err != nil {
		t.Fatalf("not bound to the ID after upgrade: %v", err)
	}
	if pw, err := OpenPassword(sealer, got); err != nil || pw != "old-secret" {
		t.Fatalf("after upgrade: %q, %v", pw, err)
	}
	if n, err := UpgradePasswords(ctx, st, sealer); err != nil || n != 0 {
		t.Fatalf("second upgrade: %d, %v", n, err)
	}

	// With the wrong key, the upgrade stops instead of overwriting anything.
	if _, err := UpgradePasswords(ctx, st, testSealer(t)); err == nil {
		t.Fatal("upgrade with a wrong key succeeded")
	}
}
