package archive

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/imapsync"
)

// Limits of background runs against IMAP servers. A run is a sync
// of one account (Syncer) or a run of one fill (Filler).

// DefaultMaxRunTime is the time limit of a background run: the web server
// sets it, the command line runs without one.
const DefaultMaxRunTime = 60 * time.Minute

// MaxRedials is how many times a run dials again after a broken
// connection (a cap, the idle deadline, a read error) before it gives up
// on the remaining folders.
const MaxRedials = 5

// loginCheckMinRead is what a login check may read at least; it scales
// with the folder limit (loginCheckMaxRead).
const loginCheckMinRead = 16 << 20

// ErrRunTimeLimit is the cause of a run's context when the run reached its
// time limit.
var ErrRunTimeLimit = errors.New("the run reached its time limit")

// IMAPSettings are the IMAP limits an operator may raise (config.IMAPLimits).
// Zero values mean imapsync's defaults.
type IMAPSettings struct {
	// MaxFolders caps the folders one LIST may answer.
	MaxFolders int
	// IdleTimeout is how long a server may send nothing during a command.
	IdleTimeout time.Duration
}

// config is the imapsync configuration of a connection with these
// settings.
func (s IMAPSettings) config(host string, port int, tlsMode, user, password string) imapsync.Config {
	return imapsync.Config{
		Host: host, Port: port, TLSMode: tlsMode, Username: user, Password: password,
		MaxFolders: s.MaxFolders, IdleTimeout: s.IdleTimeout,
	}
}

// loginCheckMaxRead is what one login check may read: at least 16 MiB,
// and about 1.1 KiB per folder the limit allows, so that an honest folder
// list under a raised limit still fits.
func loginCheckMaxRead(maxFolders int) int64 {
	if maxFolders <= 0 {
		maxFolders = imapsync.DefaultMaxFolders
	}
	return max(loginCheckMinRead, int64(maxFolders)*11*1024/10)
}

// runContext returns the context of one run: limited to maxRunTime with
// ErrRunTimeLimit as its cause, unlimited if maxRunTime is 0. seam, if
// set, replaces the limit (tests end a run at its limit with it).
func runContext(parent context.Context, maxRunTime time.Duration,
	seam func(context.Context) (context.Context, context.CancelFunc)) (context.Context, context.CancelFunc) {
	switch {
	case seam != nil:
		return seam(parent)
	case maxRunTime > 0:
		return context.WithTimeoutCause(parent, maxRunTime, ErrRunTimeLimit)
	}
	return context.WithCancel(parent)
}

// atTimeLimit reports whether run ended at its time limit while parent,
// whose end is a shutdown, goes on.
func atTimeLimit(parent, run context.Context) bool {
	return parent.Err() == nil && errors.Is(context.Cause(run), ErrRunTimeLimit)
}

// timeLimitError is the error a run records when it reached its time limit.
func timeLimitError(maxRunTime time.Duration) error {
	if maxRunTime <= 0 {
		return ErrRunTimeLimit
	}
	return fmt.Errorf("%w of %s; it continues in the next run", ErrRunTimeLimit, maxRunTime)
}

// SettingError is an error from a limit the operator may raise; its text
// names the setting.
type SettingError struct {
	Setting string
	Err     error
}

func (e *SettingError) Error() string { return e.Err.Error() + "; raise " + e.Setting }

func (e *SettingError) Unwrap() error { return e.Err }

// nameSetting wraps err in a SettingError if it comes from a limit an
// operator may raise: the folder limit, the idle timeout, or listing, the
// setting of the caller's listing limit ("" if the caller passed none).
// Fixed caps and the slack over a folder's message count name nothing.
func nameSetting(err error, listing string) error {
	if err == nil {
		return nil
	}
	var named *SettingError
	if errors.As(err, &named) {
		return err
	}
	var lim *imapsync.LimitError
	if errors.As(err, &lim) && lim.Configured {
		switch {
		case lim.What == imapsync.LimitFolders:
			return &SettingError{Setting: config.EnvIMAPMaxFolders, Err: err}
		case lim.What == imapsync.LimitFetchResponses && listing != "":
			return &SettingError{Setting: listing, Err: fmt.Errorf("the folder has more than %d messages: %w", lim.Limit, err)}
		}
	}
	var timeout *imapsync.TimeoutError
	if errors.As(err, &timeout) && timeout.Idle > 0 {
		return &SettingError{Setting: config.EnvIMAPIdleTimeout, Err: err}
	}
	return err
}

// StuckError reports a folder whose sync or fill stops before a message
// over imapsync.MaxMessageSize: later messages of that folder wait until
// the message is gone from the server. The run is partial, but counts as
// a failure in the account's or fill's health, so that the alert fires.
type StuckError struct {
	// UID is the large message's UID, 0 if the server did not say.
	UID uint32
	Err error
}

func (e *StuckError) Error() string {
	uid := ""
	if e.UID != 0 {
		uid = fmt.Sprintf(" (UID %d)", e.UID)
	}
	return fmt.Sprintf("a message%s is larger than %d MiB; later messages in this folder are not archived until it is removed from the server",
		uid, imapsync.MaxMessageSize>>20)
}

func (e *StuckError) Unwrap() error { return e.Err }

// oversized returns the size-cap error in err, if any.
func oversized(err error) (*imapsync.LimitError, bool) {
	var lim *imapsync.LimitError
	if errors.As(err, &lim) && lim.What == imapsync.LimitMessageSize {
		return lim, true
	}
	return nil, false
}

// redialer dials a run's connection again after it broke, at most
// MaxRedials times per run.
type redialer struct {
	n int
}

// errTooManyRedials ends a run whose connection broke once more after
// MaxRedials fresh dials.
var errTooManyRedials = fmt.Errorf("the connection broke again after %d new connections; the remaining folders are left for the next run", MaxRedials)

// again closes the broken conn and dials a new one. It fails with
// errTooManyRedials once the run has used its redials.
func (r *redialer) again(old *imapsync.Conn, dial func() (*imapsync.Conn, error)) (*imapsync.Conn, error) {
	if r.n >= MaxRedials {
		return nil, errTooManyRedials
	}
	r.n++
	_ = old.Close()
	conn, err := dial()
	if err != nil {
		return nil, fmt.Errorf("connect again: %w", err)
	}
	return conn, nil
}
