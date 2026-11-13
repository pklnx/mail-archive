package smtpd

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/pklnx/mail-archive/internal/quota"
)

// storageSummaryEvery is how often the deferrals of a group are summed up
// in the log after the first one was logged in full.
const storageSummaryEvery = time.Minute

// storageLog rate-limits the log lines of storage deferrals (#397): per
// customer group, the first after a change of state is logged in full,
// then one summary line per minute with the count, written with the next
// deferral after the minute. So the retries of a full group's mail
// servers cannot flood the log.
type storageLog struct {
	mu    sync.Mutex
	seen  map[string]*storageLogEntry
	group map[string]string // log key -> group key, for reset
}

type storageLogEntry struct {
	state quota.State
	since time.Time
	count int
}

func groupKey(group *int64) string {
	if group == nil {
		return "none"
	}
	return strconv.FormatInt(*group, 10)
}

// note logs one event of kind (deferred, accepted) for the group.
func (l *storageLog) note(log *slog.Logger, now time.Time, kind string, group *int64, st quota.State, msg string, attrs ...any) {
	key := kind + ":" + groupKey(group)
	l.mu.Lock()
	if l.seen == nil {
		l.seen, l.group = map[string]*storageLogEntry{}, map[string]string{}
	}
	e := l.seen[key]
	var full bool
	var summary int
	var since time.Time
	switch {
	case e == nil || e.state != st:
		l.seen[key] = &storageLogEntry{state: st, since: now}
		l.group[key] = groupKey(group)
		full = true
	default:
		e.count++
		if now.Sub(e.since) >= storageSummaryEvery {
			summary, since = e.count, e.since
			e.count, e.since = 0, now
		}
	}
	l.mu.Unlock()
	switch {
	case full:
		log.Log(context.Background(), slog.LevelWarn, msg, attrs...)
	case summary > 0:
		log.Warn("smtp storage "+kind+" since the last line",
			"group_id", group, "count", summary, "since", since, "state", st.String(), "reason", "storage")
	}
}

// reset forgets the group's entries: a recipient was accepted below full,
// so the state changed, and the next deferral is logged in full again.
func (l *storageLog) reset(group *int64) {
	g := groupKey(group)
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.seen) == 0 {
		return
	}
	for key, k := range l.group {
		if k == g {
			delete(l.seen, key)
			delete(l.group, key)
		}
	}
}
