package notify

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/pklnx/mail-archive/internal/store"
)

// Storage levels: the quota states of a scope (#397). LevelOK is the
// return below the warning level.
const (
	LevelOK       = "ok"
	LevelWarn     = "warn"
	LevelFull     = "full"
	LevelOverflow = "overflow"
	LevelDiskLow  = "disk_low"
)

// Storage scopes.
const (
	ScopeInstance = "instance"
	ScopeGroup    = "group"
)

// StorageEvent is the part of a message about one scope's storage
// transition. It holds IDs, figures and the level only, in every detail
// mode: group names reach the admins by mail (#397, part 5), never the
// webhook.
type StorageEvent struct {
	// ID is stable for one transition, so receivers can drop repeats.
	ID    string `json:"id"`
	Event string `json:"event"`
	// Level is the scope's state: ok, warn, full, overflow or disk_low.
	Level string `json:"level"`
	// Scope is instance or group; GroupID is null for the instance.
	Scope   string `json:"scope"`
	GroupID *int64 `json:"groupId"`
	// UsedBytes is the scope's message bytes after deduplication.
	UsedBytes int64 `json:"usedBytes"`
	// QuotaBytes and Percent are null without a quota.
	QuotaBytes *int64   `json:"quotaBytes"`
	Percent    *float64 `json:"percent"`
	// Since is when the scope entered the level.
	Since time.Time `json:"since"`
}

// StorageEventID is the stable ID of one storage transition: the scope and
// its transition number (storage_states.event_seq), so warn, ok and warn
// again give three IDs.
func StorageEventID(groupID *int64, seq int64) string {
	scope := ScopeInstance
	if groupID != nil {
		scope = strconv.FormatInt(*groupID, 10)
	}
	return fmt.Sprintf("storage:%s:%d", scope, seq)
}

// passStorage claims and sends one batch of storage transitions. ok is
// false when the request failed.
func (n *Notifier) passStorage(ctx context.Context) (claimed int, ok bool, err error) {
	lease := make([]byte, 16)
	_, _ = rand.Read(lease)
	pending, err := n.Store.ClaimStorageNotifications(ctx, lease, LeaseDuration, BatchSize, mailIf(n.Mail, n.storageMailEvent))
	if err != nil || len(pending) == 0 {
		return 0, true, err
	}
	msg := BuildStorageMessage(pending, n.StorageQuota)
	status, sendErr := n.send(ctx, msg)
	bg := context.WithoutCancel(ctx)
	log := n.logger()
	if sendErr == nil {
		for i, p := range pending {
			if err := n.Store.FinishStorageNotification(bg, p.ID, lease, p.EventSeq, ""); err != nil {
				return len(pending), true, n.lostStorageLease(err)
			}
			ev := msg.Storage[i]
			log.Info("storage alert sent", "scope", ev.Scope, "group_id", groupAttr(ev.GroupID), "level", ev.Level,
				"attempts", p.Attempts+1, "id", ev.ID)
		}
		return len(pending), true, nil
	}

	var de *DeliveryError
	if !errors.As(sendErr, &de) {
		de = &DeliveryError{Err: sendErr}
	}
	if de.Permanent() {
		log.Error("notification rejected; check the webhook settings", "target", n.Webhook.Target(), "status", de.Status)
	} else {
		log.Warn("notification failed", "target", n.Webhook.Target(), "status", status, "err", de.Error())
	}
	now := n.now()
	for i, p := range pending {
		ev := msg.Storage[i]
		if p.FirstErrorAt != nil && now.Sub(*p.FirstErrorAt) >= GiveUpAfter {
			if err := n.Store.FinishStorageNotification(bg, p.ID, lease, p.EventSeq, de.Error()); err != nil {
				return len(pending), false, n.lostStorageLease(err)
			}
			log.Error("notification given up", "scope", ev.Scope, "group_id", groupAttr(ev.GroupID), "attempts", p.Attempts+1,
				"event", EventStorage, "id", ev.ID)
			continue
		}
		next := now.Add(RetryDelay(p.Attempts, de))
		if err := n.Store.DeferStorageNotification(bg, p.ID, lease, next, de.Error()); err != nil {
			return len(pending), false, n.lostStorageLease(err)
		}
	}
	return len(pending), false, nil
}

// lostStorageLease reports a lease that expired during a request, or a
// group removed meanwhile: another notifier may send the message again,
// which the message ID covers.
func (n *Notifier) lostStorageLease(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		n.logger().Warn("storage notification lease expired or group removed; the message may be sent twice")
		return nil
	}
	return err
}

// groupAttr is a group ID for logs, "instance" for none.
func groupAttr(id *int64) string {
	if id == nil {
		return ScopeInstance
	}
	return strconv.FormatInt(*id, 10)
}

// BuildStorageMessage turns claimed storage transitions into one message.
// instanceQuota is the instance quota, 0 without one. The message names
// groups by ID only.
func BuildStorageMessage(pending []store.PendingStorageNotification, instanceQuota int64) *Message {
	m := &Message{Event: EventStorage, Storage: make([]StorageEvent, 0, len(pending))}
	d := draft{more: "storage"}
	var ids []string
	for _, p := range pending {
		ev := StorageEvent{
			ID: StorageEventID(p.GroupID, p.EventSeq), Event: EventStorage, Level: p.State, Scope: ScopeInstance,
			GroupID: p.GroupID, UsedBytes: p.UsedBytes, Since: p.StateSince,
		}
		quota := instanceQuota
		if p.GroupID != nil {
			ev.Scope, quota = ScopeGroup, 0
			if p.GroupQuotaBytes != nil {
				quota = *p.GroupQuotaBytes
			}
		}
		if quota > 0 {
			pct := math.Floor(float64(p.UsedBytes)*1000/float64(quota)) / 10
			ev.QuotaBytes, ev.Percent = &quota, &pct
		}
		m.Storage = append(m.Storage, ev)
		ids = append(ids, ev.ID)
		d.entries = append(d.entries, []piece{constant(describeStorage(ev))})
	}
	if len(pending) == 1 {
		d.title = []piece{constant("Mail archive: " + storageHeadline(m.Storage[0]))}
		m.ID = ids[0]
	} else {
		d.title = []piece{constant(fmt.Sprintf("Mail archive: storage changed in %d scopes", len(pending)))}
		sum := sha256.Sum256([]byte(strings.Join(ids, ",")))
		m.ID = "batch-" + hex.EncodeToString(sum[:8])
	}
	d.apply(m)
	return m
}

// scopeName is "storage" or "storage of group N".
func scopeName(ev StorageEvent) string {
	if ev.GroupID != nil {
		return fmt.Sprintf("storage of group %d", *ev.GroupID)
	}
	return "storage"
}

// storageHeadline is the title of a message about one transition, such as
// "storage at 82% of 50 GiB".
func storageHeadline(ev StorageEvent) string {
	what := scopeName(ev)
	switch {
	case ev.Level == LevelDiskLow:
		return "data disk below its free-space reserve"
	case ev.Level == LevelOK && ev.Percent != nil:
		return fmt.Sprintf("%s back to normal, at %s of %s", what, percent(*ev.Percent), humanBytes(*ev.QuotaBytes))
	case ev.Level == LevelOK:
		return what + " back to normal"
	case ev.Percent != nil:
		return fmt.Sprintf("%s at %s of %s", what, percent(*ev.Percent), humanBytes(*ev.QuotaBytes))
	}
	return fmt.Sprintf("%s %s", what, ev.Level)
}

// describeStorage is the entry of one transition.
func describeStorage(ev StorageEvent) string {
	what := "Storage of the instance"
	if ev.GroupID != nil {
		what = fmt.Sprintf("Storage of group %d", *ev.GroupID)
	}
	s := fmt.Sprintf("%s: %s since %s, %s used", what, ev.Level, ev.Since.UTC().Format("2006-01-02 15:04 UTC"), humanBytes(ev.UsedBytes))
	if ev.QuotaBytes != nil {
		s += fmt.Sprintf(" of %s (%s)", humanBytes(*ev.QuotaBytes), percent(*ev.Percent))
	}
	switch ev.Level {
	case LevelFull:
		s += ". New mail from IMAP syncs and imports is not archived; SMTP journal copies are still accepted up to the overflow."
	case LevelOverflow:
		s += ". New mail is not archived; SMTP journal copies are deferred with 452."
	case LevelDiskLow:
		s += ". The data disk is below its free-space reserve; new mail is not archived."
	default:
		s += "."
	}
	return s
}

func percent(p float64) string { return strconv.FormatFloat(p, 'f', -1, 64) + "%" }

// humanBytes writes n in binary units with one decimal, such as "1.5 GiB".
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}
