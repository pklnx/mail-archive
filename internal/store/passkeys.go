package store

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/store/db"
)

// Passkeys (WebAuthn). The store keeps the library's credential records and
// session data as opaque JSON; the web package checks them. Every change to
// a user's passkeys locks the user row first, and a login locks the passkey
// row, so concurrent registrations and logins see each other's results.

// MaxPasskeys is the number of passkeys a user can have.
const MaxPasskeys = 10

// MaxOpenLoginCeremonies bounds the logins in progress: starting one needs
// no session, so this keeps anybody from filling the table.
const MaxOpenLoginCeremonies = 1000

// Passkey errors.
var (
	ErrPasskeyLimit      = errors.New("this user has the maximum number of passkeys")
	ErrPasskeyName       = errors.New("a passkey with this name exists")
	ErrPasskeyRegistered = errors.New("this passkey is already registered")
	ErrPasskeyUnknown    = errors.New("unknown passkey")
	ErrPasskeyInvalid    = errors.New("passkey not accepted")
	ErrCeremonyExpired   = errors.New("passkey ceremony expired or invalid")
	ErrTooManyCeremonies = errors.New("too many passkey logins in progress")
	ErrUserLocked        = errors.New("this user is locked")
)

// Ceremony kinds.
const (
	ceremonyRegister = "register"
	ceremonyLogin    = "login"
)

// Passkey is a registered passkey, without its key.
type Passkey struct {
	ID           int64
	Name         string
	CredentialID []byte
	CreatedAt    time.Time
	LastUsedAt   *time.Time
}

// ListPasskeys returns a user's passkeys, oldest first.
func (s *Store) ListPasskeys(ctx context.Context, userID int64) ([]Passkey, error) {
	rows, err := s.q.ListPasskeys(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]Passkey, 0, len(rows))
	for _, r := range rows {
		out = append(out, Passkey{ID: r.ID, Name: r.Name, CredentialID: r.CredentialID, CreatedAt: r.CreatedAt, LastUsedAt: r.LastUsedAt})
	}
	return out, nil
}

// PasskeyCounts returns the number of passkeys per user ID.
func (s *Store) PasskeyCounts(ctx context.Context) (map[int64]int64, error) {
	rows, err := s.q.CountPasskeysByUser(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]int64, len(rows))
	for _, r := range rows {
		out[r.UserID] = r.N
	}
	return out, nil
}

// BeginPasskeyRegistration starts adding a passkey called name. begin gets
// the user's WebAuthn handle (created on the first registration) and the
// credential records of the existing passkeys, and returns the session data
// to keep. The result is the token for the client.
func (s *Store) BeginPasskeyRegistration(ctx context.Context, userID int64, name string, expires time.Time,
	begin func(handle []byte, credentials [][]byte) ([]byte, error)) (string, error) {
	token, hash, err := auth.NewSessionToken()
	if err != nil {
		return "", err
	}
	err = s.inTx(ctx, func(q *db.Queries) error {
		u, err := lockUser(ctx, q, userID)
		if err != nil {
			return err
		}
		handle := u.WebauthnHandle
		if handle == nil {
			handle = make([]byte, 32)
			if _, err := rand.Read(handle); err != nil {
				return err
			}
			if err := q.SetWebAuthnHandle(ctx, db.SetWebAuthnHandleParams{ID: userID, WebauthnHandle: handle}); err != nil {
				return err
			}
		}
		existing, err := q.ListPasskeys(ctx, userID)
		if err != nil {
			return err
		}
		if len(existing) >= MaxPasskeys {
			return ErrPasskeyLimit
		}
		creds := make([][]byte, 0, len(existing))
		for _, p := range existing {
			if p.Name == name {
				return ErrPasskeyName
			}
			creds = append(creds, p.Credential)
		}
		data, err := begin(handle, creds)
		if err != nil {
			return err
		}
		return q.CreateWebAuthnCeremony(ctx, db.CreateWebAuthnCeremonyParams{
			ID: hash, Kind: ceremonyRegister, UserID: &userID, Name: &name, Data: data, ExpiresAt: expires,
		})
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// takeCeremony uses up a ceremony in its own statement: even when the check
// that follows fails and its transaction rolls back, the ceremony is gone.
func (s *Store) takeCeremony(ctx context.Context, token, kind string) (db.WebauthnCeremony, error) {
	c, err := s.q.TakeWebAuthnCeremony(ctx, db.TakeWebAuthnCeremonyParams{ID: auth.HashSessionToken(token), Kind: kind})
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrCeremonyExpired
	}
	return c, err
}

// FinishPasskeyRegistration completes a registration of userID. finish gets
// the handle and the session data and returns the new credential's ID and
// record. The limit is checked again under the user's lock, so concurrent
// registrations cannot exceed it. It returns the passkey's name.
func (s *Store) FinishPasskeyRegistration(ctx context.Context, userID int64, token string,
	finish func(handle, data []byte) (credentialID, credential []byte, err error)) (string, error) {
	c, err := s.takeCeremony(ctx, token, ceremonyRegister)
	if err != nil {
		return "", err
	}
	if c.UserID == nil || *c.UserID != userID || c.Name == nil {
		return "", ErrCeremonyExpired
	}
	err = s.inTx(ctx, func(q *db.Queries) error {
		u, err := lockUser(ctx, q, userID)
		if err != nil {
			return err
		}
		if u.LockedAt != nil || u.WebauthnHandle == nil {
			return ErrCeremonyExpired
		}
		n, err := q.CountPasskeys(ctx, userID)
		if err != nil {
			return err
		}
		if n >= MaxPasskeys {
			return ErrPasskeyLimit
		}
		id, cred, err := finish(u.WebauthnHandle, c.Data)
		if err != nil {
			return errors.Join(ErrPasskeyInvalid, err)
		}
		err = q.InsertPasskey(ctx, db.InsertPasskeyParams{UserID: userID, CredentialID: id, Name: *c.Name, Credential: cred})
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			if pgErr.ConstraintName == "passkeys_credential_id_key" {
				return ErrPasskeyRegistered
			}
			return ErrPasskeyName
		}
		return err
	})
	if err != nil {
		return "", err
	}
	return *c.Name, nil
}

// BeginPasskeyLogin stores the session data of a login without a user name
// and returns the token for the client.
func (s *Store) BeginPasskeyLogin(ctx context.Context, data []byte, expires time.Time) (string, error) {
	n, err := s.q.CountOpenLoginCeremonies(ctx)
	if err != nil {
		return "", err
	}
	if n >= MaxOpenLoginCeremonies {
		return "", ErrTooManyCeremonies
	}
	token, hash, err := auth.NewSessionToken()
	if err != nil {
		return "", err
	}
	err = s.q.CreateWebAuthnCeremony(ctx, db.CreateWebAuthnCeremonyParams{ID: hash, Kind: ceremonyLogin, Data: data, ExpiresAt: expires})
	if err != nil {
		return "", err
	}
	return token, nil
}

// PasskeyLogin is the result of a passkey login.
type PasskeyLogin struct {
	User        *User
	PasskeyName string
}

// CompletePasskeyLogin checks a passkey login and, in one transaction,
// stores the passkey's new state and creates the session. verify gets the
// session data, the user's handle and the stored credential record, and
// returns the updated record. The passkey row is locked meanwhile: a second
// login with the same passkey waits and is checked against the newer sign
// count.
func (s *Store) CompletePasskeyLogin(ctx context.Context, token string, credentialID []byte,
	verify func(data, handle, credential []byte) ([]byte, error),
	sessionHash []byte, expiresAt time.Time, userAgent string) (*PasskeyLogin, error) {
	c, err := s.takeCeremony(ctx, token, ceremonyLogin)
	if err != nil {
		return nil, err
	}
	var out *PasskeyLogin
	err = s.inTx(ctx, func(q *db.Queries) error {
		owner, err := q.PasskeyOwner(ctx, credentialID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPasskeyUnknown
		}
		if err != nil {
			return err
		}
		// Lock order: user, then passkey, as for registrations.
		u, err := lockUser(ctx, q, owner)
		if errors.Is(err, ErrNotFound) {
			return ErrPasskeyUnknown
		}
		if err != nil {
			return err
		}
		p, err := q.LockPasskey(ctx, credentialID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPasskeyUnknown
		}
		if err != nil {
			return err
		}
		if p.UserID != u.ID || u.WebauthnHandle == nil {
			return ErrPasskeyUnknown
		}
		cred, err := verify(c.Data, u.WebauthnHandle, p.Credential)
		if err != nil {
			return errors.Join(ErrPasskeyInvalid, err)
		}
		// Only after a valid passkey does the answer tell that the user is
		// locked, as with a password.
		if u.LockedAt != nil {
			return ErrUserLocked
		}
		if err := q.UpdatePasskeyUse(ctx, db.UpdatePasskeyUseParams{ID: p.ID, Credential: cred}); err != nil {
			return err
		}
		if err := q.CreateSession(ctx, db.CreateSessionParams{ID: sessionHash, UserID: u.ID, ExpiresAt: expiresAt, UserAgent: userAgent}); err != nil {
			return err
		}
		if err := q.RecordLogin(ctx, u.ID); err != nil {
			return err
		}
		out = &PasskeyLogin{User: userFromDB(u), PasskeyName: p.Name}
		return nil
	})
	return out, err
}

// RemoveOwnPasskey deletes one of the user's passkeys and ends all their
// other sessions: sessions started with a stolen passkey end with it. It
// returns the removed passkey.
func (s *Store) RemoveOwnPasskey(ctx context.Context, userID, passkeyID int64, keepSession []byte) (*Passkey, error) {
	var out *Passkey
	err := s.inTx(ctx, func(q *db.Queries) error {
		if _, err := lockUser(ctx, q, userID); err != nil {
			return err
		}
		r, err := q.DeletePasskey(ctx, db.DeletePasskeyParams{ID: passkeyID, UserID: userID})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		out = &Passkey{ID: passkeyID, Name: r.Name, CredentialID: r.CredentialID}
		return q.DeleteOtherSessions(ctx, db.DeleteOtherSessionsParams{UserID: userID, ID: keepSession})
	})
	return out, err
}

// RemovePasskeys deletes all passkeys of a user (by an admin or the CLI)
// and ends all their sessions. It returns how many were removed.
func (s *Store) RemovePasskeys(ctx context.Context, userID int64) (int64, error) {
	var n int64
	err := s.inTx(ctx, func(q *db.Queries) error {
		if _, err := lockUser(ctx, q, userID); err != nil {
			return err
		}
		var err error
		if n, err = removePasskeys(ctx, q, userID); err != nil {
			return err
		}
		return q.DeleteUserSessions(ctx, userID)
	})
	return n, err
}

func removePasskeys(ctx context.Context, q *db.Queries, userID int64) (int64, error) {
	n, err := q.DeleteUserPasskeys(ctx, userID)
	if err != nil {
		return 0, err
	}
	return n, q.DeleteUserWebAuthnCeremonies(ctx, &userID)
}

// DeleteExpiredWebAuthnCeremonies removes registrations and logins nobody
// finished.
func (s *Store) DeleteExpiredWebAuthnCeremonies(ctx context.Context) error {
	return s.q.DeleteExpiredWebAuthnCeremonies(ctx)
}
