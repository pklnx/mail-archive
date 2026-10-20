package archive

import (
	"errors"
	"fmt"
	"time"

	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/store"
)

// Schedule is the web server's sync schedule. An account's own interval
// overrides the default; the floor applies to both.
type Schedule struct {
	// Sync is the default interval (MAIL_ARCHIVE_SYNC_INTERVAL); 0 turns
	// the schedule off for every account.
	Sync time.Duration
	// SyncMin is the floor (MAIL_ARCHIVE_SYNC_INTERVAL_MIN). It is applied
	// when reading, so raising it needs no data change.
	SyncMin time.Duration
}

// Limits of an own interval, also enforced by the database.
const (
	MinOwnInterval = 5 * time.Minute
	MaxOwnInterval = 720 * time.Hour
)

// Words for the own interval in the API, the CLI and the audit log.
const (
	OwnIntervalDefault = "default"
	OwnIntervalManual  = "manual"
)

// Every is how often the schedule syncs an account with this own interval:
// 0 when the schedule is off or own is 0 (not on the schedule), else the
// own interval or the default, at least the floor.
func (s Schedule) Every(own *time.Duration) time.Duration {
	if s.Sync <= 0 {
		return 0
	}
	every := s.Sync
	if own != nil {
		every = *own
	}
	if every <= 0 {
		return 0
	}
	return max(every, s.SyncMin)
}

// Scheduled is what the schedule needs to know about an account.
type Scheduled struct {
	Kind      store.AccountKind
	Enabled   bool
	Removed   bool
	Own       *time.Duration
	CreatedAt time.Time
	// LastStart is the start of the last run of any kind; nil if none.
	LastStart *time.Time
}

// ScheduledAccount describes a for the schedule.
func ScheduledAccount(a *store.Account, last *store.LastRun) Scheduled {
	sc := Scheduled{Kind: a.Kind, Enabled: a.Enabled, Removed: a.RemovedAt != nil, Own: a.SyncInterval, CreatedAt: a.CreatedAt}
	if last != nil {
		sc.LastStart = &last.StartedAt
	}
	return sc
}

// ScheduledStats describes an account from Stats for the schedule.
func ScheduledStats(a store.AccountStats) Scheduled {
	return Scheduled{
		Kind: a.Kind, Enabled: a.Enabled, Removed: a.RemovedAt != nil, Own: a.SyncInterval,
		CreatedAt: a.CreatedAt, LastStart: a.LastRunAt,
	}
}

// EveryFor is Every for an account: 0 for removed accounts and those
// without a server, which the schedule never syncs.
func (s Schedule) EveryFor(a Scheduled) time.Duration {
	if a.Removed || !a.Kind.HasServer() {
		return 0
	}
	return s.Every(a.Own)
}

// Next is when the schedule syncs a next: its last start plus its interval,
// or its creation if it never ran (due at once). It may lie in the past.
// ok is false when the schedule does not sync the account: disabled,
// removed, without a server, or Every is 0.
func (s Schedule) Next(a Scheduled) (next time.Time, ok bool) {
	every := s.EveryFor(a)
	if every == 0 || !a.Enabled {
		return time.Time{}, false
	}
	if a.LastStart == nil {
		return a.CreatedAt, true
	}
	return a.LastStart.Add(every), true
}

// NextFill is when the schedule runs an active recurring fill next: the
// start of its last run plus its interval, or its creation if it never
// ran. It may lie in the past. ok is false for other fills and when the
// schedule is off.
func (s Schedule) NextFill(f *store.Fill) (next time.Time, ok bool) {
	every := s.Every(f.Interval)
	if every == 0 || !f.KeepRunning {
		return time.Time{}, false
	}
	if f.RunStartedAt == nil {
		return f.CreatedAt, true
	}
	return f.RunStartedAt.Add(every), true
}

// ErrOwnInterval is wrapped by the errors of ParseOwnInterval and CheckFloor.
var ErrOwnInterval = errors.New("invalid sync interval")

// ParseOwnInterval reads "default" (nil), "manual" (0) or a Go duration of
// whole minutes from MinOwnInterval to MaxOwnInterval. The server's floor
// is checked separately by CheckFloor.
func ParseOwnInterval(v string) (*time.Duration, error) {
	switch v {
	case OwnIntervalDefault:
		return nil, nil
	case OwnIntervalManual:
		d := time.Duration(0)
		return &d, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d%time.Minute != 0 || d < MinOwnInterval || d > MaxOwnInterval {
		return nil, fmt.Errorf("%w %q", ErrOwnInterval, v)
	}
	return &d, nil
}

// ParseFillInterval reads the interval of a recurring fill: "default"
// (nil) or a Go duration of whole minutes from MinOwnInterval to
// MaxOwnInterval. A recurring fill is always on the schedule, so "manual"
// is refused.
func ParseFillInterval(v string) (*time.Duration, error) {
	if v == OwnIntervalManual {
		return nil, fmt.Errorf("%w %q", ErrOwnInterval, v)
	}
	return ParseOwnInterval(v)
}

// FillIntervalRule describes the accepted intervals of a recurring fill,
// for error messages.
func (s Schedule) FillIntervalRule() string {
	return fmt.Sprintf("want %s or a duration of whole minutes from %s to %s",
		OwnIntervalDefault, config.FormatDuration(max(MinOwnInterval, s.SyncMin)), config.FormatDuration(MaxOwnInterval))
}

// CheckFloor refuses an own interval below the server's floor. Callers
// check it only for a changed value, so a value stored before the floor
// was raised can be kept.
func (s Schedule) CheckFloor(own *time.Duration) error {
	if own != nil && *own > 0 && *own < s.SyncMin {
		return fmt.Errorf("%w: %s is below the server's minimum of %s", ErrOwnInterval, config.FormatDuration(*own), config.FormatDuration(s.SyncMin))
	}
	return nil
}

// OwnIntervalRule describes the accepted values, for error messages.
func (s Schedule) OwnIntervalRule() string {
	return fmt.Sprintf("want %s, %s, or a duration of whole minutes from %s to %s",
		OwnIntervalDefault, OwnIntervalManual, config.FormatDuration(max(MinOwnInterval, s.SyncMin)), config.FormatDuration(MaxOwnInterval))
}

// FormatOwnInterval is the inverse of ParseOwnInterval: "default",
// "manual" or the duration, such as "30m0s".
func FormatOwnInterval(own *time.Duration) string {
	switch {
	case own == nil:
		return OwnIntervalDefault
	case *own == 0:
		return OwnIntervalManual
	}
	return own.String()
}

// SameOwnInterval reports whether a and b are the same own interval.
func SameOwnInterval(a, b *time.Duration) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
