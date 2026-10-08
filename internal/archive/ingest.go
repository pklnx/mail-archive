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
			return meta, nil // metadata is already stored; ON CONFLICT ignores this row
		}
	}
	h, idx, err := extract(blobs, blob.Path)
	if err != nil {
		return meta, err
	}
	meta.MessageID, meta.Subject, meta.From, meta.SentAt = h.MessageID, h.Subject, h.From, h.Date
	meta.IndexData = indexData(h, idx)
	return meta, nil
}

// extract parses the headers and the body of a stored message.
func extract(blobs *blobstore.Store, path string) (Headers, mime.Indexed, error) {
	f, err := blobs.Open(path)
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
	added     int // messages new to the archive, over all batches
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

// flush commits the queued messages, if any.
func (w *folderWriter) flush(ctx context.Context) error {
	if len(w.metas) == 0 {
		return nil
	}
	n, err := w.st.SaveBatch(ctx, w.folderID, w.maxUID, w.metas, w.locs)
	if err != nil {
		return err
	}
	w.added += n
	batch := len(w.metas)
	w.metas, w.locs = w.metas[:0], w.locs[:0]
	if w.flushed != nil {
		w.flushed(batch)
	}
	return nil
}
