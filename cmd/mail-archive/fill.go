package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/store"
)

type fillFlags struct {
	host, username, tls, prefix, before string
	port                                int
	include, exclude                    []string
	confirmFolders, passwordStdin       bool
	resume, cancel                      bool
}

// fillSourceFlags are the flags of a new fill.
var fillSourceFlags = []string{"host", "username", "port", "tls", "include", "exclude", "prefix", "before", "confirm-folders"}

func newAccountFillCmd() *cobra.Command {
	var fl fillFlags
	cmd := &cobra.Command{
		Use:   "fill NAME [--host HOST --username USER | --resume | --cancel]",
		Short: "Copy an IMAP mailbox once into an smtp account (the mail from before journaling)",
		Long: `Copy the mail an IMAP mailbox holds once into an smtp account, so the mail
from before journaling started is in the same account. It is not a sync:
nothing is fetched again afterwards.

The messages go into folders of their own, PREFIX/<IMAP folder> (PREFIX is
the user name by default), never into the journal INBOX. A message is
imported when its INTERNALDATE is before the cut-off plus one hour, unless
the account already received a message with the same Message-ID over SMTP.
The cut-off is now by default; --before sets an earlier one (RFC 3339, or
YYYY-MM-DD for midnight UTC).

The password is read from the terminal (or from stdin with --password-stdin)
and kept, encrypted with ` + "MAIL_ARCHIVE_SECRET_KEY" + `, only while the fill runs.
Ctrl-C stops the fill; --resume continues it with the password asked again.
Without flags, the command lists the account's fills.`,
		Example: `  mail-archive account fill journal --host imap.example.com --username alice@example.com --exclude Spam
  mail-archive account fill journal --resume
  mail-archive account fill journal`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			source := slices.ContainsFunc(fillSourceFlags, cmd.Flags().Changed)
			switch {
			case fl.resume && fl.cancel:
				return errors.New("--resume and --cancel exclude each other")
			case (fl.resume || fl.cancel) && source:
				return errors.New("--resume and --cancel take no other settings")
			case source && (fl.host == "" || fl.username == ""):
				return errors.New("a new fill needs --host and --username")
			}
			a, acc, err := loadAccount(cmd, args[0])
			if err != nil {
				return err
			}
			defer a.close()
			if acc.Kind != store.KindSMTP {
				return fmt.Errorf("%q is not an smtp account; a fill copies a mailbox into an smtp account", acc.Name)
			}
			switch {
			case fl.cancel:
				return cancelFill(cmd, a, acc)
			case fl.resume:
				return resumeFill(cmd, a, acc, fl)
			case source:
				return startFill(cmd, a, acc, fl)
			default:
				return listFills(cmd, a, acc)
			}
		},
	}
	f := cmd.Flags()
	f.StringVar(&fl.host, "host", "", "IMAP server host name")
	f.IntVar(&fl.port, "port", 0, "IMAP port (default 993 for tls, 143 otherwise)")
	f.StringVar(&fl.tls, "tls", "tls", "connection security: tls, starttls or none")
	f.StringVar(&fl.username, "username", "", "IMAP user name")
	f.StringArrayVar(&fl.include, "include", nil, "only copy these folders (repeatable; default: all)")
	f.StringArrayVar(&fl.exclude, "exclude", nil, "skip these folders (repeatable)")
	f.StringVar(&fl.prefix, "prefix", "", "folder prefix in the account (default: the user name)")
	f.StringVar(&fl.before, "before", "", "cut-off: RFC 3339 or YYYY-MM-DD (UTC); default now")
	f.BoolVar(&fl.confirmFolders, "confirm-folders", false, "copy trash and spam folders too, if the filters select them")
	f.BoolVar(&fl.passwordStdin, "password-stdin", false, "read the password from stdin")
	f.BoolVar(&fl.resume, "resume", false, "continue the last fill that failed, was cancelled or was interrupted")
	f.BoolVar(&fl.cancel, "cancel", false, "stop the running fill")
	return cmd
}

// parseCutoff reads --before: RFC 3339, or a date for midnight UTC.
func parseCutoff(v string, now time.Time) (time.Time, error) {
	if v == "" {
		return now, nil
	}
	t, err := parseAuditDate(v, false)
	if err != nil {
		return time.Time{}, fmt.Errorf("--before: %w", err)
	}
	return t, nil
}

func startFill(cmd *cobra.Command, a *app, acc *store.Account, fl fillFlags) error {
	ctx := cmd.Context()
	tlsMode := store.TLSMode(fl.tls)
	switch tlsMode {
	case store.TLSModeTLS, store.TLSModeSTARTTLS, store.TLSModeNone:
	default:
		return fmt.Errorf("invalid --tls %q (use tls, starttls or none)", fl.tls)
	}
	port := fl.port
	if port == 0 {
		port = 993
		if tlsMode != store.TLSModeTLS {
			port = 143
		}
	}
	prefix := fl.prefix
	if prefix == "" {
		prefix = archive.DefaultFillPrefix(fl.username)
	}
	if err := store.ValidFillPrefix(prefix); err != nil {
		return fmt.Errorf("--prefix: %w", err)
	}
	cutoff, err := parseCutoff(fl.before, time.Now())
	if err != nil {
		return err
	}
	sealer, err := a.cfg.Sealer()
	if err != nil {
		return err
	}
	blobs, err := openBlobs(a.cfg.DataDir)
	if err != nil {
		return err
	}
	out := cmd.ErrOrStderr()
	if err := printFirstReceived(cmd, a, acc); err != nil {
		return err
	}
	fmt.Fprintf(out, "cut-off: %s (%s); messages from %s on are left to journaling\n",
		cutoff.UTC().Format(time.RFC3339), cutoff.Local().Format(time.RFC3339), cutoff.Add(archive.FillWindow).UTC().Format(time.RFC3339))

	password, err := readPassword(fl.passwordStdin, "IMAP password for "+fl.username+": ")
	if err != nil {
		return err
	}
	src := &store.Account{Host: fl.host, Port: port, TLSMode: tlsMode, Username: fl.username}
	fmt.Fprintf(out, "checking login at %s:%d ...\n", fl.host, port)
	folders, err := checkLogin(ctx, src, password)
	if err != nil {
		return fmt.Errorf("login check failed: %w", err)
	}
	// Trash and spam only after the user said so: once copied, excluding
	// them comes too late.
	if suggest := archive.SuggestExclusions(folders, fl.include, fl.exclude); len(suggest) > 0 && !fl.confirmFolders {
		args := []string{}
		for _, s := range suggest {
			args = append(args, "--exclude", shellQuote(s))
		}
		return fmt.Errorf("the server marks %s as trash or spam; skip them with %s, or copy them too with --confirm-folders",
			strings.Join(suggest, ", "), strings.Join(args, " "))
	}

	// The lock comes before the fill exists, so the web server's runner
	// cannot pick up the new fill in between.
	unlock, ok, err := a.store.TryLockSyncForWrite(ctx, acc.ID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%q is being synced, filled or removed by another process", acc.Name)
	}
	defer unlock()

	fill := &store.Fill{
		AccountID: acc.ID, Host: fl.host, Port: port, TLSMode: tlsMode, Username: fl.username,
		Included: fl.include, Excluded: fl.exclude, FolderPrefix: prefix, Cutoff: cutoff,
	}
	e := cliAccountTarget(cliEntry("data.fill"), acc)
	e.Detail("host", fl.host)
	e.Detail("username", fl.username)
	e.Detail("before", cutoff.UTC().Format(time.RFC3339))
	e.Detail("prefix", prefix)
	e.Detail("folders", map[string]any{"included": nonNilStrings(fl.include), "excluded": nonNilStrings(fl.exclude)})
	seal := func(id int64) ([]byte, error) { return archive.SealFillPassword(sealer, id, password) }
	if err := a.store.CreateFill(withCLIAudit(cmd, e), fill, seal); err != nil {
		switch {
		case errors.Is(err, store.ErrFillRunning):
			return fmt.Errorf("a fill of %q is running; %s account fill %s --cancel stops it", acc.Name, commandName(), shellQuote(acc.Name))
		case errors.Is(err, store.ErrConflict):
			return fmt.Errorf("%w; choose another --prefix", err)
		}
		return err
	}
	fmt.Fprintf(out, "fill %d started into %s/...\n", fill.ID, prefix)
	return runFill(cmd, a, acc, blobs, sealer, fill.ID)
}

func resumeFill(cmd *cobra.Command, a *app, acc *store.Account, fl fillFlags) error {
	ctx := cmd.Context()
	sealer, err := a.cfg.Sealer()
	if err != nil {
		return err
	}
	blobs, err := openBlobs(a.cfg.DataDir)
	if err != nil {
		return err
	}
	unlock, ok, err := a.store.TryLockSyncForWrite(ctx, acc.ID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%q is being synced, filled or removed by another process", acc.Name)
	}
	defer unlock()
	fills, err := a.store.AccountFills(ctx, acc.ID)
	if err != nil {
		return err
	}
	if len(fills) == 0 {
		return fmt.Errorf("%q has no fill to resume", acc.Name)
	}
	last := fills[0]
	switch last.Status {
	case store.FillRunning:
		// Running, but its lock was free: the process that ran it died. It
		// still has its password.
		fmt.Fprintf(cmd.ErrOrStderr(), "fill %d was interrupted; continuing it\n", last.ID)
		return runFill(cmd, a, acc, blobs, sealer, last.ID)
	case store.FillOK:
		return fmt.Errorf("the last fill of %q (%d) ended well; start a new one with --host and --username", acc.Name, last.ID)
	}
	password, err := readPassword(fl.passwordStdin, "IMAP password for "+last.Username+": ")
	if err != nil {
		return err
	}
	src := &store.Account{Host: last.Host, Port: last.Port, TLSMode: last.TLSMode, Username: last.Username}
	if _, err := checkLogin(ctx, src, password); err != nil {
		return fmt.Errorf("login check failed: %w", err)
	}
	e := cliAccountTarget(cliEntry("data.fill_resume"), acc)
	seal := func(id int64) ([]byte, error) { return archive.SealFillPassword(sealer, id, password) }
	if err := a.store.ResumeFill(withCLIAudit(cmd, e), last.ID, seal); err != nil {
		return err
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "fill %d resumed\n", last.ID)
	return runFill(cmd, a, acc, blobs, sealer, last.ID)
}

// runFill runs a fill whose account lock the caller holds, prints the
// result and records it in the audit log.
func runFill(cmd *cobra.Command, a *app, acc *store.Account, blobs *blobstore.Store, sealer *crypto.Sealer, id int64) error {
	ctx := cmd.Context()
	bg := context.WithoutCancel(ctx)
	out := cmd.ErrOrStderr()
	filler := &archive.Filler{
		Store: a.store, Blobs: blobs, Sealer: sealer, Logger: newLogger(a.cfg.LogLevel).With("account", acc.Name),
		Progress: func(ff store.FillFolder) {
			c := ff.Counts
			fmt.Fprintf(out, "%s: scanned %d, imported %d, after cut-off %d, journaled %d\n",
				ff.SourceName, c.Scanned, c.Imported, c.AfterCutoff, c.Journaled)
		},
	}
	fill, err := filler.RunLocked(ctx, id)
	if err != nil && ctx.Err() != nil {
		// Ctrl-C: the fill ends here; --resume continues it.
		if ferr := a.store.FinishFill(bg, id, store.FillFailed, "interrupted"); ferr != nil && !errors.Is(ferr, store.ErrFillNotRunning) {
			return ferr
		}
		fill, err = a.store.GetFill(bg, id)
	}
	if err != nil {
		return err
	}
	printFill(cmd.OutOrStdout(), fill)
	e := cliAccountTarget(cliEntry("data.fill_done"), acc)
	archive.AuditFillDone(e, fill)
	if err := a.store.Audit(bg, *e); err != nil {
		return fmt.Errorf("the fill ended (%s), but the audit log was not written: %w", fill.Status, err)
	}
	switch fill.Status {
	case store.FillOK:
		return nil
	case store.FillPartial:
		return &exitError{msg: "some folders failed; " + commandName() + " account fill " + shellQuote(acc.Name) + " --resume retries them", code: 1}
	case store.FillCancelled:
		return errors.New("the fill was cancelled")
	default:
		return fmt.Errorf("the fill failed: %s", fill.Error)
	}
}

func cancelFill(cmd *cobra.Command, a *app, acc *store.Account) error {
	fills, err := a.store.AccountFills(cmd.Context(), acc.ID)
	if err != nil {
		return err
	}
	if len(fills) == 0 || fills[0].Status != store.FillRunning {
		return fmt.Errorf("no fill of %q is running", acc.Name)
	}
	e := cliAccountTarget(cliEntry("data.fill_cancel"), acc)
	if err := a.store.CancelFill(withCLIAudit(cmd, e), fills[0].ID); err != nil {
		return err
	}
	fmt.Printf("fill %d cancelled; the process running it stops after its current batch\n", fills[0].ID)
	return nil
}

func listFills(cmd *cobra.Command, a *app, acc *store.Account) error {
	if err := printFirstReceived(cmd, a, acc); err != nil {
		return err
	}
	fills, err := a.store.AccountFills(cmd.Context(), acc.ID)
	if err != nil {
		return err
	}
	if len(fills) == 0 {
		fmt.Printf("no fills of %q\n", acc.Name)
		return nil
	}
	for i := range fills {
		printFill(cmd.OutOrStdout(), &fills[i])
	}
	return nil
}

func printFirstReceived(cmd *cobra.Command, a *app, acc *store.Account) error {
	first, err := a.store.FirstReceived(cmd.Context(), acc.ID)
	if err != nil {
		return err
	}
	if first == nil {
		fmt.Fprintln(cmd.ErrOrStderr(), "no message received over SMTP yet")
		return nil
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "first message received over SMTP: %s, %q\n", first.ReceivedAt.Local().Format(time.RFC3339), first.Subject)
	return nil
}

func printFill(w io.Writer, f *store.Fill) {
	fmt.Fprintf(w, "fill %d: %s, %s@%s:%d into %s/..., cut-off %s, started %s\n", f.ID, f.Status, f.Username, f.Host, f.Port,
		f.FolderPrefix, f.Cutoff.UTC().Format(time.RFC3339), f.CreatedAt.Local().Format(time.DateTime))
	if f.Error != "" {
		fmt.Fprintf(w, "  error: %s\n", f.Error)
	}
	if len(f.Folders) == 0 {
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  FOLDER\tSCANNED\tIMPORTED\tNEW\tAFTER CUT-OFF\tJOURNALED\tPRESENT\tDELETED\t")
	for _, ff := range f.Folders {
		c := ff.Counts
		fmt.Fprintf(tw, "  %s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%s\n", ff.SourceName, c.Scanned, c.Imported, c.New,
			c.AfterCutoff, c.Journaled, c.Present, c.Deleted, ff.Error)
	}
	_ = tw.Flush()
}

func nonNilStrings(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}
