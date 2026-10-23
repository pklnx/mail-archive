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
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
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
	// loginTOTPSecret is the demo admin's 2FA secret (admins need 2FA).
	loginTOTPSecret = "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP" //nolint:gosec // public demo login
)

// Logins for the end-to-end tests in web/e2e (web/e2e/demo.ts has the same
// values). The 2FA user logs in through the login page with a secret of
// its own, so its time steps never clash with the admin's API login.
const (
	twoFactorName     = "kim"
	twoFactorPassword = "kim-password"                     //nolint:gosec // public demo login
	twoFactorSecret   = "KRSXG5BAKRSXG5BAKRSXG5BAKRSXG5BA" //nolint:gosec // public demo login
	lockedName        = "lee"
	lockedPassword    = "lee-password" //nolint:gosec // public demo login
)

func main() {
	dbURL := flag.String("db", os.Getenv("MAIL_ARCHIVE_DATABASE_URL"), "PostgreSQL URL of a user with CREATEDB")
	listen := flag.String("listen", "127.0.0.1:18080", "address of the web UI")
	imapListen := flag.String("imap-listen", "127.0.0.1:0", "address of the demo IMAP server (port 0: a free port)")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := run(*dbURL, *listen, *imapListen, log); err != nil {
		fmt.Fprintln(os.Stderr, "demo:", err)
		os.Exit(1)
	}
}

func run(dbURL, listen, imapListen string, log *slog.Logger) error {
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

	imapAddr, err := startIMAP(imapListen, time.Now())
	if err != nil {
		return err
	}
	host, port, _ := net.SplitHostPort(imapAddr)
	var portNum int
	_, _ = fmt.Sscan(port, &portNum)

	syncer := &archive.Syncer{Store: st, Blobs: blobs, Sealer: sealer, Logger: log, ReconcileInterval: 24 * time.Hour}
	var reconcile []*store.Account
	for _, u := range users {
		if u.spare {
			continue
		}
		a := &store.Account{
			Name: u.account, Host: host, Port: portNum, TLSMode: store.TLSModeNone,
			Username: u.login, ExcludedFolders: u.excluded, Enabled: true,
		}
		seal := func(id int64) ([]byte, error) { return archive.SealPassword(sealer, id, u.password) }
		if err := st.CreateAccountSealed(ctx, a, seal); err != nil {
			return err
		}
		if res := syncer.SyncAccount(ctx, a); res.Err != nil {
			return fmt.Errorf("sync %s: %w", u.account, res.Err)
		}
		if u.removed {
			if _, err := st.DeleteOrRemoveAccount(ctx, a.Ref()); err != nil {
				return err
			}
		} else {
			reconcile = append(reconcile, a)
		}
	}
	// The insurance invoice is deleted on the server after it was archived;
	// the reconcile marks it as only in the archive.
	if err := expunge(imapAddr, "alex@example.com", "demo", "Archive", 1); err != nil {
		return err
	}
	for _, a := range reconcile {
		if res := syncer.SyncAccountWith(ctx, a, archive.SyncOptions{Reconcile: true}); res.Err != nil {
			return fmt.Errorf("reconcile %s: %w", a.Name, res.Err)
		}
	}
	journalAcc, err := journal(ctx, st, blobs, time.Now())
	if err != nil {
		return err
	}
	// The work mailbox's mail from before journaling, copied into the
	// journal account once, as "Import existing mail from IMAP" does.
	filler := &archive.Filler{Store: st, Blobs: blobs, Sealer: sealer, Logger: log}
	cutoff := time.Now()
	fill := &store.Fill{
		AccountID: journalAcc.ID, Host: host, Port: portNum, TLSMode: store.TLSModeNone,
		Username: "alex.morgan@work.example", Cutoff: &cutoff,
	}
	if err := st.CreateFill(ctx, fill, func(id int64) ([]byte, error) { return archive.SealFillPassword(sealer, id, "demo") }); err != nil {
		return err
	}
	if _, err := filler.Run(ctx, fill.ID); err != nil {
		return fmt.Errorf("fill: %w", err)
	}
	// A retention rule for the business account, for the retention page
	// and the message view.
	for _, a := range reconcile {
		if a.Name == "work" {
			if _, err := st.AddRetentionRule(ctx, store.NewRetentionRule{AccountID: a.ID, Years: 8, Note: "§147 AO: business letters and invoices"}); err != nil {
				return err
			}
		}
	}

	hash, err := auth.NewHasher(auth.DefaultParams).Hash(ctx, loginPassword)
	if err != nil {
		return err
	}
	admin, err := st.CreateUser(ctx, loginName, hash, true)
	if err != nil {
		return err
	}
	code, err := auth.GenerateTOTP(loginTOTPSecret, time.Now())
	if err != nil {
		return err
	}
	if err := st.BeginTwoFactorSetup(ctx, admin.ID, loginTOTPSecret, sealer); err != nil {
		return err
	}
	if err := st.ConfirmTwoFactorSetup(ctx, admin.ID, code, time.Now(), sealer, nil, key); err != nil {
		return err
	}
	// A second user for the users page; it has no accounts.
	if _, err := st.CreateUserWithGeneratedPassword(ctx, "sam", hash, false); err != nil {
		return err
	}
	if err := e2eUsers(ctx, st, sealer, key); err != nil {
		return err
	}

	runner := &archive.Runner{Syncer: syncer, Schedule: archive.Schedule{Sync: 6 * time.Hour, SyncMin: 5 * time.Minute}}
	go runner.Run(ctx)
	fills := &archive.FillRunner{Filler: filler}
	go fills.Run(ctx)
	// The retention job, as in serve: it executes deletion runs and account
	// purges confirmed in the UI.
	retention := &archive.Retention{Store: st, Blobs: blobs, Grace: 7 * 24 * time.Hour, Logger: log}
	go retention.Run(ctx)
	// Passkeys need a host name: the demo is meant for localhost.
	_, webPort, err := net.SplitHostPort(listen)
	if err != nil {
		return err
	}
	publicURL := "http://localhost:" + webPort
	srv := web.New(st, blobs, log, web.Options{
		Syncer: syncer, Runner: runner, Fills: fills, Sealer: sealer, SecretKey: key, PublicURL: publicURL,
		// The demo stores its journal messages directly; the UI shows the
		// SMTP server as on, as in a real journaling setup.
		SMTPListen: ":2525",
		Retention:  retention,
	})
	fmt.Printf("demo UI on %s, log in as %q with password %q and a TOTP code for secret %s (Ctrl-C to stop and delete the demo data)\n",
		publicURL, loginName, loginPassword, loginTOTPSecret)
	if err := srv.ListenAndServe(ctx, listen); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// e2eUsers creates the users of the end-to-end tests: one with 2FA and one
// that is locked.
func e2eUsers(ctx context.Context, st *store.Store, sealer *crypto.Sealer, key []byte) error {
	hasher := auth.NewHasher(auth.DefaultParams)
	hash, err := hasher.Hash(ctx, twoFactorPassword)
	if err != nil {
		return err
	}
	kim, err := st.CreateUser(ctx, twoFactorName, hash, false)
	if err != nil {
		return err
	}
	// The setup is confirmed with a code from an hour ago, so every current
	// time step is still unused for the login.
	past := time.Now().Add(-time.Hour)
	code, err := auth.GenerateTOTP(twoFactorSecret, past)
	if err != nil {
		return err
	}
	if err := st.BeginTwoFactorSetup(ctx, kim.ID, twoFactorSecret, sealer); err != nil {
		return err
	}
	if err := st.ConfirmTwoFactorSetup(ctx, kim.ID, code, past, sealer, nil, key); err != nil {
		return err
	}
	if hash, err = hasher.Hash(ctx, lockedPassword); err != nil {
		return err
	}
	lee, err := st.CreateUser(ctx, lockedName, hash, false)
	if err != nil {
		return err
	}
	return st.SetUserLocked(ctx, lee.ID, true)
}

// journal creates an smtp account with copies the mail server delivered,
// one of them with a BCC recipient that only the envelope shows.
func journal(ctx context.Context, st *store.Store, blobs *blobstore.Store, now time.Time) (*store.Account, error) {
	acc := &store.Account{Name: "journal", Enabled: true}
	if err := st.CreateSMTPAccount(ctx, acc, []string{"archive@archive.example.com"}); err != nil {
		return nil, err
	}
	for _, m := range []struct {
		raw   string
		from  string
		rcpts []string
		at    time.Time
	}{
		{
			plain("Alex Morgan <alex@example.com>", "Jordan Lee <jordan@example.com>", "Lease renewal for the office",
				now.Add(-26*time.Hour), "Hi Jordan,\n\nthe landlord sent the renewal. Can we sign by Friday?\n\nAlex"),
			"alex@example.com", []string{"jordan@example.com", "counsel@lawfirm.example"}, now.Add(-26 * time.Hour),
		},
		{
			plain("Billing <billing@printshop.example>", "Alex Morgan <alex@example.com>", "Invoice 2026-114",
				now.Add(-50*time.Hour), "Please find the invoice for the flyers attached."),
			"billing@printshop.example", []string{"alex@example.com"}, now.Add(-50 * time.Hour),
		},
	} {
		blob, created, err := blobs.Put(strings.NewReader(m.raw))
		if err != nil {
			return nil, err
		}
		meta, err := archive.BuildMeta(ctx, st, blobs, blob, created)
		if err != nil {
			return nil, err
		}
		env := store.Envelope{
			MailFrom: m.from, RcptTo: m.rcpts, ReceivedAt: m.at,
			ClientIP: netip.MustParseAddr("172.18.0.1"), HELO: "mx.example.com",
		}
		if _, err := st.ReceiveMessage(ctx, acc.ID, meta, env, func() bool { return blobs.Exists(blob.SHA256) }); err != nil {
			return nil, err
		}
	}
	return acc, nil
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
	// spare is a login on the IMAP server that is not an account; the
	// end-to-end tests add it through the UI.
	spare bool
}

var users = []demoUser{
	{
		account: "personal", login: "alex@example.com", password: "demo",
		// Archive/2014 holds old mail for the retention test in web/e2e,
		// which deletes it; no other test uses it.
		mailboxes: []string{"INBOX", "Archive", "Archive/2014", "Archive/2025", "Sent", "Spam"},
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
	{
		account: "spare", login: "robin@example.net", password: "demo",
		mailboxes: []string{"INBOX", "Receipts", "Trash"},
		spare:     true,
	},
}

type message struct {
	account, mailbox string
	raw              string
}

// startIMAP serves the demo users on addr.
func startIMAP(addr string, now time.Time) (string, error) {
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
	// Mail with its date as the internal date, as after a migration: the
	// retention period of the old mail has ended.
	for _, m := range datedMessages(now) {
		if _, err := byAccount[m.account].Append(m.mailbox, bytes.NewReader([]byte(m.raw)), &imap.AppendOptions{Time: m.at}); err != nil {
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
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}
	go func() { _ = srv.Serve(ln) }()
	return ln.Addr().String(), nil
}

// expunge deletes one message on the demo server, like a mail program.
func expunge(addr, login, password, mailbox string, uid imap.UID) error {
	c, err := imapclient.DialInsecure(addr, nil)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	if err := c.Login(login, password).Wait(); err != nil {
		return err
	}
	if _, err := c.Select(mailbox, nil).Wait(); err != nil {
		return err
	}
	set := imap.UIDSetNum(uid)
	if err := c.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close(); err != nil {
		return err
	}
	return c.UIDExpunge(set).Close()
}

func crlf(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }

func plain(from, to, subject string, date time.Time, body string) string {
	return crlf(fmt.Sprintf("From: %s\nTo: %s\nSubject: %s\nDate: %s\nContent-Type: text/plain; charset=utf-8\n\n%s\n",
		from, to, subject, date.Format(time.RFC1123Z), body))
}

// reply is a plain message that answers the message with ID parent; refs
// are the earlier IDs of the conversation, oldest first.
func reply(id, from, to, subject string, date time.Time, body string, refs ...string) string {
	links := ""
	if len(refs) > 0 {
		links = "In-Reply-To: <" + refs[len(refs)-1] + ">\nReferences: <" + strings.Join(refs, "> <") + ">\n"
	}
	return crlf(fmt.Sprintf("Message-ID: <%s>\n%sFrom: %s\nTo: %s\nSubject: %s\nDate: %s\nContent-Type: text/plain; charset=utf-8\n\n%s\n",
		id, links, from, to, subject, date.Format(time.RFC1123Z), body))
}

// htmlReply is reply with an HTML body.
func htmlReply(id, from, to, subject string, date time.Time, html string, refs ...string) string {
	return strings.Replace(reply(id, from, to, subject, date, html, refs...), "Content-Type: text/plain", "Content-Type: text/html", 1)
}

// offsite is a six-message conversation in work / INBOX whose 4th message
// is a long HTML mail, for the message view's scrolling and fold (#200,
// web/e2e/reading.spec.ts). Its subject wraps on a phone. Only the long
// mail has the word "itinerary". It sits between the newsletters of weeks
// 34 and 33, below the rows of the list screenshots.
func offsite(day func(days, hours int) time.Time) []message {
	const subject = "Offsite planning: agenda, venue and travel"
	priya := "Priya Shah <priya.shah@work.example>"
	jonas := "Jonas Berg <jonas.berg@work.example>"
	to := "Alex Morgan <alex.morgan@work.example>"
	var long strings.Builder
	long.WriteString(`<html><body style="font-family:Arial,sans-serif;color:#222;line-height:1.5">`)
	long.WriteString(`<p>Hi Alex, here is the full itinerary for the offsite, day by day.</p>`)
	for i := 1; i <= 60; i++ {
		fmt.Fprintf(&long, "<p><b>Item %d.</b> Session %d of the offsite: we meet in room %d, review the open points of the quarter and agree on the next steps before the break.</p>", i, i, 100+i)
	}
	long.WriteString(`<p>See you there,<br>Priya</p></body></html>`)
	ids := []string{"offsite-1@work.example", "offsite-2@work.example", "offsite-3@work.example", "offsite-4@work.example", "offsite-5@work.example", "offsite-6@work.example"}
	return []message{
		{"work", "INBOX", reply(ids[0], priya, to, subject, day(12, 10),
			"Hi Alex,\n\nwe are planning the team offsite. Which dates work for you?\n\nPriya")},
		{"work", "INBOX", reply(ids[1], jonas, to, "Re: "+subject, day(12, 8),
			"Hi both,\n\nthe second week of November works for me.\n\nJonas", ids[0])},
		{"work", "INBOX", htmlReply(ids[2], priya, to, "Re: "+subject, day(12, 6),
			`<html><body style="font-family:Arial,sans-serif;color:#222"><p>Thanks, Jonas. I booked the venue by the lake for November 12 and 13.</p><p>Priya</p></body></html>`, ids[0], ids[1])},
		{"work", "INBOX", htmlReply(ids[3], priya, to, "Re: "+subject, day(12, 4), long.String(), ids[0], ids[1], ids[2])},
		{"work", "INBOX", reply(ids[4], jonas, to, "Re: "+subject, day(12, 2),
			"Great, thanks Priya. I will book the train.\n\nJonas", ids[0], ids[1], ids[2], ids[3])},
		{"work", "INBOX", reply(ids[5], priya, to, "Re: "+subject, day(12, 1),
			"Perfect. The room for item 12 changed to 214.\n\nPriya", ids[0], ids[1], ids[2], ids[3], ids[4])},
	}
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
		// Apple Mail sends PDFs as named inline parts; they count as attachments.
		{"personal", "Sent", crlf(`From: ` + alex + `
To: Dr. Jordan Lee <office@taxadvisor.example>
Cc: Robin Morgan <robin@example.com>
Subject: Documents for the tax return
Date: ` + ago(1, 0).Format(time.RFC1123Z) + `
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="apple"

--apple
Content-Type: text/plain; charset=utf-8

Hello Dr. Lee,

attached are the receipts for our tax return. Robin has the bank statements.

Best regards
Alex Morgan
--apple
Content-Type: application/pdf; name="Receipts 2025.pdf"
Content-Disposition: inline; filename="Receipts 2025.pdf"
Content-Transfer-Encoding: base64

` + base64.StdEncoding.EncodeToString([]byte("%PDF-1.4\n% demo\n")) + `
--apple--
`)},
		// A conversation across INBOX and Sent.
		{"personal", "INBOX", reply("quote-1@kitchen.example", "Riley Chen <riley@kitchen.example>", alex, "Quote for the kitchen renovation", ago(6, 0),
			"Hello Alex,\n\nplease find our quote below: $12,400 including the countertop.\n\nRiley")},
		{"personal", "Sent", reply("re-quote@example.com", alex, "Riley Chen <riley@kitchen.example>", "Re: Quote for the kitchen renovation", ago(5, 2),
			"Hi Riley,\n\nthanks! Could you start in November?\n\nAlex", "quote-1@kitchen.example")},
		{"personal", "INBOX", reply("quote-2@kitchen.example", "Riley Chen <riley@kitchen.example>", alex, "Re: Quote for the kitchen renovation", ago(4, 0),
			"Hi Alex,\n\nyes, November 3 works. I will send the contract tomorrow.\n\nRiley", "quote-1@kitchen.example", "re-quote@example.com")},
		{"personal", "Archive", plain("Home Insurance <service@insurance.example>", alex, "Your 2026 premium invoice", ago(30, 0),
			"Your premium invoice for 2026 is available in your customer portal.")},
		{"personal", "Archive/2025", plain("City Utilities <billing@utilities.example>", alex, "Annual statement 2025", ago(280, 0),
			"Your annual statement for 2025 is attached to your customer account.")},
		{"personal", "Archive/2025", plain("Car Repair Shop <service@garage.example>", alex, "Invoice for the service in March", ago(210, 0),
			"Thanks for your visit. Your invoice of $310.00 is attached.")},
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
	msgs = append(msgs, offsite(ago)...)
	for i := range 25 {
		msgs = append(msgs, message{"work", "INBOX", plain("Newsletter <news@shop.example>", "alex.morgan@work.example",
			fmt.Sprintf("Deals of week %d", 40-i), ago(6+i, 0), fmt.Sprintf("The best deals of week %d.", 40-i))})
	}
	return msgs
}

type datedMessage struct {
	message
	at time.Time
}

// datedMessages have their own internal date: twelve-year-old mail in the
// folder the retention test in web/e2e deletes from, and the spare login's mail.
func datedMessages(now time.Time) []datedMessage {
	alex := "Alex Morgan <alex@example.com>"
	var out []datedMessage
	for i, m := range []struct{ from, subject, body string }{
		{"Tax Office <office@tax.example>", "Tax assessment 2013", "Your tax assessment for 2013 is enclosed."},
		{"City Utilities <billing@utilities.example>", "Annual statement 2013", "Your annual statement for 2013."},
		{"Court Registry <registry@court.example>", "Case 4711: hearing date", "The hearing in case 4711 is set for May 12."},
	} {
		date := now.AddDate(-12, 0, -30*i)
		out = append(out, datedMessage{message{"personal", "Archive/2014", plain(m.from, alex, m.subject, date, m.body)}, date})
	}
	robin := "robin@example.net"
	for _, m := range []struct {
		mailbox, from, subject string
		days                   int
	}{
		{"INBOX", "Book Club <club@books.example>", "Next meeting", 2},
		{"INBOX", "Bike Shop <shop@bikes.example>", "Your repair is done", 4},
		{"Receipts", "Bike Shop <shop@bikes.example>", "Receipt 2026-031", 4},
		{"Trash", "Spam Sender <spam@junk.example>", "Old junk", 9},
	} {
		date := now.AddDate(0, 0, -m.days)
		out = append(out, datedMessage{message{"spare", m.mailbox, plain(m.from, robin, m.subject, date, m.subject+".")}, date})
	}
	return out
}
