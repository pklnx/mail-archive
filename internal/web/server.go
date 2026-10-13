// Package web serves the JSON API and the UI over HTTP.
package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/store"
)

// DefaultAllowedHosts are the host names accepted without configuration.
var DefaultAllowedHosts = []string{"localhost", "127.0.0.1", "::1"}

// Server holds the dependencies of the HTTP handlers.
type Server struct {
	store        *store.Store
	blobs        *blobstore.Store
	log          *slog.Logger
	allowedHosts []string
	syncer       *archive.Syncer
	runner       *archive.Runner
	hasher       *auth.Hasher
	sealer       *crypto.Sealer
	secretKey    []byte
	require2FA   bool
	now          func() time.Time
	limiter      *auth.Limiter
	// webauthn is nil without a public URL: no passkeys.
	webauthn      *webauthn.WebAuthn
	publicOrigin  string
	passkeyBegins *auth.Rate
	// apiKeyRate limits requests per key, badKeyRate invalid keys per
	// client address. Not the login limiter: behind a reverse proxy all
	// clients may share an address, and a broken script must not block
	// everyone's password logins.
	apiKeyRate *auth.Rate
	badKeyRate *auth.Rate
	// alertAfter failed syncs in a row make an account failing.
	alertAfter  int
	healthCache syncHealthCache
	// trustedProxies may name the client in X-Forwarded-For.
	trustedProxies []netip.Prefix
	// retention executes confirmed deletion runs; nil leaves them to the
	// next start of serve or to the command line.
	retention interface{ Wake() }
	// smtpListen is MAIL_ARCHIVE_SMTP_LISTEN; empty when the SMTP server
	// is off.
	smtpListen string
}

// Options configure a Server.
type Options struct {
	// AllowedHosts are host names (without port) accepted in the Host
	// header; empty means DefaultAllowedHosts.
	AllowedHosts []string
	// Syncer and Runner enable account management and sync. Without them
	// (no secret key configured) the archive can only be browsed.
	Syncer *archive.Syncer
	Runner *archive.Runner
	// Hasher verifies passwords; nil means auth.DefaultParams.
	Hasher *auth.Hasher
	// Sealer protects TOTP secrets; nil keeps the existing browse-only mode.
	Sealer *crypto.Sealer
	// SecretKey is used only to key recovery-code hashes.
	SecretKey []byte
	// Require2FA requires TOTP for non-admin users as well. Admins always require it.
	Require2FA bool
	// Now is injectable for authentication tests.
	Now func() time.Time
	// PublicURL is the address users open (MAIL_ARCHIVE_PUBLIC_URL). It
	// turns passkeys on; its host is accepted in addition to AllowedHosts.
	PublicURL string
	// AlertAfterFailures is how many failed syncs in a row make an account
	// failing (MAIL_ARCHIVE_ALERT_AFTER_FAILURES); zero means the default.
	AlertAfterFailures int
	// TrustedProxies are reverse proxies whose X-Forwarded-For names the
	// client (MAIL_ARCHIVE_TRUSTED_PROXIES), for the audit log and the
	// login limits.
	TrustedProxies []netip.Prefix
	// Retention executes deletion runs confirmed in the web UI.
	Retention *archive.Retention
	// SMTPListen is where the SMTP server receives mail for smtp accounts
	// (MAIL_ARCHIVE_SMTP_LISTEN); empty when it is off.
	SMTPListen string
}

// New creates a Server.
func New(st *store.Store, blobs *blobstore.Store, log *slog.Logger, opts Options) *Server {
	allowed := opts.AllowedHosts
	if len(allowed) == 0 {
		allowed = DefaultAllowedHosts
	}
	hosts := make([]string, 0, len(allowed))
	for _, h := range allowed {
		if h = strings.Trim(strings.ToLower(strings.TrimSpace(h)), "[]"); h != "" {
			hosts = append(hosts, h)
		}
	}
	hasher := opts.Hasher
	if hasher == nil {
		hasher = auth.NewHasher(auth.DefaultParams)
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	alertAfter := opts.AlertAfterFailures
	if alertAfter <= 0 {
		alertAfter = config.DefaultAlertAfterFailures
	}
	wa, origin, err := newWebAuthn(opts.PublicURL)
	if err != nil {
		// config.Load checked the URL already; this is a programming error.
		log.Error("passkeys are off", "err", err)
	}
	if wa != nil && !slices.Contains(hosts, wa.Config.RPID) {
		hosts = append(hosts, wa.Config.RPID)
	}
	srv := &Server{
		store: st, blobs: blobs, log: log, allowedHosts: hosts, syncer: opts.Syncer, runner: opts.Runner,
		hasher: hasher, sealer: opts.Sealer, secretKey: opts.SecretKey, require2FA: opts.Require2FA, now: now, limiter: auth.NewLimiter(),
		webauthn: wa, publicOrigin: origin, passkeyBegins: auth.NewRate(passkeyBeginLimit, passkeyBeginWindow),
		apiKeyRate: auth.NewRate(apiKeyLimit, apiKeyWindow), badKeyRate: auth.NewRate(badAPIKeyLimit, apiKeyWindow),
		alertAfter: alertAfter, trustedProxies: opts.TrustedProxies, smtpListen: opts.SMTPListen,
	}
	if opts.Retention != nil {
		srv.retention = opts.Retention
	}
	return srv
}

// currentTime returns the server clock, or the wall clock if none is set.
func (s *Server) currentTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// Handler returns the HTTP handler with all routes and protections.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	accessOf := make(map[string]access)
	for _, rt := range s.routes() {
		mux.Handle(rt.pattern, rt.handler)
		accessOf[rt.pattern] = rt.access
	}
	return s.protect(s.authenticate(mux, accessOf))
}

// ListenAndServe serves until ctx is cancelled, then shuts down gracefully.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	if _, err := openAPI(); err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      5 * time.Minute, // large .eml and attachment downloads
		IdleTimeout:       2 * time.Minute,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	go s.housekeeping(ctx)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.log.Debug("write response", "err", err)
	}
}

type apiError struct {
	Error string `json:"error"`
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, status int, msg string, err error) {
	if status >= 500 {
		s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	}
	s.writeJSON(w, status, apiError{Error: msg})
}

func (s *Server) failStore(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrAuditUnavailable) {
		s.failAudit(w, r, err)
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "not found", err)
		return
	}
	if errors.Is(err, store.ErrOnHold) || errors.Is(err, store.ErrConflict) {
		s.fail(w, r, http.StatusConflict, err.Error(), err)
		return
	}
	s.fail(w, r, http.StatusInternalServerError, "internal error", err)
}

var shaPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// pathSHA validates the {sha} path parameter. Only valid hashes reach the
// blob store, so user input never forms a file path.
func (s *Server) pathSHA(w http.ResponseWriter, r *http.Request) (string, bool) {
	sha := r.PathValue("sha")
	if !shaPattern.MatchString(sha) {
		s.fail(w, r, http.StatusBadRequest, "invalid message id", nil)
		return "", false
	}
	return sha, true
}

// Cursors encode the last row of a page as "<RFC 3339 nano>|<sha256>".
func encodeCursor(at time.Time, sha string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(at.UTC().Format(time.RFC3339Nano) + "|" + sha))
}

func decodeCursor(c string) (time.Time, string, error) {
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return time.Time{}, "", err
	}
	ts, sha, ok := strings.Cut(string(b), "|")
	if !ok || !shaPattern.MatchString(sha) {
		return time.Time{}, "", errors.New("malformed cursor")
	}
	at, err := time.Parse(time.RFC3339Nano, ts)
	return at, sha, err
}

// parseDate accepts YYYY-MM-DD (UTC midnight) or RFC 3339.
func parseDate(v string) (*time.Time, error) {
	if v == "" {
		return nil, nil
	}
	for _, layout := range []string{time.DateOnly, time.RFC3339} {
		if t, err := time.Parse(layout, v); err == nil {
			return &t, nil
		}
	}
	return nil, fmt.Errorf("invalid date %q (use YYYY-MM-DD)", v)
}

func parseLimit(v string, def, maxLimit int) (int, error) {
	if v == "" {
		return def, nil
	}
	// Bit size 32: the limit ends up as an int32 query parameter.
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil || n < 1 || n > int64(maxLimit) {
		return 0, fmt.Errorf("limit must be between 1 and %d", maxLimit)
	}
	return int(n), nil
}
