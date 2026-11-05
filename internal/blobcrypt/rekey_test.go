package blobcrypt

import (
	"errors"
	"slices"
	"testing"
	"time"
)

// unwrapRingKey is what a holder of kek gets from a copy of a keyring
// (such as a backup) that still has kek's slot: the keyring key.
func unwrapRingKey(t *testing.T, data []byte, kek []byte) []byte {
	t.Helper()
	p, err := ParseKeyring(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Open(kek); err != nil {
		t.Fatal(err)
	}
	return p.ringKey
}

// openWithRingKey tries to read the data keys of the keyring file data with
// a keyring key alone, without any slot.
func openWithRingKey(t *testing.T, data, ringKey []byte) (map[uint32][]byte, error) {
	t.Helper()
	p, err := ParseKeyring(data)
	if err != nil {
		t.Fatal(err)
	}
	body, err := derivedGCM(ringKey, bodyKeyInfo)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := open(body, p.sealedKeys, p.bodyAAD())
	if err != nil {
		return nil, err
	}
	return parseDataKeys(plain)
}

// A key removed (or leaked) before a rekey opens neither the new keyring
// nor its new data key, even together with an older copy of the keyring
// that still has its slot: the rekey replaces the keyring key
// (coordinator decision on #437). Counter-proof: Rewrap keeping the old
// keyring key lets the old copy's keyring key read the new data key.
func TestRewrapLocksOutRemovedKey(t *testing.T) {
	k, kek := newTestKeyring(t)
	leaked := randomKey(t)
	if err := k.AddSlot(leaked, "leaked", t0); err != nil {
		t.Fatal(err)
	}
	_, backup := reparse(t, k) // an older copy, e.g. in a backup
	oldRingKey := unwrapRingKey(t, backup, leaked)

	if err := k.RemoveSlot(Fingerprint(leaked)); err != nil {
		t.Fatal(err)
	}
	oldKey := k.CurrentKey()
	newKey, err := k.AddCurrentKey()
	if err != nil {
		t.Fatal(err)
	}
	dropped, err := k.Rewrap([][]byte{kek}, nil)
	if err != nil || len(dropped) != 0 {
		t.Fatalf("Rewrap: %v, dropped %v", err, dropped)
	}
	p, data := reparse(t, k)
	if err := p.Open(leaked); !errors.Is(err, ErrNoSlot) {
		t.Errorf("the removed key opens the new keyring: %v", err)
	}
	if keys, err := openWithRingKey(t, data, oldRingKey); err == nil {
		_, hasNew := keys[newKey]
		t.Fatalf("the old keyring key reads the new keyring's data keys (new key present: %v)", hasNew)
	}
	// The kept key opens it, with the old and the new data key.
	if err := p.Open(kek); err != nil {
		t.Fatal(err)
	}
	if p.CurrentKey() != newKey {
		t.Errorf("current %08x, want %08x", p.CurrentKey(), newKey)
	}
	for _, id := range []uint32{oldKey, newKey} {
		if _, ok := p.DataKey(id); !ok {
			t.Errorf("data key %08x missing", id)
		}
	}
}

// Rewrap keeps only slots whose key is given and proven, keeps their
// labels and creation times, adds the new ones and returns the dropped.
func TestRewrapSlots(t *testing.T) {
	k, kek := newTestKeyring(t)
	rk, other, fresh := randomKey(t), randomKey(t), randomKey(t)
	if err := k.AddSlot(rk, "recovery key", t0); err != nil {
		t.Fatal(err)
	}
	if err := k.AddSlot(other, "old recovery", t0); err != nil {
		t.Fatal(err)
	}
	gen := k.Generation()
	if _, err := k.Rewrap([][]byte{randomKey(t)}, nil); !errors.Is(err, ErrNoSlot) {
		t.Errorf("an unknown key kept: %v", err)
	}
	if _, err := k.Rewrap(nil, nil); err == nil {
		t.Error("a keyring without slots accepted")
	}
	if _, err := k.Rewrap([][]byte{kek}, []NewSlot{{KEK: kek, Label: "again", Created: t0}}); err == nil {
		t.Error("the same key twice accepted")
	}
	if k.Generation() != gen {
		t.Fatalf("a refused Rewrap changed the generation to %d", k.Generation())
	}
	later := t0.Add(48 * time.Hour)
	dropped, err := k.Rewrap([][]byte{kek, rk}, []NewSlot{{KEK: fresh, Label: "new recovery", Created: later}})
	if err != nil {
		t.Fatal(err)
	}
	if len(dropped) != 1 || dropped[0].Fingerprint != Fingerprint(other) || dropped[0].Label != "old recovery" {
		t.Fatalf("dropped %+v", dropped)
	}
	if k.Generation() != gen+1 {
		t.Errorf("generation %d, want %d", k.Generation(), gen+1)
	}
	p, _ := reparse(t, k)
	for _, key := range [][]byte{kek, rk, fresh} {
		if err := p.Open(key); err != nil {
			t.Errorf("key %s: %v", Fingerprint(key), err)
		}
	}
	if err := p.Open(other); !errors.Is(err, ErrNoSlot) {
		t.Errorf("dropped key: %v", err)
	}
	if s, ok := p.Slot(Fingerprint(rk)); !ok || s.Label != "recovery key" || !s.Created.Equal(t0) {
		t.Errorf("kept slot %s %q %v", s.Fingerprint, s.Label, s.Created)
	}
	if s, ok := p.Slot(Fingerprint(fresh)); !ok || s.Label != "new recovery" || !s.Created.Equal(later) {
		t.Errorf("new slot %s %q %v", s.Fingerprint, s.Label, s.Created)
	}
}

// AddCurrentKey makes a new data key current; DeleteDataKeys removes the
// others but never the current one, and the deletion survives a reparse.
func TestDataKeyLifecycle(t *testing.T) {
	k, kek := newTestKeyring(t)
	old := k.CurrentKey()
	gen := k.Generation()
	id, err := k.AddCurrentKey()
	if err != nil || id == old || k.CurrentKey() != id || k.Generation() != gen+1 {
		t.Fatalf("AddCurrentKey: %08x %v, current %08x, generation %d", id, err, k.CurrentKey(), k.Generation())
	}
	if err := k.DeleteDataKeys([]uint32{id}); err == nil {
		t.Error("deleted the current key")
	}
	if err := k.DeleteDataKeys([]uint32{12345}); err != nil || k.Generation() != gen+1 {
		t.Errorf("deleting an absent key: %v, generation %d", err, k.Generation())
	}
	if err := k.DeleteDataKeys([]uint32{old}); err != nil || k.Generation() != gen+2 {
		t.Fatalf("delete: %v, generation %d", err, k.Generation())
	}
	p := opened(t, k, kek)
	if _, ok := p.DataKey(old); ok {
		t.Error("the deleted key is back after a reparse")
	}
	if got := p.DataKeyIDs(); len(got) != 1 || got[0] != id {
		t.Errorf("data keys %v", got)
	}
}

// ReplaceSlot (rotate-key) swaps only the replaced key's slot: the new key
// and every other slot open the keyring, the replaced key does not, and
// the keyring key and the data keys stay (coordinator decision on #437,
// 6640615690). Counter-proof: keeping the replaced slot lets the old key
// open the new keyring.
func TestReplaceSlot(t *testing.T) {
	k, kek := newTestKeyring(t)
	rk, nk := randomKey(t), randomKey(t)
	if err := k.AddSlot(rk, "recovery key", t0); err != nil {
		t.Fatal(err)
	}
	ringKey, ids, gen := append([]byte(nil), k.ringKey...), k.DataKeyIDs(), k.Generation()
	if err := k.ReplaceSlot(Fingerprint(rk), rk, "archive key", t0); err == nil {
		t.Error("replacing a key by itself accepted")
	}
	if err := k.ReplaceSlot("0123456789abcdef", nk, "archive key", t0); err == nil {
		t.Error("replacing an absent slot accepted")
	}
	if err := k.ReplaceSlot(Fingerprint(kek), rk, "archive key", t0); err == nil {
		t.Error("a key that already has a slot accepted")
	}
	if k.Generation() != gen {
		t.Fatalf("a refused ReplaceSlot changed the generation to %d", k.Generation())
	}
	later := t0.Add(time.Hour)
	if err := k.ReplaceSlot(Fingerprint(kek), nk, "archive key", later); err != nil {
		t.Fatal(err)
	}
	if k.Generation() != gen+1 || len(k.Slots()) != 2 {
		t.Fatalf("generation %d, slots %v", k.Generation(), k.fingerprints())
	}
	p, _ := reparse(t, k)
	if err := p.Open(kek); !errors.Is(err, ErrNoSlot) {
		t.Errorf("the replaced key: %v", err)
	}
	for _, key := range [][]byte{nk, rk} {
		q, _ := reparse(t, k)
		if err := q.Open(key); err != nil {
			t.Errorf("key %s: %v", Fingerprint(key), err)
		}
		if !slices.Equal(q.ringKey, ringKey) || !slices.Equal(q.DataKeyIDs(), ids) {
			t.Error("the keyring key or the data keys changed")
		}
	}
	if s, ok := k.Slot(Fingerprint(nk)); !ok || s.Label != "archive key" || !s.Created.Equal(later) {
		t.Errorf("new slot %s %q %v", s.Fingerprint, s.Label, s.Created)
	}
}

// The new slot takes the label it is given, never the replaced slot's:
// replacing a recovery key's slot (rotate-key --replace while a recovery
// key is configured, the lost-key case) yields an "archive key" slot
// (review round 2 of #519). Counter-proof: inheriting the old label
// leaves the new key labelled "recovery key".
func TestReplaceSlotLabel(t *testing.T) {
	k, kek := newTestKeyring(t)
	rk, nk := randomKey(t), randomKey(t)
	if err := k.AddSlot(rk, "recovery key", t0); err != nil {
		t.Fatal(err)
	}
	if err := k.ReplaceSlot(Fingerprint(rk), nk, "archive key", t0); err != nil {
		t.Fatal(err)
	}
	if s, ok := k.Slot(Fingerprint(nk)); !ok || s.Label != "archive key" {
		t.Errorf("new slot label %q", s.Label)
	}
	if s, ok := k.Slot(Fingerprint(kek)); !ok || s.Label != "archive key" {
		t.Errorf("untouched slot label %q", s.Label)
	}
	if k.HasSlot(Fingerprint(rk)) {
		t.Error("the replaced slot is still there")
	}
}
