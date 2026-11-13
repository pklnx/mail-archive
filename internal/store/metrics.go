package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Instance metrics (#397, part 3) are what GET /api/metrics shows: IDs,
// counts, times and fixed enums, never a name, host, address, folder,
// subject or error text. Scrapes often end in third-party monitoring, so
// the types below hold nothing else, and a reflection test in the web
// package checks every field. Exclusive bytes per account are left out:
// they are computed across groups.

// InstanceMetrics is the database part of the metrics.
type InstanceMetrics struct {
	// Messages and Bytes are the exact instance totals.
	Messages, Bytes int64
	// State and StateSince are the instance's quota state.
	State      string
	StateSince time.Time
	// SnapshotAt is when the last usage snapshot ran; nil before the
	// first. CounterDrift is its difference between the counters and an
	// exact count, in bytes.
	SnapshotAt   *time.Time
	CounterDrift int64
	// Ungrouped are the accounts without an owner, at the last snapshot.
	UngroupedMessages, UngroupedBytes int64
	// DatabaseBytes and RelationBytes (by table name) at the last
	// snapshot.
	DatabaseBytes int64
	RelationBytes map[string]int64
	Groups        []GroupMetrics
	Accounts      []AccountMetrics
	Users         []UserCount
	Jobs          []JobStat
}

// GroupMetrics is one customer group, by ID.
type GroupMetrics struct {
	ID int64
	// QuotaBytes is nil without a quota of the group's own.
	QuotaBytes      *int64
	Messages, Bytes int64
	State           string
	// StateSince is nil before the group's first quota state.
	StateSince *time.Time
}

// AccountMetrics is one account that is not removed, by ID.
type AccountMetrics struct {
	ID int64
	// GroupID is the owner's group; nil for an ownerless account.
	GroupID *int64
	Kind    AccountKind
	Enabled bool
	// Messages and Bytes at the last snapshot (0 before the first).
	Messages, Bytes int64
	// LastSuccessAt is the last successful sync of an IMAP account.
	LastSuccessAt *time.Time
}

// UserCount is the number of users with a role and lock state.
type UserCount struct {
	// Role is "admin", "member" or "auditor".
	Role   string
	Locked bool
	N      int64
}

// InstanceMetrics reads the database part of the metrics. The reads are
// separate statements, as in StorageUsage.
func (s *Store) InstanceMetrics(ctx context.Context) (*InstanceMetrics, error) {
	var m InstanceMetrics
	var err error
	if m.Messages, m.Bytes, err = s.StorageTotals(ctx); err != nil {
		return nil, err
	}
	st, err := s.q.InstanceStorageState(ctx)
	if err != nil {
		return nil, err
	}
	m.State, m.StateSince = st.State, st.StateSince
	snap, err := s.q.StorageSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	m.SnapshotAt, m.CounterDrift = snap.ComputedAt, deref64(snap.CounterDrift)
	m.UngroupedMessages, m.UngroupedBytes = deref64(snap.UngroupedMessages), deref64(snap.UngroupedBytes)
	m.DatabaseBytes = deref64(snap.DatabaseBytes)
	if len(snap.RelationBytes) > 0 {
		if err := json.Unmarshal(snap.RelationBytes, &m.RelationBytes); err != nil {
			return nil, fmt.Errorf("storage snapshot relation sizes: %w", err)
		}
	}
	groups, err := s.q.MetricsGroups(ctx)
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		m.Groups = append(m.Groups, GroupMetrics{
			ID: g.ID, QuotaBytes: g.QuotaBytes, Messages: g.Messages, Bytes: g.Bytes, State: g.State, StateSince: g.StateSince,
		})
	}
	accounts, err := s.q.MetricsAccounts(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range accounts {
		m.Accounts = append(m.Accounts, AccountMetrics{
			ID: a.ID, GroupID: a.GroupID, Kind: AccountKind(a.Kind), Enabled: a.Enabled,
			Messages: a.Messages, Bytes: a.Bytes, LastSuccessAt: a.LastSuccessAt,
		})
	}
	users, err := s.q.MetricsUsers(ctx)
	if err != nil {
		return nil, err
	}
	for _, u := range users {
		m.Users = append(m.Users, UserCount{Role: u.Role, Locked: u.Locked, N: u.N})
	}
	if m.Jobs, err = s.JobStats(ctx); err != nil {
		return nil, err
	}
	return &m, nil
}
