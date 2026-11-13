package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/pklnx/mail-archive/internal/store"
)

// withoutGroup is the row of the accounts without an owner.
const withoutGroup = "Without group"

func newUsageCmd() *cobra.Command {
	var (
		asJSON bool
		group  string
	)
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Show the storage used by the instance, each customer group and each account",
		Long: `Show the storage used, in message bytes: the size of each unique message,
counted once however many folders and accounts hold it.

The first line is the instance: exact at all times. Then one row per
customer group (its messages and bytes, deduplicated within the group: a
message in two groups counts fully in each), its quota and its state, and a
row "` + withoutGroup + `" for accounts without an owner (they exist only before
the first user). Then one row per account with its owner and group: its
messages and bytes, and in EXCLUSIVE the bytes no other account holds (what
purging it would free).

Accounts and groups are counted by a snapshot that a background job takes
every hour, and soon after a user or account moves to another group, a purge
or deletion run ends, or an import or restore ends (serve or worker must
run). Between snapshots, a group's figure adds what was archived since, and
deletions show at the next snapshot: it is an upper bound, with two
exceptions until the next snapshot. Right after a user or account moves
between groups, the groups show the moved bytes only from the snapshot the
move requests. And a message whose only other copy in the group is deleted
while it is being added is missing, one message per such race. A snapshot
that a job pool has not taken yet shows "-".

--group NAME shows that group and its accounts only. --json prints the same
as JSON, for scripts on the host. Names are shown: this is the admin's
shell.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			u, err := a.store.StorageUsage(cmd.Context())
			if err != nil {
				return err
			}
			if group != "" {
				if u, err = onlyGroup(u, group); err != nil {
					return err
				}
			}
			if asJSON {
				return writeUsageJSON(cmd.OutOrStdout(), u)
			}
			return writeUsageText(cmd.OutOrStdout(), u)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	cmd.Flags().StringVar(&group, "group", "", "show only this customer group and its accounts")
	return cmd
}

// onlyGroup keeps the group named name (case does not matter, as for group
// names) and its accounts.
func onlyGroup(u *store.StorageUsage, name string) (*store.StorageUsage, error) {
	var keep *store.GroupUsage
	for i := range u.Groups {
		if strings.EqualFold(u.Groups[i].Name, strings.TrimSpace(name)) {
			keep = &u.Groups[i]
		}
	}
	if keep == nil {
		return nil, fmt.Errorf("no group named %q", name)
	}
	out := *u
	out.Groups = []store.GroupUsage{*keep}
	out.Ungrouped = store.Usage{}
	out.Accounts = nil
	for _, acc := range u.Accounts {
		if acc.GroupID != nil && *acc.GroupID == keep.ID {
			out.Accounts = append(out.Accounts, acc)
		}
	}
	return &out, nil
}

// hasUngrouped reports whether the "Without group" row is shown: while an
// account has no owner, or the last snapshot counted such accounts.
func hasUngrouped(u *store.StorageUsage) bool {
	if u.Ungrouped.Messages > 0 {
		return true
	}
	for _, a := range u.Accounts {
		if a.OwnerID == nil {
			return true
		}
	}
	return false
}

func usageGroupNames(u *store.StorageUsage) map[int64]string {
	names := make(map[int64]string, len(u.Groups))
	for _, g := range u.Groups {
		names[g.ID] = g.Name
	}
	return names
}

// snapshotLine says when groups and accounts were counted.
func snapshotLine(at *time.Time) string {
	if at == nil {
		return "groups and accounts: not counted yet (the background jobs of serve or worker take the first snapshot)"
	}
	return "groups and accounts counted at " + at.Local().Format(time.DateTime) + "; groups include what was archived since"
}

func writeUsageText(out io.Writer, u *store.StorageUsage) error {
	fmt.Fprintf(out, "instance: %d messages, %s (exact), state %s\n", u.Messages, humanBytes(u.Bytes), u.State)
	fmt.Fprintln(out, snapshotLine(u.SnapshotAt))
	if u.CounterDrift != 0 {
		fmt.Fprintf(out, "warning: the last snapshot found the counters off by %d bytes; see the log of serve or worker\n", u.CounterDrift)
	}
	if sum, n := quotaSum(u.Groups); n > 0 {
		fmt.Fprintf(out, "group quotas: %d set, %s in total\n", n, humanBytes(sum))
	}
	fmt.Fprintln(out)
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "GROUP\tMESSAGES\tSIZE\tQUOTA\tSTATE")
	for _, g := range u.Groups {
		quota := "-"
		if g.QuotaBytes != nil {
			quota = humanBytes(*g.QuotaBytes)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", g.Name, countOr(g.SnapshotAt, g.Messages), sizeOr(g.SnapshotAt, g.Bytes), quota, g.State)
	}
	if hasUngrouped(u) {
		fmt.Fprintf(w, "%s\t%s\t%s\t-\t-\n", withoutGroup, countOr(u.SnapshotAt, u.Ungrouped.Messages), sizeOr(u.SnapshotAt, u.Ungrouped.Bytes))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(out)
	names := usageGroupNames(u)
	w = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ACCOUNT\tKIND\tOWNER\tGROUP\tMESSAGES\tSIZE\tEXCLUSIVE")
	for _, acc := range u.Accounts {
		owner, group := "-", withoutGroup
		if acc.OwnerID != nil {
			owner = acc.Owner
		}
		if acc.GroupID != nil {
			group = names[*acc.GroupID]
		}
		name := acc.Name
		if acc.Removed {
			name += " (removed)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", name, acc.Kind, owner, group,
			countOr(acc.SnapshotAt, acc.Messages), sizeOr(acc.SnapshotAt, acc.Bytes), sizeOr(acc.SnapshotAt, acc.ExclusiveBytes))
	}
	return w.Flush()
}

// countOr and sizeOr show "-" for a figure no snapshot has counted yet.
func countOr(at *time.Time, n int64) string {
	if at == nil {
		return "-"
	}
	return fmt.Sprint(n)
}

func sizeOr(at *time.Time, n int64) string {
	if at == nil {
		return "-"
	}
	return humanBytes(n)
}

func quotaSum(groups []store.GroupUsage) (sum int64, n int) {
	for _, g := range groups {
		if g.QuotaBytes != nil {
			sum += *g.QuotaBytes
			n++
		}
	}
	return sum, n
}

type usageJSON struct {
	Messages      int64            `json:"messages"`
	Bytes         int64            `json:"bytes"`
	State         string           `json:"state"`
	SnapshotAt    *time.Time       `json:"snapshotAt"`
	CounterDrift  int64            `json:"counterDriftBytes"`
	DatabaseBytes int64            `json:"databaseBytes"`
	RelationBytes map[string]int64 `json:"relationBytes"`
	Groups        []groupUsageJSON `json:"groups"`
	// WithoutGroup is set while accounts without an owner exist or the
	// snapshot counted some.
	WithoutGroup *countJSON         `json:"withoutGroup,omitempty"`
	Accounts     []accountUsageJSON `json:"accounts"`
}

type countJSON struct {
	Messages int64 `json:"messages"`
	Bytes    int64 `json:"bytes"`
}

type groupUsageJSON struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Messages   int64      `json:"messages"`
	Bytes      int64      `json:"bytes"`
	QuotaBytes *int64     `json:"quotaBytes"`
	State      string     `json:"state"`
	SnapshotAt *time.Time `json:"snapshotAt"`
}

type accountUsageJSON struct {
	ID             int64      `json:"id"`
	Name           string     `json:"name"`
	Kind           string     `json:"kind"`
	Removed        bool       `json:"removed"`
	Owner          *string    `json:"owner"`
	Group          *string    `json:"group"`
	Messages       int64      `json:"messages"`
	Bytes          int64      `json:"bytes"`
	ExclusiveBytes int64      `json:"exclusiveBytes"`
	SnapshotAt     *time.Time `json:"snapshotAt"`
}

func writeUsageJSON(out io.Writer, u *store.StorageUsage) error {
	v := usageJSON{
		Messages: u.Messages, Bytes: u.Bytes, State: u.State, SnapshotAt: u.SnapshotAt, CounterDrift: u.CounterDrift,
		DatabaseBytes: u.DatabaseBytes, RelationBytes: u.RelationBytes,
		Groups: []groupUsageJSON{}, Accounts: []accountUsageJSON{},
	}
	if v.RelationBytes == nil {
		v.RelationBytes = map[string]int64{}
	}
	for _, g := range u.Groups {
		v.Groups = append(v.Groups, groupUsageJSON{ID: g.ID, Name: g.Name, Messages: g.Messages, Bytes: g.Bytes,
			QuotaBytes: g.QuotaBytes, State: g.State, SnapshotAt: g.SnapshotAt})
	}
	if hasUngrouped(u) {
		v.WithoutGroup = &countJSON{Messages: u.Ungrouped.Messages, Bytes: u.Ungrouped.Bytes}
	}
	names := usageGroupNames(u)
	for _, acc := range u.Accounts {
		a := accountUsageJSON{ID: acc.ID, Name: acc.Name, Kind: string(acc.Kind), Removed: acc.Removed,
			Messages: acc.Messages, Bytes: acc.Bytes, ExclusiveBytes: acc.ExclusiveBytes, SnapshotAt: acc.SnapshotAt}
		if acc.OwnerID != nil {
			a.Owner = &acc.Owner
		}
		if acc.GroupID != nil {
			name := names[*acc.GroupID]
			a.Group = &name
		}
		v.Accounts = append(v.Accounts, a)
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// storageStatus is the storage line of `status`.
func storageStatus(ctx context.Context, a *app) (string, error) {
	messages, bytes, err := a.store.StorageTotals(ctx)
	if state, ok := store.SQLState(err); ok && state == "42P01" {
		return "storage: unknown until `" + commandName() + " migrate` has run", nil
	}
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("storage: %d unique messages, %s; per group and account: %s usage", messages, humanBytes(bytes), commandName()), nil
}
