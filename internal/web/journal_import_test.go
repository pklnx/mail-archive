package web

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/store"
)

// TestMessageImportOrigin checks that a location an import of files added
// to JOURNAL names the import's label, and that other locations have
// "import": null.
func TestMessageImportOrigin(t *testing.T) {
	f, acc := fillFixture(t)
	ctx := context.Background()
	journaled := receive(t, f, acc, "Message-ID: <journaled@example.com>\r\nSubject: journaled\r\n")

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "1.eml"), []byte("Message-ID: <old@example.com>\r\nSubject: old\r\n\r\nOld.\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	imp := &store.SMTPImport{AccountID: acc.ID, Label: "piler", Format: "eml", Source: dir}
	if err := f.st.CreateSMTPImport(ctx, imp); err != nil {
		t.Fatal(err)
	}
	folders, _, err := archive.JournalImportFolders("eml", dir)
	if err != nil {
		t.Fatal(err)
	}
	im := &archive.Importer{Store: f.st, Blobs: f.blobs}
	if res, err := im.ImportJournal(ctx, acc, imp, folders); err != nil || res.Total().Added != 1 {
		t.Fatalf("import %+v, %v", res, err)
	}

	var msgs struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(f.do("GET", "/api/messages", nil, 200), &msgs); err != nil {
		t.Fatal(err)
	}
	if len(msgs.Messages) != 2 {
		t.Fatalf("messages %+v", msgs)
	}
	for _, m := range msgs.Messages {
		raw := f.do("GET", "/api/messages/"+m.ID, nil, 200)
		var msg struct {
			Locations []locationJSON `json:"locations"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Fatal(err)
		}
		l := msg.Locations[0]
		if m.ID == journaled {
			if l.Filled || l.Import != nil || !bytes.Contains(raw, []byte(`"import":null`)) {
				t.Errorf("journaled location %+v\n%s", l, raw)
			}
		} else if !l.Filled || l.Envelope != nil || l.Fill != nil || l.Folder != store.SMTPFolder ||
			l.Import == nil || l.Import.ID != imp.ID || l.Import.Label != "piler" {
			t.Errorf("imported location %+v", l)
		}
	}
}
