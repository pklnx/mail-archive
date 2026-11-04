package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/attest"
	"github.com/pklnx/mail-archive/internal/gobd"
)

// writeTestExport writes a one-message export signed with key into a new
// directory and returns its path.
func writeTestExport(t *testing.T, key ed25519.PrivateKey) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "export")
	sink, err := gobd.NewDirSink(dir)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("Subject: Rechnung\r\n\r\nHallo\r\n")
	sum := sha256.Sum256(body)
	m := gobd.Message{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body)),
		SortAt: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC), Subject: "Rechnung",
		ArchivedAt: time.Date(2025, 1, 2, 4, 0, 0, 0, time.UTC), ArchiveSeq: 1}
	w, err := gobd.NewWriter(sink, gobd.Options{
		Statement: attest.Statement{
			ID: strings.Repeat("ab", 16), Created: time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC),
			Software: "test", Selection: `{"users":["anna"]}`, TimeZone: "UTC",
			Group: &attest.StatementGroup{ID: 3, Name: "Meier GmbH"}, Owners: []string{"anna"},
		},
		Location: time.UTC, Signer: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	path := gobd.MessagePath(m.SHA256, m.SortAt, time.UTC)
	steps := []error{
		w.AddFile(m, bytes.NewReader(body)),
		w.AddMessageRow(m),
		w.AddLocationRow(gobd.Location{SHA256: m.SHA256, Owner: "anna", Account: "work", AccountKind: "imap", Folder: "INBOX",
			UIDValidity: 1, UID: 1, FirstSeen: m.ArchivedAt, LastSeen: m.ArchivedAt}),
	}
	for _, err := range steps {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.Finish(gobd.Final{Files: func(yield func(string) error) error { return yield(path) }}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runExportVerifyCmd(t *testing.T, args ...string) (int, string) {
	t.Helper()
	cmd := newExportCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := runCmd(t, cmd, "", append([]string{"verify"}, args...)...)
	return exitCodeOf(err), out.String()
}

func TestExportVerifyCommand(t *testing.T) {
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{5}, ed25519.SeedSize))
	dir := writeTestExport(t, key)
	pubPEM, _ := attest.MarshalPublicKey(key.Public().(ed25519.PublicKey))
	pub := filepath.Join(t.TempDir(), "signing.pub")
	if err := os.WriteFile(pub, pubPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	code, out := runExportVerifyCmd(t, dir, "--public-key", pub, "--expect-group", "meier gmbh")
	if code != 0 || !strings.Contains(out, "checked 1 message file(s): no problems") ||
		!strings.Contains(out, `customer group "Meier GmbH" (no. 3)`) || !strings.Contains(out, "owners: anna") {
		t.Fatalf("clean: exit %d\n%s", code, out)
	}

	code, out = runExportVerifyCmd(t, dir, "--public-key", pub, "--expect-group", "Schulz KG")
	if code != 1 || !strings.Contains(out, "group ") || !strings.Contains(out, "1 group") {
		t.Fatalf("other group: exit %d\n%s", code, out)
	}

	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{6}, ed25519.SeedSize))
	otherPEM, _ := attest.MarshalPublicKey(other.Public().(ed25519.PublicKey))
	if err := os.WriteFile(pub, otherPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	code, out = runExportVerifyCmd(t, dir)
	if code != 0 || !strings.Contains(out, "note: no trusted key") {
		t.Fatalf("without --public-key: exit %d\n%s", code, out)
	}
	code, out = runExportVerifyCmd(t, dir, "--public-key", pub)
	if code != 1 || !strings.Contains(out, "statement") || !strings.Contains(out, "not trusted") {
		t.Fatalf("untrusted key: exit %d\n%s", code, out)
	}

	// A name with an escape sequence is printed quoted, never raw (review
	// A2 of #507).
	evil := filepath.Join(dir, "x\x1b[2J\x1b[1;1Hchecked 1 message file(s): no problems")
	if err := os.WriteFile(evil, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out = runExportVerifyCmd(t, dir)
	if code != 1 || strings.Contains(out, "\x1b") || !strings.Contains(out, `"x\x1b[2J\x1b[1;1Hchecked`) {
		t.Fatalf("control characters: exit %d\n%q", code, out)
	}
	if err := os.Remove(evil); err != nil {
		t.Fatal(err)
	}

	// One changed byte in messages.csv.
	csv := filepath.Join(dir, gobd.MessagesFile)
	b, _ := os.ReadFile(csv)
	if err := os.WriteFile(csv, bytes.Replace(b, []byte("Rechnung"), []byte("Rechnunk"), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out = runExportVerifyCmd(t, dir)
	if code != 1 || !strings.Contains(out, "changed      messages.csv (does not match SHA256SUMS)") {
		t.Fatalf("changed: exit %d\n%s", code, out)
	}

	for _, args := range [][]string{{}, {dir, dir}, {filepath.Join(t.TempDir(), "none")}, {dir, "--public-key", "/nonexistent"}, {dir, "--bogus"}} {
		if code, out := runExportVerifyCmd(t, args...); code != 2 {
			t.Errorf("%v: exit %d\n%s", args, code, out)
		}
	}
}
