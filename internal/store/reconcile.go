package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store/db"
)

// ReconcileCounts sums what reconciles changed.
type ReconcileCounts struct {
	Folders      int // folders compared with the server
	Gone         int // locations newly missing on the server
	Back         int // gone locations the server listed again
	FlagsChanged int // locations whose flags changed
}

// Add adds the counts of one folder.
func (c *ReconcileCounts) Add(f FolderReconcile) {
	c.Folders++
	c.Gone += f.Gone + f.Superseded
	c.Back += f.Back
	c.FlagsChanged += f.FlagsChanged
}

// FolderReconcile is what reconciling one folder changed.
type FolderReconcile struct {
	// PresentBefore counts the folder's current locations that were not
	// gone before.
	PresentBefore int
	Gone          int // current locations missing on the server
	Superseded    int // locations of an older UIDVALIDITY, now gone
	Back          int
	FlagsChanged  int
}

// ErrFolderChanged is returned when a folder's UIDVALIDITY in the archive
// is not the one the reconcile was made for.
var ErrFolderChanged = errors.New("folder UIDVALIDITY changed")

// ReconcileFolder records which of a folder's locations the server still
// lists: uids and flags (one space-separated string per UID, same order)
// are everything the server listed for the given UIDVALIDITY. All changes
// are written in one transaction, which also records the folder as
// reconciled. Call it while holding the account's sync lock.
func (s *Store) ReconcileFolder(ctx context.Context, folderID int64, uidValidity uint32,
	uids []int64, flags []string) (FolderReconcile, error) {
	if len(uids) != len(flags) {
		return FolderReconcile{}, fmt.Errorf("reconcile: %d UIDs but %d flag sets", len(uids), len(flags))
	}
	var out FolderReconcile
	err := s.inTx(ctx, func(q *db.Queries) error {
		f, err := q.LockFolderForReconcile(ctx, folderID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if f.Uidvalidity != int64(uidValidity) {
			return ErrFolderChanged
		}
		r, err := q.ReconcileFolder(ctx, db.ReconcileFolderParams{
			Uids: uids, Flags: flags, FolderID: folderID, Uidvalidity: int64(uidValidity),
			LastUid: f.LastUid, PrevReconciledAt: f.LastReconciledAt,
		})
		if err != nil {
			return err
		}
		out = FolderReconcile{
			PresentBefore: int(r.PresentBefore), Gone: int(r.Gone), Superseded: int(r.Superseded),
			Back: int(r.Back), FlagsChanged: int(r.Changed),
		}
		return nil
	})
	return out, err
}

// MarkFolderVanished marks every location of a folder that the server no
// longer lists as gone and returns how many were present before.
func (s *Store) MarkFolderVanished(ctx context.Context, folderID int64) (int, error) {
	var n int
	err := s.inTx(ctx, func(q *db.Queries) error {
		f, err := q.LockFolderForReconcile(ctx, folderID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		r, err := q.MarkFolderVanished(ctx, db.MarkFolderVanishedParams{FolderID: folderID, PrevReconciledAt: f.LastReconciledAt})
		n = int(r.Gone)
		return err
	})
	return n, err
}

// FolderState is a stored folder of an account.
type FolderState struct {
	ID               int64
	Name             string
	LastReconciledAt *time.Time
}

// ListFolderStates returns every stored folder of an account.
func (s *Store) ListFolderStates(ctx context.Context, accountID int64) ([]FolderState, error) {
	rows, err := s.q.ListAccountFolderStates(ctx, accountID)
	if err != nil {
		return nil, err
	}
	out := make([]FolderState, len(rows))
	for i, r := range rows {
		out[i] = FolderState{ID: r.ID, Name: r.Name, LastReconciledAt: r.LastReconciledAt}
	}
	return out, nil
}

// ReconcileState is what reconciles found for one account.
type ReconcileState struct {
	// Gone counts messages whose locations in the account are all gone
	// from the server.
	Gone             int64
	LastReconciledAt *time.Time
}

// ReconcileStates returns the reconcile state of a user's accounts by ID.
func (s *Store) ReconcileStates(ctx context.Context, owner int64) (map[int64]ReconcileState, error) {
	rows, err := s.q.ReconcileStates(ctx, owner)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]ReconcileState, len(rows))
	for _, r := range rows {
		out[r.ID] = ReconcileState{Gone: r.Gone, LastReconciledAt: r.LastReconciledAt}
	}
	return out, nil
}
