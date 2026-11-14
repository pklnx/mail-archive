package gobd

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keepia/archive/internal/csvsafe"
)

// The parsers of untrusted input never panic, and what they accept is in
// the form the writer produces.

func FuzzScanTable(f *testing.F) {
	sha := strings.Repeat("c", 64)
	f.Add(header(missingColumns) + `"` + sha + `";"mail/2024/01/` + sha + `.eml";"gone"` + "\r\n")
	f.Add(header(missingColumns) + `"` + sha + `";"x;y";"=1"` + "\r\n")
	f.Add(header(missingColumns) + "\r\n\r\n\"\"\"\r\n")
	f.Add(header(messageColumns) + strings.Repeat(`"a";`, 16) + "\r\n")
	f.Add("\xef\xbb\xbf" + header(missingColumns))
	f.Fuzz(func(t *testing.T, in string) {
		for _, cols := range [][]column{missingColumns, messageColumns, locationColumns} {
			err := scanTable(strings.NewReader(in), cols, func(_ int, vals []string) {
				if len(vals) != len(cols) {
					t.Fatalf("%d values for %d columns", len(vals), len(cols))
				}
				for i, v := range vals {
					if cols[i].kind == text && (csvsafe.NeedsQuote(v) || strings.ContainsAny(v, "\"\r\n\t")) {
						t.Fatalf("accepted text %q", v)
					}
				}
			}, func(int, string) {})
			if err != nil {
				t.Fatal(err)
			}
		}
	})
}

func FuzzParseSums(f *testing.F) {
	sha := strings.Repeat("d", 64)
	f.Add(sumsLine(sha, "README.txt") + sumsLine(sha, "mail/2024/01/"+sha+".eml"))
	f.Add(sha + " *x\n")
	f.Add("\\" + sha + "  a\\nb\n")
	f.Fuzz(func(t *testing.T, in string) {
		var b strings.Builder
		_, err := ParseSums(strings.NewReader(in), func(p, s string) error {
			b.WriteString(sumsLine(s, p))
			return nil
		})
		if err == nil && b.String() != in {
			t.Fatalf("accepted a non-canonical file:\n%q\n%q", in, b.String())
		}
	})
}

func FuzzCheckIndex(f *testing.F) {
	f.Add(IndexXML("mail-archive v1"))
	f.Add([]byte(`<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE x [<!ENTITY a "aaaa">]><DataSet>&a;</DataSet>`))
	f.Add([]byte(`<?xml version="1.0" encoding="UTF-8"?><DataSet><Media><Table><URL>messages.csv</URL></Table></Media></DataSet>`))
	f.Add([]byte(`<?xml version="1.0" encoding="UTF-8"?><DataSet></DataSet>`))
	f.Fuzz(func(t *testing.T, in []byte) {
		_ = checkIndex(bytes.NewReader(in))
	})
}

// FuzzVerifyZIP runs Verify on arbitrary bytes as a ZIP: it never panics
// and never needs more than the prescan allows.
func FuzzVerifyZIP(f *testing.F) {
	fx := newFixture(f)
	p := filepath.Join(f.TempDir(), "seed.zip")
	fx.write(f, p)
	clean, err := os.ReadFile(p)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(clean)
	f.Add(clean[:len(clean)/2])
	f.Add(append([]byte("junk"), clean...))
	// Crafted seeds: duplicate names, traversal, a directory and a symlink.
	for _, extra := range []zipEntryT{
		{name: MessagesFile, data: []byte("evil")},
		{name: "../x", data: []byte("x")},
		{name: "mail/x", data: []byte("x")},
		{name: "Messages.csv", data: []byte("x")},
		{name: "mail/", mode: os.ModeDir | 0o700},
		{name: "attest/l", data: []byte("/"), mode: os.ModeSymlink | 0o777},
	} {
		var b bytes.Buffer
		zw := zip.NewWriter(&b)
		zr, _ := zip.NewReader(bytes.NewReader(clean), int64(len(clean)))
		for _, zf := range zr.File {
			_ = zw.Copy(zf)
		}
		h := &zip.FileHeader{Name: extra.name, Method: zip.Deflate}
		h.SetMode(cmpOr(extra.mode, 0o600))
		w, _ := zw.CreateHeader(h)
		_, _ = w.Write(extra.data)
		_ = zw.Close()
		f.Add(b.Bytes())
	}
	for _, c := range localHeaderZIPs(f) {
		f.Add(c.data)
	}
	trusted := fx.trusted()
	f.Fuzz(func(t *testing.T, data []byte) {
		p := filepath.Join(t.TempDir(), "in.zip")
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Verify(p, VerifyOptions{Trusted: trusted}); err != nil {
			t.Fatal(err)
		}
	})
}

func cmpOr(m, def os.FileMode) os.FileMode {
	if m == 0 {
		return def
	}
	return m
}
