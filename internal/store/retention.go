package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pklnx/mail-archive/internal/store/db"
)

// retentionLockClass is the first key of the advisory locks of retention
// ("mf" in ASCII): key 1 serializes changes to rules and holds with each
// other and with the batches of a deletion run. Lock order: the blob lock,
// the attestation fence, then this lock.
const retentionLockClass = 0x6d66

// ErrOnHold is returned when a change would release mail from a legal
// hold, or remove a user or account that a hold names.
var ErrOnHold = errors.New("named by a legal hold")

// ErrInvalidRetention wraps the validation errors of rules and holds: a
// field out of range, a missing name or note.
var ErrInvalidRetention = errors.New("invalid")

// ErrInvalidTimeZone is returned for a time zone PostgreSQL does not know.
// It wraps ErrInvalidRetention.
var ErrInvalidTimeZone = fmt.Errorf("%w: unknown time zone", ErrInvalidRetention)

// DefaultRetentionTimeZone is the time zone of a rule's year end when none
// is given.
const DefaultRetentionTimeZone = "Europe/Berlin"

// MinBusinessLetterYears is the period for business letters (§147 AO,
// §257 HGB). Shorter rules need a confirmation.
const MinBusinessLetterYears = 6

// RetentionRule keeps the mail of an account, or of one of its folders,
// for Years after the end of the year it was received or sent in.
type RetentionRule struct {
	ID             int64
	AccountID      int64
	Account        string
	AccountKind    AccountKind
	AccountRemoved bool
	Owner          string // empty for an account without owner
	Folder         string // empty: every folder of the account
	Years          int
	TimeZone       string
	Note           string
	CreatedAt      time.Time
	CreatedBy      string // empty: the command line, or a removed user
}

// NewRetentionRule is a rule to add.
type NewRetentionRule struct {
	AccountID int64
	Folder    string
	Years     int
	TimeZone  string // default DefaultRetentionTimeZone
	Note      string
	CreatedBy *int64 // nil: the command line
}

// AddRetentionRule adds a rule. It fails with ErrConflict if the account
// (or folder) already has one, and with ErrInvalidTimeZone.
func (s *Store) AddRetentionRule(ctx context.Context, r NewRetentionRule) (int64, error) {
	if r.TimeZone == "" {
		r.TimeZone = DefaultRetentionTimeZone
	}
	if r.Years < 1 || r.Years > 100 {
		return 0, fmt.Errorf("%w: years must be between 1 and 100, got %d", ErrInvalidRetention, r.Years)
	}
	if len(r.Note) > 500 {
		return 0, fmt.Errorf("%w: the note is longer than 500 characters", ErrInvalidRetention)
	}
	var id int64
	err := s.inTx(ctx, func(q *db.Queries) error {
		if err := q.LockRetentionChanges(ctx, retentionLockClass); err != nil {
			return err
		}
		if err := checkTimeZone(ctx, q, r.TimeZone); err != nil {
			return err
		}
		row, err := q.InsertRetentionRule(ctx, db.InsertRetentionRuleParams{
			AccountID: r.AccountID, Folder: nonEmpty(r.Folder), Years: int32(r.Years), //nolint:gosec // checked above
			TimeZone: r.TimeZone, Note: r.Note, CreatedBy: r.CreatedBy,
		})
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return fmt.Errorf("a rule for this account and folder: %w", ErrConflict)
		}
		if err != nil {
			return err
		}
		id = row.ID
		if e := auditFrom(ctx); e != nil {
			e.Detail("rule_id", id)
		}
		return nil
	})
	return id, err
}

// checkTimeZone asks PostgreSQL, whose time zone data the rules use.
func checkTimeZone(ctx context.Context, q *db.Queries, tz string) error {
	if strings.TrimSpace(tz) != tz || tz == "" {
		return fmt.Errorf("%w %q", ErrInvalidTimeZone, tz)
	}
	if err := q.CheckTimeZone(ctx, tz); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "22023" {
			return fmt.Errorf("%w %q", ErrInvalidTimeZone, tz)
		}
		return err
	}
	return nil
}

// RetentionRules lists every rule, by owner, account and folder.
func (s *Store) RetentionRules(ctx context.Context) ([]RetentionRule, error) {
	rows, err := s.q.ListRetentionRules(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]RetentionRule, len(rows))
	for i, r := range rows {
		out[i] = RetentionRule{
			ID: r.ID, AccountID: r.AccountID, Account: r.Account, AccountKind: AccountKind(r.AccountKind),
			AccountRemoved: r.AccountRemoved, Owner: deref(r.Owner), Folder: deref(r.Folder), Years: int(r.Years),
			TimeZone: r.TimeZone, Note: r.Note, CreatedAt: r.CreatedAt, CreatedBy: deref(r.CreatedBy),
		}
	}
	return out, nil
}

// RemoveRetentionRule removes a rule. Unknown IDs are ErrNotFound.
func (s *Store) RemoveRetentionRule(ctx context.Context, id int64) error {
	return s.inTx(ctx, func(q *db.Queries) error {
		if err := q.LockRetentionChanges(ctx, retentionLockClass); err != nil {
			return err
		}
		r, err := q.DeleteRetentionRule(ctx, id)
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if e := auditFrom(ctx); e != nil {
			e.Detail("rule_id", id)
			e.Detail("years", r.Years)
			if r.Folder != nil {
				e.Detail("folder", *r.Folder)
			}
		}
		return nil
	})
}

// HoldFilter is the search filter of a legal hold: the messages that
// search finds with it are held. Empty fields are ignored.
type HoldFilter struct {
	Query         string `json:"q,omitempty"`
	From          string `json:"from,omitempty"`
	To            string `json:"to,omitempty"`
	Attachment    string `json:"attachment,omitempty"`
	HasAttachment bool   `json:"has_attachment,omitempty"`
}

// IsZero reports whether the filter selects nothing.
func (f *HoldFilter) IsZero() bool {
	return f == nil || (strings.TrimSpace(f.Query) == "" && strings.TrimSpace(f.From) == "" &&
		strings.TrimSpace(f.To) == "" && strings.TrimSpace(f.Attachment) == "" && !f.HasAttachment)
}

// NewLegalHold is a hold to add. Every condition that is set must match;
// with none, the hold covers the whole archive.
type NewLegalHold struct {
	Name        string
	Reason      string
	OwnerID     *int64
	AccountID   *int64
	Filter      *HoldFilter
	PeriodStart *time.Time
	PeriodEnd   *time.Time
	CreatedBy   *int64 // nil: the command line
}

// LegalHold is a hold, active or released.
type LegalHold struct {
	ID           int64
	Name         string
	Reason       string
	OwnerID      *int64
	Owner        string
	AccountID    *int64
	Account      string
	AccountOwner string
	Filter       *HoldFilter
	PeriodStart  *time.Time
	PeriodEnd    *time.Time
	// MatchedAt is when the filter's matches were last stored; until then
	// a filter hold covers its whole scope.
	MatchedAt   *time.Time
	Matched     int64 // messages the filter matched so far
	CreatedAt   time.Time
	CreatedBy   string
	ReleasedAt  *time.Time
	ReleasedBy  string
	ReleaseNote string
}

// AddLegalHold adds a hold and returns its ID. A filter hold covers its
// whole scope until MatchLegalHolds stored its matches; call it next.
func (s *Store) AddLegalHold(ctx context.Context, h NewLegalHold) (int64, error) {
	h.Name, h.Reason = strings.TrimSpace(h.Name), strings.TrimSpace(h.Reason)
	switch {
	case h.Name == "" || len([]rune(h.Name)) > 200:
		return 0, fmt.Errorf("%w: the name must have 1 to 200 characters", ErrInvalidRetention)
	case len([]rune(h.Reason)) > 2000:
		return 0, fmt.Errorf("%w: the reason is longer than 2000 characters", ErrInvalidRetention)
	case h.PeriodStart != nil && h.PeriodEnd != nil && !h.PeriodStart.Before(*h.PeriodEnd):
		return 0, fmt.Errorf("%w: the period must end after it starts", ErrInvalidRetention)
	}
	var filter []byte
	if !h.Filter.IsZero() {
		var err error
		if filter, err = json.Marshal(h.Filter); err != nil {
			return 0, err
		}
	}
	var id int64
	err := s.inTx(ctx, func(q *db.Queries) error {
		if err := q.LockRetentionChanges(ctx, retentionLockClass); err != nil {
			return err
		}
		row, err := q.InsertLegalHold(ctx, db.InsertLegalHoldParams{
			Name: h.Name, Reason: h.Reason, OwnerID: h.OwnerID, AccountID: h.AccountID, Filter: filter,
			PeriodStart: h.PeriodStart, PeriodEnd: h.PeriodEnd, CreatedBy: h.CreatedBy,
		})
		if err != nil {
			return err
		}
		id = row.ID
		if e := auditFrom(ctx); e != nil {
			e.Detail("hold_id", id)
			e.Detail("name", h.Name)
			if filter != nil {
				e.Detail("filter", h.Filter)
			}
			if h.PeriodStart != nil {
				e.Detail("period_start", h.PeriodStart.UTC().Format(time.RFC3339))
			}
			if h.PeriodEnd != nil {
				e.Detail("period_end", h.PeriodEnd.UTC().Format(time.RFC3339))
			}
		}
		return nil
	})
	return id, err
}

// LegalHolds lists the active holds, and the released ones too with
// released.
func (s *Store) LegalHolds(ctx context.Context, released bool) ([]LegalHold, error) {
	rows, err := s.q.ListLegalHolds(ctx, released)
	if err != nil {
		return nil, err
	}
	out := make([]LegalHold, len(rows))
	for i, r := range rows {
		h := LegalHold{
			ID: r.ID, Name: r.Name, Reason: r.Reason, OwnerID: r.OwnerID, Owner: deref(r.Owner),
			AccountID: r.AccountID, Account: deref(r.Account), AccountOwner: deref(r.AccountOwner),
			PeriodStart: r.PeriodStart, PeriodEnd: r.PeriodEnd, MatchedAt: r.MatchedAt, Matched: r.Matched,
			CreatedAt: r.CreatedAt, CreatedBy: deref(r.CreatedBy), ReleasedAt: r.ReleasedAt,
			ReleasedBy: deref(r.ReleasedBy), ReleaseNote: deref(r.ReleaseNote),
		}
		if r.Filter != nil {
			h.Filter = &HoldFilter{}
			if err := json.Unmarshal(r.Filter, h.Filter); err != nil {
				return nil, fmt.Errorf("legal hold #%d: %w", r.ID, err)
			}
		}
		out[i] = h
	}
	return out, nil
}

// ReleaseLegalHold releases an active hold. Unknown IDs are ErrNotFound;
// a hold released already is ErrConflict.
func (s *Store) ReleaseLegalHold(ctx context.Context, id int64, by *int64, note string) error {
	note = strings.TrimSpace(note)
	if note == "" || len([]rune(note)) > 2000 {
		return fmt.Errorf("%w: a release note of 1 to 2000 characters is required", ErrInvalidRetention)
	}
	return s.inTx(ctx, func(q *db.Queries) error {
		if err := q.LockRetentionChanges(ctx, retentionLockClass); err != nil {
			return err
		}
		r, err := q.ReleaseLegalHold(ctx, db.ReleaseLegalHoldParams{ID: id, ReleasedBy: by, Note: note})
		if isNoRows(err) {
			exists, eerr := q.LegalHoldExists(ctx, id)
			if eerr != nil {
				return eerr
			}
			if exists {
				return fmt.Errorf("legal hold #%d is released: %w", id, ErrConflict)
			}
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if e := auditFrom(ctx); e != nil {
			e.Detail("hold_id", id)
			e.Detail("name", r.Name)
			e.Detail("note", note)
		}
		return nil
	})
}

// holdMatchPage is how many search results are stored at a time.
const holdMatchPage = 500

// MatchLegalHolds stores, for every active filter hold, the messages its
// filter finds now, and returns how many were new. Matches are only ever
// added. Search sees what a user sees, so the filter runs as the hold's
// user, as the owner of its account, or, for a hold without either, as
// every user.
func (s *Store) MatchLegalHolds(ctx context.Context) (int64, error) {
	holds, err := s.q.ActiveFilterHolds(ctx)
	if err != nil {
		return 0, err
	}
	var added int64
	for _, h := range holds {
		var f HoldFilter
		if err := json.Unmarshal(h.Filter, &f); err != nil {
			return added, fmt.Errorf("legal hold #%d: %w", h.ID, err)
		}
		started := time.Now()
		var owners []int64
		switch {
		case h.OwnerID != nil:
			owners = []int64{*h.OwnerID}
		case h.AccountID != nil:
			if h.AccountOwner != nil {
				owners = []int64{*h.AccountOwner}
			}
		default:
			users, err := s.ListUsers(ctx)
			if err != nil {
				return added, err
			}
			for _, u := range users {
				owners = append(owners, u.ID)
			}
		}
		for _, owner := range owners {
			n, err := s.matchHold(ctx, h.ID, owner, deref(h.Account), f)
			added += n
			if err != nil {
				return added, fmt.Errorf("legal hold #%d: %w", h.ID, err)
			}
		}
		if err := s.q.SetLegalHoldMatched(ctx, db.SetLegalHoldMatchedParams{ID: h.ID, MatchedAt: started}); err != nil {
			return added, err
		}
	}
	return added, nil
}

func (s *Store) matchHold(ctx context.Context, holdID, owner int64, account string, f HoldFilter) (int64, error) {
	sf := SearchFilter{
		Owner: owner, Account: account, Query: f.Query, From: f.From, To: f.To,
		Attachment: f.Attachment, HasAttachment: f.HasAttachment, Limit: holdMatchPage,
	}
	var added int64
	for {
		page, err := s.SearchMessages(ctx, sf)
		if err != nil {
			return added, err
		}
		if len(page) == 0 {
			return added, nil
		}
		shas := make([]string, len(page))
		for i, m := range page {
			shas[i] = m.SHA256
		}
		n, err := s.q.AddLegalHoldMessages(ctx, db.AddLegalHoldMessagesParams{HoldID: holdID, Shas: shas})
		added += n
		if err != nil {
			return added, err
		}
		if len(page) < holdMatchPage {
			return added, nil
		}
		last := page[len(page)-1]
		sf.CursorAt, sf.CursorSHA = &last.SortAt, last.SHA256
	}
}

// RetentionCounts are the numbers status shows.
type RetentionCounts struct {
	Rules       int64
	ActiveHolds int64
}

// RetentionCounts returns the number of rules and active holds.
func (s *Store) RetentionCounts(ctx context.Context) (RetentionCounts, error) {
	r, err := s.q.RetentionCounts(ctx)
	return RetentionCounts{Rules: r.Rules, ActiveHolds: r.ActiveHolds}, err
}

// holdIDs formats hold IDs for an error.
func holdIDs(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("#%d", id)
	}
	return strings.Join(parts, ", ")
}

// RetentionAccount is an account to choose for a rule or a hold.
type RetentionAccount struct {
	ID      int64
	Name    string
	Kind    AccountKind
	Removed bool
	Owner   string
	Folders []string
}

// RetentionAccounts lists every account with its owner and folders.
func (s *Store) RetentionAccounts(ctx context.Context) ([]RetentionAccount, error) {
	rows, err := s.q.ListRetentionAccounts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]RetentionAccount, len(rows))
	for i, r := range rows {
		out[i] = RetentionAccount{ID: r.ID, Name: r.Name, Kind: AccountKind(r.Kind), Removed: r.Removed, Owner: deref(r.Owner), Folders: nonNil(r.Folders)}
	}
	return out, nil
}
