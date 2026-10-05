// Command imap-testserver is an in-memory IMAP server for smoke tests.
//
// It serves user "alice" (password "secret") with three messages in INBOX
// and one in Trash, without TLS, on the address given by -listen.
// Never expose it outside a test environment.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

func main() {
	listen := flag.String("listen", ":1143", "address to listen on")
	flag.Parse()

	user := imapmemserver.NewUser("alice", "secret")
	for _, name := range []string{"INBOX", "Trash"} {
		if err := user.Create(name, nil); err != nil {
			log.Fatal(err)
		}
	}
	add := func(mailbox, id string) {
		raw := fmt.Appendf(nil, "Message-ID: <%s@test>\r\nFrom: test@example.com\r\nSubject: Smoke test %s\r\n"+
			"Date: Mon, 5 Oct 2026 10:00:00 +0000\r\n\r\nBody %s\r\n", id, id, id)
		if _, err := user.Append(mailbox, bytes.NewReader(raw), &imap.AppendOptions{Time: time.Now()}); err != nil {
			log.Fatal(err)
		}
	}
	for _, id := range []string{"1", "2", "3"} {
		add("INBOX", id)
	}
	add("Trash", "deleted")

	mem := imapmemserver.New()
	mem.AddUser(user)
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}},
		InsecureAuth: true,
	})
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("imap-testserver listening on %s", ln.Addr())
	log.Fatal(srv.Serve(ln))
}
