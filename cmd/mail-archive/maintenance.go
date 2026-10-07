package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/spf13/cobra"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/config"
)

// Exit codes of verify.
const (
	exitFindings   = 1
	exitIncomplete = 2
)

func newVerifyCmd() *cobra.Command {
	var opts archive.VerifyOptions
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Check that every archived message file is present and intact",
		Long: `Check the whole archive. Every message in the database is read from disk
and hashed again, which reports missing and corrupt files with the users,
accounts and folders they belong to. Then every file in the data directory's
messages/ and tmp/ is checked against the database, which reports orphans
(files without a database row), files left by an interrupted sync, and
anything that does not belong there.

verify never changes a database row or an archived file. With --fix-orphans
it moves orphans and leftover temporary files to orphans/<time>/ in the data
directory; delete that directory yourself once you are sure.

The orphan check waits up to --lock-timeout while a sync writes files, then
is skipped. Exit codes: 0 nothing found, 1 findings, 2 the check is
incomplete (orphan check skipped, unreadable files, or another error).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			code, err := runVerify(cmd.Context(), opts, cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "error:", err)
				return &exitError{msg: err.Error(), code: exitIncomplete}
			}
			if code != 0 {
				return &exitError{msg: "verify found problems", code: code}
			}
			return nil
		},
	}
	// Usage errors mean the check did not run either.
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		fmt.Fprintln(c.ErrOrStderr(), "error:", err)
		return &exitError{msg: err.Error(), code: exitIncomplete}
	})
	cmd.Flags().IntVar(&opts.Jobs, "jobs", 4, "files hashed in parallel (1 to 16)")
	cmd.Flags().DurationVar(&opts.LockTimeout, "lock-timeout", time.Minute, "how long to wait for running syncs before the orphan check")
	cmd.Flags().BoolVar(&opts.FixOrphans, "fix-orphans", false, "move orphans and leftover temporary files to orphans/<time>/ in the data directory")
	return cmd
}

func runVerify(ctx context.Context, opts archive.VerifyOptions, stdout, stderr io.Writer) (int, error) {
	if opts.Jobs < 1 || opts.Jobs > 16 {
		return 0, fmt.Errorf("--jobs must be between 1 and 16, got %d", opts.Jobs)
	}
	if opts.LockTimeout <= 0 {
		return 0, errors.New("--lock-timeout must be positive")
	}
	a, err := openApp(ctx)
	if err != nil {
		return 0, err
	}
	defer a.close()
	blobs, err := blobstore.New(a.cfg.DataDir)
	if err != nil {
		return 0, err
	}
	opts.Logger = newLogger(a.cfg.LogLevel)
	opts.OnFinding = func(f archive.Finding) { fmt.Fprintln(stdout, formatFinding(f)) }
	opts.Progress = func(n int) { fmt.Fprintf(stderr, "checked %d messages\n", n) }
	res, err := archive.Verify(ctx, a.store, blobs, opts)
	if err != nil {
		return 0, err
	}
	fmt.Fprintln(stdout, verifySummary(res))
	if res.OrphansSkipped {
		fmt.Fprintf(stdout, "orphan check skipped: a sync was writing files for longer than %s; run verify again later\n", opts.LockTimeout)
		if opts.FixOrphans {
			fmt.Fprintln(stdout, "--fix-orphans moved nothing")
		}
	}
	if res.Moved > 0 {
		fmt.Fprintf(stdout, "moved %d file(s), %d bytes, to %s in the data directory\n", res.Moved, res.MovedBytes, res.MovedTo)
	}
	for _, d := range res.OldOrphanDirs {
		fmt.Fprintf(stdout, "note: %s holds files moved by an earlier --fix-orphans; delete it once you are sure\n", d)
	}
	switch {
	case !res.Complete():
		return exitIncomplete, nil
	case res.Problems()-res.Moved > 0:
		return exitFindings, nil
	}
	return 0, nil
}

const maxPlaces = 5

func formatFinding(f archive.Finding) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-10s %s", f.Kind, f.Path)
	if f.Detail != "" {
		fmt.Fprintf(&b, " (%s)", f.Detail)
	}
	for i, p := range f.Places {
		if i == maxPlaces {
			fmt.Fprintf(&b, "\n           and %d more place(s)", len(f.Places)-maxPlaces)
			break
		}
		owner := p.Owner
		if owner == "" {
			owner = "-"
		}
		fmt.Fprintf(&b, "\n           in %s/%s/%s UID %d", owner, p.Account, p.Folder, p.UID)
	}
	return b.String()
}

func verifySummary(res *archive.VerifyResult) string {
	parts := []string{}
	for _, k := range []string{archive.FindingMissing, archive.FindingCorrupt, archive.FindingBadPath, archive.FindingOrphan,
		archive.FindingStaleTemp, archive.FindingUnexpected, archive.FindingIOError} {
		if n := res.Findings[k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, k))
		}
	}
	if len(parts) == 0 {
		parts = []string{"no problems"}
	}
	return fmt.Sprintf("checked %d message(s): %s", res.Checked, strings.Join(parts, ", "))
}

func newExportCmd() *cobra.Command {
	var format, out, account, folder string
	cmd := &cobra.Command{
		Use:   "export --format mbox|maildir --out DIR (--user USER | --account NAME [--folder NAME])",
		Short: "Write archived mail as mbox files or Maildirs for a mail client",
		Long: `Write the archived mail of one user, one account or one folder to DIR,
as one mbox file (mboxrd) or one Maildir per folder:
DIR/<account>/<folder>.mbox or DIR/<account>/<folder>/{cur,new,tmp}.

Every message is written once per folder, byte for byte. DIR must not exist;
it appears when the export is complete. The files are not encrypted and are
readable only by the user running the command.

One export never mixes users: give --user, or --account (with --user when
several users have an account with that name). Removed accounts are included.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if account == "" && cmd.Flag("user").Value.String() == "" {
				return errors.New("choose what to export with --user or --account")
			}
			if folder != "" && account == "" {
				return errors.New("--folder needs --account")
			}
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			opts, label, err := exportSelection(cmd, a, account)
			if err != nil {
				return err
			}
			blobs, err := blobstore.New(a.cfg.DataDir)
			if err != nil {
				return err
			}
			log := newLogger(a.cfg.LogLevel)
			opts.Format, opts.Out, opts.Folder, opts.Logger = format, out, folder, log
			res, err := archive.Export(cmd.Context(), a.store, blobs, opts)
			if err != nil {
				return err
			}
			log.Info("export finished", "user", label, "account", account, "folder", folder, "format", format,
				"folders", res.Folders, "messages", res.Messages, "bytes", res.Bytes, "skipped", len(res.Skipped))
			fmt.Printf("exported %d message(s) in %d folder(s), %d bytes, to %s\n", res.Messages, res.Folders, res.Bytes, out)
			if len(res.Skipped) > 0 {
				for _, s := range res.Skipped {
					fmt.Fprintf(os.Stderr, "skipped %s in %s/%s: %s\n", s.SHA256, s.Account, s.Folder, s.Reason)
				}
				return &exitError{msg: "some messages were skipped", code: 1}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&format, "format", "", "mbox or maildir (required)")
	cmd.Flags().StringVar(&out, "out", "", "the directory to create (required; with Docker Compose under /data)")
	cmd.Flags().String("user", "", "export this user's accounts, or pick the owner of --account")
	cmd.Flags().StringVar(&account, "account", "", "only this account")
	cmd.Flags().StringVar(&folder, "folder", "", "only this folder of --account")
	_ = cmd.MarkFlagRequired("format")
	_ = cmd.MarkFlagRequired("out")
	return cmd
}

// exportSelection resolves --user and --account to an owner and accounts.
// It returns the owner's name for the log.
func exportSelection(cmd *cobra.Command, a *app, account string) (archive.ExportOptions, string, error) {
	var opts archive.ExportOptions
	if account != "" {
		acc, err := findAccount(cmd, a, account)
		if err != nil {
			return opts, "", err
		}
		opts.Owner, opts.AccountIDs = acc.OwnerID, []int64{acc.ID}
		names, err := userNames(cmd, a)
		if err != nil {
			return opts, "", err
		}
		return opts, ownerName(names, acc.OwnerID), nil
	}
	u, err := flagUser(cmd, a)
	if err != nil {
		return opts, "", err
	}
	accounts, err := a.store.ListOwnedAccounts(cmd.Context(), u.ID)
	if err != nil {
		return opts, "", err
	}
	if len(accounts) == 0 {
		return opts, "", fmt.Errorf("user %q has no accounts", u.Name)
	}
	opts.Owner = &u.ID
	for _, acc := range accounts {
		opts.AccountIDs = append(opts.AccountIDs, acc.ID)
	}
	return opts, u.Name, nil
}

func newBackupCmd() *cobra.Command {
	var noteOnly bool
	cmd := &cobra.Command{
		Use:   "backup DIR",
		Short: "Dump the database into DIR, next to a note on what else to back up",
		Long: `Dump the database with pg_dump (custom format) into
DIR/mailarchive-<time>.dump and write DIR/BACKUP-NOTE.txt, which says what
else belongs to a complete backup and how to restore it.

A complete backup is the dump, then a copy of the data directory, in this
order, and ` + config.EnvSecretKey + `, kept separately. The note never
contains the key.

Needs pg_dump in PATH, at least the server's major version. The Docker image
has none: with Docker Compose use "./ma backup DIR", which runs pg_dump in
the database container and writes DIR on the host.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if noteOnly {
				return cobra.MaximumNArgs(1)(cmd, args)
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			now := time.Now().UTC()
			name := "mailarchive-" + now.Format("20060102T150405Z") + ".dump"
			if noteOnly {
				// For ./ma backup: the dump is written by the database
				// container, the note by this command.
				if len(args) == 1 {
					name = args[0]
				}
				note, err := backupNote(cmd.Context(), a, name, now)
				if err != nil {
					return err
				}
				fmt.Print(note)
				return nil
			}
			pgDump, err := exec.LookPath("pg_dump")
			if err != nil {
				return errors.New("pg_dump not found; install the PostgreSQL client tools, or with Docker Compose use ./ma backup DIR")
			}
			major, err := a.store.ServerMajorVersion(cmd.Context())
			if err != nil {
				return err
			}
			note, err := backupNote(cmd.Context(), a, name, now)
			if err != nil {
				return err
			}
			path, err := runBackup(cmd.Context(), backupJob{
				PgDump: pgDump, DatabaseURL: a.cfg.DatabaseURL, Dir: args[0], Name: name,
				ServerMajor: major, Note: note,
			})
			if err != nil {
				return err
			}
			fmt.Printf("wrote %s and %s\n", path, filepath.Join(args[0], backupNoteName))
			fmt.Printf("now copy the data directory (%s), and keep %s separately\n", a.cfg.DataDir, config.EnvSecretKey)
			return nil
		},
	}
	cmd.Flags().BoolVar(&noteOnly, "note-only", false, "only print the backup note for the given dump name (used by ./ma backup)")
	_ = cmd.Flags().MarkHidden("note-only")
	return cmd
}

const backupNoteName = "BACKUP-NOTE.txt"

type backupJob struct {
	PgDump      string
	DatabaseURL string
	Dir         string
	Name        string // file name of the dump
	ServerMajor int
	Note        string
}

// runBackup checks the pg_dump version, dumps into Dir/Name.partial and
// renames it when pg_dump succeeds, then writes the note. The connection is
// passed in the environment, so the password is not in pg_dump's arguments.
func runBackup(ctx context.Context, job backupJob) (string, error) {
	env, err := pgEnv(job.DatabaseURL)
	if err != nil {
		return "", err
	}
	version, err := exec.CommandContext(ctx, job.PgDump, "--version").Output() //nolint:gosec // pg_dump from PATH
	if err != nil {
		return "", fmt.Errorf("pg_dump --version: %w", err)
	}
	major, err := pgDumpMajor(string(version))
	if err != nil {
		return "", err
	}
	if major < job.ServerMajor {
		return "", fmt.Errorf("pg_dump is version %d, the server %d: install pg_dump %d or newer", major, job.ServerMajor, job.ServerMajor)
	}
	if err := os.MkdirAll(job.Dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(job.Dir, job.Name)
	partial := path + ".partial"
	f, err := os.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // the backup directory given by the operator
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
			_ = os.Remove(partial)
		}
	}()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, job.PgDump, "--format=custom", "--no-password") //nolint:gosec // pg_dump from PATH
	cmd.Env, cmd.Stdout, cmd.Stderr = env, f, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("pg_dump: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(partial, path); err != nil {
		return "", err
	}
	ok = true
	return path, writeFileAtomic(filepath.Join(job.Dir, backupNoteName), []byte(job.Note))
}

// pgEnv turns a database URL into libpq environment variables, replacing
// any PG* variables of this process.
func pgEnv(databaseURL string) ([]string, error) {
	cfg, err := pgconn.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", config.EnvDatabaseURL, err)
	}
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "PG") {
			env = append(env, kv)
		}
	}
	env = append(env,
		"PGHOST="+cfg.Host,
		"PGPORT="+strconv.Itoa(int(cfg.Port)),
		"PGUSER="+cfg.User,
		"PGDATABASE="+cfg.Database,
	)
	if cfg.Password != "" {
		env = append(env, "PGPASSWORD="+cfg.Password)
	}
	for key, value := range sslSettings(databaseURL) {
		env = append(env, key+"="+value)
	}
	return env, nil
}

// sslSettings reads the TLS parameters of a URL or key=value connection
// string as libpq variables.
func sslSettings(databaseURL string) map[string]string {
	names := map[string]string{
		"sslmode": "PGSSLMODE", "sslrootcert": "PGSSLROOTCERT", "sslcert": "PGSSLCERT", "sslkey": "PGSSLKEY",
	}
	out := map[string]string{}
	if strings.HasPrefix(databaseURL, "postgres://") || strings.HasPrefix(databaseURL, "postgresql://") {
		u, err := url.Parse(databaseURL)
		if err != nil {
			return out
		}
		for k, v := range u.Query() {
			if name, ok := names[k]; ok && len(v) > 0 {
				out[name] = v[0]
			}
		}
		return out
	}
	for _, field := range strings.Fields(databaseURL) {
		k, v, ok := strings.Cut(field, "=")
		if name, known := names[k]; ok && known {
			out[name] = strings.Trim(v, "'")
		}
	}
	return out
}

var pgDumpVersion = regexp.MustCompile(`\(PostgreSQL\) (\d+)`)

func pgDumpMajor(version string) (int, error) {
	m := pgDumpVersion.FindStringSubmatch(version)
	if m == nil {
		return 0, fmt.Errorf("cannot read the pg_dump version from %q", strings.TrimSpace(version))
	}
	return strconv.Atoi(m[1])
}

// backupNote is the text of BACKUP-NOTE.txt. It never contains the key.
func backupNote(ctx context.Context, a *app, dumpName string, now time.Time) (string, error) {
	statuses, err := a.store.MigrationStatus(ctx)
	if err != nil {
		return "", err
	}
	migration := "none"
	for _, s := range statuses {
		if s.Applied {
			migration = filepath.Base(s.Path)
		}
	}
	dataDir := a.cfg.DataDir
	if abs, err := filepath.Abs(dataDir); err == nil {
		dataDir = abs
	}
	compose := commandName() == "./ma"
	if compose {
		dataDir = "the directory mounted at /data (ARCHIVE_DIR in .env, by default ./data next to docker-compose.yml)"
	}
	return formatBackupNote(backupNoteData{
		Time: now, Version: version, Migration: migration, Dump: dumpName, DataDir: dataDir, Compose: compose,
	}), nil
}

type backupNoteData struct {
	Time      time.Time
	Version   string
	Migration string
	Dump      string
	DataDir   string
	Compose   bool
}

func formatBackupNote(d backupNoteData) string {
	var b strings.Builder
	fmt.Fprintf(&b, `mail-archive backup

Database dump: %s
Written:       %s
App version:   %s
Last migration: %s

A complete backup has three parts:
1. this database dump,
2. a copy of the data directory, made after the dump:
   %s
3. %s, kept separately (password manager). It is not
   in this note or in the dump. Without it the stored IMAP passwords and
   two-factor secrets cannot be read.

Restore:
`, d.Dump, d.Time.UTC().Format(time.RFC3339), d.Version, d.Migration, d.DataDir, config.EnvSecretKey)
	if d.Compose {
		fmt.Fprintf(&b, `1. Put the key into .env, copy the data directory back, start PostgreSQL:
   docker compose up -d --wait postgres
2. Load the dump:
   docker compose exec -T postgres pg_restore --clean --if-exists --no-owner -U mailarchive -d mailarchive < %s
`, d.Dump)
	} else {
		fmt.Fprintf(&b, `1. Set the key, copy the data directory back, create an empty database.
2. Load the dump:
   pg_restore --clean --if-exists --no-owner -d "$DATABASE_URL" %s
`, d.Dump)
	}
	b.WriteString(`3. Run "migrate", then "sync", then "verify". Files newer than the dump
   show up as orphans; sync adopts those still on the server. Move the rest
   aside with "verify --fix-orphans" only after checking them.
`)
	return b.String()
}

// writeFileAtomic writes a file with mode 0600 through a temporary file.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
