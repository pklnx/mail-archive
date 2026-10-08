package archive

import (
	"context"
	"errors"
	"log/slog"

	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/store"
)

// ErrReindexRunning is returned by Reindex while another reindex runs.
var ErrReindexRunning = errors.New("reindex already running")

// reindexBatch is the number of messages per transaction.
const reindexBatch = 200

// Reindex extracts the index data (body text, recipients, attachments) of
// messages stored by an older version, so search finds them. It walks the
// table once in batches and returns how many messages it updated. It is
// safe to interrupt and rerun, and to run while syncs store new messages.
func Reindex(ctx context.Context, st *store.Store, blobs *blobstore.Store, log *slog.Logger) (int, error) {
	unlock, ok, err := st.TryLockReindex(ctx)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, ErrReindexRunning
	}
	defer unlock()

	total, after := 0, ""
	for {
		pending, err := st.ListUnindexed(ctx, after, reindexBatch)
		if err != nil {
			return total, err
		}
		if len(pending) == 0 {
			return total, nil
		}
		batch := make([]store.Indexed, 0, len(pending))
		for _, m := range pending {
			if err := ctx.Err(); err != nil {
				return total, err
			}
			h, idx, err := extract(blobs, m.StoredPath)
			if err != nil {
				// Keep going: mark it as indexed with empty values so a
				// missing or unreadable file does not block the rest forever.
				log.Warn("cannot read message for indexing", "sha256", m.SHA256, "err", err)
			}
			batch = append(batch, store.Indexed{SHA256: m.SHA256, IndexData: indexData(h, idx)})
		}
		n, err := st.SetIndexData(ctx, batch)
		if err != nil {
			return total, err
		}
		total += n
		after = pending[len(pending)-1].SHA256
		log.Info("reindex progress", "indexed", total)
	}
}
