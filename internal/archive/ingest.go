package archive

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/keepia/archive/internal/blobstore"
	"github.com/keepia/archive/internal/mime"
	"github.com/keepia/archive/internal/quota"
	"github.com/keepia/archive/internal/store"
)

// Sync and import store messages the same way: the raw bytes go into the
// blob store, the metadata comes from BuildMeta, and a folderWriter commits
// batches of messages with their locations.

// maxHeaderBytes bounds how much of a message is parsed for headers.
const maxHeaderBytes = 1 << 20

// Stored is a message written to the blob store for a save (PutFor).
type Stored struct {
	Blob blobstore.Blob
	// Created tells that the file was new on disk, so the database cannot
	// know the message.
	Created bool
	// NewToGroup tells that the customer group of a saving account holds
	// no location of the message, this account included.
	NewToGroup bool
	// Group is the group key the lookup used (store.MessageMeta.LookupGroup).
	Group *int64
}

// PutFor writes r into the blob store for a save into the lookup's
// account. As soon as the hash is known it asks the lookup
// (store.GroupLookup.Holds), for a new and an existing blob alike, so that
// the lookup costs the same on both paths. If the message is new to the
// account's group, the temporary file is synchronized as for a new blob
// (blobstore.SyncTempIf) and NewToGroup is set, so that BuildMeta parses
// it: whether another customer group already archived the same bytes
// changes nothing in the work of the save (#539, part 3). Group names the
// group the answer is for; the save refuses the message if the account is
// in another group by then (store.ErrGroupChanged, redoWork).
func PutFor(ctx context.Context, blobs *blobstore.Store, r io.Reader, lookup *store.GroupLookup) (Stored, error) {
	var (
		newToGroup bool
		group      *int64
	)
	blob, created, err := blobs.Put(r, blobstore.SyncTempIf(func(sha string) (bool, error) {
		held, err := lookup.Holds(ctx, sha)
		if err != nil {
			return false, err
		}
		if k, ok := lookup.Group(); ok {
			group = &k
		}
		newToGroup = !held
		return newToGroup, nil
	}))
	return Stored{Blob: blob, Created: created, NewToGroup: newToGroup, Group: group}, err
}

// maxRedos bounds how often a sync or fill redoes a batch that a save
// refused because the account moved to another group (store.ErrGroupChanged).
const maxRedos = 3

// redoWork does the lookup and the work of a queued message again, for the
// group the account is in now, after a save refused its batch
// (store.ErrGroupChanged): its stored file goes through PutFor and
// BuildMeta as the fetched bytes did, so the save gets the work of the new
// group, never of the old one.
func redoWork(ctx context.Context, st *store.Store, blobs *blobstore.Store, lookup *store.GroupLookup, sha string) (store.MessageMeta, error) {
	f, err := blobs.OpenMessage(sha)
	if err != nil {
		return store.MessageMeta{}, err
	}
	defer func() { _ = f.Close() }()
	stored, err := PutFor(ctx, blobs, f, lookup)
	if err != nil {
		return store.MessageMeta{}, err
	}
	if stored.Blob.SHA256 != sha {
		return store.MessageMeta{}, fmt.Errorf("stored file of %s reads as %s", sha, stored.Blob.SHA256)
	}
	meta, err := BuildMeta(ctx, st, blobs, stored.Blob, stored.Created, stored.NewToGroup)
	meta.LookupGroup = stored.Group
	return meta, err
}

// saveRedoing calls save and, while it refuses the batch because the
// account moved to another group, redoes the work of each message for the
// new group (redoWork) and saves again, at most maxRedos times. After that
// it returns the refusal: the run stops, and the next one fetches the
// batch again.
func saveRedoing(ctx context.Context, st *store.Store, blobs *blobstore.Store, lookup *store.GroupLookup, metas []store.MessageMeta, save func() error) error {
	err := save()
	for redo := 0; errors.Is(err, store.ErrGroupChanged) && lookup != nil; redo++ {
		if redo == maxRedos {
			return fmt.Errorf("the batch is stored by the next run: %w", err)
		}
		for i := range metas {
			m, rerr := redoWork(ctx, st, blobs, lookup, metas[i].SHA256)
			if rerr != nil {
				return rerr
			}
			metas[i] = m
		}
		err = save()
	}
	return err
}

// NewToGroup reports whether the group of one of the lookups' accounts
// does not hold the message (store.GroupLookup.Holds): the recipients'
// accounts of an SMTP delivery. Every lookup is asked, whatever the
// answers before; each lookup's Group names its account's group.
func NewToGroup(ctx context.Context, sha string, lookups ...*store.GroupLookup) (bool, error) {
	newToGroup := false
	for _, l := range lookups {
		held, err := l.Holds(ctx, sha)
		if err != nil {
			return false, err
		}
		if !held {
			newToGroup = true
		}
	}
	return newToGroup, nil
}

// BuildMeta returns the metadata of a stored blob. Headers and body text
// are extracted for messages the database does not know yet, and for
// known messages new to the saving account's customer group (newToGroup),
// which keep Known: their row is not written again, but the insert gets
// the parsed fields and so does the work of a new row (#539, part 3).
// created tells that the blob was new on disk, so the database cannot know
// it. Callers without a group lookup (./ma import, tools/demo, tests) pass
// newToGroup true: always the full work.
func BuildMeta(ctx context.Context, st *store.Store, blobs *blobstore.Store, blob blobstore.Blob, created, newToGroup bool) (store.MessageMeta, error) {
	meta := store.MessageMeta{SHA256: blob.SHA256, Size: blob.Size, StoredPath: blob.Path}
	if !created {
		exists, err := st.MessageExists(ctx, blob.SHA256)
		if err != nil {
			return meta, err
		}
		if exists {
			meta.Known = true // ON CONFLICT ignores this row
			if !newToGroup {
				return meta, nil // the group holds it: nothing to parse
			}
		}
	}
	h, idx, err := extract(blobs, blob.SHA256)
	if err != nil {
		return meta, err
	}
	meta.MessageID, meta.Subject, meta.From, meta.SentAt = h.MessageID, h.Subject, h.From, h.Date
	meta.IndexData = indexData(h, idx)
	return meta, nil
}

// extract parses the headers and the body of a stored message.
func extract(blobs *blobstore.Store, sha string) (Headers, mime.Indexed, error) {
	f, err := blobs.OpenMessage(sha)
	if err != nil {
		return Headers{}, mime.Indexed{}, err
	}
	defer func() { _ = f.Close() }()
	h := ParseHeaders(io.LimitReader(f, maxHeaderBytes))
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return Headers{}, mime.Indexed{}, err
	}
	return h, mime.Index(f), nil
}

func indexData(h Headers, idx mime.Indexed) store.IndexData {
	return store.IndexData{
		BodyText: idx.Text, To: h.To, Cc: h.Cc,
		AttachmentNames: idx.AttachmentNames, HasAttachment: idx.HasAttachment,
		InReplyTo: h.InReplyTo, ReferenceIDs: h.References, ThreadID: h.ThreadID,
	}
}

// folderWriter collects the messages of one folder and commits them in
// batches, together with the folder's last UID.
type folderWriter struct {
	st        *store.Store
	folderID  int64
	batchSize int
	metas     []store.MessageMeta
	locs      []store.Location
	maxUID    uint32
	added     int // messages new to the account, over all batches
	skipped   int // deleted by retention and not archived again
	// flushed is called after each committed batch.
	flushed func(batch int)
	// pending counts the files of the queued messages for the storage
	// gate (#397); every flush settles it. Nil: no gate.
	pending *quota.Pending
	// blobs and lookup, set by a sync, redo the batch's work when a save
	// refuses it because the account moved to another group
	// (saveRedoing). Nil lookup: no redo (import, no lookup).
	blobs  *blobstore.Store
	lookup *store.GroupLookup
}

func newFolderWriter(st *store.Store, folderID int64, batchSize int, flushed func(batch int)) *folderWriter {
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}
	return &folderWriter{st: st, folderID: folderID, batchSize: batchSize, flushed: flushed}
}

// add queues a message and commits the batch when it is full.
func (w *folderWriter) add(ctx context.Context, meta store.MessageMeta, loc store.Location) error {
	w.metas = append(w.metas, meta)
	w.locs = append(w.locs, loc)
	w.maxUID = max(w.maxUID, loc.UID)
	if len(w.metas) >= w.batchSize {
		return w.flush(ctx)
	}
	return nil
}

// below reports whether every queued message has a UID below uid.
func (w *folderWriter) below(uid uint32) bool {
	for _, l := range w.locs {
		if l.UID >= uid {
			return false
		}
	}
	return true
}

// admit asks the storage gate whether the group may store another
// message. When it may not while files of this batch are pending, these
// may be duplicates or what crossed the limit: the batch is committed
// first (the store then tells the gate what it really added) and the gate
// is asked again, so that known mail never pauses a sync or import, and
// new mail stops within one message of the limit.
func (w *folderWriter) admit(ctx context.Context, gate *quota.Gate, group *int64) error {
	err := gate.Admit(group)
	if err == nil || w.pending.Bytes() == 0 {
		return err
	}
	if ferr := w.flush(ctx); ferr != nil {
		return ferr
	}
	return gate.Admit(group)
}

// flush commits the queued messages, if any.
func (w *folderWriter) flush(ctx context.Context) error {
	if len(w.metas) == 0 {
		return nil
	}
	var res store.SaveResult
	err := saveRedoing(ctx, w.st, w.blobs, w.lookup, w.metas, func() error {
		var err error
		res, err = w.st.SaveBatch(ctx, w.folderID, w.maxUID, w.metas, w.locs)
		return err
	})
	// Committed (the store told the gate what it added) or failed: either
	// way these files are no longer pending.
	w.pending.Settle()
	if err != nil {
		return err
	}
	w.added += res.Added
	w.skipped += res.Skipped
	batch := len(w.metas)
	w.metas, w.locs = w.metas[:0], w.locs[:0]
	if w.flushed != nil {
		w.flushed(batch)
	}
	return nil
}
