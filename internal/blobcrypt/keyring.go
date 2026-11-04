package blobcrypt

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"
	"unicode/utf8"
)

// KeyringFormat is the "format" value of a keyring file.
const KeyringFormat = "mail-archive keyring 1"

// Keyring limits; a file beyond them is refused.
const (
	MaxSlots       = 16
	MaxDataKeys    = 64
	MaxLabelLen    = 64
	maxKeyringSize = 64 << 10
	archiveIDSize  = 16
)

// Domain separation strings of the keyring.
const (
	slotWrapInfo = "mail-archive keyslot v1"      // HKDF info: KEK -> slot wrapping key
	bodyKeyInfo  = "mail-archive keyring body v1" // HKDF info: keyring key -> body key
	slotAADTag   = "mail-archive keyslot v1\x00"
	bodyAADTag   = "mail-archive keyring v1\x00"
)

// Keyring errors.
var (
	// ErrKeyringCorrupt: the file is malformed, or a part of it fails
	// authentication although the KEK has a slot (it was modified).
	ErrKeyringCorrupt = errors.New("keyring is corrupt")
	// ErrNoSlot: the KEK has no slot in this keyring (another key, or
	// another archive's keyring).
	ErrNoSlot = errors.New("the archive key has no slot in this keyring")
	// ErrNotOpen: the operation needs the data keys, Open first.
	ErrNotOpen = errors.New("keyring is not open")
)

func keyringCorrupt(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrKeyringCorrupt, fmt.Sprintf(format, args...))
}

// Keyring holds the data keys (DKs) of an archive, wrapped under one or
// more KEK slots, like LUKS keyslots. Each slot wraps the keyring key; the
// keyring key seals the data keys together with the keyring's metadata
// (archive id, generation, current key and the slot list) as associated
// data, so none of it can be changed without a KEK. Each slot's associated
// data holds the archive id and its fingerprint.
type Keyring struct {
	archiveID  [archiveIDSize]byte
	generation uint64
	current    uint32
	slots      []Slot
	sealedKeys []byte // nonce ‖ sealed body, as read

	// Set once opened.
	ringKey []byte
	keys    map[uint32][]byte
}

// Slot is one KEK's entry in the keyring.
type Slot struct {
	Fingerprint string
	Label       string
	Created     time.Time
	wrapped     []byte // nonce ‖ sealed keyring key
}

// NewKeyring creates the keyring of a new archive: a random archive id and
// keyring key, one data key with a random id, and one slot for kek.
func NewKeyring(kek []byte, label string, now time.Time) (*Keyring, error) {
	k := &Keyring{generation: 1, keys: map[uint32][]byte{}}
	if _, err := rand.Read(k.archiveID[:]); err != nil {
		return nil, err
	}
	k.ringKey = make([]byte, KeySize)
	if _, err := rand.Read(k.ringKey); err != nil {
		return nil, err
	}
	id, err := k.addDataKey()
	if err != nil {
		return nil, err
	}
	k.current = id
	if err := k.addSlot(kek, label, now); err != nil {
		return nil, err
	}
	return k, nil
}

// addDataKey adds a random data key under a new random, non-zero id.
func (k *Keyring) addDataKey() (uint32, error) {
	if len(k.keys) >= MaxDataKeys {
		return 0, fmt.Errorf("the keyring holds the maximum of %d data keys", MaxDataKeys)
	}
	dk := make([]byte, KeySize)
	if _, err := rand.Read(dk); err != nil {
		return 0, err
	}
	for {
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			return 0, err
		}
		id := binary.BigEndian.Uint32(b[:])
		if _, taken := k.keys[id]; id != 0 && !taken {
			k.keys[id] = dk
			return id, nil
		}
	}
}

// ArchiveID is the archive's random id, fixed at keygen (32 hex digits).
func (k *Keyring) ArchiveID() string { return hex.EncodeToString(k.archiveID[:]) }

// Generation increases with every change of the keyring.
func (k *Keyring) Generation() uint64 { return k.generation }

// CurrentKey is the id of the data key new files are encrypted with.
func (k *Keyring) CurrentKey() uint32 { return k.current }

// Slots returns a copy of the slots.
func (k *Keyring) Slots() []Slot { return slices.Clone(k.slots) }

// Opened reports whether the data keys are available.
func (k *Keyring) Opened() bool { return k.keys != nil }

// DataKeyIDs lists the data key ids in ascending order; nil until opened.
func (k *Keyring) DataKeyIDs() []uint32 {
	if k.keys == nil {
		return nil
	}
	ids := make([]uint32, 0, len(k.keys))
	for id := range k.keys {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// DataKey implements DataKeys. It finds nothing until the keyring is open.
// It returns a copy, so a caller cannot change the keyring.
func (k *Keyring) DataKey(id uint32) ([]byte, bool) {
	dk, ok := k.keys[id]
	if !ok {
		return nil, false
	}
	return bytes.Clone(dk), true
}

// Current returns the current data key, for new files.
func (k *Keyring) Current() (uint32, []byte, error) {
	if k.keys == nil {
		return 0, nil, ErrNotOpen
	}
	return k.current, bytes.Clone(k.keys[k.current]), nil
}

// HasSlot reports whether a slot with this fingerprint exists.
func (k *Keyring) HasSlot(fingerprint string) bool {
	return slices.ContainsFunc(k.slots, func(s Slot) bool { return s.Fingerprint == fingerprint })
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func derivedGCM(secret []byte, info string) (cipher.AEAD, error) {
	key, err := hkdf.Key(sha256.New, secret, nil, info, KeySize)
	if err != nil {
		return nil, err
	}
	return newGCM(key)
}

func seal(aead cipher.AEAD, plaintext, aad []byte) ([]byte, error) {
	nonce := make([]byte, aead.NonceSize(), aead.NonceSize()+len(plaintext)+aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plaintext, aad), nil
}

func open(aead cipher.AEAD, sealed, aad []byte) ([]byte, error) {
	n := aead.NonceSize()
	if len(sealed) < n+aead.Overhead() {
		return nil, errors.New("too short")
	}
	return aead.Open(nil, sealed[:n], sealed[n:], aad)
}

func (k *Keyring) slotAAD(fingerprint []byte) []byte {
	b := make([]byte, 0, len(slotAADTag)+archiveIDSize+FingerprintSize)
	b = append(b, slotAADTag...)
	b = append(b, k.archiveID[:]...)
	return append(b, fingerprint...)
}

// bodyAAD is the canonical encoding of everything in the keyring but the
// sealed parts.
func (k *Keyring) bodyAAD() []byte {
	b := []byte(bodyAADTag)
	b = append(b, k.archiveID[:]...)
	b = binary.BigEndian.AppendUint64(b, k.generation)
	b = binary.BigEndian.AppendUint32(b, k.current)
	b = binary.BigEndian.AppendUint16(b, uint16(len(k.slots))) //nolint:gosec // at most MaxSlots
	for _, s := range k.slots {
		fp, _ := hex.DecodeString(s.Fingerprint)
		b = append(b, fp...)
		b = binary.BigEndian.AppendUint16(b, uint16(len(s.Label))) //nolint:gosec // at most MaxLabelLen
		b = append(b, s.Label...)
		b = binary.BigEndian.AppendUint64(b, uint64(s.Created.Unix())) //nolint:gosec // two's complement is fine
	}
	return b
}

func checkLabel(label string) error {
	if label == "" || len(label) > MaxLabelLen || !utf8.ValidString(label) {
		return fmt.Errorf("a slot label must be 1 to %d bytes of UTF-8", MaxLabelLen)
	}
	for _, r := range label {
		if r < 0x20 || r == 0x7f {
			return errors.New("a slot label must not contain control characters")
		}
	}
	return nil
}

func (k *Keyring) addSlot(kek []byte, label string, now time.Time) error {
	if len(kek) != KeySize {
		return fmt.Errorf("the archive key must be %d bytes", KeySize)
	}
	if err := checkLabel(label); err != nil {
		return err
	}
	fp := Fingerprint(kek)
	if k.HasSlot(fp) {
		return fmt.Errorf("the key %s already has a slot", fp)
	}
	if len(k.slots) >= MaxSlots {
		return fmt.Errorf("the keyring holds the maximum of %d slots", MaxSlots)
	}
	aead, err := derivedGCM(kek, slotWrapInfo)
	if err != nil {
		return err
	}
	fpRaw, _ := hex.DecodeString(fp)
	wrapped, err := seal(aead, k.ringKey, k.slotAAD(fpRaw))
	if err != nil {
		return err
	}
	k.slots = append(k.slots, Slot{Fingerprint: fp, Label: label, Created: now.UTC().Truncate(time.Second), wrapped: wrapped})
	return nil
}

func (k *Keyring) bump() error {
	if k.generation == ^uint64(0) {
		return errors.New("keyring generation overflow")
	}
	k.generation++
	return nil
}

// AddSlot adds a slot for another KEK, such as a recovery key. The keyring
// must be open; the generation increases.
func (k *Keyring) AddSlot(kek []byte, label string, now time.Time) error {
	if k.keys == nil {
		return ErrNotOpen
	}
	if err := k.addSlot(kek, label, now); err != nil {
		return err
	}
	return k.bump()
}

// RemoveSlot removes the slot with this fingerprint. The last slot cannot
// be removed. The keyring must be open; the generation increases. The
// removed KEK still opens older copies of the keyring.
func (k *Keyring) RemoveSlot(fingerprint string) error {
	if k.keys == nil {
		return ErrNotOpen
	}
	i := slices.IndexFunc(k.slots, func(s Slot) bool { return s.Fingerprint == fingerprint })
	if i < 0 {
		return fmt.Errorf("no slot with fingerprint %q", fingerprint)
	}
	if len(k.slots) == 1 {
		return errors.New("refusing to remove the last slot: nothing could open the keyring afterwards")
	}
	k.slots = slices.Delete(k.slots, i, i+1)
	return k.bump()
}

// Open unwraps the keyring key with kek and decrypts the data keys. It
// fails with ErrNoSlot when kek has no slot and with ErrKeyringCorrupt
// when the keyring was modified.
func (k *Keyring) Open(kek []byte) error {
	if len(kek) != KeySize {
		return fmt.Errorf("the archive key must be %d bytes", KeySize)
	}
	fp := Fingerprint(kek)
	i := slices.IndexFunc(k.slots, func(s Slot) bool { return s.Fingerprint == fp })
	if i < 0 {
		return fmt.Errorf("%w (key fingerprint %s; slots: %s)", ErrNoSlot, fp, k.fingerprints())
	}
	aead, err := derivedGCM(kek, slotWrapInfo)
	if err != nil {
		return err
	}
	fpRaw, _ := hex.DecodeString(fp)
	ringKey, err := open(aead, k.slots[i].wrapped, k.slotAAD(fpRaw))
	if err != nil || len(ringKey) != KeySize {
		return keyringCorrupt("the slot of key %s fails authentication", fp)
	}
	body, err := derivedGCM(ringKey, bodyKeyInfo)
	if err != nil {
		return err
	}
	plain, err := open(body, k.sealedKeys, k.bodyAAD())
	if err != nil {
		return keyringCorrupt("the data keys fail authentication (the keyring was modified)")
	}
	keys, err := parseDataKeys(plain)
	if err != nil {
		return err
	}
	if _, ok := keys[k.current]; !ok {
		return keyringCorrupt("the current data key %08x is missing", k.current)
	}
	k.ringKey, k.keys = ringKey, keys
	return nil
}

func (k *Keyring) fingerprints() string {
	fps := make([]string, len(k.slots))
	for i, s := range k.slots {
		fps[i] = s.Fingerprint
	}
	return fmt.Sprint(fps)
}

const dataKeyRecord = 4 + KeySize

func parseDataKeys(b []byte) (map[uint32][]byte, error) {
	if len(b) == 0 || len(b)%dataKeyRecord != 0 || len(b)/dataKeyRecord > MaxDataKeys {
		return nil, keyringCorrupt("malformed data keys")
	}
	keys := make(map[uint32][]byte, len(b)/dataKeyRecord)
	for ; len(b) > 0; b = b[dataKeyRecord:] {
		id := binary.BigEndian.Uint32(b)
		if _, dup := keys[id]; id == 0 || dup {
			return nil, keyringCorrupt("data key id 0 or repeated")
		}
		keys[id] = bytes.Clone(b[4:dataKeyRecord])
	}
	return keys, nil
}

// keyringJSON is the file format.
type keyringJSON struct {
	Format     string     `json:"format"`
	ArchiveID  string     `json:"archive_id"`
	Generation uint64     `json:"generation"`
	CurrentKey uint32     `json:"current_key"`
	Slots      []slotJSON `json:"slots"`
	DataKeys   string     `json:"sealed"` // base64 of nonce ‖ sealed data keys (not "…key…": secret scanners)
}

type slotJSON struct {
	Fingerprint string `json:"fingerprint"`
	Label       string `json:"label"`
	Created     string `json:"created"` // RFC 3339, UTC, seconds
	Wrapped     string `json:"wrapped"` // base64 of nonce ‖ sealed keyring key
}

// Marshal seals the data keys with the current metadata and returns the
// file contents. The keyring must be open.
func (k *Keyring) Marshal() ([]byte, error) {
	if k.keys == nil {
		return nil, ErrNotOpen
	}
	plain := make([]byte, 0, len(k.keys)*dataKeyRecord)
	for _, id := range k.DataKeyIDs() {
		plain = binary.BigEndian.AppendUint32(plain, id)
		plain = append(plain, k.keys[id]...)
	}
	body, err := derivedGCM(k.ringKey, bodyKeyInfo)
	if err != nil {
		return nil, err
	}
	sealed, err := seal(body, plain, k.bodyAAD())
	if err != nil {
		return nil, err
	}
	k.sealedKeys = sealed
	out := keyringJSON{
		Format: KeyringFormat, ArchiveID: k.ArchiveID(), Generation: k.generation, CurrentKey: k.current,
		DataKeys: base64.StdEncoding.EncodeToString(sealed),
	}
	for _, s := range k.slots {
		out.Slots = append(out.Slots, slotJSON{
			Fingerprint: s.Fingerprint, Label: s.Label, Created: s.Created.UTC().Format(time.RFC3339),
			Wrapped: base64.StdEncoding.EncodeToString(s.wrapped),
		})
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// ParseKeyring reads a keyring file strictly. The result is not open: its
// metadata is not authenticated until Open succeeds.
func ParseKeyring(data []byte) (*Keyring, error) {
	if len(data) > maxKeyringSize {
		return nil, keyringCorrupt("larger than %d bytes", maxKeyringSize)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var in keyringJSON
	if err := dec.Decode(&in); err != nil {
		return nil, keyringCorrupt("not a keyring file")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, keyringCorrupt("trailing data")
	}
	if in.Format != KeyringFormat {
		return nil, keyringCorrupt("unknown format %q", in.Format)
	}
	k := &Keyring{generation: in.Generation, current: in.CurrentKey}
	id, err := hex.DecodeString(in.ArchiveID)
	if err != nil || len(id) != archiveIDSize || hex.EncodeToString(id) != in.ArchiveID {
		return nil, keyringCorrupt("archive id is not %d lowercase hex digits", 2*archiveIDSize)
	}
	copy(k.archiveID[:], id)
	switch {
	case k.generation == 0:
		return nil, keyringCorrupt("generation 0")
	case k.current == 0:
		return nil, keyringCorrupt("current key 0")
	case len(in.Slots) == 0 || len(in.Slots) > MaxSlots:
		return nil, keyringCorrupt("want 1 to %d slots, got %d", MaxSlots, len(in.Slots))
	}
	wrappedLen := nonceSize + KeySize + TagSize
	for _, s := range in.Slots {
		fp, err := hex.DecodeString(s.Fingerprint)
		if err != nil || len(fp) != FingerprintSize || hex.EncodeToString(fp) != s.Fingerprint {
			return nil, keyringCorrupt("slot fingerprint is not %d lowercase hex digits", 2*FingerprintSize)
		}
		if k.HasSlot(s.Fingerprint) {
			return nil, keyringCorrupt("slot %s appears twice", s.Fingerprint)
		}
		if err := checkLabel(s.Label); err != nil {
			return nil, keyringCorrupt("slot %s: %v", s.Fingerprint, err)
		}
		created, err := time.Parse(time.RFC3339, s.Created)
		if err != nil || created.UTC().Format(time.RFC3339) != s.Created {
			return nil, keyringCorrupt("slot %s: creation time is not RFC 3339 UTC", s.Fingerprint)
		}
		wrapped, err := base64.StdEncoding.Strict().DecodeString(s.Wrapped)
		if err != nil || len(wrapped) != wrappedLen {
			return nil, keyringCorrupt("slot %s: malformed wrapped key", s.Fingerprint)
		}
		k.slots = append(k.slots, Slot{Fingerprint: s.Fingerprint, Label: s.Label, Created: created, wrapped: wrapped})
	}
	sealed, err := base64.StdEncoding.Strict().DecodeString(in.DataKeys)
	n := len(sealed) - nonceSize - TagSize
	if err != nil || n <= 0 || n%dataKeyRecord != 0 || n/dataKeyRecord > MaxDataKeys {
		return nil, keyringCorrupt("malformed data keys")
	}
	k.sealedKeys = sealed
	return k, nil
}

// SameKEK reports whether a and b are the same key, in constant time.
func SameKEK(a, b []byte) bool { return subtle.ConstantTimeCompare(a, b) == 1 }
