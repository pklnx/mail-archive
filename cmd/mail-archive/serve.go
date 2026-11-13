package main

import (
	"context"
	"log/slog"
	"net"
	"time"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/web"
)

// runServe is serve once its configuration is loaded and has a secret
// key. It listens at once and answers with the starting handler (#396,
// step 1: /healthz 503 "starting", every other request 503) while it
// waits for the migration lock, migrates and checks the schema and the
// secret key (prepareInstance). Then it opens the app and swaps the
// handler of the same HTTP server to the app's (step 8): one server and
// one listener for the whole run, so no request is cut off by the swap.
// It returns once ctx ends and the server has shut down gracefully. It
// closes ln.
func runServe(parent context.Context, cfg *config.Config, log *slog.Logger, ln net.Listener) error {
	front := web.NewSwap(web.Starting(log, webOptions(cfg)))
	// Everything of this run ends with ctx, also when the HTTP server
	// fails: the deferred stops below wait for goroutines that run until
	// it ends.
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- web.Serve(ctx, ln, front) }()
	var serveErr error
	serving := true
	waitServed := func() error {
		if serving {
			serveErr, serving = <-served, false
		}
		return serveErr
	}
	// A start that fails stops the server last: the starting handler uses
	// nothing that the other deferred steps close.
	defer func() {
		cancel()
		_ = waitServed()
	}()
	log.Info("listening; starting", "addr", ln.Addr().String())

	// The migration lock, start-up migrations, the schema check and the
	// secret key (#396), before anything of this process uses the
	// database.
	if err := prepareInstance(ctx, cfg, log, "serve"); err != nil {
		return err
	}
	a, err := openAppConfig(ctx, cfg, openNormal)
	if err != nil {
		return err
	}
	defer a.close()
	blobs, ar, err := openArchiveBlobs(ctx, a, "serve", archive.RoleWriter)
	if err != nil {
		return err
	}
	defer ar.Close()
	opts := webOptions(a.cfg)
	// After a restore, attestations only the file has are imported
	// before anything archives, so new messages are numbered past
	// them.
	archive.CatchUpAtStart(ctx, a.store, a.cfg.AttestDir(), log)
	// Background jobs, beside the notifier and the schedule; stopped
	// before the store closes, releasing running jobs.
	stopJobs, err := startJobs(ctx, a, blobs, log)
	if err != nil {
		return err
	}
	defer stopJobs()
	var afterSync func()
	if n := newNotifier(a, log); n != nil {
		ctx, cancel := context.WithCancel(ctx)
		notifierDone := make(chan struct{})
		go func() {
			n.Run(ctx)
			close(notifierDone)
		}()
		// Stop it also when serve fails, and before the store closes.
		defer func() {
			cancel()
			<-notifierDone
		}()
		log.Info("alerts on", "target", a.cfg.Webhook.Target(), "after_failures", a.cfg.AlertAfterFailures)
		afterSync = n.Wake
	} else {
		log.Info("alerts off", "reason", config.EnvNotifyWebhookURL+" is not set")
	}
	// serve has a secret key (checkSecretKey): account management and
	// sync are on.
	sealer, err := a.cfg.Sealer()
	if err != nil {
		return err
	}
	opts.Sealer = sealer
	if n, err := archive.UpgradePasswords(ctx, a.store, sealer); err != nil {
		return err
	} else if n > 0 {
		log.Info("stored passwords bound to the account ID", "accounts", n)
	}
	// The storage gate (#397): it reads the figures every 10 seconds, so
	// that transitions are persisted also while nothing is stored. Syncs,
	// fills and the SMTP server ask it before each message.
	warnStorageConfig(ctx, a, log)
	storage := newStorageGate(a, log)
	gateDone := make(chan struct{})
	go func() {
		storage.Run(ctx)
		close(gateDone)
	}()
	defer func() {
		cancel()
		<-gateDone
	}()
	// Background runs end at a time limit, so that a slow or
	// silent server cannot hold the only sync or fill worker.
	opts.Syncer = &archive.Syncer{
		Storage: storage,
		Store:   a.store, Blobs: blobs, Sealer: sealer, Logger: log,
		ReconcileInterval:    a.cfg.ReconcileInterval,
		MaxReconcileMessages: a.cfg.IMAP.ReconcileMaxMessages, IMAP: imapSettings(a.cfg),
		MaxRunTime: archive.DefaultMaxRunTime,
	}
	opts.Runner = &archive.Runner{Syncer: opts.Syncer, Schedule: scheduleOf(a.cfg), AfterSync: afterSync}
	runnerDone := make(chan struct{})
	go func() {
		opts.Runner.Run(ctx)
		close(runnerDone)
	}()
	// Let a running sync record its outcome before the store closes.
	// Every deferred wait for a goroutine cancels the context it waits
	// on first: deferred calls run last-in, first-out, so a start that
	// fails after this point runs the waits before the deferred cancel
	// above, and an uncancelled wait would hang serve (#550).
	defer func() {
		cancel()
		<-runnerDone
	}()
	// Recurring fills wait after the start, so that journal copies
	// the mail server queued during an outage arrive first.
	opts.Fills = &archive.FillRunner{
		Filler: &archive.Filler{
			Store: a.store, Blobs: blobs, Sealer: sealer, Logger: log, Storage: storage,
			IMAP: imapSettings(a.cfg), MaxFolderMessages: a.cfg.IMAP.FillMaxFolderMessages,
			MaxRunTime: archive.DefaultMaxRunTime,
		},
		Schedule: opts.Runner.Schedule, HoldUntil: time.Now().Add(archive.RecurringStartHold), AfterRun: afterSync,
	}
	fillsDone := make(chan struct{})
	go func() {
		opts.Fills.Run(ctx)
		close(fillsDone)
	}()
	// A fill records its last batch before the store closes.
	defer func() {
		cancel()
		<-fillsDone
	}()
	if a.cfg.SyncInterval > 0 {
		log.Info("sync schedule on", "interval", a.cfg.SyncInterval, "min", a.cfg.SyncIntervalMin, "reconcile_interval", a.cfg.ReconcileInterval)
	} else {
		log.Info("sync schedule off")
	}
	if pending, err := a.store.HasUnindexed(ctx); err == nil && pending {
		log.Warn("some messages are not fully searchable yet", "command", commandName()+" reindex")
	}
	if n, err := a.store.CountUsers(ctx); err != nil {
		return err
	} else if n == 0 && cfg.SetupToken == "" {
		log.Warn("no users yet; the web UI shows how to create the first admin", "command", commandName()+" user add NAME --admin")
	}
	// The first attestation of a large archive takes minutes; it runs
	// in the background. Stopped before the store closes.
	defer startAttester(ctx, a, log)()
	retention, stopRetention := startRetention(ctx, a, blobs, log)
	defer stopRetention()
	opts.Retention = retention
	if a.cfg.SMTP != nil {
		stop, err := startSMTP(ctx, a, blobs, storage, log)
		if err != nil {
			return err
		}
		defer stop()
		opts.SMTPListen = a.cfg.SMTP.Listen
		opts.SMTPAuth = a.cfg.SMTP.Auth
	}
	srv := web.New(a.store, blobs, log, opts)
	h, err := srv.Start(ctx)
	if err != nil {
		return err
	}
	front.Set(h)
	log.Info("ready", "addr", ln.Addr().String())
	err = waitServed()
	// The HTTP server is down: stop the rest before the deferred steps
	// wait for it.
	cancel()
	return err
}

// webOptions are the options of the web server that come from the
// configuration alone.
func webOptions(cfg *config.Config) web.Options {
	dataDir := cfg.DataDir
	return web.Options{
		AllowedHosts: cfg.AllowedHosts, Require2FA: cfg.Require2FA, SecretKey: cfg.SecretKey, PublicURL: cfg.PublicURL,
		AlertAfterFailures: cfg.AlertAfterFailures, TrustedProxies: cfg.TrustedProxies,
		Version:   version,
		DiskUsage: func() (uint64, uint64, error) { return archive.DiskUsage(dataDir) },
	}
}
