package store

import (
	"context"
	"fmt"

	"github.com/pklnx/mail-archive/internal/store/db"
)

// Attachment text per location (#539, part 2). A location shows its
// message's attachment text in search only when its attachment_text_ok is
// true (migration 00042), which holds when
//
//   - an extraction started after the location existed: the job
//     attachment_text reads the message's locations whose flag is false at
//     its start and sets it on them in the transaction that stores the
//     texts (SaveAttachmentTexts), or
//   - when the location was inserted, its customer group (that of the
//     account's owner; 0 for accounts without an owner) already had a
//     location of the message with the flag true, or the message has no
//     attachment.
//
// Nothing else sets it, and a move of a user or account to another group
// carries it with the location. Every save that inserts a location with
// the flag false queues the message's job in the same transaction, at
// PriorityNew and with RequeueIfRunning: a run that started before the
// location existed runs once more. The repair (RepairAttachmentText)
// queues the job for every location the invariant misses, such as those a
// process before 1.6.0 writes with the column's default false.

// attachmentText decides the flag of the locations one save inserts. It
// is built inside the save's transaction after lockAccountGroup, so the
// group cannot change until the commit.
type attachmentText struct {
	// known holds, for the hashes whose message row existed before the
	// save, whether a new location may show the text at once.
	known map[string]bool
	// queued holds the hashes whose job this save queued.
	queued map[string]bool
}

// newAttachmentText probes the batch's hashes: one primary key lookup per
// hash for whether the message exists and has an attachment, and, for
// those that have one, one fenced probe of the group's own folders
// (GroupAttachmentTextOK). The probe never looks up a hash over the whole
// archive, so its cost does not depend on other groups. Messages new to
// the archive need no probe.
func newAttachmentText(ctx context.Context, q *db.Queries, g accountGroup, shas []string) (*attachmentText, error) {
	a := &attachmentText{known: map[string]bool{}, queued: map[string]bool{}}
	if len(shas) == 0 {
		return a, nil
	}
	rows, err := q.MessagesWithAttachment(ctx, shas)
	if err != nil {
		return nil, fmt.Errorf("messages with attachment: %w", err)
	}
	var probe []string
	for _, r := range rows {
		a.known[r.Sha256] = !r.HasAttachment
		if r.HasAttachment {
			probe = append(probe, r.Sha256)
		}
	}
	if len(probe) == 0 {
		return a, nil
	}
	ok, err := q.GroupAttachmentTextOK(ctx, db.GroupAttachmentTextOKParams{Shas: probe, GroupID: groupKey(g)})
	if err != nil {
		return nil, fmt.Errorf("group attachment text: %w", err)
	}
	for _, sha := range ok {
		a.known[sha] = true
	}
	return a, nil
}

// groupKey is the group of a save for GroupAttachmentTextOK: 0 for an
// account without an owner.
func groupKey(g accountGroup) int64 {
	if g.Group == nil {
		return 0
	}
	return *g.Group
}

// flag returns the flag of a new location of m, whose insert returned
// inserted. A message whose row existed before the save takes the probe's
// answer. One that this save inserted shows its text at once only without
// an attachment. One inserted meanwhile by another writer (inserted 0 and
// unknown) is decided conservatively: false.
func (a *attachmentText) flag(m *MessageMeta, inserted int64) bool {
	if ok, found := a.known[m.SHA256]; found {
		return ok
	}
	return inserted == 1 && !m.HasAttachment
}

// inserted queues, in the save's transaction, the job of a message whose
// location the save inserted with the flag false, once per message: at
// PriorityNew, and with RequeueIfRunning, so that a run that started
// before the location existed runs once more.
func (a *attachmentText) inserted(ctx context.Context, q *db.Queries, sha string, ok bool) error {
	if ok || a.queued[sha] {
		return nil
	}
	a.queued[sha] = true
	j := attachmentTextJob(sha, PriorityNew)
	j.RequeueIfRunning = true
	_, err := enqueueJobs(ctx, q, []NewJob{j}, false)
	return err
}

// queueCopies queues the job of the messages whose copies a save inserted
// with the flag false (ConvertLegacyFillBatch decides it in SQL).
func queueCopies(ctx context.Context, q *db.Queries, shas []string) error {
	if len(shas) == 0 {
		return nil
	}
	jobs := make([]NewJob, len(shas))
	for i, sha := range shas {
		jobs[i] = attachmentTextJob(sha, PriorityNew)
		jobs[i].RequeueIfRunning = true
	}
	_, err := enqueueJobs(ctx, q, jobs, false)
	return err
}
