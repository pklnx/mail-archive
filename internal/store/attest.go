package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/attest"
	"github.com/pklnx/mail-archive/internal/store/db"
)

// attestPageSize is how many leaves are read per query.
const attestPageSize = 10_000

// attestLockTimeout bounds how long an attestation waits for the fence,
// that is for the sync batches and received messages in progress.
const attestLockTimeout = "30s"

// Attestations returns the chain in seq order.
func (s *Store) Attestations(ctx context.Context) ([]attest.Entry, error) {
	rows, err := s.q.ListAttestations(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]attest.Entry, len(rows))
	for i := range rows {
		out[i] = entryFromRow(&rows[i])
	}
	return out, nil
}

// LastAttestation returns the newest attestation, or nil if there is none.
func (s *Store) LastAttestation(ctx context.Context) (*attest.Entry, error) {
	r, err := s.q.LastAttestation(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	e := entryFromRow(&r)
	return &e, nil
}

// CountAttestations returns the length of the chain.
func (s *Store) CountAttestations(ctx context.Context) (int64, error) {
	return s.q.CountAttestations(ctx)
}

func entryFromRow(r *db.Attestation) attest.Entry {
	return attest.Entry{
		Seq: r.Seq, Time: r.CreatedAt.UTC(),
		MessagesAfter: r.MessagesAfter, MessagesUpto: r.MessagesUpto, MessagesCount: r.MessagesCount,
		DeletionsAfter: r.DeletionsAfter, DeletionsUpto: r.DeletionsUpto, DeletionsCount: r.DeletionsCount,
		Total: r.Total, Leaves: r.LeavesSha256, Prev: r.PrevHash, Key: r.PublicKey, Hash: r.Hash, Sig: r.Signature,
	}
}

// AttestBounds is where the snapshot of an attestation ends.
type AttestBounds struct {
	MessagesUpto  int64 // highest messages.seq, 0 if none
	DeletionsUpto int64 // highest message_deletions.id, 0 if none
	Total         int64 // number of messages
}

// AttestSnapshot is a repeatable read transaction whose snapshot no
// writer was in progress for: every message with seq <= Bounds.MessagesUpto
// is in it, and none can be added later.
type AttestSnapshot struct {
	Bounds AttestBounds
	q      *db.Queries
}

// WithAttestSnapshot runs fn in an AttestSnapshot and commits if fn
// succeeds. It takes the fence exclusively on a dedicated connection,
// which waits for running writers (at most attestLockTimeout) and makes
// new ones wait; starts the transaction, whose first query takes the
// snapshot; and releases the fence at once. Writers wait only that long.
// The caller holds the attestation writer lock.
func (s *Store) WithAttestSnapshot(ctx context.Context, fn func(*AttestSnapshot) error) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			// Closing the connection releases the fence and ends the
			// transaction, whatever state they are in.
			_ = conn.Conn().Close(context.WithoutCancel(ctx))
		}
		conn.Release()
	}()
	if _, err := conn.Exec(ctx, "SET lock_timeout = '"+attestLockTimeout+"'"); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1, 0)", attestFenceClass); err != nil {
		return fmt.Errorf("wait for the attestation fence: %w", err)
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return err
	}
	q := s.q.WithTx(tx)
	b, err := q.AttestBounds(ctx)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_unlock($1, 0)", attestFenceClass); err != nil {
		return err
	}
	if err := fn(&AttestSnapshot{Bounds: AttestBounds(b), q: q}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, "RESET lock_timeout"); err != nil {
		return err
	}
	ok = true
	return nil
}

// Messages calls fn for every message with after < seq <= upto, in seq
// order.
func (a *AttestSnapshot) Messages(ctx context.Context, after, upto int64, fn func(seq int64, sha256 string) error) error {
	for {
		rows, err := a.q.ListAttestMessages(ctx, db.ListAttestMessagesParams{After: after, Upto: upto, PageSize: attestPageSize})
		if err != nil {
			return err
		}
		for _, r := range rows {
			if err := fn(r.Seq, r.Sha256); err != nil {
				return err
			}
			after = r.Seq
		}
		if len(rows) < attestPageSize {
			return nil
		}
	}
}

// Deletions calls fn for every deletion with after < id <= upto, in id
// order.
func (a *AttestSnapshot) Deletions(ctx context.Context, after, upto int64, fn func(attest.Deletion) error) error {
	return listDeletions(ctx, a.q, after, upto, fn)
}

// Insert adds an attestation to the chain.
func (a *AttestSnapshot) Insert(ctx context.Context, e *attest.Entry) error {
	return a.q.InsertAttestation(ctx, db.InsertAttestationParams{
		Seq: e.Seq, CreatedAt: e.Time,
		MessagesAfter: e.MessagesAfter, MessagesUpto: e.MessagesUpto, MessagesCount: e.MessagesCount,
		DeletionsAfter: e.DeletionsAfter, DeletionsUpto: e.DeletionsUpto, DeletionsCount: e.DeletionsCount,
		Total: e.Total, LeavesSha256: e.Leaves, PrevHash: e.Prev, PublicKey: e.Key, Hash: e.Hash, Signature: e.Sig,
	})
}

func listDeletions(ctx context.Context, q *db.Queries, after, upto int64, fn func(attest.Deletion) error) error {
	for {
		rows, err := q.ListAttestDeletions(ctx, db.ListAttestDeletionsParams{After: after, Upto: upto, PageSize: attestPageSize})
		if err != nil {
			return err
		}
		for _, r := range rows {
			if err := fn(attest.Deletion{ID: r.ID, MessageSeq: r.MessageSeq, SHA256: r.Sha256, Reason: r.Reason}); err != nil {
				return err
			}
			after = r.ID
		}
		if len(rows) < attestPageSize {
			return nil
		}
	}
}

// RebuildLeaves writes the leaves of attestation e as today's database
// has them: the messages of its range still present plus those deleted
// after it, then the deletions of its range. If nothing was removed or
// changed since, the result is the same bytes as when e was written. It
// reads in one repeatable read transaction, so a concurrent deletion does
// not show up half.
func (s *Store) RebuildLeaves(ctx context.Context, e *attest.Entry, w *attest.LeavesWriter) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.q.WithTx(tx)
	cursor := e.MessagesAfter
	for {
		rows, err := q.ListAttestedRange(ctx, db.ListAttestedRangeParams{
			After: e.MessagesAfter, Upto: e.MessagesUpto, Cursor: cursor,
			DeletionsUpto: e.DeletionsUpto, PageSize: attestPageSize,
		})
		if err != nil {
			return err
		}
		for _, r := range rows {
			if err := w.Message(r.Seq, r.Sha256); err != nil {
				return err
			}
			cursor = r.Seq
		}
		if len(rows) < attestPageSize {
			break
		}
	}
	if err := listDeletions(ctx, q, e.DeletionsAfter, e.DeletionsUpto, w.Deletion); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
