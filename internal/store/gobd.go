package store

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pklnx/mail-archive/internal/attest"
	"github.com/pklnx/mail-archive/internal/store/db"
)

// GoBD export (#394, part 2): the export reads everything in one
// repeatable read transaction on one dedicated connection, GoBDConn. What
// it selects is a Scope (OwnerScope, RestrictScope, MergeScopes), the
// same predicate as every other read, plus the cutoff of a resumed export
// and the legal hold of an export by hold.

// gobdPage is how many rows a pass reads per query.
const gobdPage = 1000

// GoBDMember is a selected user or account, or the claimed group, with its
// customer group: an account's is its owner's, nil without an owner.
type GoBDMember struct {
	Kind    string // "user", "account" or "group"
	ID      int64
	Name    string // an account as owner/name, or name without an owner
	GroupID *int64
	Group   string
}

// GoBDSelectionGroups returns the groups of the given users and accounts
// and the group groupID (if not nil), on the pool: the command line's check
// before the audit entry. The export repeats it in its snapshot.
func (s *Store) GoBDSelectionGroups(ctx context.Context, users, accounts []int64, groupID *int64) ([]GoBDMember, error) {
	return selectionGroups(ctx, s.q, users, accounts, groupID)
}

func selectionGroups(ctx context.Context, q *db.Queries, users, accounts []int64, groupID *int64) ([]GoBDMember, error) {
	rows, err := q.GoBDSelectionGroups(ctx, db.GoBDSelectionGroupsParams{
		UserIds: nonNilIDs(users), AccountIds: nonNilIDs(accounts), GroupID: groupID,
	})
	if err != nil {
		return nil, err
	}
	out := make([]GoBDMember, len(rows))
	for i, r := range rows {
		out[i] = GoBDMember{Kind: r.Kind, ID: r.ID, Name: r.Name, GroupID: r.GroupID, Group: deref(r.GroupName)}
	}
	return out, nil
}

func nonNilIDs(ids []int64) []int64 {
	if ids == nil {
		return []int64{}
	}
	return ids
}

// GoBDConn is the dedicated connection of one export.
type GoBDConn struct {
	s    *Store
	conn *pgxpool.Conn
	// broken: a transaction may be left open; Close closes the
	// connection instead of returning it to the pool.
	broken bool
}

// GoBDConn acquires the export's connection. Close it when done.
func (s *Store) GoBDConn(ctx context.Context) (*GoBDConn, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	return &GoBDConn{s: s, conn: conn}, nil
}

// Close returns the connection, or closes it if it is in an unknown state.
func (c *GoBDConn) Close() {
	if c.conn == nil {
		return
	}
	if c.broken {
		_ = c.conn.Conn().Close(context.Background())
	}
	c.conn.Release()
	c.conn = nil
}

// NewMessages reports whether messages were archived after the newest
// attestation (or any, without one).
func (c *GoBDConn) NewMessages(ctx context.Context) (bool, error) {
	return db.New(c.conn).GoBDNewMessages(ctx)
}

// WaitForAttestation waits until no attestation is being written: it
// takes the attestation writer lock, blocking (bounded by ctx), and
// releases it at once. The export then uses what that attestation wrote.
func (c *GoBDConn) WaitForAttestation(ctx context.Context) error {
	if _, err := c.conn.Exec(ctx, "SELECT pg_advisory_lock($1, 0)", attestWriterClass); err != nil {
		// A cancelled wait leaves the lock request in an unknown state.
		c.broken = true
		return err
	}
	if _, err := c.conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1, 0)", attestWriterClass); err != nil {
		c.broken = true
		return err
	}
	return nil
}

// SelectionGroups is GoBDSelectionGroups on the export's connection, read
// committed: after the snapshot, it shows which groups changed since.
func (c *GoBDConn) SelectionGroups(ctx context.Context, users, accounts []int64, groupID *int64) ([]GoBDMember, error) {
	return selectionGroups(ctx, db.New(c.conn), users, accounts, groupID)
}

// Snapshot runs fn in one read-only repeatable read transaction on the
// export's connection. PostgreSQL takes the snapshot at the first
// statement fn runs, so fn starts with the group check
// (GoBDSnapshot.SelectionGroups). The transaction ends when fn returns.
func (c *GoBDConn) Snapshot(ctx context.Context, fn func(*GoBDSnapshot) error) error {
	tx, err := c.conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		c.broken = true
		return err
	}
	snap := &GoBDSnapshot{s: c.s, q: c.s.q.WithTx(tx), conn: c.conn}
	if err := fn(snap); err != nil {
		if rerr := tx.Rollback(context.WithoutCancel(ctx)); rerr != nil {
			c.broken = true
		}
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		c.broken = true
		return err
	}
	return nil
}

// GoBDSnapshot reads the export's snapshot.
type GoBDSnapshot struct {
	s    *Store
	q    *db.Queries
	conn *pgxpool.Conn
}

// in returns ctx for the store's own methods (OwnerScope, RestrictScope),
// which then run on the export's connection, inside the transaction.
func (g *GoBDSnapshot) in(ctx context.Context) context.Context { return withConn(ctx, g.conn) }

// SelectionGroups is GoBDSelectionGroups in the snapshot.
func (g *GoBDSnapshot) SelectionGroups(ctx context.Context, users, accounts []int64, groupID *int64) ([]GoBDMember, error) {
	return selectionGroups(ctx, g.q, users, accounts, groupID)
}

// GoBDUser is a user of a group export.
type GoBDUser struct {
	ID   int64
	Name string
}

// GroupUsers returns the users of a group, auditors left out, by name.
func (g *GoBDSnapshot) GroupUsers(ctx context.Context, groupID int64) ([]GoBDUser, error) {
	rows, err := g.q.GoBDGroupUsers(ctx, groupID)
	if err != nil {
		return nil, err
	}
	out := make([]GoBDUser, len(rows))
	for i, r := range rows {
		out[i] = GoBDUser{ID: r.ID, Name: r.Name}
	}
	return out, nil
}

// Hold returns whom a legal hold names and whether it was released;
// ErrNotFound for an unknown hold.
func (g *GoBDSnapshot) Hold(ctx context.Context, id int64) (HoldScope, bool, error) {
	r, err := g.q.GoBDHold(ctx, id)
	if isNoRows(err) {
		return HoldScope{}, false, ErrNotFound
	}
	if err != nil {
		return HoldScope{}, false, err
	}
	return HoldScope{OwnerID: r.OwnerID, AccountID: r.AccountID, GroupID: r.GroupID}, r.Released, nil
}

// GoBDGrant is the scope an auditor grant names: its owner, its accounts
// (a nil folder list for all folders) and its period.
type GoBDGrant struct {
	OwnerID                 int64
	Accounts                map[int64][]int64
	PeriodFrom, PeriodUntil *time.Time
	Revoked                 bool
}

// Grant returns the scope of an auditor grant as it is recorded, whether
// or not the grant is active; ErrNotFound for an unknown grant.
func (g *GoBDSnapshot) Grant(ctx context.Context, id int64) (*GoBDGrant, error) {
	r, err := g.q.GoBDGrant(ctx, id)
	if isNoRows(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	accs, err := g.q.GoBDGrantAccounts(ctx, id)
	if err != nil {
		return nil, err
	}
	out := &GoBDGrant{OwnerID: r.OwnerID, PeriodFrom: r.PeriodFrom, PeriodUntil: r.PeriodUntil, Revoked: r.Revoked,
		Accounts: make(map[int64][]int64, len(accs))}
	for _, a := range accs {
		if a.AllFolders {
			out.Accounts[a.AccountID] = nil
		} else {
			out.Accounts[a.AccountID] = append([]int64{}, a.Folders...)
		}
	}
	return out, nil
}

// OwnerScope is Store.OwnerScope in the snapshot.
func (g *GoBDSnapshot) OwnerScope(ctx context.Context, userID int64) (Scope, error) {
	return g.s.OwnerScope(g.in(ctx), userID)
}

// RestrictScope is Store.RestrictScope in the snapshot.
func (g *GoBDSnapshot) RestrictScope(ctx context.Context, sc Scope, folders map[int64][]int64, after, before *time.Time) (Scope, error) {
	return g.s.RestrictScope(g.in(ctx), sc, folders, after, before)
}

// SplitAccounts splits accounts by whether they have an owner: owned maps
// each owned account to its owner (whose OwnerScope holds it), unowned is
// a scope of the others, each with all its folders. Unknown IDs are left
// out.
func (g *GoBDSnapshot) SplitAccounts(ctx context.Context, ids []int64) (owned map[int64]int64, unowned Scope, err error) {
	rows, err := g.q.GoBDAccounts(ctx, nonNilIDs(ids))
	if err != nil {
		return nil, Scope{}, err
	}
	owned = map[int64]int64{}
	unowned = Scope{Accounts: []ScopeAccount{}, envelopes: []int64{}}
	for _, r := range rows {
		if r.OwnerID != nil {
			owned[r.ID] = *r.OwnerID
			continue
		}
		unowned.Accounts = append(unowned.Accounts, ScopeAccount{ID: r.ID, Name: r.Name, Kind: AccountKind(r.Kind), Removed: r.Removed})
	}
	return owned, unowned, nil
}

// ErrScopePeriod is returned by MergeScopes for a scope with a period.
var ErrScopePeriod = errors.New("only scopes without a period can be merged")

// MergeScopes returns the union of scopes without a period: every account
// of any of them. An account in two of them keeps the union of its
// folders (all folders if either has all). Restrict narrows the result.
func MergeScopes(scopes ...Scope) (Scope, error) {
	out := Scope{Accounts: []ScopeAccount{}, envelopes: []int64{}}
	at := map[int64]int{}
	for _, sc := range scopes {
		if sc.after != nil || sc.before != nil {
			return Scope{}, ErrScopePeriod
		}
		for _, a := range sc.Accounts {
			i, seen := at[a.ID]
			if !seen {
				a.Folders = cloneFolders(a.Folders)
				at[a.ID] = len(out.Accounts)
				out.Accounts = append(out.Accounts, a)
				continue
			}
			have := &out.Accounts[i]
			switch {
			case have.Folders == nil:
			case a.Folders == nil:
				have.Folders = nil
			default:
				for _, f := range a.Folders {
					if !slices.Contains(have.Folders, f) {
						have.Folders = append(have.Folders, f)
					}
				}
			}
		}
		for _, e := range sc.envelopes {
			if !slices.Contains(out.envelopes, e) {
				out.envelopes = append(out.envelopes, e)
			}
		}
	}
	slices.SortFunc(out.Accounts, func(a, b ScopeAccount) int { return cmp.Compare(a.ID, b.ID) })
	return out, nil
}

func cloneFolders(f []int64) []int64 {
	if f == nil {
		return nil
	}
	return append([]int64{}, f...)
}

// MaxLocationID returns the highest location ID in the snapshot, the
// cutoff a resumed export keeps to.
func (g *GoBDSnapshot) MaxLocationID(ctx context.Context) (int64, error) {
	return g.q.GoBDMaxLocationID(ctx)
}

// LastAttestation returns the newest attestation in the snapshot, nil if
// there is none.
func (g *GoBDSnapshot) LastAttestation(ctx context.Context) (*attest.Entry, error) {
	r, err := g.q.GoBDLastAttestation(ctx)
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	e := entryFromRow(&r)
	return &e, nil
}

// Chain returns the attestations up to seq upto, in order.
func (g *GoBDSnapshot) Chain(ctx context.Context, upto int64) ([]attest.Entry, error) {
	rows, err := g.q.GoBDChain(ctx, upto)
	if err != nil {
		return nil, err
	}
	out := make([]attest.Entry, len(rows))
	for i := range rows {
		out[i] = entryFromRow(&rows[i])
	}
	return out, nil
}

// GoBDFilter is what an export selects: the locations of the scope's
// accounts and folders whose message's sort_at is in the scope's period,
// up to MaxLocationID, and with HoldID only those the hold covers. A
// message is selected if one of its locations is.
type GoBDFilter struct {
	Scope         Scope
	MaxLocationID int64
	HoldID        *int64
}

type gobdParams struct {
	after, before                                 *time.Time
	accounts, allFolderAccounts, folderAccs, fids []int64
}

func (f *GoBDFilter) params() gobdParams {
	all, accs, fids := f.Scope.folderParams()
	after, before := f.Scope.Period()
	return gobdParams{after: after, before: before, accounts: f.Scope.ids(), allFolderAccounts: all, folderAccs: accs, fids: fids}
}

// Totals returns the number and bytes of the selected messages.
func (g *GoBDSnapshot) Totals(ctx context.Context, f GoBDFilter) (messages, bytes int64, err error) {
	p := f.params()
	r, err := g.q.GoBDTotals(ctx, db.GoBDTotalsParams{
		ScopeAfter: p.after, ScopeBefore: p.before, MaxLocationID: f.MaxLocationID, Accounts: p.accounts,
		ScopeAllFolderAccounts: p.allFolderAccounts, ScopeFolderAccounts: p.folderAccs, ScopeFolders: p.fids, HoldID: f.HoldID,
	})
	return r.Messages, r.Bytes, err
}

// GoBDMessage is a selected message with its messages.csv fields.
type GoBDMessage struct {
	Seq                                         int64
	SHA256                                      string
	Size                                        int64
	StoredPath                                  string
	SortAt, ArchivedAt                          time.Time
	From, To, Cc, Subject, MessageID, InReplyTo string
	HasAttachment                               bool
	AttachmentNames                             []string
	// Attestation is the seq of the first attestation up to the export's
	// whose range holds the message; 0 for none.
	Attestation int64
}

// Messages calls fn for every selected message in seq order, a page at a
// time. attUpto is the newest attestation the export names.
func (g *GoBDSnapshot) Messages(ctx context.Context, f GoBDFilter, attUpto int64, fn func(*GoBDMessage) error) error {
	p := f.params()
	after := int64(0)
	for {
		rows, err := g.q.GoBDMessages(ctx, db.GoBDMessagesParams{
			AttUpto: attUpto, AfterSeq: after, ScopeAfter: p.after, ScopeBefore: p.before, MaxLocationID: f.MaxLocationID,
			Accounts: p.accounts, ScopeAllFolderAccounts: p.allFolderAccounts, ScopeFolderAccounts: p.folderAccs,
			ScopeFolders: p.fids, HoldID: f.HoldID, PageSize: gobdPage,
		})
		if err != nil {
			return err
		}
		for i := range rows {
			r := &rows[i]
			m := GoBDMessage{
				Seq: r.Seq, SHA256: r.Sha256, Size: r.Size, StoredPath: r.StoredPath, SortAt: r.SortAt, ArchivedAt: r.CreatedAt,
				From: r.FromAddr, To: r.ToAddr, Cc: r.CcAddr, Subject: r.Subject, MessageID: r.MessageID, InReplyTo: r.InReplyTo,
				HasAttachment: r.HasAttachment, Attestation: r.Attestation,
			}
			if r.AttachmentNames != "" {
				m.AttachmentNames = strings.FieldsFunc(r.AttachmentNames, func(c rune) bool { return c == '\n' })
			}
			if err := fn(&m); err != nil {
				return err
			}
			after = r.Seq
		}
		if len(rows) < gobdPage {
			return nil
		}
	}
}

// SelectedAmong returns which of the given messages are selected, with
// their sort_at.
func (g *GoBDSnapshot) SelectedAmong(ctx context.Context, f GoBDFilter, shas []string) (map[string]time.Time, error) {
	p := f.params()
	rows, err := g.q.GoBDSelectedAmong(ctx, db.GoBDSelectedAmongParams{
		Shas: shas, ScopeAfter: p.after, ScopeBefore: p.before, MaxLocationID: f.MaxLocationID, Accounts: p.accounts,
		ScopeAllFolderAccounts: p.allFolderAccounts, ScopeFolderAccounts: p.folderAccs, ScopeFolders: p.fids, HoldID: f.HoldID,
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]time.Time, len(rows))
	for _, r := range rows {
		out[r.Sha256] = r.SortAt
	}
	return out, nil
}

// GoBDLocation is a selected location with its locations.csv fields.
type GoBDLocation struct {
	SHA256, Owner, Account, AccountKind, Folder string
	UIDValidity, UID                            int64
	Flags                                       []string
	InternalDate                                *time.Time
	FirstSeen, LastSeen                         time.Time
	GoneAt                                      *time.Time
	EnvelopeFrom                                string
	EnvelopeTo                                  []string
	ReceivedAt, RetainUntil                     *time.Time
	Holds                                       []int64
}

// Locations calls fn for every selected location in id order, a page at a
// time. withHolds fills Holds with the active holds that name an owner,
// an account or a group.
func (g *GoBDSnapshot) Locations(ctx context.Context, f GoBDFilter, withHolds bool, fn func(*GoBDLocation) error) error {
	p := f.params()
	after := int64(0)
	for {
		rows, err := g.q.GoBDLocations(ctx, db.GoBDLocationsParams{
			WithHolds: withHolds, AfterID: after, MaxLocationID: f.MaxLocationID, ScopeAfter: p.after, ScopeBefore: p.before,
			Accounts: p.accounts, ScopeAllFolderAccounts: p.allFolderAccounts, ScopeFolderAccounts: p.folderAccs,
			ScopeFolders: p.fids, HoldID: f.HoldID, PageSize: gobdPage,
		})
		if err != nil {
			return err
		}
		for i := range rows {
			r := &rows[i]
			l := GoBDLocation{
				SHA256: r.Sha256, Owner: r.Owner, Account: r.Account, AccountKind: r.Kind, Folder: r.Folder,
				UIDValidity: r.Uidvalidity, UID: r.Uid, Flags: r.Flags, InternalDate: r.InternalDate,
				FirstSeen: r.FirstSeenAt, LastSeen: r.LastSeenAt, GoneAt: r.GoneAt,
				EnvelopeFrom: deref(r.MailFrom), EnvelopeTo: r.RcptTo, ReceivedAt: r.ReceivedAt, Holds: r.Holds,
			}
			if t, ok := r.RetainUntil.(time.Time); ok {
				l.RetainUntil = &t
			}
			if err := fn(&l); err != nil {
				return err
			}
			after = r.ID
		}
		if len(rows) < gobdPage {
			return nil
		}
	}
}
