package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/store"
)

// The audit log records who searched, read, exported and changed what.
// Reads, searches and manual syncs are written before any data is sent or
// work is queued, and refused with 503 if the entry cannot be written (fail
// closed). Changes and logins write their entry in the same transaction
// (store.WithAudit). Failed logins and logouts are written on a best-effort
// basis: they are refused or done anyway.

const errAuditUnavailable = "the audit log cannot be written; nothing was done"

// auditEntry starts an entry for r: the logged-in user, the API key if the
// request used one, and the client.
func (s *Server) auditEntry(r *http.Request, action string) *store.AuditEntry {
	e := &store.AuditEntry{Actor: store.ActorSession, Action: action, ClientIP: s.clientIP(r), UserAgent: r.UserAgent()}
	if sess := currentSession(r); sess != nil {
		e.SetUser(sess.UserID, sess.UserName)
	}
	if k := currentAPIKey(r); k != nil {
		e.Actor, e.APIKeyID, e.APIKeyName = store.ActorAPIKey, &k.ID, k.Name
	}
	return e
}

// withAudit returns r's context with e to be written by the next store
// transaction.
func withAudit(r *http.Request, e *store.AuditEntry) context.Context {
	return store.WithAudit(r.Context(), e)
}

// audit writes e on its own. If it cannot, it answers 503 and returns false:
// the caller must then do nothing.
func (s *Server) audit(w http.ResponseWriter, r *http.Request, e *store.AuditEntry) bool {
	if err := s.store.Audit(r.Context(), *e); err != nil {
		s.failAudit(w, r, err)
		return false
	}
	return true
}

func (s *Server) failAudit(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("audit log not written; request refused", "method", r.Method, "path", r.URL.Path, "err", err)
	s.writeJSON(w, http.StatusServiceUnavailable, apiError{Error: errAuditUnavailable})
}

// auditBestEffort writes e and only logs a failure.
func (s *Server) auditBestEffort(r *http.Request, e *store.AuditEntry) {
	if err := s.store.Audit(r.Context(), *e); err != nil {
		s.log.Error("audit log entry lost", "action", e.Action, "err", err)
	}
}

// Reasons of a failed login.
const (
	reasonWrongPassword  = "wrong_password"
	reasonUnknownUser    = "unknown_user"
	reasonLocked         = "locked"
	reasonSecondFactor   = "second_factor"
	reasonUnknownPasskey = "unknown_passkey"
	reasonPasskeyInvalid = "passkey_invalid"
)

// auditLoginFailed records a refused login. u is nil when the user is not
// known.
func (s *Server) auditLoginFailed(r *http.Request, method, reason string, u *store.User) {
	e := s.auditEntry(r, "auth.login_failed")
	e.Detail("method", method)
	e.Detail("reason", reason)
	if u != nil {
		e.SetUser(u.ID, u.Name)
	}
	s.auditBestEffort(r, e)
}

func auditUser(e *store.AuditEntry, u *store.User) {
	e.SetTarget(store.TargetUser, strconv.FormatInt(u.ID, 10), u.Name)
}

func auditAccount(e *store.AuditEntry, a *store.Account) {
	e.SetTarget(store.TargetAccount, strconv.FormatInt(a.ID, 10), a.Name)
}

// searchFields are the list parameters recorded with a search.
var searchFields = []string{"q", "account", "folder", "from", "to", "attachment", "after", "before", "has", "gone", "group"}

// searchEntry returns the entry of a message list, or nil for a list that
// is a page view: plain folder lists, further pages (the first was logged)
// and expanded conversations (the search was logged).
func (s *Server) searchEntry(r *http.Request) *store.AuditEntry {
	q := r.URL.Query()
	if q.Get("cursor") != "" || q.Get("thread") != "" {
		return nil
	}
	if !slices.ContainsFunc([]string{"q", "from", "to", "attachment", "after", "before", "has"},
		func(k string) bool { return q.Get(k) != "" }) {
		return nil
	}
	e := s.auditEntry(r, "message.search")
	for _, k := range searchFields {
		if v := q.Get(k); v != "" {
			e.Detail(k, v)
		}
	}
	return e
}

// clientIP is the client's address: the peer, or behind a trusted proxy
// the last address in X-Forwarded-For that is not a trusted proxy. A
// missing or malformed header means the peer.
func (s *Server) clientIP(r *http.Request) netip.Addr {
	peer := peerAddr(r)
	if !s.trustedProxy(peer) {
		return peer
	}
	var hops []string
	for _, h := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(h, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return peer
		}
		if a = a.Unmap().WithZone(""); !s.trustedProxy(a) {
			return a
		}
	}
	return peer
}

func peerAddr(r *http.Request) netip.Addr {
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		return ap.Addr().Unmap().WithZone("")
	}
	if a, err := netip.ParseAddr(r.RemoteAddr); err == nil {
		return a.Unmap().WithZone("")
	}
	return netip.Addr{}
}

func (s *Server) trustedProxy(a netip.Addr) bool {
	return a.IsValid() && slices.ContainsFunc(s.trustedProxies, func(p netip.Prefix) bool { return p.Contains(a) })
}

// clientAddr is the key the login limits, the invalid-key limit and the
// passkey-begin cap count per client: clientIP, behind a trusted proxy per
// client again. An IPv6 address counts per /64 (limitKey). Logs and the
// audit log keep the full address (clientIP).
func (s *Server) clientAddr(r *http.Request) string {
	if a := s.clientIP(r); a.IsValid() {
		return limitKey(a)
	}
	return r.RemoteAddr
}

// limitKey groups an address for the limits: IPv6 by its /64, the block
// one host or network usually gets, so that changing addresses within it
// does not escape the limits; IPv4 (also IPv4-mapped IPv6) by address.
func limitKey(a netip.Addr) string {
	a = a.Unmap().WithZone("")
	if a.Is6() {
		return netip.PrefixFrom(a, 64).Masked().String()
	}
	return a.String()
}

// Paging of GET /api/audit.
const (
	auditPageDefault = 100
	auditPageMax     = 500
	auditDefaultDays = 30
)

type auditTargetJSON struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// Subject of a message, only if the viewer has it too.
	Subject string `json:"subject,omitempty"`
}

type auditEntryJSON struct {
	ID        int64            `json:"id"`
	At        time.Time        `json:"at"`
	Actor     string           `json:"actor"`
	User      string           `json:"user,omitempty"`
	APIKey    string           `json:"apiKey,omitempty"` // the name of the API key
	Action    string           `json:"action"`
	Target    *auditTargetJSON `json:"target"`
	ClientIP  string           `json:"clientIp,omitempty"`
	UserAgent string           `json:"userAgent,omitempty"`
	Details   json.RawMessage  `json:"details"`
}

type auditPageJSON struct {
	Entries    []auditEntryJSON `json:"entries"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

// handleAudit lists audit entries, newest first: a user's own, or for
// admins everyone's. Reading the log writes no entry.
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sess := currentSession(r)
	now := s.currentTime()
	p := store.AuditPage{Viewer: sess.UserID, All: sess.IsAdmin, Actions: q["action"], Since: now.AddDate(0, 0, -auditDefaultDays), Until: now.Add(time.Minute)}
	var err error
	if v := q.Get("since"); v != "" {
		if p.Since, err = parseAuditBound(v, false); err != nil {
			s.fail(w, r, http.StatusBadRequest, "since: "+err.Error(), nil)
			return
		}
	}
	if v := q.Get("until"); v != "" {
		if p.Until, err = parseAuditBound(v, true); err != nil {
			s.fail(w, r, http.StatusBadRequest, "until: "+err.Error(), nil)
			return
		}
	}
	if !p.Since.Before(p.Until) {
		s.fail(w, r, http.StatusBadRequest, "since must be before until", nil)
		return
	}
	if _, err := store.AuditActionPatterns(p.Actions); err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	}
	userName, actor := q.Get("user"), q.Get("actor")
	if !sess.IsAdmin && (userName != "" || actor != "") {
		s.fail(w, r, http.StatusForbidden, "only admins can see other users' entries", nil)
		return
	}
	switch actor {
	case "", store.ActorSession, store.ActorAPIKey, store.ActorCLI:
		p.Actor = actor
	default:
		s.fail(w, r, http.StatusBadRequest, "actor must be session, api_key or cli", nil)
		return
	}
	if userName != "" {
		u, err := s.store.GetUserByName(r.Context(), auth.NormalizeUserName(userName))
		if err != nil {
			s.failStore(w, r, err)
			return
		}
		p.UserID = &u.ID
	}
	if c := q.Get("cursor"); c != "" {
		at, id, err := decodeAuditCursor(c)
		if err != nil {
			s.fail(w, r, http.StatusBadRequest, "invalid cursor", nil)
			return
		}
		p.CursorAt, p.CursorID = &at, id
	}
	limit, err := parseLimit(q.Get("limit"), auditPageDefault, auditPageMax)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	}
	p.Limit = limit + 1 // one more tells whether there is a next page
	recs, err := s.store.ListAudit(r.Context(), p)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	out := auditPageJSON{Entries: make([]auditEntryJSON, 0, min(len(recs), limit))}
	if len(recs) > limit {
		last := recs[limit-1]
		out.NextCursor = encodeAuditCursor(last.At, last.ID)
		recs = recs[:limit]
	}
	for _, rec := range recs {
		e := auditEntryJSON{
			ID: rec.ID, At: rec.At.UTC(), Actor: rec.Actor, User: rec.UserName, APIKey: rec.APIKeyName, Action: rec.Action,
			ClientIP: rec.ClientIP, UserAgent: rec.UserAgent, Details: rec.Details,
		}
		if rec.TargetType != "" {
			e.Target = &auditTargetJSON{Type: rec.TargetType, ID: rec.TargetID, Name: rec.TargetName, Subject: rec.Subject}
		}
		out.Entries = append(out.Entries, e)
	}
	s.writeJSON(w, http.StatusOK, out)
}

// parseAuditBound reads YYYY-MM-DD (midnight UTC) or RFC 3339. A plain date
// as the end includes that day.
func parseAuditBound(v string, end bool) (time.Time, error) {
	if t, err := time.Parse(time.DateOnly, v); err == nil {
		if end {
			t = t.AddDate(0, 0, 1)
		}
		return t, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("want YYYY-MM-DD or RFC 3339, got %q", v)
	}
	return t, nil
}

func encodeAuditCursor(at time.Time, id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(at.UTC().Format(time.RFC3339Nano) + "|" + strconv.FormatInt(id, 10)))
}

func decodeAuditCursor(c string) (time.Time, int64, error) {
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return time.Time{}, 0, err
	}
	ts, idText, ok := strings.Cut(string(b), "|")
	if !ok {
		return time.Time{}, 0, errors.New("malformed cursor")
	}
	at, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Time{}, 0, err
	}
	id, err := strconv.ParseInt(idText, 10, 64)
	return at, id, err
}
