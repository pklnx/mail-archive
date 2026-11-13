package archive

import (
	"context"
	"io"

	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/mime"
	"github.com/pklnx/mail-archive/internal/quota"
	"github.com/pklnx/mail-archive/internal/store"
)

// Sync and import store messages the same way: the raw bytes go into the
// blob store, the metadata comes from BuildMeta, and a folderWriter commits
// batches of messages with their locations.

// maxHeaderBytes bounds how much of a message is parsed for headers.
const maxHeaderBytes = 1 << 20

// BuildMeta returns the metadata of a stored blob. Headers and body text
// are extracted only for messages the database does not know yet; created
// tells that the blob was new on disk, so the database cannot know it.
func BuildMeta(ctx context.Context, st *store.Store, blobs *blobstore.Store, blob blobstore.Blob, created bool) (store.MessageMeta, error) {
	meta := store.MessageMeta{SHA256: blob.SHA256, Size: blob.Size, StoredPath: blob.Path}
	if !created {
		exists, err := st.MessageExists(ctx, blob.SHA256)
		if err != nil {
			return meta, err
		}
		if exists {
			meta.Known = true
			return meta, nil // metadata is already stored; ON CONFLICT ignores this row
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
	res, err := w.st.SaveBatch(ctx, w.folderID, w.maxUID, w.metas, w.locs)
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
