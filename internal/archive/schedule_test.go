package archive

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/store"
)

func dur(d time.Duration) *time.Duration { return &d }

func TestScheduleEvery(t *testing.T) {
	s := Schedule{Sync: 6 * time.Hour, SyncMin: 15 * time.Minute}
	for _, c := range []struct {
		name string
		s    Schedule
		own  *time.Duration
		want time.Duration
	}{
		{"default", s, nil, 6 * time.Hour},
		{"own", s, dur(30 * time.Minute), 30 * time.Minute},
		{"below the floor", s, dur(5 * time.Minute), 15 * time.Minute},
		{"manual", s, dur(0), 0},
		{"schedule off", Schedule{SyncMin: 5 * time.Minute}, dur(time.Hour), 0},
		{"default below a raised floor", Schedule{Sync: time.Hour, SyncMin: 2 * time.Hour}, nil, 2 * time.Hour},
	} {
		if got := c.s.Every(c.own); got != c.want {
			t.Errorf("%s: Every = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestParseOwnInterval(t *testing.T) {
	for in, want := range map[string]*time.Duration{
		"default": nil, "manual": dur(0), "30m": dur(30 * time.Minute), "1h30m": dur(90 * time.Minute),
		"5m": dur(5 * time.Minute), "720h": dur(720 * time.Hour),
	} {
		got, err := ParseOwnInterval(in)
		if err != nil || !SameOwnInterval(got, want) {
			t.Errorf("ParseOwnInterval(%q) = %v, %v", in, got, err)
		}
		if f := FormatOwnInterval(got); func() bool { p, _ := ParseOwnInterval(f); return !SameOwnInterval(p, got) }() {
			t.Errorf("round trip of %q via %q", in, f)
		}
	}
	for _, in := range []string{"0", "90s", "5m30s", "-1h", "4m", "721h", "", "Default", "1d"} {
		if _, err := ParseOwnInterval(in); !errors.Is(err, ErrOwnInterval) {
			t.Errorf("ParseOwnInterval(%q) = %v, want ErrOwnInterval", in, err)
		}
	}
	if FormatOwnInterval(nil) != "default" || FormatOwnInterval(dur(0)) != "manual" || FormatOwnInterval(dur(30*time.Minute)) != "30m0s" {
		t.Error("FormatOwnInterval")
	}
}

func TestCheckFloor(t *testing.T) {
	s := Schedule{Sync: 6 * time.Hour, SyncMin: time.Hour}
	for _, ok := range []*time.Duration{nil, dur(0), dur(time.Hour), dur(2 * time.Hour)} {
		if err := s.CheckFloor(ok); err != nil {
			t.Errorf("CheckFloor(%v) = %v", ok, err)
		}
	}
	if err := s.CheckFloor(dur(30 * time.Minute)); !errors.Is(err, ErrOwnInterval) || err.Error() != "invalid sync interval: 30m is below the server's minimum of 1h" {
		t.Errorf("below the floor: %v", err)
	}
	if got := s.OwnIntervalRule(); got != "want default, manual, or a duration of whole minutes from 1h to 720h" {
		t.Errorf("rule %q", got)
	}
}

func TestScheduleNext(t *testing.T) {
	s := Schedule{Sync: 6 * time.Hour, SyncMin: 5 * time.Minute}
	created := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	last := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	removed := Scheduled{Kind: store.KindIMAP, Enabled: true, Removed: true, CreatedAt: created}
	for _, c := range []struct {
		name string
		a    Scheduled
		want time.Time
		ok   bool
	}{
		{"never synced", Scheduled{Kind: store.KindIMAP, Enabled: true, CreatedAt: created}, created, true},
		{"default", Scheduled{Kind: store.KindIMAP, Enabled: true, CreatedAt: created, LastStart: &last}, last.Add(6 * time.Hour), true},
		{"own", Scheduled{Kind: store.KindIMAP, Enabled: true, Own: dur(15 * time.Minute), CreatedAt: created, LastStart: &last}, last.Add(15 * time.Minute), true},
		{"manual", Scheduled{Kind: store.KindIMAP, Enabled: true, Own: dur(0), CreatedAt: created}, time.Time{}, false},
		{"disabled", Scheduled{Kind: store.KindIMAP, CreatedAt: created}, time.Time{}, false},
		{"removed", removed, time.Time{}, false},
		{"import", Scheduled{Kind: store.KindImport, CreatedAt: created}, time.Time{}, false},
		{"smtp", Scheduled{Kind: store.KindSMTP, Enabled: true, CreatedAt: created}, time.Time{}, false},
	} {
		next, ok := s.Next(c.a)
		if ok != c.ok || !next.Equal(c.want) {
			t.Errorf("%s: Next = %v, %v; want %v, %v", c.name, next, ok, c.want, c.ok)
		}
	}
}

func TestDueAccounts(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	imap := func(id int64, own *time.Duration, enabled bool) *store.Account {
		return &store.Account{ID: id, Kind: store.KindIMAP, Enabled: enabled, SyncInterval: own, CreatedAt: now.Add(-48 * time.Hour)}
	}
	removedAt := now.Add(-time.Hour)
	accounts := []*store.Account{
		imap(1, dur(15*time.Minute), true), // last start 15 minutes ago: due
		imap(2, dur(24*time.Hour), true),   // last start 15 minutes ago: not due
		imap(3, dur(0), true),              // manual: never
		imap(4, dur(15*time.Minute), false),
		imap(5, nil, true), // never ran: due
		{ID: 6, Kind: store.KindImport, CreatedAt: now},
		{ID: 7, Kind: store.KindIMAP, Enabled: true, RemovedAt: &removedAt, CreatedAt: now},
		imap(8, nil, true), // default 6h, last start 15 minutes ago
	}
	ago := store.LastRun{StartedAt: now.Add(-15 * time.Minute)}
	runs := map[int64]store.LastRun{1: ago, 2: ago, 3: ago, 4: ago, 8: ago}
	if got := dueAccounts(accounts, runs, Schedule{Sync: 6 * time.Hour, SyncMin: 5 * time.Minute}, now); !slices.Equal(got, []int64{1, 5}) {
		t.Errorf("due %v, want [1 5]", got)
	}
	// A raised floor delays the 15-minute account.
	if got := dueAccounts(accounts, runs, Schedule{Sync: 6 * time.Hour, SyncMin: 30 * time.Minute}, now); !slices.Equal(got, []int64{5}) {
		t.Errorf("due with floor %v, want [5]", got)
	}
}
