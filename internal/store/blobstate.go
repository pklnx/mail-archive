package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store/db"
)

// States of encryption at rest (#392). Off is no blob_encryption row, or
// the row with state 'off' (blobOffRow) that keeps the generation once
// encryption was on or a writer derived the state from the files.
const (
	BlobOff        = ""
	BlobOn         = "on"
	BlobEncrypting = "encrypting"
	BlobDecrypting = "decrypting"
	BlobRekeying   = "rekeying"
)

// blobOffRow is the database value of state for BlobOff.
const blobOffRow = "off"

// BlobTransitional reports whether state is an unfinished offline
// conversion, during which no writer may start (maintainer decision 5).
func BlobTransitional(state string) bool {
	return state == BlobEncrypting || state == BlobDecrypting || state == BlobRekeying
}

// BlobEncryption is the row of blob_encryption (see 00037).
type BlobEncryption struct {
	State      string
	Generation int64
	// Keyring is the database copy of the keyring file, as blobcrypt
	// writes it; KeyringID, KeyringGeneration and CurrentKey repeat its
	// metadata.
	Keyring           []byte
	KeyringID         string
	KeyringGeneration int64
	CurrentKey        uint32
	Cursor            string
	FilesDone         int64
	FilesCorrupt      int64
	StartedAt         time.Time
	FinishedAt        *time.Time
	UpdatedAt         time.Time
}

// KeyringCopy is the keyring as stored in blob_encryption.
type KeyringCopy struct {
	Keyring    []byte
	ID         string
	Generation int64
	CurrentKey uint32
}

func blobEncryptionOf(r db.BlobEncryption) *BlobEncryption {
	state := r.State
	if state == blobOffRow {
		state = BlobOff
	}
	return &BlobEncryption{
		State: state, Generation: r.Generation, Keyring: r.Keyring, KeyringID: r.KeyringID,
		KeyringGeneration: r.KeyringGeneration, CurrentKey: uint32(r.CurrentKey), //nolint:gosec // CHECK BETWEEN 1 AND 2^32-1
		Cursor: r.Cursor, FilesDone: r.FilesDone, FilesCorrupt: r.FilesCorrupt,
		StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, UpdatedAt: r.UpdatedAt,
	}
}

// BlobEncryption returns the state of encryption at rest: nil without a
// row, a row with State BlobOff when it is off but was recorded (its
// generation and its last keyring copy stay).
func (s *Store) BlobEncryption(ctx context.Context) (*BlobEncryption, error) {
	r, err := s.q.GetBlobEncryption(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return blobEncryptionOf(r), nil
}

// SetBlobEncryption sets the state (on or a transitional one) and
// increments the generation, resetting the progress. Without a row it
// creates one with the keyring copy k; from state off the row takes k as
// its keyring copy and continues its generation; otherwise k is ignored.
// The offline conversion commands call it with the writers lock held
// exclusively (part 3).
func (s *Store) SetBlobEncryption(ctx context.Context, state string, k KeyringCopy) error {
	if state != BlobOn && !BlobTransitional(state) {
		return fmt.Errorf("not a state of encryption at rest: %q", state)
	}
	return s.inTx(ctx, func(q *db.Queries) error {
		r, err := q.GetBlobEncryptionForUpdate(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return q.InsertBlobEncryption(ctx, db.InsertBlobEncryptionParams{
				State: state, Generation: 1, Keyring: k.Keyring, KeyringID: k.ID,
				KeyringGeneration: k.Generation, CurrentKey: int64(k.CurrentKey),
			})
		}
		if err != nil {
			return err
		}
		if r.State == blobOffRow {
			return one(q.EnableBlobEncryption(ctx, db.EnableBlobEncryptionParams{
				State: state, Keyring: k.Keyring, KeyringID: k.ID,
				KeyringGeneration: k.Generation, CurrentKey: int64(k.CurrentKey),
			}))
		}
		return one(q.SetBlobEncryptionState(ctx, state))
	})
}

// ClearBlobEncryption turns encryption off. The row stays, with state off
// and the next generation, so the generation never repeats: a writer that
// pinned a generation before a disable and an enable cannot match the new
// one (review round 1 of #510). Without a row, or already off, it does
// nothing.
func (s *Store) ClearBlobEncryption(ctx context.Context) error {
	_, err := s.q.ClearBlobEncryption(ctx)
	return err
}

// RecordBlobOff records state off with the keyring copy k: a writer that
// found a keyring file but no row, and no encrypted file, so that later
// starts do not walk the message files again. A new row has generation 0,
// the generation of no row: writers that pinned 0 keep running (review
// round 2 of #510). A row created meanwhile stays; an off row only gets k
// as its copy.
func (s *Store) RecordBlobOff(ctx context.Context, k KeyringCopy) error {
	return s.q.RecordBlobOff(ctx, db.RecordBlobOffParams{
		Keyring: k.Keyring, KeyringID: k.ID, KeyringGeneration: k.Generation, CurrentKey: int64(k.CurrentKey),
	})
}

// ErrKeyringChanged means the database copy of the keyring changed while
// it was being reconciled; reading both copies again resolves it.
var ErrKeyringChanged = errors.New("the database copy of the keyring changed meanwhile")

// UpdateBlobKeyring replaces the database copy of the keyring with k, if
// the copy is still the one read (generation expected). A new current
// key increments the state's generation. Without a row, or with state
// off, it fails with ErrNotFound.
func (s *Store) UpdateBlobKeyring(ctx context.Context, expected int64, k KeyringCopy) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		r, err := q.GetBlobEncryptionForUpdate(ctx)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && r.State == blobOffRow {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if r.KeyringGeneration != expected {
			return ErrKeyringChanged
		}
		return one(q.UpdateBlobKeyring(ctx, db.UpdateBlobKeyringParams{
			Keyring: k.Keyring, KeyringID: k.ID, KeyringGeneration: k.Generation, CurrentKey: int64(k.CurrentKey),
		}))
	})
}

// ErrBlobStateChanged means the state of encryption at rest changed since
// this process read it at start: it must not insert messages any more and
// stops (#392, part 2; revision 5, B1 (c)).
var ErrBlobStateChanged = errors.New("encryption at rest changed since this process started; it stops and must be restarted")

// blobPin is the generation a writer read at start.
type blobPin struct {
	generation int64 // 0: no row (off, never recorded)
	onChange   func()
	once       sync.Once
}

// PinBlobGeneration makes every transaction that inserts messages
// (SaveBatch, the fill and journal batches, ReceiveMessage) compare the
// generation of encryption at rest with generation (0 without a row) and fail
// with ErrBlobStateChanged on a difference; onChange then runs once, so
// that the process stops. Writers call it once at start, with the
// writers lock held.
func (s *Store) PinBlobGeneration(generation int64, onChange func()) {
	s.blobPin.Store(&blobPin{generation: generation, onChange: onChange})
}

// checkBlobGeneration is the check of PinBlobGeneration, inside an
// inserting transaction.
func (s *Store) checkBlobGeneration(ctx context.Context, q *db.Queries) error {
	p := s.blobPin.Load()
	if p == nil {
		return nil
	}
	gen, err := q.BlobGeneration(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		gen, err = 0, nil
	}
	if err != nil {
		return fmt.Errorf("read the state of encryption at rest: %w", err)
	}
	if gen == p.generation {
		return nil
	}
	if p.onChange != nil {
		p.once.Do(p.onChange)
	}
	return fmt.Errorf("%w (generation %d, started with %d)", ErrBlobStateChanged, gen, p.generation)
}
