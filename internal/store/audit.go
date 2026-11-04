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
	"strconv"
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
// Start one with TargetedEntry, SelfEntry or InstanceEntry: its class says
// which customer group it is recorded under (#470), and an entry without a
// class is refused.
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
	// GroupID is set by the store for a targeted entry whose target is gone
	// or changes group in the change's transaction, and for group actions;
	// otherwise the group comes from the class (see AuditClass).
	GroupID *int64

	class AuditClass
}

// AuditClass says whose customer group an audit entry is recorded under
// (#470). It belongs to the entry, not to its action: a legal hold, for
// example, is targeted with an owner or account and instance-level without.
type AuditClass string

// The audit classes.
const (
	// AuditTargeted: about a user or an account (or a group). The group of
	// that user, or of the account's owner, as the change's transaction
	// sees it, unless GroupID says otherwise.
	AuditTargeted AuditClass = "targeted"
	// AuditSelf: the actor acting as themself (logins, own profile and
	// passkeys, own reads, also through a share): the actor's group.
	AuditSelf AuditClass = "self"
	// AuditInstance: the operator's work on the instance (deletion runs,
	// sweeps, holds without owner or account, backups, exports of the log,
	// failed logins of unknown names): no group, whoever the actor is.
	AuditInstance AuditClass = "instance"
)

// TargetedEntry starts an entry about a user, an account or a group.
func TargetedEntry(actor, action string) *AuditEntry {
	return &AuditEntry{Actor: actor, Action: action, class: AuditTargeted}
}

// SelfEntry starts an entry of the actor acting as themself.
func SelfEntry(actor, action string) *AuditEntry {
	return &AuditEntry{Actor: actor, Action: action, class: AuditSelf}
}

// InstanceEntry starts an entry of instance-level work, recorded without a
// group.
func InstanceEntry(actor, action string) *AuditEntry {
	return &AuditEntry{Actor: actor, Action: action, class: AuditInstance}
}

// Class returns the entry's class; empty for an entry not made by one of
// the constructors, which insertAudit refuses.
func (e *AuditEntry) Class() AuditClass { return e.class }

// auditActionClasses lists every action and the classes its entries may
// take. insertAudit refuses an action that is not listed or an entry of a
// class not listed for its action, so a new action has to be classified
// here, and a test checks every action the code names against this list.
var auditActionClasses = map[string][]AuditClass{
	"auth.login":        {AuditSelf},
	"auth.logout":       {AuditSelf},
	"auth.login_failed": {AuditSelf, AuditInstance}, // instance: the name is unknown

	"profile.change":          {AuditSelf},
	"profile.password_change": {AuditSelf},
	"profile.2fa_enable":      {AuditSelf},
	"profile.2fa_disable":     {AuditSelf},
	"profile.recovery_codes":  {AuditSelf},
	"profile.passkey_add":     {AuditSelf},
	"profile.passkey_remove":  {AuditSelf},

	"api_key.create": {AuditTargeted},
	"api_key.revoke": {AuditTargeted},

	"user.add":              {AuditTargeted},
	"user.change":           {AuditTargeted},
	"user.remove":           {AuditTargeted},
	"user.password_set":     {AuditTargeted},
	"user.password_reset":   {AuditTargeted},
	"user.2fa_reset":        {AuditTargeted},
	"user.passkeys_removed": {AuditTargeted},
	"user.group_change":     {AuditTargeted},

	"group.add":    {AuditTargeted},
	"group.rename": {AuditTargeted},
	"group.remove": {AuditTargeted},

	"account.add":          {AuditTargeted},
	"account.change":       {AuditTargeted},
	"account.remove":       {AuditTargeted},
	"account.move":         {AuditTargeted},
	"account.share":        {AuditTargeted},
	"account.unshare":      {AuditTargeted},
	"account.purge":        {AuditTargeted},
	"account.purge_batch":  {AuditTargeted},
	"account.purge_finish": {AuditTargeted},
	"account.purge_cancel": {AuditTargeted},

	"message.search":       {AuditSelf},
	"message.list":         {AuditSelf},
	"message.page":         {AuditSelf},
	"message.open":         {AuditSelf},
	"message.html":         {AuditSelf},
	"message.raw":          {AuditSelf},
	"message.part":         {AuditSelf},
	"message.conversation": {AuditSelf},

	"sync.start": {AuditTargeted, AuditSelf}, // self: all of the actor's own accounts

	// Auditor access (#393). Grant changes are about the auditor and are
	// recorded in the grant's group, which the store sets explicitly; an
	// auditor's own steps in the auditor's group (the grant's group).
	"auditor.grant":        {AuditTargeted},
	"auditor.extend":       {AuditTargeted},
	"auditor.revoke":       {AuditTargeted},
	"auditor.grant_change": {AuditTargeted},
	"auditor.select":       {AuditSelf},
	"auditor.denied":       {AuditSelf},
	"account.list":         {AuditSelf},

	"data.export":      {AuditTargeted},
	"data.import":      {AuditTargeted},
	"data.fill":        {AuditTargeted},
	"data.fill_change": {AuditTargeted},
	"data.fill_resume": {AuditTargeted},
	"data.fill_cancel": {AuditTargeted},
	"data.fill_done":   {AuditTargeted},
	"data.backup":      {AuditInstance},
	"audit.export":     {AuditInstance},
	"jobs.retry":       {AuditInstance},

	"retention.rule_add":    {AuditTargeted},
	"retention.rule_remove": {AuditTargeted},
	"hold.add":              {AuditTargeted, AuditInstance}, // instance: no owner and no account
	"hold.release":          {AuditTargeted, AuditInstance},
	"retention.preview":     {AuditInstance},
	"retention.confirm":     {AuditInstance},
	"retention.cancel":      {AuditInstance},
	"retention.batch":       {AuditInstance},
	"retention.finish":      {AuditInstance},
	"retention.sweep":       {AuditInstance},
}

// AuditActionClasses returns a copy of the classes each action may take.
func AuditActionClasses() map[string][]AuditClass {
	out := make(map[string][]AuditClass, len(auditActionClasses))
	for a, c := range auditActionClasses {
		out[a] = slices.Clone(c)
	}
	return out
}

// ErrAuditClass is returned for an entry whose class is missing or not
// allowed for its action.
var ErrAuditClass = errors.New("audit entry without a valid class")

// checkClass returns the user and the account whose group the entry is
// recorded under, by its class; refused without a class allowed for its
// action.
func checkClass(e *AuditEntry) (user, account *int64, err error) {
	allowed, ok := auditActionClasses[e.Action]
	if !ok || e.class == "" || !slices.Contains(allowed, e.class) {
		return nil, nil, fmt.Errorf("%w: %q as %q", ErrAuditClass, e.Action, e.class)
	}
	if e.GroupID != nil && e.class != AuditTargeted {
		return nil, nil, fmt.Errorf("%w: %q as %q with a group", ErrAuditClass, e.Action, e.class)
	}
	switch e.class {
	case AuditSelf:
		return e.UserID, nil, nil
	case AuditTargeted:
		// Only a numeric id of a user or account target counts; anything
		// else leaves the group NULL rather than failing the change.
		id, ok := targetNumber(e.TargetID)
		switch {
		case !ok:
		case e.TargetType == TargetUser:
			return &id, nil, nil
		case e.TargetType == TargetAccount:
			return nil, &id, nil
		}
	}
	return nil, nil, nil
}

// targetNumber parses a target id; false for one that is not a bigint.
func targetNumber(s string) (int64, bool) {
	id, err := strconv.ParseInt(s, 10, 64)
	return id, err == nil
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
	groupUser, groupAccount, err := checkClass(e)
	if err != nil {
		return err
	}
	var group *int64
	if e.class == AuditTargeted {
		group = e.GroupID
	}
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
		GroupID: group, GroupUserID: groupUser, GroupAccountID: groupAccount,
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
	"users":     {"user.%", "group.%"},
	"profile":   {"profile.%"},
	"api_keys":  {"api_key.%"},
	"sync":      {"sync.%"},
	"retention": {"retention.%", "hold.%"},
	"auditor":   {"auditor.%"},
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
	// GroupID keeps only the entries of one customer group; NoGroup only
	// those without one (#470): from before 1.4.0, or instance-level.
	GroupID *int64
	NoGroup bool
}

// ErrAuditGroupFilter is returned for a filter that names a group and asks
// for entries without one, or for a group filter on a page that is not an
// admin's.
var ErrAuditGroupFilter = errors.New("invalid customer group filter")

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
	// GroupID is the customer group the entry is recorded under (#470),
	// set by ExportAudit and by ListAudit of an admin's page (All); nil
	// without one.
	GroupID *int64
	// ViaShare: another user's read of the viewer's shared account, in a
	// non-admin's ListAudit; its details are cut down by shareView, and it
	// has no client address, user agent or API key name.
	ViaShare bool
}

// AuditGroupKnown reports whether entries can be filtered by the group id:
// a current group, or a removed one whose id entries still carry (#470),
// so that an offboarded customer's entries can still be found.
func (s *Store) AuditGroupKnown(ctx context.Context, id int64) (bool, error) {
	known, err := s.q.AuditGroupKnown(ctx, id)
	return known != nil && *known, err
}

// ExportAudit calls fn for every entry of f, oldest first.
func (s *Store) ExportAudit(ctx context.Context, f AuditFilter, fn func(AuditRecord) error) error {
	patterns, err := AuditActionPatterns(f.Actions)
	if err != nil {
		return err
	}
	if f.GroupID != nil && f.NoGroup {
		return ErrAuditGroupFilter
	}
	const batch = 1000
	afterAt, afterID := f.Since, int64(0)
	for {
		rows, err := s.q.ExportAuditEntries(ctx, db.ExportAuditEntriesParams{
			Since: f.Since, Until: f.Until, UserID: f.UserID, GroupID: f.GroupID, NoGroup: f.NoGroup, Patterns: patterns,
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
				Details: r.Details, GroupID: r.GroupID,
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
	Viewer  int64
	All     bool
	UserIDs []int64
	Actors  []string
	// GroupID keeps only the entries of one customer group, NoGroup only
	// those without one (#470); both only with All. They intersect with
	// UserIDs and Actors.
	GroupID      *int64
	NoGroup      bool
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
	if (p.GroupID != nil || p.NoGroup) && (!p.All || (p.GroupID != nil && p.NoGroup)) {
		return nil, ErrAuditGroupFilter
	}
	var rows []db.ListAuditEntriesRow
	switch {
	case p.GroupID != nil:
		var group []db.ListAuditEntriesForGroupRow
		group, err = s.q.ListAuditEntriesForGroup(ctx, db.ListAuditEntriesForGroupParams{
			Viewer: p.Viewer, GroupID: *p.GroupID, Since: p.Since, Until: p.Until, UserIds: p.UserIDs, Actors: p.Actors,
			Patterns: patterns, CursorAt: p.CursorAt, CursorID: p.CursorID, Lim: clampInt32(p.Limit),
		})
		for _, r := range group {
			rows = append(rows, db.ListAuditEntriesRow(r))
		}
	case p.All:
		rows, err = s.q.ListAuditEntries(ctx, db.ListAuditEntriesParams{
			Viewer: p.Viewer, Since: p.Since, Until: p.Until, UserIds: p.UserIDs, Actors: p.Actors, NoGroup: p.NoGroup,
			Patterns: patterns, CursorAt: p.CursorAt, CursorID: p.CursorID, Lim: clampInt32(p.Limit),
		})
	default:
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
		if p.All {
			rec.GroupID = r.GroupID
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
			// Grant entries written by others (an admin's revocation, a
			// group move) carry the owner in via_owners but need their own
			// view (auditorView, #393 part 2), which also hides the actor's
			// name; until then the owner's page leaves them out.
			if strings.HasPrefix(r.Action, "auditor.") {
				continue
			}
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
	all, folderAccounts, folders := sc.folderParams()
	rows, err := s.q.AuditSubjects(ctx, db.AuditSubjectsParams{
		Shas: shas, Accounts: sc.ids(), ScopeAllFolderAccounts: all, ScopeFolderAccounts: folderAccounts, ScopeFolders: folders, ScopeAfter: sc.after, ScopeBefore: sc.before,
	})
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
