package blobcrypt

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"time"
)

// NewSlot is a key that gets a new slot when the keyring key is replaced.
type NewSlot struct {
	KEK     []byte
	Label   string
	Created time.Time
}

// Rewrap replaces the keyring key with a new random one and wraps it into
// a new set of slots: one for each key in keep, which must open its slot
// in k (its label and creation time stay), then one for each key in add
// (#392, part 5). Every other slot is dropped and returned. The data keys
// stay; Marshal seals them under the new keyring key. The keyring must be
// open; the generation increases.
//
// A key whose slot was dropped still opens older copies of the keyring,
// such as backups, and so the old keyring key; with the new keyring key it
// opens neither this keyring nor any data key added to it later
// (coordinator decision on #437, from the #506 reviews).
func (k *Keyring) Rewrap(keep [][]byte, add []NewSlot) (dropped []Slot, err error) {
	if k.keys == nil {
		return nil, ErrNotOpen
	}
	if len(keep)+len(add) == 0 {
		return nil, errors.New("refusing a keyring without slots: nothing could open it")
	}
	if len(keep)+len(add) > MaxSlots {
		return nil, fmt.Errorf("the keyring holds at most %d slots", MaxSlots)
	}
	next := &Keyring{archiveID: k.archiveID, ringKey: make([]byte, KeySize)}
	if _, err := rand.Read(next.ringKey); err != nil {
		return nil, err
	}
	kept := map[string]bool{}
	for _, kek := range keep {
		s, err := k.provenSlot(kek)
		if err != nil {
			return nil, err
		}
		if err := next.addSlot(kek, s.Label, s.Created); err != nil {
			return nil, err
		}
		kept[s.Fingerprint] = true
	}
	for _, n := range add {
		if err := next.addSlot(n.KEK, n.Label, n.Created); err != nil {
			return nil, err
		}
	}
	for _, s := range k.slots {
		if !kept[s.Fingerprint] {
			dropped = append(dropped, s)
		}
	}
	if err := k.bump(); err != nil {
		return nil, err
	}
	k.ringKey, k.slots = next.ringKey, next.slots
	return dropped, nil
}

// provenSlot returns the slot of kek, once kek has unwrapped it to the
// keyring key of k.
func (k *Keyring) provenSlot(kek []byte) (Slot, error) {
	if len(kek) != KeySize {
		return Slot{}, fmt.Errorf("a key must be %d bytes", KeySize)
	}
	fp := Fingerprint(kek)
	i := slices.IndexFunc(k.slots, func(s Slot) bool { return s.Fingerprint == fp })
	if i < 0 {
		return Slot{}, fmt.Errorf("%w (key fingerprint %s)", ErrNoSlot, fp)
	}
	aead, err := derivedGCM(kek, slotWrapInfo)
	if err != nil {
		return Slot{}, err
	}
	fpRaw, _ := hex.DecodeString(fp)
	ringKey, err := open(aead, k.slots[i].wrapped, k.slotAAD(fpRaw))
	if err != nil || subtle.ConstantTimeCompare(ringKey, k.ringKey) != 1 {
		return Slot{}, keyringCorrupt("the slot of key %s does not open this keyring", fp)
	}
	return k.slots[i], nil
}

// AddCurrentKey adds a new random data key and makes it the current one,
// for `encryption rekey` (#392, part 5). The keyring must be open; the
// generation increases.
func (k *Keyring) AddCurrentKey() (uint32, error) {
	if k.keys == nil {
		return 0, ErrNotOpen
	}
	id, err := k.addDataKey()
	if err != nil {
		return 0, err
	}
	k.current = id
	return id, k.bump()
}

// DeleteDataKeys removes the data keys with these ids; ids the keyring
// does not hold are ignored. The current key cannot be removed. The
// generation increases when a key was removed. The keyring must be open.
func (k *Keyring) DeleteDataKeys(ids []uint32) error {
	if k.keys == nil {
		return ErrNotOpen
	}
	if slices.Contains(ids, k.current) {
		return fmt.Errorf("refusing to delete the current data key %08x", k.current)
	}
	removed := false
	for _, id := range ids {
		if dk, ok := k.keys[id]; ok {
			clear(dk)
			delete(k.keys, id)
			removed = true
		}
	}
	if !removed {
		return nil
	}
	return k.bump()
}

// Slot returns the slot with this fingerprint.
func (k *Keyring) Slot(fingerprint string) (Slot, bool) {
	i := slices.IndexFunc(k.slots, func(s Slot) bool { return s.Fingerprint == fingerprint })
	if i < 0 {
		return Slot{}, false
	}
	return k.slots[i], true
}
