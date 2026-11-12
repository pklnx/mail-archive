package web

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/imapsync"
	"github.com/pklnx/mail-archive/internal/store"
)

// The limits of IMAP login checks (adding or changing an account, a fill's
// folders, starting, switching on or resuming a fill, an account's server
// folders). Each check connects to a host and port the request names and
// may take a minute, so they are limited per user, per client address and
// per IMAP login, the last counting only failed logins: the archive must
// not serve to guess a third party's IMAP password.
const (
	loginCheckWindow = 10 * time.Minute
	// loginCheckUserLimit and loginCheckAddrLimit count every check.
	loginCheckUserLimit = 20
	loginCheckAddrLimit = 60
	// loginCheckTargetLimit counts failed checks of one IMAP login (host,
	// port, user name) after LOGIN or AUTHENTICATE was sent.
	loginCheckTargetLimit = 10
	// loginChecksRunning is how many checks run at a time in all; each
	// user runs one at a time.
	loginChecksRunning = 2
	// loginCheckWait is how long a check waits for its turn at most; then
	// it is refused with this as Retry-After.
	loginCheckWait = 15 * time.Second
)

// The codes of login check answers: imapCodePrefix and the category of a
// failed check (imap_login, imap_timeout, …), errTooManyChecks for a
// refused one.
const (
	imapCodePrefix   = "imap_"
	errTooManyChecks = "too_many_checks"
)

// loginChecks holds the limits of IMAP login checks.
type loginChecks struct {
	users, addrs, targets *auth.Rate

	mu      sync.Mutex
	running int
	busy    map[int64]bool // users with a check running
	waiting int
	// changed is closed when a check ends.
	changed chan struct{}
	// waitContext bounds the wait for a turn (loginCheckWait); tests
	// replace it to end the wait without waiting for time.
	waitContext func(context.Context) (context.Context, context.CancelFunc)
}

// errCheckWait is the cause of a turn's wait that took loginCheckWait.
var errCheckWait = errors.New("waited too long for a login check's turn")

func newLoginChecks() *loginChecks {
	return &loginChecks{
		users:   auth.NewRate(loginCheckUserLimit, loginCheckWindow),
		addrs:   auth.NewRate(loginCheckAddrLimit, loginCheckWindow),
		targets: auth.NewRate(loginCheckTargetLimit, loginCheckWindow),
		busy:    map[int64]bool{},
		waitContext: func(parent context.Context) (context.Context, context.CancelFunc) {
			return context.WithTimeoutCause(parent, loginCheckWait, errCheckWait)
		},
	}
}

// acquire waits for a turn of user's check: a free place among
// loginChecksRunning, and no other check of the user running. A free turn
// is taken without looking at ctx; otherwise it waits until ctx ends.
func (c *loginChecks) acquire(ctx context.Context, user int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for c.running >= loginChecksRunning || c.busy[user] {
		if c.changed == nil {
			c.changed = make(chan struct{})
		}
		changed := c.changed
		c.waiting++
		c.mu.Unlock()
		var err error
		select {
		case <-changed:
		case <-ctx.Done():
			err = ctx.Err()
		}
		c.mu.Lock()
		c.waiting--
		if err != nil {
			return err
		}
	}
	c.running++
	c.busy[user] = true
	return nil
}

// release ends user's turn.
func (c *loginChecks) release(user int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.running--
	delete(c.busy, user)
	if c.changed != nil {
		close(c.changed)
		c.changed = nil
	}
}

// Waiting returns how many checks wait for their turn. Tests use it to wait
// for that state.
func (c *loginChecks) Waiting() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.waiting
}

// targetKey is the key of an IMAP login in the target limit: host, port
// and user name, so that each mailbox has one counter whatever the user
// of the archive. Host and user name are compared without case, an IP
// address in its canonical form.
func targetKey(a *store.Account) string {
	host := strings.TrimSuffix(strings.ToLower(strings.Trim(a.Host, "[]")), ".")
	if ip, err := netip.ParseAddr(host); err == nil {
		host = ip.Unmap().String()
	}
	return "t" + host + ":" + strconv.Itoa(a.Port) + "/" + strings.ToLower(a.Username)
}

// loginCheck runs check, a login check against a's server, within the
// limits. stored is set for a check with the account's saved password,
// which no request can choose: it is not counted against the IMAP login.
// failStatus is the status of a failed check.
//
// It returns true if the check succeeded. Otherwise it has answered the
// request: 429 with Retry-After if a limit refused the check, which then
// never connects; failStatus with a fixed text and the code
// imap_<category> if the check failed. The server's own text is logged,
// never sent.
func (s *Server) loginCheck(w http.ResponseWriter, r *http.Request, a *store.Account, stored bool, failStatus int,
	check func(ctx context.Context) error) bool {
	c := s.checks
	user := userID(r)
	if ok, wait := c.users.Allow("u" + strconv.FormatInt(user, 10)); !ok {
		s.refuseCheck(w, r, wait, "user")
		return false
	}
	if ok, wait := c.addrs.Allow(s.clientAddr(r)); !ok {
		s.refuseCheck(w, r, wait, "address")
		return false
	}
	// The wait for a turn, also for attempts of the same IMAP login in
	// flight, is bounded; the check itself runs with the request's context.
	wctx, cancel := c.waitContext(r.Context())
	defer cancel()
	var attempt *auth.RateAttempt
	if !stored {
		var wait time.Duration
		var err error
		attempt, wait, err = c.targets.Begin(wctx, targetKey(a))
		switch {
		case err != nil:
			s.refuseWait(w, r, err)
			return false
		case attempt == nil:
			s.refuseCheck(w, r, wait, "imap login")
			return false
		}
	}
	if err := c.acquire(wctx, user); err != nil {
		attempt.Release()
		s.refuseWait(w, r, err)
		return false
	}
	// The turn ends before the answer, so the client's next check finds it
	// free.
	err := func() error {
		defer c.release(user)
		return check(r.Context())
	}()
	if err == nil {
		attempt.Release()
		return true
	}
	// A failure once LOGIN or AUTHENTICATE was sent counts against the
	// IMAP login, also a server that drops the connection instead of
	// answering NO; one before it (connect, TLS, greeting) does not.
	var ie *imapsync.Error
	if errors.As(err, &ie) && ie.LoginSent {
		attempt.Fail()
	} else {
		attempt.Release()
	}
	cat := imapsync.CategoryOf(err)
	if cat == "" {
		// Not from the server or the network (the saved password cannot be
		// opened): nothing the user can fix.
		s.fail(w, r, http.StatusInternalServerError, "internal error", err)
		return false
	}
	s.log.Info("imap login check failed", "host", a.Host, "port", a.Port, "category", string(cat), "err", err)
	s.writeJSON(w, failStatus, apiError{Error: archive.DescribeError(err), Code: imapCodePrefix + string(cat)})
	return false
}

// refuseWait answers a check whose wait for its turn ended: 429 after
// loginCheckWait; nothing if the client is gone.
func (s *Server) refuseWait(w http.ResponseWriter, r *http.Request, err error) {
	if r.Context().Err() != nil {
		return
	}
	s.log.Warn("login check refused: no turn", "addr", s.clientIP(r), "err", err)
	s.refuseCheck(w, r, loginCheckWait, "")
}

// refuseCheck answers 429 with Retry-After for a login check a limit
// refused.
func (s *Server) refuseCheck(w http.ResponseWriter, r *http.Request, wait time.Duration, limit string) {
	if limit != "" {
		s.log.Warn("login check refused", "limit", limit, "user", userID(r), "addr", s.clientIP(r))
	}
	secs := max(1, int(math.Ceil(wait.Seconds())))
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	s.writeJSON(w, http.StatusTooManyRequests, retryJSON{Error: "too many server checks; try again later", Code: errTooManyChecks, RetryAfter: secs})
}
