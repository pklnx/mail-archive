package store

import (
	"context"
	"time"

	"github.com/pklnx/mail-archive/internal/store/db"
)

// HealthEffect tells FinishSyncRun how a run changes the account's sync
// health.
type HealthEffect int

// Health effects of a sync run.
const (
	// HealthUnchanged: import runs and syncs cancelled by a shutdown.
	HealthUnchanged HealthEffect = iota
	// HealthSuccess: an ok or partial run; the login worked.
	HealthSuccess
	// HealthFailure: a failed run extends the failure streak.
	HealthFailure
)

// Notified states: what the notifier last announced about an account.
const (
	NotifiedOK      = "ok"
	NotifiedFailing = "failing"
)

// Health states of an account, as shown in the API and /healthz/sync.
const (
	HealthOK      = "ok"
	HealthFailing = "failing"
	HealthStale   = "stale"
)

// SyncHealth is the failure streak of one IMAP account.
type SyncHealth struct {
	AccountID     int64
	Name          string
	OwnerID       *int64
	Enabled       bool
	CreatedAt     time.Time
	FailureStreak int
	// FailingSince is the start of the current streak; nil without one.
	FailingSince  *time.Time
	LastSuccessAt *time.Time
	NotifiedState string
}

// State is the account's health: failing when the last alertAfter syncs
// failed, stale when no sync succeeded within two intervals (counted from
// the account's creation if none ever did; not checked when interval is
// zero), else ok. Disabled accounts are always ok: they are not synced.
func (h *SyncHealth) State(alertAfter int, interval time.Duration, now time.Time) string {
	switch {
	case !h.Enabled:
		return HealthOK
	case h.FailureStreak >= alertAfter:
		return HealthFailing
	case interval > 0:
		since := h.CreatedAt
		if h.LastSuccessAt != nil {
			since = *h.LastSuccessAt
		}
		if now.Sub(since) > 2*interval {
			return HealthStale
		}
	}
	return HealthOK
}

// SyncHealth returns the health of the IMAP accounts that are not removed,
// of all users or, with owner set, of one user, by account ID.
func (s *Store) SyncHealth(ctx context.Context, owner *int64) (map[int64]*SyncHealth, error) {
	rows, err := s.q.SyncHealth(ctx, owner)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]*SyncHealth, len(rows))
	for _, r := range rows {
		h := &SyncHealth{
			AccountID: r.ID, Name: r.Name, OwnerID: r.OwnerID, Enabled: r.Enabled, CreatedAt: r.CreatedAt,
			FailureStreak: int(r.FailureStreak), LastSuccessAt: r.LastSuccessAt, NotifiedState: r.NotifiedState,
		}
		if h.FailureStreak > 0 {
			h.FailingSince = r.FailingSince
		}
		out[r.ID] = h
	}
	return out, nil
}

// CountFailingByOwner returns, per owner ID, how many enabled IMAP accounts
// failed their last threshold syncs.
func (s *Store) CountFailingByOwner(ctx context.Context, threshold int) (map[int64]int64, error) {
	rows, err := s.q.CountFailingByOwner(ctx, clampInt32(threshold))
	if err != nil {
		return nil, err
	}
	out := make(map[int64]int64, len(rows))
	for _, r := range rows {
		if r.OwnerID != nil {
			out[*r.OwnerID] = r.Failing
		}
	}
	return out, nil
}

// PendingNotification is an alert or a recovery claimed for delivery.
type PendingNotification struct {
	AccountID     int64
	Account       string
	Owner         string
	FailureStreak int
	// FailingSince is the start of the streak the message is about; for a
	// recovery, of the streak that ended.
	FailingSince *time.Time
	// NotifiedState is what was last announced: "ok" for a pending alert,
	// "failing" for a pending recovery.
	NotifiedState string
	Attempts      int
	// FirstErrorAt is when the first delivery attempt failed.
	FirstErrorAt *time.Time
	LastError    string
}

// ClaimNotifications leases up to limit announcements that are due, for
// leaseFor. Until the lease expires, no other notifier claims them, and
// only the lease token can finish them.
func (s *Store) ClaimNotifications(ctx context.Context, lease []byte, leaseFor time.Duration, threshold, limit int) ([]PendingNotification, error) {
	rows, err := s.q.ClaimNotifications(ctx, db.ClaimNotificationsParams{
		Lease: lease, LeaseSeconds: leaseFor.Seconds(), Threshold: clampInt32(threshold), MaxRows: clampInt32(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]PendingNotification, 0, len(rows))
	for _, r := range rows {
		out = append(out, PendingNotification{
			AccountID: r.AccountID, Account: r.AccountName, Owner: r.OwnerName, FailureStreak: int(r.FailureStreak),
			FailingSince: r.FailingSince, NotifiedState: r.NotifiedState, Attempts: int(r.NotifyAttempts),
			FirstErrorAt: r.NotifyFirstErrorAt, LastError: r.LastError,
		})
	}
	return out, nil
}

// FinishNotification records that state was announced (or given up, with
// errMsg) and releases the lease. It returns ErrNotFound if the lease was
// lost.
func (s *Store) FinishNotification(ctx context.Context, accountID int64, lease []byte, state, errMsg string) error {
	return one(s.q.FinishNotification(ctx, db.FinishNotificationParams{
		AccountID: accountID, Lease: lease, NotifiedState: state, NotifyError: errMsg,
	}))
}

// DeferNotification records a failed delivery attempt, schedules the next
// one and releases the lease. It returns ErrNotFound if the lease was lost.
func (s *Store) DeferNotification(ctx context.Context, accountID int64, lease []byte, next time.Time, errMsg string) error {
	return one(s.q.DeferNotification(ctx, db.DeferNotificationParams{
		AccountID: accountID, Lease: lease, NextAt: next, NotifyError: errMsg,
	}))
}
