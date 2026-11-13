package smtpd

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/pklnx/mail-archive/internal/store"
)

// The same work for a message new to the customer group (#539, part 3): a
// delivery of bytes that another group already archived is parsed, and the
// parsed fields go to every recipient account's save; when every
// recipient's group holds the message, nothing is parsed (residual).
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
	}
}
