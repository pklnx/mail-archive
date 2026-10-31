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

// jobRegistry returns the job kinds that serve and worker run.
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
	return r, nil
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
	log.Info("jobs on", "workers", a.cfg.JobWorkers, "kinds", strings.Join(kinds.Names(), ","))
	return func() {
		cancel()
		<-done
	}, nil
}

func newWorkerCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "worker",
		Short: "Run only the background jobs (attachment text), without the web server",
		Long: `Run only the background jobs, such as extracting the text of attachments
for search: no web server, SMTP, syncs, fills, attestations or retention.
` + config.EnvJobWorkers + ` (1 to 8, default 1) sets how many jobs run at a time.
Run it as a separate process with its own memory limit, and set
` + config.EnvJobWorkers + `=0 for serve, which otherwise runs them itself. Several
workers, also on several hosts, share the queue safely. It stops on SIGTERM
and hands running jobs back to the queue. See docs/guide/operations.md,
"Background jobs".`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			if a.cfg.JobWorkers == 0 {
				return errors.New(config.EnvJobWorkers + "=0: the worker would run no jobs; set it from 1 to 8")
			}
			blobs, err := openBlobs(a.cfg.DataDir)
			if err != nil {
				return err
			}
			log := newLogger(a.cfg.LogLevel)
			kinds, err := jobRegistry(attachmentIndexer(a, blobs, log))
			if err != nil {
				return err
			}
			attachmentsAtStart(cmd.Context(), a.store, log)
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
			e := cliEntry("jobs.retry")
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
