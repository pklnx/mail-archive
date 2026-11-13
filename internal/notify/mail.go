package notify

import (
	"github.com/pklnx/mail-archive/internal/store"
)

// Alert mail (#397, part 5): at each claim the notifier hands the store
// the event of every claimed transition, and the store enqueues one
// notify_mail job per recipient, once per event (store.AlertMailPayload).
// The payload holds IDs, figures and the level only; the job reads the
// names at delivery (internal/alertmail).

// mailIf returns f if on, else nil: a claim without mail.
func mailIf[T any](on bool, f store.AlertMail[T]) store.AlertMail[T] {
	if !on {
		return nil
	}
	return f
}

// healthMailEvent is the mail event of a failing or recovered sync or fill.
func healthMailEvent(kind string, p store.PendingNotification, id string, level string) *store.AlertMailEvent {
	return &store.AlertMailEvent{ID: id, Payload: store.AlertMailPayload{
		Kind: kind, Account: p.AccountID, Fill: p.FillID, Level: level, Since: p.FailingSince, Streak: p.FailureStreak,
	}}
}

// healthEvent is failing or recovered, as BuildMessage decides it.
func healthEvent(p store.PendingNotification) string {
	if p.NotifiedState == store.NotifiedFailing {
		return EventRecovered
	}
	return EventFailing
}

func syncMailEvent(p store.PendingNotification) *store.AlertMailEvent {
	ev := healthEvent(p)
	return healthMailEvent(store.AlertMailSync, p, EventID(p.AccountID, p.FailingSince, ev), ev)
}

func fillMailEvent(p store.PendingNotification) *store.AlertMailEvent {
	ev := healthEvent(p)
	return healthMailEvent(store.AlertMailFill, p, FillEventID(p.FillID, p.FailingSince, ev), ev)
}

func lossMailEvent(p store.PendingLossAlert) *store.AlertMailEvent {
	since := p.CreatedAt
	return &store.AlertMailEvent{ID: LossEventID(p.SyncRunID), Payload: store.AlertMailPayload{
		Kind: store.AlertMailLoss, Account: p.AccountID, Since: &since, Lost: p.Lost, PresentBefore: p.PresentBefore,
	}}
}

// storageMailEvent is the mail event of a storage transition, with the
// figures of the webhook's StorageEvent.
func (n *Notifier) storageMailEvent(p store.PendingStorageNotification) *store.AlertMailEvent {
	ev := BuildStorageMessage([]store.PendingStorageNotification{p}, n.StorageQuota).Storage[0]
	since := ev.Since
	pl := store.AlertMailPayload{
		Kind: store.AlertMailStorage, Group: p.GroupID, Level: ev.Level, Since: &since, UsedBytes: ev.UsedBytes,
	}
	if ev.QuotaBytes != nil {
		pl.QuotaBytes = *ev.QuotaBytes
	}
	return &store.AlertMailEvent{ID: ev.ID, Payload: pl}
}
