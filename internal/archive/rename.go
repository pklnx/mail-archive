package archive

import (
	"context"
	"errors"
	"strings"
	"unicode"

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

// RenameAccount gives an account a new name. The account's sync lock is
// held while renaming: it fails with ErrSyncRunning if the account is being
// synced. On success, a carries the new name.
func RenameAccount(ctx context.Context, st *store.Store, a *store.Account, newName string) error {
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
	if err := st.RenameAccount(ctx, a.ID, newName); err != nil {
		return err
	}
	a.Name = newName
	return nil
}
