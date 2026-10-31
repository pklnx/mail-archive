package jobs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestErrorCode(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{errors.New("Subject: secret"), CodeInternal},
		{fmt.Errorf("wrapped: %w", context.DeadlineExceeded), CodeTimeout},
		{fmt.Errorf("insert: %w", &pgconn.PgError{Code: "40001", Message: "secret"}), "db 40001"},
		{&fs.PathError{Op: "open", Path: "/data/x", Err: os.ErrPermission}, CodeBlobUnreadable},
		{fmt.Errorf("read: %w", fs.ErrNotExist), CodeBlobUnreadable},
		{Code("encrypted", errors.New("secret")), "encrypted"},
		{Code("Not A Code!", errors.New("secret")), CodeInternal},
		{Permanent(Code("too large", nil)), "too large"},
		{&panicError{value: "secret"}, CodePanic},
	} {
		if got := ErrorCode(c.err); got != c.want {
			t.Errorf("ErrorCode(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

func TestRegistry(t *testing.T) {
	var r Registry
	h := func(context.Context, Job) error { return nil }
	if err := r.Register(Kind{Name: "a_1", Handler: h}); err != nil {
		t.Fatal(err)
	}
	k, ok := r.Kind("a_1")
	if !ok || k.MaxAttempts != DefaultMaxAttempts || k.RetryFirst != DefaultRetryFirst || k.RetryCap != DefaultRetryCap {
		t.Errorf("defaults: %+v", k)
	}
	if j := k.NewJob("key", 7); j.Kind != "a_1" || j.Key != "key" || j.Priority != 7 || j.MaxAttempts != DefaultMaxAttempts {
		t.Errorf("NewJob: %+v", j)
	}
	for _, bad := range []Kind{
		{Name: "a_1", Handler: h},
		{Name: "A", Handler: h},
		{Name: "b"},
		{Name: "c", Handler: h, MaxAttempts: 101},
	} {
		if err := r.Register(bad); err == nil {
			t.Errorf("registered %+v", bad)
		}
	}
	if names := r.Names(); len(names) != 1 || names[0] != "a_1" {
		t.Errorf("names %v", names)
	}
}

func TestReconnectDelay(t *testing.T) {
	for n, want := range map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 5: 16 * time.Second, 6: 30 * time.Second, 50: 30 * time.Second} {
		if got := reconnectDelay(n); got != want {
			t.Errorf("reconnectDelay(%d) = %v, want %v", n, got, want)
		}
	}
}
