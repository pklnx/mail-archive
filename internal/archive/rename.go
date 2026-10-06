package archive

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/store"
)

// ValidateAccountName rejects names that would not work in URLs, command
// lines or logs.
func ValidateAccountName(name string) error {
	if name == "" || len(name) > 64 {
		return errors.New("name must be 1 to 64 characters")
	}
	if strings.TrimSpace(name) != name {
		return errors.New("name must not start or end with spaces")
	}
	for _, c := range name {
		if c == '/' || unicode.IsControl(c) {
			return errors.New("name must not contain slashes or control characters")
		}
	}
	return nil
}

// RenameAccount gives an account a new name. The stored password is
// encrypted with the account name as context, so it is decrypted and
// encrypted again for the new name. The account's sync lock is held while
// renaming: it fails with ErrSyncRunning if the account is being synced. On
// success, a carries the new name and password.
func RenameAccount(ctx context.Context, st *store.Store, sealer *crypto.Sealer, a *store.Account, newName string) error {
	if err := ValidateAccountName(newName); err != nil {
		return err
	}
	if a.RemovedAt != nil {
		return ErrAccountRemoved
	}
	unlock, ok, err := st.TryLockSync(ctx, a.ID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrSyncRunning
	}
	defer unlock()

	password, err := sealer.Open(a.PasswordEnc, PasswordContext(a.Name))
	if err != nil {
		return fmt.Errorf("decrypt password: %w", err)
	}
	enc, err := sealer.Seal(password, PasswordContext(newName))
	if err != nil {
		return err
	}
	// Never store a password that cannot be read back.
	if check, err := sealer.Open(enc, PasswordContext(newName)); err != nil || !bytes.Equal(check, password) {
		return errors.New("re-encrypting the password failed")
	}
	if err := st.RenameAccount(ctx, a.ID, newName, enc); err != nil {
		return err
	}
	a.Name, a.PasswordEnc = newName, enc
	return nil
}
