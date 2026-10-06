package archive

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/store"
)

// Stored IMAP passwords are encrypted with AES-GCM and the account ID as
// associated data. The ID never changes and is unique, so a ciphertext
// cannot be moved to another account row, and renaming or handing over an
// account needs no new encryption.
func passwordContext(accountID int64) []byte {
	return []byte("account-password:id:" + strconv.FormatInt(accountID, 10))
}

// legacyPasswordContext is how passwords were bound before: to the account
// name, which is no longer unique across users. Still read until
// UpgradePasswords has converted every account.
func legacyPasswordContext(accountName string) []byte {
	return []byte("account-password:" + accountName)
}

// SealPassword encrypts an account's IMAP password.
func SealPassword(sealer *crypto.Sealer, accountID int64, password string) ([]byte, error) {
	enc, err := sealer.Seal([]byte(password), passwordContext(accountID))
	if err != nil {
		return nil, err
	}
	// Never store a password that cannot be read back.
	if check, err := sealer.Open(enc, passwordContext(accountID)); err != nil || !bytes.Equal(check, []byte(password)) {
		return nil, errors.New("encrypting the password failed")
	}
	return enc, nil
}

// OpenPassword decrypts an account's IMAP password.
func OpenPassword(sealer *crypto.Sealer, a *store.Account) (string, error) {
	if len(a.PasswordEnc) == 0 {
		return "", errors.New("the account has no stored password")
	}
	pw, err := sealer.Open(a.PasswordEnc, passwordContext(a.ID))
	if err != nil {
		var legacyErr error
		if pw, legacyErr = sealer.Open(a.PasswordEnc, legacyPasswordContext(a.Name)); legacyErr != nil {
			return "", fmt.Errorf("decrypt password: %w", err)
		}
	}
	return string(pw), nil
}

// UpgradePasswords encrypts passwords still bound to the account name again
// with the account ID and returns how many it converted. It runs on
// `migrate` and when the web server starts.
func UpgradePasswords(ctx context.Context, st *store.Store, sealer *crypto.Sealer) (int, error) {
	accounts, err := st.ListAccounts(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, a := range accounts {
		if len(a.PasswordEnc) == 0 {
			continue // removed account
		}
		if _, err := sealer.Open(a.PasswordEnc, passwordContext(a.ID)); err == nil {
			continue
		}
		pw, err := sealer.Open(a.PasswordEnc, legacyPasswordContext(a.Name))
		if err != nil {
			return n, fmt.Errorf("account %q: cannot decrypt the stored password (wrong %s?)", a.Name, "MAIL_ARCHIVE_SECRET_KEY")
		}
		enc, err := SealPassword(sealer, a.ID, string(pw))
		if err != nil {
			return n, err
		}
		if err := st.UpdatePassword(ctx, a.ID, enc); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
