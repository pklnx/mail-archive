package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/imapsync"
	"github.com/pklnx/mail-archive/internal/store"
)

type app struct {
	cfg   *config.Config
	store *store.Store
}

func openApp(ctx context.Context) (*app, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	return &app{cfg: cfg, store: st}, nil
}

func (a *app) close() { a.store.Close() }

func newMigrateCmd() *cobra.Command {
	runUp := func(cmd *cobra.Command, _ []string) error {
		a, err := openApp(cmd.Context())
		if err != nil {
			return err
		}
		defer a.close()
		applied, err := a.store.Migrate(cmd.Context())
		if err != nil {
			return err
		}
		if len(applied) == 0 {
			fmt.Println("database schema is up to date")
		}
		for _, m := range applied {
			fmt.Println("applied", m)
		}
		return nil
	}
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Manage database schema migrations (default: apply all pending)",
		Args:  cobra.NoArgs,
		RunE:  runUp,
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "up",
			Short: "Apply all pending migrations",
			Args:  cobra.NoArgs,
			RunE:  runUp,
		},
		newMigrateDownCmd(),
		newMigrateStatusCmd(),
	)
	return cmd
}

func newMigrateDownCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "down",
		Short: "Roll back the most recent migration (may delete data)",
		Long: `Roll back the most recently applied migration.

Rolling back usually drops tables or columns and the data in them. Archived
.eml files on disk are not touched. Back up the database first. Intended for
development; in production, prefer restoring a backup.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !yes {
				return errors.New("refusing to roll back without --yes (this may delete data)")
			}
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			rolledBack, err := a.store.MigrateDown(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Println("rolled back", rolledBack)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm that data may be deleted")
	return cmd
}

func newMigrateStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show applied and pending migrations",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			statuses, err := a.store.MigrationStatus(cmd.Context())
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "MIGRATION\tSTATE\tAPPLIED AT")
			for _, st := range statuses {
				state, at := "pending", "-"
				if st.Applied {
					state, at = "applied", st.AppliedAt.Local().Format(time.DateTime)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", st.Path, state, at)
			}
			return w.Flush()
		},
	}
}

func newKeygenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "keygen",
		Short: "Generate a random secret key for " + config.EnvSecretKey,
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			key, err := crypto.GenerateKey()
			if err != nil {
				return err
			}
			fmt.Println(key)
			return nil
		},
	}
}

func newAccountCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "account",
		Short: "Manage IMAP accounts",
	}
	cmd.AddCommand(
		newAccountAddCmd(),
		newAccountListCmd(),
		newAccountFoldersCmd(),
		newAccountSetFoldersCmd(),
		newAccountSetPasswordCmd(),
		newAccountEnableCmd(true),
		newAccountEnableCmd(false),
		newAccountRemoveCmd(),
	)
	return cmd
}

func newAccountAddCmd() *cobra.Command {
	var (
		acc           store.Account
		tlsMode       string
		passwordStdin bool
		skipCheck     bool
	)
	cmd := &cobra.Command{
		Use:   "add NAME",
		Short: "Add an IMAP account",
		Long: `Add an IMAP account. The password is read from the terminal (or from stdin
with --password-stdin) and stored encrypted with ` + config.EnvSecretKey + `.
The login is verified before the account is saved unless --skip-check is set.`,
		Example: `  mail-archive account add private --host imap.mail.de --username me@mail.de
  mail-archive account add gmail --host imap.gmail.com --username me@gmail.com \
      --include "[Gmail]/All Mail" --include "[Gmail]/Sent Mail"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			acc.Name = args[0]
			acc.TLSMode = store.TLSMode(tlsMode)
			acc.Enabled = true
			switch acc.TLSMode {
			case store.TLSModeTLS, store.TLSModeSTARTTLS, store.TLSModeNone:
			default:
				return fmt.Errorf("invalid --tls %q (use tls, starttls or none)", tlsMode)
			}
			if acc.Port == 0 {
				acc.Port = 993
				if acc.TLSMode != store.TLSModeTLS {
					acc.Port = 143
				}
			}
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			sealer, err := a.cfg.Sealer()
			if err != nil {
				return err
			}
			password, err := readPassword(passwordStdin, "IMAP password for "+acc.Username+": ")
			if err != nil {
				return err
			}
			if !skipCheck {
				fmt.Fprintf(os.Stderr, "checking login at %s:%d ...\n", acc.Host, acc.Port)
				n, err := checkLogin(cmd.Context(), &acc, password)
				if err != nil {
					return fmt.Errorf("login check failed (use --skip-check to save anyway): %w", err)
				}
				fmt.Fprintf(os.Stderr, "login ok, %d folders found\n", n)
			}
			acc.PasswordEnc, err = sealer.Seal([]byte(password), archive.PasswordContext(acc.Name))
			if err != nil {
				return err
			}
			if err := a.store.CreateAccount(cmd.Context(), &acc); err != nil {
				return err
			}
			fmt.Printf("account %q added\n", acc.Name)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&acc.Host, "host", "", "IMAP server host name (required)")
	f.IntVar(&acc.Port, "port", 0, "IMAP port (default 993 for tls, 143 otherwise)")
	f.StringVar(&tlsMode, "tls", "tls", "connection security: tls, starttls or none")
	f.StringVar(&acc.Username, "username", "", "IMAP user name (required)")
	f.StringArrayVar(&acc.IncludedFolders, "include", nil, "only archive these folders (repeatable; default: all)")
	f.StringArrayVar(&acc.ExcludedFolders, "exclude", nil, "skip these folders (repeatable)")
	f.BoolVar(&passwordStdin, "password-stdin", false, "read the password from stdin")
	f.BoolVar(&skipCheck, "skip-check", false, "do not verify the login before saving")
	_ = cmd.MarkFlagRequired("host")
	_ = cmd.MarkFlagRequired("username")
	return cmd
}

func checkLogin(ctx context.Context, acc *store.Account, password string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	conn, err := imapsync.Dial(ctx, imapConfig(acc, password))
	if err != nil {
		return 0, err
	}
	defer func() { _ = conn.Close() }()
	folders, err := conn.ListFolders()
	return len(folders), err
}

func imapConfig(acc *store.Account, password string) imapsync.Config {
	return imapsync.Config{
		Host: acc.Host, Port: acc.Port, TLSMode: string(acc.TLSMode),
		Username: acc.Username, Password: password,
	}
}

func readPassword(fromStdin bool, prompt string) (string, error) {
	if fromStdin || !term.IsTerminal(int(os.Stdin.Fd())) { //nolint:gosec // fd fits in int
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", fmt.Errorf("read password from stdin: %w", err)
		}
		pw := strings.TrimRight(line, "\r\n")
		if pw == "" {
			return "", errors.New("empty password")
		}
		return pw, nil
	}
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(int(os.Stdin.Fd())) //nolint:gosec // fd fits in int
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if len(b) == 0 {
		return "", errors.New("empty password")
	}
	return string(b), nil
}

func newAccountListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured accounts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			accounts, err := a.store.ListAccounts(cmd.Context())
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tSERVER\tUSER\tENABLED\tINCLUDE\tEXCLUDE")
			for _, acc := range accounts {
				fmt.Fprintf(w, "%s\t%s:%d (%s)\t%s\t%v\t%s\t%s\n", acc.Name, acc.Host, acc.Port, acc.TLSMode,
					acc.Username, acc.Enabled, listOrDash(acc.IncludedFolders, "all"), listOrDash(acc.ExcludedFolders, "-"))
			}
			return w.Flush()
		},
	}
}

func listOrDash(l []string, empty string) string {
	if len(l) == 0 {
		return empty
	}
	return strings.Join(l, ", ")
}

func loadAccount(cmd *cobra.Command, name string) (*app, *store.Account, error) {
	a, err := openApp(cmd.Context())
	if err != nil {
		return nil, nil, err
	}
	acc, err := a.store.GetAccountByName(cmd.Context(), name)
	if err != nil {
		a.close()
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, fmt.Errorf("account %q not found", name)
		}
		return nil, nil, err
	}
	return a, acc, nil
}

func newAccountFoldersCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "folders NAME",
		Short: "Connect to the server and show which folders would be archived",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, acc, err := loadAccount(cmd, args[0])
			if err != nil {
				return err
			}
			defer a.close()
			sealer, err := a.cfg.Sealer()
			if err != nil {
				return err
			}
			pw, err := sealer.Open(acc.PasswordEnc, archive.PasswordContext(acc.Name))
			if err != nil {
				return err
			}
			conn, err := imapsync.Dial(cmd.Context(), imapConfig(acc, string(pw)))
			if err != nil {
				return err
			}
			defer func() { _ = conn.Close() }()
			folders, err := conn.ListFolders()
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ARCHIVE\tFOLDER\tATTRIBUTES")
			for _, f := range folders {
				mark := "no"
				if archive.FolderSelected(f.Name, acc.IncludedFolders, acc.ExcludedFolders) {
					mark = "yes"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", mark, f.Name, strings.Join(f.Attrs, " "))
			}
			return w.Flush()
		},
	}
}

func newAccountSetFoldersCmd() *cobra.Command {
	var included, excluded []string
	cmd := &cobra.Command{
		Use:   "set-folders NAME",
		Short: "Replace the include/exclude folder lists (no flags = archive all folders)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, acc, err := loadAccount(cmd, args[0])
			if err != nil {
				return err
			}
			defer a.close()
			if err := a.store.SetFolderFilters(cmd.Context(), acc.ID, included, excluded); err != nil {
				return err
			}
			fmt.Printf("folder filters of %q updated\n", acc.Name)
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&included, "include", nil, "only archive these folders (repeatable)")
	cmd.Flags().StringArrayVar(&excluded, "exclude", nil, "skip these folders (repeatable)")
	return cmd
}

func newAccountSetPasswordCmd() *cobra.Command {
	var passwordStdin bool
	cmd := &cobra.Command{
		Use:   "set-password NAME",
		Short: "Replace the stored IMAP password",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, acc, err := loadAccount(cmd, args[0])
			if err != nil {
				return err
			}
			defer a.close()
			sealer, err := a.cfg.Sealer()
			if err != nil {
				return err
			}
			pw, err := readPassword(passwordStdin, "new IMAP password for "+acc.Username+": ")
			if err != nil {
				return err
			}
			enc, err := sealer.Seal([]byte(pw), archive.PasswordContext(acc.Name))
			if err != nil {
				return err
			}
			if err := a.store.UpdatePassword(cmd.Context(), acc.ID, enc); err != nil {
				return err
			}
			fmt.Printf("password of %q updated\n", acc.Name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the password from stdin")
	return cmd
}

func newAccountEnableCmd(enable bool) *cobra.Command {
	use, short := "disable NAME", "Exclude an account from sync (archived data is kept)"
	if enable {
		use, short = "enable NAME", "Include an account in sync"
	}
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, acc, err := loadAccount(cmd, args[0])
			if err != nil {
				return err
			}
			defer a.close()
			return a.store.SetAccountEnabled(cmd.Context(), acc.ID, enable)
		},
	}
}

func newAccountRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove NAME",
		Short: "Remove an account that has no archived messages yet",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, acc, err := loadAccount(cmd, args[0])
			if err != nil {
				return err
			}
			defer a.close()
			if err := a.store.DeleteAccount(cmd.Context(), acc.ID); err != nil {
				return err
			}
			fmt.Printf("account %q removed\n", acc.Name)
			return nil
		},
	}
}

func newSyncCmd() *cobra.Command {
	var only []string
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Copy new messages from all enabled accounts",
		Long: `Copy new messages from all enabled accounts into the archive.

The server is never modified: folders are opened read-only and messages are
fetched without setting the \Seen flag. Messages deleted on the server stay in
the archive. Run this periodically (cron, systemd timer).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			sealer, err := a.cfg.Sealer()
			if err != nil {
				return err
			}
			blobs, err := blobstore.New(a.cfg.DataDir)
			if err != nil {
				return err
			}
			syncer := &archive.Syncer{
				Store: a.store, Blobs: blobs, Sealer: sealer,
				Logger: newLogger(a.cfg.LogLevel),
			}
			results, err := syncer.SyncAll(cmd.Context(), only)
			failed := 0
			for _, r := range results {
				status := "ok"
				if r.Err != nil {
					status = "error: " + r.Err.Error()
					failed++
				}
				fmt.Printf("%-20s fetched=%-6d new=%-6d %s\n", r.Account, r.Fetched, r.New, status)
			}
			if err != nil {
				return err
			}
			if len(only) > 0 && len(results) == 0 {
				return fmt.Errorf("no account matches %v", only)
			}
			if failed > 0 {
				return &exitError{fmt.Sprintf("%d account(s) failed", failed)}
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&only, "account", nil, "only sync these accounts (repeatable; also syncs disabled ones)")
	return cmd
}

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show archive statistics and the last sync per account",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			stats, unique, err := a.store.Stats(cmd.Context())
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ACCOUNT\tENABLED\tFOLDERS\tMESSAGES\tLAST SYNC\tSTATUS")
			for _, s := range stats {
				last, status := "never", "-"
				if s.LastRunAt != nil {
					last = s.LastRunAt.Local().Format(time.DateTime)
				}
				if s.LastStatus != nil {
					status = *s.LastStatus
				}
				fmt.Fprintf(w, "%s\t%v\t%d\t%d\t%s\t%s\n", s.Account, s.Enabled, s.Folders, s.Locations, last, status)
			}
			if err := w.Flush(); err != nil {
				return err
			}
			fmt.Printf("\nunique messages in archive: %d\n", unique)
			return nil
		},
	}
}
