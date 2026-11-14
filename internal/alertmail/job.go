package alertmail

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/keepia/archive/internal/jobs"
	"github.com/keepia/archive/internal/store"
)

// Retry of an alert mail: 1 minute after the first failed attempt,
// doubling up to an hour, store.NotifyMailMaxAttempts attempts in all.
const (
	RetryFirst = time.Minute
	RetryCap   = time.Hour
)

// Job codes in jobs.last_error.
const (
	// CodeRecipientGone: the user is no longer an admin, is locked or
	// removed, or has no confirmed address; nothing was sent.
	CodeRecipientGone = "recipient_gone"
	// CodeBadPayload: a payload this version cannot read.
	CodeBadPayload = "bad payload"
)

// Mailer sends one composed message (Sender.Send).
type Mailer interface {
	Send(ctx context.Context, to string, msg []byte) (*Result, error)
}

// Service builds and sends alert mail and confirmation codes.
type Service struct {
	Store  *store.Store
	Mailer Mailer
	// From is the From header (config.Mail.FromHeader).
	From string
	// PublicURL is KEEPIA_ARCHIVE_PUBLIC_URL, for links and Message-IDs.
	PublicURL string
	Logger    *slog.Logger
	// Now is injectable for tests.
	Now func() time.Time
}

// Kind is the notify_mail job kind: one alert mail to one admin.
func (s *Service) Kind() jobs.Kind {
	return jobs.Kind{
		Name: store.JobNotifyMail, MaxAttempts: store.NotifyMailMaxAttempts,
		RetryFirst: RetryFirst, RetryCap: RetryCap, Handler: s.run,
	}
}

// run delivers one alert mail. The recipient is read again: a user who is
// no longer an admin, is locked or removed, or has no confirmed address
// gets nothing (done with CodeRecipientGone), and the mail goes to the
// address confirmed now. A 4xx reply, a timeout or a network error is
// retried; a 5.1.x reply at RCPT unconfirms the address; any other 5xx
// gives up. Replies are logged by code only, and no address is logged.
func (s *Service) run(ctx context.Context, j jobs.Job) error {
	var p store.AlertMailPayload
	if err := json.Unmarshal(j.Payload, &p); err != nil || p.User == 0 || p.Event == "" {
		return jobs.Permanent(jobs.Code(CodeBadPayload, err))
	}
	log := s.logger().With("event", p.Event, "user_id", p.User)
	r, err := s.Store.AlertMailRecipient(ctx, p.User)
	if errors.Is(err, store.ErrNotFound) {
		log.Info("alert mail not sent: the user gets no alert mail now", "code", CodeRecipientGone)
		return jobs.Done(CodeRecipientGone)
	}
	if err != nil {
		return err
	}
	names, err := s.Store.AlertMailNames(ctx, p)
	if err != nil {
		return err
	}
	a := Alert{AlertMailPayload: p, Account: Name(names.Account), Owner: Name(names.Owner), Group: Name(names.Group), PublicURL: s.PublicURL}
	subject, body := Texts(Language(r.Language, r.NotifyLanguage), a)
	msg, err := Compose(Message{
		From: s.From, To: r.Email, Subject: subject, Body: body, Date: s.now(),
		MessageID: MessageID(p.Event, p.User, s.PublicURL),
	})
	if err != nil {
		// The address passed the same check when it was set.
		return jobs.Permanent(jobs.Code("bad address", err))
	}
	res, err := s.Mailer.Send(ctx, r.Email, msg)
	if err == nil {
		log.Info("alert mail sent", "reply", replyCode(res), "attempt", j.Attempt)
		return nil
	}
	if ctx.Err() != nil && errors.Is(err, context.Canceled) {
		return err
	}
	var se *SendError
	if !errors.As(err, &se) {
		return jobs.Code("smtp", err)
	}
	code := "smtp " + se.Phase
	if se.Code != 0 {
		code = "smtp " + se.ReplyCode()
	} else if se.Timeout {
		code = "smtp timeout"
	}
	switch {
	case se.BadMailbox():
		unconfirmed, uerr := s.Store.BounceNotifyEmail(context.WithoutCancel(ctx), p.User, r.Email)
		if uerr != nil {
			return uerr
		}
		log.Warn("alert mail refused: bad address; the user's notification address is unconfirmed",
			"reply", se.ReplyCode(), "unconfirmed", unconfirmed)
		return jobs.Permanent(jobs.Code(code, se))
	case se.Permanent():
		log.Error("alert mail refused by the relay; check the relay settings", "phase", se.Phase, "reply", se.ReplyCode())
		return jobs.Permanent(jobs.Code(code, se))
	}
	return jobs.Code(code, se)
}

// SendCode mails a confirmation code to email in lang. A failure is a
// *SendError, logged by code only.
func (s *Service) SendCode(ctx context.Context, userID int64, email, lang, code string) error {
	subject, body := CodeTexts(lang, code, s.PublicURL, store.NotifyCodeTTL)
	now := s.now()
	msg, err := Compose(Message{
		From: s.From, To: email, Subject: subject, Body: body, Date: now,
		MessageID: MessageID("code."+now.UTC().Format("20060102150405.000000000"), userID, s.PublicURL),
	})
	if err != nil {
		return err
	}
	res, err := s.Mailer.Send(ctx, email, msg)
	log := s.logger().With("user_id", userID)
	if err != nil {
		var se *SendError
		if errors.As(err, &se) {
			log.Warn("confirmation code not sent", "phase", se.Phase, "reply", se.ReplyCode(), "timeout", se.Timeout)
		} else {
			log.Warn("confirmation code not sent", "err", err)
		}
		return err
	}
	log.Info("confirmation code sent", "reply", replyCode(res))
	return nil
}

// replyCode is "250 2.0.0" for logs.
func replyCode(r *Result) string {
	if r == nil {
		return ""
	}
	if r.Enhanced == "" {
		return "250"
	}
	return "250 " + r.Enhanced
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}
