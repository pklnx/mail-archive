package web

import (
	"errors"
	"net/http"

	"github.com/pklnx/mail-archive/internal/store"
)

// Deleting a removed account completely (#155). Admins only, and only for
// their own accounts: other users' removed accounts are purged with
// `account purge NAME --user USER`. The web server's retention job
// executes a confirmed purge (archive.ExecutePurge), never the request.

const adminOnlyPurge = "only admins can delete accounts completely"

type purgeRuleJSON struct {
	ID int64 `json:"id"`
	// Folder is empty for a rule on all folders.
	Folder string `json:"folder"`
	Years  int    `json:"years"`
}

type purgeHoldJSON struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Released bool   `json:"released"`
	// NamesAccount: the hold names the account; otherwise it covers some
	// of its mail.
	NamesAccount bool `json:"namesAccount"`
}

type purgeBlockersJSON struct {
	NotRemoved bool `json:"notRemoved"`
	Syncing    bool `json:"syncing"`
	// PurgeOpen: another account's purge is confirmed and not finished.
	PurgeOpen bool            `json:"purgeOpen"`
	Rules     []purgeRuleJSON `json:"rules"`
	Holds     []purgeHoldJSON `json:"holds"`
	// Messages word the blockers, naming the rules and holds.
	Messages []string `json:"messages"`
}

type purgeKeptJSON struct {
	// Owner is empty for an account without owner.
	Owner    string `json:"owner"`
	Account  string `json:"account"`
	Messages int64  `json:"messages"`
}

type purgeStateJSON struct {
	ID int64 `json:"id"`
	// State is "confirmed" while it is being executed.
	State     string `json:"state"`
	Expected  int    `json:"expected"`
	Locations int64  `json:"locations"`
	Deleted   int    `json:"deleted"`
	Kept      int    `json:"kept"`
}

type purgePreviewJSON struct {
	Account      string          `json:"account"`
	Kind         string          `json:"kind"`
	Locations    int64           `json:"locations"`
	Messages     int64           `json:"messages"`
	Deleted      int64           `json:"deleted"`
	DeletedBytes int64           `json:"deletedBytes"`
	Kept         int64           `json:"kept"`
	KeptIn       []purgeKeptJSON `json:"keptIn"`
	// Grace is MAIL_ARCHIVE_RETENTION_FILE_GRACE ("720h0m0s"): how long
	// the files of deleted messages are kept.
	Grace    string            `json:"grace"`
	Blockers purgeBlockersJSON `json:"blockers"`
	// Purge is the account's confirmed purge, null without one.
	Purge *purgeStateJSON `json:"purge"`
}

// pathPurgeAccount loads the admin's own account named in the URL; other
// users' accounts are not found. Removed accounts are accepted.
func (s *Server) pathPurgeAccount(w http.ResponseWriter, r *http.Request) (*store.Account, bool) {
	if !s.requireAdminFor(w, r, adminOnlyPurge) {
		return nil, false
	}
	a, err := s.store.GetOwnedAccount(r.Context(), userID(r), r.PathValue("name"))
	if err != nil {
		s.failStore(w, r, err)
		return nil, false
	}
	return a, true
}

func (s *Server) handlePurgePreview(w http.ResponseWriter, r *http.Request) {
	a, ok := s.pathPurgeAccount(w, r)
	if !ok {
		return
	}
	p, err := s.store.PurgePreview(r.Context(), a.ID)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	b := &p.Blockers
	out := purgePreviewJSON{
		Account: a.Name, Kind: string(a.Kind), Locations: p.Locations, Messages: p.Messages, Deleted: p.Deleted,
		DeletedBytes: p.DeletedBytes, Kept: p.Kept, KeptIn: []purgeKeptJSON{},
		Blockers: purgeBlockersJSON{
			NotRemoved: b.NotRemoved, Syncing: b.Syncing, PurgeOpen: b.OpenPurge != nil,
			Rules: []purgeRuleJSON{}, Holds: []purgeHoldJSON{}, Messages: b.Messages(a.Name),
		},
	}
	if s.purgeGrace != nil {
		out.Grace = s.purgeGrace.String()
	}
	if out.Blockers.Messages == nil {
		out.Blockers.Messages = []string{}
	}
	for _, k := range p.KeptIn {
		out.KeptIn = append(out.KeptIn, purgeKeptJSON{Owner: k.Owner, Account: k.Account, Messages: k.Messages})
	}
	for _, rule := range b.Rules {
		out.Blockers.Rules = append(out.Blockers.Rules, purgeRuleJSON{ID: rule.ID, Folder: rule.Folder, Years: rule.Years})
	}
	for _, h := range b.Holds {
		out.Blockers.Holds = append(out.Blockers.Holds, purgeHoldJSON{ID: h.ID, Name: h.Name, Released: h.Released, NamesAccount: h.NamesAccount})
	}
	if o := p.Open; o != nil {
		out.Purge = &purgeStateJSON{ID: o.ID, State: o.State, Expected: o.Expected, Locations: o.Locations, Deleted: o.Deleted, Kept: o.Kept}
	}
	s.writeJSON(w, http.StatusOK, out)
}

// purgeChanged is the 409 answer when the number of messages to delete is
// no longer the one typed.
type purgeChanged struct {
	Error string `json:"error"`
	// Deleted is the number of messages a purge would delete now.
	Deleted int64 `json:"deleted"`
}

func (s *Server) handleConfirmPurge(w http.ResponseWriter, r *http.Request) {
	a, ok := s.pathPurgeAccount(w, r)
	if !ok {
		return
	}
	var in struct {
		// Count must be the preview's number of messages to delete, typed
		// by the admin.
		Count *int64 `json:"count"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if in.Count == nil {
		s.fail(w, r, http.StatusBadRequest, "type the number of messages to delete to confirm", nil)
		return
	}
	if s.retention == nil {
		// Nothing would execute it until the next start.
		s.fail(w, r, http.StatusServiceUnavailable, "this server does not execute purges; use the command line: account purge NAME", nil)
		return
	}
	by := userID(r)
	purge, err := s.store.ConfirmPurge(withAudit(r, s.auditEntry(r, "account.purge")), a.Ref(), *in.Count, &by)
	var blocked *store.PurgeBlockedError
	var changed *store.PurgeChangedError
	switch {
	case errors.As(err, &changed):
		s.writeJSON(w, http.StatusConflict, purgeChanged{Error: changed.Error(), Deleted: changed.Now})
		return
	case errors.As(err, &blocked), errors.Is(err, store.ErrPurgeOpen), errors.Is(err, store.ErrStale):
		s.fail(w, r, http.StatusConflict, err.Error(), nil)
		return
	case err != nil:
		s.failStore(w, r, err)
		return
	}
	s.log.Info("account purge confirmed", "purge", purge.ID, "account", a.Name, "by", currentSession(r).UserName)
	s.retention.Wake()
	s.writeJSON(w, http.StatusAccepted, map[string]int64{"purge": purge.ID})
}
