package store_test

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

func createSMTPAccount(t *testing.T, st *store.Store, owner *int64, name string, addrs ...string) *store.Account {
	t.Helper()
	a := &store.Account{Name: name, Enabled: true, OwnerID: owner}
	if err := st.CreateSMTPAccount(context.Background(), a, addrs); err != nil {
		t.Fatal(err)
	}
	return a
}

// present stands in for the message file check of ReceiveMessage.
func present() bool { return true }

func receivedMeta(sha byte) store.MessageMeta {
	h := strings.Repeat(fmt.Sprintf("%02x", sha), 32)
	return store.MessageMeta{SHA256: h, Size: 10, StoredPath: h[:2] + "/" + h}
}

func envelope(rcpts ...string) store.Envelope {
	return store.Envelope{
		MailFrom: "sender@example.com", RcptTo: rcpts, ReceivedAt: time.Now(),
		ClientIP: netip.MustParseAddr("192.0.2.1"), HELO: "mx.example.com",
	}
}

type accountEnvelope struct {
	Account string
	store.Envelope
}

// envelopesOf returns the envelopes of a message's locations in the
// owner's accounts.
func envelopesOf(st *store.Store, owner int64, sha256 string) ([]accountEnvelope, error) {
	d, err := st.GetMessageDetail(context.Background(), owner, sha256)
	if err != nil {
		return nil, err
	}
	var out []accountEnvelope
	for _, l := range d.Locations {
		if l.Envelope != nil {
			out = append(out, accountEnvelope{Account: l.Account, Envelope: *l.Envelope})
		}
	}
	return out, nil
}

func TestSMTPAccounts(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()

	a := createSMTPAccount(t, st, nil, "journal", "Archive@Example.COM", "archive@example.com", "journal@example.com")
	got, err := st.GetAccount(ctx, a.ID)
	if err != nil || got.Kind != store.KindSMTP || !got.Enabled || got.Port != 0 {
		t.Fatalf("smtp account: %+v, %v", got, err)
	}
	addrs, err := st.SMTPAddresses(ctx)
	if err != nil || !slices.Equal(addrs[a.ID], []string{"archive@example.com", "journal@example.com"}) {
		t.Fatalf("addresses %v, %v", addrs, err)
	}
	route, err := st.LookupSMTPAddress(ctx, "archive@example.com")
	if err != nil || route.AccountID != a.ID || !route.Enabled || route.Removed {
		t.Fatalf("route %+v, %v", route, err)
	}
	if _, err := st.LookupSMTPAddress(ctx, "other@example.com"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown address: %v", err)
	}

	// An address belongs to one account.
	if err := st.CreateSMTPAccount(ctx, &store.Account{Name: "second"}, []string{"journal@example.com"}); !errors.Is(err, store.ErrAddressTaken) {
		t.Errorf("address in use: %v", err)
	}
	for _, bad := range [][]string{nil, {"archive+x@example.com"}, {"no-at"}, {"a@b@c"}, {"@example.com"}, {"a b@example.com"}} {
		if err := st.CreateSMTPAccount(ctx, &store.Account{Name: "bad"}, bad); err == nil {
			t.Errorf("accepted addresses %q", bad)
		}
	}

	// Enabling is allowed; a server, folders or a password are not.
	a, _ = st.GetAccount(ctx, a.ID)
	off := false
	if err := st.UpdateAccount(ctx, a.Ref(), store.AccountChange{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	a, _ = st.GetAccount(ctx, a.ID)
	for _, c := range []store.AccountChange{
		{Folders: &store.FolderFilters{Excluded: []string{"x"}}},
		{PasswordEnc: []byte{1}},
		{Connection: &store.Connection{Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u"}},
	} {
		if err := st.UpdateAccount(ctx, a.Ref(), c); !errors.Is(err, store.ErrNoServer) {
			t.Errorf("change %+v: %v", c, err)
		}
	}
	if _, err := st.ReceiveMessage(ctx, a.ID, receivedMeta(1), envelope("x@example.com"), present); !errors.Is(err, store.ErrNotReceiving) {
		t.Errorf("receive while disabled: %v", err)
	}

	// Addresses change; the last one stays.
	if err := st.ChangeSMTPAddresses(ctx, a.Ref(), []string{"new@example.com"}, []string{"journal@example.com"}); err != nil {
		t.Fatal(err)
	}
	a, _ = st.GetAccount(ctx, a.ID)
	if err := st.ChangeSMTPAddresses(ctx, a.Ref(), nil, []string{"new@example.com", "archive@example.com"}); err == nil {
		t.Error("removed the last address")
	}
	if err := st.ChangeSMTPAddresses(ctx, a.Ref(), nil, []string{"gone@example.com"}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("remove unknown address: %v", err)
	}
	addrs, _ = st.SMTPAddresses(ctx)
	if !slices.Equal(addrs[a.ID], []string{"archive@example.com", "new@example.com"}) {
		t.Errorf("addresses after change %v", addrs[a.ID])
	}

	// The database refuses what the Go checks refuse.
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	for _, sql := range []string{
		`UPDATE accounts SET port = 25 WHERE kind = 'smtp'`,
		`INSERT INTO smtp_addresses (address, account_id) SELECT 'Upper@example.com', id FROM accounts LIMIT 1`,
		`INSERT INTO smtp_addresses (address, account_id) SELECT 'a+b@example.com', id FROM accounts LIMIT 1`,
		`UPDATE envelopes SET rcpt_text = 'x'`,
	} {
		if _, err := conn.Exec(ctx, sql); err == nil {
			// The envelope statement matches no row until something was received.
			if !strings.Contains(sql, "envelopes") {
				t.Errorf("database accepted %s", sql)
			}
		}
	}

	// Removing an account with mail keeps the mail and frees the addresses.
	on := true
	if err := st.UpdateAccount(ctx, a.Ref(), store.AccountChange{Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReceiveMessage(ctx, a.ID, receivedMeta(2), envelope("x@example.com"), present); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `UPDATE envelopes SET rcpt_text = 'x'`); err == nil {
		t.Error("database accepted an rcpt_text that differs from rcpt_to")
	}
	a, _ = st.GetAccount(ctx, a.ID)
	if res, err := st.DeleteOrRemoveAccount(ctx, a.Ref()); err != nil || res != store.AccountRemoved {
		t.Fatalf("remove: %v, %v", res, err)
	}
	if _, err := st.ReceiveMessage(ctx, a.ID, receivedMeta(3), envelope("x@example.com"), present); !errors.Is(err, store.ErrNotReceiving) {
		t.Errorf("receive after removal: %v", err)
	}
	if _, err := st.LookupSMTPAddress(ctx, "archive@example.com"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("address of a removed account: %v", err)
	}
	createSMTPAccount(t, st, nil, "journal 2", "archive@example.com")

	// An account without mail is deleted with its folder and addresses.
	empty := createSMTPAccount(t, st, nil, "empty", "empty@example.com")
	if res, err := st.DeleteOrRemoveAccount(ctx, empty.Ref()); err != nil || res != store.AccountDeleted {
		t.Fatalf("delete: %v, %v", res, err)
	}
	createSMTPAccount(t, st, nil, "empty", "empty@example.com")
}

func TestReceiveMessageMergesEnvelopes(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, "owner", "h", true)
	if err != nil {
		t.Fatal(err)
	}
	a := createSMTPAccount(t, st, &u.ID, "journal", "archive@example.com")
	b := createSMTPAccount(t, st, &u.ID, "other", "other@example.com")
	meta := receivedMeta(7)

	first, err := st.ReceiveMessage(ctx, a.ID, meta, envelope("alice@example.com", "bob@example.com", "Alice@example.com"), present)
	if err != nil {
		t.Fatal(err)
	}
	if !first.New || !first.NewMessage || first.UID != 1 || first.AddedRecipients != 2 {
		t.Fatalf("first delivery %+v", first)
	}
	// Postfix split the recipients, or retried some: same bytes, more recipients.
	second, err := st.ReceiveMessage(ctx, a.ID, meta, envelope("bob@example.com", "carol@example.com"), present)
	if err != nil {
		t.Fatal(err)
	}
	if second.New || second.AddedRecipients != 1 || second.UID != 1 {
		t.Fatalf("second delivery %+v", second)
	}
	// A plain retry changes nothing.
	third, err := st.ReceiveMessage(ctx, a.ID, meta, envelope("carol@example.com"), present)
	if err != nil || third.New || third.AddedRecipients != 0 {
		t.Fatalf("retry %+v, %v", third, err)
	}
	// Another account gets its own location of the same message.
	other, err := st.ReceiveMessage(ctx, b.ID, meta, envelope("dave@example.com"), present)
	if err != nil || !other.New || other.NewMessage || other.UID != 1 {
		t.Fatalf("other account %+v, %v", other, err)
	}

	envs, err := envelopesOf(st, *a.OwnerID, meta.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if len(envs) != 2 {
		t.Fatalf("envelopes %+v", envs)
	}
	got := map[string][]string{}
	for _, e := range envs {
		got[e.Account] = e.RcptTo
		if e.MailFrom != "sender@example.com" || e.HELO != "mx.example.com" || e.ClientIP.String() != "192.0.2.1" {
			t.Errorf("envelope %+v", e)
		}
	}
	if !slices.Equal(got["journal"], []string{"alice@example.com", "bob@example.com", "carol@example.com"}) {
		t.Errorf("merged recipients %v", got["journal"])
	}
	if !slices.Equal(got["other"], []string{"dave@example.com"}) {
		t.Errorf("other recipients %v", got["other"])
	}
	last, err := st.LastReceived(ctx)
	if err != nil || last[a.ID].IsZero() || last[b.ID].IsZero() {
		t.Errorf("last received %v, %v", last, err)
	}
}

func TestReceiveMessageConcurrent(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, "owner", "h", true)
	if err != nil {
		t.Fatal(err)
	}
	a := createSMTPAccount(t, st, &u.ID, "journal", "archive@example.com")

	// Different messages get distinct UIDs; the same message, delivered by
	// several connections at once, gets one location with every recipient.
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := range 20 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := st.ReceiveMessage(ctx, a.ID, receivedMeta(byte(100+i)), envelope("x@example.com"), present)
			errs <- err
		}()
		go func() {
			defer wg.Done()
			_, err := st.ReceiveMessage(ctx, a.ID, receivedMeta(1), envelope(fmt.Sprintf("r%02d@example.com", i)), present)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	res, err := st.ReceiveMessage(ctx, a.ID, receivedMeta(250), envelope("x@example.com"), present)
	if err != nil || res.UID != 22 {
		t.Fatalf("next UID %+v, %v", res, err)
	}
	envs, err := envelopesOf(st, *a.OwnerID, receivedMeta(1).SHA256)
	if err != nil || len(envs) != 1 || len(envs[0].RcptTo) != 20 {
		t.Fatalf("shared message envelopes %+v, %v", envs, err)
	}
}

func TestSMTPMigrationDownRefused(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	a := createSMTPAccount(t, st, nil, "journal", "archive@example.com")
	// First the audit log, which is empty here.
	if m, err := st.MigrateDown(ctx); err != nil || !strings.Contains(m, "00018_audit_log") {
		t.Fatalf("down the audit log: %q, %v", m, err)
	}
	if _, err := st.MigrateDown(ctx); err == nil || !strings.Contains(err.Error(), "smtp accounts exist") {
		t.Fatalf("down with an smtp account: %v", err)
	}
	if res, err := st.DeleteOrRemoveAccount(ctx, a.Ref()); err != nil || res != store.AccountDeleted {
		t.Fatalf("delete: %v, %v", res, err)
	}
	if _, err := st.MigrateDown(ctx); err != nil {
		t.Fatalf("down without smtp accounts: %v", err)
	}
}

func TestSearchEnvelopeRecipients(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	alice, err := st.CreateUser(ctx, "alice", "h", true)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := st.CreateUser(ctx, "bob", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	journal := createSMTPAccount(t, st, &alice.ID, "journal", "archive@example.com")
	meta := receivedMeta(9)
	meta.To = "visible@example.com"
	if _, err := st.ReceiveMessage(ctx, journal.ID, meta, envelope("hidden-bcc@example.com"), present); err != nil {
		t.Fatal(err)
	}
	// Bob has the same message from an import, without the envelope.
	imp := &store.Account{Kind: store.KindImport, Name: "old", OwnerID: &bob.ID}
	if err := st.CreateAccount(ctx, imp); err != nil {
		t.Fatal(err)
	}
	folder, err := st.GetOrCreateFolder(ctx, imp.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveBatch(ctx, folder.ID, 1, []store.MessageMeta{meta}, []store.Location{{FolderID: folder.ID, UIDValidity: 1, UID: 1}}); err != nil {
		t.Fatal(err)
	}

	// Both ways of resolving to: give the same answers.
	for _, few := range []bool{true, false} {
		if !few {
			defer store.SetMaxEnvelopeMatches(0)()
		}
		for _, c := range []struct {
			owner int64
			to    string
			want  int
		}{
			{alice.ID, "hidden-bcc", 1},
			{alice.ID, "visible", 1},
			{alice.ID, "nobody", 0},
			{bob.ID, "visible", 1},
			// Only the owner of the smtp account sees the BCC recipient.
			{bob.ID, "hidden-bcc", 0},
		} {
			got, err := st.SearchMessages(ctx, store.SearchFilter{Owner: c.owner, To: c.to, Limit: 50})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != c.want {
				t.Errorf("few=%v owner %d to:%s: %d results, want %d", few, c.owner, c.to, len(got), c.want)
			}
			threads, err := st.SearchThreads(ctx, store.SearchFilter{Owner: c.owner, To: c.to, Limit: 50})
			if err != nil {
				t.Fatal(err)
			}
			if len(threads) != c.want {
				t.Errorf("few=%v owner %d to:%s: %d threads, want %d", few, c.owner, c.to, len(threads), c.want)
			}
		}
	}
}

func TestReceiveMessageBlobLock(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	a := createSMTPAccount(t, st, nil, "journal", "archive@example.com")

	// verify holds the blob lock exclusive: the delivery waits for it.
	unlock, ok, err := st.TryLockBlobs(ctx)
	if err != nil || !ok {
		t.Fatalf("lock blobs: %v %v", ok, err)
	}
	// Release it before the store closes, also when the test fails early.
	var once sync.Once
	t.Cleanup(func() { once.Do(unlock) })
	done := make(chan error, 1)
	go func() {
		_, err := st.ReceiveMessage(ctx, a.ID, receivedMeta(1), envelope("x@example.com"), present)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("delivered while verify held the lock: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	once.Do(unlock)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("delivery still waits after the lock was released")
	}
	// After the commit, verify gets the lock again.
	unlock, ok, err = st.TryLockBlobs(ctx)
	if err != nil || !ok {
		t.Fatalf("lock after the delivery committed: %v %v", ok, err)
	}
	unlock()
}

func TestReceiveMessageBlobGone(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	a := createSMTPAccount(t, st, nil, "journal", "archive@example.com")
	gone := func() bool { return false }
	if _, err := st.ReceiveMessage(ctx, a.ID, receivedMeta(1), envelope("x@example.com"), gone); !errors.Is(err, store.ErrBlobGone) {
		t.Fatalf("file gone: %v", err)
	}
	// Metadata that relied on a row the database no longer has.
	known := receivedMeta(2)
	known.Known = true
	if _, err := st.ReceiveMessage(ctx, a.ID, known, envelope("x@example.com"), present); !errors.Is(err, store.ErrBlobGone) {
		t.Fatalf("row gone: %v", err)
	}
	// Nothing was stored.
	if _, err := st.ReceiveMessage(ctx, a.ID, receivedMeta(1), envelope("x@example.com"), present); err != nil {
		t.Fatal(err)
	}
	if res, err := st.ReceiveMessage(ctx, a.ID, receivedMeta(3), envelope("x@example.com"), present); err != nil || res.UID != 2 {
		t.Fatalf("next delivery %+v, %v", res, err)
	}
}
