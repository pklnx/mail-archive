package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// TestSecretKeyCheck (#396): the first check value is recorded once; an
// accepted key replaces it with an audit entry that names the
// fingerprints, and accepting the same key again changes nothing.
func TestSecretKeyCheck(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	a, b := crypto.KeyCheck(bytes.Repeat([]byte{1}, 32)), crypto.KeyCheck(bytes.Repeat([]byte{2}, 32))

	if got, err := st.SecretKeyCheck(ctx); err != nil || got != nil {
		t.Fatalf("new instance: %x %v", got, err)
	}
	if ok, err := st.RecordSecretKeyCheck(ctx, a); err != nil || !ok {
		t.Fatalf("first record: %v %v", ok, err)
	}
	if ok, err := st.RecordSecretKeyCheck(ctx, b); err != nil || ok {
		t.Fatalf("second record: %v %v", ok, err)
	}
	if got, err := st.SecretKeyCheck(ctx); err != nil || !bytes.Equal(got, a) {
		t.Fatalf("after the records: %x %v", got, err)
	}

	accept := func(check []byte) []byte {
		t.Helper()
		e := store.InstanceEntry(store.ActorCLI, "instance.secret_key_accepted")
		prev, err := st.AcceptSecretKey(store.WithAudit(ctx, e), check)
		if err != nil {
			t.Fatal(err)
		}
		return prev
	}
	entries := func() []store.AuditRecord {
		t.Helper()
		var recs []store.AuditRecord
		f := store.AuditFilter{Since: time.Now().Add(-time.Hour), Until: time.Now().Add(time.Hour), Actions: []string{"instance.secret_key_accepted"}}
		if err := st.ExportAudit(ctx, f, func(r store.AuditRecord) error {
			recs = append(recs, r)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return recs
	}
	if prev := accept(b); !bytes.Equal(prev, a) {
		t.Fatalf("accept returned %x", prev)
	}
	if got, err := st.SecretKeyCheck(ctx); err != nil || !bytes.Equal(got, b) {
		t.Fatalf("after accept: %x %v", got, err)
	}
	recs := entries()
	if len(recs) != 1 {
		t.Fatalf("entries %+v", recs)
	}
	var details map[string]string
	if err := json.Unmarshal(recs[0].Details, &details); err != nil {
		t.Fatal(err)
	}
	if details["previous"] != crypto.KeyCheckFingerprint(a) || details["fingerprint"] != crypto.KeyCheckFingerprint(b) {
		t.Errorf("details %v", details)
	}
	if prev := accept(b); !bytes.Equal(prev, b) || len(entries()) != 1 {
		t.Errorf("the same key again: %x, %d entries", prev, len(entries()))
	}
}

// TestTwoFactorSecretsOpen (#396): the sealed TOTP secrets open with their
// key and not with another one.
func TestTwoFactorSecretsOpen(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	key := bytes.Repeat([]byte{7}, 32)
	sealer, _ := crypto.NewSealer(key)
	other, _ := crypto.NewSealer(bytes.Repeat([]byte{8}, 32))
	if tried, opened, err := st.TwoFactorSecretsOpen(ctx, sealer, 5); err != nil || tried != 0 || opened != 0 {
		t.Fatalf("none: %d %d %v", tried, opened, err)
	}
	newTwoFactorLogin(t, st) // alice with TOTP sealed under key
	if tried, opened, err := st.TwoFactorSecretsOpen(ctx, sealer, 5); err != nil || tried != 1 || opened != 1 {
		t.Errorf("its key: %d %d %v", tried, opened, err)
	}
	if tried, opened, err := st.TwoFactorSecretsOpen(ctx, other, 5); err != nil || tried != 1 || opened != 0 {
		t.Errorf("another key: %d %d %v", tried, opened, err)
	}
	if tried, _, err := st.TwoFactorSecretsOpen(ctx, sealer, 0); err != nil || tried != 0 {
		t.Errorf("limit 0: %d %v", tried, err)
	}
}
