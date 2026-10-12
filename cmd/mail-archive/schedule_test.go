package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/store"
)

func TestSetScheduleCommand(t *testing.T) {
	s := newArchiveSetup(t)
	ctx := context.Background()
	t.Setenv("MAIL_ARCHIVE_SYNC_INTERVAL", "6h")
	t.Setenv("MAIL_ARCHIVE_SYNC_INTERVAL_MIN", "15m")
	run := func(args ...string) (string, error) {
		var err error
		out := captureStdout(t, func() { err = runCmd(t, newAccountCmd(), "", append([]string{"set-schedule"}, args...)...) })
		return out, err
	}
	own := func() *time.Duration {
		a, err := s.st.GetAccount(ctx, s.accs["anna"].ID)
		if err != nil {
			t.Fatal(err)
		}
		return a.SyncInterval
	}

	if out, err := run("mail", "--user", "anna", "--sync-interval", "30m"); err != nil ||
		!strings.Contains(out, `schedule of "mail": sync every 30m (disabled)`) {
		t.Fatalf("30m: %v\n%s", err, out)
	}
	if d := own(); d == nil || *d != 30*time.Minute {
		t.Errorf("stored %v", d)
	}
	if out, err := run("mail", "--user", "anna", "--sync-interval", "manual"); err != nil || !strings.Contains(out, "not on the schedule") {
		t.Fatalf("manual: %v\n%s", err, out)
	}
	if d := own(); d == nil || *d != 0 {
		t.Errorf("stored %v", d)
	}
	if out, err := run("mail", "--user", "anna", "--sync-interval", "default"); err != nil || !strings.Contains(out, "sync every 6h") {
		t.Fatalf("default: %v\n%s", err, out)
	}
	if d := own(); d != nil {
		t.Errorf("stored %v", *d)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"mail", "--user", "anna", "--sync-interval", "10m"}, "below the server's minimum of 15m"},
		{[]string{"mail", "--user", "anna", "--sync-interval", "90s"}, "whole minutes from 5m to 720h"},
		{[]string{"mail", "--user", "anna"}, `"sync-interval" not set`},
	} {
		if _, err := run(c.args...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want %q", c.args, err, c.want)
		}
	}
	// A value stored before the floor was raised can be kept.
	ten := 10 * time.Minute
	a, _ := s.st.GetAccount(ctx, s.accs["anna"].ID)
	if err := s.st.UpdateAccount(ctx, a.Ref(), store.AccountChange{SyncInterval: &store.OwnInterval{D: &ten}}); err != nil {
		t.Fatal(err)
	}
	if out, err := run("mail", "--user", "anna", "--sync-interval", "10m"); err != nil || !strings.Contains(out, "sync every 15m") {
		t.Fatalf("unchanged below the floor: %v\n%s", err, out)
	}

	// Import and smtp accounts have no schedule.
	imp := &store.Account{Kind: store.KindImport, Name: "old", OwnerID: s.accs["anna"].OwnerID}
	if err := s.st.CreateAccount(ctx, imp); err != nil {
		t.Fatal(err)
	}
	if _, err := run("old", "--user", "anna", "--sync-interval", "30m"); err == nil {
		t.Error("import account accepted a schedule")
	}

	// The schedule off is noted.
	t.Setenv("MAIL_ARCHIVE_SYNC_INTERVAL", "0")
	if out, err := run("mail", "--user", "bob", "--sync-interval", "30m"); err != nil ||
		!strings.Contains(out, "the schedule is off") || !strings.Contains(out, "note: the web server's schedule is off") {
		t.Fatalf("schedule off: %v\n%s", err, out)
	}
}

func TestStatusScheduleColumns(t *testing.T) {
	s := newArchiveSetup(t)
	ctx := context.Background()
	t.Setenv("MAIL_ARCHIVE_SYNC_INTERVAL", "6h")
	t.Setenv("MAIL_ARCHIVE_SYNC_INTERVAL_MIN", "15m")
	on, ten := true, 10*time.Minute
	a, _ := s.st.GetAccount(ctx, s.accs["anna"].ID)
	if err := s.st.UpdateAccount(ctx, a.Ref(), store.AccountChange{Enabled: &on, SyncInterval: &store.OwnInterval{D: &ten}}); err != nil {
		t.Fatal(err)
	}
	var err error
	out := captureStdout(t, func() { err = runCmd(t, newStatusCmd(), "") })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "EVERY") || !strings.Contains(out, "NEXT SYNC") || !strings.Contains(out, "HEALTH") {
		t.Fatalf("header:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || f[1] != "mail" {
			continue
		}
		switch {
		case strings.Contains(line, "15m (min)"):
			// anna: enabled and never synced, so due now; just created, so ok.
			if !strings.Contains(line, " due ") || !strings.Contains(line, " ok ") {
				t.Errorf("anna: %s", line)
			}
		case strings.Contains(line, "6h (default)"):
			// bob: disabled, so no next sync.
			if !strings.Contains(line, " - ") {
				t.Errorf("bob: %s", line)
			}
		default:
			t.Errorf("unexpected row: %s", line)
		}
	}
}

func TestScheduleColumns(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	sched := archive.Schedule{Sync: 6 * time.Hour, SyncMin: 15 * time.Minute}
	d := func(x time.Duration) *time.Duration { return &x }
	last := now.Add(-time.Hour)
	removed := now
	for _, c := range []struct {
		name        string
		sched       archive.Schedule
		s           store.AccountStats
		every, next string
	}{
		{"default", sched, store.AccountStats{Kind: store.KindIMAP, Enabled: true, LastRunAt: &last}, "6h (default)", "2026-10-12"},
		{"own", sched, store.AccountStats{Kind: store.KindIMAP, Enabled: true, SyncInterval: d(30 * time.Minute), LastRunAt: &last}, "30m", "due"},
		{"raised", sched, store.AccountStats{Kind: store.KindIMAP, Enabled: true, SyncInterval: d(10 * time.Minute)}, "15m (min)", "due"},
		{"manual", sched, store.AccountStats{Kind: store.KindIMAP, Enabled: true, SyncInterval: d(0)}, "manual", "-"},
		{"off", archive.Schedule{SyncMin: 5 * time.Minute}, store.AccountStats{Kind: store.KindIMAP, Enabled: true}, "off", "-"},
		{"disabled", sched, store.AccountStats{Kind: store.KindIMAP}, "6h (default)", "-"},
		{"removed", sched, store.AccountStats{Kind: store.KindIMAP, RemovedAt: &removed}, "-", "-"},
		{"import", sched, store.AccountStats{Kind: store.KindImport}, "-", "-"},
		{"smtp", sched, store.AccountStats{Kind: store.KindSMTP, Enabled: true}, "-", "-"},
	} {
		if got := everyColumn(c.sched, c.s); got != c.every {
			t.Errorf("%s: EVERY %q, want %q", c.name, got, c.every)
		}
		if got := nextColumn(c.sched, c.s, now); !strings.HasPrefix(got, c.next) {
			t.Errorf("%s: NEXT SYNC %q, want %q", c.name, got, c.next)
		}
	}
}
