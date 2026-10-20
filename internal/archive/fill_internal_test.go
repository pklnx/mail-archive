package archive

import (
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/imapsync"
	"github.com/pklnx/mail-archive/internal/store"
)

func TestFillVerdict(t *testing.T) {
	cutoff := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	berlin := time.FixedZone("CEST", 2*3600)
	for _, c := range []struct {
		name      string
		at        time.Time
		journaled bool
		want      fillVerdict
	}{
		{"long before", cutoff.Add(-30 * 24 * time.Hour), false, fillImport},
		{"long before, journaled", cutoff.Add(-30 * 24 * time.Hour), true, fillJournaled},
		{"just after the cut-off", cutoff.Add(30 * time.Minute), false, fillImport},
		{"just after the cut-off, journaled", cutoff.Add(30 * time.Minute), true, fillJournaled},
		{"last second of the window", cutoff.Add(FillWindow - time.Second), false, fillImport},
		{"end of the window", cutoff.Add(FillWindow), false, fillAfterCutoff},
		{"after the window, journaled", cutoff.Add(2 * time.Hour), true, fillAfterCutoff},
		// 14:59 in +02:00 is 12:59 UTC: inside the window.
		{"other zone, inside", time.Date(2026, 6, 1, 14, 59, 0, 0, berlin), false, fillImport},
		// 15:00 in +02:00 is 13:00 UTC: the end of the window.
		{"other zone, at the end", time.Date(2026, 6, 1, 15, 0, 0, 0, berlin), false, fillAfterCutoff},
		{"no date", time.Time{}, false, fillImport},
	} {
		if got := verdict(c.at, cutoff, c.journaled); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestFillPasswordIsBoundToTheFill(t *testing.T) {
	sealer := testSealer(t)
	enc, err := SealFillPassword(sealer, 7, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if pw, err := OpenFillPassword(sealer, &store.Fill{ID: 7, PasswordEnc: enc}); err != nil || pw != "secret" {
		t.Fatalf("open: %q, %v", pw, err)
	}
	if _, err := OpenFillPassword(sealer, &store.Fill{ID: 8, PasswordEnc: enc}); err == nil {
		t.Error("opened with another fill's ID")
	}
	// An account's password ciphertext does not open as a fill password, nor the other way round.
	acc, err := SealPassword(sealer, 7, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFillPassword(sealer, &store.Fill{ID: 7, PasswordEnc: acc}); err == nil {
		t.Error("an account password opened as a fill password")
	}
	if _, err := OpenPassword(sealer, &store.Account{ID: 7, Name: "x", PasswordEnc: enc}); err == nil {
		t.Error("a fill password opened as an account password")
	}
	if _, err := OpenFillPassword(sealer, &store.Fill{ID: 7}); err == nil {
		t.Error("opened a wiped password")
	}
}

func TestRecurringVerdict(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name      string
		at        time.Time
		journaled bool
		want      fillVerdict
	}{
		{"old", now.Add(-48 * time.Hour), false, fillImport},
		{"old, journaled", now.Add(-48 * time.Hour), true, fillJournaled},
		{"young", now.Add(-time.Hour), false, fillWaiting},
		{"young, journaled", now.Add(-time.Minute), true, fillJournaled},
		{"a second short of two hours", now.Add(-RecurringGrace + time.Second), false, fillWaiting},
		{"exactly two hours", now.Add(-RecurringGrace), false, fillImport},
		{"in the future", now.Add(time.Hour), false, fillWaiting},
		{"more than a day in the future", now.Add(25 * time.Hour), false, fillImport},
		{"more than a day in the future, journaled", now.Add(25 * time.Hour), true, fillJournaled},
		{"no date", time.Time{}, false, fillImport},
	} {
		if got := recurringVerdict(c.at, now, c.journaled); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// TestWatermark: LastUID moves over decided messages up to the first one
// that waits, and no further in that pass.
func TestWatermark(t *testing.T) {
	type msg struct {
		uid     uint32
		waiting bool
	}
	for _, c := range []struct {
		name string
		msgs []msg
		want uint32
	}{
		{"none waits", []msg{{3, false}, {4, false}, {9, false}}, 9},
		{"one in the middle", []msg{{3, false}, {4, true}, {5, false}, {6, false}}, 3},
		{"the first", []msg{{3, true}, {4, false}}, 0},
		{"the last", []msg{{3, false}, {4, false}, {5, true}}, 4},
	} {
		var w watermark
		last := uint32(0)
		for _, m := range c.msgs {
			w.advance(&last, m.uid, m.waiting)
		}
		if last != c.want {
			t.Errorf("%s: %d, want %d", c.name, last, c.want)
		}
	}
	// It goes on from the stored value.
	var w watermark
	last := uint32(10)
	w.advance(&last, 11, false)
	w.advance(&last, 12, true)
	w.advance(&last, 13, false)
	if last != 11 {
		t.Errorf("from 10: %d", last)
	}
}

// TestWatermarkMissingBody: a message whose body was requested but did not
// come back holds the watermark, so that the next run asks for it again.
func TestWatermarkMissingBody(t *testing.T) {
	chunk := []imapsync.Header{{UID: 4}, {UID: 5}, {UID: 6}, {UID: 7}}
	requested := map[uint32]bool{4: true, 6: true, 7: true}
	var w watermark
	last := uint32(3)
	w.advanceBatch(&last, chunk, map[uint32]bool{}, requested, map[uint32]bool{4: true, 7: true})
	if last != 5 {
		t.Errorf("last %d, want 5", last)
	}
	// It stays held for the rest of the pass.
	w.advanceBatch(&last, []imapsync.Header{{UID: 8}}, map[uint32]bool{}, map[uint32]bool{}, map[uint32]bool{})
	if last != 5 {
		t.Errorf("after the next batch %d", last)
	}
	var all watermark
	last = 3
	all.advanceBatch(&last, chunk, map[uint32]bool{}, requested, map[uint32]bool{4: true, 6: true, 7: true})
	if last != 7 {
		t.Errorf("all fetched: %d", last)
	}
}
