package web

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/store"
)

// Fills: a one-time copy of an IMAP mailbox into an smtp account (#89).
// The fill runner of the web server runs them, one at a time. The password
// is only written, and only kept while the fill runs.

type fillFolderJSON struct {
	// Source is the IMAP folder, Folder the archive folder it fills
	// ("<prefix>/<source>").
	Source string `json:"source"`
	Folder string `json:"folder"`
	Done   bool   `json:"done"`
	Error  string `json:"error"`
	// What the fill did with the folder's messages.
	Scanned     int `json:"scanned"`
	Imported    int `json:"imported"`
	New         int `json:"new"`
	AfterCutoff int `json:"afterCutoff"`
	Journaled   int `json:"journaled"`
	Present     int `json:"present"`
	Deleted     int `json:"deleted"`
}

type fillJSON struct {
	ID int64 `json:"id"`
	// Status is "running", "ok", "partial", "failed" or "cancelled".
	Status          string   `json:"status"`
	Host            string   `json:"host"`
	Port            int      `json:"port"`
	TLS             string   `json:"tls"`
	Username        string   `json:"username"`
	IncludedFolders []string `json:"includedFolders"`
	ExcludedFolders []string `json:"excludedFolders"`
	FolderPrefix    string   `json:"folderPrefix"`
	// Before is the cut-off: messages from an hour after it on are left to
	// journaling.
	Before     time.Time        `json:"before"`
	Error      string           `json:"error"`
	CreatedAt  time.Time        `json:"createdAt"`
	FinishedAt *time.Time       `json:"finishedAt"`
	Folders    []fillFolderJSON `json:"folders"`
}

// firstReceivedJSON is the first message an smtp account received that is
// still stored: a sanity check for a fill's cut-off.
type firstReceivedJSON struct {
	At      time.Time `json:"at"`
	Subject string    `json:"subject"`
}

func newFillJSON(f *store.Fill) *fillJSON {
	out := &fillJSON{
		ID: f.ID, Status: f.Status, Host: f.Host, Port: f.Port, TLS: string(f.TLSMode), Username: f.Username,
		IncludedFolders: nonNilList(f.Included), ExcludedFolders: nonNilList(f.Excluded), FolderPrefix: f.FolderPrefix,
		Before: f.Cutoff, Error: f.Error, CreatedAt: f.CreatedAt, FinishedAt: f.FinishedAt,
		Folders: make([]fillFolderJSON, 0, len(f.Folders)),
	}
	for _, ff := range f.Folders {
		c := ff.Counts
		out.Folders = append(out.Folders, fillFolderJSON{
			Source: ff.SourceName, Folder: ff.FolderName, Done: ff.Done, Error: ff.Error,
			Scanned: c.Scanned, Imported: c.Imported, New: c.New, AfterCutoff: c.AfterCutoff,
			Journaled: c.Journaled, Present: c.Present, Deleted: c.Deleted,
		})
	}
	return out
}

func nonNilList(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

// fillServerInput is the IMAP server of a fill, for its folder list.
type fillServerInput struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	TLS      string `json:"tls"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// fillInput is the body of POST /api/accounts/{name}/fills.
type fillInput struct {
	Host string `json:"host"`
	// Port defaults to 993 with tls, else 143; TLS to "tls".
	Port     int    `json:"port"`
	TLS      string `json:"tls"`
	Username string `json:"username"`
	Password string `json:"password"`
	// IncludedFolders limits the fill to these folders (empty: all);
	// ExcludedFolders skips these.
	IncludedFolders []string `json:"includedFolders"`
	ExcludedFolders []string `json:"excludedFolders"`
	// FolderPrefix names the archive folders ("<prefix>/<IMAP folder>");
	// default: the user name.
	FolderPrefix string `json:"folderPrefix"`
	// Before is the cut-off; default: now.
	Before *time.Time `json:"before"`
	// ConfirmFolders copies trash and spam folders too, if the filters
	// select them.
	ConfirmFolders bool `json:"confirmFolders"`
}

// fillSource checks the server of a fill and returns it as an account,
// for the login check.
func fillSource(host string, port int, tls, username, password string) (*store.Account, error) {
	src := &store.Account{TLSMode: store.TLSModeTLS}
	in := &accountInput{Host: &host, Username: &username}
	if port != 0 {
		in.Port = &port
	}
	if tls != "" {
		in.TLS = &tls
	}
	if err := applyConnection(src, in); err != nil {
		return nil, err
	}
	if password == "" {
		return nil, errors.New("password is required")
	}
	return src, nil
}

// requireFills is requireManage for fills, which also need the fill runner.
func (s *Server) requireFills(w http.ResponseWriter, r *http.Request) bool {
	if !s.requireManage(w, r) {
		return false
	}
	if s.fills == nil {
		s.fail(w, r, http.StatusServiceUnavailable, "fills need MAIL_ARCHIVE_SECRET_KEY on the server", nil)
		return false
	}
	return true
}

// pathSMTPAccount is pathAccount for requests about fills.
func (s *Server) pathSMTPAccount(w http.ResponseWriter, r *http.Request) (*store.Account, bool) {
	a, ok := s.pathAccount(w, r)
	if ok && a.Kind != store.KindSMTP {
		s.fail(w, r, http.StatusConflict, store.ErrNotSMTP.Error(), nil)
		return nil, false
	}
	return a, ok
}

// pathFill loads the fill named in the URL; fills of other accounts are
// not found.
func (s *Server) pathFill(w http.ResponseWriter, r *http.Request, a *store.Account) (*store.Fill, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.fail(w, r, http.StatusNotFound, "not found", nil)
		return nil, false
	}
	f, err := s.store.GetFill(r.Context(), id)
	if err == nil && f.AccountID != a.ID {
		err = store.ErrNotFound
	}
	if err != nil {
		s.failStore(w, r, err)
		return nil, false
	}
	return f, true
}

// handleFillFolders lists the folders of a fill's IMAP server, before the
// fill exists, with the trash and spam folders to confirm.
func (s *Server) handleFillFolders(w http.ResponseWriter, r *http.Request) {
	if !s.requireFills(w, r) {
		return
	}
	var in fillServerInput
	if !s.decode(w, r, &in) {
		return
	}
	if _, ok := s.pathSMTPAccount(w, r); !ok {
		return
	}
	src, err := fillSource(in.Host, in.Port, in.TLS, in.Username, in.Password)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	}
	folders, err := s.syncer.CheckLogin(r.Context(), src, in.Password)
	if err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, "login failed: "+err.Error(), nil)
		return
	}
	out := make([]serverFolderJSON, 0, len(folders))
	for _, f := range folders {
		out = append(out, serverFolderJSON{Name: f.Name, SpecialUse: f.SpecialUse(), Selected: true})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"folders": out, "suggestedExclusions": nonNilList(archive.SuggestExclusions(folders, nil, nil)),
	})
}

// handleStartFill starts a fill: it checks the login, saves the fill with
// its sealed password and queues it.
func (s *Server) handleStartFill(w http.ResponseWriter, r *http.Request) {
	if !s.requireFills(w, r) {
		return
	}
	var in fillInput
	if !s.decode(w, r, &in) {
		return
	}
	a, ok := s.pathSMTPAccount(w, r)
	if !ok {
		return
	}
	src, err := fillSource(in.Host, in.Port, in.TLS, in.Username, in.Password)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	}
	prefix := in.FolderPrefix
	if prefix == "" {
		prefix = archive.DefaultFillPrefix(src.Username)
	}
	if err := store.ValidFillPrefix(prefix); err != nil {
		s.fail(w, r, http.StatusBadRequest, "folderPrefix: "+err.Error(), nil)
		return
	}
	cutoff := s.currentTime()
	if in.Before != nil {
		cutoff = *in.Before
	}
	folders, err := s.syncer.CheckLogin(r.Context(), src, in.Password)
	if err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, "login failed: "+err.Error(), nil)
		return
	}
	// Trash and spam only after the user said so: once copied, excluding
	// them comes too late.
	if suggest := archive.SuggestExclusions(folders, in.IncludedFolders, in.ExcludedFolders); len(suggest) > 0 && !in.ConfirmFolders {
		s.writeJSON(w, http.StatusConflict, folderConfirmation{
			Error:               "the server marks these folders as trash or spam; confirm whether to copy them",
			SuggestedExclusions: suggest,
		})
		return
	}
	me := userID(r)
	fill := &store.Fill{
		AccountID: a.ID, Host: src.Host, Port: src.Port, TLSMode: src.TLSMode, Username: src.Username,
		Included: in.IncludedFolders, Excluded: in.ExcludedFolders, FolderPrefix: prefix, Cutoff: cutoff, CreatedBy: &me,
	}
	e := s.auditEntry(r, "data.fill")
	auditAccount(e, a)
	e.Detail("host", src.Host)
	e.Detail("username", src.Username)
	e.Detail("before", cutoff.UTC().Format(time.RFC3339))
	e.Detail("prefix", prefix)
	e.Detail("folders", map[string]any{"included": nonNilList(in.IncludedFolders), "excluded": nonNilList(in.ExcludedFolders)})
	seal := func(id int64) ([]byte, error) { return archive.SealFillPassword(s.syncer.Sealer, id, in.Password) }
	err = s.store.CreateFill(withAudit(r, e), fill, seal)
	switch {
	case errors.Is(err, store.ErrFillRunning):
		s.fail(w, r, http.StatusConflict, err.Error(), nil)
		return
	case errors.Is(err, store.ErrConflict):
		s.fail(w, r, http.StatusConflict, fmt.Sprintf("%v; choose another folderPrefix", err), nil)
		return
	case errors.Is(err, store.ErrNotSMTP):
		s.fail(w, r, http.StatusConflict, err.Error(), nil)
		return
	case err != nil:
		s.failStore(w, r, err)
		return
	}
	s.log.Info("fill started", "account", a.Name, "fill", fill.ID)
	s.fills.Enqueue(fill.ID, a.ID)
	s.writeJSON(w, http.StatusAccepted, map[string]int64{"id": fill.ID})
}

// handleResumeFill continues a fill that failed, partly failed or was
// cancelled, with the password given again.
func (s *Server) handleResumeFill(w http.ResponseWriter, r *http.Request) {
	if !s.requireFills(w, r) {
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	a, ok := s.pathSMTPAccount(w, r)
	if !ok {
		return
	}
	fill, ok := s.pathFill(w, r, a)
	if !ok {
		return
	}
	switch fill.Status {
	case store.FillRunning:
		s.fail(w, r, http.StatusConflict, "the fill is running", nil)
		return
	case store.FillOK:
		s.fail(w, r, http.StatusConflict, store.ErrFillNotResumable.Error(), nil)
		return
	}
	if in.Password == "" {
		s.fail(w, r, http.StatusBadRequest, "password is required", nil)
		return
	}
	src := &store.Account{Host: fill.Host, Port: fill.Port, TLSMode: fill.TLSMode, Username: fill.Username}
	if _, err := s.syncer.CheckLogin(r.Context(), src, in.Password); err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, "login failed: "+err.Error(), nil)
		return
	}
	e := s.auditEntry(r, "data.fill_resume")
	auditAccount(e, a)
	seal := func(id int64) ([]byte, error) { return archive.SealFillPassword(s.syncer.Sealer, id, in.Password) }
	err := s.store.ResumeFill(withAudit(r, e), fill.ID, seal)
	switch {
	case errors.Is(err, store.ErrFillRunning), errors.Is(err, store.ErrFillNotResumable):
		s.fail(w, r, http.StatusConflict, err.Error(), nil)
		return
	case err != nil:
		s.failStore(w, r, err)
		return
	}
	s.log.Info("fill resumed", "account", a.Name, "fill", fill.ID)
	s.fills.Enqueue(fill.ID, a.ID)
	s.writeJSON(w, http.StatusAccepted, map[string]int64{"id": fill.ID})
}

// handleCancelFill stops a running fill and wipes its password. The fill
// stops after its current batch; what it copied stays.
func (s *Server) handleCancelFill(w http.ResponseWriter, r *http.Request) {
	if !s.requireFills(w, r) {
		return
	}
	a, ok := s.pathSMTPAccount(w, r)
	if !ok {
		return
	}
	fill, ok := s.pathFill(w, r, a)
	if !ok {
		return
	}
	e := s.auditEntry(r, "data.fill_cancel")
	auditAccount(e, a)
	err := s.store.CancelFill(withAudit(r, e), fill.ID)
	switch {
	case errors.Is(err, store.ErrFillNotRunning):
		s.fail(w, r, http.StatusConflict, err.Error(), nil)
		return
	case err != nil:
		s.failStore(w, r, err)
		return
	}
	s.log.Info("fill cancelled", "account", a.Name, "fill", fill.ID)
	w.WriteHeader(http.StatusNoContent)
}
