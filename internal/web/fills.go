package web

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/store"
)

// Fills: a copy of an IMAP mailbox into an smtp account (#89), into its
// journal folder, once or on a schedule (a recurring fill, #112). The fill
// runner of the web server runs them, one at a time. The password is only
// written, and only kept while the fill runs or keeps running.

type fillFolderJSON struct {
	// Source is the IMAP folder, Folder the archive folder it fills:
	// JOURNAL, or "<prefix>/<source>" for a fill from before #112.
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
	// Waiting: recurring fills only, messages younger than two hours at
	// the last run.
	Waiting int `json:"waiting"`
}

// fillHealthJSON is the health of an active recurring fill, like an
// account's sync health.
type fillHealthJSON struct {
	// State is "ok", "failing" or "stale".
	State         string     `json:"state"`
	FailureStreak int        `json:"failureStreak"`
	FailingSince  *time.Time `json:"failingSince"`
	LastSuccessAt *time.Time `json:"lastSuccessAt"`
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
	// FolderPrefix is set only for fills from before #112, which wrote
	// into "<prefix>/<IMAP folder>"; empty for fills into JOURNAL.
	FolderPrefix string `json:"folderPrefix"`
	// Before is the cut-off: messages from an hour after it on are left to
	// journaling. Null for a fill that was recurring from the start.
	Before *time.Time `json:"before"`
	// KeepRunning: the fill is recurring and active. RecurringSince is when
	// it became recurring (null for a one-time fill); it then imports by
	// the two-hour rule, and its status, error and counts are those of its
	// current or last run, which started at RunStartedAt.
	KeepRunning    bool       `json:"keepRunning"`
	RecurringSince *time.Time `json:"recurringSince"`
	RunStartedAt   *time.Time `json:"runStartedAt"`
	// Interval is the fill's own interval, "default" or a duration;
	// Every the effective one and NextAt the next run, null when the fill
	// does not keep running or the schedule is off. Null for one-time
	// fills.
	Interval *string    `json:"interval"`
	Every    *string    `json:"every"`
	NextAt   *time.Time `json:"nextAt"`
	// Health of an active recurring fill; null otherwise.
	Health *fillHealthJSON `json:"health"`
	// UnconfirmedFolders are trash and spam folders that appeared after the
	// fill started; recurring runs skip them.
	UnconfirmedFolders []string         `json:"unconfirmedFolders"`
	Error              string           `json:"error"`
	CreatedAt          time.Time        `json:"createdAt"`
	FinishedAt         *time.Time       `json:"finishedAt"`
	Folders            []fillFolderJSON `json:"folders"`
}

// firstReceivedJSON is the first message an smtp account received that is
// still stored: a sanity check for a fill's cut-off.
type firstReceivedJSON struct {
	At      time.Time `json:"at"`
	Subject string    `json:"subject"`
}

// fillJSON returns a fill with its schedule and, if it keeps running, its
// health from health (by fill ID).
func (s *Server) fillJSON(f *store.Fill, health map[int64]*store.FillHealth, busy busyState, now time.Time) *fillJSON {
	out := &fillJSON{
		ID: f.ID, Status: f.Status, Host: f.Host, Port: f.Port, TLS: string(f.TLSMode), Username: f.Username,
		IncludedFolders: nonNilList(f.Included), ExcludedFolders: nonNilList(f.Excluded), FolderPrefix: f.FolderPrefix,
		Before: f.Cutoff, KeepRunning: f.KeepRunning, RecurringSince: f.RecurringSince, RunStartedAt: f.RunStartedAt,
		UnconfirmedFolders: nonNilList(f.Unconfirmed), Error: f.Error, CreatedAt: f.CreatedAt, FinishedAt: f.FinishedAt,
		Folders: make([]fillFolderJSON, 0, len(f.Folders)),
	}
	if f.Recurring() {
		own := archive.FormatOwnInterval(f.Interval)
		out.Interval = &own
	}
	sched := s.schedule()
	if next, ok := sched.NextFill(f); ok {
		every := sched.Every(f.Interval).String()
		var hold time.Time
		if s.fills != nil {
			hold = s.fills.HoldUntil
		}
		switch {
		case f.Status == store.FillRunning && now.Before(hold) && !busy.running[f.AccountID]:
			// A run that waits for the hold after the server's start.
			next = hold
		case next.Before(hold):
			next = hold
		}
		out.Every, out.NextAt = &every, &next
	}
	if h := health[f.ID]; h != nil {
		out.Health = &fillHealthJSON{
			State: s.fillHealthState(h, busy, now), FailureStreak: h.FailureStreak,
			FailingSince: h.FailingSince, LastSuccessAt: h.LastSuccessAt,
		}
	}
	for _, ff := range f.Folders {
		c := ff.Counts
		out.Folders = append(out.Folders, fillFolderJSON{
			Source: ff.SourceName, Folder: ff.FolderName, Done: ff.Done, Error: ff.Error,
			Scanned: c.Scanned, Imported: c.Imported, New: c.New, AfterCutoff: c.AfterCutoff,
			Journaled: c.Journaled, Present: c.Present, Deleted: c.Deleted, Waiting: c.Waiting,
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
	// FolderPrefix is refused: fills write into JOURNAL (#112).
	FolderPrefix string `json:"folderPrefix"`
	// Before is the cut-off; default: now. Not with KeepRunning.
	Before *time.Time `json:"before"`
	// ConfirmFolders copies trash and spam folders too, if the filters
	// select them.
	ConfirmFolders bool `json:"confirmFolders"`
	// KeepRunning makes the fill recurring: it imports by the two-hour rule
	// and runs again every Interval ("default" or a duration; default:
	// the server's).
	KeepRunning bool    `json:"keepRunning"`
	Interval    *string `json:"interval"`
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
	if in.FolderPrefix != "" {
		s.fail(w, r, http.StatusBadRequest, "folderPrefix is no longer supported: fills write into "+store.SMTPFolder, nil)
		return
	}
	var cutoff *time.Time
	switch {
	case in.KeepRunning && in.Before != nil:
		s.fail(w, r, http.StatusBadRequest, "before and keepRunning exclude each other: a recurring fill imports what is two hours old", nil)
		return
	case !in.KeepRunning && in.Interval != nil:
		s.fail(w, r, http.StatusBadRequest, "interval needs keepRunning", nil)
		return
	case in.Before != nil:
		cutoff = in.Before
	case !in.KeepRunning:
		now := s.currentTime()
		cutoff = &now
	}
	interval, ok := s.fillInterval(w, r, in.Interval, nil)
	if !ok {
		return
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
		Included: in.IncludedFolders, Excluded: in.ExcludedFolders, Cutoff: cutoff, CreatedBy: &me,
		KeepRunning: in.KeepRunning, Interval: interval,
	}
	if in.KeepRunning {
		// Later runs skip trash and spam folders nobody confirmed.
		fill.Confirmed = archive.SuggestExclusions(folders, in.IncludedFolders, in.ExcludedFolders)
	}
	e := s.auditEntry(r, "data.fill")
	auditAccount(e, a)
	e.Detail("host", src.Host)
	e.Detail("username", src.Username)
	if cutoff != nil {
		e.Detail("before", cutoff.UTC().Format(time.RFC3339))
	} else {
		e.Detail("keep_running", true)
		e.Detail("interval", archive.FormatOwnInterval(interval))
	}
	e.Detail("folders", map[string]any{"included": nonNilList(in.IncludedFolders), "excluded": nonNilList(in.ExcludedFolders)})
	seal := func(id int64) ([]byte, error) { return archive.SealFillPassword(s.syncer.Sealer, id, in.Password) }
	err = s.store.CreateFill(withAudit(r, e), fill, seal)
	switch {
	case errors.Is(err, store.ErrFillRunning):
		s.fail(w, r, http.StatusConflict, err.Error(), nil)
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

// fillInterval reads the interval of a recurring fill: nil for missing or
// "default". A value other than stored must not be below the server's
// floor. It answers 400 itself and returns false then.
func (s *Server) fillInterval(w http.ResponseWriter, r *http.Request, v *string, stored *time.Duration) (*time.Duration, bool) {
	if v == nil {
		return nil, true
	}
	sched := s.schedule()
	d, err := archive.ParseFillInterval(*v)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "invalid interval: "+sched.FillIntervalRule(), nil)
		return nil, false
	}
	if !archive.SameOwnInterval(d, stored) {
		if err := sched.CheckFloor(d); err != nil {
			s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
			return nil, false
		}
	}
	return d, true
}

// fillChangeInput is the body of PATCH /api/accounts/{name}/fills/{id}.
type fillChangeInput struct {
	// KeepRunning true switches an ended fill on, with the password and
	// optionally the interval; false stops a recurring fill (as cancel).
	KeepRunning *bool  `json:"keepRunning"`
	Password    string `json:"password"`
	// Interval alone changes the interval of an active recurring fill.
	Interval *string `json:"interval"`
	// ConfirmFolders: copy trash and spam folders the fill has not filled
	// yet, if its filters select them.
	ConfirmFolders bool `json:"confirmFolders"`
}

// handleChangeFill switches a fill to recurring, stops it or changes its
// interval.
func (s *Server) handleChangeFill(w http.ResponseWriter, r *http.Request) {
	if !s.requireFills(w, r) {
		return
	}
	var in fillChangeInput
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
	switch {
	case in.KeepRunning == nil && in.Interval == nil:
		s.fail(w, r, http.StatusBadRequest, "nothing to change: send keepRunning or interval", nil)
		return
	case in.Password != "" && (in.KeepRunning == nil || !*in.KeepRunning):
		s.fail(w, r, http.StatusBadRequest, "a password only goes with keepRunning: true; to change it, stop the fill and resume it", nil)
		return
	case in.KeepRunning != nil && !*in.KeepRunning:
		if in.Interval != nil {
			s.fail(w, r, http.StatusBadRequest, "interval does not go with keepRunning: false", nil)
			return
		}
		s.cancelFill(w, r, a, fill)
	case in.KeepRunning != nil:
		s.switchFillOn(w, r, a, fill, &in)
	default:
		s.changeFillInterval(w, r, a, fill, *in.Interval)
	}
}

// switchFillOn makes an ended fill recurring, after a login check, and
// queues its first run.
func (s *Server) switchFillOn(w http.ResponseWriter, r *http.Request, a *store.Account, fill *store.Fill, in *fillChangeInput) {
	switch {
	case fill.KeepRunning:
		s.fail(w, r, http.StatusConflict, store.ErrFillKeepsRunning.Error(), nil)
		return
	case fill.Status == store.FillRunning:
		s.fail(w, r, http.StatusConflict, "the fill is running", nil)
		return
	case in.Password == "":
		s.fail(w, r, http.StatusBadRequest, "password is required", nil)
		return
	}
	interval, ok := s.fillInterval(w, r, in.Interval, nil)
	if !ok {
		return
	}
	src := &store.Account{Host: fill.Host, Port: fill.Port, TLSMode: fill.TLSMode, Username: fill.Username}
	folders, err := s.syncer.CheckLogin(r.Context(), src, in.Password)
	if err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, "login failed: "+err.Error(), nil)
		return
	}
	// Trash and spam folders the fill has not filled yet need a yes, as
	// for a new fill.
	var confirm []string
	for _, name := range archive.SuggestExclusions(folders, fill.Included, fill.Excluded) {
		if !slices.ContainsFunc(fill.Folders, func(ff store.FillFolder) bool { return ff.SourceName == name }) {
			confirm = append(confirm, name)
		}
	}
	if len(confirm) > 0 && !in.ConfirmFolders {
		s.writeJSON(w, http.StatusConflict, folderConfirmation{
			Error:               "the server marks these folders as trash or spam; confirm whether to copy them",
			SuggestedExclusions: confirm,
		})
		return
	}
	e := s.auditEntry(r, "data.fill_change")
	auditAccount(e, a)
	e.Detail("keep_running", map[string]any{"from": false, "to": true})
	e.Detail("interval", map[string]any{"from": archive.FillIntervalDetail(fill), "to": archive.FormatOwnInterval(interval)})
	seal := func(id int64) ([]byte, error) { return archive.SealFillPassword(s.syncer.Sealer, id, in.Password) }
	err = s.store.SetFillRecurring(withAudit(r, e), fill.ID, interval, confirm, seal)
	switch {
	case errors.Is(err, store.ErrFillRunning), errors.Is(err, store.ErrFillKeepsRunning):
		s.fail(w, r, http.StatusConflict, err.Error(), nil)
		return
	case err != nil:
		s.failStore(w, r, err)
		return
	}
	s.log.Info("fill keeps running", "account", a.Name, "fill", fill.ID)
	s.fills.Enqueue(fill.ID, a.ID)
	s.writeJSON(w, http.StatusAccepted, map[string]int64{"id": fill.ID})
}

// changeFillInterval sets the interval of an active recurring fill.
func (s *Server) changeFillInterval(w http.ResponseWriter, r *http.Request, a *store.Account, fill *store.Fill, v string) {
	if !fill.KeepRunning {
		s.fail(w, r, http.StatusConflict, store.ErrFillNotRecurring.Error(), nil)
		return
	}
	interval, ok := s.fillInterval(w, r, &v, fill.Interval)
	if !ok {
		return
	}
	if archive.SameOwnInterval(interval, fill.Interval) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	e := s.auditEntry(r, "data.fill_change")
	auditAccount(e, a)
	e.Detail("interval", map[string]any{"from": archive.FormatOwnInterval(fill.Interval), "to": archive.FormatOwnInterval(interval)})
	err := s.store.SetFillInterval(withAudit(r, e), fill.ID, interval)
	switch {
	case errors.Is(err, store.ErrFillNotRecurring):
		s.fail(w, r, http.StatusConflict, err.Error(), nil)
		return
	case err != nil:
		s.failStore(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleResumeFill continues a fill that failed, partly failed or was
// cancelled, with the password given again. A stopped recurring fill keeps
// running again.
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
	switch {
	case fill.Status == store.FillRunning:
		s.fail(w, r, http.StatusConflict, "the fill is running", nil)
		return
	case fill.Status == store.FillOK, fill.KeepRunning:
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

// handleCancelFill stops a running fill, or a recurring one, and wipes its
// password. A running fill stops after its current batch; what it copied
// stays.
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
	s.cancelFill(w, r, a, fill)
}

func (s *Server) cancelFill(w http.ResponseWriter, r *http.Request, a *store.Account, fill *store.Fill) {
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
