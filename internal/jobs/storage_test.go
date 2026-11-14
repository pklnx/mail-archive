package jobs

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/keepia/archive/internal/store"
)

type fakeRefresher struct {
	res store.RefreshResult
	err error
}

func (f fakeRefresher) RefreshUsage(context.Context) (store.RefreshResult, error) {
	return f.res, f.err
}

// TestRefreshUsageHandler: a serialization failure and a request during
// the snapshot run it again at once (Continue: no attempt counted); other
// errors are retried as usual; a counter difference is logged at Error
// with numbers only.
func TestRefreshUsageHandler(t *testing.T) {
	isContinue := func(err error) bool {
		var c *continueError
		return errors.As(err, &c)
	}
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	ctx := context.Background()
	if err := refreshUsage(ctx, fakeRefresher{err: &pgconn.PgError{Code: "40001"}}, log); !isContinue(err) {
		t.Errorf("40001: %v", err)
	}
	if err := refreshUsage(ctx, fakeRefresher{res: store.RefreshResult{Again: true}}, log); !isContinue(err) {
		t.Errorf("again: %v", err)
	}
	other := &pgconn.PgError{Code: "57014"}
	if err := refreshUsage(ctx, fakeRefresher{err: other}, log); !errors.Is(err, other) || isContinue(err) {
		t.Errorf("timeout: %v", err)
	}
	if err := refreshUsage(ctx, fakeRefresher{}, log); err != nil {
		t.Errorf("done: %v", err)
	}
	if strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("logged an error without a difference: %s", logs.String())
	}
	drift := store.RefreshResult{CountedMessages: 3, ExactMessages: 2, CountedBytes: 30, ExactBytes: 20}
	if err := refreshUsage(ctx, fakeRefresher{res: drift}, log); err != nil {
		t.Errorf("drift: %v", err)
	}
	if !strings.Contains(logs.String(), `level=ERROR msg="storage counters differ from an exact count" counted_messages=3 exact_messages=2 counted_bytes=30 exact_bytes=20`) {
		t.Errorf("drift log: %s", logs.String())
	}
}
