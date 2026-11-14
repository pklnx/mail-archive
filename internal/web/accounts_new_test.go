package web

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/keepia/archive/internal/auth"
	"github.com/keepia/archive/internal/imaptest"
	"github.com/keepia/archive/internal/store"
)

// TestLastRunNewIgnoresOtherGroups is the regression test for per-account new counts:
// the "new" of a user's sync, as GET /api/accounts shows it, is the same
// whether or not a user in another customer group archived the same bytes
// first. On main the shared invoice did not count, as the archive held it.
func TestLastRunNewIgnoresOtherGroups(t *testing.T) {
	for _, otherFirst := range []bool{false, true} {
		kimBox := imapmemserver.NewUser("kim", "kim-pw")
		imaptest.CreateMailboxes(t, kimBox, "INBOX")
		imaptest.Append(t, kimBox, "INBOX", []byte(msgInvoice))
		f := newManageFixture(t, testUser(t), kimBox)

		if otherFirst {
			ctx := context.Background()
			groupB, err := f.st.CreateGroup(ctx, "B")
			if err != nil {
				t.Fatal(err)
			}
			kim, err := f.st.CreateUserInGroup(ctx, store.NewUser{Name: "kim", PasswordHash: "unused", GroupID: groupB.ID})
			if err != nil {
				t.Fatal(err)
			}
			token, hash, err := auth.NewSessionToken()
			if err != nil {
				t.Fatal(err)
			}
			if err := f.st.CreateSession(ctx, hash, kim.ID, time.Now().Add(time.Hour), ""); err != nil {
				t.Fatal(err)
			}
			g := *f
			g.cookie = token
			g.do("POST", "/api/accounts", withConfirm(g.newAccount("theirs", "kim", "kim-pw")), http.StatusCreated)
			if run := g.waitIdle("theirs").Sync.LastRun; run.Status != "ok" || run.Fetched != 1 || run.New != 1 {
				t.Fatalf("kim's sync: %+v", run)
			}
		}

		// The fixture's user is in the default group.
		f.do("POST", "/api/accounts", withConfirm(f.newAccount("private", "alice", "secret")), http.StatusCreated)
		if run := f.waitIdle("private").Sync.LastRun; run.Status != "ok" || run.Fetched != 2 || run.New != 2 {
			t.Errorf("other group first %v: lastRun %+v, want fetched 2, new 2", otherFirst, run)
		}
	}
}
