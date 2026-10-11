package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/store"
)

// cliEntry starts an audit entry of a command. Commands have no user, IP or
// user agent; the operating system user and MAIL_ARCHIVE_OPERATOR (set by
// ./ma to the host user) say who ran it. Neither is verified: whoever runs
// the command holds the database password.
func cliEntry(action string) *store.AuditEntry {
	e := &store.AuditEntry{Actor: store.ActorCLI, Action: action}
	if u, err := user.Current(); err == nil && u.Username != "" {
		e.Detail("os_user", u.Username)
	} else {
		e.Detail("os_user", strconv.Itoa(os.Getuid()))
	}
	if op := strings.TrimSpace(os.Getenv(config.EnvOperator)); op != "" {
		e.Detail("operator", op)
	}
	return e
}

// withCLIAudit returns cmd's context with e to be written by the next store
// transaction, together with the change it describes.
func withCLIAudit(cmd *cobra.Command, e *store.AuditEntry) context.Context {
	return store.WithAudit(cmd.Context(), e)
}

// auditNow writes e before a command reads or writes mail. A command that
// cannot record what it does does not do it.
func auditNow(cmd *cobra.Command, a *app, e *store.AuditEntry) error {
	if err := a.store.Audit(cmd.Context(), *e); err != nil {
		return fmt.Errorf("%w; nothing was done", err)
	}
	return nil
}

func cliUserTarget(e *store.AuditEntry, u *store.User) *store.AuditEntry {
	e.SetTarget(store.TargetUser, strconv.FormatInt(u.ID, 10), u.Name)
	return e
}

func cliAccountTarget(e *store.AuditEntry, acc *store.Account) *store.AuditEntry {
	e.SetTarget(store.TargetAccount, strconv.FormatInt(acc.ID, 10), acc.Name)
	return e
}

// accountChangeFields names what a change sets, never the values.
func accountChangeFields(c store.AccountChange) []string {
	var out []string
	if c.Name != nil {
		out = append(out, "name")
	}
	if c.Connection != nil {
		out = append(out, "host", "port", "tls", "username", "password")
	} else if c.PasswordEnc != nil {
		out = append(out, "password")
	}
	if c.Folders != nil {
		out = append(out, "included_folders", "excluded_folders")
	}
	if c.Enabled != nil {
		out = append(out, "enabled")
	}
	return out
}

// auditMonthsAhead is how many months of audit log partitions every command
// keeps ready beyond the current one, so that installs without serve get
// them too.
const auditMonthsAhead = 1

func ensureAuditPartitions(ctx context.Context, st *store.Store) {
	if _, err := st.EnsureAuditPartitions(ctx, auditMonthsAhead); err != nil && !errors.Is(err, store.ErrNotMigrated) {
		fmt.Fprintln(os.Stderr, "warning: cannot create audit log partitions:", err)
	}
}

func newAuditCmd() *cobra.Command {
	var (
		since, until, userName, format, out string
		actions                             []string
	)
	cmd := &cobra.Command{
		Use:   "audit --since DATE --format csv|jsonl [--out FILE]",
		Short: "Write the audit log of a period as CSV or JSON lines",
		Long: `Write the audit log entries of a period, oldest first, to stdout or FILE.

DATE is YYYY-MM-DD (midnight UTC) or RFC 3339. --until is exclusive; a plain
date includes that day. --action takes a group (login, search, read,
export, accounts, users, profile, api_keys, sync) or an action such as
message.open, and is repeatable. --user selects the entries of a current
user.

The export itself is recorded as audit.export. FILE must not exist; it is
written with mode 0600 and appears when it is complete. In CSV, a cell that
starts with =, +, -, @, a tab or a carriage return gets a leading ' so that
spreadsheets do not run it as a formula.`,
		Example: `  mail-archive audit --since 2026-01-01 --format csv --out /data/audit-2026.csv
  mail-archive audit --since 2026-10-01 --until 2026-10-31 --user alice --action read --format jsonl`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if format != "csv" && format != "jsonl" {
				return fmt.Errorf("--format must be csv or jsonl, got %q", format)
			}
			from, err := parseAuditDate(since, false)
			if err != nil {
				return fmt.Errorf("--since: %w", err)
			}
			to := time.Now().Add(time.Minute)
			if until != "" {
				if to, err = parseAuditDate(until, true); err != nil {
					return fmt.Errorf("--until: %w", err)
				}
			}
			if !from.Before(to) {
				return errors.New("--since must be before --until")
			}
			if _, err := store.AuditActionPatterns(actions); err != nil {
				return fmt.Errorf("--action: %w", err)
			}
			if out != "" {
				if _, err := os.Lstat(out); err == nil {
					return fmt.Errorf("%s exists; choose a new file", out)
				} else if !errors.Is(err, fs.ErrNotExist) {
					return err
				}
			}
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			f := store.AuditFilter{Since: from, Until: to, Actions: actions}
			e := cliEntry("audit.export")
			e.Detail("format", format)
			e.Detail("since", from.UTC().Format(time.RFC3339))
			e.Detail("until", to.UTC().Format(time.RFC3339))
			if len(actions) > 0 {
				e.Detail("actions", actions)
			}
			if out != "" {
				e.Detail("out", out)
			}
			if userName != "" {
				u, err := a.store.GetUserByName(cmd.Context(), auth.NormalizeUserName(userName))
				if errors.Is(err, store.ErrNotFound) {
					return fmt.Errorf("user %q not found", auth.NormalizeUserName(userName))
				}
				if err != nil {
					return err
				}
				f.UserID = &u.ID
				cliUserTarget(e, u)
			}
			if err := auditNow(cmd, a, e); err != nil {
				return err
			}
			if out == "" {
				return writeAudit(cmd.Context(), a.store, f, format, os.Stdout)
			}
			return writeNewFile(out, func(w io.Writer) error {
				return writeAudit(cmd.Context(), a.store, f, format, w)
			})
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "the first day (required)")
	cmd.Flags().StringVar(&until, "until", "", "the end, exclusive; a plain date includes that day (default: now)")
	cmd.Flags().StringVar(&userName, "user", "", "only this user's entries")
	cmd.Flags().StringArrayVar(&actions, "action", nil, "only these actions or groups (repeatable)")
	cmd.Flags().StringVar(&format, "format", "", "csv or jsonl (required)")
	cmd.Flags().StringVar(&out, "out", "", "write to this new file instead of stdout")
	_ = cmd.MarkFlagRequired("since")
	_ = cmd.MarkFlagRequired("format")
	return cmd
}

// parseAuditDate parses YYYY-MM-DD (midnight UTC) or RFC 3339. As an end, a
// plain date means the end of that day.
func parseAuditDate(v string, end bool) (time.Time, error) {
	if t, err := time.Parse(time.DateOnly, v); err == nil {
		if end {
			t = t.AddDate(0, 0, 1)
		}
		return t, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("want YYYY-MM-DD or RFC 3339, got %q", v)
	}
	return t, nil
}

// auditJSON is one entry in JSON lines, with the fields of the web API.
type auditJSON struct {
	ID        int64            `json:"id"`
	At        time.Time        `json:"at"`
	Actor     string           `json:"actor"`
	UserID    *int64           `json:"userId"`
	User      string           `json:"user,omitempty"`
	APIKey    string           `json:"apiKey,omitempty"` // the name of the API key
	Action    string           `json:"action"`
	Target    *auditTargetJSON `json:"target"`
	ClientIP  string           `json:"clientIp,omitempty"`
	UserAgent string           `json:"userAgent,omitempty"`
	Details   json.RawMessage  `json:"details"`
}

type auditTargetJSON struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

var auditCSVHeader = []string{"at", "actor", "user_id", "user_name", "api_key_name", "action",
	"target_type", "target_id", "target_name", "client_ip", "user_agent", "details"}

func writeAudit(ctx context.Context, st *store.Store, f store.AuditFilter, format string, w io.Writer) error {
	if format == "jsonl" {
		enc := json.NewEncoder(w)
		return st.ExportAudit(ctx, f, func(r store.AuditRecord) error {
			j := auditJSON{
				ID: r.ID, At: r.At.UTC(), Actor: r.Actor, UserID: r.UserID, User: r.UserName, APIKey: r.APIKeyName,
				Action: r.Action, ClientIP: r.ClientIP, UserAgent: r.UserAgent, Details: r.Details,
			}
			if r.TargetType != "" {
				j.Target = &auditTargetJSON{Type: r.TargetType, ID: r.TargetID, Name: r.TargetName}
			}
			return enc.Encode(j) //nolint:gosec // apiKey is the key's name, never the key
		})
	}
	cw := csv.NewWriter(w)
	if err := cw.Write(auditCSVHeader); err != nil {
		return err
	}
	err := st.ExportAudit(ctx, f, func(r store.AuditRecord) error {
		userID := ""
		if r.UserID != nil {
			userID = strconv.FormatInt(*r.UserID, 10)
		}
		row := []string{r.At.UTC().Format(time.RFC3339Nano), r.Actor, userID, r.UserName, r.APIKeyName, r.Action,
			r.TargetType, r.TargetID, r.TargetName, r.ClientIP, r.UserAgent, string(r.Details)}
		for i, c := range row {
			row[i] = csvSafe(c)
		}
		return cw.Write(row)
	})
	if err != nil {
		return err
	}
	cw.Flush()
	return cw.Error()
}

// csvSafe keeps a spreadsheet from reading a cell as a formula.
func csvSafe(c string) string {
	if c != "" && strings.ContainsRune("=+-@\t\r", rune(c[0])) {
		return "'" + c
	}
	return c
}

// writeNewFile writes path, which must not exist, through a temporary file
// in the same directory with mode 0600, and renames it when write succeeded.
func writeNewFile(path string, write func(io.Writer) error) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.partial")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if err = write(tmp); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if _, err = os.Lstat(path); err == nil {
		return fmt.Errorf("%s appeared meanwhile; not overwritten", path)
	}
	return os.Rename(tmp.Name(), path)
}
