package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store/db"
)

// Storage usage (#397, part 1). What counts is message bytes: the
// plaintext size of each unique message (messages.size).
//
//   - Instance: exact at all times. Statement triggers on messages append
//     a delta row per statement (migration 00041); storage_fold adds them
//     to a base row. StorageTotals reads base plus deltas in one statement.
//   - Customer groups: exact after each snapshot (usage_refresh),
//     deduplicated within the group; between snapshots every save adds a
//     group delta for the messages new to the group (recordGroupUsage). In
//     between, a group's figure is an upper bound, with one exception: a
//     message whose only other copy in the group is deleted while it is
//     being added is missing until the next snapshot.
//   - Accounts: messages, bytes and exclusive bytes per snapshot.
//
// Every save locks its account and the account's owner FOR KEY SHARE first
// (lockAccountGroup), so that a move of the owner to another group, an
// owner change and the adoption by the first user (all FOR UPDATE) wait
// until the owner's or account's open saves end. Moves request a snapshot
// (requestUsageRefresh), which then sees every save that committed before.
// See docs/development/architecture.md, "Storage usage".

// storageLockClass is the first key of the advisory lock that storage_fold
// (for its transaction) and usage_refresh (for its session, around its
// snapshot) share ("mi" in ASCII); the second key is always 0.
const storageLockClass = 0x6d69

// Job kinds of storage usage (registered in internal/jobs).
const (
	// JobUsageRefresh takes the snapshot of the figures per account and
	// group: hourly, and on request (RequestUsageRefresh).
	JobUsageRefresh = "usage_refresh"
	// JobStorageFold adds the counter deltas to the base and the group
	// deltas to the group rows, every 5 minutes, and queues a snapshot that
	// a request is still waiting for.
	JobStorageFold = "storage_fold"
	// StorageJobKey is the key of both: each is a singleton.
	StorageJobKey = "singleton"
	// StorageJobMaxAttempts is the attempts of both kinds.
	StorageJobMaxAttempts = 3
	// PriorityStorage runs the storage jobs after the work of new mail and
	// before backfills, so that a long attachment backfill does not delay
	// a requested snapshot.
	PriorityStorage = 50
)

// UsageRefreshJob is the snapshot job to enqueue.
func UsageRefreshJob() NewJob {
	return NewJob{Kind: JobUsageRefresh, Key: StorageJobKey, Priority: PriorityStorage, MaxAttempts: StorageJobMaxAttempts}
}

// StorageFoldJob is the fold job to enqueue.
func StorageFoldJob() NewJob {
	return NewJob{Kind: JobStorageFold, Key: StorageJobKey, Priority: PriorityStorage, MaxAttempts: StorageJobMaxAttempts}
}

// accountGroup is the account a save writes into and the customer group
// of its owner, locked by lockAccountGroup until the save commits. Group
// is nil for an account without an owner ("Without group").
type accountGroup struct {
	Account int64
	Group   *int64
}

// lockTarget names the account of a save: by one of its folders or
// directly.
type lockTarget struct {
	folder, account int64
}

func byFolder(id int64) lockTarget  { return lockTarget{folder: id} }
func byAccount(id int64) lockTarget { return lockTarget{account: id} }

// lockAccountGroup is the first step of every save that adds locations,
// before the folder's lock: it locks the account and then its owner FOR
// KEY SHARE and returns the owner's group. KEY SHARE conflicts only with
// FOR UPDATE, DELETE and key updates, which is what the moves take
// (SetUserGroup: LockUser; SetAccountOwner: LockAccount; the adoption:
// LockUnownedAccounts), not with ordinary updates of either row. So a move
// waits until the open saves of the owner or account end, and a save that
// starts during a move waits for it and reads the new group (READ
// COMMITTED reads the new row version after the wait).
//
// The owner is locked in a second statement: PostgreSQL refuses a row lock
// on the nullable side of an outer join.
func lockAccountGroup(ctx context.Context, q *db.Queries, t lockTarget) (accountGroup, error) {
	var (
		g     accountGroup
		owner *int64
	)
	if t.folder != 0 {
		r, err := q.LockFolderAccountGroup(ctx, t.folder)
		if isNoRows(err) {
			return g, fmt.Errorf("folder %d: %w", t.folder, ErrNotFound)
		}
		if err != nil {
			return g, fmt.Errorf("lock account: %w", err)
		}
		g.Account, owner = r.ID, r.OwnerID
	} else {
		r, err := q.LockAccountGroup(ctx, t.account)
		if isNoRows(err) {
			return g, fmt.Errorf("account %d: %w", t.account, ErrNotFound)
		}
		if err != nil {
			return g, fmt.Errorf("lock account: %w", err)
		}
		g.Account, owner = r.ID, r.OwnerID
	}
	if owner == nil {
		return g, nil
	}
	group, err := q.LockOwnerGroup(ctx, *owner)
	if err != nil {
		return g, fmt.Errorf("lock owner: %w", err)
	}
	g.Group = &group
	return g, nil
}

// recordGroupUsage runs after a save's location writes, in its
// transaction: added are the messages the save made new to the account
// (counted in SaveResult.Added). One probe (GroupHeldMessages) finds those
// another account of the group holds; the rest are new to the group and
// go into one delta row. The probe runs for every hash new to the account,
// whether or not it is new to the archive, and looks only into the group's
// own folders (message_locations_folder_sha_idx), so its timing depends on
// the group's folders and the batch, never on other groups (#539). Without
// a group (an account without an owner), nothing is probed or recorded.
//
// Two saves that add the same message to two accounts of one group at the
// same time may both count it: the figure stays an upper bound, and the
// next snapshot is exact.
func recordGroupUsage(ctx context.Context, q *db.Queries, g accountGroup, added []*MessageMeta) error {
	if g.Group == nil || len(added) == 0 {
		return nil
	}
	shas := make([]string, len(added))
	for i, m := range added {
		shas[i] = m.SHA256
	}
	held, err := q.GroupHeldMessages(ctx, db.GroupHeldMessagesParams{Shas: shas, AccountID: g.Account, GroupID: *g.Group})
	if err != nil {
		return fmt.Errorf("group held messages: %w", err)
	}
	elsewhere := make(map[string]bool, len(held))
	for _, sha := range held {
		elsewhere[sha] = true
	}
	var n, bytes int64
	for _, m := range added {
		if !elsewhere[m.SHA256] {
			elsewhere[m.SHA256] = true // a hash twice in added counts once
			n++
			bytes += m.Size
		}
	}
	if n == 0 {
		return nil
	}
	return q.InsertGroupDelta(ctx, db.InsertGroupDeltaParams{GroupID: *g.Group, Messages: n, Bytes: bytes})
}

// requestUsageRefresh requests a snapshot in the caller's transaction:
// it bumps storage_refresh_requests.seq and enqueues usage_refresh (a
// failed one too). A snapshot that started before the request commits saw
// an older seq; it then runs again (RefreshUsage, Again), and storage_fold
// queues one while the last snapshot covers an older seq. The row and the
// job row are locked last in every transaction that takes them.
func requestUsageRefresh(ctx context.Context, q *db.Queries) error {
	if _, err := q.BumpUsageRefreshRequest(ctx); err != nil {
		return fmt.Errorf("request a usage snapshot: %w", err)
	}
	_, err := enqueueJobs(ctx, q, []NewJob{UsageRefreshJob()}, true)
	return err
}

// RequestUsageRefresh requests a snapshot of the usage figures: at the
// end of an import and a restore, and on an admin's refresh.
func (s *Store) RequestUsageRefresh(ctx context.Context) error {
	return s.inTx(ctx, func(q *db.Queries) error { return requestUsageRefresh(ctx, q) })
}

// UsageSnapshotAt returns when the last usage snapshot ran; nil before
// the first.
func (s *Store) UsageSnapshotAt(ctx context.Context) (*time.Time, error) {
	r, err := s.q.StorageSnapshot(ctx)
	return r.ComputedAt, err
}

// StorageTotals returns the exact instance totals: unique messages and
// their bytes.
func (s *Store) StorageTotals(ctx context.Context) (messages, bytes int64, err error) {
	r, err := s.q.StorageTotals(ctx)
	return r.Messages, r.Bytes, err
}

// FoldResult tells what FoldStorage did.
type FoldResult struct {
	// Deltas counts the counter rows added to the base.
	Deltas int64
	// GroupDeltas counts the group rows changed.
	GroupDeltas int64
	// Requested is set when a requested snapshot was not covered yet and
	// usage_refresh was queued (or left as it was, when it runs or failed
	// in the last hour).
	Requested bool
}

// FoldStorage is the job storage_fold: under the storage lock it adds the
// counter deltas to the base and the group deltas to their group rows,
// each in one statement. At its end it compares the latest refresh request
// with the one the last snapshot covered; if a request is not covered, it
// enqueues usage_refresh, which re-arms a failed one only once it failed
// more than an hour ago. That catches a request that commits after a
// running snapshot read seq for the last time.
func (s *Store) FoldStorage(ctx context.Context) (FoldResult, error) {
	var res FoldResult
	err := s.inTx(ctx, func(q *db.Queries) error {
		res = FoldResult{}
		if err := q.LockStorageXact(ctx, storageLockClass); err != nil {
			return fmt.Errorf("wait for the storage lock: %w", err)
		}
		var err error
		if res.Deltas, res.GroupDeltas, err = foldStorage(ctx, q); err != nil {
			return err
		}
		st, err := q.UsageRefreshState(ctx)
		if err != nil {
			return err
		}
		if st.Seq > st.CoveredSeq {
			res.Requested = true
			_, err := enqueueJobs(ctx, q, []NewJob{UsageRefreshJob()}, !st.FailedRecently)
			return err
		}
		return nil
	})
	return res, err
}

func foldStorage(ctx context.Context, q *db.Queries) (deltas, groups int64, err error) {
	if deltas, err = q.FoldStorageCounters(ctx); err != nil {
		return 0, 0, fmt.Errorf("fold the storage counters: %w", err)
	}
	if groups, err = q.FoldGroupDeltas(ctx); err != nil {
		return 0, 0, fmt.Errorf("fold the group deltas: %w", err)
	}
	return deltas, groups, nil
}

// usageRefreshTimeout bounds each statement of the snapshot.
const usageRefreshTimeout = "5min"

// usageRefreshWorkMem is the memory of each hash of the snapshot before it
// spills to temporary files.
const usageRefreshWorkMem = "32MB"

// RefreshResult tells what RefreshUsage found.
type RefreshResult struct {
	Accounts, Groups int64
	// CoveredSeq is the refresh request the snapshot covered.
	CoveredSeq int64
	// Again is set when a request committed after the snapshot began: the
	// job runs again at once.
	Again bool
	// Counted are the counters, Exact an exact count, both in the
	// snapshot; they differ only after a bug or a manual change.
	CountedMessages, CountedBytes int64
	ExactMessages, ExactBytes     int64
}

// Drift is the difference in bytes between the counters and the exact
// count.
func (r RefreshResult) Drift() int64 { return r.CountedBytes - r.ExactBytes }

// RefreshUsage is the job usage_refresh. On a pool connection of its own
// it takes the storage lock for its session, before the snapshot, so that
// it never starts its snapshot while a fold runs; folds the deltas; and
// then, in one REPEATABLE READ transaction:
//
//  1. reads storage_refresh_requests.seq (a plain read, the first
//     statement, which takes the snapshot);
//  2. writes the figures of every account and group and of the accounts
//     without a group (RefreshUsage, one statement);
//  3. compares the counters with an exact count;
//  4. deletes the group deltas the snapshot saw (later ones stay);
//  5. writes storage_snapshot with the seq of step 1 as covered_seq, last.
//
// After the commit it releases the lock and reads seq again: if it grew,
// a request committed after step 1, and Again is set. A request that
// commits even later is queued by the next fold. On any error the
// connection is closed, which releases the lock and ends the transaction.
//
// A serialization failure (40001) means that an account or group the
// snapshot wrote a row for was changed or deleted after it began (an
// owner change, a rename, a deletion): the caller runs it again.
func (s *Store) RefreshUsage(ctx context.Context) (RefreshResult, error) {
	var res RefreshResult
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return res, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = conn.Conn().Close(context.WithoutCancel(ctx))
		}
		conn.Release()
	}()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1, 0)", storageLockClass); err != nil {
		return res, fmt.Errorf("wait for the storage lock: %w", err)
	}
	if err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		_, _, err := foldStorage(ctx, s.q.WithTx(tx))
		return err
	}); err != nil {
		return res, err
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return res, err
	}
	for _, set := range []string{
		"SET LOCAL work_mem = '" + usageRefreshWorkMem + "'",
		"SET LOCAL statement_timeout = '" + usageRefreshTimeout + "'",
	} {
		if _, err := tx.Exec(ctx, set); err != nil {
			return res, err
		}
	}
	q := s.q.WithTx(tx)
	if res.CoveredSeq, err = q.UsageRefreshRequestSeq(ctx); err != nil {
		return res, err
	}
	r, err := q.RefreshUsage(ctx)
	if err != nil {
		return res, fmt.Errorf("usage snapshot: %w", err)
	}
	res.Accounts, res.Groups = r.Accounts, r.Groups
	c, err := q.StorageCounterCheck(ctx)
	if err != nil {
		return res, fmt.Errorf("counter check: %w", err)
	}
	res.CountedMessages, res.CountedBytes, res.ExactMessages, res.ExactBytes = c.CountedMessages, c.CountedBytes, c.ExactMessages, c.ExactBytes
	if _, err := q.DeleteSeenGroupDeltas(ctx); err != nil {
		return res, err
	}
	if err := q.WriteStorageSnapshot(ctx, db.WriteStorageSnapshotParams{
		UngroupedMessages: &r.UngroupedMessages, UngroupedBytes: &r.UngroupedBytes,
		CounterDrift: ptrTo(res.Drift()), CoveredSeq: res.CoveredSeq,
	}); err != nil {
		return res, err
	}
	if err := tx.Commit(ctx); err != nil {
		return res, err
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_unlock($1, 0)", storageLockClass); err != nil {
		return res, err
	}
	ok = true
	seq, err := s.q.UsageRefreshRequestSeq(ctx)
	if err != nil {
		return res, err
	}
	res.Again = seq > res.CoveredSeq
	return res, nil
}

func ptrTo[T any](v T) *T { return &v }

// IsSerializationFailure reports whether err is PostgreSQL's 40001.
func IsSerializationFailure(err error) bool {
	state, ok := SQLState(err)
	return ok && state == "40001"
}

// StorageUsage is what `usage` shows: the instance, each customer group,
// the accounts without a group, and each account, with names.
type StorageUsage struct {
	// Messages and Bytes are exact (the counters).
	Messages, Bytes int64
	// State is the instance's quota state (part 2; "ok" until then).
	State string
	// SnapshotAt is when the last snapshot ran; nil before the first.
	SnapshotAt *time.Time
	// CounterDrift is the last snapshot's difference in bytes between the
	// counters and an exact count; 0 is right.
	CounterDrift int64
	// DatabaseBytes and RelationBytes are the database's size and that of
	// its largest relations, at the last snapshot.
	DatabaseBytes int64
	RelationBytes map[string]int64
	Groups        []GroupUsage
	// Ungrouped are the accounts without an owner (only before the first
	// user), at the last snapshot.
	Ungrouped Usage
	Accounts  []AccountUsage
}

// Usage is a count of unique messages and their bytes.
type Usage struct {
	Messages, Bytes int64
}

// GroupUsage is one customer group's usage: the last snapshot plus what
// was added since (an upper bound until the next snapshot).
type GroupUsage struct {
	ID         int64
	Name       string
	QuotaBytes *int64
	Usage
	State string
	// SnapshotAt is nil before the first snapshot that saw the group.
	SnapshotAt *time.Time
}

// AccountUsage is one account's usage at the last snapshot.
type AccountUsage struct {
	ID      int64
	Name    string
	Kind    AccountKind
	Removed bool
	OwnerID *int64
	Owner   string
	GroupID *int64
	Usage
	// ExclusiveBytes are the bytes no other account holds.
	ExclusiveBytes int64
	// SnapshotAt is nil before the first snapshot that saw the account.
	SnapshotAt *time.Time
}

// StorageUsage returns the usage of the instance, its groups and accounts.
// The reads are separate statements; the instance totals are exact, the
// rest is as of the last snapshot plus the group deltas.
func (s *Store) StorageUsage(ctx context.Context) (*StorageUsage, error) {
	var u StorageUsage
	var err error
	if u.Messages, u.Bytes, err = s.StorageTotals(ctx); err != nil {
		return nil, err
	}
	st, err := s.q.InstanceStorageState(ctx)
	if err != nil {
		return nil, err
	}
	u.State = st.State
	snap, err := s.q.StorageSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	u.SnapshotAt = snap.ComputedAt
	u.CounterDrift = deref64(snap.CounterDrift)
	u.DatabaseBytes = deref64(snap.DatabaseBytes)
	u.Ungrouped = Usage{Messages: deref64(snap.UngroupedMessages), Bytes: deref64(snap.UngroupedBytes)}
	if len(snap.RelationBytes) > 0 {
		if err := json.Unmarshal(snap.RelationBytes, &u.RelationBytes); err != nil {
			return nil, fmt.Errorf("storage snapshot relation sizes: %w", err)
		}
	}
	groups, err := s.q.GroupStorageUsage(ctx)
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		u.Groups = append(u.Groups, GroupUsage{
			ID: g.ID, Name: g.Name, QuotaBytes: g.QuotaBytes, Usage: Usage{Messages: g.Messages, Bytes: g.Bytes},
			State: g.State, SnapshotAt: g.ComputedAt,
		})
	}
	accounts, err := s.q.AccountStorageUsage(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range accounts {
		u.Accounts = append(u.Accounts, AccountUsage{
			ID: a.ID, Name: a.Name, Kind: AccountKind(a.Kind), Removed: a.Removed, OwnerID: a.OwnerID,
			Owner: deref(a.OwnerName), GroupID: a.GroupID,
			Usage:          Usage{Messages: deref64(a.Messages), Bytes: deref64(a.Bytes)},
			ExclusiveBytes: deref64(a.ExclusiveBytes), SnapshotAt: a.ComputedAt,
		})
	}
	return &u, nil
}

func deref64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}
