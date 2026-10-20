package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/store"
)

// Account management and sync. Passwords are only ever written: no response
// contains one, encrypted or not.

type lastRunJSON struct {
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
	Status     string     `json:"status"`
	Fetched    int        `json:"fetched"`
	New        int        `json:"new"`
	Error      string     `json:"error,omitempty"`
	// What reconciling changed: folders compared with the server, locations
	// newly gone or back, and locations whose flags changed.
	ReconciledFolders int `json:"reconciledFolders"`
	Gone              int `json:"gone"`
	Back              int `json:"back"`
	FlagsChanged      int `json:"flagsChanged"`
}

type syncJSON struct {
	// State is "idle", "queued" or "running" (also when the CLI syncs).
	State   string       `json:"state"`
	LastRun *lastRunJSON `json:"lastRun"`
	// FailureStreak counts the failed syncs in a row; FailingSince is when
	// the first of them started (null without a streak).
	FailureStreak int        `json:"failureStreak"`
	FailingSince  *time.Time `json:"failingSince"`
	// Health is "ok", "failing" (the last alertAfter syncs failed) or
	// "stale" (no successful sync within two of the account's intervals, at
	// least one plus an hour; never while it is running or queued).
	Health string `json:"health"`
	// SyncEvery is how often the schedule syncs the account, after the
	// default and the floor ("1h0m0s"); null when it does not (not on the
	// schedule, schedule off, removed, or no server).
	SyncEvery *string `json:"syncEvery"`
	// NextAt is when the schedule syncs it next; in the past while it waits
	// for its turn. Null without SyncEvery or when disabled.
	NextAt *time.Time `json:"nextAt"`
}

type accountJSON struct {
	Name string `json:"name"`
	// Kind is "imap", "import" for mail imported from files, or "smtp" for
	// mail a mail server delivers over SMTP. Import and smtp accounts have
	// no server and are never synced.
	Kind            string   `json:"kind"`
	Enabled         bool     `json:"enabled"`
	Removed         bool     `json:"removed"`
	Host            string   `json:"host"`
	Port            int      `json:"port"`
	TLS             string   `json:"tls"`
	Username        string   `json:"username"`
	IncludedFolders []string `json:"includedFolders"`
	ExcludedFolders []string `json:"excludedFolders"`
	// SyncInterval is the account's own sync interval: "default" (the
	// server's), "manual" (not on the schedule) or a duration ("30m0s").
	// Always "default" for import and smtp accounts.
	SyncInterval string       `json:"syncInterval"`
	Folders      []folderJSON `json:"folders"`
	Sync         syncJSON     `json:"sync"`
	// GoneMessages counts messages whose locations in this account are all
	// gone from the server; LastReconciledAt is the latest reconcile of any
	// of its folders.
	GoneMessages     int64      `json:"goneMessages"`
	LastReconciledAt *time.Time `json:"lastReconciledAt"`
	// Addresses an smtp account receives for, and when it last accepted a
	// message; empty and null for other kinds.
	Addresses      []string   `json:"addresses"`
	LastReceivedAt *time.Time `json:"lastReceivedAt"`
	// Fill is the latest fill of an smtp account, null without one;
	// Fills its recurring fills (also stopped ones) and its latest fill,
	// newest first; FirstReceived its first message still stored, null
	// without one. Null and empty for other kinds.
	Fill          *fillJSON          `json:"fill"`
	Fills         []*fillJSON        `json:"fills"`
	FirstReceived *firstReceivedJSON `json:"firstReceived"`
}

type folderJSON struct {
	Name             string     `json:"name"`
	Messages         int64      `json:"messages"`
	LastSyncedAt     *time.Time `json:"lastSyncedAt"`
	LastReconciledAt *time.Time `json:"lastReconciledAt"`
	// Delimiter is the folder hierarchy delimiter ("/" or "."), "" if the
	// server has none, null while unknown (before the folder's first sync
	// since #91). Import and smtp folders always use "/".
	Delimiter *string `json:"delimiter"`
}

// smtpServerJSON says the SMTP server is on. Where Postfix reaches it (host
// and published port) only the admin knows, so it carries no address.
type smtpServerJSON struct{}

type accountsResponse struct {
	Accounts []accountJSON `json:"accounts"`
	// Manage is false when the server has no secret key: accounts can then
	// be browsed but not added, changed or synced.
	Manage bool `json:"manage"`
	// SyncInterval is the server's MAIL_ARCHIVE_SYNC_INTERVAL ("6h0m0s"),
	// the default for accounts without their own; empty when the schedule
	// is off. Not the same as an account's syncInterval.
	SyncInterval string `json:"syncInterval"`
	// SyncIntervalMin is the floor for every account's interval ("5m0s");
	// empty when the server cannot sync.
	SyncIntervalMin string `json:"syncIntervalMin"`
	// ReconcileInterval: how often each folder is compared with the server
	// ("24h0m0s"), empty if only on request.
	ReconcileInterval string `json:"reconcileInterval"`
	// AlertAfter failed syncs in a row make an account failing.
	AlertAfter int `json:"alertAfter"`
	// SMTP is null when the SMTP server is off (MAIL_ARCHIVE_SMTP_LISTEN
	// empty): smtp accounts then receive nothing.
	SMTP *smtpServerJSON `json:"smtp"`
	// OtherFailing, for admins only, counts the failing accounts of other
	// users. Admins never see their names.
	OtherFailing *int64 `json:"otherFailing,omitempty"`
}

func (s *Server) handleAccounts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	accounts, err := s.store.ListOwnedAccounts(ctx, userID(r))
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	counts, err := s.store.ListAccountFolders(ctx, userID(r))
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	busy, err := s.loadBusy(ctx)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	runs, running, queued := busy.runs, busy.running, busy.queued
	me := userID(r)
	health, err := s.store.SyncHealth(ctx, &me)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	reconciled, err := s.store.ReconcileStates(ctx, me)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	addresses, err := s.store.SMTPAddresses(ctx)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	received, err := s.store.LastReceived(ctx)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	fills, err := s.store.OwnerFills(ctx, me)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	fillHealth, err := s.store.FillHealth(ctx, &me)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	folders := make(map[string][]folderJSON, len(counts))
	for _, a := range counts {
		list := make([]folderJSON, 0, len(a.Folders))
		for _, f := range a.Folders {
			delim := f.Delimiter
			if !a.Kind.HasServer() {
				// Import folder names are joined with "/" (mbox directories,
				// Maildir++, --folder); smtp accounts have JOURNAL, and earlier
				// fills "<prefix>/<IMAP folder>".
				slash := "/"
				delim = &slash
			}
			list = append(list, folderJSON{
				Name: f.Name, Messages: f.Messages, LastSyncedAt: f.LastSyncedAt, LastReconciledAt: f.LastReconciledAt,
				Delimiter: delim,
			})
		}
		folders[a.Name] = list
	}
	out := accountsResponse{Accounts: make([]accountJSON, 0, len(accounts)), Manage: s.syncer != nil && s.runner != nil, AlertAfter: s.alertAfter}
	if s.smtpListen != "" {
		out.SMTP = &smtpServerJSON{}
	}
	if sess := currentSession(r); sess != nil && sess.IsAdmin {
		failing, err := s.store.CountFailingByOwner(ctx, s.alertAfter)
		if err != nil {
			s.failStore(w, r, err)
			return
		}
		var others int64
		for owner, n := range failing {
			if owner != me {
				others += n
			}
		}
		out.OtherFailing = &others
	}
	now := s.currentTime()
	if sched := s.schedule(); sched.Sync > 0 {
		out.SyncInterval = sched.Sync.String()
	}
	if s.runner != nil {
		out.SyncIntervalMin = s.runner.Schedule.SyncMin.String()
	}
	if s.syncer != nil && s.syncer.ReconcileInterval > 0 {
		out.ReconcileInterval = s.syncer.ReconcileInterval.String()
	}
	for _, a := range accounts {
		aj := accountJSON{
			Name: a.Name, Kind: string(a.Kind), Enabled: a.Enabled, Removed: a.RemovedAt != nil,
			Host: a.Host, Port: a.Port, TLS: string(a.TLSMode), Username: a.Username,
			IncludedFolders: a.IncludedFolders, ExcludedFolders: a.ExcludedFolders,
			SyncInterval: archive.FormatOwnInterval(a.SyncInterval),
			Folders:      folders[a.Name], Sync: syncJSON{State: "idle", Health: store.HealthOK},
			GoneMessages: reconciled[a.ID].Gone, LastReconciledAt: reconciled[a.ID].LastReconciledAt,
		}
		if h := health[a.ID]; h != nil {
			aj.Sync.FailureStreak, aj.Sync.FailingSince = h.FailureStreak, h.FailingSince
			aj.Sync.Health = s.healthState(h, busy, now)
		}
		var last *store.LastRun
		if run, ok := runs[a.ID]; ok {
			last = &run
		}
		aj.Sync.SyncEvery, aj.Sync.NextAt = s.scheduleFields(archive.ScheduledAccount(a, last))
		if !a.Kind.HasServer() {
			aj.TLS = ""
		}
		aj.Addresses = addresses[a.ID]
		if aj.Addresses == nil {
			aj.Addresses = []string{}
		}
		if t, ok := received[a.ID]; ok {
			aj.LastReceivedAt = &t
		}
		aj.Fills = []*fillJSON{}
		for i := range fills[a.ID] {
			aj.Fills = append(aj.Fills, s.fillJSON(&fills[a.ID][i], fillHealth, busy, now))
		}
		if len(aj.Fills) > 0 {
			aj.Fill = aj.Fills[0]
		}
		if a.Kind == store.KindSMTP {
			first, err := s.store.FirstReceived(ctx, a.ID)
			if err != nil {
				s.failStore(w, r, err)
				return
			}
			if first != nil {
				aj.FirstReceived = &firstReceivedJSON{At: first.ReceivedAt, Subject: first.Subject}
			}
		}
		if aj.Folders == nil {
			aj.Folders = []folderJSON{}
		}
		if aj.IncludedFolders == nil {
			aj.IncludedFolders = []string{}
		}
		if aj.ExcludedFolders == nil {
			aj.ExcludedFolders = []string{}
		}
		switch {
		case running[a.ID]:
			aj.Sync.State = "running"
		case queued[a.ID]:
			aj.Sync.State = "queued"
		}
		if run, ok := runs[a.ID]; ok {
			aj.Sync.LastRun = &lastRunJSON{
				StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, Status: run.Status,
				Fetched: run.MessagesFetched, New: run.MessagesNew, Error: run.Error,
				ReconciledFolders: run.Reconcile.Folders, Gone: run.Reconcile.Gone,
				Back: run.Reconcile.Back, FlagsChanged: run.Reconcile.FlagsChanged,
			}
		}
		out.Accounts = append(out.Accounts, aj)
	}
	s.writeJSON(w, http.StatusOK, out)
}

// scheduleFields are syncEvery and nextAt of an account.
func (s *Server) scheduleFields(a archive.Scheduled) (*string, *time.Time) {
	sched := s.schedule()
	every := sched.EveryFor(a)
	if every == 0 {
		return nil, nil
	}
	e := every.String()
	if next, ok := sched.Next(a); ok {
		return &e, &next
	}
	return &e, nil
}

// accountInput is the body of POST and PATCH /api/accounts. In a PATCH,
// missing fields keep their value.
type accountInput struct {
	// Kind (POST only) is "imap" (the default) or "smtp".
	Kind     *string `json:"kind"`
	Name     *string `json:"name"`
	Host     *string `json:"host"`
	Port     *int    `json:"port"`
	TLS      *string `json:"tls"`
	Username *string `json:"username"`
	Password *string `json:"password"`
	Enabled  *bool   `json:"enabled"`
	// ExcludedFolders replaces the folder selection: everything except these.
	ExcludedFolders *[]string `json:"excludedFolders"`
	// SyncInterval is the account's own sync interval: "default",
	// "manual" or a duration of whole minutes (IMAP accounts only).
	SyncInterval *string `json:"syncInterval"`
	// ConfirmFolders (POST only) saves the account even if the server marks
	// folders that would be archived as trash or junk.
	ConfirmFolders bool `json:"confirmFolders"`
	// Addresses an smtp account receives for: in a POST at least one, in a
	// PATCH the complete new list. Admins only.
	Addresses *[]string `json:"addresses"`
}

// folderConfirmation is the 409 answer of POST /api/accounts when trash or
// junk folders would be archived and the client did not confirm that.
type folderConfirmation struct {
	Error               string   `json:"error"`
	SuggestedExclusions []string `json:"suggestedExclusions"`
}

// changedFields names the fields a change sets, never their values.
func (in *accountInput) changedFields() []string {
	var out []string
	for _, f := range []struct {
		name string
		set  bool
	}{
		{"name", in.Name != nil}, {"host", in.Host != nil}, {"port", in.Port != nil}, {"tls", in.TLS != nil},
		{"username", in.Username != nil}, {"password", in.Password != nil}, {"enabled", in.Enabled != nil},
		{"excluded_folders", in.ExcludedFolders != nil}, {"addresses", in.Addresses != nil},
	} {
		if f.set {
			out = append(out, f.name)
		}
	}
	return out
}

// auditAccountChange records what an account change sets: the field names,
// the new name, the new state and the new sync interval. Host, user name
// and password are only named, never recorded.
func auditAccountChange(e *store.AuditEntry, oldName string, newName *string, fields []string, enabled *bool, interval *store.OwnInterval) {
	if interval != nil {
		fields = append(fields, "sync_interval")
		e.Detail("sync_interval", archive.FormatOwnInterval(interval.D))
	}
	e.Detail("fields", fields)
	if newName != nil {
		e.Detail("name", map[string]any{"from": oldName, "to": *newName})
	}
	if enabled != nil {
		e.Detail("enabled", *enabled)
	}
}

// ownIntervalChange reads in.SyncInterval: nil when it is missing or equal
// to the stored value, which is kept even below a raised floor.
func (s *Server) ownIntervalChange(in *accountInput, stored *time.Duration) (*store.OwnInterval, error) {
	if in.SyncInterval == nil {
		return nil, nil
	}
	sched := s.schedule()
	own, err := archive.ParseOwnInterval(*in.SyncInterval)
	if err == nil && archive.SameOwnInterval(own, stored) {
		return nil, nil
	}
	if err == nil {
		err = sched.CheckFloor(own)
	}
	if err != nil {
		return nil, errors.New("syncInterval: " + sched.OwnIntervalRule())
	}
	return &store.OwnInterval{D: own}, nil
}

func (in *accountInput) changesConnection() bool {
	return in.Host != nil || in.Port != nil || in.TLS != nil || in.Username != nil || in.Password != nil
}

// maxAccountBody is far more than any valid account needs.
const maxAccountBody = 64 << 10

func (s *Server) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxAccountBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		s.fail(w, r, http.StatusBadRequest, "invalid request body: "+err.Error(), nil)
		return false
	}
	return true
}

// requireManage rejects write requests when the server cannot decrypt or
// encrypt passwords.
func (s *Server) requireManage(w http.ResponseWriter, r *http.Request) bool {
	if s.syncer == nil || s.runner == nil {
		s.fail(w, r, http.StatusServiceUnavailable, "account management needs MAIL_ARCHIVE_SECRET_KEY on the server", nil)
		return false
	}
	return true
}

// applyConnection copies connection fields from in to a and validates them.
func applyConnection(a *store.Account, in *accountInput) error {
	if in.Host != nil {
		a.Host = strings.TrimSpace(*in.Host)
	}
	if in.TLS != nil {
		a.TLSMode = store.TLSMode(*in.TLS)
	}
	if in.Username != nil {
		a.Username = strings.TrimSpace(*in.Username)
	}
	if in.Port != nil {
		a.Port = *in.Port
	}
	switch a.TLSMode {
	case store.TLSModeTLS, store.TLSModeSTARTTLS, store.TLSModeNone:
	default:
		return fmt.Errorf("tls must be tls, starttls or none")
	}
	if a.Port == 0 {
		a.Port = 993
		if a.TLSMode != store.TLSModeTLS {
			a.Port = 143
		}
	}
	switch {
	case a.Host == "" || strings.ContainsAny(a.Host, " /\t\r\n"):
		return errors.New("host must be a host name or IP address")
	case a.Port < 1 || a.Port > 65535:
		return errors.New("port must be between 1 and 65535")
	case a.Username == "":
		return errors.New("username is required")
	}
	return nil
}

// adminOnlyAddresses is the 403 answer for smtp addresses: an address
// decides who receives the journal copies of the whole mail server.
const adminOnlyAddresses = "only admins can set the addresses of smtp accounts"

// normalizeAddresses checks a list of smtp addresses as the store does, so
// a bad one answers 400 before anything is written.
func normalizeAddresses(list []string) ([]string, error) {
	if len(list) == 0 {
		return nil, store.ErrNoAddresses
	}
	out := make([]string, 0, len(list))
	for _, addr := range list {
		a, err := store.NormalizeSMTPAddress(addr)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func (s *Server) handleCreateAccount(w http.ResponseWriter, r *http.Request) {
	if !s.requireManage(w, r) {
		return
	}
	var in accountInput
	if !s.decode(w, r, &in) {
		return
	}
	switch {
	case in.Kind != nil && *in.Kind == string(store.KindSMTP):
		s.createSMTPAccount(w, r, &in)
		return
	case in.Kind != nil && *in.Kind != string(store.KindIMAP):
		s.fail(w, r, http.StatusBadRequest, `kind must be "imap" or "smtp"`, nil)
		return
	case in.Addresses != nil:
		s.fail(w, r, http.StatusBadRequest, store.ErrNotSMTP.Error(), nil)
		return
	}
	owner := userID(r)
	a := &store.Account{Enabled: true, TLSMode: store.TLSModeTLS, OwnerID: &owner}
	if in.Name != nil {
		a.Name = *in.Name
	}
	if err := archive.ValidateAccountName(a.Name); err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	}
	if err := applyConnection(a, &in); err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	}
	// Before the login check, so a bad value costs no IMAP login.
	if own, err := s.ownIntervalChange(&in, nil); err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	} else if own != nil {
		a.SyncInterval = own.D
	}
	if in.Password == nil || *in.Password == "" {
		s.fail(w, r, http.StatusBadRequest, "password is required", nil)
		return
	}
	if in.ExcludedFolders != nil {
		a.ExcludedFolders = *in.ExcludedFolders
	}
	if in.Enabled != nil {
		a.Enabled = *in.Enabled
	}
	if _, err := s.store.GetOwnedAccount(r.Context(), userID(r), a.Name); err == nil {
		s.fail(w, r, http.StatusConflict, fmt.Sprintf("an account named %q already exists (removed accounts keep their name)", a.Name), nil)
		return
	}
	folders, err := s.syncer.CheckLogin(r.Context(), a, *in.Password)
	if err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, "login failed: "+err.Error(), nil)
		return
	}
	// Trash and spam are archived like any folder, but only after the user
	// confirmed it: once the first sync ran, excluding them comes too late.
	if suggest := archive.SuggestExclusions(folders, a.IncludedFolders, a.ExcludedFolders); len(suggest) > 0 && !in.ConfirmFolders {
		s.writeJSON(w, http.StatusConflict, folderConfirmation{
			Error:               "the server marks these folders as trash or spam; confirm whether to archive them",
			SuggestedExclusions: suggest,
		})
		return
	}
	seal := func(id int64) ([]byte, error) { return archive.SealPassword(s.syncer.Sealer, id, *in.Password) }
	e := s.auditEntry(r, "account.add")
	e.SetTarget(store.TargetAccount, "", a.Name) // the ID is set when it exists
	e.Detail("kind", string(store.KindIMAP))
	if err := s.store.CreateAccountSealed(withAudit(r, e), a, seal); err != nil {
		if errors.Is(err, store.ErrConflict) {
			s.fail(w, r, http.StatusConflict, fmt.Sprintf("an account named %q already exists", a.Name), nil)
			return
		}
		s.failStore(w, r, err)
		return
	}
	s.log.Info("account added", "account", a.Name)
	if a.Enabled && s.runner != nil {
		s.runner.Enqueue(a.ID)
	}
	s.writeJSON(w, http.StatusCreated, map[string]string{"name": a.Name})
}

// createSMTPAccount handles POST /api/accounts with kind "smtp": a name and
// addresses, no server and no login check. Admins only.
func (s *Server) createSMTPAccount(w http.ResponseWriter, r *http.Request, in *accountInput) {
	if !s.requireAdminFor(w, r, "only admins can add smtp accounts") {
		return
	}
	if in.changesConnection() || in.ExcludedFolders != nil || in.SyncInterval != nil || in.ConfirmFolders {
		s.fail(w, r, http.StatusBadRequest, "an smtp account has no server", nil)
		return
	}
	owner := userID(r)
	a := &store.Account{Enabled: true, OwnerID: &owner}
	if in.Name != nil {
		a.Name = *in.Name
	}
	if err := archive.ValidateAccountName(a.Name); err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	}
	if in.Addresses == nil {
		s.fail(w, r, http.StatusBadRequest, store.ErrNoAddresses.Error(), nil)
		return
	}
	addrs, err := normalizeAddresses(*in.Addresses)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	}
	if in.Enabled != nil {
		a.Enabled = *in.Enabled
	}
	e := s.auditEntry(r, "account.add")
	e.SetTarget(store.TargetAccount, "", a.Name) // the ID is set when it exists
	e.Detail("kind", string(store.KindSMTP))
	e.Detail("addresses", addrs)
	err = s.store.CreateSMTPAccount(withAudit(r, e), a, addrs)
	switch {
	case errors.Is(err, store.ErrConflict):
		s.fail(w, r, http.StatusConflict, fmt.Sprintf("an account named %q already exists (removed accounts keep their name)", a.Name), nil)
		return
	case errors.Is(err, store.ErrAddressTaken):
		s.fail(w, r, http.StatusConflict, err.Error(), nil)
		return
	case err != nil:
		s.failStore(w, r, err)
		return
	}
	s.log.Info("smtp account added", "account", a.Name, "addresses", addrs)
	s.writeJSON(w, http.StatusCreated, map[string]string{"name": a.Name})
}

// pathAccount loads the account named in the URL. Removed accounts are
// rejected: they cannot be changed or synced.
func (s *Server) pathAccount(w http.ResponseWriter, r *http.Request) (*store.Account, bool) {
	// Other users' accounts are not found, like names that do not exist.
	a, err := s.store.GetOwnedAccount(r.Context(), userID(r), r.PathValue("name"))
	if err != nil {
		s.failStore(w, r, err)
		return nil, false
	}
	if a.RemovedAt != nil {
		s.fail(w, r, http.StatusConflict, "account was removed; its archived mail is kept, but it cannot be changed or synced", nil)
		return nil, false
	}
	return a, true
}

// pathIMAPAccount is pathAccount for requests that need a server.
func (s *Server) pathIMAPAccount(w http.ResponseWriter, r *http.Request) (*store.Account, bool) {
	a, ok := s.pathAccount(w, r)
	if ok && !a.Kind.HasServer() {
		s.fail(w, r, http.StatusConflict, store.ErrNoServer.Error(), nil)
		return nil, false
	}
	return a, ok
}

func (s *Server) handleUpdateAccount(w http.ResponseWriter, r *http.Request) {
	if !s.requireManage(w, r) {
		return
	}
	var in accountInput
	if !s.decode(w, r, &in) {
		return
	}
	if in.Name != nil {
		if err := archive.ValidateAccountName(*in.Name); err != nil {
			s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
			return
		}
	}
	if in.Kind != nil {
		s.fail(w, r, http.StatusBadRequest, "the kind of an account cannot change", nil)
		return
	}
	var addrs []string
	if in.Addresses != nil {
		if !s.requireAdminFor(w, r, adminOnlyAddresses) {
			return
		}
		var err error
		if addrs, err = normalizeAddresses(*in.Addresses); err != nil {
			s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
			return
		}
	}
	a, ok := s.pathAccount(w, r)
	if !ok {
		return
	}
	if in.Addresses != nil && a.Kind != store.KindSMTP {
		s.fail(w, r, http.StatusConflict, store.ErrNotSMTP.Error(), nil)
		return
	}
	if !a.Kind.HasServer() && (in.changesConnection() || in.ExcludedFolders != nil || in.SyncInterval != nil) ||
		a.Kind == store.KindImport && in.Enabled != nil {
		s.fail(w, r, http.StatusConflict, store.ErrNoServer.Error(), nil)
		return
	}
	ctx := r.Context()
	own, err := s.ownIntervalChange(&in, a.SyncInterval)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	}

	// Everything that needs the network or the key happens before the
	// transaction: the login check and encrypting the password. The change
	// is then saved at once, and only if the account still is as read here
	// (same owner and version); otherwise nothing is saved.
	var change store.AccountChange
	if in.changesConnection() {
		conn := *a
		if err := applyConnection(&conn, &in); err != nil {
			s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
			return
		}
		var password string
		if in.Password != nil {
			if *in.Password == "" {
				s.fail(w, r, http.StatusBadRequest, "password must not be empty", nil)
				return
			}
			password = *in.Password
		} else {
			pw, err := archive.OpenPassword(s.syncer.Sealer, a)
			if err != nil {
				s.fail(w, r, http.StatusInternalServerError, "internal error", err)
				return
			}
			password = pw
		}
		if _, err := s.syncer.CheckLogin(ctx, &conn, password); err != nil {
			s.fail(w, r, http.StatusUnprocessableEntity, "login failed: "+err.Error(), nil)
			return
		}
		enc, err := archive.SealPassword(s.syncer.Sealer, a.ID, password)
		if err != nil {
			s.fail(w, r, http.StatusInternalServerError, "internal error", err)
			return
		}
		change.Connection = &store.Connection{Host: conn.Host, Port: conn.Port, TLSMode: conn.TLSMode, Username: conn.Username, PasswordEnc: enc}
	}
	if in.Name != nil && *in.Name != a.Name {
		change.Name = in.Name
	}
	if in.ExcludedFolders != nil {
		change.Folders = &store.FolderFilters{Excluded: *in.ExcludedFolders}
	}
	change.Enabled = in.Enabled
	change.SyncInterval = own
	if in.Addresses != nil {
		change.SMTPAddresses = &addrs
	}

	e := s.auditEntry(r, "account.change")
	auditAccount(e, a)
	auditAccountChange(e, a.Name, change.Name, in.changedFields(), in.Enabled, change.SyncInterval)
	err = s.store.UpdateAccount(withAudit(r, e), a.Ref(), change)
	switch {
	case errors.Is(err, store.ErrConflict):
		s.fail(w, r, http.StatusConflict, fmt.Sprintf("an account named %q already exists (removed accounts keep their name)", *in.Name), nil)
		return
	case errors.Is(err, store.ErrStale), errors.Is(err, store.ErrNoServer), errors.Is(err, store.ErrNotSMTP),
		errors.Is(err, store.ErrAddressTaken):
		s.fail(w, r, http.StatusConflict, err.Error(), nil)
		return
	case err != nil:
		s.failStore(w, r, err)
		return
	}
	if change.Name != nil {
		s.log.Info("account renamed", "from", a.Name, "to", *change.Name)
		a.Name = *change.Name
	}
	s.log.Info("account updated", "account", a.Name)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	if !s.requireManage(w, r) {
		return
	}
	a, ok := s.pathAccount(w, r)
	if !ok {
		return
	}
	// Deleting must not overlap a sync that writes folders and messages for
	// the account: hold its sync lock for the whole operation.
	unlock, ok, err := s.store.TryLockSync(r.Context(), a.ID)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	if !ok {
		s.fail(w, r, http.StatusConflict, "the account is being synced or filled; try again when that has finished", nil)
		return
	}
	defer unlock()
	e := s.auditEntry(r, "account.remove")
	auditAccount(e, a)
	res, err := s.store.DeleteOrRemoveAccount(withAudit(r, e), a.Ref())
	if errors.Is(err, store.ErrStale) {
		s.fail(w, r, http.StatusConflict, err.Error(), nil)
		return
	}
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	s.log.Info("account removed", "account", a.Name, "result", res)
	s.writeJSON(w, http.StatusOK, map[string]string{"result": string(res)})
}

type serverFolderJSON struct {
	Name       string `json:"name"`
	SpecialUse string `json:"specialUse,omitempty"`
	Selected   bool   `json:"selected"`
}

// handleServerFolders lists the account's folders live from the IMAP server,
// with the current selection.
func (s *Server) handleServerFolders(w http.ResponseWriter, r *http.Request) {
	if !s.requireManage(w, r) {
		return
	}
	a, ok := s.pathIMAPAccount(w, r)
	if !ok {
		return
	}
	list, err := s.syncer.ListFolders(r.Context(), a)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "cannot list folders: "+err.Error(), nil)
		return
	}
	out := make([]serverFolderJSON, 0, len(list))
	for _, f := range list {
		out = append(out, serverFolderJSON{
			Name: f.Name, SpecialUse: f.SpecialUse(),
			Selected: archive.FolderSelected(f.Name, a.IncludedFolders, a.ExcludedFolders),
		})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"folders": out})
}

func (s *Server) handleSyncAccount(w http.ResponseWriter, r *http.Request) {
	if !s.requireManage(w, r) {
		return
	}
	a, ok := s.pathIMAPAccount(w, r)
	if !ok {
		return
	}
	var in struct {
		// Reconcile also compares every folder with the server, except
		// folders compared in the last few minutes.
		Reconcile bool `json:"reconcile"`
	}
	if r.ContentLength != 0 {
		dec := json.NewDecoder(io.LimitReader(r.Body, maxAccountBody))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil && !errors.Is(err, io.EOF) {
			s.fail(w, r, http.StatusBadRequest, "invalid request body: "+err.Error(), nil)
			return
		}
	}
	// Like `mail-archive sync --account`, this also syncs a disabled account.
	opts := archive.SyncOptions{}
	if in.Reconcile {
		opts = archive.SyncOptions{Reconcile: true, ReconcileMinAge: reconcileFloor}
	}
	e := s.auditEntry(r, "sync.start")
	auditAccount(e, a)
	e.Detail("reconcile", in.Reconcile)
	if !s.audit(w, r, e) {
		return
	}
	s.runner.EnqueueWith(opts, a.ID)
	s.writeJSON(w, http.StatusAccepted, map[string]int{"queued": 1})
}

// reconcileFloor: the reconcile button skips folders compared this
// recently, so that repeated clicks do not hammer the mail server.
const reconcileFloor = 5 * time.Minute

func (s *Server) handleSyncAll(w http.ResponseWriter, r *http.Request) {
	if !s.requireManage(w, r) {
		return
	}
	accounts, err := s.store.ListOwnedAccounts(r.Context(), userID(r))
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	var ids []int64
	for _, a := range accounts {
		if a.Enabled && a.RemovedAt == nil && a.Kind == store.KindIMAP {
			ids = append(ids, a.ID)
		}
	}
	e := s.auditEntry(r, "sync.start")
	e.Detail("accounts", len(ids))
	if !s.audit(w, r, e) {
		return
	}
	s.runner.Enqueue(ids...)
	s.writeJSON(w, http.StatusAccepted, map[string]int{"queued": len(ids)})
}
