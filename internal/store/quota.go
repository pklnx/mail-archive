package store

import (
	"context"
	"errors"
	"strconv"

	"github.com/pklnx/mail-archive/internal/store/db"
)

// Storage quotas (#397, part 2). The gate of every process (internal/quota)
// reads the figures of every scope, the instance and each customer group,
// with their persisted states in one statement (StorageGateFigures), and
// persists a scope's transition under the row lock of its state, with the
// scope's figures read again under it (TransitionStorageState). The states
// live in storage_states (migration 00041); a group gets its row with its
// first transition. Each transition counts up event_seq, which names the
// event (StorageEventID).

// WriteObserver is told, after a save committed, the bytes it added: to
// the group (nil: an account without an owner) and at most that much to
// the instance. The storage gate adds them to its figures until its next
// read (#397).
type WriteObserver func(group *int64, bytes int64)

// SetWriteObserver sets the observer of committed saves; nil removes it.
// Every process that enforces a quota sets its gate's.
func (s *Store) SetWriteObserver(f WriteObserver) {
	if f == nil {
		s.writes.Store(nil)
		return
	}
	s.writes.Store(&f)
}

// storageWrite is what a save added, for the observer.
type storageWrite struct {
	group *int64
	bytes int64
}

// noteWrite tells the observer what a committed save added.
func (s *Store) noteWrite(w storageWrite) {
	if w.bytes <= 0 {
		return
	}
	if f := s.writes.Load(); f != nil {
		(*f)(w.group, w.bytes)
	}
}

// GateScope is one scope's figures as the gate reads them.
type GateScope struct {
	// Group is nil for the instance.
	Group *int64
	// Quota is a group's quota, nil without one (the instance's comes
	// from the environment).
	Quota *int64
	// Bytes is the instance's exact total, or the group's figure: its last
	// snapshot plus what was added since, an upper bound between
	// snapshots except in the windows named in storage.go.
	Bytes int64
	// State is the persisted state ("ok" for a group without a row yet),
	// EventSeq its transition count.
	State    string
	EventSeq int64
}

// GateFigures are the figures of every scope, read in one statement.
type GateFigures struct {
	Instance GateScope
	Groups   []GateScope
	// Counted is false before the first snapshot: group figures do not
	// count yet.
	Counted bool
}

// StorageGateFigures reads the figures and persisted states of the
// instance and every group, in one statement.
func (s *Store) StorageGateFigures(ctx context.Context) (GateFigures, error) {
	rows, err := s.q.StorageGateFigures(ctx)
	if err != nil {
		return GateFigures{}, err
	}
	var f GateFigures
	found := false
	for _, r := range rows {
		sc := GateScope{Group: r.GroupID, Quota: r.QuotaBytes, Bytes: r.Bytes, State: r.State, EventSeq: r.EventSeq}
		if r.GroupID == nil {
			f.Instance, f.Counted, found = sc, r.Counted, true
			continue
		}
		f.Groups = append(f.Groups, sc)
	}
	if !found {
		return GateFigures{}, errors.New("storage gate: no instance row in storage_states")
	}
	return f, nil
}

// ScopeFigure is a scope's figure, read again under its state's row lock.
type ScopeFigure struct {
	Bytes int64
	// Quota is a group's quota, nil without one; always nil for the
	// instance.
	Quota *int64
	// Counted is false before the first snapshot (groups only).
	Counted bool
}

// StorageTransition is the persisted state of a scope after
// TransitionStorageState.
type StorageTransition struct {
	State    string
	EventSeq int64
	// Changed is set when this call wrote a new state.
	Changed bool
	// Figure is what the decision was made on.
	Figure ScopeFigure
}

// TransitionStorageState decides a scope's state under the row lock of its
// state (group nil: the instance): it locks the row (a group's is created
// first if needed), reads the scope's figures again (READ COMMITTED: the
// statement starts after the lock is granted, so its figures are at least
// as new as those of any transition committed before), and calls decide
// with the locked state and those figures. When decide returns another
// state, it is written with state_since and event_seq + 1. Two processes
// that see the same crossing at once therefore make one transition, and a
// process with older figures never flips the state back. A group removed
// meanwhile is ErrUnknownGroup.
func (s *Store) TransitionStorageState(ctx context.Context, group *int64,
	decide func(current string, f ScopeFigure) (string, error)) (StorageTransition, error) {
	var t StorageTransition
	err := s.inTx(ctx, func(q *db.Queries) error {
		t = StorageTransition{}
		if group != nil {
			err := q.EnsureGroupStorageState(ctx, group)
			if isForeignKeyViolation(err) {
				return ErrUnknownGroup
			}
			if err != nil {
				return err
			}
		}
		cur, err := q.LockStorageState(ctx, group)
		if isNoRows(err) {
			return ErrUnknownGroup
		}
		if err != nil {
			return err
		}
		t.State, t.EventSeq = cur.State, cur.EventSeq
		if group == nil {
			tot, err := q.StorageTotals(ctx)
			if err != nil {
				return err
			}
			t.Figure = ScopeFigure{Bytes: tot.Bytes, Counted: true}
		} else {
			g, err := q.GroupGateFigure(ctx, *group)
			if isNoRows(err) {
				return ErrUnknownGroup
			}
			if err != nil {
				return err
			}
			t.Figure = ScopeFigure{Bytes: g.Bytes, Quota: g.QuotaBytes, Counted: g.Counted}
		}
		next, err := decide(cur.State, t.Figure)
		if err != nil || next == cur.State {
			return err
		}
		r, err := q.SetStorageState(ctx, db.SetStorageStateParams{State: next, GroupID: group})
		if err != nil {
			return err
		}
		t.State, t.EventSeq, t.Changed = next, r.EventSeq, true
		return nil
	})
	return t, err
}

// StorageEventID names a scope's transition for alerts:
// storage:instance:<seq> or storage:<group id>:<seq>. A scope that goes
// warn, ok, warn gets three IDs.
func StorageEventID(group *int64, seq int64) string {
	scope := "instance"
	if group != nil {
		scope = strconv.FormatInt(*group, 10)
	}
	return "storage:" + scope + ":" + strconv.FormatInt(seq, 10)
}

// ErrQuotaInvalid is returned for a group quota that is not positive.
var ErrQuotaInvalid = errors.New("a quota is a positive number of bytes")

// GroupQuotaChange is what SetGroupQuota changed.
type GroupQuotaChange struct {
	Name     string
	From, To *int64
	Changed  bool
}

// SetGroupQuota sets a group's quota in message bytes; nil removes it. The
// caller checks it against the instance quota. The audit entry (group.quota,
// targeted at the group) gets the old and the new value in bytes, null for
// none. ErrUnknownGroup for an unknown id.
func (s *Store) SetGroupQuota(ctx context.Context, id int64, quota *int64) (GroupQuotaChange, error) {
	if quota != nil && *quota <= 0 {
		return GroupQuotaChange{}, ErrQuotaInvalid
	}
	var c GroupQuotaChange
	err := s.inTx(ctx, func(q *db.Queries) error {
		g, err := q.LockGroupQuota(ctx, id)
		if isNoRows(err) {
			return ErrUnknownGroup
		}
		if err != nil {
			return err
		}
		c = GroupQuotaChange{Name: g.Name, From: g.QuotaBytes, To: quota, Changed: !sameQuota(g.QuotaBytes, quota)}
		if err := q.SetGroupQuota(ctx, db.SetGroupQuotaParams{ID: id, QuotaBytes: quota}); err != nil {
			return err
		}
		auditGroup(ctx, db.CustomerGroup{ID: id, Name: g.Name})
		if e := auditFrom(ctx); e != nil {
			e.Detail("from", g.QuotaBytes)
			e.Detail("to", quota)
		}
		return nil
	})
	return c, err
}

func sameQuota(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// GroupQuota is a group with its quota.
type GroupQuota struct {
	ID    int64
	Name  string
	Quota int64
}

// GroupsAboveQuota returns the groups whose quota is above the instance
// quota (instanceQuota > 0): their own quota never applies before the
// instance's.
func (s *Store) GroupsAboveQuota(ctx context.Context, instanceQuota int64) ([]GroupQuota, error) {
	rows, err := s.q.GroupsAboveQuota(ctx, instanceQuota)
	if err != nil {
		return nil, err
	}
	out := make([]GroupQuota, 0, len(rows))
	for _, r := range rows {
		out = append(out, GroupQuota{ID: r.ID, Name: r.Name, Quota: r.QuotaBytes})
	}
	return out, nil
}
