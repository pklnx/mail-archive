package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pklnx/mail-archive/internal/store/db"
)

// Deletion runs (#55): a run is proposed with its candidates, reviewed,
// confirmed and then executed in batches (archive.ExecuteDeletionRun).

// States of a deletion run.
const (
	RunProposed  = "proposed"
	RunConfirmed = "confirmed"
	RunDone      = "done"
	RunCancelled = "cancelled"
)

// ErrRunOpen is returned when a run is proposed while another one is open.
var ErrRunOpen = errors.New("a deletion run is open; confirm or cancel it first")

// ErrRunState is returned for a step the run's state does not allow.
var ErrRunState = errors.New("not possible in the run's state")

// DeletionRun is a deletion run and its counts.
type DeletionRun struct {
	ID          int64
	State       string
	CreatedAt   time.Time
	CreatedBy   string // empty: the command line
	Candidates  int
	Bytes       int64
	ConfirmedAt *time.Time
	ConfirmedBy string
	FinishedAt  *time.Time
	Deleted     int
	Kept        int
}

func runFromRow(r db.GetDeletionRunRow) *DeletionRun {
	return &DeletionRun{
		ID: r.ID, State: r.State, CreatedAt: r.CreatedAt, CreatedBy: deref(r.CreatedBy), Candidates: int(r.Candidates),
		Bytes: r.Bytes, ConfirmedAt: r.ConfirmedAt, ConfirmedBy: deref(r.ConfirmedBy), FinishedAt: r.FinishedAt,
		Deleted: int(r.Deleted), Kept: int(r.Kept),
	}
}

// ProposeDeletion matches the filter holds, then creates a run with every
// message that may be deleted now: all its locations, in every user's
// accounts, have an ended retention period and no active hold. Nothing is
// deleted. It fails with ErrRunOpen while another run is open.
func (s *Store) ProposeDeletion(ctx context.Context, by *int64) (*DeletionRun, error) {
	if _, err := s.MatchLegalHolds(ctx); err != nil {
		return nil, fmt.Errorf("match legal holds: %w", err)
	}
	var id int64
	err := s.inTx(ctx, func(q *db.Queries) error {
		if err := q.LockRetentionChanges(ctx, retentionLockClass); err != nil {
			return err
		}
		switch open, err := q.OpenDeletionRunID(ctx); {
		case err == nil:
			return fmt.Errorf("%w (run %d)", ErrRunOpen, open)
		case !isNoRows(err):
			return err
		}
		row, err := q.InsertDeletionRun(ctx, by)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			// Another process proposed one meanwhile.
			return ErrRunOpen
		}
		if err != nil {
			return err
		}
		id = row.ID
		if _, err := q.InsertDeletionCandidates(ctx, id); err != nil {
			return err
		}
		counts, err := q.SetDeletionRunCounts(ctx, id)
		if err != nil {
			return err
		}
		if e := auditFrom(ctx); e != nil {
			e.Detail("run_id", id)
			e.Detail("candidates", counts.Candidates)
			e.Detail("bytes", counts.Bytes)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.DeletionRun(ctx, id)
}

// DeletionRun returns a run; unknown IDs are ErrNotFound.
func (s *Store) DeletionRun(ctx context.Context, id int64) (*DeletionRun, error) {
	r, err := s.q.GetDeletionRun(ctx, id)
	if isNoRows(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return runFromRow(r), nil
}

// DeletionRuns lists the newest runs first.
func (s *Store) DeletionRuns(ctx context.Context, limit int) ([]DeletionRun, error) {
	rows, err := s.q.ListDeletionRuns(ctx, clampInt32(limit))
	if err != nil {
		return nil, err
	}
	out := make([]DeletionRun, len(rows))
	for i, r := range rows {
		out[i] = *runFromRow(db.GetDeletionRunRow(r))
	}
	return out, nil
}

// OpenDeletionRun returns the ID of the proposed or confirmed run, 0 if
// there is none.
func (s *Store) OpenDeletionRun(ctx context.Context) (int64, error) {
	id, err := s.q.OpenDeletionRunID(ctx)
	if isNoRows(err) {
		return 0, nil
	}
	return id, err
}

// ConfirmDeletionRun confirms a proposed run. It deletes nothing itself;
// archive.ExecuteDeletionRun does.
func (s *Store) ConfirmDeletionRun(ctx context.Context, id int64, by *int64) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		n, err := q.ConfirmDeletionRun(ctx, db.ConfirmDeletionRunParams{ID: id, ConfirmedBy: by})
		if err != nil {
			return err
		}
		if n == 0 {
			return runStateError(ctx, q, id, "confirmed")
		}
		if e := auditFrom(ctx); e != nil {
			e.Detail("run_id", id)
		}
		return nil
	})
}

// CancelDeletionRun cancels a proposed run.
func (s *Store) CancelDeletionRun(ctx context.Context, id int64) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		n, err := q.CancelDeletionRun(ctx, id)
		if err != nil {
			return err
		}
		if n == 0 {
			return runStateError(ctx, q, id, "cancelled")
		}
		if e := auditFrom(ctx); e != nil {
			e.Detail("run_id", id)
		}
		return nil
	})
}

// runStateError tells why a run could not be changed: it does not exist,
// or its state does not allow it.
func runStateError(ctx context.Context, q *db.Queries, id int64, what string) error {
	r, err := q.GetDeletionRun(ctx, id)
	if isNoRows(err) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("run %d is %s and cannot be %s: %w", id, r.State, what, ErrRunState)
}

// PendingCandidates returns up to limit pending candidates of a run after
// the hash after, in hash order.
func (s *Store) PendingCandidates(ctx context.Context, runID int64, after string, limit int) ([]string, error) {
	return s.q.PendingCandidates(ctx, db.PendingCandidatesParams{RunID: runID, After: after, RowLimit: clampInt32(limit)})
}

// DeleteBatch deletes those of shas, candidates of the confirmed run, that
// are still deletable, and marks the others kept. In one transaction it
// writes the tombstones and the deletion records of the attestation chain,
// deletes the locations and the messages, and schedules their files for
// removal after grace. The caller holds the blob lock exclusively, so no
// writer stores one of these messages meanwhile. An audit entry from
// WithAudit in ctx is written with it.
func (s *Store) DeleteBatch(ctx context.Context, runID int64, shas []string, grace time.Duration) (deleted, kept int, err error) {
	err = s.inWriteTx(ctx, false, func(q *db.Queries) error {
		deleted, kept = 0, 0
		// Rule and hold changes wait for the batch, and the batch for them.
		if err := q.LockRetentionChanges(ctx, retentionLockClass); err != nil {
			return err
		}
		run, err := q.GetDeletionRun(ctx, runID)
		if err != nil {
			return err
		}
		if run.State != RunConfirmed {
			return fmt.Errorf("run %d is %s: %w", runID, run.State, ErrRunState)
		}
		deletable, err := q.DeletableOf(ctx, shas)
		if err != nil {
			return err
		}
		del := make(map[string]bool, len(deletable))
		for _, sha := range deletable {
			del[sha] = true
		}
		var keep []string
		for _, sha := range shas {
			if !del[sha] {
				keep = append(keep, sha)
			}
		}
		if len(deletable) > 0 {
			if _, err := q.InsertTombstones(ctx, db.InsertTombstonesParams{RunID: runID, Shas: deletable}); err != nil {
				return fmt.Errorf("tombstones: %w", err)
			}
			if _, err := q.InsertRetentionDeletions(ctx, deletable); err != nil {
				return fmt.Errorf("deletion records: %w", err)
			}
			if _, err := q.DeleteLocationsOf(ctx, deletable); err != nil {
				return fmt.Errorf("delete locations: %w", err)
			}
			n, err := q.DeleteMessagesOf(ctx, deletable)
			if err != nil {
				return fmt.Errorf("delete messages: %w", err)
			}
			if int(n) != len(deletable) {
				return fmt.Errorf("deleted %d of %d messages", n, len(deletable))
			}
			if err := q.ScheduleBlobRemovals(ctx, db.ScheduleBlobRemovalsParams{Shas: deletable, DueAt: time.Now().Add(grace)}); err != nil {
				return err
			}
			if err := q.MarkCandidates(ctx, db.MarkCandidatesParams{RunID: runID, Shas: deletable, State: "deleted"}); err != nil {
				return err
			}
		}
		if len(keep) > 0 {
			if err := q.MarkCandidates(ctx, db.MarkCandidatesParams{RunID: runID, Shas: keep, State: "kept"}); err != nil {
				return err
			}
		}
		deleted, kept = len(deletable), len(keep)
		if err := q.AddDeletionRunCounts(ctx, db.AddDeletionRunCountsParams{ID: runID, Deleted: int32(deleted), Kept: int32(kept)}); err != nil { //nolint:gosec // batch sizes
			return err
		}
		if e := auditFrom(ctx); e != nil {
			e.Detail("run_id", runID)
			e.Detail("deleted", deleted)
			e.Detail("kept", kept)
		}
		return nil
	})
	return deleted, kept, err
}

// FinishDeletionRun marks a confirmed run done.
func (s *Store) FinishDeletionRun(ctx context.Context, id int64) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		n, err := q.FinishDeletionRun(ctx, id)
		if err != nil {
			return err
		}
		if n == 0 {
			return runStateError(ctx, q, id, "finished")
		}
		if e := auditFrom(ctx); e != nil {
			r, err := q.GetDeletionRun(ctx, id)
			if err != nil {
				return err
			}
			e.Detail("run_id", id)
			e.Detail("deleted", r.Deleted)
			e.Detail("kept", r.Kept)
		}
		return nil
	})
}

// TryLockDeletionRun takes the lock that lets one process execute a run.
// It returns false if another one does.
func (s *Store) TryLockDeletionRun(ctx context.Context) (unlock func(), ok bool, err error) {
	return s.tryLockClass(ctx, retentionLockClass)
}

// DeletionGroup counts a run's pending candidates by where they are and
// the year their retention period ended in. A message in two folders
// counts in both.
type DeletionGroup struct {
	Owner    string
	Account  string
	Folder   string
	Year     int
	Messages int64
}

// DeletionRunSummary groups a run's pending candidates for the review.
func (s *Store) DeletionRunSummary(ctx context.Context, runID int64) ([]DeletionGroup, error) {
	rows, err := s.q.DeletionRunSummary(ctx, runID)
	if err != nil {
		return nil, err
	}
	out := make([]DeletionGroup, len(rows))
	for i, r := range rows {
		out[i] = DeletionGroup{Owner: r.Owner, Account: r.Account, Folder: r.Folder, Year: int(r.Year), Messages: r.Messages}
	}
	return out, nil
}

// DeletionCandidate is a candidate of a run with its details, while the
// message exists.
type DeletionCandidate struct {
	SHA256      string
	Size        int64
	RetainUntil time.Time
	State       string // pending, deleted or kept
	Subject     string
	From        string
	SentAt      *time.Time
	Places      []string // owner/account/folder
}

// DeletionCandidates lists a run's candidates, those whose period ended
// first first.
func (s *Store) DeletionCandidates(ctx context.Context, runID int64) ([]DeletionCandidate, error) {
	rows, err := s.q.DeletionCandidateDetails(ctx, runID)
	if err != nil {
		return nil, err
	}
	out := make([]DeletionCandidate, len(rows))
	for i, r := range rows {
		out[i] = DeletionCandidate{
			SHA256: r.Sha256, Size: r.Size, RetainUntil: r.RetainUntil, State: r.State, Subject: deref(r.Subject),
			From: deref(r.FromAddr), SentAt: r.SentAt, Places: nonNil(r.Places),
		}
	}
	return out, nil
}

// BlobRemoval is a message file due for removal.
type BlobRemoval struct {
	SHA256 string
	DueAt  time.Time
	// Archived: the message was archived again; keep the file.
	Archived bool
}

// BlobRemovalDue reports whether a file removal is due.
func (s *Store) BlobRemovalDue(ctx context.Context) (bool, error) {
	return s.q.BlobRemovalDue(ctx)
}

// DueBlobRemovals returns up to limit due removals after (afterDue,
// afterSHA), in that order.
func (s *Store) DueBlobRemovals(ctx context.Context, afterDue time.Time, afterSHA string, limit int) ([]BlobRemoval, error) {
	rows, err := s.q.DueBlobRemovals(ctx, db.DueBlobRemovalsParams{AfterDue: afterDue, AfterSha: afterSHA, RowLimit: clampInt32(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]BlobRemoval, len(rows))
	for i, r := range rows {
		out[i] = BlobRemoval{SHA256: r.Sha256, DueAt: r.DueAt, Archived: r.Archived}
	}
	return out, nil
}

// DoneBlobRemovals forgets due removals that were carried out (or skipped
// because the message came back).
func (s *Store) DoneBlobRemovals(ctx context.Context, shas []string) (int64, error) {
	return s.q.DeleteBlobRemovals(ctx, shas)
}

// ScheduleBlobRemoval schedules a message file for removal at due.
func (s *Store) ScheduleBlobRemoval(ctx context.Context, sha256 string, due time.Time) error {
	return s.q.ScheduleBlobRemovals(ctx, db.ScheduleBlobRemovalsParams{Shas: []string{sha256}, DueAt: due})
}

// IsTombstoned reports whether a message was deleted by retention.
func (s *Store) IsTombstoned(ctx context.Context, sha256 string) (bool, error) {
	return s.q.IsTombstoned(ctx, sha256)
}

// DeletionCounts are the numbers status shows.
type DeletionCounts struct {
	OpenRun      int64 // 0 without one
	PendingFiles int64 // files waiting for removal
}

// DeletionCounts returns the open run and the pending file removals.
func (s *Store) DeletionCounts(ctx context.Context) (DeletionCounts, error) {
	r, err := s.q.DeletionCounts(ctx)
	return DeletionCounts{OpenRun: r.OpenRun, PendingFiles: r.PendingFiles}, err
}

// DeletedFiles returns, of some hashes, those deleted by retention and not
// archived again, mapped to whether their file is scheduled for removal.
func (s *Store) DeletedFiles(ctx context.Context, shas []string) (map[string]bool, error) {
	rows, err := s.q.DeletedFiles(ctx, shas)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(rows))
	for _, r := range rows {
		out[r.Sha256] = r.Scheduled
	}
	return out, nil
}
