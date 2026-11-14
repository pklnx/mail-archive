package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/keepia/archive/internal/config"
	"github.com/keepia/archive/internal/store"
)

// Customer groups for admins (#470): the operator creates, renames and
// removes groups and moves users between them. Users who are not admins
// never reach these routes: every handler checks the role before it reads
// the path or the body, so a non-admin learns nothing about which ids
// exist.

type groupJSON struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Users and Accounts count the group's users and their accounts
	// (removed accounts included).
	Users     int64     `json:"users"`
	Accounts  int64     `json:"accounts"`
	CreatedAt time.Time `json:"createdAt"`
	// QuotaBytes is the group's storage quota, null without one; Bytes its
	// usage (an upper bound between snapshots), State its quota state
	// (#397, part 4).
	QuotaBytes *int64 `json:"quotaBytes"`
	Bytes      int64  `json:"bytes"`
	State      string `json:"state"`
}

// groupRefJSON names a user's group in GET /api/users.
type groupRefJSON struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type groupInput struct {
	Name string `json:"name"`
}

const groupsAdminOnly = "only admins can manage groups"

func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminFor(w, r, groupsAdminOnly) {
		return
	}
	groups, err := s.store.ListGroups(r.Context())
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	usage, err := s.store.GroupsUsage(r.Context())
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	byID := make(map[int64]store.GroupUsage, len(usage))
	for _, u := range usage {
		byID[u.ID] = u
	}
	out := make([]groupJSON, 0, len(groups))
	for _, g := range groups {
		// A group created after the usage read has no figures yet: ok, 0.
		u, ok := byID[g.ID]
		if !ok {
			u.State = "ok"
		}
		out = append(out, groupJSON{ID: g.ID, Name: g.Name, Users: g.Users, Accounts: g.Accounts, CreatedAt: g.CreatedAt,
			QuotaBytes: u.QuotaBytes, Bytes: u.Bytes, State: u.State})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"groups": out})
}

// groupName reads and checks the name of a create or rename request.
func (s *Server) groupName(w http.ResponseWriter, r *http.Request) (string, bool) {
	var in groupInput
	if !s.decode(w, r, &in) {
		return "", false
	}
	name := strings.TrimSpace(in.Name)
	if err := store.ValidateGroupName(name); err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
		return "", false
	}
	return name, true
}

func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminFor(w, r, groupsAdminOnly) {
		return
	}
	name, ok := s.groupName(w, r)
	if !ok {
		return
	}
	g, err := s.store.CreateGroup(withAudit(r, s.targetedEntry(r, "group.add")), name)
	if errors.Is(err, store.ErrConflict) {
		s.fail(w, r, http.StatusConflict, fmt.Sprintf("a group named %q already exists", name), nil)
		return
	}
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	s.log.Info("group added", "group", g.Name, "by", currentSession(r).UserName)
	s.writeJSON(w, http.StatusCreated, groupJSON{ID: g.ID, Name: g.Name, CreatedAt: g.CreatedAt, State: "ok"})
}

// pathGroupID reads {id} of a group route, after the admin check.
func (s *Server) pathGroupID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	if !s.requireAdminFor(w, r, groupsAdminOnly) {
		return 0, false
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.fail(w, r, http.StatusNotFound, "no such group", nil)
		return 0, false
	}
	return id, true
}

// groupChange is the body of PATCH /api/groups/{id}: exactly one of
// "name" (a rename) and "quotaBytes" (the storage quota in bytes, null to
// remove it; #397, part 4).
type groupChange struct {
	name  *string
	quota *json.RawMessage
}

// decodeGroupChange reads a PATCH body with exactly one known field.
func (s *Server) decodeGroupChange(w http.ResponseWriter, r *http.Request) (groupChange, bool) {
	var fields map[string]json.RawMessage
	dec := json.NewDecoder(io.LimitReader(r.Body, maxAccountBody))
	if err := dec.Decode(&fields); err != nil {
		s.fail(w, r, http.StatusBadRequest, "invalid request body: "+err.Error(), nil)
		return groupChange{}, false
	}
	var c groupChange
	for k, v := range fields {
		switch k {
		case "name":
			var name string
			if err := json.Unmarshal(v, &name); err != nil {
				s.fail(w, r, http.StatusBadRequest, "invalid request body: name is a string", nil)
				return groupChange{}, false
			}
			c.name = &name
		case "quotaBytes":
			c.quota = &v
		default:
			s.fail(w, r, http.StatusBadRequest, fmt.Sprintf("invalid request body: unknown field %q", k), nil)
			return groupChange{}, false
		}
	}
	if (c.name == nil) == (c.quota == nil) {
		s.fail(w, r, http.StatusBadRequest, `give exactly one of "name" and "quotaBytes"`, nil)
		return groupChange{}, false
	}
	return c, true
}

// handleUpdateGroup renames a group or sets its storage quota. The admin
// check comes before the path and the body are read.
func (s *Server) handleUpdateGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathGroupID(w, r)
	if !ok {
		return
	}
	c, ok := s.decodeGroupChange(w, r)
	if !ok {
		return
	}
	if c.quota != nil {
		s.setGroupQuota(w, r, id, *c.quota)
		return
	}
	name := strings.TrimSpace(*c.name)
	if err := store.ValidateGroupName(name); err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	}
	err := s.store.RenameGroup(withAudit(r, s.targetedEntry(r, "group.rename")), id, name)
	switch {
	case errors.Is(err, store.ErrUnknownGroup):
		s.fail(w, r, http.StatusNotFound, "no such group", nil)
		return
	case errors.Is(err, store.ErrConflict):
		s.fail(w, r, http.StatusConflict, fmt.Sprintf("a group named %q already exists", name), nil)
		return
	case err != nil:
		s.failStore(w, r, err)
		return
	}
	s.log.Info("group renamed", "group", name, "by", currentSession(r).UserName)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	id, ok := s.pathGroupID(w, r)
	if !ok {
		return
	}
	err := s.store.DeleteGroup(withAudit(r, s.targetedEntry(r, "group.remove")), id)
	switch {
	case errors.Is(err, store.ErrUnknownGroup):
		s.fail(w, r, http.StatusNotFound, "no such group", nil)
		return
	case errors.Is(err, store.ErrGroupHeld):
		// Holds are released, never deleted: their record keeps the group.
		s.fail(w, r, http.StatusConflict, "a legal hold names the group, so it stays", nil)
		return
	case errors.Is(err, store.ErrGroupInUse):
		s.fail(w, r, http.StatusConflict, "the group still has users; move them to another group first", nil)
		return
	case errors.Is(err, store.ErrLastGroup):
		s.fail(w, r, http.StatusConflict, store.ErrLastGroup.Error(), nil)
		return
	case err != nil:
		s.failStore(w, r, err)
		return
	}
	s.log.Info("group removed", "id", id, "by", currentSession(r).UserName)
	w.WriteHeader(http.StatusNoContent)
}

// groupRefs maps group ids to their names, for the users list.
func (s *Server) groupRefs(r *http.Request) (map[int64]groupRefJSON, error) {
	groups, err := s.store.ListGroups(r.Context())
	if err != nil {
		return nil, err
	}
	out := make(map[int64]groupRefJSON, len(groups))
	for _, g := range groups {
		out[g.ID] = groupRefJSON{ID: g.ID, Name: g.Name}
	}
	return out, nil
}

// changeUserGroup is PATCH /api/users/{name} with {"group": id}: it moves
// u and answers how many shares that ended and how many auditor grants it
// revoked (#393).
func (s *Server) changeUserGroup(w http.ResponseWriter, r *http.Request, u *store.User, groupID int64) {
	if groupID <= 0 {
		s.fail(w, r, http.StatusBadRequest, store.ErrUnknownGroup.Error(), nil)
		return
	}
	e := s.targetedEntry(r, "user.group_change")
	auditUser(e, u)
	c, err := s.store.SetUserGroup(withAudit(r, e), u.ID, groupID)
	if errors.Is(err, store.ErrUnknownGroup) {
		s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	}
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	if c.Changed {
		s.log.Info("user moved to another group", "user", u.Name, "from", c.From, "to", c.To, "sharesEnded", len(c.Ended),
			"grantsRevoked", len(c.GrantsRevoked), "by", currentSession(r).UserName)
	}
	s.writeJSON(w, http.StatusOK, map[string]int{"sharesEnded": len(c.Ended), "grantsRevoked": len(c.GrantsRevoked)})
}

// setGroupQuota is PATCH /api/groups/{id} with {"quotaBytes": n|null}: a
// positive number of bytes, at most the instance quota and at most 1 EiB
// (config.MaxByteSize, the CLI's bound), or null to remove it. Audited as
// group.quota with the old and the new value. The gate reads the figures
// again at once, so the new state shows right away.
func (s *Server) setGroupQuota(w http.ResponseWriter, r *http.Request, id int64, raw json.RawMessage) {
	var quotaBytes *int64
	if err := json.Unmarshal(raw, &quotaBytes); err != nil || (quotaBytes != nil && *quotaBytes <= 0) {
		s.fail(w, r, http.StatusBadRequest, "quotaBytes is a positive whole number of bytes, or null for no quota", nil)
		return
	}
	if quotaBytes != nil && *quotaBytes > config.MaxByteSize {
		s.fail(w, r, http.StatusBadRequest,
			fmt.Sprintf("the quota is above the largest size of %d bytes (1 EiB)", int64(config.MaxByteSize)), nil)
		return
	}
	if limit := s.storageLimits().InstanceQuota; quotaBytes != nil && limit > 0 && *quotaBytes > limit {
		s.fail(w, r, http.StatusBadRequest,
			fmt.Sprintf("the quota is above the instance quota of %d bytes; a group quota cannot be larger", limit), nil)
		return
	}
	c, err := s.store.SetGroupQuota(withAudit(r, s.targetedEntry(r, "group.quota")), id, quotaBytes)
	switch {
	case errors.Is(err, store.ErrUnknownGroup):
		s.fail(w, r, http.StatusNotFound, "no such group", nil)
		return
	case err != nil:
		s.failStore(w, r, err)
		return
	}
	if c.Changed {
		s.log.Info("group quota set", "group", c.Name, "quota_bytes", quotaBytes, "by", currentSession(r).UserName)
		if g := s.storageGate(); g != nil {
			g.Refresh(r.Context())
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
