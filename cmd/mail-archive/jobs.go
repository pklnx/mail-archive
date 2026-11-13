package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/attachtext"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/jobs"
	"github.com/pklnx/mail-archive/internal/store"
)

// testJobKinds are registered in every pool; only tests set them.
var testJobKinds []jobs.Kind

// processExtractor is the attachment text extractor of the process: one
// for all pools, so that PDFs are read one at a time (attachtext reads one
// PDF at a time per process anyway) and the module is compiled once.
var (
	extractorOnce sync.Once
	extractor     *attachtext.Extractor
)

func processExtractor(log *slog.Logger) *attachtext.Extractor {
	extractorOnce.Do(func() { extractor = attachtext.New(log) })
	return extractor
}

// attachmentIndexer runs the attachment text kinds of the process.
func attachmentIndexer(a *app, blobs *blobstore.Store, log *slog.Logger) *archive.AttachmentIndexer {
	return &archive.AttachmentIndexer{Store: a.store, Blobs: blobs, Extractor: processExtractor(log), Logger: log}
}

// jobRegistry returns the job kinds that serve and worker run: the
// attachment text kinds and those of storage usage.
func jobRegistry(x *archive.AttachmentIndexer) (*jobs.Registry, error) {
	r := &jobs.Registry{}
	for _, k := range testJobKinds {
		if err := r.Register(k); err != nil {
			return nil, err
		}
	}
	if err := x.Register(r); err != nil {
		return nil, err
	}
	for _, k := range jobs.StorageKinds(x.Store, x.Logger) {
		if err := r.Register(k); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Intervals of the storage jobs (#397, part 1).
const (
	storageFoldEvery  = 5 * time.Minute
	usageRefreshEvery = time.Hour
)

// storageSchedule enqueues the storage jobs in every process that runs a
// job pool (serve with workers, worker): storage_fold at start and every
// storageFoldEvery, usage_refresh every usageRefreshEvery. No job queues
// itself again, so this is what keeps them going; several processes
// enqueue the same singletons, which merge. Both pass retryFailed, so a
// job that failed its attempts is queued again at the next tick.
type storageSchedule struct {
	store *store.Store
	log   *slog.Logger
	// fold and refresh deliver the ticks.
	fold, refresh <-chan time.Time
	// enqueued, if set, is called after each enqueue with the kind (tests).
	enqueued func(kind string)
}

// startStorageSchedule runs the schedule with real tickers until the
// returned stop is called.
func startStorageSchedule(ctx context.Context, st *store.Store, log *slog.Logger) (stop func()) {
	fold, refresh := time.NewTicker(storageFoldEvery), time.NewTicker(usageRefreshEvery)
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s := &storageSchedule{store: st, log: log, fold: fold.C, refresh: refresh.C}
	go func() {
		defer close(done)
		s.run(ctx)
	}()
	return func() {
		cancel()
		<-done
		fold.Stop()
		refresh.Stop()
	}
}

// run enqueues storage_fold at once, and usage_refresh too as long as no
// snapshot was taken (after the upgrade to 00041), and then both on their
// ticks, until ctx ends. A failed enqueue is logged and waits for the next
// tick.
func (s *storageSchedule) run(ctx context.Context) {
	s.enqueue(ctx, store.StorageFoldJob())
	if at, err := s.store.UsageSnapshotAt(ctx); err != nil && ctx.Err() == nil {
		s.log.Warn("cannot read the time of the last usage snapshot", "err", err)
	} else if err == nil && at == nil {
		s.enqueue(ctx, store.UsageRefreshJob())
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.fold:
			s.enqueue(ctx, store.StorageFoldJob())
		case <-s.refresh:
			s.enqueue(ctx, store.UsageRefreshJob())
		}
	}
}

func (s *storageSchedule) enqueue(ctx context.Context, j store.NewJob) {
	if _, err := s.store.EnqueueJobs(ctx, []store.NewJob{j}, true); err != nil && ctx.Err() == nil {
		s.log.Warn("cannot queue a storage job", "kind", j.Kind, "err", err)
	}
	if s.enqueued != nil {
		s.enqueued(j.Kind)
	}
}

// attachmentRepairEvery is how often a process that runs a job pool
// checks the attachment text flags (#539, part 2).
const attachmentRepairEvery = 15 * time.Minute

// attachmentRepair runs the repair of the attachment text flags
// (store.RepairAttachmentText) at start and every attachmentRepairEvery,
// in every process that runs a job pool (serve with workers, worker),
// next to the storage schedule. It catches locations and job ends that a
// process before 1.6.0 writes after the upgrade, and removed done jobs.
// Several processes may run it at once: it only sets flags of messages
// without attachments and enqueues jobs, which merge.
type attachmentRepair struct {
	store *store.Store
	log   *slog.Logger
	// tick delivers the ticks.
	tick <-chan time.Time
	// repaired, if set, is called after each pass (tests).
	repaired func(store.RepairResult, error)
}

// startAttachmentRepair runs the repair with a real ticker until the
// returned stop is called.
func startAttachmentRepair(ctx context.Context, st *store.Store, log *slog.Logger) (stop func()) {
	t := time.NewTicker(attachmentRepairEvery)
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	r := &attachmentRepair{store: st, log: log, tick: t.C}
	go func() {
		defer close(done)
		r.run(ctx)
	}()
	return func() {
		cancel()
		<-done
		t.Stop()
	}
}

// run repairs at once and then on every tick, until ctx ends. A failed
// pass is logged and waits for the next tick.
func (r *attachmentRepair) run(ctx context.Context) {
	r.pass(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.tick:
			r.pass(ctx)
		}
	}
}

func (r *attachmentRepair) pass(ctx context.Context) {
	res, err := r.store.RepairAttachmentText(ctx)
	switch {
	case err != nil && ctx.Err() == nil:
		r.log.Warn("cannot check the attachment text of locations", "err", err)
	case res.Queued > 0 || res.Cleared > 0:
		r.log.Info("attachment text: repaired locations", "messages_queued", res.Queued, "locations_without_attachment", res.Cleared)
	}
	if r.repaired != nil {
		r.repaired(res, err)
	}
}

// attachmentsAtStart enqueues the attachment backfill (idempotent; a
// queued one keeps its cursor), and logs how many messages wait for their
// attachment text. serve and worker call it at start; it never fails the
// start.
func attachmentsAtStart(ctx context.Context, st *store.Store, log *slog.Logger) {
	if _, err := st.EnqueueJobs(ctx, []store.NewJob{store.AttachmentBackfillJob()}, false); err != nil {
		log.Warn("cannot queue the attachment text of older messages", "err", err)
		return
	}
	if n, err := st.CountAttachmentPending(ctx); err == nil && n > 0 {
		log.Info("attachment text: messages pending, extracted in the background, newest first", "messages", n)
	}
}

// startJobs runs the job pool of serve in the background, unless
// MAIL_ARCHIVE_JOB_WORKERS is 0. The returned stop cancels it and waits
// until its jobs are released or recorded; call it before the store
// closes.
func startJobs(ctx context.Context, a *app, blobs *blobstore.Store, log *slog.Logger) (stop func(), err error) {
	attachmentsAtStart(ctx, a.store, log)
	if a.cfg.JobWorkers == 0 {
		log.Info("jobs off in this process", "reason", config.EnvJobWorkers+"=0", "run", "mail-archive worker")
		return func() {}, nil
	}
	kinds, err := jobRegistry(attachmentIndexer(a, blobs, log))
	if err != nil {
		return nil, err
	}
	pool := &jobs.Pool{Store: a.store, Kinds: kinds, Workers: a.cfg.JobWorkers, Logger: log}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := pool.Run(ctx); err != nil {
			log.Error("jobs stopped", "err", err)
		}
	}()
	stopSchedule := startStorageSchedule(ctx, a.store, log)
	stopRepair := startAttachmentRepair(ctx, a.store, log)
	log.Info("jobs on", "workers", a.cfg.JobWorkers, "kinds", strings.Join(kinds.Names(), ","))
	return func() {
		cancel()
		stopSchedule()
		stopRepair()
		<-done
	}, nil
}

func newWorkerCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "worker",
		Short: "Run only the background jobs (attachment text, storage usage), without the web server",
		Long: `Run only the background jobs, such as extracting the text of attachments
for search: no web server, SMTP, syncs, fills, attestations or retention.
` + config.EnvJobWorkers + ` (1 to 8, default 1) sets how many jobs run at a time.
Run it as a separate process with its own memory limit, and set
` + config.EnvJobWorkers + `=0 for serve, which otherwise runs them itself. Several
workers, also on several hosts, share the queue safely. It stops on SIGTERM
and hands running jobs back to the queue. See docs/guide/operations.md,
"Background jobs".

At start it takes the migration lock, applies pending migrations with
` + config.EnvMigrateOnStart + `=on and checks the schema, as serve does (exit
codes 3 and 4).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if cfg.JobWorkers == 0 {
				return errors.New(config.EnvJobWorkers + "=0: the worker would run no jobs; set it from 1 to 8")
			}
			log := newLogger(cfg.LogLevel)
			// The migration lock, start-up migrations and the schema check
			// (#396), as serve does.
			if err := prepareInstance(cmd.Context(), cfg, log, "worker"); err != nil {
				return err
			}
			a, err := openAppConfig(cmd.Context(), cfg, openNormal)
			if err != nil {
				return err
			}
			defer a.close()
			blobs, ar, err := openArchiveBlobs(cmd.Context(), a, "worker", archive.RoleWriter)
			if err != nil {
				return err
			}
			defer ar.Close()
			kinds, err := jobRegistry(attachmentIndexer(a, blobs, log))
			if err != nil {
				return err
			}
			attachmentsAtStart(cmd.Context(), a.store, log)
			stopSchedule := startStorageSchedule(cmd.Context(), a.store, log)
			defer stopSchedule()
			stopRepair := startAttachmentRepair(cmd.Context(), a.store, log)
			defer stopRepair()
			log.Info("worker on", "workers", a.cfg.JobWorkers, "kinds", strings.Join(kinds.Names(), ","))
			return (&jobs.Pool{Store: a.store, Kinds: kinds, Workers: a.cfg.JobWorkers, Logger: log}).Run(cmd.Context())
		},
	}
}

func newJobsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "jobs",
		Short: "Manage background jobs",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "retry KIND",
		Short: "Queue the failed jobs of a kind again",
		Long: `Queue the failed jobs of KIND (for example attachment_text) again, with
their attempts reset. ./ma status shows how many failed. A job fails after
its kind's attempts, for example when its process crashed each time; retry
after an upgrade that fixes the cause. Written to the audit log as
jobs.retry.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind := args[0]
			if !store.ValidJobKind(kind) {
				return fmt.Errorf("%q is not a job kind (lower-case letters, digits and _)", kind)
			}
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			e := cliInstance("jobs.retry")
			e.Detail("kind", kind)
			n, err := a.store.RetryFailedJobs(withCLIAudit(cmd, e), kind)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "queued %d failed %s job(s) again\n", n, kind)
			return nil
		},
	})
	return cmd
}

// jobsStatus is the jobs line of `status`, and a second line when PDFs
// failed because the PDF extractor was unavailable in a process.
func jobsStatus(ctx context.Context, a *app) (string, error) {
	stats, err := a.store.JobStats(ctx)
	if err != nil {
		return "", err
	}
	line := formatJobsStatus(stats)
	n, err := a.store.CountFailedJobsWithCode(ctx, store.JobAttachmentText, archive.CodeExtractorUnavailable)
	if err != nil {
		return "", err
	}
	return line + extractorStatus(n), nil
}

// extractorStatus is the line on messages whose PDFs could not be read
// because the PDF module did not compile in the process that ran them.
func extractorStatus(failed int64) string {
	if failed == 0 {
		return ""
	}
	return fmt.Sprintf("\nattachment text: PDF extractor unavailable for %d message(s); see the error in the log of serve or worker, "+
		"restart it, then run %s jobs retry %s", failed, commandName(), store.JobAttachmentText)
}

func formatJobsStatus(stats []store.JobStat) string {
	if len(stats) == 0 {
		return "jobs: none"
	}
	kinds := make([]string, 0, len(stats))
	for _, s := range stats {
		part := fmt.Sprintf("%s %d queued", s.Kind, s.Queued)
		if s.OldestDue > 0 {
			part += " (oldest " + jobAge(s.OldestDue) + ")"
		}
		for _, c := range []struct {
			n    int64
			what string
		}{{s.Retrying, "retrying"}, {s.Running, "running"}, {s.Failed, "failed"}} {
			if c.n > 0 {
				part += fmt.Sprintf(", %d %s", c.n, c.what)
			}
		}
		kinds = append(kinds, part)
	}
	return "jobs: " + strings.Join(kinds, "; ")
}

// jobAge shows d in its largest units: 45s, 3m, 2h5m, 3d.
func jobAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return config.FormatDuration(d.Round(time.Second))
	case d < 48*time.Hour:
		return config.FormatDuration(d.Truncate(time.Minute))
	}
	return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
}
