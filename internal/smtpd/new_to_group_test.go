package smtpd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/keepia/archive/internal/store"
)

// The same work for a message new to the customer group (#539, part 3): a
// delivery of bytes that another group already archived is parsed, its
// temporary file synchronized, and the parsed fields go to every
// recipient account's save; when every recipient's group holds the
// message, nothing is parsed or synchronized (residual).
func TestReceiveNewToGroup(t *testing.T) {
	type got struct {
		account    int64
		known      bool
		subject    string
		attachment bool
	}
	var (
		mu    sync.Mutex
		metas []got
	)
	f := newFixture(t, func(s *Server) {
		s.receive = func(ctx context.Context, account int64, meta store.MessageMeta, env store.Envelope, fileExists func() bool) (store.ReceiveResult, error) {
			mu.Lock()
			metas = append(metas, got{account, meta.Known, meta.Subject, meta.HasAttachment})
			mu.Unlock()
			return s.Store.ReceiveMessage(ctx, account, meta, env, fileExists)
		}
	})
	ctx := context.Background()
	other := f.account("other", "other@archive.test")
	inGroup := func(name string) *store.Account {
		t.Helper()
		g, err := f.st.CreateGroup(ctx, "group-"+name)
		if err != nil {
			t.Fatal(err)
		}
		u, err := f.st.CreateUserInGroup(ctx, store.NewUser{Name: name, PasswordHash: "h", GroupID: g.ID})
		if err != nil {
			t.Fatal(err)
		}
		a := &store.Account{Name: name, Enabled: true, OwnerID: &u.ID}
		if err := f.st.CreateSMTPAccount(ctx, a, []string{name + "@archive.test"}); err != nil {
			t.Fatal(err)
		}
		return a
	}
	bob, dave := inGroup("bob"), inGroup("dave")
	var synced atomic.Int64 // temporary files synchronized
	f.srv.Blobs.OnTempSync(func(string) { synced.Add(1) })

	const raw = "From: sender@example.com\r\nTo: alice@example.com\r\nSubject: Foerderbescheid\r\n" +
		"Message-ID: <grant@example.com>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n" +
		"--b\r\nContent-Type: text/plain\r\n\r\nDer Bescheid liegt bei.\r\n" +
		"--b\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"Bescheid.pdf\"\r\n\r\nJVBE\r\n--b--\r\n"
	parsed := func(account int64, known bool) got { return got{account, known, "Foerderbescheid", true} }
	unparsed := func(account int64) got { return got{account: account, known: true} }
	for _, step := range []struct {
		name  string
		rcpts []string
		want  []got
	}{
		{"new to the archive", []string{"archive@archive.test"}, []got{parsed(f.journal.ID, false)}},
		{"another account of the group", []string{"other@archive.test"}, []got{unparsed(other.ID)}},
		{"new to group B", []string{"bob@archive.test"}, []got{parsed(bob.ID, true)}},
		{"two groups, one new to it", []string{"archive@archive.test", "dave@archive.test"}, []got{parsed(f.journal.ID, true), parsed(dave.ID, true)}},
		{"two accounts whose groups hold it", []string{"other@archive.test", "bob@archive.test"}, []got{unparsed(other.ID), unparsed(bob.ID)}},
	} {
		if err := f.send("sender@example.com", step.rcpts, raw); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		mu.Lock()
		have := metas
		metas = nil
		mu.Unlock()
		if !slices.Equal(have, step.want) {
			t.Errorf("%s: saves %+v, want %+v", step.name, have, step.want)
		}
		// The temporary file is synchronized exactly when the message is
		// parsed: new to the archive or to a recipient's group.
		if n, want := synced.Swap(0), map[bool]int64{true: 1, false: 0}[step.want[0].subject != ""]; n != want {
			t.Errorf("%s: temporary file synchronized %d times, want %d", step.name, n, want)
		}
	}
}

// TestReceiveMoveDuringDelivery: the journal account moves to bob's group
// B after the delivery's group lookup for its group A (new there) and
// before its save. The save refuses the message (store.ErrGroupChanged):
// 451, nothing stored. The retry looks up for group B, which holds the
// message, and stores it without parsing it.
func TestReceiveMoveDuringDelivery(t *testing.T) {
	type got struct {
		account int64
		subject string
		err     error
	}
	var (
		mu    sync.Mutex
		saves []got
	)
	f := newFixture(t, func(s *Server) {
		s.receive = func(ctx context.Context, account int64, meta store.MessageMeta, env store.Envelope, fileExists func() bool) (store.ReceiveResult, error) {
			res, err := s.Store.ReceiveMessage(ctx, account, meta, env, fileExists)
			mu.Lock()
			saves = append(saves, got{account, meta.Subject, err})
			mu.Unlock()
			return res, err
		}
	})
	ctx := context.Background()
	g, err := f.st.CreateGroup(ctx, "group-bob")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := f.st.CreateUserInGroup(ctx, store.NewUser{Name: "bob", PasswordHash: "h", GroupID: g.ID})
	if err != nil {
		t.Fatal(err)
	}
	bobAccount := &store.Account{Name: "bob", Enabled: true, OwnerID: &bob.ID}
	if err := f.st.CreateSMTPAccount(ctx, bobAccount, []string{"bob@archive.test"}); err != nil {
		t.Fatal(err)
	}
	const raw = "From: sender@example.com\r\nTo: alice@example.com\r\nSubject: Foerderbescheid\r\n" +
		"Message-ID: <moved@example.com>\r\n\r\nDer Bescheid liegt bei.\r\n"
	if err := f.send("sender@example.com", []string{"bob@archive.test"}, raw); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(raw))
	sha := hex.EncodeToString(sum[:])
	mu.Lock()
	saves = nil
	mu.Unlock()

	var once sync.Once
	var moveErr error
	f.srv.Blobs.OnTempSync(func(string) {
		once.Do(func() {
			a, err := f.st.GetAccount(ctx, f.journal.ID)
			if err == nil {
				err = f.st.SetAccountOwner(ctx, a.Ref(), bob.ID, true)
			}
			moveErr = err
		})
	})
	t.Cleanup(func() { f.srv.Blobs.OnTempSync(nil) })
	if err := f.send("sender@example.com", []string{"archive@archive.test"}, raw); code(err) != 451 {
		t.Fatalf("delivery during the move: %v, want 451", err)
	}
	if moveErr != nil {
		t.Fatal(moveErr)
	}
	folder, err := f.st.GetOrCreateFolder(ctx, f.journal.ID, store.SMTPFolder)
	if err != nil {
		t.Fatal(err)
	}
	if there, err := f.st.LocationInFolder(ctx, folder.ID, sha); err != nil || there {
		t.Fatalf("stored after the refusal: %v %v", there, err)
	}
	if err := f.send("sender@example.com", []string{"archive@archive.test"}, raw); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if there, err := f.st.LocationInFolder(ctx, folder.ID, sha); err != nil || !there {
		t.Fatalf("not stored by the retry: %v %v", there, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(saves) != 2 || !errors.Is(saves[0].err, store.ErrGroupChanged) || saves[0].subject != "Foerderbescheid" ||
		saves[1].err != nil || saves[1].subject != "" {
		t.Errorf("saves %+v, want the refused one parsed for group A, then the retry unparsed for group B", saves)
	}
}
