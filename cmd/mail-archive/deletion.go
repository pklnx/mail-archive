package main

import (
	"bufio"
	"encoding/csv"
	"errors"
	"fmt"
	"io"

	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/store"
)

func parseRunID(arg string) (int64, error) {
	id, err := strconv.ParseInt(arg, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("run ID %q is not a number", arg)
	}
	return id, nil
}

func newRetentionPreviewCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "preview",
		Short: "List what may be deleted now, as a deletion run to review",
		Long: `Create a deletion run with every message that may be deleted now: each of
its locations, in every user's account, has a retention rule whose period
has ended, and no legal hold covers it. Nothing is deleted. Review the run
with "retention show ID", then delete with "retention confirm ID" or drop it
with "retention cancel ID". Only one run can be open.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			run, err := a.store.ProposeDeletion(withCLIAudit(cmd, cliEntry("retention.preview")), nil)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "deletion run %d: %d message(s), %s\n", run.ID, run.Candidates, humanBytes(run.Bytes))
			if run.Candidates == 0 {
				fmt.Fprintln(out, "nothing to delete; cancel the run with: retention cancel", run.ID)
				return nil
			}
			if err := printRunSummary(cmd, a, run.ID); err != nil {
				return err
			}
			fmt.Fprintf(out, "\nreview with: retention show %d\ndelete with: retention confirm %d\n", run.ID, run.ID)
			return nil
		},
	}
}

func printRunSummary(cmd *cobra.Command, a *app, runID int64) error {
	groups, err := a.store.DeletionRunSummary(cmd.Context(), runID)
	if err != nil || len(groups) == 0 {
		return err
	}
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "\nOWNER\tACCOUNT\tFOLDER\tPERIOD ENDED\tMESSAGES")
	for _, g := range groups {
		owner := g.Owner
		if owner == "" {
			owner = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\n", owner, g.Account, g.Folder, g.Year, g.Messages)
	}
	return w.Flush()
}

func newRetentionShowCmd() *cobra.Command {
	var format string
	cmd := &cobra.Command{
		Use:   "show ID",
		Short: "Show a deletion run and its messages",
		Long: `Show a deletion run: its state and counts, the messages by owner, account,
folder and the year their period ended, and every message with its date,
sender, subject, locations and the end of its retention period.
--format csv writes the messages as CSV for a review elsewhere.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseRunID(args[0])
			if err != nil {
				return err
			}
			if format != "text" && format != "csv" {
				return fmt.Errorf("--format must be text or csv, got %q", format)
			}
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			run, err := a.store.DeletionRun(cmd.Context(), id)
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("no deletion run %d", id)
			}
			if err != nil {
				return err
			}
			candidates, err := a.store.DeletionCandidates(cmd.Context(), id)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if format == "csv" {
				return writeCandidatesCSV(out, candidates)
			}
			fmt.Fprintln(out, runLine(run))
			if err := printRunSummary(cmd, a, id); err != nil {
				return err
			}
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "\nSTATE\tPERIOD ENDED\tDATE\tFROM\tSUBJECT\tLOCATIONS\tSHA256")
			for _, c := range candidates {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", c.State, retentionEndDay(c.RetainUntil), dateOrDash(c.SentAt),
					clip(c.From, 40), clip(c.Subject, 60), strings.Join(c.Places, ", "), c.SHA256)
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "text or csv")
	return cmd
}

func writeCandidatesCSV(out io.Writer, candidates []store.DeletionCandidate) error {
	w := csv.NewWriter(out)
	_ = w.Write([]string{"sha256", "state", "size", "retain_until", "sent_at", "from", "subject", "locations"})
	for _, c := range candidates {
		sent := ""
		if c.SentAt != nil {
			sent = c.SentAt.UTC().Format(time.RFC3339)
		}
		_ = w.Write([]string{c.SHA256, c.State, strconv.FormatInt(c.Size, 10), c.RetainUntil.UTC().Format(time.RFC3339),
			sent, csvSafe(c.From), csvSafe(c.Subject), csvSafe(strings.Join(c.Places, "; "))})
	}
	w.Flush()
	return w.Error()
}

// retentionEndDay is the last day of a period from its end (the first
// instant after it), in UTC.
func retentionEndDay(t time.Time) string {
	return t.Add(-time.Microsecond).UTC().Format(time.DateOnly)
}

func dateOrDash(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Local().Format(time.DateOnly)
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func runLine(r *store.DeletionRun) string {
	by := r.CreatedBy
	if by == "" {
		by = "the command line"
	}
	line := fmt.Sprintf("deletion run %d: %s, %d message(s), %s, proposed %s by %s", r.ID, r.State, r.Candidates,
		humanBytes(r.Bytes), r.CreatedAt.Local().Format(time.DateTime), by)
	if r.ConfirmedAt != nil {
		line += fmt.Sprintf(", confirmed %s", r.ConfirmedAt.Local().Format(time.DateTime))
	}
	if r.State == store.RunDone || r.Deleted+r.Kept > 0 {
		line += fmt.Sprintf(", %d deleted, %d kept", r.Deleted, r.Kept)
	}
	return line
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func newRetentionConfirmCmd() *cobra.Command {
	var yes bool
	var lockTimeout time.Duration
	cmd := &cobra.Command{
		Use:   "confirm ID",
		Short: "Delete the messages of a reviewed deletion run",
		Long: `Confirm a proposed deletion run and delete its messages, in batches. Each
batch checks every candidate again: a rule removed, a legal hold added or a
new location since the preview keeps it. Deleted messages leave a record
(hash, size, dates, no subject or sender) and a signed deletion entry in
the next attestation. Their files are removed after
MAIL_ARCHIVE_RETENTION_FILE_GRACE.

It asks for the number of messages unless --yes is given. An interrupted
run resumes with the same command, or when the web server starts.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseRunID(args[0])
			if err != nil {
				return err
			}
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			run, err := a.store.DeletionRun(cmd.Context(), id)
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("no deletion run %d", id)
			}
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			switch run.State {
			case store.RunProposed:
				if !yes {
					fmt.Fprintf(out, "%s\nType the number of messages (%d) to delete them: ", runLine(run), run.Candidates)
					line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
					if err != nil && !errors.Is(err, io.EOF) {
						return err
					}
					if strings.TrimSpace(line) != strconv.Itoa(run.Candidates) {
						return errors.New("not confirmed; nothing was deleted")
					}
				}
				if err := a.store.ConfirmDeletionRun(withCLIAudit(cmd, cliEntry("retention.confirm")), id, nil); err != nil {
					return err
				}
			case store.RunConfirmed:
				fmt.Fprintf(out, "resuming deletion run %d\n", id)
			default:
				return fmt.Errorf("deletion run %d is %s", id, run.State)
			}
			done, err := archive.ExecuteDeletionRun(cmd.Context(), a.store, id, archive.DeletionOptions{
				Grace: a.cfg.RetentionFileGrace, LockTimeout: lockTimeout, Logger: newLogger(a.cfg.LogLevel),
				Audit:    func(action string) *store.AuditEntry { return cliEntry(action) },
				Progress: func(d, k int) { fmt.Fprintf(cmd.ErrOrStderr(), "deleted %d, kept %d\n", d, k) },
			})
			if err != nil {
				return fmt.Errorf("%w; the run is confirmed and resumes with: retention confirm %d", err, id)
			}
			fmt.Fprintf(out, "deletion run %d done: %d deleted, %d kept\n", id, done.Deleted, done.Kept)
			fmt.Fprintf(out, "their files are removed after %s (retention sweep, or the web server)\n", a.cfg.RetentionFileGrace)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "do not ask for the number of messages")
	cmd.Flags().DurationVar(&lockTimeout, "lock-timeout", 10*time.Minute, "how long each batch waits for running syncs")
	return cmd
}

func newRetentionCancelCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cancel ID",
		Short: "Drop a proposed deletion run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseRunID(args[0])
			if err != nil {
				return err
			}
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			err = a.store.CancelDeletionRun(withCLIAudit(cmd, cliEntry("retention.cancel")), id)
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("no deletion run %d", id)
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "deletion run %d cancelled; nothing was deleted\n", id)
			return nil
		},
	}
}

func newRetentionRunsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "runs",
		Short: "List the deletion runs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			runs, err := a.store.DeletionRuns(cmd.Context(), 50)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(runs) == 0 {
				fmt.Fprintln(out, "no deletion runs")
				return nil
			}
			for i := range runs {
				fmt.Fprintln(out, runLine(&runs[i]))
			}
			return nil
		},
	}
}

func newRetentionSweepCmd() *cobra.Command {
	var lockTimeout time.Duration
	cmd := &cobra.Command{
		Use:   "sweep",
		Short: "Remove the files of deleted messages whose grace period ended",
		Long: `Remove the files of messages deleted by retention or an account purge once
MAIL_ARCHIVE_RETENTION_FILE_GRACE has passed. The web server does this every
hour. A file whose message was archived again stays.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			blobs, err := openBlobs(a.cfg.DataDir)
			if err != nil {
				return err
			}
			n, err := archive.SweepBlobs(cmd.Context(), a.store, blobs, archive.SweepOptions{
				LockTimeout: lockTimeout, Logger: newLogger(a.cfg.LogLevel),
				Audit: func() *store.AuditEntry { return cliEntry("retention.sweep") },
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed %d file(s)\n", n)
			return nil
		},
	}
	cmd.Flags().DurationVar(&lockTimeout, "lock-timeout", time.Minute, "how long to wait for running syncs")
	return cmd
}
