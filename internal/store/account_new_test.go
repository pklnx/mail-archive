package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// newCountTarget is one way of saving a batch into an account of user a:
// save stores metas in the account's target folder and returns the result;
// spare is another folder of the same account.
type newCountTarget struct {
	save  func(t *testing.T, metas []store.MessageMeta) store.SaveResult
	spare *store.Folder
}

// newCountPaths are the save paths whose result feeds "new": SaveBatch
// (sync and import accounts), SaveFillBatch (fill counters) and
// SaveJournalImportBatch (imports into the journal).
var newCountPaths = []struct {
	name   string
	target func(t *testing.T, st *store.Store, owner *store.User, uid *uint32) newCountTarget
}{
	{"SaveBatch", func(t *testing.T, st *store.Store, owner *store.User, uid *uint32) newCountTarget {
		inbox := ownedFolder(t, st, owner, "mail", "INBOX")
		return newCountTarget{
			save: func(t *testing.T, metas []store.MessageMeta) store.SaveResult {
				return saveAt(t, st, inbox, uid, metas)
			},
			spare: ownedFolder(t, st, owner, "mail", "Archive"),
		}
	}},
	{"SaveFillBatch", func(t *testing.T, st *store.Store, owner *store.User, uid *uint32) newCountTarget {
		ctx := context.Background()
		acc := createSMTPAccount(t, st, &owner.ID, "journal", "archive@example.com")
		fill := newFill(acc, "alice@example.com")
		if err := st.CreateFill(ctx, fill, seal); err != nil {
			t.Fatal(err)
		}
		ff, err := st.FillFolder(ctx, fill, "INBOX")
		if err != nil {
			t.Fatal(err)
		}
		return newCountTarget{
			save: func(t *testing.T, metas []store.MessageMeta) store.SaveResult {
				t.Helper()
				before := ff.Counts.New
				res, err := st.SaveFillBatch(ctx, ff, metas, make([]store.Location, len(metas)))
				if err != nil {
					t.Fatal(err)
				}
				if ff.Counts.New-before != res.Added {
					t.Errorf("fill counter new grew by %d, result %d", ff.Counts.New-before, res.Added)
				}
				return res
			},
			spare: ownedFolder(t, st, owner, "journal", "Old"),
		}
	}},
	{"SaveJournalImportBatch", func(t *testing.T, st *store.Store, owner *store.User, uid *uint32) newCountTarget {
		ctx := context.Background()
		acc := createSMTPAccount(t, st, &owner.ID, "journal", "archive@example.com")
		folderID, err := st.JournalFolderID(ctx, acc.ID)
		if err != nil {
			t.Fatal(err)
		}
		imp := &store.SMTPImport{AccountID: acc.ID, Label: "piler", Format: "eml", Source: "/import/piler"}
		if err := st.CreateSMTPImport(ctx, imp); err != nil {
			t.Fatal(err)
		}
		return newCountTarget{
			save: func(t *testing.T, metas []store.MessageMeta) store.SaveResult {
				t.Helper()
				res, _, err := st.SaveJournalImportBatch(ctx, folderID, imp.ID, metas, make([]store.Location, len(metas)))
				if err != nil {
					t.Fatal(err)
				}
				return res
			},
			spare: ownedFolder(t, st, owner, "journal", "Old"),
		}
	}},
}

// ownedFolder returns the folder name of owner's account (created as an
// import account if missing) at UIDVALIDITY 1.
func ownedFolder(t *testing.T, st *store.Store, owner *store.User, account, name string) *store.Folder {
	t.Helper()
	ctx := context.Background()
	a, err := st.GetOwnedAccount(ctx, owner.ID, account)
	if err != nil {
		a = &store.Account{Kind: store.KindImport, Name: account, OwnerID: &owner.ID}
		if err := st.CreateAccount(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	fo, err := st.GetOrCreateFolder(ctx, a.ID, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ResetFolder(ctx, fo.ID, 1); err != nil {
		t.Fatal(err)
	}
	fo.UIDValidity = 1
	return fo
}

// saveAt saves metas into fo with SaveBatch, one new UID each.
func saveAt(t *testing.T, st *store.Store, fo *store.Folder, uid *uint32, metas []store.MessageMeta) store.SaveResult {
	t.Helper()
	locs := make([]store.Location, len(metas))
	for i := range locs {
		*uid++
		locs[i] = store.Location{FolderID: fo.ID, UIDValidity: fo.UIDValidity, UID: *uid, InternalDate: time.Now()}
	}
	res, err := st.SaveBatch(context.Background(), fo.ID, *uid, metas, locs)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// TestAddedIsNewToTheAccount is the regression test for per-account new counts: what a
// save reports as new does not depend on whether another account, here
// one in another customer group, archived the same bytes before. It
// counts what the account did not hold in any of its folders, gone
// locations included, and a hash twice in one batch once.
func TestAddedIsNewToTheAccount(t *testing.T) {
	for _, path := range newCountPaths {
		t.Run(path.name, func(t *testing.T) {
			for _, otherFirst := range []bool{false, true} {
				st, url := storetest.NewWithURL(t)
				conn := connect(t, url)
				ctx := context.Background()
				var uid uint32
				alice, err := st.CreateUser(ctx, "alice", "h", false)
				if err != nil {
					t.Fatal(err)
				}
				groupB, err := st.CreateGroup(ctx, "B")
				if err != nil {
					t.Fatal(err)
				}
				kim, err := st.CreateUserInGroup(ctx, store.NewUser{Name: "kim", PasswordHash: "h", GroupID: groupB.ID})
				if err != nil {
					t.Fatal(err)
				}
				x, y, z := receivedMeta(0x10), receivedMeta(0x11), receivedMeta(0x12)
				if otherFirst {
					// kim's account, in group B, holds x, y and z already.
					theirs := ownedFolder(t, st, kim, "kim", "INBOX")
					if res := saveAt(t, st, theirs, &uid, []store.MessageMeta{x, y, z}); res.Added != 3 {
						t.Fatalf("kim: %+v", res)
					}
				}
				target := path.target(t, st, alice, &uid)

				// x twice in one batch counts once; on main, with kim first,
				// this was 1 (y only).
				if res := target.save(t, []store.MessageMeta{x, y, x}); res.Added != 2 {
					t.Errorf("other account first %v: first batch added %d, want 2", otherFirst, res.Added)
				}
				// Held in another folder of the same account, even with the
				// location gone: not new.
				if res := saveAt(t, st, target.spare, &uid, []store.MessageMeta{z}); res.Added != 1 {
					t.Errorf("other account first %v: spare folder added %d, want 1", otherFirst, res.Added)
				}
				if _, err := conn.Exec(ctx, "UPDATE message_locations SET gone_at = now() WHERE folder_id = $1", target.spare.ID); err != nil {
					t.Fatal(err)
				}
				if res := target.save(t, []store.MessageMeta{z}); res.Added != 0 {
					t.Errorf("other account first %v: held in another folder, added %d, want 0", otherFirst, res.Added)
				}
			}
		})
	}
}
