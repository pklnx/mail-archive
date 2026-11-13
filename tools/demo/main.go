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
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"html"
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
	"github.com/pklnx/mail-archive/internal/attachtext"
	"github.com/pklnx/mail-archive/internal/attachtext/pdftest"
	"github.com/pklnx/mail-archive/internal/auth"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/config"
	"github.com/pklnx/mail-archive/internal/crypto"
	"github.com/pklnx/mail-archive/internal/jobs"
	"github.com/pklnx/mail-archive/internal/quota"
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
	// Shared accounts (#389): pat owns the account team, and the demo admin
	// shares personal with pat; jo is a reader without accounts, for the
	// end-to-end test of sharing.
	ownerName      = "pat"
	ownerPassword  = "pat-password" //nolint:gosec // public demo login
	readerName     = "jo"
	readerPassword = "jo-password" //nolint:gosec // public demo login
	// An auditor (#393) in the admin's group, with a grant of the admin's
	// work account: the docs screenshots show the Auditor access page, the
	// auditor's role on the users page and the auditor's view. Auditors need
	// 2FA, so it has a fixed TOTP secret of its own.
	auditorName     = "stb-meyer"
	auditorPassword = "stb-meyer-password"               //nolint:gosec // public demo login
	auditorSecret   = "GEZDGNBVGEZDGNBVGEZDGNBVGEZDGNBV" //nolint:gosec // public demo login
)

func main() {
	dbURL := flag.String("db", os.Getenv("MAIL_ARCHIVE_DATABASE_URL"), "PostgreSQL URL of a user with CREATEDB")
	listen := flag.String("listen", "127.0.0.1:18080", "address of the web UI")
	imapListen := flag.String("imap-listen", "127.0.0.1:0", "address of the demo IMAP server (port 0: a free port)")
	nowFlag := flag.String("now", "", "RFC 3339 instant the demo mail is dated from (empty: the current time); for reproducible screenshots")
	setupListen := flag.String("setup-listen", "", "address of a second, new instance that waits for its first admin from the setup link (empty: none)")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	now, err := parseNow(*nowFlag, time.Now)
	if err != nil {
		fmt.Fprintln(os.Stderr, "demo:", err)
		os.Exit(2)
	}
	if err := run(*dbURL, *listen, *imapListen, *setupListen, now, log); err != nil {
		fmt.Fprintln(os.Stderr, "demo:", err)
		os.Exit(1)
	}
}

// parseNow returns the instant the demo's mail is dated from: the -now
// value, or the wall clock when it is empty.
func parseNow(s string, wall func() time.Time) (time.Time, error) {
	if s == "" {
		return wall(), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("-now: %w", err)
	}
	return t, nil
}

// run starts the demo. now dates the made-up mail (the IMAP messages, the
// journal copies and the import's cut-off); everything else, such as the
// 2FA setup and the times the server records, uses the real clock. With
// setupListen it also serves a new instance there (setupDemo).
func run(dbURL, listen, imapListen, setupListen string, now time.Time, log *slog.Logger) error {
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

	// Passkeys need a host name: the demo is meant for localhost. The mail
	// with the link cases (neutralized) links to this address too.
	_, webPort, err := net.SplitHostPort(listen)
	if err != nil {
		return err
	}
	publicURL := "http://localhost:" + webPort

	imapAddr, err := startIMAP(imapListen, now, publicURL)
	if err != nil {
		return err
	}
	host, port, _ := net.SplitHostPort(imapAddr)
	var portNum int
	_, _ = fmt.Sscan(port, &portNum)

	syncer := &archive.Syncer{Store: st, Blobs: blobs, Sealer: sealer, Logger: log, ReconcileInterval: 24 * time.Hour}
	var reconcile []*store.Account
	for _, u := range users {
		if u.spare || u.owner != "" {
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
	journalAcc, err := journal(ctx, st, blobs, now)
	if err != nil {
		return err
	}
	// The work mailbox's mail from before journaling, copied into the
	// journal account once, as "Import existing mail from IMAP" does.
	filler := &archive.Filler{Store: st, Blobs: blobs, Sealer: sealer, Logger: log}
	cutoff := now
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
	if err := shareUsers(ctx, st, sealer, syncer, admin, host, portNum); err != nil {
		return err
	}
	if err := demoGroups(ctx, st); err != nil {
		return err
	}
	if err := demoAuditor(ctx, st, sealer, key, admin, now); err != nil {
		return fmt.Errorf("auditor: %w", err)
	}

	if err := indexAttachments(ctx, st, blobs, log); err != nil {
		return fmt.Errorf("attachment text: %w", err)
	}

	gate, err := demoStorage(ctx, st, log)
	if err != nil {
		return fmt.Errorf("storage: %w", err)
	}
	syncer.Storage, filler.Storage = gate, gate
	go gate.Run(ctx)

	runner := &archive.Runner{Syncer: syncer, Schedule: archive.Schedule{Sync: 6 * time.Hour, SyncMin: 5 * time.Minute}}
	go runner.Run(ctx)
	fills := &archive.FillRunner{Filler: filler}
	go fills.Run(ctx)
	// The retention job, as in serve: it executes deletion runs and account
	// purges confirmed in the UI.
	retention := &archive.Retention{Store: st, Blobs: blobs, Grace: 7 * 24 * time.Hour, Logger: log}
	go retention.Run(ctx)
	srv := web.New(st, blobs, log, web.Options{
		Syncer: syncer, Runner: runner, Fills: fills, Sealer: sealer, SecretKey: key, PublicURL: publicURL,
		// The demo stores its journal messages directly; the UI shows the
		// SMTP server as on, in the default mode with a login (#395), as in
		// a real journaling setup.
		SMTPListen: ":2525",
		SMTPAuth:   true,
		Retention:  retention,
		// The Storage page (#397): the quota, and the data disk.
		StorageQuotaBytes: demoInstanceQuota,
		DiskUsage:         demoDisk,
	})
	fmt.Printf("demo UI on %s, log in as %q with password %q and a TOTP code for secret %s (Ctrl-C to stop and delete the demo data)\n",
		publicURL, loginName, loginPassword, loginTOTPSecret)
	setupDone := make(chan error, 1)
	if setupListen != "" {
		// Ready before the main UI answers: whoever waits for the demo's
		// /healthz finds the new instance listening too.
		serveSetup, closeSetup, err := setupDemo(ctx, dbURL, setupListen, sealer, key, log)
		if err != nil {
			return fmt.Errorf("the setup instance: %w", err)
		}
		defer closeSetup()
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		go func() { setupDone <- serveSetup(ctx) }()
		defer func() {
			cancel()
			<-setupDone
		}()
	}
	served := make(chan error, 1)
	go func() { served <- srv.ListenAndServe(ctx, listen) }()
	select {
	case err = <-served:
	case setupErr := <-setupDone:
		setupDone <- setupErr // for the deferred wait
		// It ends with an error, or without one when ctx ended (a
		// signal): then the main UI stops as well.
		stop()
		err = <-served
		if setupErr != nil {
			return fmt.Errorf("the setup instance: %w", setupErr)
		}
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// demoSetupToken is the setup token of the new instance (setupDemo); the
// end-to-end tests and the screenshots open its link. Made up and not
// secret: it only works on this demo's throwaway database.
var demoSetupToken = strings.Repeat("demo-setup-", 4)

// setupDemo prepares a second, new instance at listen, on a database of its
// own: no user yet and a setup token stored, as `serve` stores it at
// start (#396). Its link is http://localhost:<port>/#setup=<demoSetupToken>.
// It listens at once; serve serves until its ctx ends, and closeAll drops
// the database afterwards.
func setupDemo(ctx context.Context, dbURL, listen string, sealer *crypto.Sealer, key []byte, log *slog.Logger) (serve func(context.Context) error, closeAll func(), err error) {
	var closers []func()
	closeAll = func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}
	defer func() {
		if err != nil {
			closeAll()
		}
	}()
	setupURL, dropDB, err := createDatabase(ctx, dbURL)
	if err != nil {
		return nil, nil, err
	}
	closers = append(closers, dropDB)
	dataDir, err := os.MkdirTemp("", "mail-archive-demo-setup-")
	if err != nil {
		return nil, nil, err
	}
	closers = append(closers, func() { _ = os.RemoveAll(dataDir) })
	st, err := store.Open(ctx, setupURL)
	if err != nil {
		return nil, nil, err
	}
	closers = append(closers, st.Close)
	if _, err := st.Migrate(ctx); err != nil {
		return nil, nil, err
	}
	if _, _, err := st.RecordSetupToken(ctx, store.HashSetupToken(demoSetupToken), time.Now()); err != nil {
		return nil, nil, err
	}
	blobs, err := blobstore.New(dataDir)
	if err != nil {
		return nil, nil, err
	}
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return nil, nil, err
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", listen)
	if err != nil {
		return nil, nil, err
	}
	publicURL := "http://localhost:" + port
	srv := web.New(st, blobs, log, web.Options{Sealer: sealer, SecretKey: key, PublicURL: publicURL})
	fmt.Printf("new instance on %s, set it up with %s/#setup=%s\n", publicURL, publicURL, demoSetupToken)
	return func(ctx context.Context) error {
		if err := srv.Serve(ctx, ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}, closeAll, nil
}

// indexAttachments extracts the text of the archived attachments once, in
// the foreground, as `reindex` does: the screenshots and web/e2e find words
// of the lease PDF from the start. The demo runs no job pool afterwards.
func indexAttachments(ctx context.Context, st *store.Store, blobs *blobstore.Store, log *slog.Logger) error {
	ex := attachtext.New(log)
	defer func() { _ = ex.Close() }()
	x := &archive.AttachmentIndexer{Store: st, Blobs: blobs, Extractor: ex, Logger: log}
	kinds := &jobs.Registry{}
	if err := x.Register(kinds); err != nil {
		return err
	}
	if err := (&jobs.Pool{Store: st, Kinds: kinds, Workers: 1, Logger: log, UntilIdle: true}).Run(ctx); err != nil {
		return err
	}
	if x.Extracted() == 0 {
		return errors.New("no attachment text extracted")
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

// shareUsers creates pat with the account team and jo without accounts,
// and shares the admin's personal with pat (#389): the docs screenshots
// show it on personal's page and on pat's Accounts page.
func shareUsers(ctx context.Context, st *store.Store, sealer *crypto.Sealer, syncer *archive.Syncer, admin *store.User, host string, port int) error {
	hasher := auth.NewHasher(auth.DefaultParams)
	hash, err := hasher.Hash(ctx, ownerPassword)
	if err != nil {
		return err
	}
	pat, err := st.CreateUser(ctx, ownerName, hash, false)
	if err != nil {
		return err
	}
	if hash, err = hasher.Hash(ctx, readerPassword); err != nil {
		return err
	}
	if _, err := st.CreateUser(ctx, readerName, hash, false); err != nil {
		return err
	}
	for _, u := range users {
		if u.owner != ownerName {
			continue
		}
		a := &store.Account{
			Name: u.account, Host: host, Port: port, TLSMode: store.TLSModeNone,
			Username: u.login, Enabled: true, OwnerID: &pat.ID,
		}
		seal := func(id int64) ([]byte, error) { return archive.SealPassword(sealer, id, u.password) }
		if err := st.CreateAccountSealed(ctx, a, seal); err != nil {
			return err
		}
		if res := syncer.SyncAccount(ctx, a); res.Err != nil {
			return fmt.Errorf("sync %s: %w", u.account, res.Err)
		}
	}
	personal, err := st.GetOwnedAccount(ctx, admin.ID, "personal")
	if err != nil {
		return err
	}
	_, err = st.ShareAccount(ctx, personal.Ref(), pat.ID)
	return err
}

// secondGroup is the demo's second customer group (#470): kim, lee and
// sam are in it; the demo admin, pat and jo stay in "Default". With two
// groups the users page shows its group column, filter and section, and
// web/e2e/accounts.spec.ts checks that pat cannot share with kim.
const secondGroup = "Lindqvist Design"

// The demo's storage limits (#397): an instance quota of 1 GiB, and a quota
// of 512 MiB for the second group, so the Storage page shows both bars.
const (
	demoInstanceQuota int64 = 1 << 30
	demoGroupQuota    int64 = 512 << 20
)

// demoDisk stands in for the data disk: made-up, fixed figures, so the
// Storage page shows no figure of the machine the demo runs on and two
// runs of the docs screenshots give the same image.
func demoDisk() (free, total uint64, err error) {
	return 180 << 30, 250 << 30, nil
}

// demoStorage counts the usage once, as the hourly job would, gives the
// second group its quota and returns the storage gate the syncs, fills and
// the web UI ask (#397). It has no disk reserve: the demo's temporary
// directory shares its disk with everything else.
func demoStorage(ctx context.Context, st *store.Store, log *slog.Logger) (*quota.Gate, error) {
	g, err := st.GetGroupByName(ctx, secondGroup)
	if err != nil {
		return nil, err
	}
	q := demoGroupQuota
	if _, err := st.SetGroupQuota(ctx, g.ID, &q); err != nil {
		return nil, err
	}
	if _, err := st.RefreshUsage(ctx); err != nil {
		return nil, err
	}
	gate := quota.New(quota.Config{
		InstanceQuota: demoInstanceQuota,
		Thresholds:    quota.Thresholds{WarnPercent: config.DefaultStorageWarnPercent, OverflowPercent: config.DefaultStorageSMTPOverflowPercent},
		Persist:       true,
	}, st, nil, nil, log)
	st.SetWriteObserver(gate.Wrote)
	gate.Refresh(ctx)
	return gate, nil
}

// demoGroups creates the second group and moves kim, lee and sam into it.
// They have no shares, so nothing ends.
func demoGroups(ctx context.Context, st *store.Store) error {
	g, err := st.CreateGroup(ctx, secondGroup)
	if err != nil {
		return err
	}
	for _, name := range []string{twoFactorName, lockedName, "sam"} {
		u, err := st.GetUserByName(ctx, name)
		if err != nil {
			return err
		}
		if _, err := st.SetUserGroup(ctx, u.ID, g.ID); err != nil {
			return err
		}
	}
	return nil
}

// demoAuditor creates the auditor stb-meyer in the admin's group with 2FA
// on, an active grant of the admin's work account (its INBOX, mail from the
// start of last year on) and a grant the admin revoked, for the history.
// The period and the window are dated from now, as the mail is, so the
// screenshots keep their dates; when now lies so far back that the window
// would be over, it starts today.
func demoAuditor(ctx context.Context, st *store.Store, sealer *crypto.Sealer, key []byte, admin *store.User, now time.Time) error {
	hash, err := auth.NewHasher(auth.DefaultParams).Hash(ctx, auditorPassword)
	if err != nil {
		return err
	}
	u, err := st.CreateUserInGroup(ctx, store.NewUser{Name: auditorName, PasswordHash: hash, GroupID: admin.GroupID, Auditor: true})
	if err != nil {
		return err
	}
	// Confirmed with a code from an hour ago, so every current time step is
	// still unused for a login.
	past := time.Now().Add(-time.Hour)
	code, err := auth.GenerateTOTP(auditorSecret, past)
	if err != nil {
		return err
	}
	if err := st.BeginTwoFactorSetup(ctx, u.ID, auditorSecret, sealer); err != nil {
		return err
	}
	if err := st.ConfirmTwoFactorSetup(ctx, u.ID, code, past, sealer, nil, key); err != nil {
		return err
	}
	work, err := st.GetOwnedAccount(ctx, admin.ID, "work")
	if err != nil {
		return err
	}
	folders, err := st.ListFolderStates(ctx, work.ID)
	if err != nil {
		return err
	}
	var inbox []int64
	for _, f := range folders {
		if f.Name == "INBOX" {
			inbox = append(inbox, f.ID)
		}
	}
	// Midnights in the screenshots' zone (Europe/Berlin, CET in winter), as
	// an owner's browser there sends them.
	cet := time.FixedZone("CET", 3600)
	day := func(y int, m time.Month, d int) *time.Time {
		t := time.Date(y, m, d, 0, 0, 0, 0, cet)
		return &t
	}
	start := now.Add(-24 * time.Hour).Truncate(time.Hour)
	if !start.AddDate(0, 0, 90).After(time.Now().Add(time.Hour)) {
		start = time.Now()
	}
	year := now.Year()
	grant := store.NewGrant{
		OwnerID: admin.ID, AuditorID: u.ID, Label: fmt.Sprintf("Betriebsprüfung %d–%d", year-1, year),
		Accounts:   []store.GrantAccount{{AccountID: work.ID, Folders: inbox}},
		PeriodFrom: day(year-1, time.January, 1),
		StartsAt:   &start, EndsAt: start.AddDate(0, 0, 90),
	}
	if _, err := st.CreateGrant(ctx, grant); err != nil {
		return err
	}
	grant.Label, grant.PeriodFrom, grant.PeriodUntil = fmt.Sprintf("Umsatzsteuer-Sonderprüfung %d", year-1), day(year-1, time.January, 1), day(year, time.January, 1)
	grant.Accounts = []store.GrantAccount{{AccountID: work.ID}}
	ended, err := st.CreateGrant(ctx, grant)
	if err != nil {
		return err
	}
	return st.RevokeGrant(ctx, ended, &admin.ID, &admin.ID)
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
	// A secret the mail server logs in with (#395), three days old, as
	// `account smtp-secret journal --new` creates it: the account page and
	// the Users page list it. Only the hash of a fixed text is stored, which
	// is no secret of the SMTP format, so it lets no one log in.
	hash := sha256.Sum256([]byte("mail-archive demo: a listed SMTP secret"))
	if _, _, err := st.CreateSMTPCredential(ctx, acc.Ref(), store.NewSMTPCredential{
		Hash: hash[:], Hint: "x7qm", Now: now.Add(-72 * time.Hour),
	}); err != nil {
		return nil, err
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
	// owner is the user who owns the account; empty for the demo admin.
	owner string
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
		account: "team", login: "pat@team.example", password: "demo",
		mailboxes: []string{"INBOX"},
		owner:     ownerName,
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
func startIMAP(addr string, now time.Time, webURL string) (string, error) {
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
	for _, m := range messages(now, webURL) {
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
// 34 and 33, below the rows of the list screenshots. The long mail has a
// doctype, so it renders in standards mode (#206); Sam's mail (the docs
// screenshots) has none and renders in quirks mode: the e2e tests cover both.
func offsite(day func(days, hours int) time.Time) []message {
	const subject = "Offsite planning: agenda, venue and travel"
	priya := "Priya Shah <priya.shah@work.example>"
	jonas := "Jonas Berg <jonas.berg@work.example>"
	to := "Alex Morgan <alex.morgan@work.example>"
	var long strings.Builder
	long.WriteString(`<!DOCTYPE html>` + "\n")
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

// neutralized is an HTML mail with declarative shadow roots and srcdoc
// frames in every syntax variant (#209), for web/e2e/reading.spec.ts. Each
// host is a fixed-size box whose content would be one link filling it, to
// /api/openapi.json?case=<name> in a new tab: an API route a browser opens,
// so the server's Fetch Metadata guard lets it through. The server renames
// the attributes (neutralizeHTML in internal/web), so the boxes stay empty
// and no click on them opens anything. An external link is the sentinel.
// Only this mail has the word "kaleidoscope". It sits between the
// newsletters of weeks 27 and 26, below the rows of the list screenshots.
// Nothing in it violates the frame's CSP.
//
// A further row holds the link cases of web/e2e/frame-links.spec.ts (#459):
// fixed-size boxes marked data-link-case, each filled by a link into the app
// (to /api/openapi.json?link=<name>, relative, or absolute to webURL, the
// web UI's address) or to a javascript: or data: URL, in every syntax and
// element the server's link rewrite covers, and a meta refresh. The server
// renames the attributes, so no box is a link in any engine. A second
// external link is the spec's redirect check.
func neutralized(date time.Time, webURL string) message {
	const box = "display:inline-block;vertical-align:top;width:120px;height:40px;margin:4px;border:1px dashed #888"
	link := func(name string) string {
		return `<a href="/api/openapi.json?case=` + name + `" target="_blank" style="display:block;height:100%;background:#fde68a">` + name + `</a>`
	}
	// A shadow root host: the markup before the template, the template's
	// start tag, and what closes the host.
	host := func(name, before, tag, after string) string {
		return before + `<div data-case="` + name + `" style="` + box + `">` + tag + link(name) + `</template></div>` + after
	}
	// The document of a srcdoc frame: the link fills the frame.
	frameDoc := func(name string) string {
		return `<body style="margin:0"><a href="/api/openapi.json?case=` + name + `" target="_blank" ` +
			`style="position:fixed;inset:0;background:#fde68a">` + name + `</a></body>`
	}
	frame := func(name, start, end string) string {
		return start + ` data-case="` + name + `" style="` + box + `"></iframe>` + end
	}
	esc := html.EscapeString
	var b strings.Builder
	b.WriteString(`<html><body style="font-family:Arial,sans-serif;color:#222">`)
	b.WriteString(`<p>Hi Alex, this is the kaleidoscope rendering check. A safe mail client shows every box below empty.</p>`)
	b.WriteString(`<p>Shadow roots:</p><div>`)
	b.WriteString(host("open", "", `<template shadowrootmode="open">`, ""))
	b.WriteString(host("closed", "", `<template shadowrootmode="closed">`, ""))
	b.WriteString(host("legacy", "", `<template shadowroot="closed">`, ""))
	b.WriteString(host("uppercase", "", `<TEMPLATE ShadowRootMode=Closed>`, ""))
	b.WriteString(host("formfeed", "", "<template\fshadowrootmode=closed>", ""))
	b.WriteString(host("slash", "", `<template/shadowrootmode=closed>`, ""))
	b.WriteString(host("nospace", "", `<template title="t"shadowrootmode=closed>`, ""))
	b.WriteString(host("entity", "", `<template shadowrootmode="&#99;losed">`, ""))
	b.WriteString(host("noscript", "<noscript>", `<template shadowrootmode=closed>`, "</noscript>"))
	b.WriteString(host("math", `<math><mtext><style></style></mtext><title>`, `<template shadowrootmode=closed>`, "</title></math>"))
	// An unknown cid: reference whose match runs over "=": the server
	// inlines it as "data:," and only then renames, so that the value
	// "shadowrootmode=closed" cannot turn into an attribute name.
	b.WriteString(host("cid", "", `<template cid:q= shadowrootmode=closed>`, ""))
	b.WriteString(`</div><p>Inline frames:</p><div>`)
	b.WriteString(frame("srcdoc", `<iframe srcdoc="`+esc(frameDoc("srcdoc"))+`"`, ""))
	b.WriteString(frame("srcdoc-upper", `<IFRAME SRCDOC="`+esc(frameDoc("srcdoc-upper"))+`"`, ""))
	b.WriteString(frame("srcdoc-formfeed", "<iframe\fsrcdoc=\""+esc(frameDoc("srcdoc-formfeed"))+`"`, ""))
	b.WriteString(frame("srcdoc-slash", `<iframe/srcdoc="`+esc(frameDoc("srcdoc-slash"))+`"`, ""))
	b.WriteString(frame("srcdoc-nospace", `<iframe title="t"srcdoc="`+esc(frameDoc("srcdoc-nospace"))+`"`, ""))
	b.WriteString(frame("srcdoc-entity", `<iframe srcdoc="`+strings.NewReplacer("<", "&#x3C;", ">", "&#x3E;", `"`, "&#x22;").Replace(frameDoc("srcdoc-entity"))+`"`, ""))
	b.WriteString(frame("srcdoc-noscript", `<noscript><iframe srcdoc="`+esc(frameDoc("srcdoc-noscript"))+`"`, "</noscript>"))
	// As the "cid" host: an unquoted srcdoc value without white space.
	b.WriteString(frame("srcdoc-cid", `<iframe cid:q= srcdoc=`+strings.ReplaceAll(esc(frameDoc("srcdoc-cid")), " ", "&#32;"), ""))
	b.WriteString(frame("srcdoc-shadow", `<iframe srcdoc="`+esc(`<body style="margin:0"><div style="position:fixed;inset:0">`+
		`<template shadowrootmode="closed">`+link("srcdoc-shadow")+`</template></div></body>`)+`"`, ""))
	b.WriteString(`</div><p>Links into the archive:</p><div>`)
	hostPort := strings.TrimPrefix(webURL, "http://")
	api := func(name string) string { return "/api/openapi.json?link=" + name }
	fill := `style="display:block;height:100%;background:#fde68a"`
	linkBox := func(name, content string) {
		b.WriteString(`<div data-link-case="` + name + `" style="` + box + `">` + content + `</div>`)
	}
	a := func(name, href string) {
		linkBox(name, `<a href="`+href+`" `+fill+`>`+name+`</a>`)
	}
	a("root", api("root"))
	linkBox("self", `<a href="`+api("self")+`" target="_self" `+fill+`>self</a>`)
	a("relative", "api/openapi.json?link=relative")
	a("scheme-relative", "//"+hostPort+api("scheme-relative"))
	a("absolute", webURL+api("absolute"))
	a("uppercase", strings.ToUpper(webURL)+api("uppercase"))
	a("ip", "http://127.0.0.1:"+hostPort[strings.LastIndexByte(hostPort, ':')+1:]+api("ip"))
	a("entity", "http&#58;//"+hostPort+api("entity"))
	a("slash", "http:"+api("slash"))
	a("backslash", `/\`+hostPort+api("backslash"))
	a("fragment", "#top")
	a("empty", "")
	linkBox("bare", `<a href `+fill+`>bare</a>`)
	a("javascript", "javascript:void(0)")
	a("data", "data:text/html,link=data")
	// A transparent GIF as the image map's picture.
	linkBox("area", `<img src="data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7" usemap="#link-map" width="120" height="40" alt="">`+
		`<map name="link-map"><area href="`+api("area")+`" shape="rect" coords="0,0,120,40" alt="area"></map>`)
	svg := func(name, content string) {
		linkBox(name, `<svg width="120" height="40" xmlns:xlink="http://www.w3.org/1999/xlink">`+content+`</svg>`)
	}
	rect := `<rect width="120" height="40" fill="#fde68a"/>`
	svg("svg", `<a href="`+api("svg")+`">`+rect+`</a>`)
	svg("svg-xlink", `<a xlink:href="`+api("svg-xlink")+`">`+rect+`</a>`)
	svg("use", `<defs><g id="link-use">`+rect+`</g></defs><a href="`+api("use")+`"><use href="#link-use"/></a>`)
	svg("set", `<a><set attributeName="href" to="`+api("set")+`"/>`+rect+`</a>`)
	svg("animate", `<a><animate attributeName="href" values="`+api("animate")+`"/>`+rect+`</a>`)
	linkBox("mathml", `<math style="display:block;width:100%;height:100%"><mi href="`+api("mathml")+`">mathml</mi></math>`)
	b.WriteString(`<meta http-equiv="refresh" content="0;url=` + api("refresh") + `">`)
	// An external link to another site, where web/e2e/frame-links.spec.ts
	// runs a server (web/e2e/demo.ts, redirectOrigin) that redirects back to
	// the archive: the server refuses that navigation as cross-site.
	b.WriteString(`</div><p><a href="https://links.example/kaleidoscope">The check online</a></p>` +
		`<p><a href="http://127.0.0.2:18182/back">The redirect check</a></p></body></html>`)
	return message{"work", "INBOX", htmlReply("kaleidoscope@layout.example", "Layout Lab <check@layout.example>", "alex.morgan@work.example",
		"Rendering check: boxes that stay empty", date, b.String())}
}

// The hosts and ports the request checks below point at (web/e2e/demo.ts):
// the hint servers of web/e2e/hints.spec.ts on *.localhost, and the server
// of web/e2e/referrer.spec.ts on 127.0.0.2, which is not a demo host, so
// the server keeps its links.
const (
	hintHTTP    = "http://hint-prerender.localhost:18184"
	hintConnect = "http://hint-preconnect.localhost:18185"
	referrerURL = "http://127.0.0.2:18183"
)

// hintCheck is an HTML mail that asks the browser for requests on its own,
// for web/e2e/hints.spec.ts: resource hints (prerender, prefetch,
// preconnect, dns-prefetch, preload, modulepreload) in <head> and <body> and
// in syntax variants, speculation rules, and frames whose src would make the
// browser connect to the host. The server renames rel and src (and the
// sandbox and script-src 'none' block the rules), so the browser sends none
// of them. Its attribute variant is for web/e2e/referrer.spec.ts: a link and
// an image that set their own referrer policy and cors mode, which the
// server renames too. It has no SVG or MathML, so its remote images load. Only this mail has the word "periscope". It sits below the
// kaleidoscope mail, below the rows of the list screenshots.
func hintCheck(date time.Time) message {
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html>` + "\n" + `<html><head>`)
	b.WriteString(`<link rel="prerender" href="` + hintHTTP + `/head-prerender">`)
	b.WriteString(`<link rel="prefetch" href="` + hintHTTP + `/head-prefetch">`)
	b.WriteString(`<link rel="preconnect" href="` + hintConnect + `/">`)
	b.WriteString(`<link rel="dns-prefetch" href="//hint-dns.localhost/">`)
	b.WriteString(`<link rel="preload" as="image" href="` + hintHTTP + `/preload.png" imagesrcset="` + hintHTTP + `/preload-2x.png 2x">`)
	b.WriteString(`<link rel="modulepreload" href="` + hintHTTP + `/module.js">`)
	b.WriteString(`<script type="speculationrules">{"prerender":[{"source":"list","urls":["` + hintHTTP + `/speculation"]}]}</script>`)
	b.WriteString(`</head><body style="font-family:Arial,sans-serif;color:#222">`)
	b.WriteString(`<p>Hi Alex, this is the periscope check. A safe mail client sends no request for this mail until you click.</p>`)
	b.WriteString(`<LINK REL=PRERENDER HREF=` + hintHTTP + `/body-prerender>`)
	b.WriteString(`<link rel=preconnect href=` + hintConnect + `/body>`)
	b.WriteString(`<link/rel=dns-prefetch href="//hint-dns-body.localhost/">`)
	b.WriteString(`<link href="` + hintHTTP + `/after-quote"rel="prefetch">`)
	// A frame's URL: the CSP blocks the navigation, but a full Chromium
	// connects to the host first. The server renames src on <iframe> and
	// <frame>. A <frame> outside a <frameset> is dropped by the parser; it is
	// here for the bytes, the unit tests cover the frameset.
	b.WriteString(`<iframe src="` + hintHTTP + `/iframe" width="16" height="16"></iframe>`)
	b.WriteString(`<IFRAME SRC=` + hintConnect + `/iframe-upper width=16 height=16></IFRAME>`)
	b.WriteString(`<frame src="` + hintHTTP + `/frame">`)
	b.WriteString(`<p><a referrerpolicy="unsafe-url" href="` + referrerURL + `/attr-click">The attribute check</a></p>`)
	// No SVG <image> here: a mail with inline SVG gets no remote images at
	// all, so this mail would get none either. cssSVGCheck has one.
	b.WriteString(`<p><img referrerpolicy="unsafe-url" crossorigin="anonymous" src="` + referrerURL + `/attr-img" width="16" height="16" alt=""></p>`)
	b.WriteString(`</body></html>`)
	return message{"work", "INBOX", htmlReply("periscope@layout.example", "Layout Lab <check@layout.example>", "alex.morgan@work.example",
		"Request check: hints that stay silent", date, b.String())}
}

// referrerCheck is an HTML mail whose <meta> asks for the full referrer,
// with a link and an image, for web/e2e/referrer.spec.ts. The server renames
// the meta's content, so the server's Referrer-Policy: no-referrer holds.
// Only this mail has the word "semaphore". It sits below the kaleidoscope
// mail, below the rows of the list screenshots.
func referrerCheck(date time.Time) message {
	return message{"work", "INBOX", htmlReply("semaphore@layout.example", "Layout Lab <check@layout.example>", "alex.morgan@work.example",
		"Referrer check: links that tell nothing", date,
		`<html><head><meta name="referrer" content="unsafe-url"></head><body style="font-family:Arial,sans-serif;color:#222">`+
			`<p>Hi Alex, this is the semaphore check. A safe mail client does not tell the sender where you read this mail.</p>`+
			`<p><a href="`+referrerURL+`/meta-click">The meta check</a></p>`+
			`<p><img src="`+referrerURL+`/meta-img" width="16" height="16" alt=""></p></body></html>`)}
}

// cssBox sizes a check's element, so its CSS images are in the layout.
const cssBox = `display:block;width:24px;height:24px;margin:2px;`

// cssCheck is an HTML mail whose CSS asks for images on
// web/e2e/css-images.spec.ts's recorder (referrerURL), one path per
// construct: every property that loads an image (as a style attribute,
// /css-<name>-attr, and in <style>, /css-<name>-style), image-set() and
// friends, var(), a custom function, escapes, character references,
// preprocessing and comments. Chromium would send the archive's origin with
// each of them; the server makes them inert, so after "Load remote images"
// only the <img> control loads. It has no SVG or MathML (a mail with them
// gets no remote images at all, cssSVGCheck), so it is also the positive
// control that an ordinary HTML mail still loads remote images. Only this
// mail has the word "zoetrope". It sits below the kaleidoscope mail, below
// the rows of the list screenshots.
func cssCheck(date time.Time) message {
	u := func(path string) string { return referrerURL + "/css-" + path }
	var rules, body strings.Builder
	// attr adds an element whose style attribute is decl ({u}: the URL),
	// with its double quotes as &quot;; raw adds one with decl as it is.
	raw := func(name, decl string) {
		body.WriteString(`<div style="` + cssBox + strings.ReplaceAll(decl, "{u}", u(name+"-attr")) + `"></div>`)
	}
	attr := func(name, decl string) { raw(name, strings.ReplaceAll(decl, `"`, "&quot;")) }
	// style adds a rule with decl and an element it applies to.
	style := func(name, decl string) {
		rules.WriteString(".c-" + name + "{" + cssBox + strings.ReplaceAll(decl, "{u}", u(name+"-style")) + "}")
		body.WriteString(`<div class="c-` + name + `"></div>`)
	}
	both := func(name, decl string) { attr(name, decl); style(name, decl) }

	// Every property that loads an image.
	both("background-image", `background-image:url({u})`)
	both("background", `background:#036 url({u}) no-repeat`)
	both("list-style-image", `display:list-item;list-style-position:inside;list-style-image:url({u})`)
	both("list-style", `display:list-item;list-style:square inside url({u})`)
	both("border-image", `border:4px solid #036;border-image:url({u}) 30 round`)
	both("content", `content:url({u})`)
	both("cursor", `cursor:url({u}),auto`)
	both("mask-image", `mask-image:url({u})`)
	both("mask", `mask:url({u})`)
	both("webkit-mask-image", `-webkit-mask-image:url({u})`)
	both("shape-outside", `float:left;shape-outside:url({u})`)
	both("clip-path", `clip-path:url({u}#c)`)
	// Functions that take a URL as a string, also through var() and a
	// custom function (defined once in <style>).
	both("image-set", `background-image:image-set("{u}" 1x)`)
	both("webkit-image-set", `background-image:-webkit-image-set("{u}" 1x)`)
	both("var-url", `--v:url({u});background-image:var(--v)`)
	both("var-string", `--s:"{u}";background-image:image-set(var(--s) 1x)`)
	both("function", `background-image:--f_("{u}")`)
	// Spellings: a string, escapes, preprocessing, comments.
	both("url-string", `background-image:url("{u}")`)
	both("css-escape", `background-image:\75 rl({u})`)
	both("esc-backslash-l", `background-image:ur\l({u})`)
	both("esc-55-RL", `background-image:\55 RL({u})`)
	both("esc-000072", `background-image:u\000072l({u})`)
	both("esc-imageset", `background-image:imag\65-set("{u}" 1x)`)
	both("upper-imageset", `background-image:IMAGE-SET("{u}" 1x)`)
	both("url-tab-escape", `background-image:url(\9 {u})`)
	both("comment-around", `/**/background-image:url({u})/**/`)
	style("pre-ff", "background-image:\furl({u})")
	// Character references, only in an attribute.
	raw("pre-cr", `background-image:&#13;url({u})`)
	raw("pre-ff", `background-image:&#12;url({u})`)
	raw("ref-r", `background-image:u&#114;l({u})`)
	raw("quot-bare", `background-image:url(&quot`+strings.TrimPrefix(u("quot-bare-attr"), "http:")+`&quot)`)
	// <!-- and --> around a rule.
	rules.WriteString("<!-- .c-cdo-cdc{" + cssBox + "background-image:url(" + u("cdo-cdc-style") + ")} -->")
	body.WriteString(`<div class="c-cdo-cdc"></div>`)

	return message{"work", "INBOX", htmlReply("zoetrope@layout.example", "Layout Lab <check@layout.example>", "alex.morgan@work.example",
		"CSS image check: backgrounds that stay empty", date,
		`<!DOCTYPE html>`+"\n"+`<html><head><style>@function --f_(--s){result:image-set(var(--s) 1x)}`+rules.String()+`</style></head>`+
			`<body style="font-family:Arial,sans-serif;color:#222">`+
			`<p>Hi Alex, this is the zoetrope check. A safe mail client loads no image a mail sets in its CSS, not even after you load remote images.</p>`+
			`<p><img src="`+referrerURL+`/css-control-img" width="16" height="16" alt=""></p>`+
			body.String()+`</body></html>`)}
}

// cssSVGCheck is an HTML mail with inline SVG and MathML whose CSS loads
// images, for web/e2e/css-images.spec.ts: SVG presentation attributes and a
// plain SVG <style>, which the server's rewrite makes inert, an SVG <image>
// element (/css-svg-image, not CSS, but under the same refusal), and SVG
// <style> that x/net/html, the server's HTML parser, does not see where the
// browser does (found in review): a duplicate attribute with the
// unquoted value "/" (x/net/html calls the tag self-closing, so the <style>
// after it is HTML raw text there), and a <template> in an integration
// point (x/net/html ends the document there). The browser joins the
// <style>'s text children around a comment or CDATA section to url(, so the
// rewrite, which reads raw text, sees no URL either. A mail with inline SVG
// or MathML gets no remote images, so no path may get a request, and the
// web UI says why after "Load remote images". Nothing in it is CSS that the
// server's parse check finds: without the refusal by "<svg" and "<math",
// the parser cases would load. Only this mail has the word "stereoscope".
// It sits below the kaleidoscope mail, below the rows of the list
// screenshots.
func cssSVGCheck(date time.Time) message {
	u := func(path string) string { return referrerURL + "/css-" + path }
	var b strings.Builder
	// SVG: presentation attributes, and a plain <style>.
	svg := func(content string) {
		b.WriteString(`<svg width="24" height="24" style="margin:2px">` + content + `</svg>`)
	}
	rect := `<rect width="24" height="24"`
	svg(rect + ` fill="url(` + u("svg-fill-attr") + `#p)"/>`)
	svg(rect + ` fill="none" stroke-width="4" stroke="url(` + u("svg-stroke-attr") + `#p)"/>`)
	svg(rect + ` fill="#036" mask="url(` + u("svg-mask-attr") + `#m)"/>`)
	svg(rect + ` fill="#036" clip-path="url(` + u("svg-clip-path-attr") + `#c)"/>`)
	svg(`<path d="M4 4L20 20" stroke="#036" marker-start="url(` + u("svg-marker-start-attr") + `#k)"/>`)
	svg(`<style>.sv{fill:url(` + u("svg-style") + `#p)}</style>` + rect + ` class="sv"/>`)
	// An SVG image element, with its own referrer policy and cors mode
	// (formerly in hintCheck): no CSS, but refused all the same.
	svg(`<image crossorigin="anonymous" referrerpolicy="unsafe-url" href="` + u("svg-image") + `" width="24" height="24"/>`)
	// The parser cases: each <style> gives the HTML <div> after it a
	// background. The duplicate attribute cases first, the <template>
	// cases last: x/net/html sees nothing after the first of those.
	n := 0
	parser := func(name, before, style, after string) {
		n++
		c := fmt.Sprintf("p%d", n)
		b.WriteString(before + `<style>.` + c + `{background-image:` + strings.ReplaceAll(style, "{u}", u(name)) + `}</style>` +
			after + `<div class="` + c + `" style="` + cssBox + `"></div>`)
	}
	parser("dup-attr-slash", `<svg width="24" height="24"><foreignObject width="24" height="24"><svg data-x=a data-x=/>`, `url<!--c-->({u})`, `</svg></foreignObject></svg>`)
	parser("dup-attr-bare", `<svg width="24" height="24"><foreignObject width="24" height="24"><svg data-x data-x=/>`, `url<!--c-->({u})`, `</svg></foreignObject></svg>`)
	parser("math-dup-attr", `<math><mi><svg data-x=a data-x=/>`, `url<!--c-->({u})`, `</svg></mi></math>`)
	parser("template-foreignobject", `<svg width="24" height="24"><foreignObject width="24" height="24"><div><template></template></div></foreignObject></svg><svg width="0" height="0">`, `url<!--c-->({u})`, `</svg>`)
	parser("template-mtext", `<math><mtext><template></template></mtext></math><svg width="0" height="0">`, `url<![CDATA[(]]>{u})`, `</svg>`)
	parser("template-mi", `<math><mi><template></template></mi></math><svg width="0" height="0">`, `url<!--c-->({u})`, `</svg>`)
	return message{"work", "INBOX", htmlReply("stereoscope@layout.example", "Layout Lab <check@layout.example>", "alex.morgan@work.example",
		"CSS check, SVG style: images that stay off", date,
		`<html><body style="font-family:Arial,sans-serif;color:#222">`+
			`<p>Hi Alex, this is the stereoscope check. A safe mail client loads no image this mail's SVG styles ask for.</p>`+
			`<p><img src="`+referrerURL+`/css-svg-control-img" width="16" height="16" alt=""></p>`+
			b.String()+`</body></html>`)}
}

// leasePDF is the lease attached to Sam's mail: a PDF with a text layer.
// Only it has the word "deposit", so a search for it matches the
// attachment alone (web/e2e/mail.spec.ts, the search-attachment
// screenshot).
var leasePDF = pdftest.Text("Residential Lease Agreement\n\n" +
	"Landlord: Sam Taylor\nTenant: Alex Morgan\n\n" +
	"The monthly rent is $1,450, due on the first day of each month.\n" +
	"The tenant pays a security deposit of $2,900 before moving in;\n" +
	"it is returned within 30 days after the end of the lease.")

// A 16x16 blue square as an inline image.
const logoPNG = "iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAIAAACQkWg2AAAAFklEQVR4nGNQTX5NEmIY1TCqYfhqAAB2SXMQ7mVTQgAAAABJRU5ErkJggg=="

// messages are the demo's IMAP mail. webURL is the web UI's address, which
// the rendering check links to (neutralized).
func messages(now time.Time, webURL string) []message {
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

` + base64.StdEncoding.EncodeToString(leasePDF) + `
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
		// pat's account; only the kickoff mail has the word "moodboard" (web/e2e/accounts.spec.ts).
		{"team", "INBOX", plain("Mia Park <mia@team.example>", "Pat Kim <pat@team.example>", "Spring campaign kickoff", ago(2, 3),
			"Hi Pat,\n\nthe kickoff is on Monday. I attached the moodboard to the shared drive.\n\nMia")},
		{"team", "INBOX", plain("Print Studio <hello@print.example>", "Pat Kim <pat@team.example>", "Proofs for the flyers", ago(4, 1),
			"Hello Pat,\n\nthe proofs for the flyers are ready for your review.\n\nPrint Studio")},
		{"old-provider", "INBOX", plain("Welcome Team <welcome@oldmail.example>", "alex@oldmail.example", "Welcome to Oldmail", ago(900, 0),
			"Thanks for signing up. Your mailbox is ready.")},
	}
	msgs = append(msgs, offsite(ago)...)
	msgs = append(msgs, neutralized(ago(19, 12), webURL), hintCheck(ago(19, 14)), referrerCheck(ago(19, 16)),
		cssCheck(ago(19, 18)), cssSVGCheck(ago(19, 20)))
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
