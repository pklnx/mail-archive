package web

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"
)

// While serve starts (#396), it already listens: it takes the migration
// lock, may wait for a migrate, applies migrations and checks the secret
// key before the app can answer. Meanwhile the starting handler answers
// every request with 503, so that monitoring tells "starting" (or
// "migrating") from "down". It runs behind the same protections as the
// app (protect: the Host check, the security headers, the Fetch Metadata
// guard), with the same allowed hosts.

// startingRetryAfter is the Retry-After of the starting handler, in
// seconds.
const startingRetryAfter = 5

// Starting returns the handler serve answers with while it starts:
// GET /healthz gives 503 {"status":"starting"}, every other request 503
// with an error and Retry-After. opts are the options the app will get;
// only the host names (AllowedHosts, PublicURL) and TrustedProxies count.
// It uses no database.
func Starting(log *slog.Logger, opts Options) http.Handler {
	s := New(nil, nil, log, Options{AllowedHosts: opts.AllowedHosts, PublicURL: opts.PublicURL, TrustedProxies: opts.TrustedProxies})
	return s.protect(http.HandlerFunc(s.handleStarting))
}

func (s *Server) handleStarting(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Retry-After", strconv.Itoa(startingRetryAfter))
	if r.URL.Path == "/healthz" {
		s.writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "starting"})
		return
	}
	s.writeJSON(w, http.StatusServiceUnavailable, apiError{Error: "the server is starting; try again shortly"})
}

// Swap is the handler of serve's one HTTP server for its whole run: the
// starting handler first, then the app (Set). Requests read the current
// handler once, so a request that began before Set completes with the
// handler it began with; no listener or connection is closed.
type Swap struct {
	h atomic.Pointer[http.Handler]
}

// NewSwap returns a Swap that serves h.
func NewSwap(h http.Handler) *Swap {
	sw := &Swap{}
	sw.Set(h)
	return sw
}

// Set makes h serve every request from now on.
func (sw *Swap) Set(h http.Handler) { sw.h.Store(&h) }

func (sw *Swap) ServeHTTP(w http.ResponseWriter, r *http.Request) { (*sw.h.Load()).ServeHTTP(w, r) }

// Serve serves h on ln until ctx is cancelled, then shuts down gracefully:
// open requests get 10 seconds. It returns nil after a shutdown, else the
// error that stopped it. It closes ln.
func Serve(ctx context.Context, ln net.Listener, h http.Handler) error {
	srv := newHTTPServer(ln.Addr().String(), h)
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		err := srv.Shutdown(shutdownCtx)
		if serveErr := <-errc; err == nil && !errors.Is(serveErr, http.ErrServerClosed) {
			err = serveErr
		}
		return err
	}
}

// Start makes s ready to serve: it checks the API description and starts
// the housekeeping, which runs until ctx ends. It returns the handler of
// the app (Handler).
func (s *Server) Start(ctx context.Context) (http.Handler, error) {
	if _, err := openAPI(); err != nil {
		return nil, err
	}
	go s.housekeeping(ctx)
	return s.Handler(), nil
}
