package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/spf13/cobra"

	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/quota"
	"github.com/pklnx/mail-archive/internal/store"
)

// Storage quotas (#397, part 2): the instance quota and the disk reserve
// come from the environment, group quotas from the database.

// exitPaused is the exit code of a sync, import or fill that the storage
// gate paused (EX_TEMPFAIL): cron wrappers can tell a pause from a
// failure.
const exitPaused = 75

// newStorageGate returns this process's storage gate and makes every save
// of the store tell it what it stored.
func newStorageGate(a *app, log *slog.Logger) *quota.Gate {
	s := a.cfg.Storage
	dir := a.cfg.DataDir
	g := quota.New(quota.Config{
		InstanceQuota: s.Quota, MinFreeDisk: s.MinFreeDisk,
		Thresholds: quota.Thresholds{WarnPercent: s.WarnPercent, OverflowPercent: s.OverflowPercent},
	}, a.store, func() (int64, error) { return quota.FreeSpace(dir) }, nil, log)
	a.store.SetWriteObserver(g.Wrote)
	return g
}

// warnStorageConfig logs at start what the storage settings mean, and
// warns about group quotas above the instance quota: the instance's
// applies first.
func warnStorageConfig(ctx context.Context, a *app, log *slog.Logger) {
	s := a.cfg.Storage
	attrs := []any{"min_free_disk_bytes", s.MinFreeDisk, "warn_percent", s.WarnPercent, "smtp_overflow_percent", s.OverflowPercent}
	if s.Quota > 0 {
		attrs = append(attrs, "instance_quota_bytes", s.Quota)
	}
	log.Info("storage limits", attrs...)
	if s.Quota <= 0 {
		return
	}
	groups, err := a.store.GroupsAboveQuota(ctx, s.Quota)
	if err != nil {
		log.Warn("cannot check the group quotas against the instance quota", "err", err)
		return
	}
	for _, g := range groups {
		log.Warn("a group quota is above the instance quota ("+config.EnvStorageQuota+"); the instance quota applies first",
			"group", g.Name, "group_id", g.ID, "group_quota_bytes", g.Quota, "instance_quota_bytes", s.Quota)
	}
}

// pausedMessage says which limit paused a command, for the operator:
// "storage quota of group NAME reached".
func pausedMessage(ctx context.Context, a *app, err error) string {
	var fe *quota.FullError
	if !errors.As(err, &fe) {
		return err.Error()
	}
	switch fe.Scope {
	case quota.ScopeDisk:
		return fmt.Sprintf("disk reserve reached (less than %s free; %s)", humanBytes(a.cfg.Storage.MinFreeDisk), config.EnvMinFreeDisk)
	case quota.ScopeInstance:
		return fmt.Sprintf("storage quota of the instance reached (%s)", config.EnvStorageQuota)
	}
	name := "?"
	if fe.Group != nil {
		if g, gerr := a.store.GetGroup(ctx, *fe.Group); gerr == nil {
			name = g.Name
		}
	}
	return fmt.Sprintf("storage quota of group %s reached", name)
}

// pausedError is the error of a command the gate paused: exit code 75.
func pausedError(ctx context.Context, a *app, err error, stored int, what string) error {
	msg := pausedMessage(ctx, a, err)
	if stored >= 0 {
		msg += fmt.Sprintf(" after %d messages; run the same command again once space is freed", stored)
	} else {
		msg += "; " + what + " once space is freed"
	}
	return &exitError{msg: msg, code: exitPaused}
}

func newGroupSetQuotaCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set-quota NAME SIZE|none",
		Short: "Set or remove a customer group's storage quota",
		Long: `Set a customer group's storage quota in message bytes, deduplicated within the
group: the size of each unique message its accounts hold, counted once.
SIZE is a whole number with a unit, like 50GiB, 500GB or 2TiB (KiB, MiB,
GiB, TiB: powers of 1024; KB, MB, GB, TB: powers of 1000). "none" removes
the quota: then only the instance quota (` + config.EnvStorageQuota + `) limits
the group.

A group quota cannot be above the instance quota. Group quotas may add up to
more than the instance quota. From ` + fmt.Sprint(config.DefaultStorageWarnPercent) + ` % (` + config.EnvStorageWarnPercent + `)
the group is in "warn"; at 100 % its syncs, fills and imports pause; from
100 % plus ` + config.EnvStorageSMTPOverflowPercent + ` the SMTP server defers its journal mail
with 452. The change takes effect within 10 seconds in every process. It is
recorded in the audit log as group.quota.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var size *int64
			if !strings.EqualFold(strings.TrimSpace(args[1]), "none") {
				n, err := config.ParseByteSize(args[1])
				if err != nil || n <= 0 {
					return fmt.Errorf("invalid size %q: want a size like 50GiB or 2TiB, or none", args[1])
				}
				size = &n
			}
			a, err := openApp(cmd.Context())
			if err != nil {
				return err
			}
			defer a.close()
			if limit := a.cfg.Storage.Quota; size != nil && limit > 0 && *size > limit {
				return fmt.Errorf("%s is above the instance quota of %s (%s); a group quota cannot be larger",
					humanBytes(*size), humanBytes(limit), config.EnvStorageQuota)
			}
			g, err := findGroup(cmd, a, args[0])
			if err != nil {
				return err
			}
			c, err := a.store.SetGroupQuota(withCLIAudit(cmd, cliTargeted("group.quota")), g.ID, size)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			switch {
			case !c.Changed:
				fmt.Fprintf(out, "group %q already has the quota %s\n", c.Name, quotaText(size))
			default:
				fmt.Fprintf(out, "group %q: quota %s (was %s)\n", c.Name, quotaText(size), quotaText(c.From))
			}
			return nil
		},
	}
}

// quotaText shows a quota: its size, or "none".
func quotaText(q *int64) string {
	if q == nil {
		return "none"
	}
	return humanBytes(*q)
}

// groupStorage maps group IDs to their usage figure and state, for group
// list; groups without a snapshot yet have no figure.
func groupStorage(ctx context.Context, a *app) (map[int64]store.GroupUsage, error) {
	u, err := a.store.StorageUsage(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]store.GroupUsage, len(u.Groups))
	for _, g := range u.Groups {
		out[g.ID] = g
	}
	return out, nil
}
