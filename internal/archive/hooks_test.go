package archive

// Test hooks for archive_test.

func SetBeforeMove(o *VerifyOptions, f func()) { o.beforeMove = f }

func SetAfterStart(o *ExportOptions, f func()) { o.afterStart = f }
