package store

import (
	"context"
	"time"

	"github.com/pklnx/mail-archive/internal/store/db"
)

// Storage alerts (#397, part 3): storage_states is the outbox of the
// quota states of the instance (GroupID nil) and of each customer group.
// Part 2's gate records a transition by setting state and state_since and
// incrementing event_seq; the notifier announces a scope while event_seq
// is above notified_seq, under a lease as for account_sync_health. A group
// removed with its row leaves nothing to announce. Transitions written
// faster than they are announced are announced once, with the state of the
// claim.

// PendingStorageNotification is a storage transition claimed for delivery.
// It holds IDs, figures and the state only.
type PendingStorageNotification struct {
	// ID is the storage_states row.
	ID int64
	// GroupID is nil for the instance.
	GroupID    *int64
	State      string
	StateSince time.Time
	// EventSeq numbers the scope's transitions; with the scope it makes
	// the event ID (StorageEventID).
	EventSeq int64
	Attempts int
	// FirstErrorAt is when the first delivery attempt failed.
	FirstErrorAt *time.Time
	// GroupQuotaBytes is the group's quota; nil for the instance and for
	// a group without one.
	GroupQuotaBytes *int64
	// UsedBytes is the scope's figure at the claim.
	UsedBytes int64
	// mailEnqueued is the last event whose alert mail was enqueued.
	mailEnqueued *string
}

// ClaimStorageNotifications leases up to limit storage transitions that
// are due, for leaseFor. Until the lease expires, no other notifier claims
// them, and only the lease token can finish them.
func (s *Store) ClaimStorageNotifications(ctx context.Context, lease []byte, leaseFor time.Duration, limit int,
	mail AlertMail[PendingStorageNotification]) ([]PendingStorageNotification, error) {
	return claimWithMail(s, ctx, mail, func(q *db.Queries) ([]PendingStorageNotification, error) {
		rows, err := q.ClaimStorageNotifications(ctx, db.ClaimStorageNotificationsParams{
			Lease: lease, LeaseSeconds: leaseFor.Seconds(), MaxRows: clampInt32(limit),
		})
		if err != nil {
			return nil, err
		}
		out := make([]PendingStorageNotification, 0, len(rows))
		for _, r := range rows {
			out = append(out, PendingStorageNotification{
				ID: r.ID, GroupID: r.GroupID, State: r.State, StateSince: r.StateSince, EventSeq: r.EventSeq,
				Attempts: int(r.NotifyAttempts), FirstErrorAt: r.NotifyFirstErrorAt,
				GroupQuotaBytes: r.GroupQuotaBytes, UsedBytes: r.UsedBytes, mailEnqueued: r.MailEnqueuedEvent,
			})
		}
		return out, nil
	}, s.markStorageMail(ctx))
}

// FinishStorageNotification records that the transitions up to seq were
// announced (or given up, with errMsg) and releases the lease. It returns
// ErrNotFound if the lease was lost or the group was removed.
func (s *Store) FinishStorageNotification(ctx context.Context, id int64, lease []byte, seq int64, errMsg string) error {
	return one(s.q.FinishStorageNotification(ctx, db.FinishStorageNotificationParams{
		ID: id, Lease: lease, EventSeq: seq, NotifyError: errMsg,
	}))
}

// DeferStorageNotification records a failed delivery attempt, schedules
// the next one and releases the lease. It returns ErrNotFound if the lease
// was lost or the group was removed.
func (s *Store) DeferStorageNotification(ctx context.Context, id int64, lease []byte, next time.Time, errMsg string) error {
	return one(s.q.DeferStorageNotification(ctx, db.DeferStorageNotificationParams{
		ID: id, Lease: lease, NextAt: next, NotifyError: errMsg,
	}))
}
