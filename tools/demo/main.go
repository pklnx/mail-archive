// Command demo runs the web UI with made-up mail, for screenshots and for
// trying the UI without real accounts.
//
// It starts an in-memory IMAP server with fictitious messages, creates a
// temporary database next to the one in -db (the user needs CREATEDB),
// adds accounts, syncs them like `mail-archive sync` would, and serves the
// UI. On exit (Ctrl-C) the database and the data directory are deleted.
// Nothing touches the database named in -db itself.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/web"
)

// The demo login, also used by docs/scripts/screenshots.mjs.
const (
	loginName     = "demo"
	loginPassword = "demo-password" //nolint:gosec // public demo login
)

func main() {
	dbURL := flag.String("db", os.Getenv("MAIL_ARCHIVE_DATABASE_URL"), "PostgreSQL URL of a user with CREATEDB")
	listen := flag.String("listen", "127.0.0.1:18080", "address of the web UI")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := run(*dbURL, *listen, log); err != nil {
		fmt.Fprintln(os.Stderr, "demo:", err)
		os.Exit(1)
	}
}

func run(dbURL, listen string, log *slog.Logger) error {
	if dbURL == "" {
		return errors.New("set -db or MAIL_ARCHIVE_DATABASE_URL")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	demoURL, dropDB, err := createDatabase(ctx, dbURL)
	if err != nil {
		return err
	}
	defer dropDB()
	dataDir, err := os.MkdirTemp("", "mail-archive-demo-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dataDir) }()

	st, err := store.Open(ctx, demoURL)
	if err != nil {
		return err
	}
	defer st.Close()
	if _, err := st.Migrate(ctx); err != nil {
		return err
	}
	blobs, err := blobstore.New(dataDir)
	if err != nil {
		return err
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	sealer, err := crypto.NewSealer(key)
	if err != nil {
		return err
	}

	imapAddr, err := startIMAP(time.Now())
	if err != nil {
		return err
	}
	host, port, _ := net.SplitHostPort(imapAddr)
	var portNum int
	_, _ = fmt.Sscan(port, &portNum)

	syncer := &archive.Syncer{Store: st, Blobs: blobs, Sealer: sealer, Logger: log}
	for _, u := range users {
		enc, err := sealer.Seal([]byte(u.password), archive.PasswordContext(u.account))
		if err != nil {
			return err
		}
		a := &store.Account{
			Name: u.account, Host: host, Port: portNum, TLSMode: store.TLSModeNone,
			Username: u.login, PasswordEnc: enc, ExcludedFolders: u.excluded, Enabled: true,
		}
		if err := st.CreateAccount(ctx, a); err != nil {
			return err
		}
		if res := syncer.SyncAccount(ctx, a); res.Err != nil {
			return fmt.Errorf("sync %s: %w", u.account, res.Err)
		}
		if u.removed {
			if _, err := st.DeleteOrRemoveAccount(ctx, a.ID); err != nil {
				return err
			}
		}
	}

	hash, err := auth.NewHasher(auth.DefaultParams).Hash(ctx, loginPassword)
	if err != nil {
		return err
	}
	if _, err := st.CreateUser(ctx, loginName, hash, true); err != nil {
		return err
	}

	runner := &archive.Runner{Syncer: syncer, Interval: 6 * time.Hour}
	go runner.Run(ctx)
	srv := web.New(st, blobs, log, web.Options{Syncer: syncer, Runner: runner})
	fmt.Printf("demo UI on http://%s, log in as %q with password %q (Ctrl-C to stop and delete the demo data)\n", listen, loginName, loginPassword)
	if err := srv.ListenAndServe(ctx, listen); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// createDatabase creates a uniquely named database on the server of base
// and returns its URL and a function that drops it.
func createDatabase(ctx context.Context, base string) (string, func(), error) {
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		return "", nil, err
	}
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	name := "mailarchive_demo_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		_ = admin.Close(ctx)
		return "", nil, fmt.Errorf("create demo database (the user needs CREATEDB): %w", err)
	}
	u, err := url.Parse(base)
	if err != nil {
		_ = admin.Close(ctx)
		return "", nil, err
	}
	u.Path = "/" + name
	drop := func() {
		bg := context.Background()
		_, _ = admin.Exec(bg, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		_ = admin.Close(bg)
	}
	return u.String(), drop, nil
}

type demoUser struct {
	account, login, password string
	mailboxes                []string
	excluded                 []string
	removed                  bool
}

var users = []demoUser{
	{
		account: "personal", login: "alex@example.com", password: "demo",
		mailboxes: []string{"INBOX", "Archive", "Sent", "Spam"},
		excluded:  []string{"Spam"},
	},
	{
		account: "work", login: "alex.morgan@work.example", password: "demo",
		mailboxes: []string{"INBOX", "Sent"},
	},
	{
		account: "old-provider", login: "alex@oldmail.example", password: "demo",
		mailboxes: []string{"INBOX"},
		removed:   true,
	},
}

type message struct {
	account, mailbox string
	raw              string
}

// startIMAP serves the demo users on a random local port.
func startIMAP(now time.Time) (string, error) {
	mem := imapmemserver.New()
	byAccount := map[string]*imapmemserver.User{}
	for _, u := range users {
		user := imapmemserver.NewUser(u.login, u.password)
		for _, name := range u.mailboxes {
			if err := user.Create(name, nil); err != nil {
				return "", err
			}
		}
		mem.AddUser(user)
		byAccount[u.account] = user
	}
	for _, m := range messages(now) {
		if _, err := byAccount[m.account].Append(m.mailbox, bytes.NewReader([]byte(m.raw)), &imap.AppendOptions{Time: now}); err != nil {
			return "", fmt.Errorf("append to %s/%s: %w", m.account, m.mailbox, err)
		}
	}
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}},
		InsecureAuth: true,
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	go func() { _ = srv.Serve(ln) }()
	return ln.Addr().String(), nil
}

func crlf(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }

func plain(from, to, subject string, date time.Time, body string) string {
	return crlf(fmt.Sprintf("From: %s\nTo: %s\nSubject: %s\nDate: %s\nContent-Type: text/plain; charset=utf-8\n\n%s\n",
		from, to, subject, date.Format(time.RFC1123Z), body))
}

// A 16x16 blue square as an inline image.
const logoPNG = "iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAIAAACQkWg2AAAAFklEQVR4nGNQTX5NEmIY1TCqYfhqAAB2SXMQ7mVTQgAAAABJRU5ErkJggg=="

func messages(now time.Time) []message {
	ago := func(days, hours int) time.Time {
		return now.AddDate(0, 0, -days).Add(-time.Duration(hours) * time.Hour)
	}
	alex := "Alex Morgan <alex@example.com>"
	msgs := []message{
		{"personal", "INBOX", plain("City Utilities <billing@utilities.example>", alex, "Your invoice for October 2026", ago(0, 1),
			"Hello Alex,\n\nyour invoice for October is ready. The amount of $84.20 will be charged on October 15.\n\nKind regards\nCity Utilities")},
		{"personal", "INBOX", crlf(`From: Sam Taylor <sam@example.org>
To: ` + alex + `
Subject: Photos from the weekend and the lease
Date: ` + ago(0, 3).Format(time.RFC1123Z) + `
Content-Type: multipart/mixed; boundary="outer"

--outer
Content-Type: multipart/related; boundary="inner"

--inner
Content-Type: text/html; charset=utf-8

<html><body style="font-family:Arial,sans-serif;color:#222"><h2>Hi Alex,</h2><p>as promised, here is our new logo <img src="cid:logo@demo" width="16" height="16"> and the photos from the weekend.</p><p>I attached the signed lease as well.</p><p>Best,<br>Sam</p><p><img src="https://tracker.example/pixel.gif" width="1" height="1"></p></body></html>
--inner
Content-Type: image/png
Content-ID: <logo@demo>
Content-Disposition: inline
Content-Transfer-Encoding: base64

` + logoPNG + `
--inner--
--outer
Content-Type: application/pdf; name="Lease.pdf"
Content-Disposition: attachment; filename="Lease.pdf"
Content-Transfer-Encoding: base64

` + base64.StdEncoding.EncodeToString([]byte("%PDF-1.4\n% demo\n")) + `
--outer
Content-Type: image/png; name="Weekend.png"
Content-Disposition: attachment; filename="Weekend.png"
Content-Transfer-Encoding: base64

` + logoPNG + `
--outer--
`)},
		{"personal", "INBOX", plain("Parcel Service <noreply@parcels.example>", alex, "Your parcel arrives today", ago(1, 2),
			"Your parcel 00340434 will be delivered today between 10 am and 2 pm.")},
		{"personal", "Sent", plain(alex, "Dr. Jordan Lee <office@taxadvisor.example>", "Re: Tax appointment", ago(2, 0),
			"Hello Dr. Lee,\n\nOctober 14 at 10 am works for me.\n\nBest regards\nAlex Morgan")},
		{"personal", "Archive", plain("Home Insurance <service@insurance.example>", alex, "Your 2026 premium invoice", ago(30, 0),
			"Your premium invoice for 2026 is available in your customer portal.")},
		{"personal", "Spam", plain("Prize Office <win@prizes.example>", alex, "You have won!!!", ago(1, 5),
			"Click here to claim your prize.")},
		{"work", "INBOX", plain("Code Review Bot <noreply@ci.example>", "alex.morgan@work.example", "[web-shop] Pull request #42 merged", ago(0, 8),
			"Add search to the order history (#42) was merged into main.")},
		{"work", "INBOX", plain("Hosting Provider <billing@hosting.example>", "alex.morgan@work.example", "Invoice 2026-10", ago(3, 0),
			"Your invoice of $4.51 for server web-1 is available.")},
		{"work", "Sent", plain("Alex Morgan <alex.morgan@work.example>", "Team <team@work.example>", "Holiday photos", ago(5, 0),
			"Here are the photos from Italy!")},
		{"old-provider", "INBOX", plain("Welcome Team <welcome@oldmail.example>", "alex@oldmail.example", "Welcome to Oldmail", ago(900, 0),
			"Thanks for signing up. Your mailbox is ready.")},
	}
	for i := range 25 {
		msgs = append(msgs, message{"work", "INBOX", plain("Newsletter <news@shop.example>", "alex.morgan@work.example",
			fmt.Sprintf("Deals of week %d", 40-i), ago(6+i, 0), fmt.Sprintf("The best deals of week %d.", 40-i))})
	}
	return msgs
}
