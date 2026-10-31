package archive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/pklnx/mail-archive/internal/attachtext"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/jobs"
	"github.com/pklnx/mail-archive/internal/store"
)

// CodeExtractorUnavailable is the job error of a message whose PDF could
// not be read because the PDF module did not compile in that process. The
// job fails at once; `jobs retry attachment_text` queues it again after a
// restart.
const CodeExtractorUnavailable = "extractor unavailable"

// CodePDFBusy is the job error of a message whose PDF waited for another
// one past its deadline; the job is retried.
const CodePDFBusy = "pdf busy"

// AttachmentExtractor extracts the text of a raw message's attachments
// (attachtext.Extractor; tests use a fake).
type AttachmentExtractor interface {
	Message(ctx context.Context, r io.Reader) ([]attachtext.Result, error)
}

// AttachmentIndexer runs the two job kinds of attachment text: one message
// at a time (store.JobAttachmentText), and the backfill that enqueues the
// messages archived before (store.JobAttachmentBackfill). Both are
// idempotent: a message that is done is skipped, and of two writers of the
// same message (a retried job, reindex beside the web server) only the
// first stores its texts. It never writes messages rows.
type AttachmentIndexer struct {
	Store     *store.Store
	Blobs     *blobstore.Store
	Extractor AttachmentExtractor
	Logger    *slog.Logger

	// extracted counts the attachments with text that this indexer
	// stored.
	extracted atomic.Int64

	// Test seams.
	batch     int                    // backfill batch size; store.AttachmentBackfillBatch if 0
	afterSave func(sha string)       // after a message's texts are stored
	onBatch   func(n int, last bool) // after a backfill batch was enqueued
}

// Extracted returns how many attachments with text this indexer stored.
func (x *AttachmentIndexer) Extracted() int64 { return x.extracted.Load() }

// Kinds returns the job kinds to register in a pool.
func (x *AttachmentIndexer) Kinds() []jobs.Kind {
	return []jobs.Kind{
		{Name: store.JobAttachmentText, Handler: x.extractMessage, MaxAttempts: store.AttachmentTextMaxAttempts},
		{Name: store.JobAttachmentBackfill, Handler: x.backfill, MaxAttempts: store.AttachmentBackfillMaxAttempts},
	}
}

// Register adds the kinds to r.
func (x *AttachmentIndexer) Register(r *jobs.Registry) error {
	for _, k := range x.Kinds() {
		if err := r.Register(k); err != nil {
			return err
		}
	}
	return nil
}

func (x *AttachmentIndexer) log() *slog.Logger {
	if x.Logger == nil {
		return slog.Default()
	}
	return x.Logger
}

// extractMessage is the handler of store.JobAttachmentText. Results of the
// extraction (a damaged PDF, a timeout, a password) are stored as statuses
// and the job is done; only a cancel (shutdown, a lost lease) and errors
// of the database are errors. A message whose extraction crashed the
// process is retried by the queue up to its attempts, and then fails.
func (x *AttachmentIndexer) extractMessage(ctx context.Context, j jobs.Job) error {
	sha := j.MessageSHA256
	if sha == "" {
		sha = j.Key
	}
	done, err := x.Store.AttachmentIndexed(ctx, sha)
	if err != nil || done {
		return err
	}
	m, err := x.Store.GetMessage(ctx, sha)
	if errors.Is(err, store.ErrNotFound) {
		return nil // deleted meanwhile
	}
	if err != nil {
		return err
	}
	start := time.Now()
	f, err := x.Blobs.Open(m.StoredPath)
	if err != nil {
		// Done without text, so that the backfill does not queue it again;
		// as reindex does for bodies.
		x.log().Warn("attachment text: cannot read message", "sha256", sha, "err", err)
		_, err := x.Store.SaveAttachmentTexts(ctx, sha, nil)
		return err
	}
	results, err := x.Extractor.Message(ctx, f)
	_ = f.Close()
	switch {
	case errors.Is(err, attachtext.ErrUnavailable):
		return jobs.Permanent(jobs.Code(CodeExtractorUnavailable, err))
	case errors.Is(err, attachtext.ErrBusy):
		// Nothing was read: try again later.
		return jobs.Code(CodePDFBusy, err)
	}
	if err != nil {
		return err
	}
	texts := make([]store.AttachmentText, 0, len(results))
	withText := 0
	for _, r := range results {
		texts = append(texts, store.AttachmentText{Part: r.Part, Filename: r.Filename, Status: string(r.Status), Text: r.Text})
		if r.Text != "" {
			withText++
		}
		// Never the file name or text: no message content in logs.
		x.log().Debug("attachment text", "sha256", sha, "part", r.Part, "status", r.Status)
	}
	stored, err := x.Store.SaveAttachmentTexts(ctx, sha, texts)
	if err != nil {
		return err
	}
	if stored {
		x.extracted.Add(int64(withText))
	}
	x.log().Debug("attachment text extracted", "sha256", sha, "attachments", len(results), "with_text", withText,
		"stored", stored, "took", time.Since(start).Round(time.Millisecond))
	if x.afterSave != nil {
		x.afterSave(sha)
	}
	return nil
}

// backfill is the handler of store.JobAttachmentBackfill: it enqueues the
// next batch of pending messages, newest first, and continues from the
// last of them, until the walk finds none.
func (x *AttachmentIndexer) backfill(ctx context.Context, j jobs.Job) error {
	var cur store.AttachmentCursor
	if len(j.Payload) > 0 {
		if err := json.Unmarshal(j.Payload, &cur); err != nil {
			// A cursor this version cannot read: start again from the
			// newest message; done ones are skipped.
			x.log().Warn("attachment backfill: unreadable cursor; starting from the newest message", "job", j.ID, "err", err)
			cur = store.AttachmentCursor{}
		}
	}
	limit := x.batch
	if limit <= 0 {
		limit = store.AttachmentBackfillBatch
	}
	n, next, err := x.Store.EnqueueAttachmentBackfill(ctx, cur, limit)
	if err != nil {
		return err
	}
	last := n < limit
	if x.onBatch != nil {
		x.onBatch(n, last)
	}
	if n > 0 {
		x.log().Info("attachment backfill: queued messages", "messages", n)
	}
	if last {
		return nil
	}
	payload, err := json.Marshal(next)
	if err != nil {
		return fmt.Errorf("attachment backfill cursor: %w", err)
	}
	return jobs.Continue(payload)
}
