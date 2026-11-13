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

// HoldError is an ErrOnHold (errors.Is) that names the legal holds in the
// way: the web answer carries their IDs instead of the message.
type HoldError struct {
	IDs []int64
	msg string
}

// newHoldError returns a HoldError for ids; format gets ErrOnHold (%v)
// and the IDs as "#1, #2" (%s).
func newHoldError(ids []int64, format string) *HoldError {
	return &HoldError{IDs: ids, msg: fmt.Sprintf(format, ErrOnHold, holdIDs(ids))}
}

func (e *HoldError) Error() string { return e.msg }

// Is makes a HoldError match ErrOnHold.
func (e *HoldError) Is(target error) bool { return target == ErrOnHold }

// ErrInvalidRetention wraps the validation errors of rules and holds: a
// field out of range, a missing name or note.
var ErrInvalidRetention = errors.New("invalid")

// ErrInvalidHold is returned for a hold whose user or account is not in
// the customer group it names (#470): such a hold would cover nothing. It
// wraps ErrInvalidRetention.
var ErrInvalidHold = fmt.Errorf("%w: the user or account is not in the hold's group", ErrInvalidRetention)

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
	// GroupID and Group are the customer group of the account's owner
	// (#470); nil and empty without an owner.
	GroupID   *int64
	Group     string
	Folder    string // empty: every folder of the account
	Years     int
	TimeZone  string
	Note      string
	CreatedAt time.Time
	CreatedBy string // empty: the command line, or a removed user
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
			GroupID: r.GroupID, Group: deref(r.GroupName),
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
			// A rule is always about an account: the entry is recorded
			// under the group of its owner (#470), which the entry has no
			// target to find.
			if e.GroupID, err = q.AccountGroupID(ctx, r.AccountID); err != nil {
				return err
			}
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
	Name      string
	Reason    string
	OwnerID   *int64
	AccountID *int64
	// GroupID limits the hold to the accounts whose owner is in this
	// customer group, also of users who join it later (#470). With an
	// owner or an account, they must be in the group (ErrInvalidHold).
	GroupID     *int64
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
	// AccountOwnerID is the owner of the hold's account; nil without an
	// account or for an account without owner.
	AccountOwnerID *int64
	// GroupID and Group: the customer group the hold names (#470).
	GroupID *int64
	Group   string
	// CoveredGroupID and CoveredGroup: the group whose mail the hold
	// covers: the one it names, or the group of its user or of its
	// account's owner; nil when it covers every group.
	CoveredGroupID *int64
	CoveredGroup   string
	Filter         *HoldFilter
	PeriodStart    *time.Time
	PeriodEnd      *time.Time
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
		var group *db.CustomerGroup
		if h.GroupID != nil {
			g, err := checkHoldGroup(ctx, q, h)
			if err != nil {
				return err
			}
			group = g
		}
		row, err := q.InsertLegalHold(ctx, db.InsertLegalHoldParams{
			Name: h.Name, Reason: h.Reason, OwnerID: h.OwnerID, AccountID: h.AccountID, GroupID: h.GroupID, Filter: filter,
			PeriodStart: h.PeriodStart, PeriodEnd: h.PeriodEnd, CreatedBy: h.CreatedBy,
		})
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" && pgErr.ConstraintName == "legal_holds_group_id_fkey" {
			return ErrUnknownGroup // removed concurrently
		}
		if err != nil {
			return err
		}
		id = row.ID
		if e := auditFrom(ctx); e != nil {
			e.Detail("hold_id", id)
			e.Detail("name", h.Name)
			auditHoldGroup(e, group)
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

// checkHoldGroup returns the group a new hold names; ErrUnknownGroup if
// there is none, ErrInvalidHold if the hold's user, or its account's
// owner, is in another group. Their rows stay locked FOR SHARE until the
// commit, so that neither a group change (SetUserGroup) nor an account
// move (SetAccountOwner) slips in between: those wait, and then see the
// hold.
func checkHoldGroup(ctx context.Context, q *db.Queries, h NewLegalHold) (*db.CustomerGroup, error) {
	g, err := q.GetGroupByID(ctx, *h.GroupID)
	if isNoRows(err) {
		return nil, ErrUnknownGroup
	}
	if err != nil {
		return nil, err
	}
	users := []*int64{h.OwnerID}
	if h.AccountID != nil {
		owner, err := q.LockAccountOwnerShare(ctx, *h.AccountID)
		if isNoRows(err) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		if owner == nil { // an account without owner is in no group
			return nil, ErrInvalidHold
		}
		users = append(users, owner)
	}
	for _, u := range users {
		if u == nil {
			continue
		}
		gid, err := q.LockUserGroupShare(ctx, *u)
		if isNoRows(err) {
			return nil, ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		if gid != g.ID {
			return nil, ErrInvalidHold
		}
	}
	return &g, nil
}

// auditHoldGroup records the group a hold names in its pending entry; a
// targeted entry without a user or account target is recorded under that
// group (#470).
func auditHoldGroup(e *AuditEntry, g *db.CustomerGroup) {
	if g == nil {
		return
	}
	e.Detail("group_id", g.ID)
	e.Detail("group", g.Name)
	if e.class == AuditTargeted && e.GroupID == nil {
		id := g.ID
		e.GroupID = &id
	}
}

// LegalHolds lists the active holds, and the released ones too with
// released, each with the number of messages its filter matched.
func (s *Store) LegalHolds(ctx context.Context, released bool) ([]LegalHold, error) {
	return s.legalHolds(ctx, db.ListLegalHoldsParams{Released: released})
}

// LegalHoldsCountingFor lists the holds as LegalHolds does, but counts the
// matches only of the caller's holds: those naming the caller, or naming
// no user and one of the caller's accounts. Matched is 0 for every other
// hold. The web UI shows no other count, and the list's time then does not
// depend on what other holds' filters matched in mail the caller cannot
// read (#538).
func (s *Store) LegalHoldsCountingFor(ctx context.Context, released bool, caller int64) ([]LegalHold, error) {
	return s.legalHolds(ctx, db.ListLegalHoldsParams{Released: released, CountFor: &caller})
}

func (s *Store) legalHolds(ctx context.Context, p db.ListLegalHoldsParams) ([]LegalHold, error) {
	rows, err := s.q.ListLegalHolds(ctx, p)
	if err != nil {
		return nil, err
	}
	out := make([]LegalHold, len(rows))
	for i, r := range rows {
		h := LegalHold{
			ID: r.ID, Name: r.Name, Reason: r.Reason, OwnerID: r.OwnerID, Owner: deref(r.Owner),
			AccountID: r.AccountID, Account: deref(r.Account), AccountOwner: deref(r.AccountOwner), AccountOwnerID: r.AccountOwnerID,
			PeriodStart: r.PeriodStart, PeriodEnd: r.PeriodEnd, MatchedAt: r.MatchedAt, Matched: r.Matched,
			CreatedAt: r.CreatedAt, CreatedBy: deref(r.CreatedBy), ReleasedAt: r.ReleasedAt,
			ReleasedBy: deref(r.ReleasedBy), ReleaseNote: deref(r.ReleaseNote),
			GroupID: r.GroupID, Group: deref(r.GroupName), CoveredGroupID: r.CoveredGroupID, CoveredGroup: deref(r.CoveredGroupName),
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
			if r.GroupID != nil {
				g, err := q.GetGroupByID(ctx, *r.GroupID)
				if err != nil {
					return err
				}
				auditHoldGroup(e, &g)
			}
		}
		return nil
	})
}

// HoldScope is whom a hold names; any of them may be nil.
type HoldScope struct {
	OwnerID, AccountID, GroupID *int64
}

// Instance reports whether the hold names no user, account or group: it is
// instance-level, and its entries have no group (#470).
func (h HoldScope) Instance() bool {
	return h.OwnerID == nil && h.AccountID == nil && h.GroupID == nil
}

// LegalHoldScope returns whom a hold names; ErrNotFound for an unknown
// hold.
func (s *Store) LegalHoldScope(ctx context.Context, id int64) (HoldScope, error) {
	r, err := s.q.LegalHoldScope(ctx, id)
	if isNoRows(err) {
		return HoldScope{}, ErrNotFound
	}
	if err != nil {
		return HoldScope{}, err
	}
	return HoldScope{OwnerID: r.OwnerID, AccountID: r.AccountID, GroupID: r.GroupID}, nil
}

// holdMatchPage is how many search results are stored at a time.
const holdMatchPage = 500

// MatchLegalHolds stores, for every active filter hold, the messages its
// filter finds now, and returns how many were new. Matches are only ever
// added. Search sees what a user sees, so the filter runs as the hold's
// user, as the owner of its account, for a group hold without either as
// every user of the group (#470), and otherwise as every user.
func (s *Store) MatchLegalHolds(ctx context.Context) (int64, error) {
	holds, err := s.q.ActiveFilterHolds(ctx, nil)
	if err != nil {
		return 0, err
	}
	var added int64
	for _, h := range holds {
		n, err := s.matchFilterHold(ctx, h)
		added += n
		if err != nil {
			return added, err
		}
	}
	return added, nil
}

// MatchLegalHold matches one active filter hold as MatchLegalHolds does,
// and no other: the web UI matches only the hold an admin just added, so
// the request's time depends only on that hold (#538). ErrNotFound if id
// is not an active filter hold.
func (s *Store) MatchLegalHold(ctx context.Context, id int64) (int64, error) {
	holds, err := s.q.ActiveFilterHolds(ctx, &id)
	if err != nil {
		return 0, err
	}
	if len(holds) != 1 {
		return 0, ErrNotFound
	}
	return s.matchFilterHold(ctx, holds[0])
}

// matchFilterHold stores the messages one filter hold's filter finds now
// and marks it matched; it returns how many were new.
func (s *Store) matchFilterHold(ctx context.Context, h db.ActiveFilterHoldsRow) (int64, error) {
	var f HoldFilter
	if err := json.Unmarshal(h.Filter, &f); err != nil {
		return 0, fmt.Errorf("legal hold #%d: %w", h.ID, err)
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
			return 0, err
		}
		for _, u := range users {
			if h.GroupID == nil || u.GroupID == *h.GroupID {
				owners = append(owners, u.ID)
			}
		}
	}
	var added int64
	for _, owner := range owners {
		n, err := s.matchHold(ctx, h.ID, owner, h.AccountID, f)
		added += n
		if err != nil {
			return added, fmt.Errorf("legal hold #%d: %w", h.ID, err)
		}
	}
	if err := s.q.SetLegalHoldMatched(ctx, db.SetLegalHoldMatchedParams{ID: h.ID, MatchedAt: started}); err != nil {
		return added, err
	}
	return added, nil
}

// matchHold matches a hold's filter as one owner, over that owner's own
// accounts only (of account, if set): a hold on a user never covers the
// accounts other users shared with them (#389). It never searches smtp
// envelopes: to: matches the To and Cc headers only.
func (s *Store) matchHold(ctx context.Context, holdID, owner int64, account *int64, f HoldFilter) (int64, error) {
	sc, err := s.OwnerScope(ctx, owner)
	if err != nil {
		return 0, err
	}
	if account != nil {
		sc = sc.Only(*account)
	}
	// Matches are stored per message, and every account's copy of a
	// message has the same bytes; only envelopes differ per account. A
	// filter that searched them would reveal one user's envelopes through
	// the hold on another's copy.
	sc = sc.WithoutEnvelopes()
	sf := SearchFilter{
		Scope: sc, Query: f.Query, From: f.From, To: f.To,
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

// HoldAddEntry is who added a legal hold, from its hold.add audit entry.
type HoldAddEntry struct {
	Actor    string // ActorSession or ActorCLI
	UserName string // the web user's name at the time; empty for the CLI
}

// LegalHoldAddEntry returns the hold.add audit entry of the hold id created
// at createdAt (LegalHold.CreatedAt), or nil when the log has none: its
// partition was detached or dropped, or the row was written by hand. The
// entry is written in the hold's own transaction, so its time is
// createdAt exactly, and the lookup reads that time's partition only.
func (s *Store) LegalHoldAddEntry(ctx context.Context, id int64, createdAt time.Time) (*HoldAddEntry, error) {
	r, err := s.q.HoldAddEntry(ctx, db.HoldAddEntryParams{At: createdAt, HoldID: id})
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &HoldAddEntry{Actor: r.Actor, UserName: deref(r.UserName)}, nil
}

// LegalHoldMatched returns how many messages one hold's filter matched so
// far.
func (s *Store) LegalHoldMatched(ctx context.Context, id int64) (int64, error) {
	return s.q.CountLegalHoldMessages(ctx, id)
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
	// GroupID and Group: the customer group of the owner (#470); nil and
	// empty without an owner.
	GroupID *int64
	Group   string
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
		out[i] = RetentionAccount{
			ID: r.ID, Name: r.Name, Kind: AccountKind(r.Kind), Removed: r.Removed, Owner: deref(r.Owner),
			GroupID: r.GroupID, Group: deref(r.GroupName), Folders: nonNil(r.Folders),
		}
	}
	return out, nil
}
