package store_test

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/store"
)

// usageModel is what the storage figures must be, kept in Go: which
// account holds which message (gone locations count), who owns each
// account and which group each user is in.
type usageModel struct {
	size    map[string]int64          // live messages
	held    map[int64]map[string]bool // account → messages
	owner   map[int64]*int64          // account → owner
	group   map[int64]int64           // user → group
	retired map[string]bool           // deleted messages, never added again
}

func (m *usageModel) groupOf(account int64) *int64 {
	o := m.owner[account]
	if o == nil {
		return nil
	}
	g := m.group[*o]
	return &g
}

func (m *usageModel) add(account int64, metas []store.MessageMeta) {
	if m.held[account] == nil {
		m.held[account] = map[string]bool{}
	}
	for _, x := range metas {
		m.held[account][x.SHA256] = true
		m.size[x.SHA256] = x.Size
	}
}

// remove deletes messages everywhere (a deletion run).
func (m *usageModel) remove(shas []string) {
	for _, sha := range shas {
		delete(m.size, sha)
		m.retired[sha] = true
		for _, h := range m.held {
			delete(h, sha)
		}
	}
}

// purge removes an account; the messages no other account holds go.
func (m *usageModel) purge(account int64) {
	mine := m.held[account]
	delete(m.held, account)
	delete(m.owner, account)
	for sha := range mine {
		if !m.heldAnywhere(sha) {
			delete(m.size, sha)
			m.retired[sha] = true
		}
	}
}

func (m *usageModel) heldAnywhere(sha string) bool {
	for _, h := range m.held {
		if h[sha] {
			return true
		}
	}
	return false
}

func (m *usageModel) instance() store.Usage {
	var u store.Usage
	for _, s := range m.size {
		u.Messages++
		u.Bytes += s
	}
	return u
}

// usageOf counts the distinct messages of the accounts that match.
func (m *usageModel) usageOf(match func(account int64) bool) store.Usage {
	seen := map[string]bool{}
	var u store.Usage
	for a, h := range m.held {
		if !match(a) {
			continue
		}
		for sha := range h {
			if !seen[sha] {
				seen[sha] = true
				u.Messages++
				u.Bytes += m.size[sha]
			}
		}
	}
	return u
}

func (m *usageModel) groupUsage(g int64) store.Usage {
	return m.usageOf(func(a int64) bool { gg := m.groupOf(a); return gg != nil && *gg == g })
}

func (m *usageModel) ungrouped() store.Usage {
	return m.usageOf(func(a int64) bool { return m.groupOf(a) == nil })
}

// account returns an account's messages, bytes and exclusive bytes.
func (m *usageModel) account(a int64) (n, bytes, exclusive int64) {
	for sha := range m.held[a] {
		n++
		bytes += m.size[sha]
		alone := true
		for b, h := range m.held {
			if b != a && h[sha] {
				alone = false
			}
		}
		if alone {
			exclusive += m.size[sha]
		}
	}
	return n, bytes, exclusive
}

// modelRun is one seeded sequence against a fresh database.
type modelRun struct {
	*usageFixture
	rnd   *rand.Rand
	model *usageModel
	log   []string
	// Since the last snapshot: groups that gained accounts or users
	// (their figure may be below the model), groups that changed at all,
	// and whether anything was deleted.
	gained, changed map[int64]bool
	deleted         bool

	groups   []int64
	users    []int64
	imports  []*store.Folder // import accounts: sync batches
	smtp     []*store.Account
	rules    map[int64]int64 // account → retention rule
	fill     *store.FillFolder
	journal  int64 // journal folder of smtp[0]
	importID int64
	legacy   *store.FillFolder // a fill from before #112, until converted
	legacyF  *store.Fill
	purged   bool
}

func (r *modelRun) fatalf(format string, args ...any) {
	r.t.Helper()
	r.t.Fatalf("%s\nsequence:\n  %s", fmt.Sprintf(format, args...), strings.Join(r.log, "\n  "))
}

func (r *modelRun) must(err error, what string) {
	r.t.Helper()
	if err != nil {
		r.fatalf("%s: %v", what, err)
	}
}

// pick returns 1 to 5 messages from a pool of 40, duplicates possible,
// never one deleted before (a tombstone would skip it). Half are old
// enough for the retention rules.
func (r *modelRun) pick() []store.MessageMeta {
	var out []store.MessageMeta
	for range 1 + r.rnd.IntN(5) {
		i := r.rnd.IntN(40)
		m := usageMsg(100+i, int64(i*7+1))
		if r.model.retired[m.SHA256] {
			continue
		}
		at := time.Now().Add(-time.Hour)
		if i%2 == 0 {
			at = time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
		}
		m.SentAt = &at
		out = append(out, m)
	}
	return out
}

func (r *modelRun) sentAt(metas []store.MessageMeta) []store.Location {
	locs := make([]store.Location, len(metas))
	for i, m := range metas {
		locs[i] = store.Location{InternalDate: *m.SentAt}
	}
	return locs
}

func shortSHAs(metas []store.MessageMeta) string {
	var s []string
	for _, m := range metas {
		s = append(s, strings.TrimLeft(m.SHA256, "0"))
	}
	return strings.Join(s, ",")
}

func (r *modelRun) setup() {
	f := r.usageFixture
	// Before the first user: an account without an owner.
	early := f.folder(nil, "early")
	r.model.owner[early.AccountID] = nil
	for range 1 + r.rnd.IntN(3) {
		metas := r.pick()
		r.syncInto(early, metas)
	}
	r.refresh()
	g1 := f.defaultGroup()
	u1 := f.user("u1", g1) // adopts "early"
	r.log = append(r.log, "adoption by u1")
	r.model.owner[early.AccountID] = &u1.ID
	r.model.group[u1.ID] = g1
	r.markGained(g1)
	r.checkInstance()
	r.groups = []int64{g1, f.group("G2"), f.group("G3")}
	r.users = []int64{u1.ID}
	for i, g := range []int64{r.groups[1], r.groups[2], g1} {
		u := f.user(fmt.Sprintf("u%d", i+2), g)
		r.users = append(r.users, u.ID)
		r.model.group[u.ID] = g
	}
	own := func(i int) *int64 { return &r.users[i] }
	r.imports = []*store.Folder{early, f.folder(own(0), "a1"), f.folder(own(1), "a2"), f.folder(own(2), "a3"), f.folder(nil, "nobody")}
	for _, fo := range r.imports[1:] {
		acc, err := f.st.GetAccount(f.ctx, fo.AccountID)
		r.must(err, "account")
		r.model.owner[fo.AccountID] = acc.OwnerID
	}
	r.smtp = []*store.Account{
		createSMTPAccount(f.t, f.st, own(3), "j1", "j1@example.com"),
		createSMTPAccount(f.t, f.st, own(1), "j2", "j2@example.com"),
	}
	for _, a := range r.smtp {
		r.model.owner[a.ID] = a.OwnerID
	}
	// Retention rules on every account, so that deletion runs find the
	// old messages.
	r.rules = map[int64]int64{}
	for a := range r.model.owner {
		id, err := f.st.AddRetentionRule(f.ctx, store.NewRetentionRule{AccountID: a, Years: 1})
		r.must(err, "rule")
		r.rules[a] = id
	}
	// A recurring fill and a journal import on j1, a fill from before
	// #112 on j2.
	fill := newRecurringFill(r.smtp[0], "alice@example.com")
	r.must(f.st.CreateFill(f.ctx, fill, seal), "fill")
	var err error
	r.fill, err = f.st.FillFolder(f.ctx, fill, "INBOX")
	r.must(err, "fill folder")
	r.journal, err = f.st.JournalFolderID(f.ctx, r.smtp[0].ID)
	r.must(err, "journal")
	imp := &store.SMTPImport{AccountID: r.smtp[0].ID, Label: "piler", Format: "eml", Source: "/import/piler"}
	r.must(f.st.CreateSMTPImport(f.ctx, imp), "import")
	r.importID = imp.ID
	r.legacyF = newFill(r.smtp[1], "bob@example.com")
	r.must(f.st.CreateFill(f.ctx, r.legacyF, seal), "legacy fill")
	f.exec("UPDATE smtp_fills SET folder_prefix = 'legacy' WHERE id = $1", r.legacyF.ID)
	r.legacyF, err = f.st.GetFill(f.ctx, r.legacyF.ID)
	r.must(err, "legacy fill")
	r.legacy, err = f.st.FillFolder(f.ctx, r.legacyF, "INBOX")
	r.must(err, "legacy folder")
	if r.legacy.FolderName != "legacy/INBOX" {
		r.fatalf("legacy folder %q", r.legacy.FolderName)
	}
	r.refresh()
}

func (r *modelRun) markGained(g int64) { r.gained[g], r.changed[g] = true, true }

func (r *modelRun) syncInto(fo *store.Folder, metas []store.MessageMeta) {
	r.log = append(r.log, fmt.Sprintf("sync into account %d: %s", fo.AccountID, shortSHAs(metas)))
	locs := r.sentAt(metas)
	for i := range locs {
		r.uid++
		locs[i].FolderID, locs[i].UIDValidity, locs[i].UID = fo.ID, fo.UIDValidity, r.uid
	}
	_, err := r.st.SaveBatch(r.ctx, fo.ID, r.uid, metas, locs)
	r.must(err, "sync")
	r.model.add(fo.AccountID, metas)
}

func (r *modelRun) refresh() {
	r.t.Helper()
	r.log = append(r.log, "refresh")
	res, err := r.st.RefreshUsage(r.ctx)
	r.must(err, "refresh")
	if res.Again || res.Drift() != 0 {
		r.fatalf("refresh %+v", res)
	}
	r.gained, r.changed, r.deleted = map[int64]bool{}, map[int64]bool{}, false
	u := r.usage()
	for _, g := range u.Groups {
		if want := r.model.groupUsage(g.ID); g.Usage != want {
			r.fatalf("after the snapshot, group %s: %+v, model %+v", g.Name, g.Usage, want)
		}
	}
	if want := r.model.ungrouped(); u.Ungrouped != want {
		r.fatalf("after the snapshot, without group: %+v, model %+v", u.Ungrouped, want)
	}
	for _, a := range u.Accounts {
		n, b, x := r.model.account(a.ID)
		if a.Messages != n || a.Bytes != b || a.ExclusiveBytes != x {
			r.fatalf("after the snapshot, account %s: %d/%d/%d, model %d/%d/%d", a.Name, a.Messages, a.Bytes, a.ExclusiveBytes, n, b, x)
		}
	}
	r.checkInstance()
}

func (r *modelRun) checkInstance() {
	r.t.Helper()
	if got, want := r.totals(), r.model.instance(); got != want {
		r.fatalf("instance %+v, model %+v", got, want)
	}
}

// checkBetween: between snapshots each group's figure is at least the
// model, unless it gained accounts or users; and equal to it when nothing
// changed it and nothing was deleted.
func (r *modelRun) checkBetween() {
	r.t.Helper()
	r.checkInstance()
	for _, g := range r.usage().Groups {
		want := r.model.groupUsage(g.ID)
		if !r.gained[g.ID] && (g.Messages < want.Messages || g.Bytes < want.Bytes) {
			r.fatalf("group %s below the model: %+v, model %+v", g.Name, g.Usage, want)
		}
		if !r.changed[g.ID] && !r.deleted && g.Usage != want {
			r.fatalf("group %s: %+v, model %+v", g.Name, g.Usage, want)
		}
	}
}

func (r *modelRun) live(fo *store.Folder) bool {
	_, ok := r.model.owner[fo.AccountID]
	return ok
}

// step runs one random operation.
func (r *modelRun) step() {
	f := r.usageFixture
	switch n := r.rnd.IntN(100); {
	case n < 25: // a sync or import batch
		fo := r.imports[r.rnd.IntN(len(r.imports))]
		if r.live(fo) {
			r.syncInto(fo, r.pick())
		}
	case n < 38: // SMTP receives
		a := r.smtp[r.rnd.IntN(len(r.smtp))]
		for _, m := range r.pick() {
			r.log = append(r.log, fmt.Sprintf("receive into account %d: %s", a.ID, shortSHAs([]store.MessageMeta{m})))
			env := envelope("x@example.com")
			env.ReceivedAt = *m.SentAt
			_, err := f.st.ReceiveMessage(f.ctx, a.ID, m, env, present)
			r.must(err, "receive")
			r.model.add(a.ID, []store.MessageMeta{m})
		}
	case n < 46: // a recurring fill's batch
		metas := r.pick()
		r.log = append(r.log, fmt.Sprintf("fill into account %d: %s", r.smtp[0].ID, shortSHAs(metas)))
		r.fill.Counts.Imported += len(metas)
		_, err := f.st.SaveFillBatch(f.ctx, r.fill, metas, r.sentAt(metas))
		r.must(err, "fill")
		r.model.add(r.smtp[0].ID, metas)
	case n < 54: // a journal import's batch
		metas := r.pick()
		r.log = append(r.log, fmt.Sprintf("journal import into account %d: %s", r.smtp[0].ID, shortSHAs(metas)))
		_, _, err := f.st.SaveJournalImportBatch(f.ctx, r.journal, r.importID, metas, r.sentAt(metas))
		r.must(err, "journal import")
		r.model.add(r.smtp[0].ID, metas)
	case n < 59: // a batch of the fill from before #112, or its conversion
		if r.legacy == nil {
			return
		}
		if r.rnd.IntN(3) > 0 {
			metas := r.pick()
			r.log = append(r.log, fmt.Sprintf("legacy fill into account %d: %s", r.smtp[1].ID, shortSHAs(metas)))
			r.legacy.Counts.Imported += len(metas)
			_, err := f.st.SaveFillBatch(f.ctx, r.legacy, metas, r.sentAt(metas))
			r.must(err, "legacy fill")
			r.model.add(r.smtp[1].ID, metas)
			return
		}
		r.log = append(r.log, "convert the legacy fill")
		r.must(f.st.FinishFill(f.ctx, r.legacyF.ID, store.FillOK, ""), "finish legacy fill")
		r.must(f.st.SetFillRecurring(f.ctx, r.legacyF.ID, nil, nil, seal), "recurring")
		lf, err := f.st.GetFill(f.ctx, r.legacyF.ID)
		r.must(err, "legacy fill")
		_, err = f.st.ConvertLegacyFill(f.ctx, lf)
		r.must(err, "convert")
		r.legacy = nil // it adds nothing new to the account: the model stays
	case n < 63: // a deletion run
		run, err := f.st.ProposeDeletion(f.ctx, nil)
		r.must(err, "propose")
		if run.Candidates == 0 {
			r.must(f.st.CancelDeletionRun(f.ctx, run.ID), "cancel")
			return
		}
		r.must(f.st.ConfirmDeletionRun(f.ctx, run.ID, nil), "confirm")
		shas, err := f.st.PendingCandidates(f.ctx, run.ID, "", 1000)
		r.must(err, "candidates")
		r.log = append(r.log, fmt.Sprintf("deletion run of %d messages", len(shas)))
		deleted, _, err := f.st.DeleteBatch(f.ctx, run.ID, shas, 0)
		r.must(err, "delete")
		if deleted != len(shas) {
			r.fatalf("deleted %d of %d", deleted, len(shas))
		}
		r.must(f.st.FinishDeletionRun(f.ctx, run.ID), "finish run")
		r.model.remove(shas)
		r.deleted = true
	case n < 65: // a purge, once
		fo := r.imports[2+r.rnd.IntN(2)]
		if r.purged || !r.live(fo) {
			return
		}
		r.purged = true
		r.log = append(r.log, fmt.Sprintf("purge account %d", fo.AccountID))
		r.must(f.st.RemoveRetentionRule(f.ctx, r.rules[fo.AccountID]), "rule")
		acc, err := f.st.GetAccount(f.ctx, fo.AccountID)
		r.must(err, "account")
		removed, err := f.st.DeleteOrRemoveAccount(f.ctx, acc.Ref())
		r.must(err, "remove")
		if removed == store.AccountDeleted { // it held nothing
			r.model.purge(fo.AccountID)
			break
		}
		p, err := f.st.PurgePreview(f.ctx, fo.AccountID)
		r.must(err, "preview")
		acc, err = f.st.GetAccount(f.ctx, fo.AccountID)
		r.must(err, "account")
		purge, err := f.st.ConfirmPurge(f.ctx, acc.Ref(), p.Deleted, nil)
		r.must(err, "confirm purge")
		r.must(f.st.CheckPurgeCount(f.ctx, purge.ID), "count")
		for {
			res, err := f.st.PurgeBatch(f.ctx, purge.ID, 100, 0)
			r.must(err, "purge batch")
			if res.Done {
				break
			}
		}
		_, err = f.st.FinishPurge(f.ctx, purge.ID)
		r.must(err, "finish purge")
		r.model.purge(fo.AccountID)
		r.deleted = true
	case n < 72: // a user moves to another group
		u := r.users[r.rnd.IntN(len(r.users))]
		to := r.groups[r.rnd.IntN(len(r.groups))]
		from := r.model.group[u]
		if from == to {
			return
		}
		r.log = append(r.log, fmt.Sprintf("user %d to group %d", u, to))
		_, err := f.st.SetUserGroup(f.ctx, u, to)
		r.must(err, "set group")
		r.model.group[u] = to
		r.changed[from] = true
		r.markGained(to)
	case n < 79: // an account gets another owner
		var accounts []int64
		for a := range r.model.owner {
			accounts = append(accounts, a)
		}
		slices.Sort(accounts)
		a := accounts[r.rnd.IntN(len(accounts))]
		u := r.users[r.rnd.IntN(len(r.users))]
		if o := r.model.owner[a]; o != nil && *o == u {
			return
		}
		r.log = append(r.log, fmt.Sprintf("account %d to user %d", a, u))
		acc, err := f.st.GetAccount(f.ctx, a)
		r.must(err, "account")
		r.must(f.st.SetAccountOwner(f.ctx, acc.Ref(), u, true), "set owner")
		if g := r.model.groupOf(a); g != nil {
			r.changed[*g] = true
		}
		r.model.owner[a] = &u
		r.markGained(r.model.group[u])
	case n < 88:
		r.log = append(r.log, "fold")
		_, err := f.st.FoldStorage(f.ctx)
		r.must(err, "fold")
	default:
		r.refresh()
		return
	}
	r.checkBetween()
}

// TestUsageModel is the model-based property test of storage usage (#397,
// part 1): random sequences of sync and import batches, SMTP receives,
// recurring fills, journal imports, a fill from before #112 and its
// conversion, deletion runs, a purge, user and account moves, folds and
// snapshots, over 3 groups, 6 accounts and accounts without an owner
// (one of them adopted by the first user), checked against usageModel
// after every step. Sequential, so the race of the upper bound (a
// concurrent deletion) does not occur. Fixed seeds; a failure prints the
// seed and the sequence.
func TestUsageModel(t *testing.T) {
	for seed := uint64(1); seed <= 6; seed++ {
		t.Run(fmt.Sprintf("seed %d", seed), func(t *testing.T) {
			r := &modelRun{
				usageFixture: newUsageFixture(t),
				rnd:          rand.New(rand.NewPCG(seed, 397)), //nolint:gosec // a fixed test sequence
				model: &usageModel{
					size: map[string]int64{}, held: map[int64]map[string]bool{}, owner: map[int64]*int64{},
					group: map[int64]int64{}, retired: map[string]bool{},
				},
				gained: map[int64]bool{}, changed: map[int64]bool{},
			}
			r.setup()
			for range 60 {
				r.step()
			}
			r.refresh()
			ops := map[string]int{}
			for _, l := range r.log {
				ops[strings.SplitN(l, " ", 2)[0]]++
			}
			t.Logf("operations: %v", ops)
		})
	}
}
