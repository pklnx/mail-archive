// Package notify tells the operator when syncs of an account or runs of a
// recurring fill keep failing, when they work again, when many of an account's messages were
// deleted on the server, and when the storage quota state of the instance
// or of a customer group changes, through a webhook. By default
// (config.WebhookDetailIDs) the webhook gets IDs and counts only.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/pklnx/mail-archive/internal/config"
)

// RequestTimeout bounds one webhook request, including reading the answer.
const RequestTimeout = 10 * time.Second

// maxResponseBody is how much of the answer is read before it is dropped.
const maxResponseBody = 4 << 10

// Events.
const (
	EventFailing   = "failing"
	EventRecovered = "recovered"
	EventMixed     = "mixed" // a batch with alerts and recoveries
	EventTest      = "test"
	// EventGone: many messages of an account were deleted on the server.
	EventGone = "gone"
	// EventStorage: the storage quota state of a scope changed (#397).
	EventStorage = "storage"
)

// AccountEvent is the part of a message about one account.
type AccountEvent struct {
	// ID is stable for one transition, so receivers can drop repeats.
	ID            string     `json:"id"`
	Event         string     `json:"event"`
	AccountID     int64      `json:"accountId"`
	Account       string     `json:"account"`
	Owner         string     `json:"owner"`
	FailureStreak int        `json:"failureStreak"`
	FailingSince  *time.Time `json:"failingSince"`
	LastError     string     `json:"lastError,omitempty"`
	// For a recurring fill: its ID and the mailbox it imports from (the
	// IMAP user name); the account is the smtp account it fills.
	FillID  int64  `json:"fillId,omitempty"`
	Mailbox string `json:"mailbox,omitempty"`
	// For EventGone: messages now only in the archive, how many were on the
	// server before, and the folders they were deleted from.
	Lost          int          `json:"lost,omitempty"`
	PresentBefore int          `json:"presentBefore,omitempty"`
	Folders       []FolderLoss `json:"folders,omitempty"`
	MoreFolders   int          `json:"moreFolders,omitempty"`
}

// FolderLoss is how many lost messages were deleted from a folder.
type FolderLoss struct {
	Name string `json:"name"`
	Lost int    `json:"lost"`
}

// Message is one webhook request.
type Message struct {
	ID    string
	Event string
	// Title and Body are plain text, for Gotify, ntfy and generic
	// receivers.
	Title string
	Body  string
	// Slack (Slack and Mattermost) and Discord are the title and the body
	// with every value as a code span, fitted to the field's limit. Empty
	// means Title and Body hold no value and are sent as they are.
	Slack    string
	Discord  string
	Accounts []AccountEvent
	// Storage holds the transitions of an EventStorage message.
	Storage []StorageEvent
}

// jsonBody covers Gotify (title, message, extras), Slack and Mattermost
// (text) and Discord (content, allowed_mentions), plus the structured
// fields for everything else.
type jsonBody struct {
	ID              string          `json:"id"`
	Event           string          `json:"event"`
	Title           string          `json:"title"`
	Message         string          `json:"message"`
	Text            string          `json:"text"`
	Content         string          `json:"content"`
	AllowedMentions allowedMentions `json:"allowed_mentions"`
	Extras          extras          `json:"extras"`
	Accounts        any             `json:"accounts"`
	Storage         []StorageEvent  `json:"storage,omitempty"`
}

// idsAccountEvent is AccountEvent as the ids detail mode sends it: the
// fields that can hold names, mailboxes, errors or folder names are not
// there at all, whatever the builder put into the AccountEvent.
type idsAccountEvent struct {
	ID            string     `json:"id"`
	Event         string     `json:"event"`
	AccountID     int64      `json:"accountId"`
	FailureStreak int        `json:"failureStreak"`
	FailingSince  *time.Time `json:"failingSince"`
	FillID        int64      `json:"fillId,omitempty"`
	Lost          int        `json:"lost,omitempty"`
	PresentBefore int        `json:"presentBefore,omitempty"`
}

func idsAccounts(accounts []AccountEvent) []idsAccountEvent {
	out := make([]idsAccountEvent, 0, len(accounts))
	for _, a := range accounts {
		out = append(out, idsAccountEvent{
			ID: a.ID, Event: a.Event, AccountID: a.AccountID, FailureStreak: a.FailureStreak, FailingSince: a.FailingSince,
			FillID: a.FillID, Lost: a.Lost, PresentBefore: a.PresentBefore,
		})
	}
	return out
}

// allowedMentions with an empty Parse makes Discord ping no one.
type allowedMentions struct {
	Parse []string `json:"parse"`
}

// extras pins Gotify to plain text.
type extras struct {
	ClientDisplay struct {
		ContentType string `json:"contentType"`
	} `json:"client::display"`
}

// Webhook sends messages to the configured URL. It never follows
// redirects, so the Authorization header and the body reach only that
// host, and it never puts the URL into an error.
type Webhook struct {
	cfg    *config.Webhook
	client *http.Client
}

// NewWebhook returns a sender for cfg.
func NewWebhook(cfg *config.Webhook) *Webhook {
	return &Webhook{cfg: cfg, client: &http.Client{
		Timeout: RequestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

// Target names the scheme and host, for logs.
func (w *Webhook) Target() string { return w.cfg.Target() }

// Full reports whether alerts carry names, mailboxes, errors and folders
// (config.WebhookDetailFull); by default they carry IDs only.
func (w *Webhook) Full() bool { return w.cfg.Full() }

// DeliveryError is a failed webhook request. Its text never contains the
// URL.
type DeliveryError struct {
	// Status is the HTTP status, or 0 if no answer arrived.
	Status int
	// RetryAfter is the server's Retry-After, if any.
	RetryAfter time.Duration
	Err        error
}

func (e *DeliveryError) Error() string {
	if e.Status != 0 {
		return "HTTP " + strconv.Itoa(e.Status)
	}
	return e.Err.Error()
}

func (e *DeliveryError) Unwrap() error { return e.Err }

// Permanent reports an answer that a retry will not change soon: a 3xx or
// 4xx other than 429. It usually means a wrong URL, token or format.
func (e *DeliveryError) Permanent() bool {
	return e.Status >= 300 && e.Status < 500 && e.Status != http.StatusTooManyRequests
}

// Send posts m. It returns the HTTP status, and a *DeliveryError unless
// the status is 2xx.
func (w *Webhook) Send(ctx context.Context, m *Message) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()
	var body []byte
	contentType := "application/json"
	if w.cfg.Format == config.WebhookFormatNtfy {
		body, contentType = []byte(m.Body), "text/plain; charset=utf-8"
	} else {
		slack, discord := m.Slack, m.Discord
		if slack == "" {
			slack = m.Title + "\n" + m.Body
		}
		if discord == "" {
			discord = m.Title + "\n" + m.Body
		}
		var accounts any = idsAccounts(m.Accounts)
		if w.cfg.Full() {
			full := m.Accounts
			if full == nil {
				full = []AccountEvent{}
			}
			accounts = full
		}
		jb := jsonBody{
			ID: m.ID, Event: m.Event, Title: m.Title, Message: m.Body,
			Text: slack, Content: discord, AllowedMentions: allowedMentions{Parse: []string{}}, Accounts: accounts,
			Storage: m.Storage,
		}
		jb.Extras.ClientDisplay.ContentType = "text/plain"
		var err error
		body, err = json.Marshal(jb)
		if err != nil {
			return 0, &DeliveryError{Err: err}
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.cfg.URL.String(), bytes.NewReader(body))
	if err != nil {
		return 0, &DeliveryError{Err: errors.New("cannot build the request")}
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "mail-archive")
	req.Header.Set("X-Mail-Archive-Id", m.ID)
	if w.cfg.Authorization != "" {
		req.Header.Set("Authorization", w.cfg.Authorization)
	}
	if w.cfg.Format == config.WebhookFormatNtfy {
		req.Header.Set("Title", encodeTitle(m.Title))
		priority, tags := "default", "white_check_mark"
		switch m.Event {
		case EventFailing, EventMixed:
			priority, tags = "high", "warning"
		case EventGone:
			priority, tags = "high", "wastebasket"
		case EventStorage:
			if m.storageAlert() {
				priority, tags = "high", "warning"
			}
		}
		if m.Event == EventTest {
			tags = "test_tube"
		}
		req.Header.Set("Priority", priority)
		req.Header.Set("Tags", tags)
	}
	resp, err := w.client.Do(req)
	if err != nil {
		// *url.Error repeats the whole URL; keep only the cause.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return 0, &DeliveryError{Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBody))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp.StatusCode, nil
	}
	return resp.StatusCode, &DeliveryError{Status: resp.StatusCode, RetryAfter: retryAfter(resp.Header.Get("Retry-After"), time.Now())}
}

// storageAlert reports whether a storage message holds a level other than
// ok.
func (m *Message) storageAlert() bool {
	for _, ev := range m.Storage {
		if ev.Level != LevelOK {
			return true
		}
	}
	return false
}

// retryAfter parses a Retry-After value in seconds or as an HTTP date.
func retryAfter(v string, now time.Time) time.Duration {
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}

// truncate cuts s to at most n runes, marking the cut with an ellipsis.
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// TestMessage is what `notify test` sends.
func TestMessage(now time.Time) *Message {
	return &Message{
		ID:    fmt.Sprintf("test-%d", now.Unix()),
		Event: EventTest,
		Title: "Mail archive: test notification",
		Body:  "This is a test from mail-archive. Alerts about failing syncs, mail deleted on the server and storage will arrive here.",
	}
}
