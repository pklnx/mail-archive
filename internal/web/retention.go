package web

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/store"
)

// Retention rules and legal holds (#55). Admins only.

type retentionRuleJSON struct {
	ID             int64     `json:"id"`
	AccountID      int64     `json:"accountId"`
	Account        string    `json:"account"`
	AccountKind    string    `json:"accountKind"`
	AccountRemoved bool      `json:"accountRemoved"`
	Owner          string    `json:"owner"`
	Folder         string    `json:"folder"` // empty: every folder
	Years          int       `json:"years"`
	TimeZone       string    `json:"timeZone"`
	Note           string    `json:"note"`
	CreatedAt      time.Time `json:"createdAt"`
	CreatedBy      string    `json:"createdBy"`
}

type retentionAccountJSON struct {
	ID      int64    `json:"id"`
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`
	Removed bool     `json:"removed"`
	Owner   string   `json:"owner"`
	Folders []string `json:"folders"`
}

type retentionRulesJSON struct {
	Rules []retentionRuleJSON `json:"rules"`
	// Accounts to choose from, with their folders.
	Accounts []retentionAccountJSON `json:"accounts"`
	// MinYears: shorter rules need force.
	MinYears        int    `json:"minYears"`
	DefaultTimeZone string `json:"defaultTimeZone"`
}

func (s *Server) handleListRetentionRules(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	rules, err := s.store.RetentionRules(r.Context())
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	accounts, err := s.store.RetentionAccounts(r.Context())
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	out := retentionRulesJSON{
		Rules: make([]retentionRuleJSON, 0, len(rules)), Accounts: make([]retentionAccountJSON, 0, len(accounts)),
		MinYears: store.MinBusinessLetterYears, DefaultTimeZone: store.DefaultRetentionTimeZone,
	}
	for _, x := range rules {
		out.Rules = append(out.Rules, retentionRuleJSON{
			ID: x.ID, AccountID: x.AccountID, Account: x.Account, AccountKind: string(x.AccountKind), AccountRemoved: x.AccountRemoved,
			Owner: x.Owner, Folder: x.Folder, Years: x.Years, TimeZone: x.TimeZone, Note: x.Note,
			CreatedAt: x.CreatedAt, CreatedBy: x.CreatedBy,
		})
	}
	for _, a := range accounts {
		out.Accounts = append(out.Accounts, retentionAccountJSON{
			ID: a.ID, Name: a.Name, Kind: string(a.Kind), Removed: a.Removed, Owner: a.Owner, Folders: a.Folders,
		})
	}
	s.writeJSON(w, http.StatusOK, out)
}

type retentionRuleInput struct {
	AccountID int64  `json:"accountId"`
	Folder    string `json:"folder"`
	Years     int    `json:"years"`
	TimeZone  string `json:"timeZone"`
	Note      string `json:"note"`
	// Force allows a period under MinYears.
	Force bool `json:"force"`
}

func (s *Server) handleCreateRetentionRule(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var in retentionRuleInput
	if !s.decode(w, r, &in) {
		return
	}
	if in.Years < 1 || in.Years > 100 {
		s.fail(w, r, http.StatusBadRequest, "years must be between 1 and 100", nil)
		return
	}
	if in.Years < store.MinBusinessLetterYears && !in.Force {
		s.fail(w, r, http.StatusBadRequest, fmt.Sprintf("%d years is shorter than the %d years for business letters (§147 AO); confirm with force",
			in.Years, store.MinBusinessLetterYears), nil)
		return
	}
	acc, err := s.store.GetAccount(r.Context(), in.AccountID)
	if errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, http.StatusBadRequest, "unknown account", nil)
		return
	}
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	e := s.auditEntry(r, "retention.rule_add")
	auditAccount(e, acc)
	e.Detail("years", in.Years)
	if in.Folder != "" {
		e.Detail("folder", in.Folder)
	}
	by := userID(r)
	id, err := s.store.AddRetentionRule(withAudit(r, e), store.NewRetentionRule{
		AccountID: acc.ID, Folder: in.Folder, Years: in.Years, TimeZone: in.TimeZone, Note: in.Note, CreatedBy: &by,
	})
	switch {
	case errors.Is(err, store.ErrInvalidRetention):
		s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	case errors.Is(err, store.ErrConflict):
		s.fail(w, r, http.StatusConflict, "this account or folder already has a rule; remove it first", nil)
		return
	case err != nil:
		s.failStore(w, r, err)
		return
	}
	s.log.Info("retention rule added", "id", id, "account", acc.Name, "folder", in.Folder, "years", in.Years, "by", currentSession(r).UserName)
	s.writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (s *Server) handleDeleteRetentionRule(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "not found", nil)
		return
	}
	if err := s.store.RemoveRetentionRule(withAudit(r, s.auditEntry(r, "retention.rule_remove")), id); err != nil {
		s.failStore(w, r, err)
		return
	}
	s.log.Info("retention rule removed", "id", id, "by", currentSession(r).UserName)
	w.WriteHeader(http.StatusNoContent)
}

type holdFilterJSON struct {
	Query         string `json:"q,omitempty"`
	From          string `json:"from,omitempty"`
	To            string `json:"to,omitempty"`
	Attachment    string `json:"attachment,omitempty"`
	HasAttachment bool   `json:"hasAttachment,omitempty"`
}

type legalHoldJSON struct {
	ID           int64           `json:"id"`
	Name         string          `json:"name"`
	Reason       string          `json:"reason"`
	User         string          `json:"user"`
	AccountID    *int64          `json:"accountId"`
	Account      string          `json:"account"`
	AccountOwner string          `json:"accountOwner"`
	Filter       *holdFilterJSON `json:"filter"`
	Since        *time.Time      `json:"since"`
	Until        *time.Time      `json:"until"`
	MatchedAt    *time.Time      `json:"matchedAt"`
	Matched      int64           `json:"matched"`
	CreatedAt    time.Time       `json:"createdAt"`
	CreatedBy    string          `json:"createdBy"`
	ReleasedAt   *time.Time      `json:"releasedAt"`
	ReleasedBy   string          `json:"releasedBy"`
	ReleaseNote  string          `json:"releaseNote"`
}

func (s *Server) handleListLegalHolds(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	holds, err := s.store.LegalHolds(r.Context(), r.URL.Query().Get("released") == "1")
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	out := make([]legalHoldJSON, 0, len(holds))
	for _, h := range holds {
		j := legalHoldJSON{
			ID: h.ID, Name: h.Name, Reason: h.Reason, User: h.Owner, AccountID: h.AccountID, Account: h.Account,
			AccountOwner: h.AccountOwner, Since: h.PeriodStart, Until: h.PeriodEnd, MatchedAt: h.MatchedAt, Matched: h.Matched,
			CreatedAt: h.CreatedAt, CreatedBy: h.CreatedBy, ReleasedAt: h.ReleasedAt, ReleasedBy: h.ReleasedBy, ReleaseNote: h.ReleaseNote,
		}
		if f := h.Filter; f != nil {
			j.Filter = &holdFilterJSON{Query: f.Query, From: f.From, To: f.To, Attachment: f.Attachment, HasAttachment: f.HasAttachment}
		}
		out = append(out, j)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"holds": out})
}

type legalHoldInput struct {
	Name      string          `json:"name"`
	Reason    string          `json:"reason"`
	User      string          `json:"user"`
	AccountID *int64          `json:"accountId"`
	Filter    *holdFilterJSON `json:"filter"`
	Since     *time.Time      `json:"since"`
	Until     *time.Time      `json:"until"`
	// All confirms a hold without any condition, over the whole archive.
	All bool `json:"all"`
}

func (s *Server) handleCreateLegalHold(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var in legalHoldInput
	if !s.decode(w, r, &in) {
		return
	}
	h := store.NewLegalHold{Name: in.Name, Reason: in.Reason, AccountID: in.AccountID, PeriodStart: in.Since, PeriodEnd: in.Until}
	if f := in.Filter; f != nil {
		h.Filter = &store.HoldFilter{Query: f.Query, From: f.From, To: f.To, Attachment: f.Attachment, HasAttachment: f.HasAttachment}
		if h.Filter.IsZero() {
			h.Filter = nil
		}
	}
	e := s.auditEntry(r, "hold.add")
	if in.User != "" {
		u, err := s.store.GetUserByName(r.Context(), auth.NormalizeUserName(in.User))
		if errors.Is(err, store.ErrNotFound) {
			s.fail(w, r, http.StatusBadRequest, "unknown user", nil)
			return
		}
		if err != nil {
			s.failStore(w, r, err)
			return
		}
		h.OwnerID = &u.ID
		auditUser(e, u)
	}
	if in.AccountID != nil {
		acc, err := s.store.GetAccount(r.Context(), *in.AccountID)
		if errors.Is(err, store.ErrNotFound) {
			s.fail(w, r, http.StatusBadRequest, "unknown account", nil)
			return
		}
		if err != nil {
			s.failStore(w, r, err)
			return
		}
		auditAccount(e, acc)
	}
	if h.OwnerID == nil && h.AccountID == nil && h.Filter == nil && h.PeriodStart == nil && h.PeriodEnd == nil && !in.All {
		s.fail(w, r, http.StatusBadRequest, "a hold without user, account, period or filter covers the whole archive; confirm with all", nil)
		return
	}
	by := userID(r)
	h.CreatedBy = &by
	id, err := s.store.AddLegalHold(withAudit(r, e), h)
	if errors.Is(err, store.ErrInvalidRetention) {
		s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	}
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	s.log.Info("legal hold added", "id", id, "name", h.Name, "by", currentSession(r).UserName)
	out := map[string]int64{"id": id}
	if h.Filter != nil {
		n, err := s.store.MatchLegalHolds(r.Context())
		if err != nil {
			// The hold covers its whole scope until it is matched.
			s.log.Error("matching legal holds failed", "err", err)
		}
		out["matched"] = n
	}
	s.writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleReleaseLegalHold(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "not found", nil)
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	by := userID(r)
	err = s.store.ReleaseLegalHold(withAudit(r, s.auditEntry(r, "hold.release")), id, &by, in.Note)
	switch {
	case errors.Is(err, store.ErrInvalidRetention):
		s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	case err != nil:
		s.failStore(w, r, err)
		return
	}
	s.log.Info("legal hold released", "id", id, "by", currentSession(r).UserName)
	w.WriteHeader(http.StatusNoContent)
}
