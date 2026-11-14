package archive

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/keepia/archive/internal/archive/gobdtest"
	"github.com/keepia/archive/internal/attest"
	"github.com/keepia/archive/internal/blobcrypt"
	"github.com/keepia/archive/internal/blobstore"
	"github.com/keepia/archive/internal/gobd"
	"github.com/keepia/archive/internal/store"
	"github.com/keepia/archive/internal/testwait"
)

// gobdRun is one export of the fixture with fixed clock and id.
type gobdRun struct {
	f    *gobdtest.Fixture
	opts GoBDOptions
}

var gobdClock = time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)

func newGoBDRun(f *gobdtest.Fixture, out string) *gobdRun {
	return &gobdRun{f: f, opts: GoBDOptions{
		Out:       out,
		AttestDir: filepath.Join(f.DataDir, "attest"),
		Software:  "test",
		Now:       func() time.Time { return gobdClock },
		NewID:     func() string { return "0123456789abcdef0123456789abcdef" },
		Selection: GoBDSelection{TimeZone: "Europe/Berlin"},
	}}
}

// users selects whole users.
func (r *gobdRun) users(us ...*store.User) *gobdRun {
	for _, u := range us {
		r.opts.Selection.Owners = append(r.opts.Selection.Owners, u.ID)
	}
	return r
}

// account selects an account with all or some folders.
func (r *gobdRun) account(a *store.Account, folders ...int64) *gobdRun {
	if r.opts.Selection.Accounts == nil {
		r.opts.Selection.Accounts = map[int64][]int64{}
	}
	if len(folders) == 0 {
		folders = nil
	}
	r.opts.Selection.Accounts[a.ID] = folders
	return r
}

func (r *gobdRun) claim(g *store.Group) *gobdRun {
	if g == nil {
		r.opts.Selection.Group = GroupClaim{Given: true}
		return r
	}
	id := g.ID
	r.opts.Selection.Group = GroupClaim{Given: true, ID: &id, Name: g.Name}
	return r
}

func (r *gobdRun) run(t *testing.T) (*GoBDResult, error) {
	t.Helper()
	if r.opts.Selection.Text == "" {
		r.opts.Selection.Text = GoBDSelectionText{TimeZone: r.opts.Selection.TimeZone}.JSON()
	}
	return GoBDExport(context.Background(), r.f.St, r.f.Blobs, r.opts)
}

func (r *gobdRun) mustRun(t *testing.T) *GoBDResult {
	t.Helper()
	res, err := r.run(t)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// verifyExport runs export verify and fails on any finding.
func verifyExport(t *testing.T, path string, trusted attest.Keys) *gobd.VerifyResult {
	t.Helper()
	var findings []string
	res, err := gobd.Verify(path, gobd.VerifyOptions{Trusted: trusted, OnFinding: func(f gobd.Finding) {
		findings = append(findings, f.Kind+" "+f.Path+" "+f.Detail)
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Problems() > 0 {
		t.Fatalf("verify %s: %v", path, findings)
	}
	return res
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// keygen creates the attestation key of the fixture.
func keygen(t *testing.T, f *gobdtest.Fixture) ed25519.PrivateKey {
	t.Helper()
	dir := filepath.Join(f.DataDir, "attest")
	keyFile := filepath.Join(dir, attest.DefaultKeyFile)
	if _, err := attest.Keygen(keyFile, dir); err != nil {
		t.Fatal(err)
	}
	key, err := attest.LoadSigner(keyFile, dir)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// csvRows returns the data lines of a table, header left out.
func csvRows(t *testing.T, dir, table string) []string {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(readFile(t, filepath.Join(dir, table)), "\r\n"), "\r\n")
	return lines[1:]
}

// emls lists the message files of a directory export.
func emls(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(filepath.Join(dir, "mail"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return err
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return out
}

func trustKey(key ed25519.PrivateKey) attest.Keys {
	return attest.Keys{attest.EncodeKey(key.Public().(ed25519.PublicKey)): true}
}

// TestGoBDExportVerified: a signed export of two owners of one group
// passes export verify with the trusted key, and the statement names the
// group and the owners.
func TestGoBDExportVerified(t *testing.T) {
	f := gobdtest.New(t)
	key := keygen(t, f)
	out := filepath.Join(t.TempDir(), "export")
	r := newGoBDRun(f, out).users(f.Anna, f.Ben).claim(f.A)
	r.opts.Signer = key
	res := r.mustRun(t)
	if res.Attested == 0 {
		t.Error("no attestation before the export")
	}
	v := verifyExport(t, out, trustKey(key))
	if v.Messages != 3 {
		t.Errorf("messages %d", v.Messages)
	}
	st := readFile(t, filepath.Join(out, gobd.StatementFile))
	for _, want := range []string{fmt.Sprintf("group-id %d\n", f.A.ID), "group \"Meier GmbH\"\n", "owners [\"anna\",\"ben\"]\n",
		"complete yes\n", "messages 3\n", "locations 4\n", "resumed no\n", "chain one\n", fmt.Sprintf("attestation %d ", res.Attested)} {
		if !strings.Contains(st, want) {
			t.Errorf("statement lacks %q:\n%s", want, st)
		}
	}
	if _, err := os.Stat(out + gobd.PartialSuffix); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("partial left: %v", err)
	}
	// Modes: directories 0700, files 0600; nothing else is left over.
	err := filepath.WalkDir(out, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, _ := d.Info()
		want := os.FileMode(0o600)
		if d.IsDir() {
			want = 0o700
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s: mode %v", p, fi.Mode().Perm())
		}
		if strings.HasPrefix(d.Name(), ".") {
			t.Errorf("left over: %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestGoBDSelection: accounts, folders, the period and its time zone, a
// message in two folders, removed accounts, and another customer's place
// of the same message (T-G13).
func TestGoBDSelection(t *testing.T) {
	f := gobdtest.New(t)
	dir := t.TempDir()
	n := 0
	export := func(r *gobdRun) string {
		t.Helper()
		r.mustRun(t)
		verifyExport(t, r.opts.Out, nil)
		return r.opts.Out
	}
	next := func() string { n++; return filepath.Join(dir, fmt.Sprint(n)) }

	// One folder of one account.
	out := export(newGoBDRun(f, next()).account(f.AnnaWork, f.Folders["anna/work/Archiv"]))
	if got := csvRows(t, out, gobd.LocationsFile); len(got) != 1 || !strings.Contains(got[0], `"Archiv"`) {
		t.Errorf("folder Archiv: %v", got)
	}
	// The whole account: the message in INBOX and Archiv is one file and
	// two locations.
	out = export(newGoBDRun(f, next()).account(f.AnnaWork))
	if files, locs := emls(t, out), csvRows(t, out, gobd.LocationsFile); len(files) != 2 || len(locs) != 3 {
		t.Errorf("anna/work: %v %v", files, locs)
	}
	// T-G13: the message anna and kim received is exported once, with
	// anna's place only; kim appears nowhere.
	walk := func(dir string, fn func(name string, data []byte)) {
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				data, _ := os.ReadFile(p)
				fn(p, data)
			}
			return err
		})
	}
	walk(out, func(name string, data []byte) {
		if bytes.Contains(data, []byte("kim")) || bytes.Contains(data, []byte("Schulz")) {
			t.Errorf("%s names kim's account or group", name)
		}
	})
	shared := 0
	for _, l := range csvRows(t, out, gobd.LocationsFile) {
		if strings.HasPrefix(l, `"`+f.Shared+`"`) {
			shared++
		}
	}
	if shared != 1 {
		t.Errorf("%d locations of the shared message", shared)
	}

	// The period in the export time zone: 2024-12-31 23:30 UTC is the
	// first of January in Berlin.
	since := time.Date(2025, 1, 1, 0, 0, 0, 0, mustLoc(t, "Europe/Berlin"))
	r := newGoBDRun(f, next()).users(f.Anna)
	r.opts.Selection.Since = &since
	out = export(r)
	files := emls(t, out)
	if len(files) != 1 || files[0] != "mail/2025/01/"+f.AnnaMsg+".eml" {
		t.Errorf("since 2025 in Berlin: %v", files)
	}
	until := since
	r = newGoBDRun(f, next()).users(f.Anna)
	r.opts.Selection.Until = &until
	out = export(r)
	if files := emls(t, out); len(files) != 1 || !strings.Contains(files[0], f.Shared) {
		t.Errorf("until 2025 in Berlin: %v", files)
	}
	// In UTC the same message is in December.
	r = newGoBDRun(f, next()).account(f.AnnaWork, f.Folders["anna/work/Archiv"])
	r.opts.Selection.TimeZone = "UTC"
	out = export(r)
	if files := emls(t, out); len(files) != 1 || files[0] != "mail/2024/12/"+f.AnnaMsg+".eml" {
		t.Errorf("UTC: %v", files)
	}

	// A folder of an account without an owner is narrowed as an owned
	// account's is: old-imap/Sent stays out (review A, #512).
	sent := f.Add(t, "old-imap/Sent", "Gesendet", time.Date(2019, 7, 1, 9, 0, 0, 0, time.UTC))
	out = export(newGoBDRun(f, next()).account(f.OldIMAP, f.Folders["old-imap/INBOX"]).claim(nil))
	if files, locs := emls(t, out), csvRows(t, out, gobd.LocationsFile); len(files) != 1 || len(locs) != 1 ||
		!strings.Contains(files[0], f.OldMsg) || strings.Contains(strings.Join(locs, "\n"), sent) {
		t.Errorf("old-imap/INBOX: %v %v", files, locs)
	}
	out = export(newGoBDRun(f, next()).account(f.OldIMAP).claim(nil))
	if locs := csvRows(t, out, gobd.LocationsFile); len(locs) != 2 {
		t.Errorf("old-imap: %v", locs)
	}

	// A removed account is exported with its owner.
	if _, err := f.Conn.Exec(f.Ctx, "UPDATE accounts SET removed_at = now(), enabled = false WHERE id = $1", f.BenWork.ID); err != nil {
		t.Fatal(err)
	}
	out = export(newGoBDRun(f, next()).users(f.Ben))
	if files := emls(t, out); len(files) != 1 {
		t.Errorf("removed account: %v", files)
	}
}

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// noOutput fails if the export left out or out.partial behind.
func noOutput(t *testing.T, out string) {
	t.Helper()
	for _, p := range []string{out, out + gobd.PartialSuffix} {
		if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s exists after a refused export: %v", p, err)
		}
	}
}

// TestGoBDGroupChecksInSnapshot (T-G5, T-G2): the archive refuses a mixed
// selection and a claim the selection does not meet, without the command
// line, before it writes a file.
func TestGoBDGroupChecksInSnapshot(t *testing.T) {
	f := gobdtest.New(t)
	dir := t.TempDir()

	out := filepath.Join(dir, "mixed")
	_, err := newGoBDRun(f, out).users(f.Anna, f.Kim).run(t)
	if !errors.Is(err, ErrSelectionSpansGroups) || !strings.Contains(err.Error(), "anna (Meier GmbH)") ||
		!strings.Contains(err.Error(), "kim (Schulz KG)") {
		t.Errorf("anna and kim: %v", err)
	}
	noOutput(t, out)

	// T-G2: an account without a group next to one with a group.
	out = filepath.Join(dir, "unowned")
	_, err = newGoBDRun(f, out).account(f.AnnaWork).account(f.OldIMAP).run(t)
	if !errors.Is(err, ErrSelectionSpansGroups) || !strings.Contains(err.Error(), `account "old-imap" (no group)`) {
		t.Errorf("anna/work and old-imap: %v", err)
	}
	noOutput(t, out)

	// Rev. 4: kim with the claim Meier GmbH, as if kim had moved after the
	// command line's check.
	out = filepath.Join(dir, "claim")
	_, err = newGoBDRun(f, out).users(f.Kim).claim(f.A).run(t)
	if !errors.Is(err, ErrGroupClaimMismatch) || !strings.Contains(err.Error(), "Schulz KG, not Meier GmbH") {
		t.Errorf("kim as Meier GmbH: %v", err)
	}
	noOutput(t, out)

	// An account without an owner and --without-group: group none.
	out = filepath.Join(dir, "none")
	newGoBDRun(f, out).account(f.OldIMAP).claim(nil).mustRun(t)
	verifyExport(t, out, nil)
	if st := readFile(t, filepath.Join(out, gobd.StatementFile)); !strings.Contains(st, "group none\n") || !strings.Contains(st, "owners []\n") {
		t.Errorf("unowned export:\n%s", st)
	}
}

// TestGoBDWholeGroup (T-G11, G1): --group alone exports every user of the
// group, also one added later, and nothing of another group.
func TestGoBDWholeGroup(t *testing.T) {
	f := gobdtest.New(t)
	dir := t.TempDir()
	run := func(name string) (*GoBDResult, string) {
		r := newGoBDRun(f, filepath.Join(dir, name)).claim(f.A)
		r.opts.Selection.WholeGroup = true
		res := r.mustRun(t)
		verifyExport(t, r.opts.Out, nil)
		return res, r.opts.Out
	}
	res, out := run("a")
	if !slices.Equal(res.Owners, []string{"anna", "ben"}) || len(emls(t, out)) != 3 {
		t.Errorf("group A: owners %v, files %v", res.Owners, emls(t, out))
	}
	for _, l := range csvRows(t, out, gobd.LocationsFile) {
		if strings.Contains(l, `"kim"`) {
			t.Errorf("kim's location: %s", l)
		}
	}
	// A user who joined the group is in the next export, also without
	// accounts.
	if _, err := f.St.CreateUserInGroup(f.Ctx, store.NewUser{Name: "carla", PasswordHash: "h", GroupID: f.A.ID}); err != nil {
		t.Fatal(err)
	}
	if res, _ := run("b"); !slices.Equal(res.Owners, []string{"anna", "ben", "carla"}) {
		t.Errorf("after carla joined: %v", res.Owners)
	}
}

// TestGoBDHolds: an export by legal hold (M2, T-G10, T-G11), and the holds
// column in other exports.
func TestGoBDHolds(t *testing.T) {
	f := gobdtest.New(t)
	dir := t.TempDir()
	hold := func(h store.NewLegalHold) int64 {
		t.Helper()
		h.Name, h.Reason = "Prüfung", "test"
		id, err := f.St.AddLegalHold(f.Ctx, h)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	accHold := hold(store.NewLegalHold{AccountID: &f.AnnaWork.ID})
	instHold := hold(store.NewLegalHold{})
	groupHold := hold(store.NewLegalHold{GroupID: &f.A.ID})

	// T-G10: the account hold's export lists the account hold and the
	// group hold (both in the group), never the hold over every customer.
	r := newGoBDRun(f, filepath.Join(dir, "acc"))
	r.opts.Selection.HoldID = accHold
	r.mustRun(t)
	verifyExport(t, r.opts.Out, nil)
	want := fmt.Sprintf(`"%d %d"`, accHold, groupHold)
	locs := csvRows(t, r.opts.Out, gobd.LocationsFile)
	if len(locs) != 3 {
		t.Errorf("account hold: %d locations", len(locs))
	}
	for _, l := range locs {
		if !strings.HasSuffix(l, want) {
			t.Errorf("holds of %s, want %s", l, want)
		}
	}

	// T-G11: the group hold exports A's held locations, not kim's place of
	// the shared message.
	r = newGoBDRun(f, filepath.Join(dir, "group"))
	r.opts.Selection.HoldID = groupHold
	res := r.mustRun(t)
	verifyExport(t, r.opts.Out, nil)
	if res.Group == nil || res.Group.ID != f.A.ID || len(csvRows(t, r.opts.Out, gobd.LocationsFile)) != 4 {
		t.Errorf("group hold: %+v, %v", res.Group, csvRows(t, r.opts.Out, gobd.LocationsFile))
	}
	for _, l := range csvRows(t, r.opts.Out, gobd.LocationsFile) {
		if strings.Contains(l, `"kim"`) {
			t.Errorf("kim's location in the group hold's export: %s", l)
		}
	}

	// M2: a hold over every customer is refused, as is a released one.
	out := filepath.Join(dir, "inst")
	r = newGoBDRun(f, out)
	r.opts.Selection.HoldID = instHold
	if _, err := r.run(t); !errors.Is(err, ErrGoBDSelection) || !strings.Contains(err.Error(), "name the users with --user") {
		t.Errorf("instance hold: %v", err)
	}
	noOutput(t, out)
	if err := f.St.ReleaseLegalHold(f.Ctx, accHold, nil, "done"); err != nil {
		t.Fatal(err)
	}
	r = newGoBDRun(f, filepath.Join(dir, "released"))
	r.opts.Selection.HoldID = accHold
	if _, err := r.run(t); !errors.Is(err, ErrGoBDSelection) || !strings.Contains(err.Error(), "released") {
		t.Errorf("released hold: %v", err)
	}

	// Without --hold the column stays empty, also for held places.
	r = newGoBDRun(f, filepath.Join(dir, "user")).users(f.Anna)
	r.mustRun(t)
	for _, l := range csvRows(t, r.opts.Out, gobd.LocationsFile) {
		if !strings.HasSuffix(l, `;""`) {
			t.Errorf("holds in a user export: %s", l)
		}
	}
}

// TestGoBDGrant: --grant exports the grant's accounts, folders and period,
// with the holds column empty; a revoked grant is refused.
func TestGoBDGrant(t *testing.T) {
	f := gobdtest.New(t)
	aud, err := f.St.CreateUserInGroup(f.Ctx, store.NewUser{Name: "pruefer", PasswordHash: "h", GroupID: f.A.ID, Auditor: true})
	if err != nil {
		t.Fatal(err)
	}
	until := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	id, err := f.St.CreateGrant(f.Ctx, store.NewGrant{
		OwnerID: f.Anna.ID, AuditorID: aud.ID, Label: "Betriebsprüfung",
		Accounts:    []store.GrantAccount{{AccountID: f.AnnaWork.ID, Folders: []int64{f.Folders["anna/work/INBOX"]}}},
		PeriodUntil: &until, EndsAt: time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.St.AddLegalHold(f.Ctx, store.NewLegalHold{Name: "h", Reason: "r", AccountID: &f.AnnaWork.ID}); err != nil {
		t.Fatal(err)
	}
	r := newGoBDRun(f, filepath.Join(t.TempDir(), "grant"))
	r.opts.Selection.GrantID = id
	res := r.mustRun(t)
	verifyExport(t, r.opts.Out, nil)
	// INBOX before 2025 in UTC: only the shared message (the other is
	// 2024-12-31 23:30 UTC, but in the Archiv folder too and in INBOX).
	locs := csvRows(t, r.opts.Out, gobd.LocationsFile)
	if !slices.Equal(res.Owners, []string{"anna"}) || len(locs) != 2 {
		t.Fatalf("grant: owners %v, locations %v", res.Owners, locs)
	}
	for _, l := range locs {
		if strings.Contains(l, `"Archiv"`) || !strings.HasSuffix(l, `;""`) {
			t.Errorf("grant location %s", l)
		}
	}
	if err := f.St.RevokeGrant(f.Ctx, id, &f.Anna.ID, &f.Anna.ID); err != nil {
		t.Fatal(err)
	}
	r = newGoBDRun(f, filepath.Join(t.TempDir(), "revoked"))
	r.opts.Selection.GrantID = id
	if _, err := r.run(t); !errors.Is(err, ErrGoBDSelection) || !strings.Contains(err.Error(), "revoked") {
		t.Errorf("revoked grant: %v", err)
	}
}

// TestGoBDMissing: a deleted and a corrupt file are listed in missing.csv
// only, the statement says complete no, and verify reports it.
func TestGoBDMissing(t *testing.T) {
	f := gobdtest.New(t)
	if err := os.Remove(filepath.Join(f.DataDir, blobPath(t, f, f.BenMsg))); err != nil {
		t.Fatal(err)
	}
	// Same size, other bytes.
	p := filepath.Join(f.DataDir, blobPath(t, f, f.AnnaMsg))
	data := []byte(readFile(t, p))
	data[len(data)-3] ^= 1
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "x")
	res := newGoBDRun(f, out).users(f.Anna, f.Ben).mustRun(t)
	if res.Statement.Complete || len(res.Missing) != 2 {
		t.Fatalf("complete %v, missing %v", res.Statement.Complete, res.Missing)
	}
	missing := strings.Join(csvRows(t, out, gobd.MissingFile), "\n")
	for _, want := range []string{f.BenMsg + `";"mail/2025/02/` + f.BenMsg + `.eml";"file missing"`, f.AnnaMsg + `";"mail/2025/01/` + f.AnnaMsg + `.eml";"file damaged (SHA-256 differs)"`} {
		if !strings.Contains(missing, want) {
			t.Errorf("missing.csv lacks %s:\n%s", want, missing)
		}
	}
	all := strings.Join(append(csvRows(t, out, gobd.MessagesFile), csvRows(t, out, gobd.LocationsFile)...), "\n")
	if strings.Contains(all, f.BenMsg) || strings.Contains(all, f.AnnaMsg) {
		t.Error("a missing message is in messages.csv or locations.csv")
	}
	v, err := gobd.Verify(out, gobd.VerifyOptions{})
	if err != nil || v.Findings[gobd.KindIncomplete] == 0 {
		t.Errorf("verify: %+v, %v", v, err)
	}
}

// blobPath is the path of the message's file as it is on disk, in either
// form (plaintext or encrypted at rest), relative to the data directory.
func blobPath(t *testing.T, f *gobdtest.Fixture, sha string) string {
	t.Helper()
	m, err := f.Blobs.OpenMessage(sha)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	return m.Path()
}

// TestGoBDZIP: a ZIP passes verify; the same snapshot with the same clock
// and id gives the same bytes; the message limit; a damaged file a ZIP
// cannot leave out stops the export and leaves no file.
func TestGoBDZIP(t *testing.T) {
	f := gobdtest.New(t)
	dir := t.TempDir()
	zipOf := func(name string, max int64) (string, error) {
		r := newGoBDRun(f, filepath.Join(dir, name)).users(f.Anna, f.Ben)
		r.opts.maxZIP = max
		_, err := r.run(t)
		return r.opts.Out, err
	}
	a, err := zipOf("a.zip", 0)
	if err != nil {
		t.Fatal(err)
	}
	verifyExport(t, a, nil)
	b, err := zipOf("b.zip", 0)
	if err != nil {
		t.Fatal(err)
	}
	if readFile(t, a) != readFile(t, b) {
		t.Error("the same snapshot gave different ZIPs")
	}
	// Every entry has the injected time, not the wall clock's.
	zr, err := zip.OpenReader(a)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range zr.File {
		if !e.Modified.Equal(gobdClock) {
			t.Errorf("%s: time %v", e.Name, e.Modified)
		}
	}
	_ = zr.Close()
	// The boundary: 3 messages fit a limit of 3, not of 2.
	if _, err := zipOf("three.zip", 3); err != nil {
		t.Errorf("3 of 3: %v", err)
	}
	out, err := zipOf("two.zip", 2)
	if !errors.Is(err, ErrGoBDSelection) || !strings.Contains(err.Error(), "use a directory or split by year") {
		t.Errorf("3 of 2: %v", err)
	}
	noOutput(t, out)

	p := filepath.Join(f.DataDir, blobPath(t, f, f.BenMsg))
	data := []byte(readFile(t, p))
	data[len(data)-3] ^= 1
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = zipOf("damaged.zip", 0)
	if !errors.Is(err, gobd.ErrCannotUndo) || !strings.Contains(err.Error(), "export to a directory") {
		t.Errorf("damaged in a ZIP: %v", err)
	}
	noOutput(t, out)
}

// TestGoBDDeterministicDir: the same snapshot gives the same directory.
func TestGoBDDeterministicDir(t *testing.T) {
	f := gobdtest.New(t)
	dir := t.TempDir()
	a := newGoBDRun(f, filepath.Join(dir, "a")).users(f.Anna)
	a.mustRun(t)
	b := newGoBDRun(f, filepath.Join(dir, "b")).users(f.Anna)
	b.mustRun(t)
	if diff := treeDiff(t, a.opts.Out, b.opts.Out); diff != "" {
		t.Error(diff)
	}
	if st := readFile(t, filepath.Join(a.opts.Out, gobd.StatementFile)); !strings.Contains(st, "created 2026-11-04T10:00:00Z\n") ||
		!strings.Contains(st, "id 0123456789abcdef0123456789abcdef\n") {
		t.Errorf("not the injected clock and id:\n%s", st)
	}
}

// TestGoBDEncryptedEqualsPlain: with the message files encrypted at rest
// (#392), the export reads the plaintext by hash (OpenMessage): directory
// and ZIP are byte for byte the exports of the same archive in plaintext,
// and both verify. The stored paths still name the plaintext files, which
// are gone, so a read by stored path finds nothing.
func TestGoBDEncryptedEqualsPlain(t *testing.T) {
	f := gobdtest.New(t)
	dir := t.TempDir()
	export := func(name string) string {
		t.Helper()
		r := newGoBDRun(f, filepath.Join(dir, name)).users(f.Anna, f.Ben)
		if res := r.mustRun(t); len(res.Missing) != 0 {
			t.Fatalf("%s: missing %v", name, res.Missing)
		}
		verifyExport(t, r.opts.Out, nil)
		return r.opts.Out
	}
	plainDir, plainZIP := export("plain"), export("plain.zip")

	// Encrypt every message file in place: Put with encryption on writes
	// <hash>.enc and unlinks the plaintext form.
	kek, err := blobcrypt.GenerateKEK()
	if err != nil {
		t.Fatal(err)
	}
	ring, err := blobcrypt.NewKeyring(kek, "archive key", gobdClock)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Blobs.SetKeys(ring, true); err != nil {
		t.Fatal(err)
	}
	rows, err := f.Conn.Query(f.Ctx, "SELECT sha256 FROM messages ORDER BY sha256")
	if err != nil {
		t.Fatal(err)
	}
	shas, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(shas) == 0 {
		t.Fatalf("messages: %v %v", shas, err)
	}
	for _, sha := range shas {
		m, err := f.Blobs.OpenMessage(sha)
		if err != nil {
			t.Fatal(err)
		}
		body := new(bytes.Buffer)
		_, err = body.ReadFrom(m)
		_ = m.Close()
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.Blobs.Put(body); err != nil {
			t.Fatal(err)
		}
		if m, err := f.Blobs.OpenMessage(sha); err != nil || !m.Encrypted() || f.Blobs.HasPlaintext(sha) {
			t.Fatalf("%s not encrypted in place: %v", sha, err)
		} else {
			_ = m.Close()
		}
	}

	encDir, encZIP := export("enc"), export("enc.zip")
	if diff := treeDiff(t, plainDir, encDir); diff != "" {
		t.Errorf("directory of the encrypted archive: %s", diff)
	}
	if readFile(t, plainZIP) != readFile(t, encZIP) {
		t.Error("ZIP of the encrypted archive differs")
	}
}

// TestGoBDSnapshotGuard: a change committed after the first file of pass
// B (a message's sent_at, and so its sort_at and path) is not seen by the
// later passes: the export equals the one made before and verifies. Every
// pass reads the one repeatable read snapshot.
func TestGoBDSnapshotGuard(t *testing.T) {
	f := gobdtest.New(t)
	dir := t.TempDir()
	before := newGoBDRun(f, filepath.Join(dir, "before")).users(f.Anna)
	before.mustRun(t)
	r := newGoBDRun(f, filepath.Join(dir, "during")).users(f.Anna)
	changed := false
	r.opts.Progress = func(done int64) error {
		if done != 1 || changed {
			return nil
		}
		changed = true
		ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
		defer cancel()
		tag, err := f.Conn.Exec(ctx, "UPDATE messages SET sent_at = '2023-01-01T09:00:00Z' WHERE sha256 = $1", f.Shared)
		if err == nil && tag.RowsAffected() != 1 {
			err = fmt.Errorf("%d rows changed", tag.RowsAffected())
		}
		return err
	}
	r.mustRun(t)
	if !changed {
		t.Fatal("the change never ran")
	}
	verifyExport(t, r.opts.Out, nil)
	if diff := treeDiff(t, before.opts.Out, r.opts.Out); diff != "" {
		t.Errorf("the change after the snapshot reached the export: %s", diff)
	}
}

// TestGoBDDeletionBetweenPasses (plan section 7): a deletion run commits
// after the first file of pass B and deletes a message whose file is not
// copied yet. With a grace period the file is still there and the export
// is complete as of its snapshot; with grace 0 and a sweep the file is
// gone, and the message is listed as missing.
func TestGoBDDeletionBetweenPasses(t *testing.T) {
	for _, c := range []struct {
		name    string
		grace   time.Duration
		missing int
	}{{"grace", time.Hour, 0}, {"no grace and sweep", 0, 1}} {
		t.Run(c.name, func(t *testing.T) {
			f := gobdtest.New(t)
			// old-imap/INBOX: OldMsg (2019) first, then a recent message,
			// then an expired one; a one-year rule makes both old ones
			// candidates.
			f.Add(t, "old-imap/INBOX", "Neu", time.Now().UTC().Add(-time.Hour).Truncate(time.Second))
			late := f.Add(t, "old-imap/INBOX", "Alt 2018", time.Date(2018, 5, 1, 9, 0, 0, 0, time.UTC))
			if _, err := f.St.AddRetentionRule(f.Ctx, store.NewRetentionRule{AccountID: f.OldIMAP.ID, Years: 1}); err != nil {
				t.Fatal(err)
			}
			run, err := f.St.ProposeDeletion(f.Ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.St.ConfirmDeletionRun(f.Ctx, run.ID, nil); err != nil {
				t.Fatal(err)
			}
			r := newGoBDRun(f, filepath.Join(t.TempDir(), "x")).account(f.OldIMAP).claim(nil)
			deleted := false
			r.opts.Progress = func(done int64) error {
				if done != 1 || deleted {
					return nil
				}
				deleted = true
				ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
				defer cancel()
				res, err := ExecuteDeletionRun(ctx, f.St, run.ID, DeletionOptions{Grace: c.grace, LockTimeout: testwait.Limit(t)})
				if err != nil {
					return err
				}
				if res.Deleted != 2 {
					return fmt.Errorf("deleted %d, want 2", res.Deleted)
				}
				if c.grace == 0 {
					if n, err := SweepBlobs(ctx, f.St, f.Blobs, SweepOptions{LockTimeout: testwait.Limit(t)}); err != nil || n != 2 {
						return fmt.Errorf("sweep %d: %w", n, err)
					}
				}
				return nil
			}
			res := r.mustRun(t)
			if !deleted {
				t.Fatal("the deletion never ran")
			}
			if len(res.Missing) != c.missing || c.missing == 1 && res.Missing[0].SHA256 != late {
				t.Fatalf("missing %+v, want %d (%s)", res.Missing, c.missing, late)
			}
			if got := csvRows(t, r.opts.Out, gobd.LocationsFile); len(got) != 3-c.missing {
				t.Errorf("locations %v", got)
			}
			if c.missing == 0 {
				verifyExport(t, r.opts.Out, nil)
			}
		})
	}
}

// treeDiff compares two directory trees file by file.
func treeDiff(t *testing.T, a, b string) string {
	t.Helper()
	read := func(root string) map[string]string {
		out := map[string]string{}
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				rel, _ := filepath.Rel(root, p)
				out[rel] = readFile(t, p)
			}
			return err
		})
		return out
	}
	ta, tb := read(a), read(b)
	for k, v := range ta {
		if tb[k] != v {
			return "differs: " + k
		}
	}
	if len(ta) != len(tb) {
		return fmt.Sprintf("%d files against %d", len(ta), len(tb))
	}
	return ""
}

// countingOpen counts how often each message file is opened.
type countingOpen struct {
	blobs  *blobstore.Store
	opened map[string]int
}

func (c *countingOpen) open(sha string) (*blobstore.Message, error) {
	c.opened[sha]++
	return c.blobs.OpenMessage(sha)
}

var errStop = errors.New("stopped by the test")

// TestGoBDResume: an export stopped after two files is resumed: a
// truncated file is written again, a file no longer selected and a temp
// file are removed, a good file is not read again, and verify is clean.
func TestGoBDResume(t *testing.T) {
	f := gobdtest.New(t)
	out := filepath.Join(t.TempDir(), "x")
	r := newGoBDRun(f, out).users(f.Anna, f.Ben)
	r.opts.Progress = func(done int64) error {
		if done == 2 {
			return errStop
		}
		return nil
	}
	if _, err := r.run(t); !errors.Is(err, errStop) {
		t.Fatalf("first run: %v", err)
	}
	partial := out + gobd.PartialSuffix
	got := emls(t, partial)
	if len(got) != 2 {
		t.Fatalf("after the stop: %v", got)
	}
	// Truncate one, plant another customer's message and a temp file.
	if err := os.Truncate(filepath.Join(partial, got[0]), 5); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(partial, "mail", "2025", "02", f.KimMsg+".eml")
	if err := os.MkdirAll(filepath.Dir(stray), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{stray, filepath.Join(partial, "mail", "2025", "02", ".tmp-x.eml-1")} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Without --resume an interrupted export is refused.
	if _, err := r.run(t); !errors.Is(err, ErrGoBDSelection) || !strings.Contains(err.Error(), "--resume") {
		t.Errorf("again without resume: %v", err)
	}
	c := &countingOpen{blobs: f.Blobs, opened: map[string]int{}}
	r2 := newGoBDRun(f, out).users(f.Anna, f.Ben)
	r2.opts.Resume, r2.opts.Open = true, c.open
	res := r2.mustRun(t)
	verifyExport(t, out, nil)
	if !res.Statement.Resumed || !strings.Contains(readFile(t, filepath.Join(out, gobd.StatementFile)), "resumed yes\n") {
		t.Error("the statement does not say resumed")
	}
	kept := strings.TrimSuffix(filepath.Base(got[1]), ".eml")
	if c.opened[kept] != 0 || len(c.opened) != 2 {
		t.Errorf("opened %v; %s was complete", c.opened, kept)
	}
	if slices.ContainsFunc(emls(t, out), func(p string) bool { return strings.Contains(p, f.KimMsg) || strings.Contains(p, ".tmp-") }) {
		t.Errorf("stray files stayed: %v", emls(t, out))
	}
	// A resume with other options is refused.
	r3 := newGoBDRun(f, filepath.Join(t.TempDir(), "y")).users(f.Anna)
	r3.opts.Progress = func(int64) error { return errStop }
	_, _ = r3.run(t)
	r3.opts.Progress, r3.opts.Resume, r3.opts.WithChain = nil, true, true
	if _, err := r3.run(t); !errors.Is(err, ErrGoBDSelection) || !strings.Contains(err.Error(), "same options") {
		t.Errorf("resume with other options: %v", err)
	}
}

// TestGoBDFreeSpace: too little space is refused before anything is
// written.
func TestGoBDFreeSpace(t *testing.T) {
	f := gobdtest.New(t)
	out := filepath.Join(t.TempDir(), "x")
	r := newGoBDRun(f, out).users(f.Anna)
	r.opts.FreeSpace = func(string) (uint64, error) { return 1 << 20, nil }
	if _, err := r.run(t); !errors.Is(err, ErrGoBDSelection) || !strings.Contains(err.Error(), "are needed") {
		t.Errorf("1 MiB free: %v", err)
	}
	noOutput(t, out)
	r.opts.FreeSpace = func(string) (uint64, error) { return 3 << 20, nil }
	if _, err := r.run(t); err != nil {
		t.Errorf("3 MiB free: %v", err)
	}
}

// TestGoBDAttestation: an attestation only when messages are new; the
// export waits for one being written (the lock held by the test) and then
// writes none itself; without a key the statement is unsigned.
func TestGoBDAttestation(t *testing.T) {
	f := gobdtest.New(t)
	key := keygen(t, f)
	dir := t.TempDir()
	count := func() int64 {
		n, err := f.St.CountAttestations(f.Ctx)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	r := newGoBDRun(f, filepath.Join(dir, "1")).users(f.Anna)
	r.opts.Signer = key
	if res := r.mustRun(t); res.Attested != 1 || count() != 1 {
		t.Fatalf("first export: attested %d, chain %d", res.Attested, count())
	}
	r = newGoBDRun(f, filepath.Join(dir, "2")).users(f.Anna)
	r.opts.Signer = key
	if res := r.mustRun(t); res.Attested != 0 || count() != 1 {
		t.Errorf("nothing new: attested %d, chain %d", res.Attested, count())
	}

	// A new message, and an attestation in progress (the test holds its
	// lock): the export waits for it.
	f.Add(t, "anna/work/INBOX", "Nachtrag", time.Date(2025, 3, 1, 9, 0, 0, 0, time.UTC))
	unlock, ok, err := f.St.TryLockAttest(f.Ctx)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	r = newGoBDRun(f, filepath.Join(dir, "3")).users(f.Anna)
	r.opts.Signer = key
	done := make(chan error, 1)
	var res atomic.Pointer[GoBDResult]
	go func() {
		out, err := r.run(t)
		res.Store(out)
		done <- err
	}()
	waitForAdvisoryWait(t, f.Conn, done)
	unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := res.Load(); !got.Waited || got.Attested != 0 || count() != 1 {
		t.Errorf("after waiting: %+v, chain %d", got, count())
	}
	verifyExport(t, r.opts.Out, trustKey(key))

	// Without a key: unsigned, and still an export.
	r = newGoBDRun(f, filepath.Join(dir, "4")).users(f.Anna)
	r.mustRun(t)
	if st := readFile(t, filepath.Join(r.opts.Out, gobd.StatementFile)); !strings.HasSuffix(st, "sig none\n") {
		t.Errorf("unsigned statement:\n%s", st)
	}
}

// waitForAdvisoryWait returns once a session of the test's database waits
// for an advisory lock.
func waitForAdvisoryWait(t *testing.T, conn *pgx.Conn, done <-chan error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testwait.Limit(t))
	defer cancel()
	for {
		var n int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock' AND wait_event = 'advisory'`).Scan(&n); err != nil {
			t.Fatalf("waiting for the advisory lock wait: %v", err)
		}
		if n > 0 {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("the export finished without waiting: %v", err)
		default:
		}
	}
}

// TestGoBDChainAndLeaves: one entry by default, the whole chain with
// WithChain, the leaves with WithLeaves; verify is clean for each.
func TestGoBDChainAndLeaves(t *testing.T) {
	f := gobdtest.New(t)
	key := keygen(t, f)
	if _, err := Attest(f.Ctx, f.St, filepath.Join(f.DataDir, "attest"), key); err != nil {
		t.Fatal(err)
	}
	f.Add(t, "anna/work/INBOX", "Nachtrag", time.Date(2025, 3, 1, 9, 0, 0, 0, time.UTC))
	dir := t.TempDir()
	lines := func(out string) int {
		return strings.Count(readFile(t, filepath.Join(out, gobd.ChainFile)), "\n")
	}
	r := newGoBDRun(f, filepath.Join(dir, "one")).users(f.Anna)
	r.opts.Signer = key
	r.mustRun(t)
	verifyExport(t, r.opts.Out, trustKey(key))
	if n := lines(r.opts.Out); n != 1 {
		t.Errorf("default chain: %d entries", n)
	}
	r = newGoBDRun(f, filepath.Join(dir, "full")).users(f.Anna)
	r.opts.Signer, r.opts.WithChain, r.opts.WithLeaves = key, true, true
	r.mustRun(t)
	verifyExport(t, r.opts.Out, trustKey(key))
	if n := lines(r.opts.Out); n != 2 {
		t.Errorf("full chain: %d entries", n)
	}
	for _, seq := range []int64{1, 2} {
		if _, err := os.Stat(filepath.Join(r.opts.Out, filepath.FromSlash(gobd.LeavesPath(seq)))); err != nil {
			t.Errorf("leaves %d: %v", seq, err)
		}
	}
}

// TestGoBDRenameDuringExport (T-G7): a rename that commits while the
// export runs, after its snapshot began, does not change it: the
// statement and the README have the name the snapshot saw.
func TestGoBDRenameDuringExport(t *testing.T) {
	f := gobdtest.New(t)
	out := filepath.Join(t.TempDir(), "x")
	r := newGoBDRun(f, out).users(f.Anna).claim(f.A)
	r.opts.afterResolve = func() {
		if err := f.St.RenameGroup(context.Background(), f.A.ID, "Meier AG"); err != nil {
			t.Error(err)
		}
	}
	res := r.mustRun(t)
	if res.Group.Name != "Meier GmbH" || !strings.Contains(readFile(t, filepath.Join(out, gobd.StatementFile)), "group \"Meier GmbH\"\n") ||
		strings.Contains(readFile(t, filepath.Join(out, gobd.ReadmeFile)), "Meier AG") {
		t.Errorf("statement after the rename: %+v", res.Group)
	}
	if len(res.Moved) != 0 {
		t.Errorf("a rename reported as a move: %v", res.Moved)
	}
}

// TestGoBDBoundedMemory (Rev. 4): over 10,000 messages the heap grows by
// less than the 64 MiB budget, and every message file is opened exactly
// once.
func TestGoBDBoundedMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds 10,000 messages")
	}
	f := gobdtest.New(t)
	const n = 10_000
	seedMessages(t, f, n)
	c := &countingOpen{blobs: f.Blobs, opened: map[string]int{}}
	out := filepath.Join(t.TempDir(), "x")
	r := newGoBDRun(f, out).users(f.Ben)
	var base runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&base)
	var peak uint64
	sample := func() {
		var m runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&m)
		peak = max(peak, m.HeapAlloc)
	}
	r.opts.Open = c.open
	r.opts.Progress = func(done int64) error {
		if done%1000 == 0 {
			sample()
		}
		return nil
	}
	res := r.mustRun(t)
	sample()
	if res.Statement.Messages != n+1 {
		t.Fatalf("%d messages", res.Statement.Messages)
	}
	for sha, k := range c.opened {
		if k != 1 {
			t.Errorf("%s opened %d times", sha, k)
		}
	}
	if len(c.opened) != n+1 {
		t.Errorf("%d files opened", len(c.opened))
	}
	// The counting map itself holds about 100 B per message; the budget
	// is for the export.
	growth := int64(peak) - int64(base.HeapAlloc)
	t.Logf("heap growth %d KiB over %d messages", growth>>10, n)
	if growth > 64<<20 {
		t.Errorf("heap grew by %d MiB", growth>>20)
	}
}

// seedMessages stores n small messages in ben/work/INBOX.
func seedMessages(t *testing.T, f *gobdtest.Fixture, n int) {
	t.Helper()
	folder := f.Folder(t, "ben/work/INBOX")
	base := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	for start := 0; start < n; start += 500 {
		var metas []store.MessageMeta
		var locs []store.Location
		for i := start; i < min(n, start+500); i++ {
			at := base.Add(time.Duration(i) * time.Hour)
			body := gobdtest.Body(fmt.Sprintf("Beleg %d", i), at)
			b, _, err := f.Blobs.Put(strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			metas = append(metas, store.MessageMeta{SHA256: b.SHA256, Size: b.Size, StoredPath: b.Path, Subject: fmt.Sprintf("Beleg %d", i), SentAt: &at})
			locs = append(locs, store.Location{FolderID: folder, UIDValidity: 1, UID: uint32(1000 + i), InternalDate: at}) //nolint:gosec // small test numbers
		}
		if _, err := f.St.SaveBatch(f.Ctx, folder, uint32(1000+min(n, start+500)), metas, locs); err != nil { //nolint:gosec // small test numbers
			t.Fatal(err)
		}
	}
}
