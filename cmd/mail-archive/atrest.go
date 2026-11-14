package main

import (
	"context"
	"time"

	"github.com/keepia/archive/internal/archive"
	"github.com/keepia/archive/internal/blobstore"
)

// writerStopped is the cause of the root context when a writer must stop
// (#392, part 2): its writers lock is gone, or encryption at rest changed
// under it. main exits non-zero with it, so that Compose restarts the
// process under the start-up rules.
type writerStopped struct{ err error }

func (e *writerStopped) Error() string { return e.err.Error() }
func (e *writerStopped) Unwrap() error { return e.err }

// stopProcess cancels the root context with a cause (set by main).
var stopProcess context.CancelCauseFunc = func(error) {}

// writersTick, for tests, drives the writers lock's check instead of its
// ticker.
var writersTick <-chan time.Time

// openArchiveBlobs opens the blob store for a command that writes or reads
// message files, after the start-up checks of encryption at rest
// (archive.OpenAtRest). The caller closes the result when it is done,
// which releases a writer's lock.
func openArchiveBlobs(ctx context.Context, a *app, command string, role archive.AtRestRole) (*blobstore.Store, *archive.AtRest, error) {
	blobs, err := openBlobs(a.cfg.DataDir)
	if err != nil {
		return nil, nil, err
	}
	ar, err := archive.OpenAtRest(ctx, a.store, blobs, archive.AtRestOptions{
		DataDir: a.cfg.DataDir, KEK: a.cfg.ArchiveKey, Role: role, Command: command,
		OnLost: func(err error) { stopProcess(&writerStopped{err: err}) },
		Tick:   writersTick, Logger: newLogger(a.cfg.LogLevel),
	})
	if err != nil {
		return nil, nil, err
	}
	return blobs, ar, nil
}
