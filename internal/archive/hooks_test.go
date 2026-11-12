package archive

import "context"

// Test hooks for archive_test.

func SetBeforeMove(o *VerifyOptions, f func()) { o.beforeMove = f }

func SetAfterStart(o *ExportOptions, f func()) { o.afterStart = f }

// SetAttachmentHooks sets the backfill's batch size (0: the default) and
// hooks after a backfill batch and after a message's texts were stored.
func SetAttachmentHooks(x *AttachmentIndexer, batch int, onBatch func(n int, last bool), afterSave func(sha string)) {
	x.batch, x.onBatch, x.afterSave = batch, onBatch, afterSave
}

// SetSyncLimitRun replaces the time limit of s's runs with fn.
func SetSyncLimitRun(s *Syncer, fn func(context.Context) (context.Context, context.CancelFunc)) {
	s.limitRun = fn
}

// SetFillLimitRun replaces the time limit of f's runs with fn.
func SetFillLimitRun(f *Filler, fn func(context.Context) (context.Context, context.CancelFunc)) {
	f.limitRun = fn
}

// SetAfterListFlags sets a hook called with the length and capacity of a
// reconcile's UID list.
func SetAfterListFlags(s *Syncer, fn func(n, capacity int)) { s.afterListFlags = fn }

// SetAfterList sets a hook called with the length and capacity of a fill's
// listing of a folder.
func SetAfterList(f *Filler, fn func(n, capacity int)) { f.afterList = fn }
