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

// Notifier announces accounts whose syncs keep failing, recurring fills
// whose runs keep failing, and their recovery, once each, and the storage
// quota transitions of the instance and of each group. The account's health row in the database is the
// state: a sync only updates the failure streak, and the notifier sends
// the difference to what it last announced. Leases keep two notifiers (two
// servers, or a server and `sync`) from claiming the same transition.
// Delivery is at least once: a crash after the request and before the
// state is written repeats the message with the same ID.
type Notifier struct {
	Store *store.Store
	// Webhook receives the alerts; nil without one. A claim without a
	// webhook is finished at once; mail does not depend on it.
	Webhook *Webhook
	// Mail turns on the mail pass after each webhook pass: it enqueues a
	// notify_mail job per admin with a confirmed address for every
	// transition that is due by the mail's own state, once per transition
	// (#397, part 5). The claims enqueue no mail, so webhook and mail are
	// independent: a failing webhook neither holds mail back nor adds any,
	// and the jobs run on their own.
	Mail bool
	// AlertAfter is how many failed syncs in a row make an account failing.
	AlertAfter int
	// StorageQuota is the instance quota in bytes (#397, part 2), 0
	// without one; storage alerts of the instance show it.
	StorageQuota int64
	Logger       *slog.Logger
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
// failing syncs and recoveries first, then failing recurring fills and
// theirs, then loss alerts, then storage transitions. It stops sending at
// the first failed request; the retry waits for its time. With Mail, it
// then enqueues the mail that is due by the mail's own state
// (deliverMail), also after a failed request. It returns an error only
// if the database fails.
func (n *Notifier) Deliver(ctx context.Context) error {
	if err := n.deliverWebhook(ctx); err != nil {
		return err
	}
	return n.deliverMail(ctx)
}

func (n *Notifier) deliverWebhook(ctx context.Context) error {
	for _, pass := range []func(context.Context) (int, bool, error){n.pass, n.passFill, n.passLoss, n.passStorage} {
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

// deliverMail enqueues, in batches of BatchSize, the alert mail that is
// due whatever the webhook's delivery state: a recovery while the webhook
// still fails to announce the alert, or a transition while it waits for
// its retry. It is the only path that enqueues alert mail; the mail's own
// state (store.EnqueueDueSyncMail) and mail_enqueued_event keep it to one mail
// per transition and recipient.
func (n *Notifier) deliverMail(ctx context.Context) error {
	if !n.Mail {
		return nil
	}
	for _, pass := range []func(context.Context) (int, error){
		func(ctx context.Context) (int, error) {
			return n.Store.EnqueueDueSyncMail(ctx, n.AlertAfter, BatchSize, syncMailEvent)
		},
		func(ctx context.Context) (int, error) {
			return n.Store.EnqueueDueFillMail(ctx, n.AlertAfter, BatchSize, fillMailEvent)
		},
		func(ctx context.Context) (int, error) {
			return n.Store.EnqueueDueLossMail(ctx, store.LossAlertMaxAge, BatchSize, lossMailEvent)
		},
		func(ctx context.Context) (int, error) {
			return n.Store.EnqueueDueStorageMail(ctx, BatchSize, n.storageMailEvent)
		},
	} {
		for ctx.Err() == nil {
			handled, err := pass(ctx)
			if err != nil {
				return err
			}
			if handled < BatchSize {
				break
			}
		}
	}
	return nil
}

// healthKind is what pass announces: accounts whose syncs fail, or
// recurring fills whose runs fail.
type healthKind struct {
	claim    func(ctx context.Context, lease []byte, leaseFor time.Duration, threshold, limit int) ([]store.PendingNotification, error)
	build    func([]store.PendingNotification, bool) *Message
	finish   func(ctx context.Context, id int64, lease []byte, state, errMsg string) error
	postpone func(ctx context.Context, id int64, lease []byte, next time.Time, errMsg string) error
	id       func(store.PendingNotification) int64
}

// pass claims and sends one batch of failing and recovered syncs. ok is
// false when the request failed.
func (n *Notifier) pass(ctx context.Context) (claimed int, ok bool, err error) {
	return n.passHealth(ctx, healthKind{
		claim: n.Store.ClaimNotifications, build: BuildMessage,
		finish: n.Store.FinishNotification, postpone: n.Store.DeferNotification,
		id: func(p store.PendingNotification) int64 { return p.AccountID },
	})
}

// passFill claims and sends one batch of failing and recovered recurring
// fills.
func (n *Notifier) passFill(ctx context.Context) (claimed int, ok bool, err error) {
	return n.passHealth(ctx, healthKind{
		claim: n.Store.ClaimFillNotifications, build: BuildFillMessage,
		finish: n.Store.FinishFillNotification, postpone: n.Store.DeferFillNotification,
		id: func(p store.PendingNotification) int64 { return p.FillID },
	})
}

func (n *Notifier) passHealth(ctx context.Context, k healthKind) (claimed int, ok bool, err error) {
	lease := make([]byte, 16)
	_, _ = rand.Read(lease)
	pending, err := k.claim(ctx, lease, LeaseDuration, n.AlertAfter, BatchSize)
	if err != nil || len(pending) == 0 {
		return 0, true, err
	}
	msg := k.build(pending, n.Webhook.Full())
	status, sendErr := n.send(ctx, msg)
	// Record the outcome even if ctx was cancelled during the request.
	bg := context.WithoutCancel(ctx)
	log := n.logger()
	if sendErr == nil {
		for i, p := range pending {
			ev := msg.Accounts[i]
			if err := k.finish(bg, k.id(p), lease, targetState(ev.Event), ""); err != nil {
				return len(pending), true, n.lostLease(p, err)
			}
			what := "alert sent"
			if ev.Event == EventRecovered {
				what = "recovery sent"
			}
			if n.Webhook == nil {
				what = "alert recorded without a webhook"
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
			if err := k.finish(bg, k.id(p), lease, targetState(ev.Event), de.Error()); err != nil {
				return len(pending), false, n.lostLease(p, err)
			}
			log.Error("notification given up", "account", p.Account, "owner", p.Owner, "attempts", p.Attempts+1,
				"event", ev.Event, "id", ev.ID)
			continue
		}
		next := now.Add(RetryDelay(p.Attempts, de))
		if err := k.postpone(bg, k.id(p), lease, next, de.Error()); err != nil {
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

// BuildMessage turns claimed announcements into one message. Without full
// (the webhook's default detail, config.WebhookDetailIDs) it names the
// accounts by ID only: no account or owner name and no error text, and
// title and body are fixed text with IDs.
func BuildMessage(pending []store.PendingNotification, full bool) *Message {
	m := &Message{Accounts: make([]AccountEvent, 0, len(pending))}
	var d draft
	var failing, recovered int
	var ids []string
	for _, p := range pending {
		ev := AccountEvent{
			Event: EventFailing, AccountID: p.AccountID,
			FailureStreak: p.FailureStreak, FailingSince: p.FailingSince,
		}
		if full {
			ev.Account, ev.Owner, ev.LastError = p.Account, p.Owner, truncate(p.LastError, maxErrorLen)
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
		d.entries = append(d.entries, describe(ev, full))
	}
	switch {
	case failing > 0 && recovered > 0:
		m.Event = EventMixed
		d.title = []piece{constant(fmt.Sprintf("Mail archive: %s failing, %d recovered", plural(failing, "account"), recovered))}
	case failing > 0:
		m.Event = EventFailing
		switch {
		case failing == 1 && full:
			d.title = []piece{constant("Mail archive: sync of "), value(m.Accounts[0].Account), constant(" keeps failing")}
		case failing == 1:
			d.title = []piece{constant(fmt.Sprintf("Mail archive: sync of account %d keeps failing", m.Accounts[0].AccountID))}
		default:
			d.title = []piece{constant(fmt.Sprintf("Mail archive: syncs of %d accounts keep failing", failing))}
		}
	default:
		m.Event = EventRecovered
		switch {
		case recovered == 1 && full:
			d.title = []piece{constant("Mail archive: "), value(m.Accounts[0].Account), constant(" syncs again")}
		case recovered == 1:
			d.title = []piece{constant(fmt.Sprintf("Mail archive: account %d syncs again", m.Accounts[0].AccountID))}
		default:
			d.title = []piece{constant(fmt.Sprintf("Mail archive: %d accounts sync again", recovered))}
		}
	}
	if len(ids) == 1 {
		m.ID = ids[0]
	} else {
		sum := sha256.Sum256([]byte(strings.Join(ids, ",")))
		m.ID = "batch-" + hex.EncodeToString(sum[:8])
	}
	d.apply(m)
	return m
}

// FillEventID is the stable ID of one transition of a recurring fill: the
// fill, the start of its streak and the event.
func FillEventID(fillID int64, failingSince *time.Time, event string) string {
	return "fill-" + EventID(fillID, failingSince, event)
}

// BuildFillMessage turns claimed announcements of recurring fills into one
// message, like BuildMessage; without full it names no mailbox either.
func BuildFillMessage(pending []store.PendingNotification, full bool) *Message {
	m := &Message{Accounts: make([]AccountEvent, 0, len(pending))}
	var d draft
	var failing, recovered int
	var ids []string
	for _, p := range pending {
		ev := AccountEvent{
			Event: EventFailing, AccountID: p.AccountID, FillID: p.FillID,
			FailureStreak: p.FailureStreak, FailingSince: p.FailingSince,
		}
		if full {
			ev.Account, ev.Owner, ev.Mailbox, ev.LastError = p.Account, p.Owner, p.Mailbox, truncate(p.LastError, maxErrorLen)
		}
		if p.NotifiedState == store.NotifiedFailing {
			ev.Event, ev.LastError = EventRecovered, ""
			recovered++
		} else {
			failing++
		}
		ev.ID = FillEventID(p.FillID, p.FailingSince, ev.Event)
		m.Accounts = append(m.Accounts, ev)
		ids = append(ids, ev.ID)
		d.entries = append(d.entries, describeFill(ev, full))
	}
	first := m.Accounts[0]
	switch {
	case failing > 0 && recovered > 0:
		m.Event = EventMixed
		d.title = []piece{constant(fmt.Sprintf("Mail archive: %s failing, %d recovered", plural(failing, "import"), recovered))}
	case failing > 0:
		m.Event = EventFailing
		switch {
		case failing == 1 && full:
			d.title = []piece{constant("Mail archive: import of "), value(first.Mailbox), constant(" into "), value(first.Account), constant(" keeps failing")}
		case failing == 1:
			d.title = []piece{constant(fmt.Sprintf("Mail archive: import of fill %d into account %d keeps failing", first.FillID, first.AccountID))}
		default:
			d.title = []piece{constant(fmt.Sprintf("Mail archive: imports of %d mailboxes keep failing", failing))}
		}
	default:
		m.Event = EventRecovered
		switch {
		case recovered == 1 && full:
			d.title = []piece{constant("Mail archive: "), value(first.Mailbox), constant(" imports into "), value(first.Account), constant(" again")}
		case recovered == 1:
			d.title = []piece{constant(fmt.Sprintf("Mail archive: fill %d imports into account %d again", first.FillID, first.AccountID))}
		default:
			d.title = []piece{constant(fmt.Sprintf("Mail archive: %d mailboxes import again", recovered))}
		}
	}
	if len(ids) == 1 {
		m.ID = ids[0]
	} else {
		sum := sha256.Sum256([]byte(strings.Join(ids, ",")))
		m.ID = "batch-" + hex.EncodeToString(sum[:8])
	}
	d.apply(m)
	return m
}

func describeFill(ev AccountEvent, full bool) []piece {
	who := []piece{constant(fmt.Sprintf("Import of fill %d into account %d", ev.FillID, ev.AccountID))}
	if full {
		who = []piece{constant("Import of "), value(ev.Mailbox), constant(" into account "), quoted(ev.Account),
			constant(" (owner "), owner(ev.Owner), constant(fmt.Sprintf(", fill %d)", ev.FillID))}
	}
	return describeHealth(ev, who, "ran successfully again", "run")
}

func describe(ev AccountEvent, full bool) []piece {
	who := []piece{constant(fmt.Sprintf("Account %d", ev.AccountID))}
	if full {
		who = []piece{constant("Account "), quoted(ev.Account), constant(" (owner "), owner(ev.Owner),
			constant(fmt.Sprintf(", ID %d)", ev.AccountID))}
	}
	return describeHealth(ev, who, "synced successfully again", "sync")
}

// describeHealth is the entry of a failing or recovered sync or fill: who,
// what happened since when, and the last error.
func describeHealth(ev AccountEvent, who []piece, again, unit string) []piece {
	since := ""
	if ev.FailingSince != nil {
		since = " since " + ev.FailingSince.UTC().Format("2006-01-02 15:04 UTC")
	}
	if ev.Event == EventRecovered {
		return append(who, constant(" "+again+" after failing"+since+"."))
	}
	s := append(who, constant(fmt.Sprintf(" failed %s in a row%s.", plural(ev.FailureStreak, unit), since)))
	if ev.LastError != "" {
		s = append(s, constant("\nLast error: "), value(ev.LastError))
	}
	return s
}

// owner is the owner's name as a value, or "-" for none.
func owner(name string) piece {
	if name == "" {
		return constant("-")
	}
	return value(name)
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return thousands(n) + " " + word + "s"
}

// send posts msg to the webhook; without one there is nothing to send and
// the claim is finished at once (mail follows in the mail pass).
func (n *Notifier) send(ctx context.Context, msg *Message) (int, error) {
	if n.Webhook == nil {
		return 0, nil
	}
	return n.Webhook.Send(ctx, msg)
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
	msg := BuildLossMessage(pending, n.Webhook.Full())
	status, sendErr := n.send(ctx, msg)
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

// BuildLossMessage turns claimed loss alerts into one message; without
// full it names the accounts by ID only, and no folders.
func BuildLossMessage(pending []store.PendingLossAlert, full bool) *Message {
	m := &Message{Event: EventGone, Accounts: make([]AccountEvent, 0, len(pending))}
	var d draft
	var ids []string
	total := 0
	for _, p := range pending {
		ev := AccountEvent{
			ID: LossEventID(p.SyncRunID), Event: EventGone, AccountID: p.AccountID,
			Lost: p.Lost, PresentBefore: p.PresentBefore,
		}
		if full {
			ev.Account, ev.Owner, ev.MoreFolders = p.Account, p.Owner, p.MoreFolders
			for _, f := range p.Folders {
				ev.Folders = append(ev.Folders, FolderLoss{Name: f.Name, Lost: f.Lost})
			}
		}
		total += p.Lost
		m.Accounts = append(m.Accounts, ev)
		ids = append(ids, ev.ID)
		d.entries = append(d.entries, describeLoss(ev, full))
	}
	switch {
	case len(pending) == 1 && full:
		d.title = []piece{constant(fmt.Sprintf("Mail archive: %s deleted on the server (", plural(total, "message"))),
			value(m.Accounts[0].Account), constant(")")}
		m.ID = ids[0]
	case len(pending) == 1:
		d.title = []piece{constant(fmt.Sprintf("Mail archive: %s deleted on the server (account %d)", plural(total, "message"), m.Accounts[0].AccountID))}
		m.ID = ids[0]
	default:
		d.title = []piece{constant(fmt.Sprintf("Mail archive: %s deleted on the server in %d accounts", plural(total, "message"), len(pending)))}
		sum := sha256.Sum256([]byte(strings.Join(ids, ",")))
		m.ID = "batch-" + hex.EncodeToString(sum[:8])
	}
	d.apply(m)
	return m
}

func describeLoss(ev AccountEvent, full bool) []piece {
	what := fmt.Sprintf("%s of %s are no longer on the server. They stay in the archive.",
		thousands(ev.Lost), plural(ev.PresentBefore, "message"))
	if !full {
		return []piece{constant(fmt.Sprintf("Account %d: %s", ev.AccountID, what))}
	}
	s := []piece{constant("Account "), quoted(ev.Account), constant(" (owner "), owner(ev.Owner),
		constant(fmt.Sprintf(", ID %d): %s", ev.AccountID, what))}
	if len(ev.Folders) > 0 {
		s = append(s, constant("\nFolders: "))
		for i, f := range ev.Folders {
			if i > 0 {
				s = append(s, constant(", "))
			}
			s = append(s, value(f.Name), constant(" "+thousands(f.Lost)))
		}
		if ev.MoreFolders > 0 {
			s = append(s, constant(fmt.Sprintf(" and %d more", ev.MoreFolders)))
		}
		s = append(s, constant("."))
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
