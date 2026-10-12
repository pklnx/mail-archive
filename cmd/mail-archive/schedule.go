package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/store"
)

// scheduleOf is the web server's sync schedule from the configuration.
func scheduleOf(cfg *config.Config) archive.Schedule {
	return archive.Schedule{Sync: cfg.SyncInterval, SyncMin: cfg.SyncIntervalMin}
}

func newAccountSetScheduleCmd() *cobra.Command {
	var interval string
	cmd := &cobra.Command{
		Use:   "set-schedule NAME --sync-interval V",
		Short: "Set how often the web server syncs an account",
		Long: `Set the account's own sync interval for the web server's schedule:
"default" (MAIL_ARCHIVE_SYNC_INTERVAL), "manual" (not on the schedule) or a
duration of whole minutes from 5m to 720h, such as 30m or 1h30m.

The server's floor, MAIL_ARCHIVE_SYNC_INTERVAL_MIN, also applies: a
shorter value is refused, and a value stored before the floor was raised
is synced at the floor.

"manual" only takes the account off the schedule: the Sync button, "sync
--account NAME" and a plain "sync" (for example from cron) still sync it.
To stop syncing it altogether, disable it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			own, err := archive.ParseOwnInterval(interval)
			if err != nil {
				return fmt.Errorf("--sync-interval: %s", archive.Schedule{}.OwnIntervalRule())
			}
			a, acc, err := loadIMAPAccount(cmd, args[0])
			if err != nil {
				return err
			}
			defer a.close()
			sched := scheduleOf(a.cfg)
			if !archive.SameOwnInterval(own, acc.SyncInterval) {
				if err := sched.CheckFloor(own); err != nil {
					return fmt.Errorf("--sync-interval: %w", err)
				}
				if err := updateAccount(cmd, a, acc, store.AccountChange{SyncInterval: &store.OwnInterval{D: own}}); err != nil {
					return err
				}
				acc.SyncInterval = own
			}
			last, err := lastRun(cmd, a, acc.ID)
			if err != nil {
				return err
			}
			fmt.Println(scheduleLine(sched, acc, last))
			if sched.Sync == 0 {
				fmt.Printf("note: the web server's schedule is off (%s=0)\n", config.EnvSyncInterval)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&interval, "sync-interval", "", `"default", "manual" or a duration like 30m (required)`)
	_ = cmd.MarkFlagRequired("sync-interval")
	return cmd
}

func lastRun(cmd *cobra.Command, a *app, id int64) (*store.LastRun, error) {
	runs, err := a.store.LastRuns(cmd.Context())
	if err != nil {
		return nil, err
	}
	if run, ok := runs[id]; ok {
		return &run, nil
	}
	return nil, nil
}

// scheduleLine tells how often the schedule syncs the account and when next.
func scheduleLine(sched archive.Schedule, acc *store.Account, last *store.LastRun) string {
	sc := archive.ScheduledAccount(acc, last)
	every := sched.EveryFor(sc)
	if every == 0 {
		if acc.SyncInterval != nil && *acc.SyncInterval == 0 {
			return fmt.Sprintf("schedule of %q: not on the schedule", acc.Name)
		}
		return fmt.Sprintf("schedule of %q: the schedule is off", acc.Name)
	}
	line := fmt.Sprintf("schedule of %q: sync every %s", acc.Name, config.FormatDuration(every))
	if next, ok := sched.Next(sc); ok {
		line += ", next " + nextSync(next, time.Now())
	} else {
		line += " (disabled)"
	}
	return line
}

// nextSync formats a next due time: "due" when it is in the past.
func nextSync(next, now time.Time) string {
	if !next.After(now) {
		return "due"
	}
	return "due " + next.Local().Format("2006-01-02 15:04")
}

// everyColumn is the EVERY column of status: the effective interval, with
// "(default)" or "(min)" when it does not come from the account's own.
func everyColumn(sched archive.Schedule, s store.AccountStats) string {
	sc := archive.ScheduledStats(s)
	switch {
	case !s.Kind.HasServer() || s.RemovedAt != nil:
		return "-"
	case sched.Sync == 0:
		return "off"
	case s.SyncInterval != nil && *s.SyncInterval == 0:
		return "manual"
	}
	every := sched.EveryFor(sc)
	out := config.FormatDuration(every)
	own := sched.Sync
	if s.SyncInterval != nil {
		own = *s.SyncInterval
	}
	switch {
	case own < every:
		out += " (min)"
	case s.SyncInterval == nil:
		out += " (default)"
	}
	return out
}

// nextColumn is the NEXT SYNC column of status.
func nextColumn(sched archive.Schedule, s store.AccountStats, now time.Time) string {
	next, ok := sched.Next(archive.ScheduledStats(s))
	switch {
	case !ok:
		return "-"
	case !next.After(now):
		return "due"
	}
	return next.Local().Format("2006-01-02 15:04")
}

// healthColumn is the HEALTH column of status, as in /healthz/sync. The CLI
// cannot see the web server's queue, so a queued account may show stale.
func healthColumn(cfg *config.Config, sched archive.Schedule, s store.AccountStats, h *store.SyncHealth,
	running map[int64]bool, runs map[int64]store.LastRun, now time.Time) string {
	if !s.Kind.HasServer() || h == nil {
		return "-"
	}
	var busy *time.Time
	if running[s.AccountID] {
		busy = &now
		if run, ok := runs[s.AccountID]; ok && run.FinishedAt == nil {
			busy = &run.StartedAt
		}
	}
	return h.State(cfg.AlertAfterFailures, sched.Every(h.SyncInterval), busy, now)
}
