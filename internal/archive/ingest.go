package archive

import (
	"context"
	"io"

	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/mime"
	"github.com/pklnx/mail-archive/internal/store"
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
}

// PutFor writes r into the blob store for a save into the accounts of
// the lookups (one, or the recipients' accounts of an SMTP delivery). As
// soon as the hash is known it asks NewToGroup, for a new and an existing
// blob alike, so that the lookup costs the same on both paths. If the
// message is new to a group, the temporary file is synchronized as for a
// new blob (blobstore.SyncTempIf) and NewToGroup is set, so that BuildMeta
// parses it: whether another customer group already archived the same
// bytes changes nothing in the work of the save (#539, part 3).
func PutFor(ctx context.Context, blobs *blobstore.Store, r io.Reader, lookups ...*store.GroupLookup) (Stored, error) {
	var newToGroup bool
	blob, created, err := blobs.Put(r, blobstore.SyncTempIf(func(sha string) (bool, error) {
		n, err := NewToGroup(ctx, sha, lookups...)
		newToGroup = n
		return n, err
	}))
	return Stored{Blob: blob, Created: created, NewToGroup: newToGroup}, err
}

// NewToGroup reports whether the group of one of the lookups' accounts
// does not hold the message (store.GroupLookup.Holds). Every lookup is
// asked, whatever the answers before.
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

// flush commits the queued messages, if any.
func (w *folderWriter) flush(ctx context.Context) error {
	if len(w.metas) == 0 {
		return nil
	}
	res, err := w.st.SaveBatch(ctx, w.folderID, w.maxUID, w.metas, w.locs)
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
