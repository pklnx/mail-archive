package blobcrypt

import (
	"bytes"
	"crypto/subtle"
	"errors"
	"fmt"
	"maps"
	"slices"
)

// ErrOtherArchive means two keyrings belong to different archives (their
// archive ids differ), such as the database of one instance restored next
// to the data directory of another.
var ErrOtherArchive = errors.New("the keyring copies belong to different archives")

// ErrKeyConflict means two copies of a keyring hold the same data key id
// with different key material: their histories diverged in a way that
// cannot be merged.
var ErrKeyConflict = errors.New("the keyring copies hold the same data key id with different keys")

// Reconcile merges two opened copies of one archive's keyring, such as the
// file in the data directory and the copy in the database (#392, part 2).
// The copy with the newer generation decides the current data key and the
// slots; the data keys are the union of both, so a file encrypted under a
// key only one copy knows stays readable (restoring a data directory from
// before a rekey next to a database from after it). If the result differs
// from the newer copy (keys added, or both copies have the same generation
// but differ), its generation is one above both, so that each copy can be
// told apart from it and is written again.
//
// It refuses copies of different archives (ErrOtherArchive) and the same
// data key id with different material (ErrKeyConflict). Neither input is
// changed.
func Reconcile(a, b *Keyring) (*Keyring, error) {
	if a.keys == nil || b.keys == nil {
		return nil, ErrNotOpen
	}
	if a.archiveID != b.archiveID {
		return nil, fmt.Errorf("%w (archive ids %s and %s)", ErrOtherArchive, a.ArchiveID(), b.ArchiveID())
	}
	newer, older := a, b
	if b.generation > a.generation {
		newer, older = b, a
	}
	out := newer.clone()
	changed := false
	for _, id := range older.DataKeyIDs() {
		dk := older.keys[id]
		have, ok := out.keys[id]
		switch {
		case !ok:
			if len(out.keys) >= MaxDataKeys {
				return nil, fmt.Errorf("the union of the keyring copies holds more than %d data keys", MaxDataKeys)
			}
			out.keys[id] = bytes.Clone(dk)
			changed = true
		case subtle.ConstantTimeCompare(have, dk) != 1:
			return nil, fmt.Errorf("%w (data key %08x)", ErrKeyConflict, id)
		}
	}
	if !changed && a.generation == b.generation && !a.sameContent(b) {
		changed = true
	}
	if changed {
		out.generation = max(a.generation, b.generation)
		if err := out.bump(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// SameAs reports whether two opened copies are the same keyring: archive
// id, generation, current key, data keys and slots. A copy that is not the
// same as the result of Reconcile needs to be written again.
func (k *Keyring) SameAs(o *Keyring) bool {
	return k.keys != nil && o.keys != nil && k.archiveID == o.archiveID && k.generation == o.generation && k.sameContent(o)
}

// sameContent compares everything but the archive id and the generation.
func (k *Keyring) sameContent(o *Keyring) bool {
	if k.current != o.current || !bytes.Equal(k.ringKey, o.ringKey) || len(k.keys) != len(o.keys) {
		return false
	}
	for id, dk := range k.keys {
		if odk, ok := o.keys[id]; !ok || !bytes.Equal(dk, odk) {
			return false
		}
	}
	return slices.EqualFunc(k.slots, o.slots, func(x, y Slot) bool {
		return x.Fingerprint == y.Fingerprint && x.Label == y.Label && x.Created.Equal(y.Created) && bytes.Equal(x.wrapped, y.wrapped)
	})
}

// clone is a deep copy of an opened keyring.
func (k *Keyring) clone() *Keyring {
	c := &Keyring{
		archiveID: k.archiveID, generation: k.generation, current: k.current,
		sealedKeys: bytes.Clone(k.sealedKeys), ringKey: bytes.Clone(k.ringKey),
		keys: maps.Clone(k.keys),
	}
	for id, dk := range c.keys {
		c.keys[id] = bytes.Clone(dk)
	}
	for _, s := range k.slots {
		s.wrapped = bytes.Clone(s.wrapped)
		c.slots = append(c.slots, s)
	}
	return c
}
