package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pklnx/mail-archive/internal/store/db"
)

// Purges (#155) delete a removed account completely: its locations,
// folders and the account row. A message whose last location goes is
// deleted like a retention deletion (tombstone, deletion record, file
// removal after the grace period); one that is also in another account
// stays there unchanged. A purge is previewed, confirmed with the number
// of messages it deletes, and executed in batches
// (archive.ExecutePurge), each in one transaction under the exclusive
// blob lock and the retention lock.

// purgeLockClass is the first key of the advisory lock that lets one
// process execute a purge ("mg" in ASCII); the second key is always 0.
const purgeLockClass = 0x6d67

// States of a purge.
const (
	PurgeConfirmed = "confirmed"
	PurgeDone      = "done"
	PurgeCancelled = "cancelled"
)

// ErrNotRemoved is returned for a purge of an account that is not removed.
var ErrNotRemoved = errors.New("the account is not removed")

// ErrRetained is returned for a purge of an account that a retention rule
// covers.
var ErrRetained = errors.New("retention rules cover the account")

// ErrAccountBusy is returned for a purge of an account that is being
// synced, filled or imported into.
var ErrAccountBusy = errors.New("the account is being synced, filled or imported into")

// ErrPurgeOpen is returned when a purge is confirmed while another one is
// open.
var ErrPurgeOpen = errors.New("an account purge is open")

// ErrPurgeState is returned for a step the purge's state does not allow.
var ErrPurgeState = errors.New("not possible in the purge's state")

// ErrPurgeChanged is returned when the number of messages a purge deletes
// is not the confirmed one; see PurgeChangedError.
var ErrPurgeChanged = errors.New("the number of messages to delete changed")

// PurgeChangedError tells the number of messages a purge would delete now.
type PurgeChangedError struct {
	Expected int64 // what the admin confirmed
	Now      int64 // what the purge deletes now (deleted so far included)
}

func (e *PurgeChangedError) Error() string {
	return fmt.Sprintf("the number of messages to delete is now %d, not %d; review again", e.Now, e.Expected)
}

// Is makes errors.Is(err, ErrPurgeChanged) true.
func (e *PurgeChangedError) Is(target error) bool { return target == ErrPurgeChanged }

// PurgeRule is a rule that keeps an account from being purged.
type PurgeRule struct {
	ID     int64
	Folder string // empty: all folders
	Years  int
}

// PurgeHold is a legal hold that keeps an account from being purged: it
// names the account (also when released) or covers some of its mail.
type PurgeHold struct {
	ID           int64
	Name         string
	Released     bool
	NamesAccount bool
}

// PurgeBlockers are the reasons an account cannot be purged now.
type PurgeBlockers struct {
	NotRemoved bool
	Syncing    bool
	// OpenPurge is another account's open purge.
	OpenPurge *AccountPurge
	Rules     []PurgeRule
	Holds     []PurgeHold
}

// Any reports whether there is a blocker.
func (b *PurgeBlockers) Any() bool {
	return b.NotRemoved || b.Syncing || b.OpenPurge != nil || len(b.Rules) > 0 || len(b.Holds) > 0
}

// Messages words the blockers, one per line, naming the rules and holds.
func (b *PurgeBlockers) Messages(account string) []string {
	var out []string
	if b.NotRemoved {
		out = append(out, fmt.Sprintf("account %q is not removed; remove it first", account))
	}
	if b.Syncing {
		out = append(out, fmt.Sprintf("account %q is being synced, filled or imported into; try again later", account))
	}
	if p := b.OpenPurge; p != nil {
		out = append(out, fmt.Sprintf("purge %d of account %q is open; it must finish or be cancelled first", p.ID, p.Account))
	}
	if len(b.Rules) > 0 {
		parts := make([]string, len(b.Rules))
		for i, r := range b.Rules {
			where := "all folders"
			if r.Folder != "" {
				where = fmt.Sprintf("folder %q", r.Folder)
			}
			parts[i] = fmt.Sprintf("#%d (%s, %d years)", r.ID, where, r.Years)
		}
		word, them := "rule", "it"
		if len(parts) > 1 {
			word, them = "rules", "them"
		}
		out = append(out, fmt.Sprintf("account %q has retention %s %s; remove %s first", account, word, strings.Join(parts, ", "), them))
	}
	for _, h := range b.Holds {
		switch {
		case h.Released:
			out = append(out, fmt.Sprintf("released legal hold #%d %q names account %q; its record keeps the account", h.ID, h.Name, account))
		case h.NamesAccount:
			out = append(out, fmt.Sprintf("legal hold #%d %q names account %q", h.ID, h.Name, account))
		default:
			out = append(out, fmt.Sprintf("legal hold #%d %q covers mail of account %q", h.ID, h.Name, account))
		}
	}
	return out
}

// PurgeBlockedError is returned when blockers keep an account from being
// purged. errors.Is matches ErrNotRemoved, ErrAccountBusy, ErrPurgeOpen,
// ErrRetained and ErrOnHold for the blockers it has.
type PurgeBlockedError struct {
	Account  string
	Blockers PurgeBlockers
}

func (e *PurgeBlockedError) Error() string {
	return strings.Join(e.Blockers.Messages(e.Account), "; ")
}

// Is matches the sentinel errors of the blockers.
func (e *PurgeBlockedError) Is(target error) bool {
	b := &e.Blockers
	switch target {
	case ErrNotRemoved:
		return b.NotRemoved
	case ErrAccountBusy:
		return b.Syncing
	case ErrPurgeOpen:
		return b.OpenPurge != nil
	case ErrRetained:
		return len(b.Rules) > 0
	case ErrOnHold:
		return len(b.Holds) > 0
	}
	return false
}

// AccountPurge is a purge and its counts.
type AccountPurge struct {
	ID             int64
	AccountID      int64
	Account        string
	Kind           AccountKind
	AccountVersion int64
	OwnerID        *int64
	Owner          string
	State          string
	Expected       int
	CreatedAt      time.Time
	CreatedBy      string // empty: the command line
	FinishedAt     *time.Time
	Locations      int64
	Deleted        int
	Kept           int
	Note           string // why it was cancelled
}

func purgeFromRow(r db.GetAccountPurgeRow) *AccountPurge {
	return &AccountPurge{
		ID: r.ID, AccountID: r.AccountID, Account: r.AccountName, Kind: AccountKind(r.AccountKind), AccountVersion: r.AccountVersion,
		OwnerID: r.OwnerID, Owner: deref(r.OwnerName), State: r.State, Expected: int(r.Expected), CreatedAt: r.CreatedAt,
		CreatedBy: deref(r.CreatedBy), FinishedAt: r.FinishedAt, Locations: r.Locations, Deleted: int(r.Deleted), Kept: int(r.Kept),
		Note: deref(r.Note),
	}
}

// PurgeKept counts the messages a purge keeps by where else they are.
type PurgeKept struct {
	Owner    string // empty: no owner
	Account  string
	Messages int64
}

// PurgePreview is what a purge of an account would do now.
type PurgePreview struct {
	Account *Account
	Owner   string // empty: no owner
	// Locations to remove: every location in the account's folders.
	Locations int64
	// Messages in the account; Deleted of them have no location elsewhere
	// and are deleted, Kept are also in other accounts and stay there.
	Messages     int64
	Deleted      int64
	DeletedBytes int64
	Kept         int64
	KeptIn       []PurgeKept
	Blockers     PurgeBlockers
	// Open is this account's open purge, if there is one.
	Open *AccountPurge
}

// PurgePreview returns what a purge of the account would do and what
// keeps it from being purged. It changes nothing; filter holds count as
// matching everything in their scope until MatchLegalHolds stored their
// matches.
func (s *Store) PurgePreview(ctx context.Context, accountID int64) (*PurgePreview, error) {
	syncing, err := s.SyncingAccounts(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.q.GetAccount(ctx, accountID)
	if isNoRows(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return purgePreview(ctx, s.q, r, syncing[accountID])
}

func purgePreview(ctx context.Context, q *db.Queries, r db.Account, syncing bool) (*PurgePreview, error) {
	p := &PurgePreview{Account: accountFromDB(r)}
	if r.OwnerID != nil {
		u, err := q.GetUserByID(ctx, *r.OwnerID)
		if err != nil {
			return nil, err
		}
		p.Owner = u.Name
	}
	c, err := q.PurgeCounts(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	p.Locations, p.Messages, p.Deleted, p.DeletedBytes, p.Kept = c.Locations, c.Messages, c.Deleted, c.DeletedBytes, c.Messages-c.Deleted
	kept, err := q.PurgeKeptIn(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	for _, k := range kept {
		p.KeptIn = append(p.KeptIn, PurgeKept{Owner: k.Owner, Account: k.Account, Messages: k.Messages})
	}
	b := &p.Blockers
	b.NotRemoved = r.RemovedAt == nil
	b.Syncing = syncing
	if b.Rules, err = purgeRules(ctx, q, r.ID); err != nil {
		return nil, err
	}
	if b.Holds, err = purgeHolds(ctx, q, r.ID); err != nil {
		return nil, err
	}
	switch id, err := q.OpenAccountPurgeID(ctx); {
	case isNoRows(err):
	case err != nil:
		return nil, err
	default:
		open, err := q.GetAccountPurge(ctx, id)
		if err != nil {
			return nil, err
		}
		if open.AccountID == r.ID {
			p.Open = purgeFromRow(open)
		} else {
			b.OpenPurge = purgeFromRow(open)
		}
	}
	return p, nil
}

func purgeRules(ctx context.Context, q *db.Queries, accountID int64) ([]PurgeRule, error) {
	rows, err := q.PurgeRules(ctx, accountID)
	if err != nil {
		return nil, err
	}
	var out []PurgeRule
	for _, r := range rows {
		out = append(out, PurgeRule{ID: r.ID, Folder: deref(r.Folder), Years: int(r.Years)})
	}
	return out, nil
}

func purgeHolds(ctx context.Context, q *db.Queries, accountID int64) ([]PurgeHold, error) {
	rows, err := q.PurgeHolds(ctx, accountID)
	if err != nil {
		return nil, err
	}
	var out []PurgeHold
	for _, h := range rows {
		out = append(out, PurgeHold{ID: h.ID, Name: h.Name, Released: h.Released, NamesAccount: h.NamesAccount})
	}
	return out, nil
}

// ConfirmPurge matches the filter holds, then, checked against ref like
// UpdateAccount but for a removed account, records a confirmed purge of
// it, if nothing blocks it (PurgeBlockedError) and it deletes exactly
// expected messages (PurgeChangedError otherwise). It deletes nothing
// itself; archive.ExecutePurge does. An audit entry from WithAudit in ctx
// is written with it.
func (s *Store) ConfirmPurge(ctx context.Context, ref AccountRef, expected int64, by *int64) (*AccountPurge, error) {
	if _, err := s.MatchLegalHolds(ctx); err != nil {
		return nil, fmt.Errorf("match legal holds: %w", err)
	}
	syncing, err := s.SyncingAccounts(ctx)
	if err != nil {
		return nil, err
	}
	var id int64
	err = s.inTx(ctx, func(q *db.Queries) error {
		if err := q.LockRetentionChanges(ctx, retentionLockClass); err != nil {
			return err
		}
		r, err := q.LockAccount(ctx, ref.ID)
		if isNoRows(err) || err == nil && !sameOwner(r.OwnerID, ref.OwnerID) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if r.Version != ref.Version {
			return ErrStale
		}
		p, err := purgePreview(ctx, q, r, syncing[ref.ID])
		if err != nil {
			return err
		}
		if p.Open != nil {
			return fmt.Errorf("purge %d of account %q is open already: %w", p.Open.ID, r.Name, ErrPurgeOpen)
		}
		if p.Blockers.Any() {
			return &PurgeBlockedError{Account: r.Name, Blockers: p.Blockers}
		}
		if p.Deleted != expected {
			return &PurgeChangedError{Expected: expected, Now: p.Deleted}
		}
		if expected > 1<<31-1 {
			return fmt.Errorf("%d messages are too many for one purge", expected)
		}
		var owner *string
		if r.OwnerID != nil {
			owner = &p.Owner
		}
		id, err = q.InsertAccountPurge(ctx, db.InsertAccountPurgeParams{
			AccountID: r.ID, AccountName: r.Name, AccountKind: r.Kind, AccountVersion: r.Version, OwnerID: r.OwnerID,
			OwnerName: owner, Expected: int32(expected), CreatedBy: by, //nolint:gosec // checked above
		})
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			// Another process confirmed one meanwhile.
			return ErrPurgeOpen
		}
		if err != nil {
			return err
		}
		if e := auditFrom(ctx); e != nil {
			setPurgeTarget(e, r.ID, r.Name)
			e.Detail("purge_id", id)
			e.Detail("owner", p.Owner)
			e.Detail("kind", r.Kind)
			e.Detail("locations", p.Locations)
			e.Detail("messages", p.Messages)
			e.Detail("deleted", p.Deleted)
			e.Detail("kept", p.Kept)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.AccountPurge(ctx, id)
}

func setPurgeTarget(e *AuditEntry, accountID int64, name string) {
	if e.TargetType == "" {
		e.SetTarget(TargetAccount, strconv.FormatInt(accountID, 10), name)
	}
}

// AccountPurge returns a purge; unknown IDs are ErrNotFound.
func (s *Store) AccountPurge(ctx context.Context, id int64) (*AccountPurge, error) {
	r, err := s.q.GetAccountPurge(ctx, id)
	if isNoRows(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return purgeFromRow(r), nil
}

// OpenAccountPurge returns the confirmed purge, nil if there is none.
func (s *Store) OpenAccountPurge(ctx context.Context) (*AccountPurge, error) {
	id, err := s.q.OpenAccountPurgeID(ctx)
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.AccountPurge(ctx, id)
}

// TryLockPurge takes the lock that lets one process execute a purge. It
// returns false if another one does.
func (s *Store) TryLockPurge(ctx context.Context) (unlock func(), ok bool, err error) {
	return s.tryLockClass(ctx, purgeLockClass)
}

// connKey marks a context whose store transactions run on a dedicated
// connection instead of the pool.
type connKey struct{}

func withConn(ctx context.Context, c *pgxpool.Conn) context.Context {
	return context.WithValue(ctx, connKey{}, c)
}

func connFrom(ctx context.Context) *pgxpool.Conn {
	c, _ := ctx.Value(connKey{}).(*pgxpool.Conn)
	return c
}

// PurgeSession holds the locks of a purge's execution on one dedicated
// connection, and runs its batches on that connection too: the purge lock,
// the account's sync lock and, per batch, the blob lock. While it holds
// the blob lock exclusively, writers wait for it with a connection of the
// pool each; a batch that needed another connection could wait for them
// for ever. So the execution uses this connection and at most one more,
// for reads without the blob lock.
type PurgeSession struct {
	s    *Store
	conn *pgxpool.Conn
}

// StartPurgeSession takes the purge lock on a dedicated connection. It
// returns false if another session holds it.
func (s *Store) StartPurgeSession(ctx context.Context) (*PurgeSession, bool, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	var ok bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1, 0)", purgeLockClass).Scan(&ok); err != nil {
		_ = conn.Conn().Close(context.WithoutCancel(ctx))
		conn.Release()
		return nil, false, err
	}
	if !ok {
		conn.Release()
		return nil, false, nil
	}
	return &PurgeSession{s: s, conn: conn}, true, nil
}

// TryLockSync takes the account's sync lock (see Store.TryLockSync) on the
// session's connection.
func (p *PurgeSession) TryLockSync(ctx context.Context, accountID int64) (bool, error) {
	if accountID < 1 || accountID > math.MaxInt32 {
		return false, fmt.Errorf("account id %d out of range for the sync lock", accountID)
	}
	var ok bool
	err := p.conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1, $2)", syncLockClass, int32(accountID)).Scan(&ok) //nolint:gosec // range checked above
	return ok, err
}

// TryLockBlobs takes the blob lock exclusively on the session's
// connection, without waiting. It returns false while a writer holds it.
func (p *PurgeSession) TryLockBlobs(ctx context.Context) (bool, error) {
	var ok bool
	err := p.conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1, 0)", blobLockClass).Scan(&ok)
	return ok, err
}

// UnlockBlobs releases the blob lock taken by TryLockBlobs.
func (p *PurgeSession) UnlockBlobs(ctx context.Context) error {
	_, err := p.conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1, 0)", blobLockClass)
	return err
}

// Batch is Store.PurgeBatch on the session's connection.
func (p *PurgeSession) Batch(ctx context.Context, purgeID int64, limit int, grace time.Duration) (PurgeBatchResult, error) {
	return p.s.PurgeBatch(withConn(ctx, p.conn), purgeID, limit, grace)
}

// Finish is Store.FinishPurge on the session's connection.
func (p *PurgeSession) Finish(ctx context.Context, purgeID int64) (*AccountPurge, error) {
	return p.s.FinishPurge(withConn(ctx, p.conn), purgeID)
}

// Close releases every lock of the session and its connection. If the
// locks cannot be released, the connection is closed, which releases
// them.
func (p *PurgeSession) Close(ctx context.Context) {
	bg := context.WithoutCancel(ctx)
	if _, err := p.conn.Exec(bg, "SELECT pg_advisory_unlock_all()"); err != nil {
		_ = p.conn.Conn().Close(bg)
	}
	p.conn.Release()
}

// PurgeStaleError is returned when the account of a confirmed purge was
// moved to another user or changed after the confirmation, or is gone.
// The purge stays confirmed until it is cancelled (CancelPurge).
type PurgeStaleError struct {
	PurgeID int64
	Account string
	Reason  string
}

func (e *PurgeStaleError) Error() string {
	return fmt.Sprintf("account %q %s after purge %d was confirmed; cancel the purge and review again", e.Account, e.Reason, e.PurgeID)
}

// Is makes errors.Is(err, ErrStale) true.
func (e *PurgeStaleError) Is(target error) bool { return target == ErrStale }

// lockPurge locks a confirmed purge and its account, and checks that the
// account is still the removed one that was confirmed.
func lockPurge(ctx context.Context, q *db.Queries, purgeID int64) (db.LockAccountPurgeRow, db.Account, error) {
	p, err := q.LockAccountPurge(ctx, purgeID)
	if isNoRows(err) {
		return p, db.Account{}, ErrNotFound
	}
	if err != nil {
		return p, db.Account{}, err
	}
	if p.State != PurgeConfirmed {
		return p, db.Account{}, fmt.Errorf("purge %d is %s: %w", purgeID, p.State, ErrPurgeState)
	}
	a, err := q.LockAccount(ctx, p.AccountID)
	stale := func(reason string) error {
		name := a.Name
		if name == "" {
			if r, err := q.GetAccountPurge(ctx, purgeID); err == nil {
				name = r.AccountName
			}
		}
		return &PurgeStaleError{PurgeID: purgeID, Account: name, Reason: reason}
	}
	switch {
	case isNoRows(err):
		return p, a, stale("is gone")
	case err != nil:
		return p, a, err
	case !sameOwner(a.OwnerID, p.OwnerID):
		return p, a, stale("was moved to another user")
	case a.Version != p.AccountVersion:
		return p, a, stale("was changed")
	case a.RemovedAt == nil:
		// Cannot happen: nothing restores a removed account.
		return p, a, stale("is no longer removed")
	}
	return p, a, nil
}

// purgeNamingBlockers re-checks, under the retention lock, the rules of
// the account and the holds naming it, which a batch or the end of a
// purge must not drop or break.
func purgeNamingBlockers(ctx context.Context, q *db.Queries, a db.Account) error {
	n, err := q.PurgeNamingBlockers(ctx, a.ID)
	if err != nil {
		return err
	}
	if n.Rules == 0 && n.Holds == 0 {
		return nil
	}
	return purgeBlocked(ctx, q, a)
}

// purgeBlocked returns the PurgeBlockedError with the account's rules and
// holds.
func purgeBlocked(ctx context.Context, q *db.Queries, a db.Account) error {
	var b PurgeBlockers
	var err error
	if b.Rules, err = purgeRules(ctx, q, a.ID); err != nil {
		return err
	}
	if b.Holds, err = purgeHolds(ctx, q, a.ID); err != nil {
		return err
	}
	if !b.Any() {
		return fmt.Errorf("a legal hold covers mail of account %q: %w", a.Name, ErrOnHold)
	}
	return &PurgeBlockedError{Account: a.Name, Blockers: b}
}

// PurgeBatchResult is what one batch of a purge did.
type PurgeBatchResult struct {
	Locations int64 // removed
	Deleted   int   // messages deleted: their last location was in the account
	Kept      int   // messages that stay in other accounts
	// Done: the account has no location left after this batch.
	Done bool
}

// PurgeBatch purges up to limit messages of a confirmed purge's account,
// in one transaction: it re-checks the account (PurgeStaleError), its
// rules and the holds naming or covering it (PurgeBlockedError; the caller
// then cancels the purge with CancelPurge), and that it deletes no more
// than confirmed (PurgeChangedError). For the messages without a location
// elsewhere it writes the tombstones and the deletion records, deletes
// the account's locations of all of them, deletes those messages and
// schedules their files for removal after grace. The caller holds the
// blob lock exclusively, so no writer adds a location meanwhile. An audit
// entry from WithAudit in ctx is written with it.
func (s *Store) PurgeBatch(ctx context.Context, purgeID int64, limit int, grace time.Duration) (PurgeBatchResult, error) {
	var res PurgeBatchResult
	err := s.inWriteTx(ctx, false, func(q *db.Queries) error {
		res = PurgeBatchResult{}
		// Rule and hold changes, deletion batches and other purges wait
		// for the batch, and the batch for them.
		if err := q.LockRetentionChanges(ctx, retentionLockClass); err != nil {
			return err
		}
		p, a, err := lockPurge(ctx, q, purgeID)
		if err != nil {
			return err
		}
		if err := purgeNamingBlockers(ctx, q, a); err != nil {
			return err
		}
		shas, err := q.PurgeBatchShas(ctx, db.PurgeBatchShasParams{AccountID: a.ID, RowLimit: clampInt32(limit)})
		if err != nil {
			return err
		}
		if len(shas) == 0 {
			// Nothing left (a resume after the last batch): no entry.
			skipAudit(ctx)
			res.Done = true
			return nil
		}
		held, err := q.PurgeHeldOf(ctx, db.PurgeHeldOfParams{AccountID: a.ID, Shas: shas})
		if err != nil {
			return err
		}
		if held {
			return purgeBlocked(ctx, q, a)
		}
		last, err := q.PurgeLastOf(ctx, db.PurgeLastOfParams{AccountID: a.ID, Shas: shas})
		if err != nil {
			return err
		}
		if int64(p.Deleted)+int64(len(last)) > int64(p.Expected) {
			return &PurgeChangedError{Expected: int64(p.Expected), Now: int64(p.Deleted) + int64(len(last))}
		}
		if len(last) > 0 {
			if _, err := q.InsertPurgeTombstones(ctx, db.InsertPurgeTombstonesParams{PurgeID: purgeID, Shas: last}); err != nil {
				return fmt.Errorf("tombstones: %w", err)
			}
			if _, err := q.InsertPurgeDeletions(ctx, last); err != nil {
				return fmt.Errorf("deletion records: %w", err)
			}
		}
		if err := q.SetPurgeAccount(ctx, strconv.FormatInt(a.ID, 10)); err != nil {
			return err
		}
		n, err := q.DeleteAccountLocationsOf(ctx, db.DeleteAccountLocationsOfParams{AccountID: a.ID, Shas: shas})
		if err != nil {
			return fmt.Errorf("delete locations: %w", err)
		}
		if len(last) > 0 {
			m, err := q.DeleteMessagesOf(ctx, last)
			if err != nil {
				return fmt.Errorf("delete messages: %w", err)
			}
			if int(m) != len(last) {
				return fmt.Errorf("deleted %d of %d messages", m, len(last))
			}
			if err := q.ScheduleBlobRemovals(ctx, db.ScheduleBlobRemovalsParams{Shas: last, DueAt: time.Now().Add(grace)}); err != nil {
				return err
			}
		}
		res.Locations, res.Deleted, res.Kept = n, len(last), len(shas)-len(last)
		left, err := q.CountAccountLocations(ctx, a.ID)
		if err != nil {
			return err
		}
		res.Done = left == 0
		if err := q.AddAccountPurgeCounts(ctx, db.AddAccountPurgeCountsParams{
			ID: purgeID, Locations: n, Deleted: int32(res.Deleted), Kept: int32(res.Kept), //nolint:gosec // batch sizes
		}); err != nil {
			return err
		}
		if e := auditFrom(ctx); e != nil {
			setPurgeTarget(e, a.ID, a.Name)
			e.Detail("purge_id", purgeID)
			e.Detail("locations", n)
			e.Detail("deleted", res.Deleted)
			e.Detail("kept", res.Kept)
		}
		return nil
	})
	return res, err
}

// CheckPurgeCount checks, before a confirmed purge is executed or
// resumed, that the messages deleted so far and those still to delete are
// no more than confirmed (PurgeChangedError otherwise). Their number can
// only shrink: a message gets a location elsewhere.
func (s *Store) CheckPurgeCount(ctx context.Context, purgeID int64) error {
	p, err := s.AccountPurge(ctx, purgeID)
	if err != nil {
		return err
	}
	c, err := s.q.PurgeCounts(ctx, p.AccountID)
	if err != nil {
		return err
	}
	if now := int64(p.Deleted) + c.Deleted; now > int64(p.Expected) {
		return &PurgeChangedError{Expected: int64(p.Expected), Now: now}
	}
	return nil
}

// FinishPurge ends a purge whose account has no location left: in one
// transaction it re-checks the account, its rules and the holds naming it
// (as PurgeBatch does; deleting the account would drop its rules), deletes
// its folders and the account row with everything that belongs to it,
// and marks the purge done. The account's name is free again. An audit
// entry from WithAudit in ctx is written with it.
func (s *Store) FinishPurge(ctx context.Context, purgeID int64) (*AccountPurge, error) {
	err := s.inTx(ctx, func(q *db.Queries) error {
		if err := q.LockRetentionChanges(ctx, retentionLockClass); err != nil {
			return err
		}
		_, a, err := lockPurge(ctx, q, purgeID)
		if err != nil {
			return err
		}
		if err := purgeNamingBlockers(ctx, q, a); err != nil {
			return err
		}
		left, err := q.CountAccountLocations(ctx, a.ID)
		if err != nil {
			return err
		}
		if left > 0 {
			return fmt.Errorf("account %q still has %d location(s); purge %d is not finished", a.Name, left, purgeID)
		}
		if err := q.DeleteAccountFolders(ctx, a.ID); err != nil {
			return err
		}
		if err := one(q.DeleteAccount(ctx, a.ID)); err != nil {
			return err
		}
		if err := one(q.FinishAccountPurge(ctx, purgeID)); err != nil {
			return err
		}
		if e := auditFrom(ctx); e != nil {
			r, err := q.GetAccountPurge(ctx, purgeID)
			if err != nil {
				return err
			}
			setPurgeTarget(e, a.ID, a.Name)
			e.Detail("purge_id", purgeID)
			e.Detail("owner", deref(r.OwnerName))
			e.Detail("kind", r.AccountKind)
			e.Detail("locations", r.Locations)
			e.Detail("deleted", r.Deleted)
			e.Detail("kept", r.Kept)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.AccountPurge(ctx, purgeID)
}

// CancelPurge cancels a confirmed purge, with a note on why. What its
// batches deleted stays deleted; the account stays removed, with the mail
// that is left. Unknown IDs are ErrNotFound, a purge that is not confirmed
// is ErrPurgeState. An audit entry from WithAudit in ctx is written with
// it.
func (s *Store) CancelPurge(ctx context.Context, purgeID int64, note string) error {
	note = strings.TrimSpace(note)
	if note == "" {
		note = "cancelled"
	}
	return s.inTx(ctx, func(q *db.Queries) error {
		n, err := q.CancelAccountPurge(ctx, db.CancelAccountPurgeParams{ID: purgeID, Note: &note})
		if err != nil {
			return err
		}
		r, err := q.GetAccountPurge(ctx, purgeID)
		if isNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("purge %d is %s and cannot be cancelled: %w", purgeID, r.State, ErrPurgeState)
		}
		if e := auditFrom(ctx); e != nil {
			setPurgeTarget(e, r.AccountID, r.AccountName)
			e.Detail("purge_id", purgeID)
			e.Detail("note", note)
			e.Detail("locations", r.Locations)
			e.Detail("deleted", r.Deleted)
			e.Detail("kept", r.Kept)
		}
		return nil
	})
}
