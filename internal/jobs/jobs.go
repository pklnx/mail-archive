// Package jobs runs background jobs from the queue in PostgreSQL (the
// jobs table, see internal/store/jobs.go): a registry of kinds and a
// bounded pool of workers that claims due jobs with a lease, keeps the
// lease alive, retries failed jobs with a backoff and gives up after a
// kind's attempts. Delivery is at least once, so handlers are idempotent.
// See docs/development/architecture.md, "Background jobs".
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"time"

	"github.com/pklnx/mail-archive/internal/store"
)

// Defaults of a kind.
const (
	DefaultMaxAttempts = 5
	DefaultRetryFirst  = 30 * time.Second
	DefaultRetryCap    = time.Hour
)

// MaxWorkers is the largest pool.
const MaxWorkers = 8

// Job is a claimed job, as its handler sees it.
type Job struct {
	ID            int64
	Kind          string
	Key           string
	MessageSHA256 string
	Priority      int
	// Payload is the cursor or identifiers the job was enqueued or
	// continued with ({} without).
	Payload json.RawMessage
	// Attempt counts the claims since the job was enqueued, from 1.
	Attempt     int
	MaxAttempts int
}

// Handler runs a job. Its context ends at shutdown and when the pool lost
// the job's lease. It returns nil when the job is done, Continue to be
// called again with a new payload, Permanent to fail at once, or another
// error to be retried (Code names it in the queue).
type Handler func(ctx context.Context, j Job) error

// Kind is a type of job.
type Kind struct {
	// Name matches ^[a-z][a-z0-9_]{0,39}$.
	Name    string
	Handler Handler
	// MaxAttempts claims before the job fails; DefaultMaxAttempts if 0.
	MaxAttempts int
	// RetryFirst is the delay after the first failed attempt, doubled per
	// attempt up to RetryCap, with jitter (half to all of it).
	RetryFirst time.Duration
	RetryCap   time.Duration
}

func (k Kind) backoff() store.JobBackoff {
	return store.JobBackoff{First: k.RetryFirst, Cap: k.RetryCap}
}

// NewJob returns a job of the kind to enqueue (store.EnqueueJobs).
func (k Kind) NewJob(key string, priority int) store.NewJob {
	return store.NewJob{Kind: k.Name, Key: key, Priority: priority, MaxAttempts: k.MaxAttempts}
}

// Registry holds the kinds a process runs.
type Registry struct {
	kinds map[string]Kind
	order []string
}

// Register adds a kind and fills in its defaults.
func (r *Registry) Register(k Kind) error {
	if !store.ValidJobKind(k.Name) {
		return fmt.Errorf("job kind %q: want ^[a-z][a-z0-9_]{0,39}$", k.Name)
	}
	if k.Handler == nil {
		return fmt.Errorf("job kind %s has no handler", k.Name)
	}
	if _, ok := r.kinds[k.Name]; ok {
		return fmt.Errorf("job kind %s is registered twice", k.Name)
	}
	if k.MaxAttempts == 0 {
		k.MaxAttempts = DefaultMaxAttempts
	}
	if k.MaxAttempts < 1 || k.MaxAttempts > 100 {
		return fmt.Errorf("job kind %s: max attempts must be 1 to 100", k.Name)
	}
	if k.RetryFirst <= 0 {
		k.RetryFirst = DefaultRetryFirst
	}
	if k.RetryCap <= 0 {
		k.RetryCap = DefaultRetryCap
	}
	k.RetryCap = max(k.RetryCap, k.RetryFirst)
	if r.kinds == nil {
		r.kinds = map[string]Kind{}
	}
	r.kinds[k.Name] = k
	r.order = append(r.order, k.Name)
	return nil
}

// Kind returns a registered kind.
func (r *Registry) Kind(name string) (Kind, bool) {
	if r == nil {
		return Kind{}, false
	}
	k, ok := r.kinds[name]
	return k, ok
}

// Names returns the registered kinds in the order of registration.
func (r *Registry) Names() []string {
	if r == nil {
		return nil
	}
	return append([]string(nil), r.order...)
}

// Error codes in jobs.last_error that the pool sets itself. A kind adds
// its own with Code.
const (
	CodeLeaseExpired   = "lease expired"
	CodePanic          = "panic"
	CodeTimeout        = "timeout"
	CodeBlobUnreadable = "blob unreadable"
	CodeInternal       = "internal"
)

var codePattern = regexp.MustCompile(`^[a-z][a-z0-9 _.-]{0,39}$`)

type codeError struct {
	code string
	err  error
}

func (e *codeError) Error() string { return e.err.Error() }
func (e *codeError) Unwrap() error { return e.err }

// Code names err in the queue with code (lower-case letters, digits,
// spaces, '_', '.' and '-', at most 40 characters), instead of the error
// text, which may contain message content. An invalid code becomes
// "internal".
func Code(code string, err error) error {
	if err == nil {
		err = errors.New(code)
	}
	return &codeError{code: code, err: err}
}

type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// Permanent marks err as not worth a retry: the job fails at once.
func Permanent(err error) error {
	if err == nil {
		err = errors.New("permanent failure")
	}
	return &permanentError{err: err}
}

type continueError struct{ payload json.RawMessage }

func (e *continueError) Error() string { return "continue" }

// Continue tells the pool that the handler did part of the work: the job
// is queued again at once with payload (a cursor, at most
// store.MaxJobPayload bytes) and its attempts start again.
func Continue(payload json.RawMessage) error {
	return &continueError{payload: payload}
}

type panicError struct {
	value any
	stack []byte
}

func (e *panicError) Error() string { return fmt.Sprintf("panic: %v\n%s", e.value, e.stack) }

func newLease() []byte {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return b
}

// ErrorCode returns the code stored for err in jobs.last_error: never the
// error text.
func ErrorCode(err error) string {
	var ce *codeError
	var pe *panicError
	var pathErr *fs.PathError
	switch {
	case errors.As(err, &ce):
		if codePattern.MatchString(ce.code) {
			return ce.code
		}
		return CodeInternal
	case errors.As(err, &pe):
		return CodePanic
	case errors.Is(err, context.DeadlineExceeded):
		return CodeTimeout
	}
	if state, ok := store.SQLState(err); ok {
		return "db " + state
	}
	if errors.Is(err, fs.ErrNotExist) || errors.As(err, &pathErr) {
		return CodeBlobUnreadable
	}
	return CodeInternal
}
