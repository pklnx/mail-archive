package gobd

import (
	"bytes"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keepia/archive/internal/attest"
)

// A clean export verifies without findings, as a directory and as a ZIP,
// with and without the full chain and the leaves, and with the
// customer's off-host copy.
func TestVerifyClean(t *testing.T) {
	for _, zipped := range []bool{false, true} {
		for _, full := range []bool{false, true} {
			for _, leaves := range []bool{false, true} {
				name := map[bool]string{false: "dir", true: "zip"}[zipped] +
					map[bool]string{false: "", true: "+chain"}[full] + map[bool]string{false: "", true: "+leaves"}[leaves]
				t.Run(name, func(t *testing.T) {
					f := newFixture(t)
					f.fullChain, f.withLeave = full, leaves
					out := filepath.Join(t.TempDir(), "export")
					if zipped {
						out += ".zip"
					}
					f.write(t, out)
					fs, res := verify(t, out, VerifyOptions{Trusted: f.trusted(), Attestations: f.copyFile(t, f.chain, true)})
					if len(fs) != 0 {
						t.Fatalf("findings:\n%s", strings.Join(kinds(fs), "\n"))
					}
					if res.Messages != 4 || res.Statement == nil || res.Statement.Group.Name != "Meier GmbH" {
						t.Fatalf("result: %+v", res)
					}
					// Without the copy and the key: still clean, with notes.
					fs, res = verify(t, out, VerifyOptions{})
					if len(fs) != 0 || len(res.Notes) == 0 {
						t.Fatalf("without key: %v %v", kinds(fs), res.Notes)
					}
				})
			}
		}
	}
}

// Leaves next to the copy are used when the export has none.
func TestVerifyLeavesNextToCopy(t *testing.T) {
	f := newFixture(t)
	out := filepath.Join(t.TempDir(), "export")
	f.write(t, out)
	f.leaves[1] = bytes.Replace(f.leaves[1], []byte("+ 3 "+f.msgs[1].m.SHA256), []byte("+ 3 "+shaOf([]byte("x"))), 1)
	fs, _ := verify(t, out, VerifyOptions{Trusted: f.trusted(), Attestations: f.copyFile(t, f.chain, true)})
	if !hasFinding(fs, KindChain, "another SHA-256 in leaves file #1") {
		t.Fatalf("findings: %v", kinds(fs))
	}
}

// One changed byte in a message file, a table, SHA256SUMS or the
// statement is found.
func TestVerifyChangedByte(t *testing.T) {
	f := newFixture(t)
	src := filepath.Join(t.TempDir(), "export")
	f.write(t, src)
	eml := MessagePath(f.msgs[0].m.SHA256, f.msgs[0].m.SortAt, berlin(t))
	for _, c := range []struct{ file, kind string }{
		{eml, KindChanged},
		{MessagesFile, KindChanged},
		{LocationsFile, KindChanged},
		{ReadmeFile, KindChanged},
		{IndexFile, KindChanged},
		{SumsFile, KindManifest},
		{StatementFile, KindStatement},
		{ChainFile, KindChanged},
	} {
		t.Run(c.file, func(t *testing.T) {
			files := readTree(t, src)
			b := files[c.file]
			// Change a byte in the middle that keeps the format, so the
			// digest is what catches it.
			i := bytes.IndexAny(b[len(b)/2:], "aeinorst") + len(b)/2
			b[i] ^= 0x20
			dir := writeTree(t, files)
			fs, _ := verify(t, dir, VerifyOptions{Trusted: f.trusted()})
			if !hasFinding(fs, c.kind, "") {
				t.Fatalf("no %s finding: %v", c.kind, kinds(fs))
			}
		})
	}
}

// A removed, an extra and a renamed file are found.
func TestVerifyRemovedExtraRenamed(t *testing.T) {
	f := newFixture(t)
	src := filepath.Join(t.TempDir(), "export")
	f.write(t, src)
	loc := berlin(t)
	eml := MessagePath(f.msgs[2].m.SHA256, f.msgs[2].m.SortAt, loc)

	files := readTree(t, src)
	delete(files, eml)
	fs, _ := verify(t, writeTree(t, files), VerifyOptions{Trusted: f.trusted()})
	if !hasFinding(fs, KindMissingFile, eml) {
		t.Fatalf("removed: %v", kinds(fs))
	}

	files = readTree(t, src)
	extra := []byte("Subject: planted\r\n\r\n")
	files[MessagePath(shaOf(extra), f.msgs[0].m.SortAt, loc)] = extra
	fs, _ = verify(t, writeTree(t, files), VerifyOptions{Trusted: f.trusted()})
	if !hasFinding(fs, KindExtraFile, shaOf(extra)) {
		t.Fatalf("extra: %v", kinds(fs))
	}

	files = readTree(t, src)
	files["mail/2025/03/"+f.msgs[2].m.SHA256+".eml"] = files[eml]
	delete(files, eml)
	fs, _ = verify(t, writeTree(t, files), VerifyOptions{Trusted: f.trusted()})
	if !hasFinding(fs, KindMissingFile, eml) || !hasFinding(fs, KindExtraFile, "mail/2025/03/") {
		t.Fatalf("renamed: %v", kinds(fs))
	}

	files = readTree(t, src)
	files["notes.txt"] = []byte("hi")
	fs, _ = verify(t, writeTree(t, files), VerifyOptions{Trusted: f.trusted()})
	if !hasFinding(fs, KindName, "notes.txt") {
		t.Fatalf("unexpected file: %v", kinds(fs))
	}
}

func TestVerifyKeysAndChain(t *testing.T) {
	f := newFixture(t)
	src := filepath.Join(t.TempDir(), "export")
	f.write(t, src)

	// A key the customer does not trust.
	other := attest.Keys{attest.EncodeKey(otherKey().Public().(ed25519.PublicKey)): true}
	fs, _ := verify(t, src, VerifyOptions{Trusted: other})
	if !hasFinding(fs, KindStatement, "not trusted") || !hasFinding(fs, KindChain, "not trusted") {
		t.Fatalf("untrusted: %v", kinds(fs))
	}

	// The one entry is not in the copy: the copy has another chain.
	fs, _ = verify(t, src, VerifyOptions{Trusted: f.trusted(), Attestations: f.copyFile(t, resignChain(f.chain, otherKey()), false)})
	if !hasFinding(fs, KindChain, "is not in the copy") {
		t.Fatalf("one entry not in copy: %v", kinds(fs))
	}
	// A copy that is too short.
	fs, _ = verify(t, src, VerifyOptions{Trusted: f.trusted(), Attestations: f.copyFile(t, f.chain[:1], false)})
	if !hasFinding(fs, KindChain, "is not in the copy") {
		t.Fatalf("short copy: %v", kinds(fs))
	}

	// Review B1 of #507 (M16, M17): the shipped attestation must be the
	// one the statement names, also when it is genuinely signed.
	for name, c := range map[string]struct {
		chain  []attest.Entry
		detail string
	}{
		// More entries than the statement allows stop readChain itself
		// ("more entries than the statement says"); fewer reach M16.
		"M16 no entry for chain one": {nil, "attest/attestations.jsonl 0 entries, the statement says 1"},
		"two entries for chain one":  {f.chain, "attest/attestations.jsonl more entries than the statement says (1)"},
		"M17 another attestation":    {f.chain[:1], "attest/attestations.jsonl ends with attestation #1, the statement names #2 " + f.chain[1].Hash},
	} {
		t.Run(name, func(t *testing.T) {
			files := readTree(t, src)
			reseal(t, files, f.key, func(files map[string][]byte, _ *attest.Statement) {
				var b []byte
				for i := range c.chain {
					line, _ := attest.MarshalLine(&c.chain[i])
					b = append(b, line...)
				}
				files[ChainFile] = b
			})
			fs, _ := verify(t, writeTree(t, files), VerifyOptions{Trusted: f.trusted(), Attestations: f.copyFile(t, f.chain, false)})
			if len(fs) != 1 || !hasFinding(fs, KindChain, c.detail) {
				t.Fatalf("findings: %v", kinds(fs))
			}
		})
	}

	// A full chain that is not a prefix of the copy.
	f.fullChain = true
	full := filepath.Join(t.TempDir(), "full")
	f.write(t, full)
	cp := resignChain(f.chain, f.key)
	cp[0].Total++
	cp[0].Sign(f.key)
	cp[1].Prev = cp[0].Hash
	cp[1].Sign(f.key)
	fs, _ = verify(t, full, VerifyOptions{Trusted: f.trusted(), Attestations: f.copyFile(t, cp, false)})
	if !hasFinding(fs, KindChain, "not a prefix") {
		t.Fatalf("not a prefix: %v", kinds(fs))
	}

	// A message missing from its leaves file.
	f.withLeave = true
	f.fullChain = false
	lv := filepath.Join(t.TempDir(), "leaves")
	f.write(t, lv)
	files := readTree(t, lv)
	reseal(t, files, f.key, func(files map[string][]byte, _ *attest.Statement) {
		b := files[LeavesPath(1)]
		files[LeavesPath(1)] = bytes.Replace(b, []byte("+ 3 "+f.msgs[1].m.SHA256+"\n"), nil, 1)
	})
	fs, _ = verify(t, writeTree(t, files), VerifyOptions{Trusted: f.trusted(), Attestations: f.copyFile(t, f.chain, false)})
	if !hasFinding(fs, KindChain, "missing in leaves file #1") || !hasFinding(fs, KindChain, "does not match the leaves digest") {
		t.Fatalf("missing leaf: %v", kinds(fs))
	}
}

// resignChain signs every entry again with key, relinking prev.
func resignChain(chain []attest.Entry, key ed25519.PrivateKey) []attest.Entry {
	out := make([]attest.Entry, len(chain))
	prev := attest.ZeroHash
	for i, e := range chain {
		e.Prev = prev
		e.Sign(key)
		out[i] = e
		prev = e.Hash
	}
	return out
}

// T-G9: the group lines are signed; --expect-group matches by name,
// case-insensitively; a locations.csv owner outside the statement's owners
// is found even when manifest and signature were recomputed.
func TestVerifyGroupAndOwners(t *testing.T) {
	f := newFixture(t)
	src := filepath.Join(t.TempDir(), "export")
	f.write(t, src)

	files := readTree(t, src)
	files[StatementFile] = bytes.Replace(files[StatementFile], []byte(`group "Meier GmbH"`), []byte(`group "Schulz KG"`), 1)
	fs, _ := verify(t, writeTree(t, files), VerifyOptions{Trusted: f.trusted()})
	if !hasFinding(fs, KindStatement, "bad signature") {
		t.Fatalf("tampered group: %v", kinds(fs))
	}

	for _, c := range []struct {
		expect string
		ok     bool
	}{{"meier gmbh", true}, {"  Meier GmbH ", true}, {"Schulz KG", false}, {"none", false}} {
		e := c.expect
		fs, _ := verify(t, src, VerifyOptions{Trusted: f.trusted(), ExpectGroup: &e})
		if got := hasFinding(fs, KindGroup, ""); got == c.ok {
			t.Errorf("--expect-group %q: findings %v", c.expect, kinds(fs))
		}
	}

	files = readTree(t, src)
	reseal(t, files, f.key, func(files map[string][]byte, _ *attest.Statement) {
		files[LocationsFile] = bytes.Replace(files[LocationsFile], []byte(`;"ben";`), []byte(`;"kim";`), 1)
	})
	fs, _ = verify(t, writeTree(t, files), VerifyOptions{Trusted: f.trusted()})
	if len(fs) != 1 || !hasFinding(fs, KindOwner, `"kim"`) {
		t.Fatalf("owner: %v", kinds(fs))
	}

	// A name with " and a newline: one line in the statement, the same
	// name after verify.
	g := newFixture(t)
	g.st.Group.Name = "Meier \"Söhne\"\nGmbH"
	out := filepath.Join(t.TempDir(), "g.zip")
	g.write(t, out)
	e := "meier \"söhne\"\ngmbh"
	fs, res := verify(t, out, VerifyOptions{Trusted: g.trusted(), ExpectGroup: &e})
	if len(fs) != 0 || res.Statement.Group.Name != g.st.Group.Name {
		t.Fatalf("quoted name: %v %+v", kinds(fs), res.Statement.Group)
	}

	// No group: owners empty, locations without owner; --expect-group none.
	h := newFixture(t)
	h.st.Group, h.st.Owners = nil, nil
	for i := range h.locs {
		h.locs[i].Owner = ""
	}
	nog := filepath.Join(t.TempDir(), "nog")
	h.write(t, nog)
	none := "none"
	fs, _ = verify(t, nog, VerifyOptions{Trusted: h.trusted(), ExpectGroup: &none})
	if len(fs) != 0 {
		t.Fatalf("no group: %v", kinds(fs))
	}
	other := "Meier GmbH"
	fs, _ = verify(t, nog, VerifyOptions{Trusted: h.trusted(), ExpectGroup: &other})
	if !hasFinding(fs, KindGroup, "no customer group") {
		t.Fatalf("no group, expected one: %v", kinds(fs))
	}
}

// Changes that keep manifest and signature consistent are found by the
// checks of the content.
func TestVerifyResealedContent(t *testing.T) {
	f := newFixture(t)
	f.missing = []Missing{{SHA256: shaOf([]byte("lost")), SortAt: f.msgs[0].m.SortAt, Reason: "file missing"}}
	src := filepath.Join(t.TempDir(), "export")
	f.write(t, src)
	fs, _ := verify(t, src, VerifyOptions{Trusted: f.trusted()})
	if len(fs) != 1 || !hasFinding(fs, KindIncomplete, "1 message") {
		t.Fatalf("incomplete: %v", kinds(fs))
	}
	for name, c := range map[string]struct {
		mutate   func(files map[string][]byte, st *attest.Statement)
		kind     string
		part     string
		withCopy bool
	}{
		"count": {func(_ map[string][]byte, st *attest.Statement) { st.Locations++ }, KindIndex, "the statement says", false},
		"formula": {func(files map[string][]byte, _ *attest.Statement) {
			files[MessagesFile] = bytes.Replace(files[MessagesFile], []byte(`"'=SUM(A1)`), []byte(`"=SUM(A1)`), 1)
		}, KindIndex, "unquoted formula", false},
		"missing also exported": {func(files map[string][]byte, _ *attest.Statement) {
			files[MissingFile] = bytes.Replace(files[MissingFile], []byte(shaOf([]byte("lost"))), []byte(f.msgs[0].m.SHA256), 2)
		}, KindIndex, "also in missing.csv", false},
		"index columns": {func(files map[string][]byte, _ *attest.Statement) {
			files[IndexFile] = bytes.Replace(files[IndexFile], []byte("<Name>subject</Name>"), []byte("<Name>betreff</Name>"), 1)
		}, KindIndex, "columns", false},
		"index encoding": {func(files map[string][]byte, _ *attest.Statement) {
			files[IndexFile] = bytes.Replace(files[IndexFile], []byte(`encoding="UTF-8"`), []byte(`encoding="ISO-8859-1"`), 1)
		}, KindIndex, "UTF-8", false},
		"unattested claim": {func(files map[string][]byte, _ *attest.Statement) {
			files[MessagesFile] = bytes.Replace(files[MessagesFile], []byte(";4;2\r\n"), []byte(";4;\r\n"), 1)
		}, KindIndex, "its attestation is empty", false},
		"location of nothing": {func(files map[string][]byte, _ *attest.Statement) {
			files[LocationsFile] = append(files[LocationsFile], []byte(`"`+shaOf([]byte("nothing"))+`";"anna";"work";"imap";"INBOX";1;99;"";"";"2025-01-01T00:00:00Z";"2025-01-01T00:00:00Z";"";"";"";"";"";""`+"\r\n")...)
		}, KindIndex, "refers to a message that is not in messages.csv", false},
		// Review B1 of #507: one case per check that had none.
		"M3 seq outside its attestation": {mutate: func(files map[string][]byte, _ *attest.Statement) {
			files[MessagesFile] = bytes.Replace(files[MessagesFile], []byte(";4;2\r\n"), []byte(";4;1\r\n"), 1)
		}, kind: KindChain, part: "archive_seq 4 is outside attestation #1 (1 to 3)", withCopy: true},
		"M7 seq out of order": {mutate: func(files map[string][]byte, _ *attest.Statement) {
			files[MessagesFile] = bytes.Replace(files[MessagesFile], []byte(";3;1\r\n"), []byte(";1;1\r\n"), 1)
		}, kind: KindIndex, part: "messages.csv line 3: archive_seq is not in increasing order"},
		"M8 attestation newer than the statement's": {mutate: func(files map[string][]byte, _ *attest.Statement) {
			files[MessagesFile] = bytes.Replace(files[MessagesFile], []byte(";4;2\r\n"), []byte(";4;3\r\n"), 1)
		}, kind: KindIndex, part: "messages.csv line 4: attestation 3 is newer than the statement's"},
		"M9 file does not match the date": {mutate: func(files map[string][]byte, _ *attest.Statement) {
			files[MessagesFile] = bytes.Replace(files[MessagesFile], []byte(";01.02.2025;"), []byte(";01.03.2025;"), 1)
		}, kind: KindIndex, part: "messages.csv line 4: file does not match sha256 and date"},
		"M10 missing path of another message": {mutate: func(files map[string][]byte, _ *attest.Statement) {
			files[MissingFile] = bytes.Replace(files[MissingFile], []byte("/"+shaOf([]byte("lost"))+".eml"), []byte("/"+shaOf([]byte("x"))+".eml"), 1)
		}, kind: KindIndex, part: "missing.csv line 2: file is not the message's path"},
		"M11 place without owner": {mutate: func(files map[string][]byte, _ *attest.Statement) {
			files[LocationsFile] = bytes.Replace(files[LocationsFile], []byte(`;"ben";`), []byte(`;"";`), 1)
		}, kind: KindOwner, part: "locations.csv line 3: a place without owner in an export of a customer group"},
		"M12 attestation file without attestation": {mutate: func(_ map[string][]byte, st *attest.Statement) {
			st.AttestationSeq, st.AttestationHash = 0, ""
		}, kind: KindName, part: ChainFile + " unexpected file (the statement names no attestation)"},
		"M13 SHA256SUMS digest is not the name": {mutate: func(files map[string][]byte, _ *attest.Statement) {
			files[SumsFile] = bytes.Replace(files[SumsFile], []byte(f.msgs[0].m.SHA256+"  mail/"), []byte(shaOf([]byte("x"))+"  mail/"), 1)
		}, kind: KindManifest, part: "the digest in SHA256SUMS is not the file's name"},
		"M18 message file without row": {mutate: func(files map[string][]byte, st *attest.Statement) {
			files[MessagesFile] = removeRow(t, files[MessagesFile], f.msgs[3].m.SHA256)
			st.Messages--
			st.Bytes -= f.msgs[3].m.Size
		}, kind: KindIndex, part: MessagePath(f.msgs[3].m.SHA256, f.msgs[3].m.SortAt, berlin(t)) + " not in messages.csv"},
		"M19 message without place": {mutate: func(files map[string][]byte, st *attest.Statement) {
			files[LocationsFile] = removeRow(t, files[LocationsFile], f.msgs[3].m.SHA256)
			st.Locations--
		}, kind: KindIndex, part: MessagePath(f.msgs[3].m.SHA256, f.msgs[3].m.SortAt, berlin(t)) + ": the message has no row in locations.csv"},
		"M20 missing message twice": {mutate: func(files map[string][]byte, st *attest.Statement) {
			lines := bytes.SplitAfter(files[MissingFile], []byte("\r\n"))
			files[MissingFile] = append(files[MissingFile], lines[1]...)
			st.Missing++
		}, kind: KindIndex, part: "missing.csv line 3: the message is listed twice"},
	} {
		t.Run(name, func(t *testing.T) {
			files := readTree(t, src)
			reseal(t, files, f.key, c.mutate)
			opts := VerifyOptions{Trusted: f.trusted()}
			if c.withCopy {
				opts.Attestations = f.copyFile(t, f.chain, false)
			}
			fs, _ := verify(t, writeTree(t, files), opts)
			if !hasFinding(fs, c.kind, c.part) {
				t.Fatalf("no %s %q finding: %v", c.kind, c.part, kinds(fs))
			}
		})
	}
}

// removeRow removes the CSV row of sha.
func removeRow(t *testing.T, table []byte, sha string) []byte {
	t.Helper()
	var out []byte
	removed := false
	for _, l := range bytes.SplitAfter(table, []byte("\r\n")) {
		if bytes.HasPrefix(l, []byte(`"`+sha+`"`)) {
			removed = true
			continue
		}
		out = append(out, l...)
	}
	if !removed {
		t.Fatalf("no row of %s", sha)
	}
	return out
}

// An unsigned statement: a note without --public-key, a finding with it.
func TestVerifyUnsigned(t *testing.T) {
	f := newFixture(t)
	f.unsigned = true
	src := filepath.Join(t.TempDir(), "export")
	f.write(t, src)
	readme, _ := os.ReadFile(filepath.Join(src, ReadmeFile))
	if !bytes.Contains(readme, []byte("NOT signed")) {
		t.Fatal("README does not say the statement is unsigned")
	}
	fs, res := verify(t, src, VerifyOptions{})
	if len(fs) != 0 || !strings.Contains(strings.Join(res.Notes, "\n"), "not signed") {
		t.Fatalf("unsigned without key: %v %v", kinds(fs), res.Notes)
	}
	fs, _ = verify(t, src, VerifyOptions{Trusted: f.trusted()})
	if !hasFinding(fs, KindStatement, "not signed") {
		t.Fatalf("unsigned with key: %v", kinds(fs))
	}
}

func TestVerifyDirectoryEntries(t *testing.T) {
	f := newFixture(t)
	src := filepath.Join(t.TempDir(), "export")
	f.write(t, src)
	// README.txt replaced by a symbolic link to a copy outside the export:
	// same bytes, but an unpacker or a later reader may follow it.
	readme := filepath.Join(src, ReadmeFile)
	outside := filepath.Join(t.TempDir(), "README.txt")
	b, _ := os.ReadFile(readme)
	if err := os.WriteFile(outside, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(readme); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, readme); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "mail", "2024", "13"), 0o700); err != nil {
		t.Fatal(err)
	}
	fs, _ := verify(t, src, VerifyOptions{Trusted: f.trusted()})
	if !hasFinding(fs, KindName, "README.txt not a regular file (symbolic link") {
		t.Fatalf("symlink: %v", kinds(fs))
	}
	if !hasFinding(fs, KindName, "mail/2024/13/") {
		t.Fatalf("dir entries: %v", kinds(fs))
	}
}
