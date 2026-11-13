package store

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/pklnx/mail-archive/internal/store/db"
)

// Email alerts for admins (#397, part 5). When the notifier claims a
// transition, it enqueues one notify_mail job per recipient in the same
// transaction: admins with a confirmed notification address who are not
// locked. The outbox row remembers the event in mail_enqueued_event, so a
// webhook retry claims the same event again and enqueues nothing, and the
// enqueue is insert-only, so a done mail is never sent again. The job
// checks the recipient again at delivery (AlertMailRecipient).
//
// Mail keeps its own state beside the webhook's delivery state:
// mailed_state for syncs and fills, mailed_seq for storage, and
// mail_enqueued_event for loss alerts. After its webhook claims, the
// notifier enqueues the mail that is due by that state alone
// (EnqueueDue*Mail), so a webhook that fails or waits for its retry holds
// no mail back.

// JobNotifyMail is the job kind of one alert mail to one admin.
const JobNotifyMail = "notify_mail"

// NotifyMailMaxAttempts is how often an alert mail is tried.
const NotifyMailMaxAttempts = 10

// Notification address limits (Profile → Notifications).
const (
	// NotifyCodeTTL is how long a confirmation code is valid.
	NotifyCodeTTL = 30 * time.Minute
	// NotifyCodeMaxTries is the most codes entered per code.
	NotifyCodeMaxTries = 5
	// NotifyCodesPerHour is the most codes sent per user and hour.
	NotifyCodesPerHour = 3
)

// Alert mail kinds: what an AlertMailPayload is about.
const (
	AlertMailSync    = "sync"
	AlertMailFill    = "fill"
	AlertMailLoss    = "loss"
	AlertMailStorage = "storage"
)

// AlertMailPayload is the payload of a notify_mail job: IDs, figures and
// enums, never names, addresses or error texts. The job reads the names
// at delivery.
type AlertMailPayload struct {
	// Event is the event ID, as the webhook has it.
	Event string `json:"event"`
	Kind  string `json:"kind"`
	User  int64  `json:"user"`
	// Account is set for sync, fill and loss alerts; Fill for fills.
	Account int64 `json:"account,omitempty"`
	Fill    int64 `json:"fill,omitempty"`
	// Group is the group of a storage alert; nil for the instance.
	Group *int64 `json:"group,omitempty"`
	// Level is failing or recovered for syncs and fills, the state for
	// storage.
	Level string `json:"level,omitempty"`
	// Since is the start of the streak, the time of the loss or when the
	// storage state was entered.
	Since  *time.Time `json:"since,omitempty"`
	Streak int        `json:"streak,omitempty"`
	// Lost and PresentBefore are the figures of a loss alert.
	Lost          int `json:"lost,omitempty"`
	PresentBefore int `json:"presentBefore,omitempty"`
	// UsedBytes and QuotaBytes are the figures of a storage alert;
	// QuotaBytes is 0 without a quota.
	UsedBytes  int64 `json:"usedBytes,omitempty"`
	QuotaBytes int64 `json:"quotaBytes,omitempty"`
}

// AlertMailEvent is one claimed transition to mail: its event ID and the
// payload (without the user).
type AlertMailEvent struct {
	ID      string
	Payload AlertMailPayload
}

// AlertMail turns a claimed transition into the event to mail, or nil for
// none. A claim with a nil AlertMail enqueues no mail.
type AlertMail[T any] func(T) *AlertMailEvent

// alertMailKey is the job key of an event's mail to one user.
func alertMailKey(event string, userID int64) string {
	return event + ":" + strconv.FormatInt(userID, 10)
}

// enqueueAlertMail enqueues the jobs of ev for every recipient in q's
// transaction, unless enqueued is ev's ID already, and records ev with
// mark either way, so the row's mail state follows ev. A job that exists
// in any state stays as it is.
func enqueueAlertMail(ctx context.Context, q *db.Queries, ev *AlertMailEvent, enqueued *string, mark func() error) error {
	if ev == nil {
		return nil
	}
	if enqueued != nil && *enqueued == ev.ID {
		return mark()
	}
	users, err := q.AlertMailRecipients(ctx)
	if err != nil {
		return err
	}
	queued := false
	for _, u := range users {
		p := ev.Payload
		p.Event, p.User = ev.ID, u
		payload, err := json.Marshal(p)
		if err != nil {
			return err
		}
		if len(payload) > MaxJobPayload {
			return fmt.Errorf("alert mail payload over %d bytes", MaxJobPayload)
		}
		n, err := q.EnqueueJobOnce(ctx, db.EnqueueJobOnceParams{
			Kind: JobNotifyMail, Key: alertMailKey(ev.ID, u), Priority: PriorityNew,
			MaxAttempts: NotifyMailMaxAttempts, Payload: payload,
		})
		if err != nil {
			return fmt.Errorf("enqueue alert mail: %w", err)
		}
		queued = queued || n > 0
	}
	if queued {
		if err := q.NotifyJobs(ctx, JobNotifyMail); err != nil {
			return err
		}
	}
	return mark()
}

// claimWithMail runs claim, and with mail the enqueue of each claimed
// row's mail, in one transaction.
func claimWithMail[T any](s *Store, ctx context.Context, mail AlertMail[T],
	claim func(q *db.Queries) ([]T, error), each func(q *db.Queries, p T, ev *AlertMailEvent) error) ([]T, error) {
	if mail == nil {
		return claim(s.q)
	}
	var out []T
	err := s.inTx(ctx, func(q *db.Queries) error {
		var err error
		if out, err = claim(q); err != nil {
			return err
		}
		for _, p := range out {
			if err := each(q, p, mail(p)); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// mailedState is the mail state a health event leaves: ok after a
// recovery, failing after a failing mail. p.NotifiedState is what was
// last announced, so the event is the other state.
func mailedState(p PendingNotification) string {
	if p.NotifiedState == NotifiedFailing {
		return NotifiedOK
	}
	return NotifiedFailing
}

// EnqueueDueSyncMail enqueues the mail of up to limit accounts whose mail
// is due by the mail's own state, whatever the webhook's: failing when
// the streak reached threshold and the last mail said ok, recovered when
// the streak ended and the last mail said failing. The rows it hands mail
// have NotifiedState set to what the last mail said, so mail decides the
// event as for a claim. It returns how many rows it handled.
func (s *Store) EnqueueDueSyncMail(ctx context.Context, threshold, limit int, mail AlertMail[PendingNotification]) (int, error) {
	if mail == nil {
		return 0, nil
	}
	out, err := claimWithMail(s, ctx, mail, func(q *db.Queries) ([]PendingNotification, error) {
		rows, err := q.DueSyncMail(ctx, db.DueSyncMailParams{Threshold: clampInt32(threshold), MaxRows: clampInt32(limit)})
		if err != nil {
			return nil, err
		}
		out := make([]PendingNotification, 0, len(rows))
		for _, r := range rows {
			out = append(out, PendingNotification{
				AccountID: r.AccountID, FailureStreak: int(r.FailureStreak), FailingSince: r.FailingSince,
				NotifiedState: r.MailedState, mailEnqueued: r.MailEnqueuedEvent,
			})
		}
		return out, nil
	}, s.markSyncMail(ctx))
	return len(out), err
}

// EnqueueDueFillMail is EnqueueDueSyncMail for recurring fills.
func (s *Store) EnqueueDueFillMail(ctx context.Context, threshold, limit int, mail AlertMail[PendingNotification]) (int, error) {
	if mail == nil {
		return 0, nil
	}
	out, err := claimWithMail(s, ctx, mail, func(q *db.Queries) ([]PendingNotification, error) {
		rows, err := q.DueFillMail(ctx, db.DueFillMailParams{Threshold: clampInt32(threshold), MaxRows: clampInt32(limit)})
		if err != nil {
			return nil, err
		}
		out := make([]PendingNotification, 0, len(rows))
		for _, r := range rows {
			out = append(out, PendingNotification{
				AccountID: r.AccountID, FillID: r.FillID, FailureStreak: int(r.FailureStreak), FailingSince: r.FailingSince,
				NotifiedState: r.MailedState, mailEnqueued: r.MailEnqueuedEvent,
			})
		}
		return out, nil
	}, s.markFillMail(ctx))
	return len(out), err
}

// EnqueueDueLossMail enqueues the mail of up to limit loss alerts younger
// than maxAge whose mail was not enqueued, whether the webhook sent them
// or not. It returns how many rows it handled.
func (s *Store) EnqueueDueLossMail(ctx context.Context, maxAge time.Duration, limit int, mail AlertMail[PendingLossAlert]) (int, error) {
	if mail == nil {
		return 0, nil
	}
	out, err := claimWithMail(s, ctx, mail, func(q *db.Queries) ([]PendingLossAlert, error) {
		rows, err := q.DueLossMail(ctx, db.DueLossMailParams{MaxAgeSeconds: maxAge.Seconds(), MaxRows: clampInt32(limit)})
		if err != nil {
			return nil, err
		}
		out := make([]PendingLossAlert, 0, len(rows))
		for _, r := range rows {
			out = append(out, PendingLossAlert{
				ID: r.ID, SyncRunID: r.SyncRunID, AccountID: r.AccountID, CreatedAt: r.CreatedAt, mailEnqueued: r.MailEnqueuedEvent,
				LossAlert: LossAlert{Lost: int(r.Lost), PresentBefore: int(r.PresentBefore)},
			})
		}
		return out, nil
	}, s.markLossMail(ctx))
	return len(out), err
}

// EnqueueDueStorageMail enqueues the mail of up to limit scopes with a
// transition not mailed yet, whatever the webhook's state; a scope back at
// ok only after an earlier mail of it. It returns how many rows it
// handled.
func (s *Store) EnqueueDueStorageMail(ctx context.Context, limit int, mail AlertMail[PendingStorageNotification]) (int, error) {
	if mail == nil {
		return 0, nil
	}
	out, err := claimWithMail(s, ctx, mail, func(q *db.Queries) ([]PendingStorageNotification, error) {
		rows, err := q.DueStorageMail(ctx, clampInt32(limit))
		if err != nil {
			return nil, err
		}
		out := make([]PendingStorageNotification, 0, len(rows))
		for _, r := range rows {
			out = append(out, PendingStorageNotification{
				ID: r.ID, GroupID: r.GroupID, State: r.State, StateSince: r.StateSince, EventSeq: r.EventSeq,
				GroupQuotaBytes: r.GroupQuotaBytes, UsedBytes: r.UsedBytes, mailEnqueued: r.MailEnqueuedEvent,
			})
		}
		return out, nil
	}, s.markStorageMail(ctx))
	return len(out), err
}

// The marks of each outbox: enqueue a row's mail and record it.

func (s *Store) markSyncMail(ctx context.Context) func(q *db.Queries, p PendingNotification, ev *AlertMailEvent) error {
	return func(q *db.Queries, p PendingNotification, ev *AlertMailEvent) error {
		return enqueueAlertMail(ctx, q, ev, p.mailEnqueued, func() error {
			return q.SetSyncMailEnqueued(ctx, db.SetSyncMailEnqueuedParams{AccountID: p.AccountID, Event: &ev.ID, MailedState: mailedState(p)})
		})
	}
}

func (s *Store) markFillMail(ctx context.Context) func(q *db.Queries, p PendingNotification, ev *AlertMailEvent) error {
	return func(q *db.Queries, p PendingNotification, ev *AlertMailEvent) error {
		return enqueueAlertMail(ctx, q, ev, p.mailEnqueued, func() error {
			return q.SetFillMailEnqueued(ctx, db.SetFillMailEnqueuedParams{FillID: p.FillID, Event: &ev.ID, MailedState: mailedState(p)})
		})
	}
}

func (s *Store) markLossMail(ctx context.Context) func(q *db.Queries, p PendingLossAlert, ev *AlertMailEvent) error {
	return func(q *db.Queries, p PendingLossAlert, ev *AlertMailEvent) error {
		return enqueueAlertMail(ctx, q, ev, p.mailEnqueued, func() error {
			return q.SetLossMailEnqueued(ctx, db.SetLossMailEnqueuedParams{ID: p.ID, Event: &ev.ID})
		})
	}
}

func (s *Store) markStorageMail(ctx context.Context) func(q *db.Queries, p PendingStorageNotification, ev *AlertMailEvent) error {
	return func(q *db.Queries, p PendingStorageNotification, ev *AlertMailEvent) error {
		return enqueueAlertMail(ctx, q, ev, p.mailEnqueued, func() error {
			return q.SetStorageMailEnqueued(ctx, db.SetStorageMailEnqueuedParams{ID: p.ID, Event: &ev.ID, EventSeq: p.EventSeq})
		})
	}
}

// AlertRecipient is the recipient of an alert mail at delivery.
type AlertRecipient struct {
	UserID int64
	Name   string
	Email  string
	// Language is the user's UI setting; NotifyLanguage the UI's
	// language when the code was asked for (de or en), empty if unknown.
	Language       string
	NotifyLanguage string
}

// AlertMailRecipient returns the user as a recipient of alert mail: an
// admin, not locked, with a confirmed address. ErrNotFound otherwise.
func (s *Store) AlertMailRecipient(ctx context.Context, userID int64) (*AlertRecipient, error) {
	r, err := s.q.AlertMailRecipient(ctx, userID)
	if isNoRows(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &AlertRecipient{UserID: r.ID, Name: r.Name, Email: r.NotifyEmail, Language: r.Language, NotifyLanguage: deref(r.NotifyEmailLanguage)}, nil
}

// AlertNames are the names an alert mail shows, as at delivery; empty for
// a removed account or group.
type AlertNames struct {
	Account, Owner, Group string
}

// AlertMailNames reads the names of p's account, its owner and the owner's
// group, or of p's group for a storage alert.
func (s *Store) AlertMailNames(ctx context.Context, p AlertMailPayload) (AlertNames, error) {
	var n AlertNames
	switch {
	case p.Account != 0:
		r, err := s.q.AlertMailAccountNames(ctx, p.Account)
		if isNoRows(err) {
			return n, nil
		}
		if err != nil {
			return n, err
		}
		return AlertNames{Account: r.AccountName, Owner: r.OwnerName, Group: r.GroupName}, nil
	case p.Group != nil:
		name, err := s.q.AlertMailGroupName(ctx, *p.Group)
		if isNoRows(err) {
			return n, nil
		}
		n.Group = name
		return n, err
	}
	return n, nil
}

// BounceNotifyEmail unconfirms the user's address after a 5.1.x answer at
// RCPT, if it is still email. It reports whether it did.
func (s *Store) BounceNotifyEmail(ctx context.Context, userID int64, email string) (bool, error) {
	n, err := s.q.BounceNotifyEmail(ctx, db.BounceNotifyEmailParams{ID: userID, Email: email})
	return n > 0, err
}

// NotifyEmailState is a user's own notification address.
type NotifyEmailState struct {
	// Email is empty without an address.
	Email       string
	ConfirmedAt *time.Time
	// BouncedAt is set when a relay refused the address, until it is
	// confirmed again.
	BouncedAt *time.Time
	// CodeExpiresAt is set while a code waits to be entered.
	CodeExpiresAt *time.Time
	CodeTries     int
}

// NotifyEmail returns the user's notification address.
func (s *Store) NotifyEmail(ctx context.Context, userID int64) (NotifyEmailState, error) {
	r, err := s.q.NotifyEmailState(ctx, userID)
	if isNoRows(err) {
		return NotifyEmailState{}, ErrNotFound
	}
	if err != nil {
		return NotifyEmailState{}, err
	}
	return NotifyEmailState{
		Email: deref(r.NotifyEmail), ConfirmedAt: r.NotifyEmailConfirmedAt, BouncedAt: r.NotifyEmailBouncedAt,
		CodeExpiresAt: r.NotifyEmailCodeExpiresAt, CodeTries: int(r.NotifyEmailCodeAttempts),
	}, nil
}

// Errors of the notification address.
var (
	// ErrNotifyAdminOnly: only admins have a notification address.
	ErrNotifyAdminOnly = errors.New("only admins get alert mail")
	// ErrNotifyCodeLimit: NotifyCodesPerHour codes were sent in the last
	// hour; RetryAt says when the next may go.
	ErrNotifyCodeLimit = errors.New("too many codes requested")
	// ErrNotifyNoCode: no code waits, or it expired.
	ErrNotifyNoCode = errors.New("no valid code; request a new one")
	// ErrNotifyCodeTries: the code was entered wrong NotifyCodeMaxTries
	// times.
	ErrNotifyCodeTries = errors.New("too many wrong codes; request a new one")
	// ErrNotifyWrongCode: the code does not match.
	ErrNotifyWrongCode = errors.New("wrong code")
)

// NotifyCodeLimitError is ErrNotifyCodeLimit with the time the next code
// may be sent.
type NotifyCodeLimitError struct{ RetryAt time.Time }

func (e *NotifyCodeLimitError) Error() string { return ErrNotifyCodeLimit.Error() }
func (e *NotifyCodeLimitError) Unwrap() error { return ErrNotifyCodeLimit }

// NotifyCodeHash is the SHA-256 stored for a confirmation code of the
// user: the code is bound to the user, and never stored itself.
func NotifyCodeHash(userID int64, code string) []byte {
	sum := sha256.Sum256([]byte("mail-archive notify-email code\x00" + strconv.FormatInt(userID, 10) + "\x00" + code))
	return sum[:]
}

// NotifyCodeRequest sets an address with a new code.
type NotifyCodeRequest struct {
	Email string
	// CodeHash is the SHA-256 of the code; the code itself is never
	// stored.
	CodeHash []byte
	// Language is the UI's language (de or en), empty if unknown.
	Language string
	Now      time.Time
}

// SetNotifyEmail sets the admin's address, unconfirmed, with a new code,
// within NotifyCodesPerHour codes per hour, and writes the audit entry of
// ctx (no address). The caller mails the code after the commit.
func (s *Store) SetNotifyEmail(ctx context.Context, userID int64, r NotifyCodeRequest) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		u, err := q.LockNotifyEmail(ctx, userID)
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if !u.IsAdmin {
			return ErrNotifyAdminOnly
		}
		start, sent := r.Now, 0
		if u.NotifyEmailCodesWindowStart != nil && r.Now.Sub(*u.NotifyEmailCodesWindowStart) < time.Hour {
			start, sent = *u.NotifyEmailCodesWindowStart, int(u.NotifyEmailCodesSent)
		}
		if sent >= NotifyCodesPerHour {
			return &NotifyCodeLimitError{RetryAt: start.Add(time.Hour)}
		}
		p := db.SetNotifyEmailCodeParams{
			ID: userID, Email: r.Email, CodeHash: r.CodeHash, ExpiresAt: r.Now.Add(NotifyCodeTTL),
			WindowStart: start, CodesSent: int16(sent + 1),
		}
		if r.Language != "" {
			p.Language = &r.Language
		}
		if err := q.SetNotifyEmailCode(ctx, p); err != nil {
			return err
		}
		if e := auditFrom(ctx); e != nil && u.NotifyEmail != nil {
			e.Detail("replaced", true)
		}
		return nil
	})
}

// ConfirmNotifyEmail checks codeHash against the waiting code and confirms
// the address. Every call counts a try first, committed on its own; after
// NotifyCodeMaxTries the code is spent. The audit entry of ctx is written
// only on success.
func (s *Store) ConfirmNotifyEmail(ctx context.Context, userID int64, codeHash []byte, now time.Time) error {
	n, err := s.q.CountNotifyEmailCodeTry(ctx, db.CountNotifyEmailCodeTryParams{ID: userID, MaxTries: NotifyCodeMaxTries})
	if err != nil {
		return err
	}
	tried := n > 0
	return s.inTx(ctx, func(q *db.Queries) error {
		u, err := q.LockNotifyEmail(ctx, userID)
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		switch {
		case !u.IsAdmin:
			return ErrNotifyAdminOnly
		case u.NotifyEmailCodeHash == nil || u.NotifyEmailCodeExpiresAt == nil || !now.Before(*u.NotifyEmailCodeExpiresAt):
			return ErrNotifyNoCode
		case !tried:
			return ErrNotifyCodeTries
		case subtle.ConstantTimeCompare(u.NotifyEmailCodeHash, codeHash) != 1:
			if int(u.NotifyEmailCodeAttempts) >= NotifyCodeMaxTries {
				return ErrNotifyCodeTries
			}
			return ErrNotifyWrongCode
		}
		return q.ConfirmNotifyEmail(ctx, userID)
	})
}

// RemoveNotifyEmail removes the address. ErrNotFound if there is none
// (no audit entry then).
func (s *Store) RemoveNotifyEmail(ctx context.Context, userID int64) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		return one(q.RemoveNotifyEmail(ctx, userID))
	})
}
