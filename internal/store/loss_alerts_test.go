package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/store"
)

func TestLossAlertDue(t *testing.T) {
	for _, c := range []struct {
		lost, before int
		want         bool
	}{
		{9, 10, false},      // under the minimum
		{10, 100, true},     // exactly 10 %
		{10, 101, false},    // just under 10 %
		{99, 5000, false},   // 2 %
		{100, 100000, true}, // always from 100
		{0, 0, false},
	} {
		if got := store.LossAlertDue(c.lost, c.before); got != c.want {
			t.Errorf("LossAlertDue(%d, %d) = %v, want %v", c.lost, c.before, got, c.want)
		}
	}
}

// Only messages that lost their last present location in the account
// during the run count: not moved ones, renumbered ones, earlier losses,
// baseline folders or other users' copies.
func TestLostMessages(t *testing.T) {
	f := newReconcileFixture(t)
	inbox := f.folder(f.alice, "mail", "INBOX", store.KindIMAP)
	archive := f.folder(f.alice, "mail", "Archive", store.KindIMAP)
	sent := f.folder(f.alice, "mail", "Sent", store.KindIMAP)
	old := f.folder(f.alice, "mail", "Old", store.KindIMAP)
	fresh := f.folder(f.alice, "mail", "New", store.KindIMAP)
	bobInbox := f.folder(f.bob, "mail", "INBOX", store.KindIMAP)
	for uid, c := range []byte("abcdg") {
		f.save(inbox, uint32(uid+1), hashOf(c)) //nolint:gosec // test UIDs
	}
	f.save(sent, 1, hashOf('x'))
	f.save(old, 1, hashOf('f'))
	f.save(old, 2, hashOf('g'))
	f.save(fresh, 1, hashOf('h'))
	f.save(bobInbox, 1, hashOf('b'))
	// Reconciled before: everything there, except d, deleted earlier.
	f.reconcile(inbox, map[uint32]string{1: "", 2: "", 3: "", 5: ""})
	f.reconcile(sent, map[uint32]string{1: ""})
	f.reconcile(old, map[uint32]string{1: "", 2: ""})
	f.reconcile(bobInbox, map[uint32]string{1: ""})

	acc, err := f.st.GetOwnedAccount(f.ctx, f.alice.ID, "mail")
	if err != nil {
		t.Fatal(err)
	}
	run, err := f.st.StartSyncRun(f.ctx, acc.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The run: a moved to Archive, b deleted, Sent renumbered (x still
	// there), Old vanished (g is also in INBOX), and New reconciled for the
	// first time (h deleted long ago). Bob deleted b too.
	f.save(archive, 1, hashOf('a'))
	f.reconcile(inbox, map[uint32]string{3: "", 5: ""})
	if err := f.st.ResetFolder(f.ctx, sent.ID, 2); err != nil {
		t.Fatal(err)
	}
	sent.UIDValidity = 2
	f.save(sent, 1, hashOf('x'))
	f.reconcile(sent, map[uint32]string{1: ""})
	if _, err := f.st.MarkFolderVanished(f.ctx, old.ID); err != nil {
		t.Fatal(err)
	}
	f.reconcile(fresh, nil)
	f.reconcile(bobInbox, nil)

	got, err := f.st.LostMessages(f.ctx, acc.ID, run.ID, []int64{fresh.ID})
	if err != nil {
		t.Fatal(err)
	}
	// b and f; 'a' moved, c and g are present, d was earlier, h is baseline.
	want := store.LossAlert{Lost: 2, PresentBefore: 2, Folders: []store.FolderLoss{{Name: "INBOX", Lost: 1}, {Name: "Old", Lost: 1}}}
	if got.Lost != want.Lost || got.PresentBefore != want.PresentBefore || len(got.Folders) != 2 ||
		got.Folders[0] != want.Folders[0] || got.Folders[1] != want.Folders[1] {
		t.Fatalf("lost = %+v, want %+v", got, want)
	}
	// Without the baseline, h counts as well.
	if got, err := f.st.LostMessages(f.ctx, acc.ID, run.ID, nil); err != nil || got.Lost != 3 {
		t.Fatalf("without baseline: %+v, %v", got, err)
	}
}

// PresentBefore is counted once the loss can alert.
func TestLostMessagesPresentBefore(t *testing.T) {
	f := newReconcileFixture(t)
	inbox := f.folder(f.alice, "mail", "INBOX", store.KindIMAP)
	listed := map[uint32]string{}
	for i := range 30 {
		f.save(inbox, uint32(i+1), hashOf(byte('A'+i))) //nolint:gosec // test UIDs
		listed[uint32(i+1)] = ""                        //nolint:gosec // test UIDs
	}
	f.reconcile(inbox, listed)
	acc, _ := f.st.GetOwnedAccount(f.ctx, f.alice.ID, "mail")
	run, err := f.st.StartSyncRun(f.ctx, acc.ID)
	if err != nil {
		t.Fatal(err)
	}
	for uid := uint32(1); uid <= 12; uid++ {
		delete(listed, uid)
	}
	f.reconcile(inbox, listed)
	got, err := f.st.LostMessages(f.ctx, acc.ID, run.ID, nil)
	if err != nil || got.Lost != 12 || got.PresentBefore != 30 || !store.LossAlertDue(got.Lost, got.PresentBefore) {
		t.Fatalf("lost = %+v, %v", got, err)
	}
}

func TestLossAlertOutbox(t *testing.T) {
	f := newReconcileFixture(t)
	f.folder(f.alice, "mail", "INBOX", store.KindIMAP)
	acc, _ := f.st.GetOwnedAccount(f.ctx, f.alice.ID, "mail")
	conn := rawConn(t, f.url)
	newAlert := func(lost int) *store.SyncRun {
		t.Helper()
		run, err := f.st.StartSyncRun(f.ctx, acc.ID)
		if err != nil {
			t.Fatal(err)
		}
		run.Status, run.Health = "ok", store.HealthSuccess
		run.LossAlert = &store.LossAlert{Lost: lost, PresentBefore: 100, Folders: []store.FolderLoss{{Name: "INBOX", Lost: lost}}}
		if err := f.st.FinishSyncRun(f.ctx, run); err != nil {
			t.Fatal(err)
		}
		return run
	}
	first := newAlert(40)

	// A run whose alert cannot be stored is not stored either.
	bad, err := f.st.StartSyncRun(f.ctx, acc.ID)
	if err != nil {
		t.Fatal(err)
	}
	bad.Status = "ok"
	bad.LossAlert = &store.LossAlert{Lost: 50, PresentBefore: 10} // violates present_before >= lost
	if err := f.st.FinishSyncRun(f.ctx, bad); err == nil {
		t.Fatal("stored an invalid alert")
	}
	if n := countRows(t, conn, "loss_alerts"); n != 1 {
		t.Fatalf("%d alert rows after the failed finish, want 1", n)
	}
	if runs, _ := f.st.LastRuns(f.ctx); runs[acc.ID].FinishedAt != nil {
		t.Fatal("the failed finish recorded the run")
	}

	ctx := context.Background()
	lease := []byte("lease-1")
	got, err := f.st.ClaimLossAlerts(ctx, lease, time.Minute, store.LossAlertMaxAge, 10)
	if err != nil || len(got) != 1 || got[0].SyncRunID != first.ID || got[0].Lost != 40 || got[0].Account != "mail" ||
		got[0].Owner != "alice" || len(got[0].Folders) != 1 || got[0].Folders[0].Name != "INBOX" {
		t.Fatalf("claim = %+v, %v", got, err)
	}
	// Leased: nobody else gets it.
	if again, err := f.st.ClaimLossAlerts(ctx, []byte("lease-2"), time.Minute, store.LossAlertMaxAge, 10); err != nil || len(again) != 0 {
		t.Fatalf("claimed twice: %+v, %v", again, err)
	}
	if err := f.st.DeferLossAlert(ctx, got[0].ID, []byte("other"), time.Now(), "x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("defer with a foreign lease: %v", err)
	}
	if err := f.st.DeferLossAlert(ctx, got[0].ID, lease, time.Now().Add(-time.Second), "HTTP 500"); err != nil {
		t.Fatal(err)
	}
	got, err = f.st.ClaimLossAlerts(ctx, lease, time.Minute, store.LossAlertMaxAge, 10)
	if err != nil || len(got) != 1 || got[0].Attempts != 1 {
		t.Fatalf("claim after defer = %+v, %v", got, err)
	}
	if err := f.st.FinishLossAlert(ctx, got[0].ID, lease, false, ""); err != nil {
		t.Fatal(err)
	}
	if again, _ := f.st.ClaimLossAlerts(ctx, lease, time.Minute, store.LossAlertMaxAge, 10); len(again) != 0 {
		t.Fatalf("sent alert claimed again: %+v", again)
	}

	// Older than a day: never claimed, and the next finished run gives it up.
	old := newAlert(30)
	if _, err := conn.Exec(ctx, "UPDATE loss_alerts SET created_at = now() - interval '25 hours' WHERE sync_run_id = $1", old.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.st.ClaimLossAlerts(ctx, lease, time.Minute, store.LossAlertMaxAge, 10); len(got) != 0 {
		t.Fatalf("old alert claimed: %+v", got)
	}
	newAlert(20)
	var pending int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM loss_alerts WHERE sent_at IS NULL AND given_up_at IS NULL").Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("pending after expiry = %d, %v", pending, err)
	}

	// Removed accounts drop their alerts.
	if _, err := conn.Exec(ctx, "UPDATE accounts SET removed_at = now() WHERE id = $1", acc.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.st.ClaimLossAlerts(ctx, lease, time.Minute, store.LossAlertMaxAge, 10); len(got) != 0 {
		t.Fatalf("alert of a removed account claimed: %+v", got)
	}
}
