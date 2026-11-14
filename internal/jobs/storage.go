package jobs

import (
	"context"
	"log/slog"

	"github.com/keepia/archive/internal/store"
)

// StorageKinds are the job kinds of storage usage (#397, part 1):
// store.JobStorageFold and store.JobUsageRefresh. The process that runs a
// pool also enqueues them on a schedule (cmd/mail-archive, storageTicker).
func StorageKinds(st *store.Store, log *slog.Logger) []Kind {
	if log == nil {
		log = slog.Default()
	}
	return []Kind{
		{Name: store.JobStorageFold, MaxAttempts: store.StorageJobMaxAttempts, Handler: func(ctx context.Context, _ Job) error {
			_, err := st.FoldStorage(ctx)
			return err
		}},
		{Name: store.JobUsageRefresh, MaxAttempts: store.StorageJobMaxAttempts, Handler: func(ctx context.Context, _ Job) error {
			return refreshUsage(ctx, st, log)
		}},
	}
}

// usageRefresher takes a usage snapshot (store.Store.RefreshUsage).
type usageRefresher interface {
	RefreshUsage(ctx context.Context) (store.RefreshResult, error)
}

// refreshUsage is the handler of store.JobUsageRefresh. A serialization
// failure (an account or group changed or deleted during the snapshot)
// and a request that came in during it both run it again at once, without
// counting an attempt. A difference between the counters and the exact
// count is logged; it means a bug or a manual change of the database.
func refreshUsage(ctx context.Context, st usageRefresher, log *slog.Logger) error {
	res, err := st.RefreshUsage(ctx)
	if store.IsSerializationFailure(err) {
		log.Info("usage snapshot: an account or group changed meanwhile; taking it again")
		return Continue(nil)
	}
	if err != nil {
		return err
	}
	if res.CountedMessages != res.ExactMessages || res.CountedBytes != res.ExactBytes {
		log.Error("storage counters differ from an exact count",
			"counted_messages", res.CountedMessages, "exact_messages", res.ExactMessages,
			"counted_bytes", res.CountedBytes, "exact_bytes", res.ExactBytes)
	}
	if res.Again {
		return Continue(nil)
	}
	return nil
}
