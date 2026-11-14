package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"time"
	_ "time/tzdata" // the export time zone on any system

	"github.com/spf13/cobra"

	"github.com/keepia/archive/internal/archive"
	"github.com/keepia/archive/internal/attest"
	"github.com/keepia/archive/internal/auth"
	"github.com/keepia/archive/internal/blobcrypt"
	"github.com/keepia/archive/internal/store"
)

// exitGroupMoved: the export is complete as of its snapshot, but a
// selected user or account changed customer group after it (#394).
const exitGroupMoved = 3

// gobdMemoryLimit is the Go heap's soft limit for a GoBD export, unless
// GOMEMLIMIT sets one: the export streams (one message at a time, about
// 300 B per entry of a ZIP until its end, at most 100,000 missing
// messages), so its live heap stays within the plan's 64 MiB; the limit
// makes the garbage collector keep the process near that on the 4 GB
// target, next to PostgreSQL and the web server.
const gobdMemoryLimit = 256 << 20

// defaultExportTimeZone is the export time zone without --time-zone.
const defaultExportTimeZone = "Europe/Berlin"

// gobdFlags are the flags of export --format gobd.
type gobdFlags struct {
	out                               string
	users, accounts, folders          []string
	hold, grant                       int64
	group                             string
	withoutGroup                      bool
	since, until, timeZone            string
	withChain, withLeaves, resume     bool
	noAttest                          bool
	requestedBy                       string
	groupGiven, holdGiven, grantGiven bool
}

// gobdTestHook lets tests change the options right before the export
// runs (a clock, a blocking progress callback).
var gobdTestHook func(*archive.GoBDOptions)

const gobdHelp = `With --format gobd, write a GoBD export (format v1) of one customer
group: the original .eml files, messages.csv, locations.csv and missing.csv
with index.xml, SHA256SUMS and the signed STATEMENT.txt, as a directory or,
when --out ends in .zip, as a ZIP (at most 200,000 messages). Check it with
"export verify". Select with --user (repeatable), --account [OWNER/]NAME
(repeatable; --folder, repeatable, only with exactly one --account),
--hold ID (a legal hold that names a user, an account or a group), --grant
ID (the scope of an auditor grant) or --group NAME alone (every user of the
group). --since and --until bound the date of the messages (a plain date is
a day in --time-zone, default Europe/Berlin; --until includes that day).

One export covers one customer group: a selection whose users and accounts
are not all in one group (or all without one) is refused. With two or more
groups, --group NAME or --without-group is required and checked. Everything
is read in one database snapshot; a user or account that changes group
afterwards is reported, and the command exits 3.

An attestation is written first if messages came since the newest one
(--no-attest skips it); STATEMENT.txt is signed with the attestation key.
--with-chain ships the whole attestation chain, --with-leaves the leaves
files. A directory export is written into OUT.partial and renamed when
complete; --resume continues an interrupted one (same options).
--requested-by records who asked for the export in the audit log.

The export is not encrypted. With Docker Compose write it to /export
(EXPORT_DIR on the host), outside the data directory.

Exit codes: 0 complete, 1 messages missing (listed on stderr) or an error,
2 a usage error or a selection that is refused, 3 complete, but a selected
user or account changed group after the snapshot.`

func addGoBDFlags(cmd *cobra.Command, f *gobdFlags) {
	cmd.Flags().Int64Var(&f.hold, "hold", 0, "gobd: the locations this legal hold covers")
	cmd.Flags().Int64Var(&f.grant, "grant", 0, "gobd: the scope of this auditor grant")
	cmd.Flags().StringVar(&f.group, "group", "", "gobd: the customer group (alone: every user of it)")
	cmd.Flags().BoolVar(&f.withoutGroup, "without-group", false, "gobd: the accounts have no owner and so no group")
	cmd.Flags().StringVar(&f.since, "since", "", "gobd: messages from this date (YYYY-MM-DD or RFC 3339)")
	cmd.Flags().StringVar(&f.until, "until", "", "gobd: messages up to this date (a plain date includes the day)")
	cmd.Flags().StringVar(&f.timeZone, "time-zone", defaultExportTimeZone, "gobd: the time zone of dates and folders")
	cmd.Flags().BoolVar(&f.withChain, "with-chain", false, "gobd: include the whole attestation chain")
	cmd.Flags().BoolVar(&f.withLeaves, "with-leaves", false, "gobd: include the leaves files of the attestations")
	cmd.Flags().BoolVar(&f.resume, "resume", false, "gobd: continue the interrupted export in OUT.partial")
	cmd.Flags().BoolVar(&f.noAttest, "no-attest", false, "gobd: do not write an attestation first")
	cmd.Flags().StringVar(&f.requestedBy, "requested-by", "", "gobd: who asked for the export (for the audit log)")
}

// gobdOnlyFlags are refused with mbox and maildir.
var gobdOnlyFlags = []string{"hold", "grant", "group", "without-group", "since", "until", "time-zone", "with-chain",
	"with-leaves", "resume", "no-attest", "requested-by"}

// gobdError is a refusal of a GoBD export: printed, exit code 2.
func gobdError(cmd *cobra.Command, err error) error { return usageError(cmd, err) }

func runExportGoBD(cmd *cobra.Command, f *gobdFlags) error {
	f.groupGiven = cmd.Flags().Changed("group")
	f.holdGiven = cmd.Flags().Changed("hold")
	f.grantGiven = cmd.Flags().Changed("grant")
	if err := checkGoBDFlags(f); err != nil {
		return gobdError(cmd, err)
	}
	if os.Getenv("GOMEMLIMIT") == "" {
		defer debug.SetMemoryLimit(debug.SetMemoryLimit(gobdMemoryLimit))
	}
	ctx := cmd.Context()
	a, err := openApp(ctx)
	if err != nil {
		return err
	}
	defer a.close()
	opts, text, err := resolveGoBD(cmd, a, f)
	if err != nil {
		return gobdError(cmd, err)
	}
	// The start-up rule of encryption at rest for readers, as for mbox and
	// maildir: with encryption on or being changed, the archive key must
	// open the keyring, checked before the audit entry.
	blobs, ar, err := openArchiveBlobs(ctx, a, "export", archive.RoleReader)
	if err != nil {
		return err
	}
	defer ar.Close()
	opts.AttestDir = a.cfg.AttestDir()
	if attestKeyExists(a.cfg) {
		if opts.Signer, err = loadAttestKey(a.cfg); err != nil {
			return err
		}
	}
	opts.Software, opts.Warn = version, cmd.ErrOrStderr()
	if gobdTestHook != nil {
		gobdTestHook(&opts)
	}

	// The check before the audit entry: a refused selection writes nothing.
	group, owners, err := archive.GoBDCheck(ctx, a.store, a.cfg.DataDir, opts)
	if err != nil {
		return gobdRefusal(cmd, err)
	}
	warnPlaintext(cmd.ErrOrStderr(), a.cfg.DataDir, opts.Out)
	// A grant's export is in its owner's Log and in the admin's view of
	// the grant (#393, part 4), as the auditor's reads are: both entries
	// carry the owner in via_owners. A grant's owner never changes
	// (auditor_grants_same_group), so this read agrees with the check's.
	var viaOwners []int64
	if opts.Selection.GrantID != 0 {
		owner, err := a.store.GrantOwner(ctx, opts.Selection.GrantID)
		if err != nil {
			return err
		}
		viaOwners = []int64{owner}
	}

	e := cliTargeted("data.export")
	gobdTarget(e, opts, group, owners, cmd, a)
	e.Detail("format", "gobd")
	audit := text
	audit.Group, audit.WithoutGroup = "", false // details never name a group
	e.Detail("selection", audit.JSON())
	e.Detail("out", opts.Out)
	e.Detail("resume", opts.Resume)
	if f.requestedBy != "" {
		e.Detail("requested_by", f.requestedBy)
	}
	if opts.Selection.GrantID != 0 {
		e.Detail(store.DetailAuditorGrant, opts.Selection.GrantID)
		e.Detail(store.DetailViaOwners, viaOwners)
	}
	if err := auditNow(cmd, a, e); err != nil {
		return err
	}

	res, runErr := archive.GoBDExport(ctx, a.store, blobs, opts)
	done := cliTargeted("data.export_done")
	done.TargetType, done.TargetID, done.TargetName = e.TargetType, e.TargetID, e.TargetName
	done.Detail("format", "gobd")
	done.Detail("out", opts.Out)
	if opts.Selection.GrantID != 0 {
		done.Detail(store.DetailAuditorGrant, opts.Selection.GrantID)
		done.Detail(store.DetailViaOwners, viaOwners)
	}
	doneGroup := group
	if runErr == nil {
		doneGroup = res.Group
		done.Detail("id", res.Statement.ID)
		done.Detail("messages", res.Statement.Messages)
		done.Detail("locations", res.Statement.Locations)
		done.Detail("missing", res.Statement.Missing)
		done.Detail("bytes", res.Statement.Bytes)
		done.Detail("manifest_sha256", res.SumsSHA256)
		done.Detail("statement_sha256", res.StatementSHA256)
		done.Detail("complete", res.Statement.Complete)
		if len(res.Moved) > 0 {
			names := make([]string, len(res.Moved))
			for i, m := range res.Moved {
				names[i] = m.Name
			}
			done.Detail("group_moved_after_snapshot", names)
		}
	} else {
		done.Detail("aborted", abortReason(runErr))
	}
	setAuditGroup(done, doneGroup)
	// Recorded also when the command was interrupted.
	if err := a.store.Audit(context.WithoutCancel(ctx), *done); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: the end of the export is not in the audit log: %v\n", err)
	}
	if runErr != nil {
		return gobdRefusal(cmd, runErr)
	}
	return reportGoBD(cmd, opts, res)
}

// gobdRefusal maps the export's errors to exit codes: refusals 2, the rest
// 1.
func gobdRefusal(cmd *cobra.Command, err error) error {
	for _, e := range []error{archive.ErrSelectionSpansGroups, archive.ErrGroupClaimMismatch, archive.ErrGroupChanged, archive.ErrGoBDSelection} {
		if errors.Is(err, e) {
			return gobdError(cmd, err)
		}
	}
	return err
}

// abortReason is the aborted detail of data.export_done: a fixed text for
// the group refusals, which never names a group, else the error.
func abortReason(err error) string {
	switch {
	case errors.Is(err, archive.ErrSelectionSpansGroups):
		return "selection spans groups"
	case errors.Is(err, archive.ErrGroupClaimMismatch):
		return "group claim differs"
	case errors.Is(err, archive.ErrGroupChanged):
		return "group changed since the export started"
	case errors.Is(err, context.Canceled):
		return "interrupted"
	}
	return err.Error()
}

func reportGoBD(cmd *cobra.Command, opts archive.GoBDOptions, res *archive.GoBDResult) error {
	out, stderr := cmd.OutOrStdout(), cmd.ErrOrStderr()
	st := res.Statement
	switch {
	case res.Attested > 0:
		fmt.Fprintf(out, "attestation #%d written before the export\n", res.Attested)
	case res.Waited:
		fmt.Fprintln(out, "waited for the attestation being written")
	}
	label := "no customer group"
	if res.Group != nil {
		label = fmt.Sprintf("customer group %s (id %d)", terminalSafe(res.Group.Name), res.Group.ID)
	}
	fmt.Fprintf(out, "exported %d message(s), %d location(s), %d bytes of %s to %s\n", st.Messages, st.Locations, st.Bytes, label, opts.Out)
	fmt.Fprintf(out, "export %s, SHA256SUMS %s\n", st.ID, res.SumsSHA256)
	if st.Sig == "" {
		fmt.Fprintln(out, "STATEMENT.txt is not signed: there is no attestation key")
	}
	for _, m := range res.Missing {
		fmt.Fprintf(stderr, "missing %s: %s\n", m.SHA256, m.Reason)
	}
	for _, m := range res.Moved {
		fmt.Fprintf(stderr, "warning: %s moved from %s to %s after the snapshot\n", terminalSafe(m.Name), terminalSafe(m.From), terminalSafe(m.To))
	}
	if len(res.Moved) > 0 {
		fmt.Fprintln(stderr, "the export is complete as of the snapshot; check with the customer before handing it over")
	}
	if !st.Complete {
		return &exitError{msg: "messages are missing", code: exitFindings}
	}
	if len(res.Moved) > 0 {
		return &exitError{msg: "a selected user or account changed group", code: exitGroupMoved}
	}
	return nil
}

// warnPlaintext warns when the export lands inside the data directory
// while encryption at rest is on: the export itself is plaintext.
func warnPlaintext(w io.Writer, dataDir, out string) {
	if _, err := os.Stat(blobcrypt.KeyringPath(dataDir)); err != nil {
		return
	}
	abs, err := filepath.Abs(out)
	if err != nil {
		return
	}
	data, err := filepath.Abs(dataDir)
	if err != nil {
		return
	}
	if rel, err := filepath.Rel(data, abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		fmt.Fprintf(w, "warning: the export is not encrypted, and %s is inside the data directory, which encryption at rest protects; "+
			"write it outside (with Docker Compose to /export)\n", out)
	}
}

// setAuditGroup records the entry under the export's group, set
// explicitly: with only the target, the group would be derived when the
// entry is written, which after a move is another customer's. An export
// without a group is recorded without one, whatever its accounts' owner is
// by then.
func setAuditGroup(e *store.AuditEntry, g *attest.StatementGroup) {
	if g == nil {
		e.GroupID, e.NoGroup = nil, true
		return
	}
	id := g.ID
	e.GroupID, e.NoGroup = &id, false
}

// gobdTarget sets what data.export is about: the one owner, or the one
// account of a selection without users; several owners have no single
// target and are listed in the details. Its group is the check's.
func gobdTarget(e *store.AuditEntry, opts archive.GoBDOptions, group *attest.StatementGroup, owners []string, cmd *cobra.Command, a *app) {
	sel := opts.Selection
	oneAccount := len(sel.Owners) == 0 && len(sel.Accounts) == 1 && sel.HoldID == 0 && sel.GrantID == 0 && !sel.WholeGroup
	switch {
	case oneAccount:
		for id := range sel.Accounts {
			if acc, err := a.store.GetAccount(cmd.Context(), id); err == nil {
				cliAccountTarget(e, acc)
			}
		}
	case len(owners) == 1:
		if u, err := a.store.GetUserByName(cmd.Context(), owners[0]); err == nil {
			cliUserTarget(e, u)
		}
	}
	if len(owners) > 1 {
		e.Detail("owners", owners)
	}
	setAuditGroup(e, group)
}

// resolveGoBD turns the flags into the export's options: names into IDs,
// dates into instants in the time zone, and the selection's canonical
// text.
func resolveGoBD(cmd *cobra.Command, a *app, f *gobdFlags) (archive.GoBDOptions, archive.GoBDSelectionText, error) {
	ctx := cmd.Context()
	var opts archive.GoBDOptions
	text := archive.GoBDSelectionText{TimeZone: f.timeZone}
	loc, err := time.LoadLocation(f.timeZone)
	if err != nil || f.timeZone == "" || f.timeZone == "Local" {
		return opts, text, fmt.Errorf("--time-zone: unknown time zone %q", f.timeZone)
	}
	sel := &opts.Selection
	sel.TimeZone = f.timeZone
	for _, name := range f.users {
		u, err := a.store.GetUserByName(ctx, auth.NormalizeUserName(name))
		if errors.Is(err, store.ErrNotFound) {
			return opts, text, fmt.Errorf("user %q not found", name)
		}
		if err != nil {
			return opts, text, err
		}
		if !slices.Contains(sel.Owners, u.ID) {
			sel.Owners = append(sel.Owners, u.ID)
			text.Users = append(text.Users, u.Name)
		}
	}
	for _, key := range f.accounts {
		acc, label, err := findGoBDAccount(cmd, a, key)
		if err != nil {
			return opts, text, err
		}
		if sel.Accounts == nil {
			sel.Accounts = map[int64][]int64{}
		}
		sel.Accounts[acc.ID] = nil
		text.Accounts = append(text.Accounts, label)
		if len(f.folders) > 0 {
			var ids []int64
			var names []string
			for _, name := range f.folders {
				fs, err := a.store.ListExportFolders(ctx, acc.OwnerID, []int64{acc.ID}, name)
				if err != nil {
					return opts, text, err
				}
				if len(fs) == 0 {
					return opts, text, fmt.Errorf("account %s has no folder %q", label, name)
				}
				for _, fo := range fs {
					ids = append(ids, fo.ID)
				}
				names = append(names, name)
			}
			slices.Sort(names)
			sel.Accounts[acc.ID] = ids
			text.Folders = map[string][]string{label: slices.Compact(names)}
		}
	}
	slices.Sort(text.Users)
	slices.Sort(text.Accounts)
	sel.HoldID, sel.GrantID = f.hold, f.grant
	text.Hold, text.Grant = f.hold, f.grant

	groups, err := a.store.ListGroups(ctx)
	if err != nil {
		return opts, text, err
	}
	switch {
	case f.groupGiven:
		g, err := a.store.GetGroupByName(ctx, f.group)
		if errors.Is(err, store.ErrUnknownGroup) {
			return opts, text, fmt.Errorf("group %q not found", f.group)
		}
		if err != nil {
			return opts, text, err
		}
		id := g.ID
		sel.Group = archive.GroupClaim{Given: true, ID: &id, Name: g.Name}
		text.Group = g.Name
		explicit := len(f.users) > 0 || len(f.accounts) > 0 || f.holdGiven || f.grantGiven
		sel.WholeGroup, text.WholeGroup = !explicit, !explicit
	case f.withoutGroup:
		sel.Group = archive.GroupClaim{Given: true}
		text.WithoutGroup = true
	case len(groups) > 1:
		return opts, text, errors.New("this instance has several customer groups: name the customer with --group or --without-group")
	}
	if f.since != "" {
		t, err := parseExportDate(f.since, loc, false)
		if err != nil {
			return opts, text, fmt.Errorf("--since: %w", err)
		}
		sel.Since, text.Since = &t, f.since
	}
	if f.until != "" {
		t, err := parseExportDate(f.until, loc, true)
		if err != nil {
			return opts, text, fmt.Errorf("--until: %w", err)
		}
		sel.Until, text.Until = &t, f.until
	}
	if sel.Since != nil && sel.Until != nil && !sel.Until.After(*sel.Since) {
		return opts, text, errors.New("--until must be after --since")
	}
	sel.Text = text.JSON()
	opts.Out, opts.Resume = f.out, f.resume
	opts.WithChain, opts.WithLeaves, opts.NoAttest = f.withChain, f.withLeaves, f.noAttest
	return opts, text, nil
}

// parseExportDate reads YYYY-MM-DD as the start of that day in loc (as an
// end: the start of the next day) or RFC 3339.
func parseExportDate(v string, loc *time.Location, end bool) (time.Time, error) {
	if t, err := time.ParseInLocation(time.DateOnly, v, loc); err == nil {
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

// findGoBDAccount resolves --account: OWNER/NAME, or a NAME only one
// account has (also one without an owner). It returns the account and
// its label in the selection (owner/name, or name).
func findGoBDAccount(cmd *cobra.Command, a *app, key string) (*store.Account, string, error) {
	ctx := cmd.Context()
	if owner, name, ok := strings.Cut(key, "/"); ok {
		u, err := a.store.GetUserByName(ctx, auth.NormalizeUserName(owner))
		if errors.Is(err, store.ErrNotFound) {
			return nil, "", fmt.Errorf("user %q not found", owner)
		}
		if err != nil {
			return nil, "", err
		}
		acc, err := a.store.GetOwnedAccount(ctx, u.ID, name)
		if errors.Is(err, store.ErrNotFound) {
			return nil, "", fmt.Errorf("user %q has no account %q", u.Name, name)
		}
		if err != nil {
			return nil, "", err
		}
		return acc, u.Name + "/" + acc.Name, nil
	}
	accounts, err := a.store.ListAccountsByName(ctx, key)
	if err != nil {
		return nil, "", err
	}
	switch len(accounts) {
	case 0:
		return nil, "", fmt.Errorf("account %q not found", key)
	case 1:
	default:
		return nil, "", fmt.Errorf("several users have an account %q; name it as OWNER/%s", key, key)
	}
	acc := accounts[0]
	if acc.OwnerID == nil {
		return acc, acc.Name, nil
	}
	names, err := userNames(cmd, a)
	if err != nil {
		return nil, "", err
	}
	return acc, ownerName(names, acc.OwnerID) + "/" + acc.Name, nil
}

func checkGoBDFlags(f *gobdFlags) error {
	if f.out == "" {
		return errors.New("--out is required")
	}
	if f.groupGiven && f.withoutGroup {
		return errors.New("--group and --without-group exclude each other")
	}
	explicit := len(f.users) > 0 || len(f.accounts) > 0
	kinds := btoi(explicit) + btoi(f.holdGiven) + btoi(f.grantGiven)
	switch {
	case kinds > 1:
		return errors.New("choose one of --user/--account, --hold and --grant")
	case kinds == 0 && f.withoutGroup:
		return errors.New("name the accounts with --account")
	case kinds == 0 && !f.groupGiven:
		return errors.New("choose what to export with --user, --account, --hold, --grant or --group")
	}
	if len(f.folders) > 0 && len(f.accounts) != 1 {
		return errors.New("--folder needs exactly one --account")
	}
	if f.holdGiven && f.hold <= 0 || f.grantGiven && f.grant <= 0 {
		return errors.New("--hold and --grant take an id")
	}
	return nil
}
