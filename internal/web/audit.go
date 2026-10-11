package web

import (
	"context"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"

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

// clientAddr is clientIP as a string, for the login limiter: behind a
// trusted proxy it counts per client again.
func (s *Server) clientAddr(r *http.Request) string {
	if a := s.clientIP(r); a.IsValid() {
		return a.String()
	}
	return r.RemoteAddr
}
