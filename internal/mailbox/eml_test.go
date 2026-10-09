package mailbox

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestListEML(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"1.eml":             "Subject: a\n\nx\n",
		"notes.txt":         "not mail",
		"export.zip":        "PK\x03\x04",
		"._1.eml":           "AppleDouble",
		"a/2.EML":           "Subject: b\n\nx\n",
		"a/b/3.eml":         "Subject: c\n\nx\n",
		".hidden/4.eml":     "Subject: d\n\nx\n",
		"empty-dir/.keep":   "",
		"other/readme.md":   "no mail here",
		"a/b/c/d/deep.eml":  "Subject: e\n\nx\n",
		"a/b/c/d/empty.eml": "",
	})
	if err := os.Symlink(filepath.Join(dir, "1.eml"), filepath.Join(dir, "a", "link.eml")); err != nil {
		t.Fatal(err)
	}
	folders, skipped, err := ListEML(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range folders {
		if f.Kind != KindEML {
			t.Errorf("kind %q", f.Kind)
		}
		names = append(names, f.Name)
	}
	if want := []string{"", "a", "a/b", "a/b/c/d"}; !slices.Equal(names, want) {
		t.Fatalf("folders = %q, want %q", names, want)
	}
	reasons := map[string]string{}
	for _, s := range skipped {
		reasons[filepath.ToSlash(s.Path)] = s.Reason
	}
	for path, want := range map[string]string{
		"notes.txt": "not an .eml file", "export.zip": ReasonArchive, "._1.eml": "hidden",
		".hidden": "hidden", "a/link.eml": "not a regular file", "other/readme.md": "not an .eml file",
	} {
		if reasons[path] != want {
			t.Errorf("%s: reason %q, want %q (all: %v)", path, reasons[path], want, reasons)
		}
	}

	files, err := EMLFiles(filepath.Join(dir, "a", "b", "c", "d"))
	if err != nil || len(files) != 2 || filepath.Base(files[0].Path) != "deep.eml" || files[1].Size != 0 {
		t.Fatalf("files = %+v, %v", files, err)
	}
	// Only the directory itself, not its subdirectories, without symlinks.
	files, err = EMLFiles(filepath.Join(dir, "a"))
	if err != nil || len(files) != 1 || filepath.Base(files[0].Path) != "2.EML" {
		t.Fatalf("files of a = %+v, %v", files, err)
	}
}

func TestEMLFilesSortByName(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"2.eml": "x", "10.eml": "x", "b.eml": "x", "A.eml": "x"})
	files, err := EMLFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range files {
		names = append(names, filepath.Base(f.Path))
	}
	if want := []string{"10.eml", "2.eml", "A.eml", "b.eml"}; !slices.Equal(names, want) {
		t.Fatalf("order %v, want %v (byte order)", names, want)
	}
}

func TestListEMLRefusesFiles(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"export.zip": "PK\x03\x04rest", "one.eml": "Subject: x\n\nx\n"})
	if _, _, err := ListEML(filepath.Join(dir, "export.zip")); !errors.Is(err, ErrCompressed) {
		t.Errorf("zip: %v", err)
	}
	if _, _, err := ListEML(filepath.Join(dir, "one.eml")); err == nil {
		t.Error("a single file was accepted")
	}
	if _, _, err := ListEML(filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
}
