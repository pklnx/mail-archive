package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/keepia/archive/internal/store/db"
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

// AttachmentTextNeeded returns the ids of the message's locations that do
// not show its attachment text yet (attachment_text_ok false). The job
// reads them at its start: an extraction that starts afterwards may set
// them (SaveAttachmentTexts).
func (s *Store) AttachmentTextNeeded(ctx context.Context, sha256 string) ([]int64, error) {
	return s.q.AttachmentTextNeeded(ctx, sha256)
}

// SaveAttachmentTexts stores the result of an extraction that started
// after the locations needed existed, in one transaction: those locations
// show the attachment text from now on (the ones deleted meanwhile are
// left out), and, unless the message is done at the current version
// already, it is marked done with its texts, which replace those of an
// older version. It returns whether the texts were stored: false without
// an error when another writer finished the message first (the texts are
// dropped, the flags are set all the same) or the message is gone
// (nothing is written).
func (s *Store) SaveAttachmentTexts(ctx context.Context, sha256 string, texts []AttachmentText, needed []int64) (bool, error) {
	stored := false
	err := s.inTx(ctx, func(q *db.Queries) error {
		stored = false
		// The locations first, as a deletion locks them before the
		// message and its state row.
		if err := setAttachmentTextOK(ctx, q, needed); err != nil {
			return err
		}
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

func setAttachmentTextOK(ctx context.Context, q *db.Queries, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	if _, err := q.SetAttachmentTextOK(ctx, ids); err != nil {
		return fmt.Errorf("set attachment text flags: %w", err)
	}
	return nil
}

// ErrAttachmentTextUnreadable is returned by AttachmentTextUnreadable for
// a message whose stored attachment text could be searched but whose file
// cannot be read: no new extraction can start, so the locations needed
// stay as they are.
var ErrAttachmentTextUnreadable = errors.New("message file unreadable")

// errAttachmentTextRace: another writer marked the message done during
// AttachmentTextUnreadable; nothing was written.
var errAttachmentTextRace = errors.New("attachment text: the message was done meanwhile; try again")

// AttachmentTextUnreadable records, in one transaction, that a message's
// file could not be read for the extraction that should cover the
// locations needed. A message that is not done is marked done without
// text, as before, and a done message without searchable text needs none:
// in both cases nothing can be searched, so those locations show the
// (empty) text from now on. A done message with searchable text returns
// ErrAttachmentTextUnreadable and changes nothing.
func (s *Store) AttachmentTextUnreadable(ctx context.Context, sha256 string, needed []int64) error {
	err := s.inTx(ctx, func(q *db.Queries) error {
		v, err := q.AttachmentIndexVersion(ctx, sha256)
		if err != nil {
			return err
		}
		if v >= AttachmentIndexVersion {
			searchable, err := q.AttachmentTextSearchable(ctx, sha256)
			if err != nil {
				return err
			}
			if searchable {
				return ErrAttachmentTextUnreadable
			}
		}
		if err := setAttachmentTextOK(ctx, q, needed); err != nil {
			return err
		}
		if v >= AttachmentIndexVersion {
			return nil
		}
		n, err := q.MarkAttachmentsIndexed(ctx, db.MarkAttachmentsIndexedParams{Sha256: sha256, Version: AttachmentIndexVersion})
		if err != nil {
			return err
		}
		if n == 0 {
			// Another writer finished it since the version was read: undo,
			// and the job's retry decides again.
			return errAttachmentTextRace
		}
		// Texts of an older version cannot be searched any more.
		return q.DeleteAttachmentTexts(ctx, sha256)
	})
	if state, ok := SQLState(err); ok && state == "23503" {
		return nil // the message was deleted meanwhile
	}
	return err
}

// Batches of the repair.
const (
	repairNoAttachmentBatch = 10000
	repairQueueBatch        = 1000
)

// RepairResult tells what one pass of RepairAttachmentText did.
type RepairResult struct {
	// Cleared counts the locations of messages without attachments that
	// now show their (empty) attachment text.
	Cleared int64
	// Queued counts the messages whose attachment_text job was queued.
	Queued int
}

// RepairAttachmentText checks the invariant of the attachment text flags
// against the data (#539, part 2): every location with
// attachment_text_ok false belongs to a message whose attachment_text job
// is queued, running or failed, or that is not done at the current version
// (the backfill's). It runs at the start of every process that runs the
// attachment jobs and every 15 minutes there. It
//
//   - sets the flag on the locations of messages without attachments, as
//     an insert does, since there is nothing to show; and
//   - queues the job at PriorityBackfill for every message that breaks the
//     invariant: a location or a job end written by a process before
//     1.6.0, or a done job that was removed. Failed jobs stay failed.
//
// Both walk only message_locations_attachment_pending_idx, which in
// steady state holds the few locations whose job is pending or failed.
func (s *Store) RepairAttachmentText(ctx context.Context) (RepairResult, error) {
	var res RepairResult
	for {
		n, err := s.q.RepairAttachmentTextNoAttachment(ctx, repairNoAttachmentBatch)
		res.Cleared += n
		if err != nil {
			return res, fmt.Errorf("attachment text repair: %w", err)
		}
		if n < repairNoAttachmentBatch {
			break
		}
	}
	after := ""
	for {
		var shas []string
		err := s.inTx(ctx, func(q *db.Queries) error {
			var err error
			shas, err = q.ListAttachmentTextRepair(ctx, db.ListAttachmentTextRepairParams{
				After: after, Version: AttachmentIndexVersion, RowLimit: repairQueueBatch,
			})
			if err != nil || len(shas) == 0 {
				return err
			}
			jobs := make([]NewJob, len(shas))
			for i, sha := range shas {
				jobs[i] = attachmentTextJob(sha, PriorityBackfill)
			}
			_, err = enqueueJobs(ctx, q, jobs, false)
			return err
		})
		if err != nil {
			return res, fmt.Errorf("attachment text repair: %w", err)
		}
		res.Queued += len(shas)
		if len(shas) < repairQueueBatch {
			return res, nil
		}
		after = shas[len(shas)-1]
	}
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
		ScopeAllFolderAccounts: p.ScopeAllFolderAccounts, ScopeFolderAccounts: p.ScopeFolderAccounts, ScopeFolders: p.ScopeFolders, ScopeAfter: p.After, ScopeBefore: p.Before,
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
