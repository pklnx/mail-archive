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

// Retention rules and legal holds (#55). Admins only. Rules, accounts,
// holds and the groups of a deletion review carry their customer group
// (#470), and a hold can name a group.

// groupRef is the JSON of a customer group an entry belongs to; nil
// without one.
func groupRef(id *int64, name string) *groupRefJSON {
	if id == nil {
		return nil
	}
	return &groupRefJSON{ID: *id, Name: name}
}

type retentionRuleJSON struct {
	ID             int64  `json:"id"`
	AccountID      int64  `json:"accountId"`
	Account        string `json:"account"`
	AccountKind    string `json:"accountKind"`
	AccountRemoved bool   `json:"accountRemoved"`
	Owner          string `json:"owner"`
	// Group is the account's owner's customer group; null without an owner.
	Group     *groupRefJSON `json:"group"`
	Folder    string        `json:"folder"` // empty: every folder
	Years     int           `json:"years"`
	TimeZone  string        `json:"timeZone"`
	Note      string        `json:"note"`
	CreatedAt time.Time     `json:"createdAt"`
	CreatedBy string        `json:"createdBy"`
}

type retentionAccountJSON struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Removed bool   `json:"removed"`
	Owner   string `json:"owner"`
	// Group is the owner's customer group; null without an owner.
	Group   *groupRefJSON `json:"group"`
	Folders []string      `json:"folders"`
}

type retentionRulesJSON struct {
	Rules []retentionRuleJSON `json:"rules"`
	// Accounts to choose from, with their folders.
	Accounts []retentionAccountJSON `json:"accounts"`
	// Groups are every customer group, for a hold's group and the filters.
	Groups []groupRefJSON `json:"groups"`
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
	groups, err := s.store.ListGroups(r.Context())
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	out := retentionRulesJSON{
		Rules: make([]retentionRuleJSON, 0, len(rules)), Accounts: make([]retentionAccountJSON, 0, len(accounts)),
		Groups:   make([]groupRefJSON, 0, len(groups)),
		MinYears: store.MinBusinessLetterYears, DefaultTimeZone: store.DefaultRetentionTimeZone,
	}
	for _, g := range groups {
		out.Groups = append(out.Groups, groupRefJSON{ID: g.ID, Name: g.Name})
	}
	for _, x := range rules {
		out.Rules = append(out.Rules, retentionRuleJSON{
			ID: x.ID, AccountID: x.AccountID, Account: x.Account, AccountKind: string(x.AccountKind), AccountRemoved: x.AccountRemoved,
			Owner: x.Owner, Group: groupRef(x.GroupID, x.Group), Folder: x.Folder, Years: x.Years, TimeZone: x.TimeZone, Note: x.Note,
			CreatedAt: x.CreatedAt, CreatedBy: x.CreatedBy,
		})
	}
	for _, a := range accounts {
		out.Accounts = append(out.Accounts, retentionAccountJSON{
			ID: a.ID, Name: a.Name, Kind: string(a.Kind), Removed: a.Removed, Owner: a.Owner,
			Group: groupRef(a.GroupID, a.Group), Folders: a.Folders,
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
	e := s.targetedEntry(r, "retention.rule_add")
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
	if err := s.store.RemoveRetentionRule(withAudit(r, s.targetedEntry(r, "retention.rule_remove")), id); err != nil {
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
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Reason       string `json:"reason"`
	User         string `json:"user"`
	AccountID    *int64 `json:"accountId"`
	Account      string `json:"account"`
	AccountOwner string `json:"accountOwner"`
	// Group is the customer group the hold names (#470); null if none.
	Group *groupRefJSON `json:"group"`
	// CoveredGroup is the group whose mail the hold covers: Group, or the
	// group of its user or of its account's owner; null for every group.
	CoveredGroup *groupRefJSON   `json:"coveredGroup"`
	Filter       *holdFilterJSON `json:"filter"`
	Since        *time.Time      `json:"since"`
	Until        *time.Time      `json:"until"`
	MatchedAt    *time.Time      `json:"matchedAt"`
	// Matched is how many messages the filter matched so far, only for a
	// hold on the caller or on one of their own accounts; null otherwise.
	Matched     *int64     `json:"matched"`
	CreatedAt   time.Time  `json:"createdAt"`
	CreatedBy   string     `json:"createdBy"`
	ReleasedAt  *time.Time `json:"releasedAt"`
	ReleasedBy  string     `json:"releasedBy"`
	ReleaseNote string     `json:"releaseNote"`
}

// holdOwnedBy reports whether every account a hold can cover belongs to
// the caller: the hold names the caller as its user, or names no user and
// one of the caller's own accounts. A number found by searching mail is
// shown only for such holds: admins see no mail except their own.
// Group, instance and other users' holds, and holds on accounts without
// owner, are not the caller's.
func holdOwnedBy(ownerID, accountID, accountOwnerID *int64, caller int64) bool {
	if ownerID != nil {
		return *ownerID == caller
	}
	return accountID != nil && accountOwnerID != nil && *accountOwnerID == caller
}

func (s *Server) handleListLegalHolds(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	caller := userID(r)
	// Only the caller's holds are counted: the time of this request must
	// not depend on what other holds' filters matched (#538).
	holds, err := s.store.LegalHoldsCountingFor(r.Context(), r.URL.Query().Get("released") == "1", caller)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	out := make([]legalHoldJSON, 0, len(holds))
	for _, h := range holds {
		j := legalHoldJSON{
			ID: h.ID, Name: h.Name, Reason: h.Reason, User: h.Owner, AccountID: h.AccountID, Account: h.Account,
			AccountOwner: h.AccountOwner, Group: groupRef(h.GroupID, h.Group),
			CoveredGroup: groupRef(h.CoveredGroupID, h.CoveredGroup), Since: h.PeriodStart, Until: h.PeriodEnd, MatchedAt: h.MatchedAt,
			CreatedAt: h.CreatedAt, CreatedBy: h.CreatedBy, ReleasedAt: h.ReleasedAt, ReleasedBy: h.ReleasedBy, ReleaseNote: h.ReleaseNote,
		}
		if holdOwnedBy(h.OwnerID, h.AccountID, h.AccountOwnerID, caller) {
			n := h.Matched
			j.Matched = &n
		}
		if f := h.Filter; f != nil {
			j.Filter = &holdFilterJSON{Query: f.Query, From: f.From, To: f.To, Attachment: f.Attachment, HasAttachment: f.HasAttachment}
		}
		out = append(out, j)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"holds": out})
}

type legalHoldInput struct {
	Name      string `json:"name"`
	Reason    string `json:"reason"`
	User      string `json:"user"`
	AccountID *int64 `json:"accountId"`
	// Group is a customer group's id (#470): the hold covers the accounts
	// of its users only, also of users who join it later.
	Group  *int64          `json:"group"`
	Filter *holdFilterJSON `json:"filter"`
	Since  *time.Time      `json:"since"`
	Until  *time.Time      `json:"until"`
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
	// A hold naming a user or an account is about them (their group), one
	// naming only a group about that group; one naming none of them covers
	// the instance and has no group (#470). The account wins over the user
	// as the target, as before.
	if in.Group != nil {
		g, err := s.store.GetGroup(r.Context(), *in.Group)
		if errors.Is(err, store.ErrUnknownGroup) {
			s.fail(w, r, http.StatusBadRequest, "unknown group", nil)
			return
		}
		if err != nil {
			s.failStore(w, r, err)
			return
		}
		h.GroupID = &g.ID
	}
	var owner *store.User
	var account *store.Account
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
		owner = u
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
		account = acc
	}
	by := userID(r)
	var accountOwner *int64
	if account != nil {
		accountOwner = account.OwnerID
	}
	if h.Filter != nil {
		// A filter decides which of the covered mail is kept, and so what
		// deletion runs list: in the web UI an admin chooses one only over
		// their own mail, the 1.5.3 rule of holdOwnedBy (#538). A filter
		// over other users' mail is the operator's, on the command line.
		// Nothing was done, so nothing is recorded.
		if !holdOwnedBy(h.OwnerID, h.AccountID, accountOwner, by) {
			s.log.Info("legal hold with a filter over other users' mail refused", "by", currentSession(r).UserName)
			s.writeJSON(w, http.StatusForbidden, apiError{
				Error: "A hold with a search filter over other users' mail is added by the operator: ./ma hold add --query",
				Code:  errFilterHoldCLI,
			})
			return
		}
		// A filter hold on the caller's own account names the caller too,
		// so it covers the account only while the caller owns it, and the
		// account cannot move to another owner while the hold is active
		// (ActiveHoldsOnOwner): the filter never runs over other users'
		// mail.
		if h.OwnerID == nil {
			h.OwnerID = &by
		}
	}
	var e *store.AuditEntry
	switch {
	case account != nil:
		e = s.targetedEntry(r, "hold.add")
		auditAccount(e, account)
	case owner != nil:
		e = s.targetedEntry(r, "hold.add")
		auditUser(e, owner)
	case h.GroupID != nil:
		// The store records it under the group.
		e = s.targetedEntry(r, "hold.add")
	default:
		e = s.instanceEntry(r, "hold.add")
	}
	if h.OwnerID == nil && h.AccountID == nil && h.GroupID == nil && h.Filter == nil && h.PeriodStart == nil && h.PeriodEnd == nil && !in.All {
		s.fail(w, r, http.StatusBadRequest, "a hold without user, account, period or filter covers the whole archive; confirm with all", nil)
		return
	}
	h.CreatedBy = &by
	id, err := s.store.AddLegalHold(withAudit(r, e), h)
	if errors.Is(err, store.ErrInvalidRetention) {
		s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	}
	if errors.Is(err, store.ErrUnknownGroup) {
		s.fail(w, r, http.StatusBadRequest, "unknown group", nil)
		return
	}
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	s.log.Info("legal hold added", "id", id, "name", h.Name, "by", currentSession(r).UserName)
	out := map[string]int64{"id": id}
	if h.Filter != nil {
		// Only the new hold is matched, which is the caller's (checked
		// above): the request's time depends on the caller's own mail
		// only, not on other holds' filters (#538). Deletion runs, purges
		// and the CLI still match every hold.
		n, err := s.store.MatchLegalHold(r.Context(), id)
		if err != nil {
			// The hold covers its whole scope until it is matched; its
			// count would be partial, so it is left out.
			s.log.Error("matching the legal hold failed", "id", id, "err", err)
		} else {
			s.log.Info("legal hold matched", "id", id, "added", n)
			m, err := s.store.LegalHoldMatched(r.Context(), id)
			if err != nil {
				s.log.Error("counting a legal hold's matches failed", "id", id, "err", err)
			} else {
				out["matched"] = m
			}
		}
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
	e, ok := s.holdEntry(w, r, "hold.release", id)
	if !ok {
		return
	}
	by := userID(r)
	err = s.store.ReleaseLegalHold(withAudit(r, e), id, &by, in.Note)
	switch {
	case errors.Is(err, store.ErrInvalidRetention):
		s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	case errors.Is(err, store.ErrConflict):
		s.writeJSON(w, http.StatusConflict, apiError{Error: "this legal hold is already released", Code: errReleased})
		return
	case err != nil:
		s.failStore(w, r, err)
		return
	}
	s.log.Info("legal hold released", "id", id, "by", currentSession(r).UserName)
	w.WriteHeader(http.StatusNoContent)
}

// Deletion runs: proposed here, reviewed with counts and hashes (no
// subjects: admins do not read other users' mail), confirmed by typing
// the number of messages, executed by the retention job of serve.

type deletionRunJSON struct {
	ID          int64      `json:"id"`
	State       string     `json:"state"`
	CreatedAt   time.Time  `json:"createdAt"`
	CreatedBy   string     `json:"createdBy"`
	Candidates  int        `json:"candidates"`
	Bytes       int64      `json:"bytes"`
	ConfirmedAt *time.Time `json:"confirmedAt"`
	ConfirmedBy string     `json:"confirmedBy"`
	FinishedAt  *time.Time `json:"finishedAt"`
	Deleted     int        `json:"deleted"`
	Kept        int        `json:"kept"`
}

func toRunJSON(r *store.DeletionRun) deletionRunJSON {
	return deletionRunJSON{
		ID: r.ID, State: r.State, CreatedAt: r.CreatedAt, CreatedBy: r.CreatedBy, Candidates: r.Candidates, Bytes: r.Bytes,
		ConfirmedAt: r.ConfirmedAt, ConfirmedBy: r.ConfirmedBy, FinishedAt: r.FinishedAt, Deleted: r.Deleted, Kept: r.Kept,
	}
}

func (s *Server) handleListDeletionRuns(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	runs, err := s.store.DeletionRuns(r.Context(), 50)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	out := make([]deletionRunJSON, 0, len(runs))
	for i := range runs {
		out = append(out, toRunJSON(&runs[i]))
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"runs": out})
}

func (s *Server) handleCreateDeletionRun(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	by := userID(r)
	run, err := s.store.ProposeDeletion(withAudit(r, s.instanceEntry(r, "retention.preview")), &by)
	if errors.Is(err, store.ErrRunOpen) {
		s.fail(w, r, http.StatusConflict, err.Error(), nil)
		return
	}
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	s.log.Info("deletion run proposed", "run", run.ID, "candidates", run.Candidates, "by", currentSession(r).UserName)
	s.writeJSON(w, http.StatusCreated, toRunJSON(run))
}

// maxReviewCandidates is how many candidates the review lists; the counts
// cover all of them.
const maxReviewCandidates = 1000

type deletionGroupJSON struct {
	Owner   string `json:"owner"`
	Account string `json:"account"`
	Folder  string `json:"folder"`
	// Group is the customer group of the account's owner (#470); null
	// without an owner.
	Group    *groupRefJSON `json:"group"`
	Year     int           `json:"year"`
	Messages int64         `json:"messages"`
}

type deletionCandidateJSON struct {
	SHA256      string    `json:"sha256"`
	Size        int64     `json:"size"`
	RetainUntil time.Time `json:"retainUntil"`
	State       string    `json:"state"`
}

func (s *Server) pathRunID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "not found", nil)
		return 0, false
	}
	return id, true
}

func (s *Server) handleGetDeletionRun(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id, ok := s.pathRunID(w, r)
	if !ok {
		return
	}
	run, err := s.store.DeletionRun(r.Context(), id)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	groups, err := s.store.DeletionRunSummary(r.Context(), id)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	candidates, err := s.store.DeletionCandidates(r.Context(), id)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	out := struct {
		Run        deletionRunJSON         `json:"run"`
		Groups     []deletionGroupJSON     `json:"groups"`
		Candidates []deletionCandidateJSON `json:"candidates"`
		Listed     int                     `json:"listed"`
	}{Run: toRunJSON(run), Groups: make([]deletionGroupJSON, 0, len(groups)), Candidates: []deletionCandidateJSON{}}
	for _, g := range groups {
		out.Groups = append(out.Groups, deletionGroupJSON{
			Owner: g.Owner, Account: g.Account, Folder: g.Folder, Group: groupRef(g.GroupID, g.CustomerGroup), Year: g.Year, Messages: g.Messages,
		})
	}
	for i, c := range candidates {
		if i == maxReviewCandidates {
			break
		}
		out.Candidates = append(out.Candidates, deletionCandidateJSON{SHA256: c.SHA256, Size: c.Size, RetainUntil: c.RetainUntil, State: c.State})
	}
	out.Listed = len(out.Candidates)
	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleConfirmDeletionRun(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id, ok := s.pathRunID(w, r)
	if !ok {
		return
	}
	var in struct {
		// Count must be the run's number of candidates, typed by the admin.
		Count int `json:"count"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	run, err := s.store.DeletionRun(r.Context(), id)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	if in.Count != run.Candidates {
		s.fail(w, r, http.StatusBadRequest, fmt.Sprintf("type the number of messages (%d) to confirm", run.Candidates), nil)
		return
	}
	by := userID(r)
	err = s.store.ConfirmDeletionRun(withAudit(r, s.instanceEntry(r, "retention.confirm")), id, &by)
	if errors.Is(err, store.ErrRunState) {
		s.fail(w, r, http.StatusConflict, err.Error(), nil)
		return
	}
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	s.log.Info("deletion run confirmed", "run", id, "by", currentSession(r).UserName)
	if s.retention != nil {
		s.retention.Wake()
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleCancelDeletionRun(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id, ok := s.pathRunID(w, r)
	if !ok {
		return
	}
	err := s.store.CancelDeletionRun(withAudit(r, s.instanceEntry(r, "retention.cancel")), id)
	if errors.Is(err, store.ErrRunState) {
		s.fail(w, r, http.StatusConflict, err.Error(), nil)
		return
	}
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	s.log.Info("deletion run cancelled", "run", id, "by", currentSession(r).UserName)
	w.WriteHeader(http.StatusNoContent)
}

// holdEntry starts the entry of a change to hold id: targeted at the
// account, the user or the group the hold names, as hold.add was, or
// instance-level for a hold that names none of them (#470). An unknown
// hold answers 404.
func (s *Server) holdEntry(w http.ResponseWriter, r *http.Request, action string, id int64) (*store.AuditEntry, bool) {
	scope, err := s.store.LegalHoldScope(r.Context(), id)
	if err != nil {
		s.failStore(w, r, err)
		return nil, false
	}
	switch {
	case scope.AccountID != nil:
		e := s.targetedEntry(r, action)
		acc, err := s.store.GetAccount(r.Context(), *scope.AccountID)
		if err != nil {
			s.failStore(w, r, err)
			return nil, false
		}
		auditAccount(e, acc)
		return e, true
	case scope.OwnerID != nil:
		e := s.targetedEntry(r, action)
		u, err := s.store.GetUserByID(r.Context(), *scope.OwnerID)
		if err != nil {
			s.failStore(w, r, err)
			return nil, false
		}
		auditUser(e, u)
		return e, true
	case scope.GroupID != nil:
		// The store records it under the group.
		return s.targetedEntry(r, action), true
	}
	return s.instanceEntry(r, action), true
}
