package notify

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pklnx/mail-archive/internal/store"
)

// Delivery limits.
const (
	// BatchSize is the most accounts in one message; the rest follow in
	// the next request.
	BatchSize = 50
	// LeaseDuration is how long a claimed announcement belongs to one
	// notifier. It must outlast a request.
	LeaseDuration = 2 * time.Minute
	// FirstRetry is the delay after the first failed attempt; it doubles up
	// to MaxRetry.
	FirstRetry = 30 * time.Second
	MaxRetry   = time.Hour
	// GiveUpAfter is how long an announcement is retried.
	GiveUpAfter = 24 * time.Hour
	// DefaultCheckEvery is how often Run looks for due announcements.
	DefaultCheckEvery = 30 * time.Second
	// maxErrorLen limits the sync error quoted in a message.
	maxErrorLen = 200
)

// Notifier announces accounts whose syncs keep failing, and their
// recovery, once each. The account's health row in the database is the
// state: a sync only updates the failure streak, and the notifier sends
// the difference to what it last announced. Leases keep two notifiers (two
// servers, or a server and `sync`) from claiming the same transition.
// Delivery is at least once: a crash after the request and before the
// state is written repeats the message with the same ID.
type Notifier struct {
	Store   *store.Store
	Webhook *Webhook
	// AlertAfter is how many failed syncs in a row make an account failing.
	AlertAfter int
	Logger     *slog.Logger
	// CheckEvery is how often Run looks for due announcements (default
	// DefaultCheckEvery).
	CheckEvery time.Duration
	// Now is injectable for tests.
	Now func() time.Time

	once sync.Once
	wake chan struct{}
}

func (n *Notifier) wakeChan() chan struct{} {
	n.once.Do(func() { n.wake = make(chan struct{}, 1) })
	return n.wake
}

// Wake makes Run check now, for example after a sync.
func (n *Notifier) Wake() {
	select {
	case n.wakeChan() <- struct{}{}:
	default:
	}
}

// Run delivers announcements until ctx is cancelled.
func (n *Notifier) Run(ctx context.Context) {
	wake := n.wakeChan()
	every := n.CheckEvery
	if every <= 0 {
		every = DefaultCheckEvery
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		if err := n.Deliver(ctx); err != nil && ctx.Err() == nil {
			n.logger().Error("deliver notifications", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-tick.C:
		}
	}
}

// Deliver sends every announcement that is due, in batches of BatchSize:
// failing syncs and recoveries first, then loss alerts. It stops at the
// first failed request; the retry waits for its time. It returns an error
// only if the database fails.
func (n *Notifier) Deliver(ctx context.Context) error {
	for _, pass := range []func(context.Context) (int, bool, error){n.pass, n.passLoss} {
		for ctx.Err() == nil {
			claimed, ok, err := pass(ctx)
			if err != nil || !ok {
				return err
			}
			if claimed < BatchSize {
				break
			}
		}
	}
	return nil
}

// pass claims and sends one batch. ok is false when the request failed.
func (n *Notifier) pass(ctx context.Context) (claimed int, ok bool, err error) {
	lease := make([]byte, 16)
	_, _ = rand.Read(lease)
	pending, err := n.Store.ClaimNotifications(ctx, lease, LeaseDuration, n.AlertAfter, BatchSize)
	if err != nil || len(pending) == 0 {
		return 0, true, err
	}
	msg := BuildMessage(pending)
	status, sendErr := n.Webhook.Send(ctx, msg)
	// Record the outcome even if ctx was cancelled during the request.
	bg := context.WithoutCancel(ctx)
	log := n.logger()
	if sendErr == nil {
		for i, p := range pending {
			ev := msg.Accounts[i]
			if err := n.Store.FinishNotification(bg, p.AccountID, lease, targetState(ev.Event), ""); err != nil {
				return len(pending), true, n.lostLease(p, err)
			}
			what := "alert sent"
			if ev.Event == EventRecovered {
				what = "recovery sent"
			}
			log.Info(what, "account", p.Account, "owner", p.Owner, "attempts", p.Attempts+1, "id", ev.ID)
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
		ev := msg.Accounts[i]
		if p.FirstErrorAt != nil && now.Sub(*p.FirstErrorAt) >= GiveUpAfter {
			if err := n.Store.FinishNotification(bg, p.AccountID, lease, targetState(ev.Event), de.Error()); err != nil {
				return len(pending), false, n.lostLease(p, err)
			}
			log.Error("notification given up", "account", p.Account, "owner", p.Owner, "attempts", p.Attempts+1,
				"event", ev.Event, "id", ev.ID)
			continue
		}
		next := now.Add(RetryDelay(p.Attempts, de))
		if err := n.Store.DeferNotification(bg, p.AccountID, lease, next, de.Error()); err != nil {
			return len(pending), false, n.lostLease(p, err)
		}
	}
	return len(pending), false, nil
}

// lostLease reports a lease that expired during a request: another
// notifier may send the message again, which the message ID covers.
func (n *Notifier) lostLease(p store.PendingNotification, err error) error {
	if errors.Is(err, store.ErrNotFound) {
		n.logger().Warn("notification lease expired; the message may be sent twice", "account", p.Account)
		return nil
	}
	return err
}

// RetryDelay is the wait after a failed attempt, given the attempts that
// failed before it: FirstRetry doubling up to MaxRetry, at least the
// server's Retry-After (up to MaxRetry), and MaxRetry for answers that mean
// a configuration error.
func RetryDelay(previous int, de *DeliveryError) time.Duration {
	if de != nil && de.Permanent() {
		return MaxRetry
	}
	d := MaxRetry
	if previous < 20 {
		d = min(FirstRetry<<previous, MaxRetry)
	}
	if de != nil && de.RetryAfter > d {
		d = min(de.RetryAfter, MaxRetry)
	}
	return d
}

func targetState(event string) string {
	if event == EventRecovered {
		return store.NotifiedOK
	}
	return store.NotifiedFailing
}

// EventID is the stable ID of one transition: the account, the start of
// its streak and the event.
func EventID(accountID int64, failingSince *time.Time, event string) string {
	var since int64
	if failingSince != nil {
		since = failingSince.Unix()
	}
	return fmt.Sprintf("%d-%d-%s", accountID, since, event)
}

// BuildMessage turns claimed announcements into one message.
func BuildMessage(pending []store.PendingNotification) *Message {
	m := &Message{Accounts: make([]AccountEvent, 0, len(pending))}
	var failing, recovered int
	var lines, ids []string
	for _, p := range pending {
		ev := AccountEvent{
			Event: EventFailing, AccountID: p.AccountID, Account: p.Account, Owner: p.Owner,
			FailureStreak: p.FailureStreak, FailingSince: p.FailingSince, LastError: truncate(p.LastError, maxErrorLen),
		}
		if p.NotifiedState == store.NotifiedFailing {
			ev.Event, ev.LastError = EventRecovered, ""
			recovered++
		} else {
			failing++
		}
		ev.ID = EventID(p.AccountID, p.FailingSince, ev.Event)
		m.Accounts = append(m.Accounts, ev)
		ids = append(ids, ev.ID)
		lines = append(lines, describe(ev))
	}
	switch {
	case failing > 0 && recovered > 0:
		m.Event = EventMixed
		m.Title = fmt.Sprintf("Mail archive: %s failing, %d recovered", plural(failing, "account"), recovered)
	case failing > 0:
		m.Event = EventFailing
		if failing == 1 {
			m.Title = "Mail archive: sync of " + m.Accounts[0].Account + " keeps failing"
		} else {
			m.Title = fmt.Sprintf("Mail archive: syncs of %d accounts keep failing", failing)
		}
	default:
		m.Event = EventRecovered
		if recovered == 1 {
			m.Title = "Mail archive: " + m.Accounts[0].Account + " syncs again"
		} else {
			m.Title = fmt.Sprintf("Mail archive: %d accounts sync again", recovered)
		}
	}
	if len(ids) == 1 {
		m.ID = ids[0]
	} else {
		sum := sha256.Sum256([]byte(strings.Join(ids, ",")))
		m.ID = "batch-" + hex.EncodeToString(sum[:8])
	}
	m.Body = strings.Join(lines, "\n\n")
	return m
}

func describe(ev AccountEvent) string {
	who := fmt.Sprintf("Account %q (owner %s, ID %d)", ev.Account, orDash(ev.Owner), ev.AccountID)
	since := ""
	if ev.FailingSince != nil {
		since = " since " + ev.FailingSince.UTC().Format("2006-01-02 15:04 UTC")
	}
	if ev.Event == EventRecovered {
		return who + " synced successfully again after failing" + since + "."
	}
	s := fmt.Sprintf("%s failed %s in a row%s.", who, plural(ev.FailureStreak, "sync"), since)
	if ev.LastError != "" {
		s += "\nLast error: " + ev.LastError
	}
	return s
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return thousands(n) + " " + word + "s"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (n *Notifier) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}

func (n *Notifier) logger() *slog.Logger {
	if n.Logger != nil {
		return n.Logger
	}
	return slog.Default()
}

// passLoss claims and sends one batch of loss alerts. ok is false when the
// request failed.
func (n *Notifier) passLoss(ctx context.Context) (claimed int, ok bool, err error) {
	lease := make([]byte, 16)
	_, _ = rand.Read(lease)
	pending, err := n.Store.ClaimLossAlerts(ctx, lease, LeaseDuration, store.LossAlertMaxAge, BatchSize)
	if err != nil || len(pending) == 0 {
		return 0, true, err
	}
	msg := BuildLossMessage(pending)
	status, sendErr := n.Webhook.Send(ctx, msg)
	bg := context.WithoutCancel(ctx)
	log := n.logger()
	if sendErr == nil {
		for i, p := range pending {
			if err := n.Store.FinishLossAlert(bg, p.ID, lease, false, ""); err != nil {
				return len(pending), true, n.lostLossLease(p, err)
			}
			log.Info("loss alert sent", "account", p.Account, "owner", p.Owner, "lost", p.Lost,
				"attempts", p.Attempts+1, "id", msg.Accounts[i].ID)
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
		// A loss alert is about one run: past the age limit it is no news.
		if now.Sub(p.CreatedAt) >= store.LossAlertMaxAge {
			if err := n.Store.FinishLossAlert(bg, p.ID, lease, true, de.Error()); err != nil {
				return len(pending), false, n.lostLossLease(p, err)
			}
			log.Error("notification given up", "account", p.Account, "owner", p.Owner, "attempts", p.Attempts+1,
				"event", EventGone, "id", msg.Accounts[i].ID)
			continue
		}
		next := now.Add(RetryDelay(p.Attempts, de))
		if err := n.Store.DeferLossAlert(bg, p.ID, lease, next, de.Error()); err != nil {
			return len(pending), false, n.lostLossLease(p, err)
		}
	}
	return len(pending), false, nil
}

func (n *Notifier) lostLossLease(p store.PendingLossAlert, err error) error {
	if errors.Is(err, store.ErrNotFound) {
		n.logger().Warn("notification lease expired; the message may be sent twice", "account", p.Account)
		return nil
	}
	return err
}

// LossEventID is the stable ID of one loss alert: the sync run that found
// the loss.
func LossEventID(syncRunID int64) string {
	return fmt.Sprintf("gone-%d", syncRunID)
}

// BuildLossMessage turns claimed loss alerts into one message.
func BuildLossMessage(pending []store.PendingLossAlert) *Message {
	m := &Message{Event: EventGone, Accounts: make([]AccountEvent, 0, len(pending))}
	var lines, ids []string
	total := 0
	for _, p := range pending {
		ev := AccountEvent{
			ID: LossEventID(p.SyncRunID), Event: EventGone, AccountID: p.AccountID, Account: p.Account, Owner: p.Owner,
			Lost: p.Lost, PresentBefore: p.PresentBefore, MoreFolders: p.MoreFolders,
		}
		for _, f := range p.Folders {
			ev.Folders = append(ev.Folders, FolderLoss{Name: f.Name, Lost: f.Lost})
		}
		total += p.Lost
		m.Accounts = append(m.Accounts, ev)
		ids = append(ids, ev.ID)
		lines = append(lines, describeLoss(ev))
	}
	if len(pending) == 1 {
		m.Title = fmt.Sprintf("Mail archive: %s deleted on the server (%s)", plural(total, "message"), m.Accounts[0].Account)
		m.ID = ids[0]
	} else {
		m.Title = fmt.Sprintf("Mail archive: %s deleted on the server in %d accounts", plural(total, "message"), len(pending))
		sum := sha256.Sum256([]byte(strings.Join(ids, ",")))
		m.ID = "batch-" + hex.EncodeToString(sum[:8])
	}
	m.Body = strings.Join(lines, "\n\n")
	return m
}

func describeLoss(ev AccountEvent) string {
	s := fmt.Sprintf("Account %q (owner %s, ID %d): %s of %s are no longer on the server. They stay in the archive.",
		ev.Account, orDash(ev.Owner), ev.AccountID, thousands(ev.Lost), plural(ev.PresentBefore, "message"))
	if len(ev.Folders) > 0 {
		parts := make([]string, 0, len(ev.Folders))
		for _, f := range ev.Folders {
			parts = append(parts, f.Name+" "+thousands(f.Lost))
		}
		s += "\nFolders: " + strings.Join(parts, ", ")
		if ev.MoreFolders > 0 {
			s += fmt.Sprintf(" and %d more", ev.MoreFolders)
		}
		s += "."
	}
	return s
}

// thousands writes n with commas: 1234567 is "1,234,567".
func thousands(n int) string {
	if n < 0 {
		return "-" + thousands(-n)
	}
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
