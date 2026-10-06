package web

import (
	"context"
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
}

type syncJSON struct {
	// State is "idle", "queued" or "running" (also when the CLI syncs).
	State   string       `json:"state"`
	LastRun *lastRunJSON `json:"lastRun"`
}

type accountJSON struct {
	Name            string       `json:"name"`
	Enabled         bool         `json:"enabled"`
	Removed         bool         `json:"removed"`
	Host            string       `json:"host"`
	Port            int          `json:"port"`
	TLS             string       `json:"tls"`
	Username        string       `json:"username"`
	IncludedFolders []string     `json:"includedFolders"`
	ExcludedFolders []string     `json:"excludedFolders"`
	Folders         []folderJSON `json:"folders"`
	Sync            syncJSON     `json:"sync"`
}

type folderJSON struct {
	Name         string     `json:"name"`
	Messages     int64      `json:"messages"`
	LastSyncedAt *time.Time `json:"lastSyncedAt"`
}

type accountsResponse struct {
	Accounts []accountJSON `json:"accounts"`
	// Manage is false when the server has no secret key: accounts can then
	// be browsed but not added, changed or synced.
	Manage bool `json:"manage"`
	// SyncInterval of the schedule ("6h0m0s"), empty if off.
	SyncInterval string `json:"syncInterval"`
}

func (s *Server) handleAccounts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	accounts, err := s.store.ListAccounts(ctx)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	counts, err := s.store.ListAccountFolders(ctx)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	runs, err := s.store.LastRuns(ctx)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	running, err := s.store.SyncingAccounts(ctx)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	var queued map[int64]bool
	if s.runner != nil {
		queued = s.runner.Queued()
	}
	folders := make(map[string][]folderJSON, len(counts))
	for _, a := range counts {
		list := make([]folderJSON, 0, len(a.Folders))
		for _, f := range a.Folders {
			list = append(list, folderJSON{Name: f.Name, Messages: f.Messages, LastSyncedAt: f.LastSyncedAt})
		}
		folders[a.Name] = list
	}
	out := accountsResponse{Accounts: make([]accountJSON, 0, len(accounts)), Manage: s.syncer != nil && s.runner != nil}
	if s.runner != nil && s.runner.Interval > 0 {
		out.SyncInterval = s.runner.Interval.String()
	}
	for _, a := range accounts {
		aj := accountJSON{
			Name: a.Name, Enabled: a.Enabled, Removed: a.RemovedAt != nil,
			Host: a.Host, Port: a.Port, TLS: string(a.TLSMode), Username: a.Username,
			IncludedFolders: a.IncludedFolders, ExcludedFolders: a.ExcludedFolders,
			Folders: folders[a.Name], Sync: syncJSON{State: "idle"},
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
			}
		}
		out.Accounts = append(out.Accounts, aj)
	}
	s.writeJSON(w, http.StatusOK, out)
}

// accountInput is the body of POST and PATCH /api/accounts. In a PATCH,
// missing fields keep their value.
type accountInput struct {
	Name     *string `json:"name"`
	Host     *string `json:"host"`
	Port     *int    `json:"port"`
	TLS      *string `json:"tls"`
	Username *string `json:"username"`
	Password *string `json:"password"`
	Enabled  *bool   `json:"enabled"`
	// ExcludedFolders replaces the folder selection: everything except these.
	ExcludedFolders *[]string `json:"excludedFolders"`
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

// checkAndSeal verifies the login and encrypts the password for storage.
func (s *Server) checkAndSeal(ctx context.Context, a *store.Account, password string) (int, string) {
	if _, err := s.syncer.CheckLogin(ctx, a, password); err != nil {
		return http.StatusUnprocessableEntity, "login failed: " + err.Error()
	}
	enc, err := s.syncer.Sealer.Seal([]byte(password), archive.PasswordContext(a.Name))
	if err != nil {
		return http.StatusInternalServerError, "internal error"
	}
	a.PasswordEnc = enc
	return 0, ""
}

func (s *Server) handleCreateAccount(w http.ResponseWriter, r *http.Request) {
	if !s.requireManage(w, r) {
		return
	}
	var in accountInput
	if !s.decode(w, r, &in) {
		return
	}
	a := &store.Account{Enabled: true, TLSMode: store.TLSModeTLS}
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
	if _, err := s.store.GetAccountByName(r.Context(), a.Name); err == nil {
		s.fail(w, r, http.StatusConflict, fmt.Sprintf("an account named %q already exists (removed accounts keep their name)", a.Name), nil)
		return
	}
	if status, msg := s.checkAndSeal(r.Context(), a, *in.Password); status != 0 {
		s.fail(w, r, status, msg, nil)
		return
	}
	if err := s.store.CreateAccount(r.Context(), a); err != nil {
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

// pathAccount loads the account named in the URL. Removed accounts are
// rejected: they cannot be changed or synced.
func (s *Server) pathAccount(w http.ResponseWriter, r *http.Request) (*store.Account, bool) {
	a, err := s.store.GetAccountByName(r.Context(), r.PathValue("name"))
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
	a, ok := s.pathAccount(w, r)
	if !ok {
		return
	}
	ctx := r.Context()

	// Check a new connection before changing anything, so a failed login
	// leaves the account as it was (also when it is renamed in the same
	// request).
	var password string
	if in.changesConnection() {
		if err := applyConnection(a, &in); err != nil {
			s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
			return
		}
		if in.Password != nil {
			if *in.Password == "" {
				s.fail(w, r, http.StatusBadRequest, "password must not be empty", nil)
				return
			}
			password = *in.Password
		} else {
			pw, err := s.syncer.Sealer.Open(a.PasswordEnc, archive.PasswordContext(a.Name))
			if err != nil {
				s.fail(w, r, http.StatusInternalServerError, "internal error", err)
				return
			}
			password = string(pw)
		}
		if _, err := s.syncer.CheckLogin(ctx, a, password); err != nil {
			s.fail(w, r, http.StatusUnprocessableEntity, "login failed: "+err.Error(), nil)
			return
		}
	}

	if in.Name != nil && *in.Name != a.Name {
		oldName := a.Name
		err := archive.RenameAccount(ctx, s.store, s.syncer.Sealer, a, *in.Name)
		switch {
		case errors.Is(err, archive.ErrSyncRunning):
			s.fail(w, r, http.StatusConflict, "the account is being synced; try again when the sync has finished", nil)
			return
		case errors.Is(err, store.ErrConflict):
			s.fail(w, r, http.StatusConflict, fmt.Sprintf("an account named %q already exists (removed accounts keep their name)", *in.Name), nil)
			return
		case errors.Is(err, store.ErrNotFound):
			s.failStore(w, r, err)
			return
		case err != nil:
			s.fail(w, r, http.StatusInternalServerError, "internal error", err)
			return
		}
		s.log.Info("account renamed", "from", oldName, "to", a.Name)
	}

	if in.changesConnection() {
		enc, err := s.syncer.Sealer.Seal([]byte(password), archive.PasswordContext(a.Name))
		if err != nil {
			s.fail(w, r, http.StatusInternalServerError, "internal error", err)
			return
		}
		a.PasswordEnc = enc
		if err := s.store.UpdateConnection(ctx, a); err != nil {
			s.failStore(w, r, err)
			return
		}
	}
	if in.ExcludedFolders != nil {
		if err := s.store.SetFolderFilters(ctx, a.ID, nil, *in.ExcludedFolders); err != nil {
			s.failStore(w, r, err)
			return
		}
	}
	if in.Enabled != nil {
		if err := s.store.SetAccountEnabled(ctx, a.ID, *in.Enabled); err != nil {
			s.failStore(w, r, err)
			return
		}
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
	running, err := s.store.SyncingAccounts(r.Context())
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	if running[a.ID] {
		s.fail(w, r, http.StatusConflict, "the account is being synced; try again when the sync has finished", nil)
		return
	}
	res, err := s.store.DeleteOrRemoveAccount(r.Context(), a.ID)
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
	a, ok := s.pathAccount(w, r)
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
	a, ok := s.pathAccount(w, r)
	if !ok {
		return
	}
	// Like `mail-archive sync --account`, this also syncs a disabled account.
	s.runner.Enqueue(a.ID)
	s.writeJSON(w, http.StatusAccepted, map[string]int{"queued": 1})
}

func (s *Server) handleSyncAll(w http.ResponseWriter, r *http.Request) {
	if !s.requireManage(w, r) {
		return
	}
	accounts, err := s.store.ListAccounts(r.Context())
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	var ids []int64
	for _, a := range accounts {
		if a.Enabled && a.RemovedAt == nil {
			ids = append(ids, a.ID)
		}
	}
	s.runner.Enqueue(ids...)
	s.writeJSON(w, http.StatusAccepted, map[string]int{"queued": len(ids)})
}
