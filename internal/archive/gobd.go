package archive

import (
	"cmp"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/keepia/archive/internal/attest"
	"github.com/keepia/archive/internal/blobstore"
	"github.com/keepia/archive/internal/gobd"
	"github.com/keepia/archive/internal/store"
)

// GoBD export (#394, part 2): the selection of one customer group, read in
// one repeatable read snapshot and written by gobd.Writer in the pass
// order of format v1 (docs/development/architecture.md, "GoBD export
// format v1"). The snapshot defines the export: a group move or rename
// that commits later cannot change it, and none is locked out while it
// runs.

// Errors of a GoBD export that are the operator's to fix (exit code 2).
var (
	// ErrSelectionSpansGroups: the selected users and accounts are not all
	// in one customer group, or all without one.
	ErrSelectionSpansGroups = errors.New("the selection spans customer groups")
	// ErrGroupClaimMismatch: the selection's group is not the one named
	// with --group or --without-group.
	ErrGroupClaimMismatch = errors.New("the selection is not in the customer group named")
	// ErrGroupChanged: a resumed export's selection is no longer in the
	// group the export started with.
	ErrGroupChanged = errors.New("the selection's group changed since the export started; start a new export")
	// ErrGoBDSelection: the selection cannot be exported as given (a
	// released or too wide hold, a revoked grant, an unknown name, a ZIP
	// that would be too large, too little free space, a resume that does
	// not match).
	ErrGoBDSelection = errors.New("the selection cannot be exported")
)

// GroupClaim is the customer group the operator names (--group or
// --without-group): the export refuses if the selection's group differs.
type GroupClaim struct {
	Given bool   // one of the two flags was given
	ID    *int64 // nil with Given: --without-group
	Name  string // as given, for messages
}

// GoBDSelection is what an export covers. Users, accounts, a hold, a grant
// and a whole group are resolved in the snapshot and combined; a period
// and the time zone apply to all.
type GoBDSelection struct {
	// Owners: every account of these users, removed ones included.
	Owners []int64
	// Accounts: these accounts, each with all its folders (nil) or these
	// folder IDs.
	Accounts map[int64][]int64
	// WholeGroup: every user of Group.ID as the snapshot sees them, with
	// all their accounts (maintainer decision G1).
	WholeGroup bool
	Group      GroupClaim
	// HoldID: the locations a legal hold covers, which must name a user,
	// an account or a group (M2). GrantID: the scope of an auditor grant.
	HoldID, GrantID int64
	// Since and Until bound sort_at: [Since, Until).
	Since, Until *time.Time
	// TimeZone is the export time zone (IANA name).
	TimeZone string
	// Text is the selection in canonical form for the statement and the
	// resume state: compact JSON (GoBDSelectionText.JSON).
	Text string
}

// GoBDSelectionText is the selection as the statement records it, with
// names instead of IDs. Every field is optional; the JSON is compact with
// the fields in this order.
type GoBDSelectionText struct {
	Users        []string            `json:"users,omitempty"`
	Accounts     []string            `json:"accounts,omitempty"`
	Folders      map[string][]string `json:"folders,omitempty"`
	WholeGroup   bool                `json:"whole_customer,omitempty"`
	Group        string              `json:"group,omitempty"`
	WithoutGroup bool                `json:"without_group,omitempty"`
	Hold         int64               `json:"hold,omitempty"`
	Grant        int64               `json:"grant,omitempty"`
	Since        string              `json:"since,omitempty"`
	Until        string              `json:"until,omitempty"`
	TimeZone     string              `json:"time_zone"`
}

// JSON is the compact JSON form.
func (t GoBDSelectionText) JSON() string {
	b, _ := json.Marshal(t)
	return string(b)
}

// GoBDOptions configure an export.
type GoBDOptions struct {
	Selection GoBDSelection
	// Out is the directory to create, or a ZIP file if it ends in ".zip".
	Out string
	// Resume continues the interrupted directory export in Out.partial.
	Resume bool
	// WithChain ships the whole attestation chain, WithLeaves the leaves
	// files of the attestations the messages name.
	WithChain, WithLeaves bool
	// AttestDir is <data dir>/attest. Signer signs the statement; nil
	// writes it unsigned. With a signer and without NoAttest, an
	// attestation is written first if messages were added since the
	// newest one.
	AttestDir string
	Signer    ed25519.PrivateKey
	NoAttest  bool
	// Software is the version written into the statement.
	Software string
	// Now and NewID give the statement's time and id; tests make the
	// output deterministic with them.
	Now   func() time.Time
	NewID func() string
	// Open opens a message by its hash and returns its plaintext; nil
	// reads blobs.OpenMessage, which finds the file in either form
	// (plaintext or encrypted at rest), never by the stored path.
	Open func(sha string) (*blobstore.Message, error)
	// FreeSpace returns the bytes free for an unprivileged user in dir; nil
	// asks the file system.
	FreeSpace func(dir string) (uint64, error)
	// Progress is called after each message file of pass B with the
	// number done so far; an error stops the export there.
	Progress func(done int64) error
	// Warn receives warnings (an attestation that could not be written).
	Warn io.Writer

	maxZIP int64 // for tests; 0 means gobd.MaxZIPMessages
	// afterResolve runs in tests right after the selection was resolved
	// in the snapshot, before anything is written.
	afterResolve func()
}

// GroupMove is a selected user or account whose group changed after the
// snapshot.
type GroupMove struct {
	Name     string // a user name, or "account owner/name"
	From, To string // group names; "no group" for none
}

// GoBDResult is a finished export.
type GoBDResult struct {
	*gobd.Result
	// Group is the export's group (nil: none) and Owners its owners, as
	// the snapshot saw them.
	Group  *attest.StatementGroup
	Owners []string
	// Missing are the messages without a valid file.
	Missing []gobd.Missing
	// Moved lists the users and accounts whose group changed after the
	// snapshot; the export is still complete as of the snapshot.
	Moved []GroupMove
	// Attested is the seq of the attestation written before the export,
	// 0 if none; Waited says it waited for one being written.
	Attested int64
	Waited   bool
}

// GoBDCheck is the command line's check before the audit entry: the same
// resolution and group check as the export's snapshot (in a short
// snapshot of its own), and the output path. It returns the group (nil:
// none) and the owners.
func GoBDCheck(ctx context.Context, st *store.Store, dataDir string, opts GoBDOptions) (*attest.StatementGroup, []string, error) {
	if err := checkGoBDOut(opts, dataDir); err != nil {
		return nil, nil, err
	}
	var state *gobdState
	if opts.Resume {
		var err error
		if state, err = readGoBDState(opts.Out); err != nil {
			return nil, nil, err
		}
		if err := state.matches(&opts); err != nil {
			return nil, nil, err
		}
	}
	conn, err := st.GoBDConn(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer conn.Close()
	var r *resolved
	err = conn.Snapshot(ctx, func(snap *store.GoBDSnapshot) error {
		var err error
		r, err = resolveSelection(ctx, snap, &opts.Selection, state)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	return r.group, r.owners, nil
}

// GoBDExport writes the export. A group or selection error comes before
// any file is written. A directory export interrupted later leaves
// Out.partial, which Resume continues; a ZIP is removed.
func GoBDExport(ctx context.Context, st *store.Store, blobs *blobstore.Store, opts GoBDOptions) (*GoBDResult, error) {
	if err := checkGoBDOut(opts, blobs.Root()); err != nil {
		return nil, err
	}
	loc, err := time.LoadLocation(opts.Selection.TimeZone)
	if err != nil {
		return nil, fmt.Errorf("%w: time zone %q: %w", ErrGoBDSelection, opts.Selection.TimeZone, err)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.NewID == nil {
		opts.NewID = newExportID
	}
	if opts.Open == nil {
		opts.Open = blobs.OpenMessage
	}
	if opts.FreeSpace == nil {
		opts.FreeSpace = freeSpace
	}
	if opts.Warn == nil {
		opts.Warn = io.Discard
	}
	var state *gobdState
	if opts.Resume {
		if state, err = readGoBDState(opts.Out); err != nil {
			return nil, err
		}
	}
	conn, err := st.GoBDConn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	res := &GoBDResult{}
	// Before the snapshot, on its connection: wait for an attestation
	// being written, or write one if messages came since the newest. The
	// snapshot then holds the attestation the statement names.
	if opts.Signer != nil && !opts.NoAttest {
		if err := attestBefore(ctx, st, conn, &opts, res); err != nil {
			return nil, err
		}
	}

	e := &gobdExport{opts: opts, loc: loc, state: state, res: res}
	err = conn.Snapshot(ctx, func(snap *store.GoBDSnapshot) error { return e.run(ctx, snap) })
	if err != nil {
		e.discard()
		return nil, err
	}
	// After the snapshot, read committed: did a selected user or account
	// change group meanwhile?
	now, err := conn.SelectionGroups(ctx, e.r.users, e.r.accounts, nil)
	if err != nil {
		return nil, fmt.Errorf("the export in %s is complete, but checking the groups after it failed: %w", opts.Out, err)
	}
	res.Moved = movedMembers(e.r.members, now)
	return res, nil
}

// attestBefore writes an attestation if messages were added since the
// newest one, or waits for the one being written.
func attestBefore(ctx context.Context, st *store.Store, conn *store.GoBDConn, opts *GoBDOptions, res *GoBDResult) error {
	fresh, err := conn.NewMessages(ctx)
	if err != nil || !fresh {
		return err
	}
	ar, err := Attest(ctx, st, opts.AttestDir, opts.Signer)
	switch {
	case errors.Is(err, ErrAttestRunning):
		if err := conn.WaitForAttestation(ctx); err != nil {
			return fmt.Errorf("wait for the attestation being written: %w", err)
		}
		res.Waited = true
	case err != nil:
		fmt.Fprintf(opts.Warn, "warning: no attestation before the export: %v\n", err)
	default:
		res.Attested = ar.Entry.Seq
	}
	return nil
}

func newExportID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func isZIP(out string) bool { return strings.HasSuffix(strings.ToLower(out), ".zip") }

// checkGoBDOut checks the output path: a ZIP is never resumed; out must
// not exist; <out>.partial must exist exactly when resuming; and out must
// not lie inside the archive's messages/ or tmp/.
func checkGoBDOut(opts GoBDOptions, dataDir string) error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", ErrGoBDSelection, fmt.Sprintf(format, a...))
	}
	if opts.Out == "" {
		return bad("no output path")
	}
	if isZIP(opts.Out) && opts.Resume {
		return bad("only a directory export can be resumed")
	}
	if _, err := exportTarget(opts.Out, dataDir); err != nil {
		return fmt.Errorf("%w: %w", ErrGoBDSelection, err)
	}
	_, err := os.Lstat(opts.Out + gobd.PartialSuffix)
	switch {
	case err == nil && !opts.Resume && !isZIP(opts.Out):
		return bad("%s exists: an interrupted export; resume it with --resume (same options) or remove it", opts.Out+gobd.PartialSuffix)
	case err == nil && !opts.Resume:
		return bad("%s exists: remove it", opts.Out+gobd.PartialSuffix)
	case errors.Is(err, fs.ErrNotExist) && opts.Resume:
		return bad("%s does not exist: there is no interrupted export to resume", opts.Out+gobd.PartialSuffix)
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return err
	}
	return nil
}

// resolved is a selection as the snapshot sees it.
type resolved struct {
	filter store.GoBDFilter
	group  *attest.StatementGroup
	owners []string
	// users and accounts are the IDs whose group is checked, members
	// their groups in the snapshot.
	users, accounts []int64
	members         []store.GoBDMember
	withHolds       bool
}

func selErr(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrGoBDSelection, fmt.Sprintf(format, a...))
}

// resolveSelection resolves the selection in the snapshot and checks its
// group: first, when resuming, against the state file's; then that it is
// one group; then against the claim.
func resolveSelection(ctx context.Context, snap *store.GoBDSnapshot, sel *GoBDSelection, state *gobdState) (*resolved, error) {
	r := &resolved{withHolds: sel.HoldID != 0}
	users := slices.Clone(sel.Owners)
	accounts := maps.Clone(sel.Accounts)
	if accounts == nil {
		accounts = map[int64][]int64{}
	}
	var groupID *int64 // a group selected as a whole
	if sel.WholeGroup {
		if !sel.Group.Given || sel.Group.ID == nil {
			return nil, selErr("a whole-group export needs the group")
		}
		groupID = sel.Group.ID
	}
	var grant *store.GoBDGrant
	if sel.HoldID != 0 {
		h, released, err := snap.Hold(ctx, sel.HoldID)
		if errors.Is(err, store.ErrNotFound) {
			return nil, selErr("legal hold %d not found", sel.HoldID)
		}
		if err != nil {
			return nil, err
		}
		switch {
		case released:
			return nil, selErr("legal hold %d is released", sel.HoldID)
		case h.Instance():
			// M2: a hold over every customer is never exported as a whole.
			return nil, selErr("legal hold %d covers every customer; name the users with --user", sel.HoldID)
		case h.AccountID != nil:
			if _, ok := accounts[*h.AccountID]; !ok {
				accounts[*h.AccountID] = nil
			}
		case h.OwnerID != nil:
			users = append(users, *h.OwnerID)
		default:
			if groupID != nil && *groupID != *h.GroupID {
				return nil, selErr("legal hold %d names another customer group", sel.HoldID)
			}
			groupID = h.GroupID
		}
		id := sel.HoldID
		r.filter.HoldID = &id
	}
	if sel.GrantID != 0 {
		g, err := snap.Grant(ctx, sel.GrantID)
		if errors.Is(err, store.ErrNotFound) {
			return nil, selErr("auditor grant %d not found", sel.GrantID)
		}
		if err != nil {
			return nil, err
		}
		if g.Revoked {
			return nil, selErr("auditor grant %d was revoked", sel.GrantID)
		}
		grant = g
		users = append(users, g.OwnerID)
	}
	if groupID != nil {
		us, err := snap.GroupUsers(ctx, *groupID)
		if err != nil {
			return nil, err
		}
		for _, u := range us {
			users = append(users, u.ID)
		}
	}
	slices.Sort(users)
	users = slices.Compact(users)
	r.users = users
	r.accounts = slices.Sorted(maps.Keys(accounts))

	claimed := groupID
	if claimed == nil && sel.Group.Given {
		claimed = sel.Group.ID
	}
	members, err := snap.SelectionGroups(ctx, r.users, r.accounts, claimed)
	if err != nil {
		return nil, err
	}
	group, err := checkGroups(members, sel, groupID, r.users, r.accounts, state)
	if err != nil {
		return nil, err
	}
	r.group = group
	for _, m := range members {
		if m.Kind != "group" {
			r.members = append(r.members, m)
		}
	}

	// The scope. A grant's is its owner's accounts narrowed to the grant
	// (RestrictScope with the grant's period), never combined with other
	// selections (the command line refuses that). Otherwise each owner's
	// accounts (OwnerScope), each named account with its folders
	// (RestrictScope), and the accounts without an owner, also narrowed to
	// their named folders, merged. Then the period.
	owners := map[string]bool{}
	for _, m := range members {
		if m.Kind == "user" {
			owners[m.Name] = true
		}
	}
	var merged store.Scope
	if grant != nil {
		sc, err := snap.OwnerScope(ctx, grant.OwnerID)
		if err != nil {
			return nil, err
		}
		if merged, err = snap.RestrictScope(ctx, sc, grant.Accounts, grant.PeriodFrom, grant.PeriodUntil); err != nil {
			return nil, err
		}
	} else {
		var scopes []store.Scope
		for _, u := range users {
			sc, err := snap.OwnerScope(ctx, u)
			if err != nil {
				return nil, err
			}
			scopes = append(scopes, sc)
		}
		if len(accounts) > 0 {
			owned, unowned, err := snap.SplitAccounts(ctx, r.accounts)
			if err != nil {
				return nil, err
			}
			// The accounts without an owner come with all their folders:
			// narrow them to the named folders too, as an owned account is.
			named := make(map[int64][]int64, len(unowned.Accounts))
			for _, acc := range unowned.Accounts {
				named[acc.ID] = accounts[acc.ID]
			}
			if unowned, err = snap.RestrictScope(ctx, unowned, named, nil, nil); err != nil {
				return nil, err
			}
			scopes = append(scopes, unowned)
			byOwner := map[int64]map[int64][]int64{}
			for id, owner := range owned {
				if byOwner[owner] == nil {
					byOwner[owner] = map[int64][]int64{}
				}
				byOwner[owner][id] = accounts[id]
			}
			for _, owner := range slices.Sorted(maps.Keys(byOwner)) {
				sc, err := snap.OwnerScope(ctx, owner)
				if err != nil {
					return nil, err
				}
				if sc, err = snap.RestrictScope(ctx, sc, byOwner[owner], nil, nil); err != nil {
					return nil, err
				}
				scopes = append(scopes, sc)
			}
		}
		var err error
		if merged, err = store.MergeScopes(scopes...); err != nil {
			return nil, err
		}
	}
	r.filter.Scope = merged.Restrict(nil, sel.Since, sel.Until)
	for _, a := range merged.Accounts {
		if a.Owner != "" {
			owners[a.Owner] = true
		}
	}
	if r.group == nil && len(owners) > 0 {
		return nil, errors.New("gobd: owners in an export without a group")
	}
	r.owners = slices.Sorted(maps.Keys(owners))
	return r, nil
}

func groupLabel(id *int64, name string) string {
	if id == nil {
		return "no group"
	}
	return name
}

// checkGroups returns the selection's one group (nil for none), or why
// there is none: on resume a different group than the state file's, then
// a mixed selection, then one that differs from the claim. Nothing is
// ever filtered out.
func checkGroups(members []store.GoBDMember, sel *GoBDSelection, groupID *int64, users, accounts []int64, state *gobdState) (*attest.StatementGroup, error) {
	type key struct {
		none bool
		id   int64
	}
	groups := map[key]string{}
	var named *store.GoBDMember
	seenUsers, seenAccounts := map[int64]bool{}, map[int64]bool{}
	for i := range members {
		m := &members[i]
		switch m.Kind {
		case "group":
			named = m
			continue
		case "user":
			seenUsers[m.ID] = true
		case "account":
			seenAccounts[m.ID] = true
		}
		if m.GroupID == nil {
			groups[key{none: true}] = ""
		} else {
			groups[key{id: *m.GroupID}] = m.Group
		}
	}
	for _, u := range users {
		if !seenUsers[u] {
			return nil, selErr("user %d was removed", u)
		}
	}
	for _, a := range accounts {
		if !seenAccounts[a] {
			return nil, selErr("account %d was removed", a)
		}
	}
	if groupID != nil {
		if named == nil {
			return nil, selErr("customer group %d not found", *groupID)
		}
		groups[key{id: named.ID}] = named.Group
	}
	if len(groups) == 0 {
		return nil, selErr("nothing is selected")
	}
	if state != nil {
		if len(groups) != 1 {
			return nil, ErrGroupChanged
		}
		for k := range groups {
			if k.none != (state.GroupID == nil) || (!k.none && k.id != *state.GroupID) {
				return nil, ErrGroupChanged
			}
		}
	}
	if len(groups) > 1 {
		var parts []string
		for _, m := range members {
			if m.Kind == "group" {
				continue
			}
			label := m.Name
			if m.Kind == "account" {
				label = fmt.Sprintf("account %q", m.Name)
			}
			parts = append(parts, fmt.Sprintf("%s (%s)", label, groupLabel(m.GroupID, m.Group)))
		}
		return nil, fmt.Errorf("%w: %s; one export covers one customer group", ErrSelectionSpansGroups, strings.Join(parts, ", "))
	}
	var out *attest.StatementGroup
	for k, name := range groups {
		if !k.none {
			out = &attest.StatementGroup{ID: k.id, Name: name}
		}
	}
	if c := sel.Group; c.Given {
		switch {
		case c.ID == nil && out != nil:
			return nil, fmt.Errorf("%w: the selection is in %s, not without a group", ErrGroupClaimMismatch, out.Name)
		case c.ID != nil && out == nil:
			return nil, fmt.Errorf("%w: the selection has no group, not %s", ErrGroupClaimMismatch, c.Name)
		case c.ID != nil && *c.ID != out.ID:
			return nil, fmt.Errorf("%w: the selection is in %s, not %s", ErrGroupClaimMismatch, out.Name, c.Name)
		}
	}
	return out, nil
}

// movedMembers compares the groups of the selected users and accounts
// after the snapshot with those in it.
func movedMembers(before, after []store.GoBDMember) []GroupMove {
	type key struct {
		kind string
		id   int64
	}
	now := map[key]store.GoBDMember{}
	for _, m := range after {
		now[key{m.Kind, m.ID}] = m
	}
	var out []GroupMove
	for _, b := range before {
		a, ok := now[key{b.Kind, b.ID}]
		if !ok {
			continue // removed meanwhile: not a move to another customer
		}
		if (a.GroupID == nil) != (b.GroupID == nil) || (a.GroupID != nil && *a.GroupID != *b.GroupID) {
			name := b.Name
			if b.Kind == "account" {
				name = "account " + b.Name
			}
			out = append(out, GroupMove{Name: name, From: groupLabel(b.GroupID, b.Group), To: groupLabel(a.GroupID, a.Group)})
		}
	}
	return out
}

// gobdState is <out>.partial/.export-state.json: what a resumed export
// must keep.
type gobdState struct {
	Version       int    `json:"version"`
	ID            string `json:"id"`
	Selection     string `json:"selection"`
	TimeZone      string `json:"time_zone"`
	MaxLocationID int64  `json:"max_location_id"`
	GroupID       *int64 `json:"group_id"`
	WithChain     bool   `json:"with_chain"`
	WithLeaves    bool   `json:"with_leaves"`
}

// matches refuses a resume whose options differ from the interrupted
// export's.
func (s *gobdState) matches(opts *GoBDOptions) error {
	if s.Selection != opts.Selection.Text || s.TimeZone != opts.Selection.TimeZone ||
		s.WithChain != opts.WithChain || s.WithLeaves != opts.WithLeaves {
		return selErr("the options differ from those of the interrupted export; resume with the same options")
	}
	return nil
}

// stateFile is the resume state's name in <out>.partial. It never ends up
// in the export: DirSink removes it before the rename.
const stateFile = ".export-state.json"

func readGoBDState(out string) (*gobdState, error) {
	data, err := os.ReadFile(filepath.Join(out+gobd.PartialSuffix, stateFile)) //nolint:gosec // the export's own directory
	if errors.Is(err, fs.ErrNotExist) {
		return nil, selErr("%s has no %s: it cannot be resumed; remove it and start again", out+gobd.PartialSuffix, stateFile)
	}
	if err != nil {
		return nil, err
	}
	var s gobdState
	if err := json.Unmarshal(data, &s); err != nil || s.Version != 1 || s.ID == "" {
		return nil, selErr("%s is damaged; remove %s and start again", stateFile, out+gobd.PartialSuffix)
	}
	return &s, nil
}

// gobdExport is one run of an export inside the snapshot.
type gobdExport struct {
	opts  GoBDOptions
	loc   *time.Location
	state *gobdState
	res   *GoBDResult
	r     *resolved

	zipFile *os.File // the ZIP being written, <out>.partial
	sink    gobd.Sink
	w       *gobd.Writer
	missing map[string]bool
	atts    map[int64]bool // attestations the messages name (leaves)
}

// discard removes a ZIP that was not finished; a directory stays for
// --resume.
func (e *gobdExport) discard() {
	if e.zipFile != nil {
		_ = e.zipFile.Close()
		_ = os.Remove(e.zipFile.Name())
	}
}

func (e *gobdExport) run(ctx context.Context, snap *store.GoBDSnapshot) error {
	opts := &e.opts
	sel := &opts.Selection
	r, err := resolveSelection(ctx, snap, sel, e.state)
	if err != nil {
		return err
	}
	e.r = r
	if opts.afterResolve != nil {
		opts.afterResolve()
	}
	if e.state != nil {
		if err := e.state.matches(opts); err != nil {
			return err
		}
		r.filter.MaxLocationID = e.state.MaxLocationID
	} else if r.filter.MaxLocationID, err = snap.MaxLocationID(ctx); err != nil {
		return err
	}
	att, err := snap.LastAttestation(ctx)
	if err != nil {
		return err
	}
	messages, bytes, err := snap.Totals(ctx, r.filter)
	if err != nil {
		return err
	}
	maxZIP := cmp.Or(opts.maxZIP, gobd.MaxZIPMessages)
	if isZIP(opts.Out) && messages > maxZIP {
		return selErr("%d messages are more than a ZIP holds (%d); use a directory or split by year", messages, maxZIP)
	}

	id := opts.NewID()
	if e.state != nil {
		id = e.state.ID
	}
	st := attest.Statement{
		ID: id, Created: opts.Now().UTC().Truncate(time.Second), Software: opts.Software,
		Selection: sel.Text, TimeZone: sel.TimeZone, Group: r.group, Owners: r.owners,
		Resumed: e.state != nil, FullChain: opts.WithChain, Leaves: opts.WithLeaves,
	}
	if att != nil {
		st.AttestationSeq, st.AttestationHash = att.Seq, att.Hash
	}
	wopts := gobd.Options{Statement: st, Location: e.loc, Signer: opts.Signer}

	if err := e.openSink(ctx, snap, id, messages, bytes, r.group); err != nil {
		return err
	}
	if isZIP(opts.Out) {
		wopts.MaxMessages = maxZIP
	}
	if e.w, err = gobd.NewWriter(e.sink, wopts); err != nil {
		return err
	}
	e.missing = map[string]bool{}
	e.atts = map[int64]bool{}
	attUpto := st.AttestationSeq

	// Pass B: the message files.
	var done int64
	err = snap.Messages(ctx, r.filter, attUpto, func(m *store.GoBDMessage) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := e.addFile(m); err != nil {
			return err
		}
		done++
		if opts.Progress != nil {
			return opts.Progress(done)
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Pass A1: messages.csv.
	err = snap.Messages(ctx, r.filter, attUpto, func(m *store.GoBDMessage) error {
		if e.missing[m.SHA256] {
			return nil
		}
		if m.Attestation > 0 {
			e.atts[m.Attestation] = true
		}
		return e.w.AddMessageRow(messageOf(m))
	})
	if err != nil {
		return err
	}
	// Pass A2: locations.csv.
	err = snap.Locations(ctx, r.filter, r.withHolds, func(l *store.GoBDLocation) error {
		if e.missing[l.SHA256] {
			return nil
		}
		return e.w.AddLocationRow(gobd.Location{
			SHA256: l.SHA256, Owner: l.Owner, Account: l.Account, AccountKind: l.AccountKind, Folder: l.Folder,
			UIDValidity: l.UIDValidity, UID: l.UID, Flags: l.Flags, InternalDate: l.InternalDate,
			FirstSeen: l.FirstSeen, LastSeen: l.LastSeen, GoneAt: l.GoneAt,
			EnvelopeFrom: l.EnvelopeFrom, EnvelopeTo: l.EnvelopeTo, ReceivedAt: l.ReceivedAt, RetainUntil: l.RetainUntil,
			Holds: l.Holds,
		})
	})
	if err != nil {
		return err
	}
	// Pass C: missing.csv, attest/, README, index, SHA256SUMS, STATEMENT.
	final, err := e.final(ctx, snap, att)
	if err != nil {
		return err
	}
	out, err := e.w.Finish(*final)
	if err != nil {
		return err
	}
	if e.zipFile != nil {
		if err := e.finishZIP(); err != nil {
			return err
		}
	}
	e.res.Result = out
	e.res.Group, e.res.Owners = r.group, r.owners
	return nil
}

// openSink creates the directory or ZIP, after the checks and the
// free-space estimate: nothing is written for a refused selection.
func (e *gobdExport) openSink(ctx context.Context, snap *store.GoBDSnapshot, id string, messages, bytes int64, group *attest.StatementGroup) error {
	opts := &e.opts
	parent := filepath.Dir(opts.Out)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	var have int64
	if e.state != nil {
		ds, err := gobd.ResumeDirSink(opts.Out)
		if err != nil {
			return err
		}
		if have, err = e.cleanResume(ctx, snap, ds.Dir()); err != nil {
			return err
		}
		ds.DropOnClose(stateFile)
		e.sink = ds
	}
	// The emls, about 1 KiB of CSV per message, and the small files, plus
	// 2 %.
	need := (bytes - have + messages*1024 + 1<<20) * 102 / 100
	free, err := opts.FreeSpace(parent)
	if err != nil {
		return err
	}
	if need > 0 && free < uint64(need) {
		return selErr("about %d MB are needed in %s, %d MB are free", need>>20, parent, free>>20)
	}
	if e.sink != nil {
		return nil
	}
	if isZIP(opts.Out) {
		f, err := os.OpenFile(opts.Out+gobd.PartialSuffix, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		e.zipFile = f
		e.sink = gobd.NewZipSink(f, opts.Now().UTC().Truncate(time.Second))
		return nil
	}
	ds, err := gobd.NewDirSink(opts.Out)
	if err != nil {
		return err
	}
	state := gobdState{
		Version: 1, ID: id, Selection: opts.Selection.Text, TimeZone: opts.Selection.TimeZone,
		MaxLocationID: e.r.filter.MaxLocationID, WithChain: opts.WithChain, WithLeaves: opts.WithLeaves,
	}
	if group != nil {
		id := group.ID
		state.GroupID = &id
	}
	e.sink = ds
	ds.DropOnClose(stateFile)
	return e.writeState(ds.Dir(), &state)
}

func (e *gobdExport) writeState(dir string, s *gobdState) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, stateFile+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, stateFile))
}

// cleanResume prepares an interrupted export's directory: everything but
// mail/ and the state file goes (it is written again), and in mail/ every
// file that is not the .eml of a selected message at its path. It returns
// the bytes of the emls kept, for the free-space check.
func (e *gobdExport) cleanResume(ctx context.Context, snap *store.GoBDSnapshot, dir string) (int64, error) {
	// Everything goes through a root at dir: a symbolic link in the
	// directory never leads a removal out of it.
	root, err := os.OpenRoot(dir)
	if err != nil {
		return 0, err
	}
	defer func() { _ = root.Close() }()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return 0, err
	}
	for _, d := range entries {
		if d.Name() == stateFile || d.Name() == "mail" && d.IsDir() {
			continue
		}
		if err := root.RemoveAll(d.Name()); err != nil {
			return 0, err
		}
	}
	if _, err := root.Lstat("mail"); errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	var kept int64
	type file struct {
		rel  string
		sha  string
		size int64
	}
	var batch []file
	flush := func() error {
		shas := make([]string, len(batch))
		for i, f := range batch {
			shas[i] = f.sha
		}
		sel, err := snap.SelectedAmong(ctx, e.r.filter, shas)
		if err != nil {
			return err
		}
		for _, f := range batch {
			sortAt, ok := sel[f.sha]
			if ok && gobd.MessagePath(f.sha, sortAt, e.loc) == f.rel {
				kept += f.size
				continue
			}
			if err := root.Remove(f.rel); err != nil {
				return err
			}
		}
		batch = batch[:0]
		return nil
	}
	err = fs.WalkDir(root.FS(), "mail", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		sha, ok := strings.CutSuffix(d.Name(), ".eml")
		info, ierr := d.Info()
		if !ok || !blobstore.IsHash(sha) || !d.Type().IsRegular() || ierr != nil {
			return root.RemoveAll(rel)
		}
		batch = append(batch, file{rel: rel, sha: sha, size: info.Size()})
		if len(batch) >= 1000 {
			return flush()
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if len(batch) > 0 {
		if err := flush(); err != nil {
			return 0, err
		}
	}
	return kept, nil
}

func messageOf(m *store.GoBDMessage) gobd.Message {
	return gobd.Message{
		SHA256: m.SHA256, Size: m.Size, SortAt: m.SortAt,
		From: m.From, To: m.To, Cc: m.Cc, Subject: m.Subject, MessageID: m.MessageID, InReplyTo: m.InReplyTo,
		HasAttachment: m.HasAttachment, AttachmentNames: m.AttachmentNames,
		ArchivedAt: m.ArchivedAt, ArchiveSeq: m.Seq, Attestation: m.Attestation,
	}
}

// addFile writes one message file, or lists the message as missing. A
// resumed export keeps a file that is already right without reading the
// archive's copy.
func (e *gobdExport) addFile(m *store.GoBDMessage) error {
	msg := messageOf(m)
	if e.state != nil {
		kept, err := e.w.KeepFile(msg)
		if err != nil || kept {
			return err
		}
	}
	miss := func(reason string) error {
		e.missing[m.SHA256] = true
		e.res.Missing = append(e.res.Missing, gobd.Missing{SHA256: m.SHA256, SortAt: m.SortAt, Reason: reason})
		return e.w.AddMissing(gobd.Missing{SHA256: m.SHA256, SortAt: m.SortAt, Reason: reason})
	}
	f, err := e.opts.Open(m.SHA256)
	if errors.Is(err, fs.ErrNotExist) {
		return miss("file missing")
	}
	if err != nil {
		return miss("file cannot be read: " + err.Error())
	}
	defer func() { _ = f.Close() }()
	// A wrong size is found before anything is written, which a ZIP
	// could not take back. Size is the plaintext's, also of an encrypted
	// file.
	if f.Size() != m.Size {
		return miss("file damaged (size differs)")
	}
	err = e.w.AddFile(msg, f)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, gobd.ErrCannotUndo):
		return fmt.Errorf("message %s could not be copied as archived (%w), and a ZIP cannot leave it out once written; "+
			"run verify, or export to a directory, which lists it in missing.csv", m.SHA256, err)
	case e.w.Err() != nil:
		return err // the export itself failed
	case errors.Is(err, gobd.ErrMismatch):
		return miss("file damaged (SHA-256 differs)")
	}
	return miss("file cannot be read: " + err.Error())
}

// final collects what pass C writes besides the tables.
func (e *gobdExport) final(ctx context.Context, snap *store.GoBDSnapshot, att *attest.Entry) (*gobd.Final, error) {
	opts := &e.opts
	f := &gobd.Final{}
	if att != nil {
		if opts.WithChain {
			chain, err := snap.Chain(ctx, att.Seq)
			if err != nil {
				return nil, err
			}
			f.Chain = chain
		} else {
			f.Chain = []attest.Entry{*att}
		}
	}
	for _, k := range []struct {
		name string
		dst  *[]byte
	}{{attest.PublicKeyFile, &f.PublicKey}, {attest.RetiredKeysFile, &f.RetiredKeys}} {
		data, err := os.ReadFile(filepath.Join(opts.AttestDir, k.name)) //nolint:gosec // fixed names in the attestation directory
		switch {
		case err == nil:
			*k.dst = data
		case !errors.Is(err, fs.ErrNotExist):
			return nil, err
		}
	}
	if opts.WithLeaves {
		seqs := slices.Sorted(maps.Keys(e.atts))
		f.Leaves = func(add func(seq int64, r io.Reader) error) error {
			for _, seq := range seqs {
				lf, err := os.Open(attest.LeavesPath(opts.AttestDir, seq)) //nolint:gosec // the attestation directory
				if err != nil {
					return fmt.Errorf("leaves of attestation %d: %w", seq, err)
				}
				err = add(seq, lf)
				_ = lf.Close()
				if err != nil {
					return err
				}
			}
			return nil
		}
	}
	f.Files = func(yield func(string) error) error {
		return snap.Messages(ctx, e.r.filter, att0(att), func(m *store.GoBDMessage) error {
			if e.missing[m.SHA256] {
				return nil
			}
			return yield(gobd.MessagePath(m.SHA256, m.SortAt, e.loc))
		})
	}
	return f, nil
}

func att0(a *attest.Entry) int64 {
	if a == nil {
		return 0
	}
	return a.Seq
}

// finishZIP syncs the ZIP and gives it its name.
func (e *gobdExport) finishZIP() error {
	f := e.zipFile
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	e.zipFile = nil
	if err := mustNotExistPath(e.opts.Out); err != nil {
		_ = os.Remove(f.Name())
		return err
	}
	if err := os.Rename(f.Name(), e.opts.Out); err != nil {
		return err
	}
	return syncParent(e.opts.Out)
}

func mustNotExistPath(p string) error {
	if _, err := os.Lstat(p); err == nil {
		return fmt.Errorf("%s was created meanwhile", p)
	}
	return nil
}

func syncParent(p string) error {
	d, err := os.Open(filepath.Dir(p))
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}
