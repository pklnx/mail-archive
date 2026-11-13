package store

import (
	"context"
	"time"

	"github.com/pklnx/mail-archive/internal/store/db"
)

// Thresholds of a loss alert, per account and sync run.
const (
	// LossAlertAlways: this many lost messages always alert.
	LossAlertAlways = 100
	// LossAlertMin: fewer lost messages never alert.
	LossAlertMin = 10
	// LossAlertPercent: between the two, a loss of at least this share of
	// the messages present before the run alerts.
	LossAlertPercent = 10
	// MaxLossAlertFolders is the most folders an alert names.
	MaxLossAlertFolders = 20
)

// LossAlertDue reports whether losing lost of presentBefore messages in
// one run is worth an alert.
func LossAlertDue(lost, presentBefore int) bool {
	return lost >= LossAlertAlways || lost >= LossAlertMin && lost*100 >= presentBefore*LossAlertPercent
}

// FolderLoss is the number of lost messages that went gone in a folder.
type FolderLoss struct {
	Name string
	Lost int
}

// LossAlert describes the messages an account lost in one run: they are
// now only in the archive.
type LossAlert struct {
	Lost          int
	PresentBefore int
	// Folders are the folders where they went gone, most first, at most
	// MaxLossAlertFolders; MoreFolders counts the rest.
	Folders     []FolderLoss
	MoreFolders int
}

// LostMessages counts the messages of the account that lost their last
// present location during the run. Locations in baseline folders (whose
// first reconcile ran in this run) do not count. PresentBefore is only
// counted when Lost reaches LossAlertMin, so that a normal run stays cheap.
func (s *Store) LostMessages(ctx context.Context, accountID, runID int64, baseline []int64) (*LossAlert, error) {
	if baseline == nil {
		baseline = []int64{}
	}
	rows, err := s.q.LostMessages(ctx, db.LostMessagesParams{AccountID: accountID, RunID: runID, Baseline: baseline})
	if err != nil {
		return nil, err
	}
	a := &LossAlert{}
	for _, r := range rows {
		switch {
		case r.Total:
			a.Lost = int(r.Lost)
		case len(a.Folders) < MaxLossAlertFolders:
			a.Folders = append(a.Folders, FolderLoss{Name: r.Folder, Lost: int(r.Lost)})
		default:
			a.MoreFolders++
		}
	}
	if a.Lost < LossAlertMin {
		a.PresentBefore = a.Lost
		return a, nil
	}
	present, err := s.q.CountPresentMessages(ctx, accountID)
	if err != nil {
		return nil, err
	}
	a.PresentBefore = int(present) + a.Lost
	return a, nil
}

func insertLossAlert(ctx context.Context, q *db.Queries, accountID, runID int64, a *LossAlert) error {
	names := make([]string, len(a.Folders))
	lost := make([]int32, len(a.Folders))
	for i, f := range a.Folders {
		names[i], lost[i] = f.Name, clampInt32(f.Lost)
	}
	return q.InsertLossAlert(ctx, db.InsertLossAlertParams{
		AccountID: accountID, SyncRunID: runID, Lost: clampInt32(a.Lost), PresentBefore: clampInt32(a.PresentBefore),
		FolderNames: names, FolderLost: lost, MoreFolders: clampInt32(a.MoreFolders),
	})
}

// PendingLossAlert is a loss alert claimed for delivery.
type PendingLossAlert struct {
	ID        int64
	SyncRunID int64
	AccountID int64
	Account   string
	Owner     string
	CreatedAt time.Time
	LossAlert
	Attempts int
}

// ExpireLossAlerts gives up the alerts not sent within maxAge.
func (s *Store) ExpireLossAlerts(ctx context.Context, maxAge time.Duration) (int, error) {
	n, err := s.q.ExpireLossAlerts(ctx, maxAge.Seconds())
	return int(n), err
}

// ClaimLossAlerts leases up to limit unsent loss alerts younger than
// maxAge that are due, for leaseFor.
func (s *Store) ClaimLossAlerts(ctx context.Context, lease []byte, leaseFor, maxAge time.Duration, limit int) ([]PendingLossAlert, error) {
	rows, err := s.q.ClaimLossAlerts(ctx, db.ClaimLossAlertsParams{
		Lease: lease, LeaseSeconds: leaseFor.Seconds(), MaxAgeSeconds: maxAge.Seconds(), MaxRows: clampInt32(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]PendingLossAlert, 0, len(rows))
	for _, r := range rows {
		p := PendingLossAlert{
			ID: r.ID, SyncRunID: r.SyncRunID, AccountID: r.AccountID, Account: r.AccountName, Owner: r.OwnerName,
			CreatedAt: r.CreatedAt, Attempts: int(r.NotifyAttempts),
			LossAlert: LossAlert{Lost: int(r.Lost), PresentBefore: int(r.PresentBefore), MoreFolders: int(r.MoreFolders)},
		}
		for i, name := range r.FolderNames {
			if i < len(r.FolderLost) {
				p.Folders = append(p.Folders, FolderLoss{Name: name, Lost: int(r.FolderLost[i])})
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// FinishLossAlert records an alert as sent, or with givenUp as given up
// after errMsg, and releases the lease. It returns ErrNotFound if the
// lease was lost.
func (s *Store) FinishLossAlert(ctx context.Context, id int64, lease []byte, givenUp bool, errMsg string) error {
	return one(s.q.FinishLossAlert(ctx, db.FinishLossAlertParams{ID: id, Lease: lease, GivenUp: givenUp, NotifyError: errMsg}))
}

// DeferLossAlert records a failed delivery attempt, schedules the next one
// and releases the lease. It returns ErrNotFound if the lease was lost.
func (s *Store) DeferLossAlert(ctx context.Context, id int64, lease []byte, next time.Time, errMsg string) error {
	return one(s.q.DeferLossAlert(ctx, db.DeferLossAlertParams{ID: id, Lease: lease, NextAt: next, NotifyError: errMsg}))
}
