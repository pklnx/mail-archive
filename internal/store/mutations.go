package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pklnx/mail-archive/internal/store/db"
)

// Account mutations follow one pattern, so that concurrent changes from the
// web server, the CLI and other users stay consistent:
//
//   - The caller passes an AccountRef: the account ID together with the
//     owner and version it read.
//   - The mutation runs in one transaction that locks the account row,
//     checks that the account still exists, is not removed and has the
//     expected owner (ErrNotFound otherwise, as for an unknown account) and
//     the expected version (ErrStale otherwise), applies all changes and
//     increments the version.
//
// Only deleting needs the sync lock in addition (see DeleteOrRemoveAccount):
// a sync never writes the accounts row, so every other change can happen
// while a sync runs and takes effect from the next one.

// ErrStale is returned when an account changed after the caller read it.
var ErrStale = errors.New("the account was changed meanwhile; reload and try again")

// AccountRef names the state of an account a mutation is based on.
type AccountRef struct {
	ID      int64
	OwnerID *int64 // nil for an account without owner
	Version int64
}

// Ref returns the reference to a's current state.
func (a *Account) Ref() AccountRef {
	return AccountRef{ID: a.ID, OwnerID: a.OwnerID, Version: a.Version}
}

// Connection is how to reach an account's server, with the password
// already encrypted for the account.
type Connection struct {
	Host        string
	Port        int
	TLSMode     TLSMode
	Username    string
	PasswordEnc []byte
}

// FolderFilters select the folders to archive.
type FolderFilters struct {
	Included []string // empty means all folders
	Excluded []string
}

// AccountChange lists the fields to change; nil fields keep their value.
type AccountChange struct {
	Name        *string
	Connection  *Connection
	PasswordEnc []byte // a new encrypted password; ignored with Connection
	Folders     *FolderFilters
	Enabled     *bool
}

// lockAccount locks the account row and checks it against ref.
func lockAccount(ctx context.Context, q *db.Queries, ref AccountRef) (db.Account, error) {
	r, err := q.LockAccount(ctx, ref.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if r.RemovedAt != nil || !sameOwner(r.OwnerID, ref.OwnerID) {
		return r, ErrNotFound
	}
	if r.Version != ref.Version {
		return r, ErrStale
	}
	return r, nil
}

func sameOwner(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// UpdateAccount applies all changes at once or none of them. It fails with
// ErrNotFound if the account is gone, removed or has another owner than in
// ref, with ErrStale if it changed after ref was read, and with ErrConflict
// if the owner already has an account with the new name.
func (s *Store) UpdateAccount(ctx context.Context, ref AccountRef, c AccountChange) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		r, err := lockAccount(ctx, q, ref)
		if err != nil {
			return err
		}
		p := db.WriteAccountParams{
			ID: r.ID, Name: r.Name, Host: r.Host, Port: r.Port, TlsMode: r.TlsMode, Username: r.Username,
			PasswordEnc: r.PasswordEnc, IncludedFolders: r.IncludedFolders, ExcludedFolders: r.ExcludedFolders,
			Enabled: r.Enabled,
		}
		if c.Name != nil {
			p.Name = *c.Name
		}
		if c.Connection != nil {
			if c.Connection.Port < 1 || c.Connection.Port > 65535 {
				return fmt.Errorf("invalid port %d", c.Connection.Port)
			}
			p.Host, p.TlsMode, p.Username = c.Connection.Host, string(c.Connection.TLSMode), c.Connection.Username
			p.Port = int32(c.Connection.Port) //nolint:gosec // range checked above
			p.PasswordEnc = c.Connection.PasswordEnc
		} else if c.PasswordEnc != nil {
			p.PasswordEnc = c.PasswordEnc
		}
		if c.Folders != nil {
			p.IncludedFolders, p.ExcludedFolders = nonNil(c.Folders.Included), nonNil(c.Folders.Excluded)
		}
		if c.Enabled != nil {
			p.Enabled = *c.Enabled
		}
		err = q.WriteAccount(ctx, p)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return fmt.Errorf("account %q: %w", p.Name, ErrConflict)
		}
		return err
	})
}

func nonNil(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

// SetAccountOwner hands an account to another user, checked against ref
// like UpdateAccount. It fails with ErrConflict if that user already has an
// account with the same name. Removed accounts can be moved too, so their
// archived mail moves along.
func (s *Store) SetAccountOwner(ctx context.Context, ref AccountRef, owner int64) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		r, err := q.LockAccount(ctx, ref.ID)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && !sameOwner(r.OwnerID, ref.OwnerID) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if r.Version != ref.Version {
			return ErrStale
		}
		err = one(q.SetAccountOwner(ctx, db.SetAccountOwnerParams{ID: ref.ID, OwnerID: &owner}))
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return fmt.Errorf("account name: %w", ErrConflict)
		}
		return err
	})
}

// RemoveResult tells how DeleteOrRemoveAccount handled an account.
type RemoveResult string

// Possible outcomes of DeleteOrRemoveAccount.
const (
	AccountDeleted RemoveResult = "deleted" // no archived mail: the account is gone
	AccountRemoved RemoveResult = "removed" // archived mail kept, credentials wiped
)

// DeleteOrRemoveAccount deletes an account without archived mail. An account
// with archived mail is marked as removed instead: its mail stays searchable
// and keeps showing where it came from, but the password is wiped and the
// account is never synced again. It is checked against ref like
// UpdateAccount. The caller must hold the account's sync lock (TryLockSync),
// so that no sync writes folders or messages for it meanwhile.
func (s *Store) DeleteOrRemoveAccount(ctx context.Context, ref AccountRef) (RemoveResult, error) {
	var result RemoveResult
	err := s.inTx(ctx, func(q *db.Queries) error {
		if _, err := lockAccount(ctx, q, ref); err != nil {
			return err
		}
		n, err := q.CountAccountLocations(ctx, ref.ID)
		if err != nil {
			return err
		}
		if n > 0 {
			result = AccountRemoved
			return one(q.RemoveAccount(ctx, ref.ID))
		}
		result = AccountDeleted
		if err := q.DeleteAccountFolders(ctx, ref.ID); err != nil {
			return err
		}
		return one(q.DeleteAccount(ctx, ref.ID))
	})
	return result, err
}

// ReplacePassword stores a new encryption of the same password, but only if
// the stored value is still old. It reports whether it replaced it.
func (s *Store) ReplacePassword(ctx context.Context, id int64, old, enc []byte) (bool, error) {
	n, err := s.q.ReplacePassword(ctx, db.ReplacePasswordParams{ID: id, OldEnc: old, NewEnc: enc})
	return n == 1, err
}
