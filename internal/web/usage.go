package web

import (
	"context"
	"net/http"
	"time"

	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/quota"
	"github.com/pklnx/mail-archive/internal/store"
)

// Storage usage in the web UI (#397, part 4). GET /api/usage answers by
// role, with a session only:
//
//   - Admins get the instance (usage, quota, state, disk, database, last
//     snapshot, last backup, encryption), every customer group with its
//     quota and state, the accounts without a group, and every account
//     with its owner, group and exclusive bytes.
//   - Members get the size of each of their own accounts and the effective
//     state of their group as the plain enum ok, full or overflow: the
//     same answer, byte for byte, whichever limit was reached (the group's,
//     the instance's or the disk's), so a member never learns another
//     group's usage, quota or state, nor the instance's (#539). Shared
//     accounts carry no figures; usageMemberJSON has no field that could.
//   - Auditors are refused (the route is not on their allowlist).

// usageFigureJSON is a count of unique messages and their bytes.
type usageFigureJSON struct {
	Messages int64 `json:"messages"`
	Bytes    int64 `json:"bytes"`
}

// usageDiskJSON is the data directory's file system.
type usageDiskJSON struct {
	FreeBytes  uint64 `json:"freeBytes"`
	TotalBytes uint64 `json:"totalBytes"`
}

type usageInstanceJSON struct {
	Messages int64 `json:"messages"`
	Bytes    int64 `json:"bytes"`
	// QuotaBytes is the instance quota (MAIL_ARCHIVE_STORAGE_QUOTA), null
	// without one.
	QuotaBytes *int64 `json:"quotaBytes"`
	// WarnPercent and OverflowPercent are the thresholds of every quota.
	WarnPercent     int `json:"warnPercent"`
	OverflowPercent int `json:"overflowPercent"`
	// MinFreeDiskBytes is the disk reserve, 0 when it is off.
	MinFreeDiskBytes int64 `json:"minFreeDiskBytes"`
	// State is ok, warn, full, overflow or disk_low.
	State      string    `json:"state"`
	StateSince time.Time `json:"stateSince"`
	// Disk is null when the data disk could not be measured.
	Disk          *usageDiskJSON   `json:"disk"`
	DatabaseBytes int64            `json:"databaseBytes"`
	Relations     map[string]int64 `json:"relations"`
	LastBackupAt  *time.Time       `json:"lastBackupAt"`
	// Encryption is the state of encryption at rest: off, on, or a
	// conversion in progress.
	Encryption string `json:"encryption"`
}

type usageGroupJSON struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Messages   int64      `json:"messages"`
	Bytes      int64      `json:"bytes"`
	QuotaBytes *int64     `json:"quotaBytes"`
	State      string     `json:"state"`
	StateSince *time.Time `json:"stateSince"`
}

type usageAccountJSON struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Removed bool   `json:"removed"`
	// Owner is empty and GroupID null for an account without an owner.
	Owner   string `json:"owner"`
	GroupID *int64 `json:"groupId"`
	// Own: the caller owns the account (the account page shows its size).
	Own            bool  `json:"own"`
	Messages       int64 `json:"messages"`
	Bytes          int64 `json:"bytes"`
	ExclusiveBytes int64 `json:"exclusiveBytes"`
	// Counted is false before the first snapshot that saw the account.
	Counted bool `json:"counted"`
}

// usageAdminJSON is an admin's GET /api/usage.
type usageAdminJSON struct {
	Instance usageInstanceJSON `json:"instance"`
	Groups   []usageGroupJSON  `json:"groups"`
	// Ungrouped are the accounts without an owner (only before the first
	// user), at the last snapshot.
	Ungrouped usageFigureJSON    `json:"ungrouped"`
	Accounts  []usageAccountJSON `json:"accounts"`
	// ComputedAt is when the last snapshot ran (per-account figures and
	// the group figures' base); null before the first.
	ComputedAt *time.Time `json:"computedAt"`
}

// usageMemberJSON is a member's GET /api/usage. Its fields are an
// allowlist (TestUsageMemberFields): no group id, no limit, no cause.
type usageMemberJSON struct {
	// State is the effective state of the member's group: ok, full or
	// overflow (memberState).
	State    string                `json:"state"`
	Accounts []usageOwnAccountJSON `json:"accounts"`
}

// usageOwnAccountJSON is one of the member's own accounts. Messages and
// Bytes are null before the first snapshot that saw it.
type usageOwnAccountJSON struct {
	Name     string `json:"name"`
	Messages *int64 `json:"messages"`
	Bytes    *int64 `json:"bytes"`
}

// memberState is what a member learns of the effective state: whether new
// mail is archived. warn is ok (it says nothing about the member's own
// mail and would tell how full the instance is); disk_low is overflow (it
// would name the cause).
func memberState(st quota.State) string {
	switch {
	case st >= quota.Overflow:
		return quota.Overflow.String()
	case st >= quota.Full:
		return quota.Full.String()
	}
	return quota.OK.String()
}

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	sess := currentSession(r)
	if sess == nil {
		s.fail(w, r, http.StatusForbidden, "a session is needed", nil)
		return
	}
	if !sess.IsAdmin {
		out, err := s.memberUsage(r.Context(), sess)
		if err != nil {
			s.failStore(w, r, err)
			return
		}
		s.writeJSON(w, http.StatusOK, out)
		return
	}
	out, err := s.adminUsage(r.Context(), sess.UserID)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, out)
}

// memberUsage reads a member's view: the gate's effective state of their
// group, a lookup in memory, and their own accounts' last snapshot.
func (s *Server) memberUsage(ctx context.Context, sess *store.Session) (*usageMemberJSON, error) {
	group := sess.GroupID
	out := &usageMemberJSON{State: memberState(s.storageGate().State(&group)), Accounts: []usageOwnAccountJSON{}}
	accounts, err := s.store.AccountsUsage(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range accounts {
		if a.OwnerID == nil || *a.OwnerID != sess.UserID {
			continue
		}
		row := usageOwnAccountJSON{Name: a.Name}
		if a.SnapshotAt != nil {
			row.Messages, row.Bytes = &a.Messages, &a.Bytes
		}
		out.Accounts = append(out.Accounts, row)
	}
	return out, nil
}

// storageLimits are the instance's limits: the gate's, or without one
// (browse-only) the quota and the defaults.
func (s *Server) storageLimits() quota.Config {
	if g := s.storageGate(); g != nil {
		return g.Config()
	}
	return quota.Config{InstanceQuota: s.storageQuota, Thresholds: quota.Thresholds{
		WarnPercent: config.DefaultStorageWarnPercent, OverflowPercent: config.DefaultStorageSMTPOverflowPercent,
	}}
}

func (s *Server) adminUsage(ctx context.Context, me int64) (*usageAdminJSON, error) {
	u, err := s.store.StorageUsage(ctx)
	if err != nil {
		return nil, err
	}
	limits := s.storageLimits()
	in := usageInstanceJSON{
		Messages: u.Messages, Bytes: u.Bytes,
		WarnPercent: limits.WarnPercent, OverflowPercent: limits.OverflowPercent,
		MinFreeDiskBytes: limits.MinFreeDisk,
		State:            u.State, StateSince: u.StateSince,
		DatabaseBytes: u.DatabaseBytes, Relations: u.RelationBytes,
	}
	if limits.InstanceQuota > 0 {
		q := limits.InstanceQuota
		in.QuotaBytes = &q
	}
	if in.Relations == nil {
		in.Relations = map[string]int64{}
	}
	if s.diskUsage != nil {
		free, total, err := s.diskUsage()
		if err != nil {
			s.log.Warn("usage: measure the data disk", "err", err)
		} else {
			in.Disk = &usageDiskJSON{FreeBytes: free, TotalBytes: total}
		}
	}
	if in.LastBackupAt, err = s.store.LastBackupSince(ctx, s.currentTime().Add(-metricsBackupLookBack)); err != nil {
		return nil, err
	}
	is, err := s.store.InstanceStatus(ctx, s.version)
	if err != nil {
		return nil, err
	}
	in.Encryption = is.EncryptionAtRest
	if in.Encryption == store.BlobOff {
		in.Encryption = "off"
	}
	out := &usageAdminJSON{
		Instance: in, Groups: make([]usageGroupJSON, 0, len(u.Groups)), Accounts: make([]usageAccountJSON, 0, len(u.Accounts)),
		Ungrouped: usageFigureJSON{Messages: u.Ungrouped.Messages, Bytes: u.Ungrouped.Bytes}, ComputedAt: u.SnapshotAt,
	}
	for _, g := range u.Groups {
		out.Groups = append(out.Groups, usageGroupJSON{
			ID: g.ID, Name: g.Name, Messages: g.Messages, Bytes: g.Bytes,
			QuotaBytes: g.QuotaBytes, State: g.State, StateSince: g.StateSince,
		})
	}
	for _, a := range u.Accounts {
		out.Accounts = append(out.Accounts, usageAccountJSON{
			ID: a.ID, Name: a.Name, Kind: string(a.Kind), Removed: a.Removed, Owner: a.Owner, GroupID: a.GroupID,
			Own:      a.OwnerID != nil && *a.OwnerID == me,
			Messages: a.Messages, Bytes: a.Bytes,
			ExclusiveBytes: a.ExclusiveBytes, Counted: a.SnapshotAt != nil,
		})
	}
	return out, nil
}

// handleRefreshUsage requests a usage snapshot now (the singleton job
// usage_refresh): admins only.
func (s *Server) handleRefreshUsage(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminFor(w, r, "only admins can refresh the storage figures") {
		return
	}
	if err := s.store.RequestUsageRefresh(r.Context()); err != nil {
		s.failStore(w, r, err)
		return
	}
	s.log.Info("usage refresh requested", "by", currentSession(r).UserName)
	w.WriteHeader(http.StatusAccepted)
}
