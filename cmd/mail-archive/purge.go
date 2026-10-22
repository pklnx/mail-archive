package main

import (
	"bufio"
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

func newAccountPurgeCmd() *cobra.Command {
	var (
		dryRun, cancel bool
		count          int64
		lockTimeout    time.Duration
	)
	cmd := &cobra.Command{
		Use:   "purge NAME [--dry-run] [--count N] [--cancel]",
		Short: "Delete a removed account completely, with the mail only it has",
		Long: `Delete a removed account completely: its locations, folders and the account
itself; its name is free again. A message that is also in another account
(of any user) stays there unchanged. A message whose last location is in
this account is deleted like a retention deletion: it leaves a record
(hash, size, dates, no subject or sender) and a signed deletion entry in
the next attestation, and its file is removed after
MAIL_ARCHIVE_RETENTION_FILE_GRACE.

Only for removed accounts ("account remove" first) that no retention rule
covers and no legal hold names or covers, released holds included. It is
refused while the account is being synced, filled or imported into.

It always shows what it would do first. --dry-run stops there. Otherwise
it asks for the number of messages it deletes; --count N gives it instead
and must match. An interrupted purge resumes with the same command (it was
confirmed), or when the web server starts. --cancel cancels an open purge:
what it deleted stays deleted, the account stays removed with the rest of
its mail.`,
		Example: `  mail-archive account purge old-import --dry-run
  mail-archive account purge old-import --user anna --count 42`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			acc, err := findAccount(cmd, a, args[0])
			if err != nil {
				return err
			}
			if cancel {
				return cancelAccountPurge(cmd, a, acc)
			}
			out := cmd.OutOrStdout()
			p, err := a.store.PurgePreview(cmd.Context(), acc.ID)
			if err != nil {
				return err
			}
			opts := purgeOptions(cmd, a, acc, lockTimeout)
			if p.Open != nil {
				if dryRun {
					fmt.Fprintf(out, "purge %d of account %q is confirmed and not finished: %s\n", p.Open.ID, acc.Name, purgeCounts(p.Open))
					fmt.Fprintf(out, "resume it with: account purge %s%s\ncancel it with: account purge %s%s --cancel\n",
						shellQuote(acc.Name), userArg(cmd), shellQuote(acc.Name), userArg(cmd))
					return nil
				}
				fmt.Fprintf(out, "resuming purge %d of account %q (confirmed %s)\n", p.Open.ID, acc.Name, p.Open.CreatedAt.Local().Format(time.DateTime))
				return executeAccountPurge(cmd, a, acc, p.Open.ID, opts)
			}
			printPurgePreview(cmd, a, p)
			if p.Blockers.Any() {
				return purgeBlockedError(cmd, acc, &p.Blockers)
			}
			if dryRun {
				fmt.Fprintln(out, "dry run: nothing was deleted")
				return nil
			}
			if !cmd.Flags().Changed("count") {
				fmt.Fprintf(out, "Type the number of messages to delete (%d) to delete account %q completely: ", p.Deleted, acc.Name)
				line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
				if err != nil && !errors.Is(err, io.EOF) {
					return err
				}
				if count, err = strconv.ParseInt(strings.TrimSpace(line), 10, 64); err != nil || count != p.Deleted {
					return errors.New("not confirmed; nothing was deleted")
				}
			} else if count != p.Deleted {
				return fmt.Errorf("--count %d does not match the %d message(s) to delete; nothing was deleted", count, p.Deleted)
			}
			e := cliAccountTarget(cliEntry("account.purge"), acc)
			purge, err := a.store.ConfirmPurge(withCLIAudit(cmd, e), p.Account.Ref(), count, nil)
			var blocked *store.PurgeBlockedError
			switch {
			case errors.As(err, &blocked):
				return purgeBlockedError(cmd, acc, &blocked.Blockers)
			case errors.Is(err, store.ErrPurgeChanged):
				return fmt.Errorf("%w; nothing was deleted", err)
			case err != nil:
				return staleError(err)
			}
			return executeAccountPurge(cmd, a, acc, purge.ID, opts)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "only show what would be deleted")
	cmd.Flags().Int64Var(&count, "count", 0, "the number of messages to delete, instead of the prompt (must match)")
	cmd.Flags().BoolVar(&cancel, "cancel", false, "cancel the open purge of the account")
	cmd.Flags().DurationVar(&lockTimeout, "lock-timeout", 10*time.Minute, "how long each batch waits for running syncs")
	return cmd
}

// userArg repeats --user for a printed command.
func userArg(cmd *cobra.Command) string {
	if f := cmd.Flag("user"); f != nil && f.Value.String() != "" {
		return " --user " + shellQuote(f.Value.String())
	}
	return ""
}

func purgeOptions(cmd *cobra.Command, a *app, acc *store.Account, lockTimeout time.Duration) archive.PurgeOptions {
	return archive.PurgeOptions{
		Grace: a.cfg.RetentionFileGrace, LockTimeout: lockTimeout, Logger: newLogger(a.cfg.LogLevel),
		Audit: func(action string) *store.AuditEntry { return cliAccountTarget(cliEntry(action), acc) },
		Progress: func(p *store.AccountPurge) {
			fmt.Fprintf(cmd.ErrOrStderr(), "%d location(s) removed, %d message(s) deleted, %d kept\n", p.Locations, p.Deleted, p.Kept)
		},
	}
}

func purgeCounts(p *store.AccountPurge) string {
	return fmt.Sprintf("%d location(s) removed, %d of %d message(s) deleted, %d kept in other accounts", p.Locations, p.Deleted, p.Expected, p.Kept)
}

func printPurgePreview(cmd *cobra.Command, a *app, p *store.PurgePreview) {
	out := cmd.OutOrStdout()
	owner := p.Owner
	if owner == "" {
		owner = "-"
	}
	fmt.Fprintf(out, "account %q (owner %s, %s)\n", p.Account.Name, owner, p.Account.Kind)
	fmt.Fprintf(out, "  locations to remove:   %d\n", p.Locations)
	fmt.Fprintf(out, "  messages to delete:    %d (%s), only in this account\n", p.Deleted, humanBytes(p.DeletedBytes))
	fmt.Fprintf(out, "  messages kept:         %d, also in other accounts\n", p.Kept)
	fmt.Fprintf(out, "  files removed after:   %s\n", a.cfg.RetentionFileGrace)
	if len(p.KeptIn) > 0 {
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(out, "\nmessages kept, by where they also are:")
		fmt.Fprintln(w, "  OWNER\tACCOUNT\tMESSAGES")
		for _, k := range p.KeptIn {
			o := k.Owner
			if o == "" {
				o = "-"
			}
			fmt.Fprintf(w, "  %s\t%s\t%d\n", o, k.Account, k.Messages)
		}
		_ = w.Flush()
		fmt.Fprintln(out)
	}
}

// purgeBlockedError names every blocker with how to remove it.
func purgeBlockedError(cmd *cobra.Command, acc *store.Account, b *store.PurgeBlockers) error {
	msgs := b.Messages(acc.Name)
	if b.NotRemoved {
		msgs = append(msgs, fmt.Sprintf("remove the account with: account remove %s%s", shellQuote(acc.Name), userArg(cmd)))
	}
	if len(b.Rules) > 0 {
		msgs = append(msgs, `remove a rule with: retention rule remove ID`)
	}
	for _, h := range b.Holds {
		if !h.Released {
			msgs = append(msgs, "an active legal hold must be released first: hold release ID --note TEXT")
			break
		}
	}
	return errors.New("cannot delete the account completely:\n  " + strings.Join(msgs, "\n  "))
}

func executeAccountPurge(cmd *cobra.Command, a *app, acc *store.Account, purgeID int64, opts archive.PurgeOptions) error {
	out := cmd.OutOrStdout()
	done, err := archive.ExecutePurge(cmd.Context(), a.store, purgeID, opts)
	var cancelled *archive.PurgeCancelledError
	switch {
	case errors.Is(err, archive.ErrPurgeRunning):
		// The web server's retention job took it over, or another command
		// runs it.
		fmt.Fprintf(out, "purge %d of account %q is being executed by another process (the web server, or another command); check with: account list\n",
			purgeID, acc.Name)
		return nil
	case errors.Is(err, archive.ErrSyncRunning):
		return fmt.Errorf("account %q is being synced, filled or imported into; purge %d is confirmed and resumes with: account purge %s%s",
			acc.Name, purgeID, shellQuote(acc.Name), userArg(cmd))
	case errors.As(err, &cancelled):
		p, perr := a.store.AccountPurge(cmd.Context(), purgeID)
		if perr == nil {
			fmt.Fprintf(out, "purge %d cancelled: %s\n", purgeID, purgeCounts(p))
		}
		return fmt.Errorf("%w; the account stays removed with the rest of its mail; preview again with: account purge %s%s --dry-run",
			cancelled.Err, shellQuote(acc.Name), userArg(cmd))
	case errors.Is(err, store.ErrStale):
		return fmt.Errorf("%w: account purge %s%s --cancel", err, shellQuote(acc.Name), userArg(cmd))
	case err != nil:
		return fmt.Errorf("%w; purge %d is confirmed and resumes with: account purge %s%s", err, purgeID, shellQuote(acc.Name), userArg(cmd))
	}
	fmt.Fprintf(out, "account %q deleted completely: %d location(s) removed, %d message(s) deleted, %d kept in other accounts; their files are removed after %s\n",
		acc.Name, done.Locations, done.Deleted, done.Kept, a.cfg.RetentionFileGrace)
	return nil
}

func cancelAccountPurge(cmd *cobra.Command, a *app, acc *store.Account) error {
	open, err := a.store.OpenAccountPurge(cmd.Context())
	if err != nil {
		return err
	}
	if open == nil || open.AccountID != acc.ID {
		return fmt.Errorf("account %q has no open purge", acc.Name)
	}
	e := cliAccountTarget(cliEntry("account.purge_cancel"), acc)
	if err := a.store.CancelPurge(withCLIAudit(cmd, e), open.ID, "cancelled from the command line"); err != nil {
		return err
	}
	p, err := a.store.AccountPurge(cmd.Context(), open.ID)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "purge %d of account %q cancelled: %s; the account stays removed with the rest of its mail\n",
		open.ID, acc.Name, purgeCounts(p))
	return nil
}
