package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/blobcrypt"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/store"
)

// encryptedFixture is newAPIFixture with every message file encrypted, as
// a web server started with encryption at rest on stores them.
func encryptedFixture(t *testing.T) *apiFixture {
	t.Helper()
	return newAPIFixtureWith(t, func(b *blobstore.Store) {
		kek, err := blobcrypt.GenerateKEK()
		if err != nil {
			t.Fatal(err)
		}
		ring, err := blobcrypt.NewKeyring(kek, "archive key", time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		if err := b.SetKeys(ring, true); err != nil {
			t.Fatal(err)
		}
	})
}

// addLarge stores a message of several 64 KiB chunks in alice's INBOX.
func (f *apiFixture) addLarge(raw string) string {
	f.t.Helper()
	ctx := context.Background()
	accs, err := f.st.ListAccounts(ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	var id int64
	for _, a := range accs {
		if a.Name == "alice" {
			id = a.ID
		}
	}
	folder, err := f.st.GetOrCreateFolder(ctx, id, "INBOX")
	if err != nil {
		f.t.Fatal(err)
	}
	blob, _, err := f.blobs.Put(strings.NewReader(raw))
	if err != nil {
		f.t.Fatal(err)
	}
	meta, err := archive.BuildMeta(ctx, f.st, f.blobs, blob, true)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.st.SaveBatch(ctx, folder.ID, 10, []store.MessageMeta{meta},
		[]store.Location{{FolderID: folder.ID, UIDValidity: 1, UID: 10}}); err != nil {
		f.t.Fatal(err)
	}
	return blob.SHA256
}

func (f *apiFixture) getRange(path, rng string) (*http.Response, string) {
	f.t.Helper()
	req, _ := http.NewRequest("GET", f.srv.URL+path, nil)
	req.Header.Set("Range", rng)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, string(body)
}

// /raw serves byte ranges from an encrypted file as from a plaintext one
// (http.ServeContent over OpenMessage's ReadSeeker), across chunk borders.
func TestEncryptedRawRanges(t *testing.T) {
	f := encryptedFixture(t)
	var sb strings.Builder
	sb.WriteString("Subject: large\r\nContent-Type: text/plain\r\n\r\n")
	for i := 0; sb.Len() < 3*blobcrypt.ChunkSize+500; i++ {
		fmt.Fprintf(&sb, "line %06d of a large message\r\n", i)
	}
	raw := sb.String()
	sha := f.addLarge(raw)
	if _, err := os.Stat(filepath.Join(f.blobs.Root(), filepath.FromSlash(blobstore.EncPath(sha)))); err != nil {
		t.Fatalf("not stored encrypted: %v", err)
	}
	path := "/api/messages/" + sha + "/raw"
	resp := f.get(path)
	body, _ := io.ReadAll(resp.Body)
	if string(body) != raw || resp.Header.Get("Accept-Ranges") != "bytes" || resp.ContentLength != int64(len(raw)) {
		t.Fatalf("full raw: %d bytes of %d, %v", len(body), len(raw), resp.Header)
	}
	n := len(raw)
	c := blobcrypt.ChunkSize
	for _, tc := range []struct {
		rng        string
		start, end int // inclusive
	}{
		{"bytes=0-9", 0, 9},
		{fmt.Sprintf("bytes=%d-%d", c-5, c+5), c - 5, c + 5},
		{fmt.Sprintf("bytes=%d-%d", c, 2*c), c, 2 * c},
		{fmt.Sprintf("bytes=%d-", n-100), n - 100, n - 1},
		{"bytes=-37", n - 37, n - 1},
	} {
		resp, body := f.getRange(path, tc.rng)
		want := raw[tc.start : tc.end+1]
		if resp.StatusCode != http.StatusPartialContent || body != want ||
			resp.Header.Get("Content-Range") != fmt.Sprintf("bytes %d-%d/%d", tc.start, tc.end, n) {
			t.Errorf("%s: %d %q, %d bytes (want %d)", tc.rng, resp.StatusCode, resp.Header.Get("Content-Range"), len(body), len(want))
		}
	}
	resp, body2 := f.getRange(path, "bytes=0-4,"+fmt.Sprint(c)+"-"+fmt.Sprint(c+4))
	if resp.StatusCode != http.StatusPartialContent || !strings.HasPrefix(resp.Header.Get("Content-Type"), "multipart/byteranges") ||
		!strings.Contains(body2, raw[:5]) || !strings.Contains(body2, raw[c:c+5]) {
		t.Errorf("multiple ranges: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if resp, _ := f.getRange(path, fmt.Sprintf("bytes=%d-", n+10)); resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("range past the end: %d", resp.StatusCode)
	}
}

// The detail, the parts and the served HTML of an encrypted archive are
// byte for byte those of the same plaintext archive.
func TestEncryptedPartsAndHTMLIdentical(t *testing.T) {
	plain := newAPIFixture(t)
	enc := encryptedFixture(t)
	for name, sha := range enc.ids {
		if _, err := os.Stat(filepath.Join(enc.blobs.Root(), filepath.FromSlash(blobstore.EncPath(sha)))); err != nil {
			t.Fatalf("%s not stored encrypted: %v", name, err)
		}
		if plain.ids[name] != sha {
			t.Fatalf("%s: the identity changed with encryption", name)
		}
	}
	paths := []string{}
	for _, name := range []string{"invoice", "meeting", "rich"} {
		paths = append(paths, "/api/messages/"+enc.ids[name], "/api/messages/"+enc.ids[name]+"/raw")
	}
	rich := enc.ids["rich"]
	paths = append(paths, "/api/messages/"+rich+"/html", "/api/messages/"+rich+"/html?images=1")
	for n := 0; n <= 3; n++ {
		paths = append(paths, fmt.Sprintf("/api/messages/%s/parts/%d", rich, n))
	}
	for _, p := range paths {
		pr := plain.get(p)
		pb, _ := io.ReadAll(pr.Body)
		er := enc.get(p)
		eb, _ := io.ReadAll(er.Body)
		if !strings.Contains(p, "/raw") && !strings.Contains(p, "/html") && !strings.Contains(p, "/parts/") {
			// The detail: only what comes from the file, not the database's
			// times of the two archives.
			pb, eb = fromFile(t, pb), fromFile(t, eb)
		}
		if pr.StatusCode != er.StatusCode || string(pb) != string(eb) || pr.Header.Get("Content-Type") != er.Header.Get("Content-Type") ||
			pr.Header.Get("Content-Disposition") != er.Header.Get("Content-Disposition") {
			t.Errorf("%s differs: %d vs %d, %d vs %d bytes", p, pr.StatusCode, er.StatusCode, len(pb), len(eb))
		}
		if pr.StatusCode != http.StatusOK && !strings.HasSuffix(p, "/parts/0") {
			t.Errorf("%s: status %d", p, pr.StatusCode)
		}
	}
}

// fromFile keeps the fields of a message detail that are parsed from the
// message file.
func fromFile(t *testing.T, body []byte) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	keep := map[string]any{}
	for _, k := range []string{"to", "cc", "dateHeader", "text", "hasHtml", "truncated", "parts"} {
		keep[k] = m[k]
	}
	out, err := json.Marshal(keep)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
