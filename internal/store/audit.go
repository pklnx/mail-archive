package store

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pklnx/mail-archive/internal/store/db"
)

// Who caused an audit entry.
const (
	ActorSession = "session" // the web UI, also for login attempts
	ActorAPIKey  = "api_key"
	ActorCLI     = "cli"
)

// What an audit entry is about.
const (
	TargetMessage = "message"
	TargetAccount = "account"
	TargetUser    = "user"
)

// ErrAuditUnavailable wraps every failure to write an audit entry. Whatever
// the entry was for has not happened.
var ErrAuditUnavailable = errors.New("the audit log cannot be written")

// ErrNotMigrated is returned when the database lacks the audit log, before
// the migrations ran.
var ErrNotMigrated = errors.New("the database schema is not up to date; run migrate")

// AuditEntry is one row of the audit log. The time comes from the database.
type AuditEntry struct {
	Actor      string
	UserID     *int64
	UserName   string
	APIKeyID   *int64
	APIKeyName string
	Action     string
	TargetType string
	TargetID   string
	TargetName string
	ClientIP   netip.Addr
	UserAgent  string
	Details    map[string]any
}

// SetUser records the user the entry is about or by.
func (e *AuditEntry) SetUser(id int64, name string) {
	e.UserID, e.UserName = &id, name
}

// SetTarget records what the entry is about.
func (e *AuditEntry) SetTarget(typ, id, name string) {
	e.TargetType, e.TargetID, e.TargetName = typ, id, name
}

// Detail adds a detail.
func (e *AuditEntry) Detail(key string, value any) {
	if e.Details == nil {
		e.Details = make(map[string]any)
	}
	e.Details[key] = value
}

type auditKey struct{}

// pendingAudit is an entry waiting for the transaction of the change it
// describes.
type pendingAudit struct {
	entry   *AuditEntry
	written bool
	// skip: the transaction changed nothing worth an entry (skipAudit).
	skip bool
}

// skipAudit keeps the current transaction from writing the pending entry,
// for a step that turned out to change nothing; a later one may write it.
func skipAudit(ctx context.Context) {
	if p, ok := ctx.Value(auditKey{}).(*pendingAudit); ok && !p.written {
		p.skip = true
	}
}

// WithAudit returns a context whose next store transaction also writes e,
// after the change succeeded and before the commit: the change and its
// entry are committed together or not at all. If the entry cannot be
// written, the change is rolled back and the error wraps
// ErrAuditUnavailable. The store method may complete e (the user of a
// passkey login, the result of a removal). Only one transaction writes it.
func WithAudit(ctx context.Context, e *AuditEntry) context.Context {
	return context.WithValue(ctx, auditKey{}, &pendingAudit{entry: e})
}

// auditFrom returns the entry still to be written with ctx's transaction.
func auditFrom(ctx context.Context) *AuditEntry {
	if p, ok := ctx.Value(auditKey{}).(*pendingAudit); ok && !p.written {
		return p.entry
	}
	return nil
}

// fillAuditTarget sets the ID of a target created in the transaction (a new
// account or user) if the pending entry is about such a target.
func fillAuditTarget(ctx context.Context, typ string, id int64) {
	if e := auditFrom(ctx); e != nil && e.TargetType == typ && e.TargetID == "" {
		e.TargetID = fmt.Sprint(id)
	}
}

// Audit writes one entry outside any transaction: for reads, searches,
// exports and failed logins. A missing partition is created and the insert
// tried once more. It runs on the pool, or on the dedicated connection of
// ctx (withConn).
func (s *Store) Audit(ctx context.Context, e AuditEntry) error {
	q := s.qFor(ctx)
	ua, err := s.auditUserAgent(ctx, &e)
	if err == nil {
		err = insertAudit(ctx, q, &e, ua)
	}
	if missingPartition(err) {
		if _, perr := s.EnsureAuditPartitions(ctx, 1); perr == nil {
			err = insertAudit(ctx, q, &e, ua)
		}
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrAuditUnavailable, err)
	}
	return nil
}

// auditUserAgent returns the id of e's user agent, or nil without one. It
// may insert outside any transaction, on a pool connection of its own or on
// the dedicated connection of ctx (withConn), so inTx calls it before the
// transaction starts: called with the transaction's connection held, it
// can wait for a free connection while every other one waits for a row
// lock that transaction holds.
func (s *Store) auditUserAgent(ctx context.Context, e *AuditEntry) (*int64, error) {
	v := cleanUserAgent(e.UserAgent)
	if v == "" {
		return nil, nil
	}
	id, err := s.userAgentID(ctx, v)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// insertAudit writes e with the user agent id ua through q, which may be a
// transaction's: it never uses the pool itself.
func insertAudit(ctx context.Context, q *db.Queries, e *AuditEntry, ua *int64) error {
	details, err := json.Marshal(cleanDetail(e.Details))
	if err != nil {
		return err
	}
	if string(details) == "null" {
		details = []byte("{}")
	}
	var ip *netip.Addr
	if e.ClientIP.IsValid() {
		a := e.ClientIP.Unmap()
		ip = &a
	}
	return q.InsertAuditEntry(ctx, db.InsertAuditEntryParams{
		Actor: e.Actor, UserID: e.UserID, UserName: nullString(e.UserName),
		ApiKeyID: e.APIKeyID, ApiKeyName: nullString(e.APIKeyName), Action: e.Action,
		TargetType: nullString(e.TargetType), TargetID: nullString(e.TargetID), TargetName: nullString(e.TargetName),
		ClientIp: ip, UserAgentID: ua, Details: details,
	})
}

// missingPartition reports the error of a row whose month has no partition.
func missingPartition(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514" && pgErr.ConstraintName == "" &&
		strings.Contains(pgErr.Message, "no partition")
}

// EnsureAuditPartitions creates the audit log partitions of this month and
// the next monthsAhead months that do not exist yet, and returns how many it
// created. It fails with ErrNotMigrated before the migrations ran. It runs
// on the pool, or on the dedicated connection of ctx (withConn).
func (s *Store) EnsureAuditPartitions(ctx context.Context, monthsAhead int) (int, error) {
	n, err := s.qFor(ctx).EnsureAuditPartitions(ctx, clampInt32(monthsAhead))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42883" { // undefined_function
		return 0, ErrNotMigrated
	}
	return int(n), err
}

// The user agent ids already looked up. They are inserted outside any
// transaction (autocommit), so a cached id always exists.
const maxCachedUserAgents = 1000

type userAgentCache struct {
	mu  sync.Mutex
	ids map[string]int64
}

func (s *Store) userAgentID(ctx context.Context, ua string) (int64, error) {
	c := &s.userAgents
	c.mu.Lock()
	id, ok := c.ids[ua]
	c.mu.Unlock()
	if ok {
		return id, nil
	}
	var err error
	for range 2 {
		// No row: a concurrent insert of the same string won; look again.
		if id, err = s.qFor(ctx).AuditUserAgentID(ctx, ua); err == nil || !isNoRows(err) {
			break
		}
	}
	if err != nil {
		return 0, err
	}
	c.mu.Lock()
	if c.ids == nil || len(c.ids) >= maxCachedUserAgents {
		c.ids = make(map[string]int64)
	}
	c.ids[ua] = id
	c.mu.Unlock()
	return id, nil
}

// maxUserAgent is the longest user agent stored, in bytes.
const maxUserAgent = 256

// cleanUserAgent makes a header value storable: valid UTF-8 without NUL,
// at most maxUserAgent bytes, cut at a character boundary.
func cleanUserAgent(ua string) string {
	ua = cleanString(ua)
	if len(ua) <= maxUserAgent {
		return ua
	}
	cut := maxUserAgent
	for cut > 0 && !utf8.RuneStart(ua[cut]) {
		cut--
	}
	return ua[:cut]
}

// cleanString replaces invalid UTF-8 and NUL, which PostgreSQL refuses in
// text and jsonb.
func cleanString(s string) string {
	return strings.ReplaceAll(strings.ToValidUTF8(s, "�"), "\x00", "�")
}

func cleanDetail(v any) any {
	switch v := v.(type) {
	case string:
		return cleanString(v)
	case []string:
		out := make([]string, len(v))
		for i, s := range v {
			out[i] = cleanString(s)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = cleanDetail(x)
		}
		return out
	case map[string]any:
		if v == nil {
			return nil
		}
		out := make(map[string]any, len(v))
		for k, x := range v {
			out[cleanString(k)] = cleanDetail(x)
		}
		return out
	}
	return v
}

func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// The action groups a filter may name instead of exact actions.
var auditGroups = map[string][]string{
	"login":     {"auth.%"},
	"search":    {"message.search", "message.list", "message.page"},
	"read":      {"message.open", "message.html", "message.raw", "message.part", "message.conversation"},
	"export":    {"data.%", "audit.export"},
	"accounts":  {"account.%"},
	"users":     {"user.%"},
	"profile":   {"profile.%"},
	"api_keys":  {"api_key.%"},
	"sync":      {"sync.%"},
	"retention": {"retention.%", "hold.%"},
}

var auditActionPattern = regexp.MustCompile(`^[a-z0-9_]+\.[a-z0-9_]+$`)

// AuditActionPatterns turns action filters (groups or exact actions) into
// LIKE patterns. No filter matches every action.
func AuditActionPatterns(filters []string) ([]string, error) {
	if len(filters) == 0 {
		return []string{"%"}, nil
	}
	var out []string
	for _, f := range filters {
		if g, ok := auditGroups[f]; ok {
			out = append(out, g...)
			continue
		}
		if !auditActionPattern.MatchString(f) {
			return nil, fmt.Errorf("unknown action %q", f)
		}
		out = append(out, strings.ReplaceAll(f, "_", `\_`))
	}
	return out, nil
}

// AuditFilter selects audit entries.
type AuditFilter struct {
	Since, Until time.Time
	UserID       *int64
	Actions      []string // groups or actions; none means all
}

// AuditRecord is a stored audit entry.
type AuditRecord struct {
	ID         int64
	At         time.Time
	Actor      string
	UserID     *int64
	UserName   string
	APIKeyName string
	Action     string
	TargetType string
	TargetID   string
	TargetName string
	ClientIP   string
	UserAgent  string
	Details    json.RawMessage
	// Subject of a target message, set only by ListAudit and only for a
	// message the viewer has a location of.
	Subject string
	// ViaShare: another user's read of the viewer's shared account, in a
	// non-admin's ListAudit; its details are cut down by shareView, and it
	// has no client address, user agent or API key name.
	ViaShare bool
}

// ExportAudit calls fn for every entry of f, oldest first.
func (s *Store) ExportAudit(ctx context.Context, f AuditFilter, fn func(AuditRecord) error) error {
	patterns, err := AuditActionPatterns(f.Actions)
	if err != nil {
		return err
	}
	const batch = 1000
	afterAt, afterID := f.Since, int64(0)
	for {
		rows, err := s.q.ExportAuditEntries(ctx, db.ExportAuditEntriesParams{
			Since: f.Since, Until: f.Until, UserID: f.UserID, Patterns: patterns,
			AfterAt: afterAt, AfterID: afterID, Lim: batch,
		})
		if err != nil {
			return err
		}
		for _, r := range rows {
			rec := AuditRecord{
				ID: r.ID, At: r.At, Actor: r.Actor, UserID: r.UserID, UserName: deref(r.UserName),
				APIKeyName: deref(r.ApiKeyName), Action: r.Action, TargetType: deref(r.TargetType),
				TargetID: deref(r.TargetID), TargetName: deref(r.TargetName), UserAgent: deref(r.UserAgent),
				Details: r.Details,
			}
			if r.ClientIp != nil {
				rec.ClientIP = r.ClientIp.String()
			}
			if err := fn(rec); err != nil {
				return err
			}
			afterAt, afterID = r.At, r.ID
		}
		if len(rows) < batch {
			return nil
		}
	}
}

// AuditPage selects a page of the Log page, newest first.
type AuditPage struct {
	// Viewer is who reads the log. Without All, only the viewer's own
	// entries are listed; with All (admins), every user's, optionally
	// narrowed to the entries whose user is one of UserIDs or whose actor is
	// one of Actors (a union, like several actions). Both empty means all.
	Viewer       int64
	All          bool
	UserIDs      []int64
	Actors       []string
	Since, Until time.Time
	Actions      []string // groups or actions; none means all
	// After the entry (CursorAt, CursorID) of the previous page.
	CursorAt *time.Time
	CursorID int64
	Limit    int
}

// ListAudit returns a page of entries, newest first. A non-admin's page
// also holds other users' reads of the viewer's shared accounts (#389),
// marked ViaShare and cut down by shareView. Subjects come from the
// viewer's current scope: a message the viewer cannot read shows none.
func (s *Store) ListAudit(ctx context.Context, p AuditPage) ([]AuditRecord, error) {
	patterns, err := AuditActionPatterns(p.Actions)
	if err != nil {
		return nil, err
	}
	var rows []db.ListAuditEntriesRow
	if p.All {
		rows, err = s.q.ListAuditEntries(ctx, db.ListAuditEntriesParams{
			Viewer: p.Viewer, Since: p.Since, Until: p.Until, UserIds: p.UserIDs, Actors: p.Actors,
			Patterns: patterns, CursorAt: p.CursorAt, CursorID: p.CursorID, Lim: clampInt32(p.Limit),
		})
	} else {
		var own []db.ListAuditEntriesForUserRow
		own, err = s.q.ListAuditEntriesForUser(ctx, db.ListAuditEntriesForUserParams{
			UserID: p.Viewer, Since: p.Since, Until: p.Until,
			Patterns: patterns, CursorAt: p.CursorAt, CursorID: p.CursorID, Lim: clampInt32(p.Limit),
		})
		for _, r := range own {
			rows = append(rows, db.ListAuditEntriesRow(r))
		}
	}
	if err != nil {
		return nil, err
	}
	out := make([]AuditRecord, 0, len(rows))
	for _, r := range rows {
		rec := AuditRecord{
			ID: r.ID, At: r.At, Actor: r.Actor, UserID: r.UserID, UserName: deref(r.UserName),
			APIKeyName: deref(r.ApiKeyName), Action: r.Action, TargetType: deref(r.TargetType),
			TargetID: deref(r.TargetID), TargetName: deref(r.TargetName), UserAgent: deref(r.UserAgent),
			Details: r.Details, Subject: deref(r.Subject),
		}
		if r.ClientIp != nil {
			rec.ClientIP = r.ClientIp.String()
		}
		out = append(out, rec)
	}
	if !p.All {
		// The two sets are disjoint (own entries have the viewer as user,
		// the shared reads never), and both are ordered and cut with the
		// same cursor: merged and cut to Limit, the page goes on where the
		// last kept row ends.
		shared, err := s.q.ListSharedReadsForOwner(ctx, db.ListSharedReadsForOwnerParams{
			OwnerID: p.Viewer, Since: p.Since, Until: p.Until,
			Patterns: patterns, CursorAt: p.CursorAt, CursorID: p.CursorID, Lim: clampInt32(p.Limit),
		})
		if err != nil {
			return nil, err
		}
		for _, r := range shared {
			details, err := shareView(r.Action, r.Details, p.Viewer)
			if err != nil {
				return nil, err
			}
			out = append(out, AuditRecord{
				ID: r.ID, At: r.At, Actor: r.Actor, UserID: r.UserID, UserName: deref(r.UserName), Action: r.Action,
				TargetType: deref(r.TargetType), TargetID: deref(r.TargetID), TargetName: deref(r.TargetName),
				Details: details, ViaShare: true,
			})
		}
		slices.SortFunc(out, func(a, b AuditRecord) int {
			if c := b.At.Compare(a.At); c != 0 {
				return c
			}
			return cmp.Compare(b.ID, a.ID)
		})
		if p.Limit >= 0 && len(out) > p.Limit {
			out = out[:p.Limit]
		}
	}
	if err := s.auditSubjects(ctx, p.Viewer, out); err != nil {
		return nil, err
	}
	return out, nil
}

// auditSubjects fills in the subjects of message entries still without
// one from the viewer's current scope (own and shared accounts), so an
// admin still sees no subject of mail they cannot read. A shared read's
// message.conversation entry whose requested message the viewer cannot
// read loses its target: only its hash would show.
func (s *Store) auditSubjects(ctx context.Context, viewer int64, recs []AuditRecord) error {
	var shas []string
	for _, r := range recs {
		if r.TargetType == TargetMessage && r.Subject == "" && r.TargetID != "" {
			shas = append(shas, r.TargetID)
		}
	}
	if len(shas) == 0 {
		return nil
	}
	sc, err := s.ViewerScope(ctx, viewer)
	if err != nil {
		return err
	}
	rows, err := s.q.AuditSubjects(ctx, db.AuditSubjectsParams{Shas: shas, Accounts: sc.ids()})
	if err != nil {
		return err
	}
	visible := make(map[string]string, len(rows))
	for _, r := range rows {
		visible[r.Sha256] = deref(r.Subject)
	}
	for i := range recs {
		r := &recs[i]
		if r.TargetType != TargetMessage || r.Subject != "" {
			continue
		}
		subject, ok := visible[r.TargetID]
		if !ok && r.ViaShare && r.Action == "message.conversation" {
			r.TargetType, r.TargetID, r.TargetName = "", "", ""
			continue
		}
		r.Subject = subject
	}
	return nil
}

// ViaAccount is one element of the details' via_accounts of a shared
// read (#389): an account shared with the reader whose mail the entry is
// about, with its owner at the time of the read. N, in list, page and
// conversation entries, counts the rows of that answer the reader sees
// only through this account.
type ViaAccount struct {
	ID    int64  `json:"id"`
	Owner int64  `json:"owner"`
	Key   string `json:"key"` // owner/name
	N     *int   `json:"n,omitempty"`
}

// Details of shared reads (#389). DetailViaOwners holds the owners' user
// IDs and makes the entry part of their Log (ListSharedReadsForOwner);
// DetailViaAccounts holds []ViaAccount.
const (
	DetailViaOwners   = "via_owners"
	DetailViaAccounts = "via_accounts"
)

// ShareSearchFields are the list parameters that make a list a search; a
// named entry also keeps ShareNamedFields in an owner's view.
var (
	ShareSearchFields = []string{"q", "from", "to", "attachment", "after", "before", "has"}
	ShareNamedFields  = []string{"q", "account", "folder", "from", "to", "attachment", "after", "before", "has", "gone", "group", "thread"}
)

// shareListActions are the entries of message lists.
var shareListActions = []string{"message.search", "message.list", "message.page"}

// shareView cuts the details of another user's read down to what the
// owner viewer may see (#389, revision 2, R1/R2). It is an allowlist:
//   - via_accounts keeps only the viewer's own accounts; via_owners and
//     every other key go, so no owner learns of other owners' shares;
//   - a named first page (message.search or message.list with "account")
//     keeps its list parameters, because named searches keep their terms
//     (decision 13);
//   - every other list entry, unnamed pages and named cursor pages
//     (message.page, decision 17), keeps only "search": whether any search
//     parameter was set, never the terms, folder or thread;
//   - other entries (message reads, conversations) keep the accounts only.
func shareView(action string, details []byte, viewer int64) (json.RawMessage, error) {
	var in map[string]json.RawMessage
	if err := json.Unmarshal(details, &in); err != nil {
		return nil, err
	}
	var via []ViaAccount
	if raw, ok := in[DetailViaAccounts]; ok {
		if err := json.Unmarshal(raw, &via); err != nil {
			return nil, err
		}
	}
	mine := []ViaAccount{}
	for _, a := range via {
		if a.Owner == viewer {
			mine = append(mine, a)
		}
	}
	out := map[string]any{DetailViaAccounts: mine}
	if slices.Contains(shareListActions, action) {
		// Named first pages keep their terms (decision 13); further pages,
		// named ones too, show only whether they searched (decision 17).
		if _, named := in["account"]; named && action != "message.page" {
			for _, k := range ShareNamedFields {
				if v, ok := in[k]; ok {
					out[k] = v
				}
			}
		} else {
			out["search"] = slices.ContainsFunc(ShareSearchFields, func(k string) bool { _, ok := in[k]; return ok })
		}
	}
	return json.Marshal(out)
}
