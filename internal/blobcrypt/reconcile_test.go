package blobcrypt

import (
	"bytes"
	"errors"
	"testing"
)

// opened is a reparsed and opened copy of k, as read from a file or the
// database.
func opened(t *testing.T, k *Keyring, kek []byte) *Keyring {
	t.Helper()
	p, _ := reparse(t, k)
	if err := p.Open(kek); err != nil {
		t.Fatal(err)
	}
	return p
}

// rekeyed simulates part 5's rekey on a copy: a new current data key.
func rekeyed(t *testing.T, k *Keyring) uint32 {
	t.Helper()
	id, err := k.addDataKey()
	if err != nil {
		t.Fatal(err)
	}
	k.current = id
	if err := k.bump(); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestReconcileNewerWins(t *testing.T) {
	k, kek := newTestKeyring(t)
	old := opened(t, k, kek)
	if err := k.AddSlot(randomKey(t), "recovery key", t0); err != nil {
		t.Fatal(err)
	}
	cur := opened(t, k, kek)
	for _, order := range [][2]*Keyring{{old, cur}, {cur, old}} {
		got, err := Reconcile(order[0], order[1])
		if err != nil {
			t.Fatal(err)
		}
		if !got.SameAs(cur) {
			t.Fatalf("result is not the newer copy: generation %d, %d slots", got.Generation(), len(got.Slots()))
		}
		if got.SameAs(old) {
			t.Fatal("result is the same as the older copy")
		}
	}
	// Equal copies stay as they are.
	got, err := Reconcile(cur, opened(t, k, kek))
	if err != nil || !got.SameAs(cur) || got.Generation() != cur.Generation() {
		t.Fatalf("equal copies: %v, generation %d", err, got.Generation())
	}
}

// A data directory from before a rekey next to a database from after it
// (and the other way round): every data key stays, the newer copy's
// current key wins.
func TestReconcileUnionOfDataKeys(t *testing.T) {
	k, kek := newTestKeyring(t)
	before := opened(t, k, kek)
	oldID := k.CurrentKey()
	newID := rekeyed(t, k)
	// The rekey finished and deleted the old key in this copy.
	delete(k.keys, oldID)
	if err := k.bump(); err != nil {
		t.Fatal(err)
	}
	after := opened(t, k, kek)

	got, err := Reconcile(before, after)
	if err != nil {
		t.Fatal(err)
	}
	if got.CurrentKey() != newID {
		t.Fatalf("current key %08x, want the newer copy's %08x", got.CurrentKey(), newID)
	}
	for _, id := range []uint32{oldID, newID} {
		if _, ok := got.DataKey(id); !ok {
			t.Fatalf("data key %08x lost", id)
		}
	}
	if got.Generation() <= after.Generation() || got.SameAs(after) || got.SameAs(before) {
		t.Fatalf("generation %d: the union must differ from both copies (%d, %d)", got.Generation(), before.Generation(), after.Generation())
	}
	// What is written opens with the KEK again and keeps both keys.
	back := opened(t, got, kek)
	if !back.SameAs(got) {
		t.Fatal("the reconciled keyring does not survive Marshal and Open")
	}
}

func TestReconcileRefusals(t *testing.T) {
	a, kek := newTestKeyring(t)
	b, _ := newTestKeyring(t)
	if _, err := Reconcile(opened(t, a, kek), b); !errors.Is(err, ErrOtherArchive) {
		t.Fatalf("other archive: %v", err)
	}

	// The same random id with other material: diverged histories.
	x := opened(t, a, kek)
	y := opened(t, a, kek)
	id := rekeyed(t, x)
	y.keys[id] = bytes.Repeat([]byte{7}, KeySize)
	y.current = id
	if _, err := Reconcile(x, y); !errors.Is(err, ErrKeyConflict) {
		t.Fatalf("conflicting data key: %v", err)
	}

	p, _ := reparse(t, a)
	if _, err := Reconcile(p, x); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("unopened copy: %v", err)
	}
}

// Two changes on diverged copies that end at the same generation: the
// result is neither copy, so both are written again.
func TestReconcileSameGenerationDiverged(t *testing.T) {
	k, kek := newTestKeyring(t)
	x := opened(t, k, kek)
	y := opened(t, k, kek)
	if err := x.AddSlot(randomKey(t), "recovery key", t0); err != nil {
		t.Fatal(err)
	}
	if err := y.AddSlot(randomKey(t), "recovery key", t0); err != nil {
		t.Fatal(err)
	}
	got, err := Reconcile(x, y)
	if err != nil {
		t.Fatal(err)
	}
	if got.SameAs(x) || got.SameAs(y) || got.Generation() != x.Generation()+1 {
		t.Fatalf("generation %d: diverged copies must both be written again", got.Generation())
	}
}
