// Package quota is the storage gate (#397, part 2): it decides per customer
// group and for the instance whether new mail may be stored.
//
// Every scope, the instance and each group, has a state: ok, warn (from
// the warn share of its quota), full (from 100 %) and overflow (from 100 %
// plus the SMTP overflow share); the instance also disk_low, when the data
// directory's file system has less free space than the reserve. A state is
// left only once the figure is 1 % of the quota below its threshold (for
// disk_low: the free space 1 % of the reserve above it), so a figure that
// moves around a threshold does not flip the state.
//
// The effective state of an account is the worst of its group's, the
// instance's and the disk's. An account without an owner, and one in a
// group without a quota, follows the instance only; so does every account
// before the first usage snapshot. From full on, syncs, fills and imports
// pause; from overflow on (and at disk_low), the SMTP server defers new
// mail with 452.
package quota

import (
	"errors"
	"fmt"
)

// State is a scope's quota state, ordered from best to worst.
type State int

// The states. DiskLow is the instance's only; it is worse than Overflow.
const (
	OK State = iota
	Warn
	Full
	Overflow
	DiskLow
)

var stateNames = [...]string{OK: "ok", Warn: "warn", Full: "full", Overflow: "overflow", DiskLow: "disk_low"}

// String is the state's name, as stored in storage_states.
func (s State) String() string {
	if s < OK || s > DiskLow {
		return fmt.Sprintf("State(%d)", int(s))
	}
	return stateNames[s]
}

// ParseState reads a stored state name.
func ParseState(name string) (State, error) {
	for i, n := range stateNames {
		if n == name {
			return State(i), nil
		}
	}
	return OK, fmt.Errorf("unknown storage state %q", name)
}

// Paused reports whether syncs, fills and imports pause: full or worse.
func (s State) Paused() bool { return s >= Full }

// Deferred reports whether the SMTP server defers new mail: overflow or
// worse.
func (s State) Deferred() bool { return s >= Overflow }

// Thresholds are the percentages of a quota at which its scope changes
// state; the same for the instance quota and every group quota.
type Thresholds struct {
	// WarnPercent (1-99): from this share of the quota, warn.
	WarnPercent int
	// OverflowPercent (0-100): from 100 % plus this share, overflow.
	OverflowPercent int
}

// percent is p % of q, rounded down, without overflowing for any quota up
// to 1 EiB and p up to 200.
func percent(q int64, p int) int64 {
	return q/100*int64(p) + q%100*int64(p)/100
}

// threshold is the figure from which a scope of quota q is in state s
// (Warn, Full or Overflow).
func (t Thresholds) threshold(q int64, s State) int64 {
	switch s {
	case Warn:
		return percent(q, t.WarnPercent)
	case Full:
		return q
	case Overflow:
		return q + percent(q, t.OverflowPercent)
	}
	return 0
}

// Level is the state of a figure without hysteresis: the worst state
// whose threshold it reached. Without a quota (q <= 0) it is OK.
func (t Thresholds) Level(q, bytes int64) State {
	if q <= 0 {
		return OK
	}
	for s := Overflow; s > OK; s-- {
		if bytes >= t.threshold(q, s) {
			return s
		}
	}
	return OK
}

// Next is the quota state that follows cur (OK to Overflow) for a figure:
// up to the Level as soon as the figure reaches a threshold, and down only
// as far as the figure is 1 % of the quota below the thresholds it leaves:
// the worst state s <= cur with bytes >= threshold(s) - 1 % of the quota.
// Inside that band nothing changes. Without a quota it is OK.
func (t Thresholds) Next(cur State, q, bytes int64) State {
	if q <= 0 {
		return OK
	}
	if cur > Overflow {
		cur = Overflow
	}
	up := t.Level(q, bytes)
	if up > cur {
		return up
	}
	band := q / 100
	for s := cur; s > OK; s-- {
		if bytes >= t.threshold(q, s)-band {
			return s
		}
	}
	return OK
}

// diskLow reports whether the instance is at disk_low for this much free
// space, after cur: below reserve to enter, at least 1 % of the reserve
// above it to leave. A reserve of 0 turns it off.
func diskLow(cur State, reserve, free int64) bool {
	if reserve <= 0 {
		return false
	}
	if cur == DiskLow {
		return free < reserve+reserve/100
	}
	return free < reserve
}

// ErrStorageFull is matched by every FullError.
var ErrStorageFull = errors.New("storage_full")

// Scope names the limit behind a FullError, for the operator's messages
// and logs only: users only learn that storage is full (#539).
type Scope string

// The scopes of a FullError.
const (
	ScopeGroup    Scope = "group"
	ScopeInstance Scope = "instance"
	ScopeDisk     Scope = "disk"
)

// FullError is returned when new mail of a group may not be stored now.
type FullError struct {
	// State is the effective state, Scope the limit that set it (the
	// group's before the instance's when both are equal), Group the
	// group asked about (nil without one).
	State State
	Scope Scope
	Group *int64
}

func (e *FullError) Error() string {
	switch e.Scope {
	case ScopeDisk:
		return "the disk reserve is reached (storage_full)"
	case ScopeInstance:
		return "the storage quota of the instance is reached (storage_full)"
	}
	return "the storage quota of the customer group is reached (storage_full)"
}

// Is makes every FullError match ErrStorageFull.
func (e *FullError) Is(target error) bool { return target == ErrStorageFull }

// PausedText is the error stored with a run that paused: shown to the
// account's users and in alerts, so it names no limit and no figure.
const PausedText = "storage_full: the storage limit is reached; new mail is archived again once space is freed"
