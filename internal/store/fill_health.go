package store

import (
	"context"
	"time"

	"github.com/pklnx/mail-archive/internal/store/db"
)

// FillHealth is the failure streak of an active recurring fill, like
// SyncHealth for an IMAP account: a run that fails at the login or for the
// whole mailbox extends it, an ok or partial run ends it.
type FillHealth struct {
	FillID    int64
	AccountID int64
	Account   string
	OwnerID   *int64
	// Enabled is the account's: a disabled account's fills do not run.
	Enabled bool
	// Mailbox is the IMAP user name the fill imports from.
	Mailbox string
	// Interval is the fill's own interval, nil for the server's.
	Interval       *time.Duration
	RecurringSince time.Time
	// Running: a run is in progress (or waits to start), since
	// RunStartedAt.
	Running       bool
	RunStartedAt  *time.Time
	FailureStreak int
	FailingSince  *time.Time
	LastSuccessAt *time.Time
	NotifiedState string
}

// State is the fill's health, as SyncHealth.State: the time counts from
// the last successful run, or from when the fill became recurring.
func (h *FillHealth) State(alertAfter int, every time.Duration, busySince *time.Time, now time.Time) string {
	since := h.RecurringSince
	if h.LastSuccessAt != nil && h.LastSuccessAt.After(since) {
		since = *h.LastSuccessAt
	}
	return HealthState(h.Enabled, h.FailureStreak, since, alertAfter, every, busySince, now)
}

// FillHealth returns the health of the active recurring fills of accounts
// that are not removed, of all users or, with owner set, of one user, by
// fill ID.
func (s *Store) FillHealth(ctx context.Context, owner *int64) (map[int64]*FillHealth, error) {
	rows, err := s.q.FillHealth(ctx, owner)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]*FillHealth, len(rows))
	for _, r := range rows {
		h := &FillHealth{
			FillID: r.ID, AccountID: r.AccountID, Account: r.AccountName, OwnerID: r.OwnerID, Enabled: r.Enabled,
			Mailbox: r.Username, Interval: secondsToInterval(r.IntervalSeconds), RecurringSince: r.RecurringSince,
			Running: r.Status == FillRunning, RunStartedAt: r.RunStartedAt,
			FailureStreak: int(r.FailureStreak), LastSuccessAt: r.LastSuccessAt, NotifiedState: r.NotifiedState,
		}
		if h.FailureStreak > 0 {
			h.FailingSince = r.FailingSince
		}
		out[r.ID] = h
	}
	return out, nil
}

// ClaimFillNotifications is ClaimNotifications for recurring fills; the
// announcements carry FillID and Mailbox.
func (s *Store) ClaimFillNotifications(ctx context.Context, lease []byte, leaseFor time.Duration, threshold, limit int) ([]PendingNotification, error) {
	rows, err := s.q.ClaimFillNotifications(ctx, db.ClaimFillNotificationsParams{
		Lease: lease, LeaseSeconds: leaseFor.Seconds(), Threshold: clampInt32(threshold), MaxRows: clampInt32(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]PendingNotification, 0, len(rows))
	for _, r := range rows {
		out = append(out, PendingNotification{
			AccountID: r.AccountID, Account: r.AccountName, Owner: r.OwnerName, FillID: r.FillID, Mailbox: r.Username,
			FailureStreak: int(r.FailureStreak), FailingSince: r.FailingSince, NotifiedState: r.NotifiedState,
			Attempts: int(r.NotifyAttempts), FirstErrorAt: r.NotifyFirstErrorAt, LastError: r.LastError,
		})
	}
	return out, nil
}

// FinishFillNotification is FinishNotification for a recurring fill.
func (s *Store) FinishFillNotification(ctx context.Context, fillID int64, lease []byte, state, errMsg string) error {
	return one(s.q.FinishFillNotification(ctx, db.FinishFillNotificationParams{
		FillID: fillID, Lease: lease, NotifiedState: state, NotifyError: errMsg,
	}))
}

// DeferFillNotification is DeferNotification for a recurring fill.
func (s *Store) DeferFillNotification(ctx context.Context, fillID int64, lease []byte, next time.Time, errMsg string) error {
	return one(s.q.DeferFillNotification(ctx, db.DeferFillNotificationParams{
		FillID: fillID, Lease: lease, NextAt: next, NotifyError: errMsg,
	}))
}
