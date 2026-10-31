package store

import (
	"context"
	"fmt"
	"time"

	"github.com/pklnx/mail-archive/internal/store/db"
)

// AttachmentIndexVersion is the version of attachment text extraction. A
// message with attachments is done when its attachment_index_state has at
// least this version. Raising it extracts every message again: the
// backfill's key (AttachmentBackfillKey) changes with it, and its enqueues
// then retry failed jobs.
const AttachmentIndexVersion = 1

// Job kinds of attachment text (registered in internal/archive).
const (
	// JobAttachmentText extracts the text of one message's attachments;
	// its key and message are the message's SHA-256.
	JobAttachmentText = "attachment_text"
	// JobAttachmentBackfill enqueues JobAttachmentText for messages
	// archived before, a batch at a time, with a cursor in its payload.
	JobAttachmentBackfill = "attachment_backfill"
	// AttachmentTextMaxAttempts is how often a message's extraction is
	// claimed before it fails: a message whose extraction crashed the
	// process three times is skipped (the crash-loop guard).
	AttachmentTextMaxAttempts = 3
	// AttachmentBackfillMaxAttempts is the backfill's.
	AttachmentBackfillMaxAttempts = 5
	// AttachmentBackfillBatch is how many messages one backfill run
	// enqueues.
	AttachmentBackfillBatch = 1000
)

// PriorityBackfillCursor is the priority of the backfill job itself: after
// its children (PriorityBackfill), so that it runs again only when its
// batch has been claimed, and the queue holds about one batch.
const PriorityBackfillCursor = PriorityBackfill + 1

// AttachmentBackfillKey is the key of the backfill job of the current
// version.
var AttachmentBackfillKey = fmt.Sprintf("v%d", AttachmentIndexVersion)

// attachmentTextJob is the job of a message stored with attachments.
func attachmentTextJob(sha string, priority int) NewJob {
	return NewJob{
		Kind: JobAttachmentText, Key: sha, MessageSHA256: sha,
		Priority: priority, MaxAttempts: AttachmentTextMaxAttempts,
	}
}

// enqueueAttachmentText queues the extraction of a message that was just
// inserted (inserted is the row count of its insert) in the caller's
// transaction, if it has attachments: one more row in that transaction,
// with a new key, so it meets no conflict and waits on nothing.
func enqueueAttachmentText(ctx context.Context, q *db.Queries, m *MessageMeta, inserted int64) error {
	if inserted != 1 || !m.HasAttachment {
		return nil
	}
	_, err := enqueueJobs(ctx, q, []NewJob{attachmentTextJob(m.SHA256, PriorityNew)}, false)
	return err
}

// AttachmentBackfillJob is the backfill job that serve, worker and reindex
// enqueue at start: from the newest message (an empty payload), after its
// children. A backfill that is queued keeps its cursor.
func AttachmentBackfillJob() NewJob {
	return NewJob{
		Kind: JobAttachmentBackfill, Key: AttachmentBackfillKey, Priority: PriorityBackfillCursor,
		MaxAttempts: AttachmentBackfillMaxAttempts,
	}
}

// AttachmentIndexed reports whether a message's attachments are done at
// the current version.
func (s *Store) AttachmentIndexed(ctx context.Context, sha256 string) (bool, error) {
	v, err := s.q.AttachmentIndexVersion(ctx, sha256)
	return v >= AttachmentIndexVersion, err
}

// AttachmentText is the extracted text of one attachment.
type AttachmentText struct {
	Part     int
	Filename string
	// Status is one of the statuses of package attachtext.
	Status string
	// Text is set for the statuses ok and truncated only.
	Text string
}

// SaveAttachmentTexts marks a message done at the current version and
// stores its texts, replacing those of an older version, in one
// transaction. It returns false without an error when another writer
// finished the message first (the texts are dropped) or the message is
// gone.
func (s *Store) SaveAttachmentTexts(ctx context.Context, sha256 string, texts []AttachmentText) (bool, error) {
	stored := false
	err := s.inTx(ctx, func(q *db.Queries) error {
		n, err := q.MarkAttachmentsIndexed(ctx, db.MarkAttachmentsIndexedParams{Sha256: sha256, Version: AttachmentIndexVersion})
		if err != nil || n == 0 {
			return err
		}
		if err := q.DeleteAttachmentTexts(ctx, sha256); err != nil {
			return err
		}
		for _, t := range texts {
			var body *string
			if t.Status == "ok" || t.Status == "truncated" {
				body = &t.Text
			}
			if err := q.InsertAttachmentText(ctx, db.InsertAttachmentTextParams{
				Sha256: sha256, PartIndex: int32(t.Part), Filename: t.Filename, Status: t.Status, BodyText: body, //nolint:gosec // part indexes stay below 1000
			}); err != nil {
				return fmt.Errorf("insert attachment text %d: %w", t.Part, err)
			}
		}
		stored = true
		return nil
	})
	if state, ok := SQLState(err); ok && state == "23503" {
		// foreign_key_violation: the message was deleted meanwhile.
		return false, nil
	}
	return stored, err
}

// AttachmentCursor is the position of the backfill: the last message it
// enqueued, by (sort_at, sha256), newest first. The zero value starts at
// the newest message.
type AttachmentCursor struct {
	SortAt time.Time `json:"sort_at"`
	SHA256 string    `json:"sha256"`
}

// EnqueueAttachmentBackfill enqueues JobAttachmentText with
// PriorityBackfill for up to limit pending messages after the cursor,
// newest first, in one transaction, and returns how many it walked and the
// new cursor. Messages that are done, or whose job is queued, running or
// failed, are skipped.
func (s *Store) EnqueueAttachmentBackfill(ctx context.Context, after AttachmentCursor, limit int) (int, AttachmentCursor, error) {
	p := db.ListAttachmentPendingParams{Version: AttachmentIndexVersion, RowLimit: clampInt32(limit)}
	if after.SHA256 != "" {
		p.AfterSortAt, p.AfterSha = &after.SortAt, &after.SHA256
	}
	var walked int
	next := after
	err := s.inTx(ctx, func(q *db.Queries) error {
		rows, err := q.ListAttachmentPending(ctx, p)
		if err != nil || len(rows) == 0 {
			return err
		}
		// In the walk's order, so that the jobs' IDs, which break the tie
		// of priority and due time in the claim, keep it: newest first.
		jobs := make([]NewJob, 0, len(rows))
		for _, r := range rows {
			jobs = append(jobs, attachmentTextJob(r.Sha256, PriorityBackfill))
		}
		if _, err := enqueueJobs(ctx, q, jobs, false); err != nil {
			return err
		}
		last := rows[len(rows)-1]
		walked, next = len(rows), AttachmentCursor{SortAt: derefTime(last.SortAt), SHA256: last.Sha256}
		return nil
	})
	if err != nil {
		return 0, after, err
	}
	return walked, next, nil
}

// CountAttachmentPending counts the messages with attachments whose text
// is not extracted yet, without those whose job failed.
func (s *Store) CountAttachmentPending(ctx context.Context) (int64, error) {
	return s.q.CountAttachmentPending(ctx, AttachmentIndexVersion)
}

// CountFailedJobsWithCode counts the failed jobs of kind whose last error
// is code.
func (s *Store) CountFailedJobsWithCode(ctx context.Context, kind, code string) (int64, error) {
	return s.q.CountFailedJobsWithCode(ctx, db.CountFailedJobsWithCodeParams{Kind: kind, Code: code})
}

// maxAttachmentMatches is how many messages a query may match by their
// attachments for the search to take them as a list; with more, it checks
// the attachments of each message it walks. A variable for tests.
var maxAttachmentMatches = 5000

// attachmentMatches fills the attachment arm of a query: the messages of
// the scope whose attachment text matches, or the walk when there are too
// many.
func (s *Store) attachmentMatches(ctx context.Context, p *db.SearchMessagesParams) error {
	if p.Query == nil {
		return nil
	}
	p.AttShas = []string{}
	if len(p.Accounts) == 0 {
		return nil
	}
	shas, err := s.q.AttachmentMatches(ctx, db.AttachmentMatchesParams{
		Query: *p.Query, Accounts: p.Accounts, RowLimit: int32(maxAttachmentMatches) + 1, //nolint:gosec // small
	})
	if err != nil {
		return fmt.Errorf("attachment matches: %w", err)
	}
	if len(shas) > maxAttachmentMatches {
		p.AttWalk, p.AttShas = true, nil
	} else {
		p.AttShas = append(p.AttShas, shas...)
	}
	return nil
}
