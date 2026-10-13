package archive

import (
	"testing"
	"time"

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
