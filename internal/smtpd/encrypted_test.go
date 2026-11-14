package smtpd

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/keepia/archive/internal/archive"
	"github.com/keepia/archive/internal/blobcrypt"
	"github.com/keepia/archive/internal/blobstore"
	"github.com/keepia/archive/internal/store"
)

func encryptingServer(t *testing.T) func(*Server) {
	return func(s *Server) {
		kek, err := blobcrypt.GenerateKEK()
		if err != nil {
			t.Fatal(err)
		}
		ring, err := blobcrypt.NewKeyring(kek, "archive key", time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Blobs.SetKeys(ring, true); err != nil {
			t.Fatal(err)
		}
	}
}

// The SMTP receiver stores encrypted files when encryption at rest is on
// (the writer path of serve), and they read back by their hash.
func TestReceiveEncrypted(t *testing.T) {
	f := newFixture(t, encryptingServer(t))
	raw := message("Zebrafink")
	if err := f.send("sender@example.com", []string{"archive+alice=example.com@archive.test"}, raw); err != nil {
		t.Fatal(err)
	}
	if got := f.envelopes("Zebrafink"); len(got["journal"]) != 1 {
		t.Fatalf("envelopes %v", got)
	}
	n := 0
	if err := f.srv.Blobs.Walk(func(e blobstore.Entry) error {
		if e.Kind != blobstore.EntryBlob || !e.Encrypted {
			t.Errorf("entry %+v", e)
			return nil
		}
		n++
		data, err := os.ReadFile(filepath.Join(f.srv.Blobs.Root(), filepath.FromSlash(e.Path)))
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte("Zebrafink")) {
			t.Errorf("plaintext in %s", e.Path)
		}
		m, err := f.srv.Blobs.OpenMessage(e.SHA256)
		if err != nil {
			return err
		}
		defer func() { _ = m.Close() }()
		got, err := io.ReadAll(m)
		if err != nil || string(got) != raw {
			t.Errorf("read back %q, %v", got, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("%d files", n)
	}
	res, err := archive.Verify(context.Background(), f.st, f.srv.Blobs, archive.VerifyOptions{Logger: f.srv.Logger, Encryption: store.BlobOn})
	if err != nil || res.Problems() != 0 || res.Checked != 1 {
		t.Fatalf("verify %+v %v", res, err)
	}
}

// A delivery after a missed change of encryption at rest gets 451: the
// insert fails on the generation (revision 5, B1 (c)).
func TestReceiveAfterStateChangeDefers(t *testing.T) {
	stopped := make(chan struct{}, 1)
	f := newFixture(t, func(s *Server) {
		s.Store.PinBlobGeneration(0, func() { stopped <- struct{}{} })
	})
	if err := f.st.SetBlobEncryption(context.Background(), store.BlobOn, store.KeyringCopy{
		Keyring: []byte("{}"), ID: "00000000000000000000000000000000", Generation: 1, CurrentKey: 1,
	}); err != nil {
		t.Fatal(err)
	}
	err := f.send("sender@example.com", []string{"archive+alice=example.com@archive.test"}, message("late"))
	if code(err) != 451 {
		t.Fatalf("delivery after the change: %v", err)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("the process was not told to stop")
	}
}
