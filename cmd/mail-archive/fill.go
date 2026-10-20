package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/store"
)

type fillFlags struct {
	host, username, tls, prefix, before, interval string
	port                                          int
	fillID                                        int64
	include, exclude                              []string
	confirmFolders, passwordStdin                 bool
	resume, cancel, keepRunning                   bool
}

// fillSourceFlags are the flags of a new fill.
var fillSourceFlags = []string{"host", "username", "port", "tls", "include", "exclude", "before"}

func newAccountFillCmd() *cobra.Command {
	var fl fillFlags
	cmd := &cobra.Command{
		Use:   "fill NAME [--host HOST --username USER [--keep-running] | --resume | --cancel | --keep-running | --interval D]",
		Short: "Copy an IMAP mailbox into an smtp account, once or on a schedule",
		Long: `Copy the mail an IMAP mailbox holds into an smtp account, so the mail from
before journaling started is in the same account. By default the fill runs
once; with --keep-running it keeps importing what shows up in the mailbox
later and never passed the journal.

The messages go into the account's folder JOURNAL, next to the journaled
mail, whatever their IMAP folder; they have no envelope, and the message
view names the mailbox they came from.

A one-time fill imports a message when its INTERNALDATE is before the
cut-off plus one hour, unless the account already received a message with
the same Message-ID over SMTP. The cut-off is now by default; --before sets
an earlier one (RFC 3339, or YYYY-MM-DD for midnight UTC).

A recurring fill (--keep-running) has no cut-off: it imports a message
once it is two hours old, unless the account received it over SMTP. The
first run happens here; the web server then runs it every --interval
("default": ` + config.EnvSyncInterval + `, or a duration from 5m to 720h),
continuing each IMAP folder where the last run stopped. Moves and
deletions in the mailbox are ignored. --keep-running without --host
switches an ended fill on (its first run scans the folders again and
imports what the cut-off left out; it first copies an older fill's folders
into JOURNAL); --interval alone changes the interval; --cancel stops it.
With the schedule off (` + config.EnvSyncInterval + `=0) there are no
scheduled runs; a run already started still finishes.

The password is read from the terminal (or from stdin with --password-stdin)
and kept, encrypted with ` + "MAIL_ARCHIVE_SECRET_KEY" + `, while the fill runs or keeps
running. Ctrl-C stops a one-time fill; --resume continues it with the
password asked again, and resumes a stopped recurring fill.
--fill ID chooses the fill; without it, --cancel and --interval take the
only fill that runs or keeps running, --resume and --keep-running the
newest fill. Without flags, the command lists the account's fills.`,
		Example: `  mail-archive account fill journal --host imap.example.com --username alice@example.com --exclude Spam
  mail-archive account fill journal --host imap.example.com --username alice@example.com --keep-running --interval 12h
  mail-archive account fill journal --resume
  mail-archive account fill journal --cancel --fill 3
  mail-archive account fill journal`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			source := slices.ContainsFunc(fillSourceFlags, cmd.Flags().Changed)
			changed := cmd.Flags().Changed
			actions := 0
			for _, on := range []bool{fl.resume, fl.cancel, fl.keepRunning && !source} {
				if on {
					actions++
				}
			}
			switch {
			case changed("prefix"):
				return errors.New("--prefix is no longer supported: fills write into " + store.SMTPFolder)
			case actions > 1:
				return errors.New("--resume, --cancel and --keep-running exclude each other")
			case (fl.resume || fl.cancel) && (source || changed("interval") || fl.keepRunning):
				return errors.New("--resume and --cancel take no other settings")
			case source && (fl.host == "" || fl.username == ""):
				return errors.New("a new fill needs --host and --username")
			case source && changed("fill"):
				return errors.New("--fill chooses an existing fill; a new fill takes no --fill")
			case fl.keepRunning && changed("before"):
				return errors.New("--before and --keep-running exclude each other: a recurring fill imports what is two hours old")
			case source && changed("interval") && !fl.keepRunning:
				return errors.New("--interval needs --keep-running")
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
				return cancelFill(cmd, a, acc, fl)
			case fl.resume:
				return resumeFill(cmd, a, acc, fl)
			case source:
				return startFill(cmd, a, acc, fl)
			case fl.keepRunning:
				return keepFillRunning(cmd, a, acc, fl)
			case changed("interval"):
				return changeFillInterval(cmd, a, acc, fl)
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
	// Gone with #112; kept hidden for a clear error instead of "unknown flag".
	f.StringVar(&fl.prefix, "prefix", "", "no longer supported")
	_ = f.MarkHidden("prefix")
	f.StringVar(&fl.before, "before", "", "cut-off: RFC 3339 or YYYY-MM-DD (UTC); default now")
	f.BoolVar(&fl.confirmFolders, "confirm-folders", false, "copy trash and spam folders too, if the filters select them")
	f.BoolVar(&fl.passwordStdin, "password-stdin", false, "read the password from stdin")
	f.BoolVar(&fl.keepRunning, "keep-running", false, "keep importing new mail on the web server's schedule (recurring fill)")
	f.StringVar(&fl.interval, "interval", archive.OwnIntervalDefault, `how often a recurring fill runs: "default" or a duration like 12h`)
	f.Int64Var(&fl.fillID, "fill", 0, "the fill to resume, cancel or change (see the list)")
	f.BoolVar(&fl.resume, "resume", false, "continue a fill that failed, was cancelled or was interrupted; resume a stopped recurring fill")
	f.BoolVar(&fl.cancel, "cancel", false, "stop a running or recurring fill")
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
	var cutoff *time.Time
	var interval *time.Duration
	sched := scheduleOf(a.cfg)
	if fl.keepRunning {
		var err error
		if interval, err = parseFillInterval(sched, fl.interval, nil); err != nil {
			return err
		}
	} else {
		c, err := parseCutoff(fl.before, time.Now())
		if err != nil {
			return err
		}
		cutoff = &c
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
	if cutoff != nil {
		fmt.Fprintf(out, "cut-off: %s (%s); messages from %s on are left to journaling\n",
			cutoff.UTC().Format(time.RFC3339), cutoff.Local().Format(time.RFC3339), cutoff.Add(archive.FillWindow).UTC().Format(time.RFC3339))
	} else {
		fmt.Fprintf(out, "recurring: messages are imported once they are %s old, unless journaled\n", config.FormatDuration(archive.RecurringGrace))
	}

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
	suggest := archive.SuggestExclusions(folders, fl.include, fl.exclude)
	if len(suggest) > 0 && !fl.confirmFolders {
		return trashSpamError(suggest)
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
		Included: fl.include, Excluded: fl.exclude, Cutoff: cutoff, KeepRunning: fl.keepRunning, Interval: interval,
	}
	if fl.keepRunning {
		fill.Confirmed = suggest
	}
	e := cliAccountTarget(cliEntry("data.fill"), acc)
	e.Detail("host", fl.host)
	e.Detail("username", fl.username)
	if cutoff != nil {
		e.Detail("before", cutoff.UTC().Format(time.RFC3339))
	} else {
		e.Detail("keep_running", true)
		e.Detail("interval", archive.FormatOwnInterval(interval))
	}
	e.Detail("folders", map[string]any{"included": nonNilStrings(fl.include), "excluded": nonNilStrings(fl.exclude)})
	seal := func(id int64) ([]byte, error) { return archive.SealFillPassword(sealer, id, password) }
	if err := a.store.CreateFill(withCLIAudit(cmd, e), fill, seal); err != nil {
		switch {
		case errors.Is(err, store.ErrFillRunning):
			return fmt.Errorf("a fill of %q is running; %s account fill %s --cancel stops it", acc.Name, commandName(), shellQuote(acc.Name))
		}
		return err
	}
	fmt.Fprintf(out, "fill %d started into %s\n", fill.ID, store.SMTPFolder)
	return runFill(cmd, a, acc, blobs, sealer, fill.ID)
}

// trashSpamError asks to exclude or confirm trash and spam folders.
func trashSpamError(suggest []string) error {
	args := []string{}
	for _, s := range suggest {
		args = append(args, "--exclude", shellQuote(s))
	}
	return fmt.Errorf("the server marks %s as trash or spam; skip them with %s, or copy them too with --confirm-folders",
		strings.Join(suggest, ", "), strings.Join(args, " "))
}

// parseFillInterval reads --interval of a recurring fill; a value other
// than stored must not be below the server's floor.
func parseFillInterval(sched archive.Schedule, v string, stored *time.Duration) (*time.Duration, error) {
	d, err := archive.ParseFillInterval(v)
	if err != nil {
		return nil, fmt.Errorf("--interval: %s", sched.FillIntervalRule())
	}
	if !archive.SameOwnInterval(d, stored) {
		if err := sched.CheckFloor(d); err != nil {
			return nil, fmt.Errorf("--interval: %w", err)
		}
	}
	return d, nil
}

// pickFill returns the fill --fill names, or else the only fill of the
// account that match accepts; what describes them in errors.
func pickFill(fills []store.Fill, acc *store.Account, id int64, what string, match func(*store.Fill) bool) (*store.Fill, error) {
	if id != 0 {
		for i := range fills {
			if fills[i].ID == id {
				return &fills[i], nil
			}
		}
		return nil, fmt.Errorf("%q has no fill %d", acc.Name, id)
	}
	var found []*store.Fill
	for i := range fills {
		if match(&fills[i]) {
			found = append(found, &fills[i])
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("no fill of %q %s", acc.Name, what)
	case 1:
		return found[0], nil
	}
	ids := make([]string, len(found))
	for i, f := range found {
		ids[i] = strconv.FormatInt(f.ID, 10)
	}
	return nil, fmt.Errorf("several fills of %q %s (%s); choose one with --fill", acc.Name, what, strings.Join(ids, ", "))
}

// newestFill returns the fill --fill names, or else the newest one (fills
// are newest first); what is the action, for the error without fills.
func newestFill(fills []store.Fill, acc *store.Account, id int64, what string) (*store.Fill, error) {
	switch {
	case id != 0:
		return pickFill(fills, acc, id, "", nil)
	case len(fills) == 0:
		return nil, fmt.Errorf("%q has no fill to %s", acc.Name, what)
	}
	return &fills[0], nil
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
	last, err := newestFill(fills, acc, fl.fillID, "resume")
	if err != nil {
		return err
	}
	switch {
	case last.Status == store.FillRunning:
		// Running, but its lock was free: the process that ran it died. It
		// still has its password.
		fmt.Fprintf(cmd.ErrOrStderr(), "fill %d was interrupted; continuing it\n", last.ID)
		return runFill(cmd, a, acc, blobs, sealer, last.ID)
	case last.KeepRunning:
		return fmt.Errorf("fill %d of %q keeps running; nothing to resume", last.ID, acc.Name)
	case last.Status == store.FillOK:
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

// keepFillRunning switches an ended fill on: it asks for the password,
// checks the login and runs the first run here.
func keepFillRunning(cmd *cobra.Command, a *app, acc *store.Account, fl fillFlags) error {
	ctx := cmd.Context()
	sealer, err := a.cfg.Sealer()
	if err != nil {
		return err
	}
	blobs, err := openBlobs(a.cfg.DataDir)
	if err != nil {
		return err
	}
	interval, err := parseFillInterval(scheduleOf(a.cfg), fl.interval, nil)
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
	fill, err := newestFill(fills, acc, fl.fillID, "switch on")
	if err != nil {
		return err
	}
	switch {
	case fill.KeepRunning:
		return fmt.Errorf("fill %d of %q already keeps running", fill.ID, acc.Name)
	case fill.Status == store.FillRunning:
		return fmt.Errorf("fill %d of %q is running; let it end, or cancel it, first", fill.ID, acc.Name)
	}
	password, err := readPassword(fl.passwordStdin, "IMAP password for "+fill.Username+": ")
	if err != nil {
		return err
	}
	src := &store.Account{Host: fill.Host, Port: fill.Port, TLSMode: fill.TLSMode, Username: fill.Username}
	folders, err := checkLogin(ctx, src, password)
	if err != nil {
		return fmt.Errorf("login check failed: %w", err)
	}
	// Trash and spam folders the fill has not filled yet need a yes.
	var confirm []string
	for _, name := range archive.SuggestExclusions(folders, fill.Included, fill.Excluded) {
		if !slices.ContainsFunc(fill.Folders, func(ff store.FillFolder) bool { return ff.SourceName == name }) {
			confirm = append(confirm, name)
		}
	}
	if len(confirm) > 0 && !fl.confirmFolders {
		return fmt.Errorf("the server marks %s as trash or spam, and the fill has not copied them; copy them too with --confirm-folders",
			strings.Join(confirm, ", "))
	}
	e := cliAccountTarget(cliEntry("data.fill_change"), acc)
	e.Detail("keep_running", map[string]any{"from": false, "to": true})
	e.Detail("interval", map[string]any{"from": archive.FillIntervalDetail(fill), "to": archive.FormatOwnInterval(interval)})
	seal := func(id int64) ([]byte, error) { return archive.SealFillPassword(sealer, id, password) }
	if err := a.store.SetFillRecurring(withCLIAudit(cmd, e), fill.ID, interval, confirm, seal); err != nil {
		return err
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "fill %d keeps running; its first run starts now\n", fill.ID)
	return runFill(cmd, a, acc, blobs, sealer, fill.ID)
}

// changeFillInterval sets the interval of a recurring fill.
func changeFillInterval(cmd *cobra.Command, a *app, acc *store.Account, fl fillFlags) error {
	ctx := cmd.Context()
	fills, err := a.store.AccountFills(ctx, acc.ID)
	if err != nil {
		return err
	}
	fill, err := pickFill(fills, acc, fl.fillID, "keeps running", func(f *store.Fill) bool { return f.KeepRunning })
	if err != nil {
		return err
	}
	if !fill.KeepRunning {
		return fmt.Errorf("fill %d of %q does not keep running", fill.ID, acc.Name)
	}
	sched := scheduleOf(a.cfg)
	interval, err := parseFillInterval(sched, fl.interval, fill.Interval)
	if err != nil {
		return err
	}
	if !archive.SameOwnInterval(interval, fill.Interval) {
		e := cliAccountTarget(cliEntry("data.fill_change"), acc)
		e.Detail("interval", map[string]any{"from": archive.FormatOwnInterval(fill.Interval), "to": archive.FormatOwnInterval(interval)})
		if err := a.store.SetFillInterval(withCLIAudit(cmd, e), fill.ID, interval); err != nil {
			return err
		}
		fill.Interval = interval
	}
	fmt.Printf("fill %d: %s\n", fill.ID, fillSchedule(sched, fill, time.Now()))
	return nil
}

// runFill runs a fill whose account lock the caller holds, prints the
// result and records the end of a one-time fill in the audit log.
func runFill(cmd *cobra.Command, a *app, acc *store.Account, blobs *blobstore.Store, sealer *crypto.Sealer, id int64) error {
	ctx := cmd.Context()
	bg := context.WithoutCancel(ctx)
	out := cmd.ErrOrStderr()
	filler := &archive.Filler{
		Store: a.store, Blobs: blobs, Sealer: sealer, Logger: newLogger(a.cfg.LogLevel).With("account", acc.Name),
		Progress: func(ff store.FillFolder) {
			c := ff.Counts
			fmt.Fprintf(out, "%s: scanned %d, imported %d, after cut-off %d, journaled %d, waiting %d\n",
				ff.SourceName, c.Scanned, c.Imported, c.AfterCutoff, c.Journaled, c.Waiting)
		},
	}
	fill, err := filler.RunLocked(ctx, id)
	if errors.Is(err, archive.ErrFillSkipped) {
		fmt.Fprintf(out, "%q is disabled: the web server runs the fill once it is enabled\n", acc.Name)
		return nil
	}
	if err != nil && ctx.Err() != nil {
		if fill != nil && fill.Recurring() {
			// A recurring fill stays running with its password: the web
			// server continues the run.
			return errors.New("interrupted; the web server continues the fill's run")
		}
		// Ctrl-C: the fill ends here; --resume continues it.
		if ferr := a.store.FinishFill(bg, id, store.FillFailed, "interrupted"); ferr != nil && !errors.Is(ferr, store.ErrFillNotRunning) {
			return ferr
		}
		fill, err = a.store.GetFill(bg, id)
	}
	if err != nil {
		return err
	}
	sched := scheduleOf(a.cfg)
	printFill(cmd.OutOrStdout(), fill, fillInfo{sched: sched, now: time.Now()})
	if !fill.Recurring() {
		e := cliAccountTarget(cliEntry("data.fill_done"), acc)
		archive.AuditFillDone(e, fill)
		if err := a.store.Audit(bg, *e); err != nil {
			return fmt.Errorf("the fill ended (%s), but the audit log was not written: %w", fill.Status, err)
		}
	} else if fill.KeepRunning {
		if every := sched.Every(fill.Interval); every > 0 {
			fmt.Fprintf(out, "the web server runs it every %s\n", config.FormatDuration(every))
		} else {
			fmt.Fprintf(out, "note: the web server's schedule is off (%s=0), so it does not run again\n", config.EnvSyncInterval)
		}
	}
	switch fill.Status {
	case store.FillOK:
		return nil
	case store.FillPartial:
		if fill.KeepRunning {
			return &exitError{msg: "some folders failed; the next run retries them", code: 1}
		}
		return &exitError{msg: "some folders failed; " + commandName() + " account fill " + shellQuote(acc.Name) + " --resume retries them", code: 1}
	case store.FillCancelled:
		return errors.New("the fill was cancelled")
	default:
		return fmt.Errorf("the fill failed: %s", fill.Error)
	}
}

func cancelFill(cmd *cobra.Command, a *app, acc *store.Account, fl fillFlags) error {
	fills, err := a.store.AccountFills(cmd.Context(), acc.ID)
	if err != nil {
		return err
	}
	fill, err := pickFill(fills, acc, fl.fillID, "runs or keeps running", func(f *store.Fill) bool {
		return f.Status == store.FillRunning || f.KeepRunning
	})
	if err != nil {
		return err
	}
	e := cliAccountTarget(cliEntry("data.fill_cancel"), acc)
	if err := a.store.CancelFill(withCLIAudit(cmd, e), fill.ID); err != nil {
		if errors.Is(err, store.ErrFillNotRunning) {
			return fmt.Errorf("fill %d of %q neither runs nor keeps running", fill.ID, acc.Name)
		}
		return err
	}
	if fill.Status == store.FillRunning {
		fmt.Printf("fill %d cancelled; the process running it stops after its current batch\n", fill.ID)
	} else {
		fmt.Printf("fill %d stopped; it no longer keeps running\n", fill.ID)
	}
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
	health, err := a.store.FillHealth(cmd.Context(), nil)
	if err != nil {
		return err
	}
	info := fillInfo{sched: scheduleOf(a.cfg), health: health, alertAfter: a.cfg.AlertAfterFailures, now: time.Now()}
	for i := range fills {
		printFill(cmd.OutOrStdout(), &fills[i], info)
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

// fillInfo is what printFill shows of a recurring fill: the schedule and,
// if set, the health.
type fillInfo struct {
	sched      archive.Schedule
	health     map[int64]*store.FillHealth
	alertAfter int
	now        time.Time
}

// fillSchedule tells how often the web server runs a recurring fill and
// when next.
func fillSchedule(sched archive.Schedule, f *store.Fill, now time.Time) string {
	if !f.KeepRunning {
		return "stopped"
	}
	next, ok := sched.NextFill(f)
	if !ok {
		return "keeps running, but the schedule is off"
	}
	return fmt.Sprintf("keeps running every %s, next %s", config.FormatDuration(sched.Every(f.Interval)), nextSync(next, now))
}

func printFill(w io.Writer, f *store.Fill, info fillInfo) {
	// Fills from before #112 write into folders of their own, until they
	// become recurring.
	into := store.SMTPFolder
	if !f.IntoJournal() {
		into = f.FolderPrefix + "/..."
	}
	rule := "two-hour rule"
	if f.Cutoff != nil {
		rule = "cut-off " + f.Cutoff.UTC().Format(time.RFC3339)
	}
	fmt.Fprintf(w, "fill %d: %s, %s@%s:%d into %s, %s, started %s\n", f.ID, f.Status, f.Username, f.Host, f.Port,
		into, rule, f.CreatedAt.Local().Format(time.DateTime))
	if f.Recurring() {
		line := "  " + fillSchedule(info.sched, f, info.now)
		if h := info.health[f.ID]; h != nil {
			var busy *time.Time
			if h.Running {
				busy = &info.now
				if h.RunStartedAt != nil {
					busy = h.RunStartedAt
				}
			}
			line += ", health " + h.State(info.alertAfter, info.sched.Every(h.Interval), busy, info.now)
		}
		var t store.FillCounts
		for _, ff := range f.Folders {
			t.Add(ff.Counts)
		}
		line += fmt.Sprintf(", %d waiting; counts of the last run", t.Waiting)
		fmt.Fprintln(w, line)
		if len(f.Unconfirmed) > 0 {
			fmt.Fprintf(w, "  not confirmed (skipped): %s\n", strings.Join(f.Unconfirmed, ", "))
		}
	}
	if f.Error != "" {
		fmt.Fprintf(w, "  error: %s\n", f.Error)
	}
	if len(f.Folders) == 0 {
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  FOLDER\tSCANNED\tIMPORTED\tNEW\tAFTER CUT-OFF\tJOURNALED\tPRESENT\tDELETED\tWAITING\t")
	for _, ff := range f.Folders {
		c := ff.Counts
		fmt.Fprintf(tw, "  %s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%s\n", ff.SourceName, c.Scanned, c.Imported, c.New,
			c.AfterCutoff, c.Journaled, c.Present, c.Deleted, c.Waiting, ff.Error)
	}
	_ = tw.Flush()
}

func nonNilStrings(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}
