package archive

// Test hooks for archive_test.

func SetBeforeMove(o *VerifyOptions, f func()) { o.beforeMove = f }

func SetAfterStart(o *ExportOptions, f func()) { o.afterStart = f }

// SetAttachmentHooks sets the backfill's batch size (0: the default) and
// hooks after a backfill batch and after a message's texts were stored.
func SetAttachmentHooks(x *AttachmentIndexer, batch int, onBatch func(n int, last bool), afterSave func(sha string)) {
	x.batch, x.onBatch, x.afterSave = batch, onBatch, afterSave
}
