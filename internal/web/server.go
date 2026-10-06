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
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/web/ui"
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
	return &Server{
		store: st, blobs: blobs, log: log, allowedHosts: hosts, syncer: opts.Syncer, runner: opts.Runner,
		hasher: hasher, sealer: opts.Sealer, secretKey: opts.SecretKey, require2FA: opts.Require2FA, now: opts.Now, limiter: auth.NewLimiter(),
	}
}

// Handler returns the HTTP handler with all routes and protections.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /api/session", s.handleGetSession)
	mux.HandleFunc("POST /api/session", s.handleLogin)
	mux.HandleFunc("POST /api/session/2fa", s.handleTwoFactorLogin)
	mux.HandleFunc("DELETE /api/session", s.handleLogout)
	mux.HandleFunc("PUT "+profilePasswordPath, s.handleChangeOwnPassword)
	mux.HandleFunc("GET /api/profile/2fa", s.handleGetTwoFactor)
	mux.HandleFunc("POST /api/profile/2fa/setup", s.handleBeginTwoFactor)
	mux.HandleFunc("POST /api/profile/2fa/confirm", s.handleConfirmTwoFactor)
	mux.HandleFunc("DELETE /api/profile/2fa", s.handleDisableTwoFactor)
	mux.HandleFunc("POST /api/profile/2fa/recovery-codes", s.handleRegenerateRecoveryCodes)
	mux.HandleFunc("GET /api/users", s.handleListUsers)
	mux.HandleFunc("POST /api/users", s.handleCreateUser)
	mux.HandleFunc("PATCH /api/users/{name}", s.handleUpdateUser)
	mux.HandleFunc("DELETE /api/users/{name}", s.handleDeleteUser)
	mux.HandleFunc("POST /api/users/{name}/password", s.handleResetUserPassword)
	mux.HandleFunc("POST /api/users/{name}/2fa/reset", s.handleResetUserTwoFactor)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/accounts", s.handleAccounts)
	mux.HandleFunc("POST /api/accounts", s.handleCreateAccount)
	mux.HandleFunc("PATCH /api/accounts/{name}", s.handleUpdateAccount)
	mux.HandleFunc("DELETE /api/accounts/{name}", s.handleDeleteAccount)
	mux.HandleFunc("GET /api/accounts/{name}/server-folders", s.handleServerFolders)
	mux.HandleFunc("POST /api/accounts/{name}/sync", s.handleSyncAccount)
	mux.HandleFunc("POST /api/sync", s.handleSyncAll)
	mux.HandleFunc("GET /api/messages", s.handleListMessages)
	mux.HandleFunc("GET /api/messages/{sha}", s.handleMessage)
	mux.HandleFunc("GET /api/messages/{sha}/html", s.handleMessageHTML)
	mux.HandleFunc("GET /api/messages/{sha}/raw", s.handleMessageRaw)
	mux.HandleFunc("GET /api/messages/{sha}/parts/{n}", s.handleMessagePart)
	app := ui.Handler()
	mux.Handle("GET /{$}", app)
	mux.Handle("GET /assets/", app)
	return s.protect(s.requireLogin(mux))
}

// ListenAndServe serves until ctx is cancelled, then shuts down gracefully.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
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
	go s.cleanSessions(ctx)
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
	if errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "not found", err)
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
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > maxLimit {
		return 0, fmt.Errorf("limit must be between 1 and %d", maxLimit)
	}
	return n, nil
}
