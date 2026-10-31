package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/jobs"
	"github.com/pklnx/mail-archive/internal/store"
)

// testJobKinds are registered in every pool; only tests set them.
var testJobKinds []jobs.Kind

// jobRegistry returns the job kinds that serve and worker run.
func jobRegistry() (*jobs.Registry, error) {
	r := &jobs.Registry{}
	for _, k := range testJobKinds {
		if err := r.Register(k); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// startJobs runs the job pool of serve in the background, unless
// MAIL_ARCHIVE_JOB_WORKERS is 0. The returned stop cancels it and waits
// until its jobs are released or recorded; call it before the store
// closes.
func startJobs(ctx context.Context, a *app, log *slog.Logger) (stop func(), err error) {
	if a.cfg.JobWorkers == 0 {
		log.Info("jobs off in this process", "reason", config.EnvJobWorkers+"=0", "run", "mail-archive worker")
		return func() {}, nil
	}
	kinds, err := jobRegistry()
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
			if _, err := openBlobs(a.cfg.DataDir); err != nil {
				return err
			}
			kinds, err := jobRegistry()
			if err != nil {
				return err
			}
			log := newLogger(a.cfg.LogLevel)
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

// jobsStatus is the jobs line of `status`.
func jobsStatus(ctx context.Context, a *app) (string, error) {
	stats, err := a.store.JobStats(ctx)
	if err != nil {
		return "", err
	}
	return formatJobsStatus(stats), nil
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
