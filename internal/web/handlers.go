package web

import (
	"errors"
	"fmt"
	"io"
	gomime "mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pklnx/mail-archive/internal/mime"
	"github.com/pklnx/mail-archive/internal/store"
)

// maxPartSize limits attachment downloads held in memory.
const maxPartSize = 64 << 20

type statusAccount struct {
	Name       string     `json:"name"`
	Enabled    bool       `json:"enabled"`
	Folders    int        `json:"folders"`
	Messages   int64      `json:"messages"`
	LastRunAt  *time.Time `json:"lastRunAt"`
	LastStatus *string    `json:"lastStatus"`
	LastError  *string    `json:"lastError"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	stats, unique, err := s.store.Stats(r.Context())
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	accounts := make([]statusAccount, 0, len(stats))
	for _, st := range stats {
		accounts = append(accounts, statusAccount{
			Name: st.Account, Enabled: st.Enabled, Folders: st.Folders, Messages: st.Locations,
			LastRunAt: st.LastRunAt, LastStatus: st.LastStatus, LastError: st.LastRunError,
		})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"uniqueMessages": unique, "accounts": accounts})
}

type folderJSON struct {
	Name         string     `json:"name"`
	Messages     int64      `json:"messages"`
	LastSyncedAt *time.Time `json:"lastSyncedAt"`
}

type accountJSON struct {
	Name    string       `json:"name"`
	Enabled bool         `json:"enabled"`
	Folders []folderJSON `json:"folders"`
}

func (s *Server) handleAccounts(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListAccountFolders(r.Context())
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	out := make([]accountJSON, 0, len(list))
	for _, a := range list {
		aj := accountJSON{Name: a.Name, Enabled: a.Enabled, Folders: make([]folderJSON, 0, len(a.Folders))}
		for _, f := range a.Folders {
			aj.Folders = append(aj.Folders, folderJSON{Name: f.Name, Messages: f.Messages, LastSyncedAt: f.LastSyncedAt})
		}
		out = append(out, aj)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"accounts": out})
}

type summaryJSON struct {
	ID      string     `json:"id"`
	Size    int64      `json:"size"`
	Subject string     `json:"subject"`
	From    string     `json:"from"`
	SentAt  *time.Time `json:"sentAt"`
	SortAt  time.Time  `json:"sortAt"`
	// Snippet marks query matches with U+E000 (start) and U+E001 (end).
	Snippet string `json:"snippet,omitempty"`
}

func toSummary(m store.MessageSummary) summaryJSON {
	return summaryJSON{ID: m.SHA256, Size: m.Size, Subject: m.Subject, From: m.From, SentAt: m.SentAt, SortAt: m.SortAt, Snippet: m.Snippet}
}

func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.SearchFilter{Query: q.Get("q"), Account: q.Get("account"), Folder: q.Get("folder")}
	var err error
	if f.After, err = parseDate(q.Get("after")); err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	}
	if f.Before, err = parseDate(q.Get("before")); err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	}
	if f.Limit, err = parseLimit(q.Get("limit"), 50, 200); err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error(), nil)
		return
	}
	if c := q.Get("cursor"); c != "" {
		at, sha, err := decodeCursor(c)
		if err != nil {
			s.fail(w, r, http.StatusBadRequest, "invalid cursor", nil)
			return
		}
		f.CursorAt, f.CursorSHA = &at, sha
	}
	// Fetch one extra row to know whether another page exists.
	want := f.Limit
	f.Limit++
	rows, err := s.store.SearchMessages(r.Context(), f)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	var next *string
	if len(rows) > want {
		rows = rows[:want]
		last := rows[len(rows)-1]
		c := encodeCursor(last.SortAt, last.SHA256)
		next = &c
	}
	items := make([]summaryJSON, 0, len(rows))
	for _, m := range rows {
		items = append(items, toSummary(m))
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"messages": items, "nextCursor": next})
}

type locationJSON struct {
	Account      string     `json:"account"`
	Folder       string     `json:"folder"`
	UID          int64      `json:"uid"`
	Flags        []string   `json:"flags"`
	InternalDate *time.Time `json:"internalDate"`
}

type partJSON struct {
	Index       int    `json:"index"`
	ContentType string `json:"contentType"`
	Filename    string `json:"filename,omitempty"`
	ContentID   string `json:"contentId,omitempty"`
	Size        int64  `json:"size"`
	Attachment  bool   `json:"attachment"`
	Inline      bool   `json:"inline"`
}

type messageJSON struct {
	summaryJSON
	MessageID string         `json:"messageId"`
	To        string         `json:"to"`
	Cc        string         `json:"cc"`
	DateRaw   string         `json:"dateHeader"`
	Text      string         `json:"text"`
	HasHTML   bool           `json:"hasHtml"`
	Truncated bool           `json:"truncated"`
	Parts     []partJSON     `json:"parts"`
	Locations []locationJSON `json:"locations"`
}

func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	sha, ok := s.pathSHA(w, r)
	if !ok {
		return
	}
	d, err := s.store.GetMessageDetail(r.Context(), sha)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	m, err := s.parseStored(d.StoredPath)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "cannot read message", err)
		return
	}
	out := messageJSON{
		summaryJSON: toSummary(d.MessageSummary),
		MessageID:   d.MessageID, To: m.To, Cc: m.Cc, DateRaw: m.Date,
		Text: m.Text, HasHTML: m.HTML != "", Truncated: m.Truncated,
		Parts: make([]partJSON, 0, len(m.Parts)), Locations: make([]locationJSON, 0, len(d.Locations)),
	}
	if out.Text == "" && m.HTML != "" {
		out.Text = mime.HTMLToText(m.HTML)
	}
	for _, p := range m.Parts {
		out.Parts = append(out.Parts, partJSON{
			Index: p.Index, ContentType: p.ContentType, Filename: p.Filename, ContentID: p.ContentID,
			Size: p.Size, Attachment: p.Attachment, Inline: p.Inline,
		})
	}
	for _, l := range d.Locations {
		flags := l.Flags
		if flags == nil {
			flags = []string{}
		}
		out.Locations = append(out.Locations, locationJSON{
			Account: l.Account, Folder: l.Folder, UID: l.UID, Flags: flags, InternalDate: l.InternalDate,
		})
	}
	s.writeJSON(w, http.StatusOK, out)
}

// handleMessageHTML serves the HTML body for display in a sandboxed iframe.
// The Content-Security-Policy forbids scripts, forms, plugins and remote
// content. Remote images stay blocked (they reveal that a mail was opened)
// unless the client asks for them with ?images=1.
func (s *Server) handleMessageHTML(w http.ResponseWriter, r *http.Request) {
	sha, ok := s.pathSHA(w, r)
	if !ok {
		return
	}
	d, err := s.store.GetMessageDetail(r.Context(), sha)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	m, err := s.parseStored(d.StoredPath)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "cannot read message", err)
		return
	}
	if m.HTML == "" {
		s.fail(w, r, http.StatusNotFound, "message has no HTML body", nil)
		return
	}
	// Inline images (cid:...) are served by the parts endpoint, which is
	// relative to this URL: /api/messages/{sha}/parts/{n}.
	pairs := make([]string, 0, 2*len(m.Parts))
	for _, p := range m.Parts {
		if p.ContentID != "" {
			pairs = append(pairs, "cid:"+p.ContentID, "parts/"+strconv.Itoa(p.Index))
		}
	}
	body := strings.NewReplacer(pairs...).Replace(m.HTML)

	self := "http://" + r.Host + " https://" + r.Host
	img := "'self' " + self + " data:"
	if r.URL.Query().Get("images") == "1" {
		img += " https: http:"
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src "+img+
		"; font-src data:; form-action 'none'; base-uri 'none'; frame-ancestors 'self'"+
		"; sandbox allow-popups allow-popups-to-escape-sandbox")
	h.Set("X-Frame-Options", "SAMEORIGIN")
	// Links open in a new tab instead of inside the viewer frame.
	_, _ = io.WriteString(w, `<base target="_blank">`+"\n"+body)
}

func (s *Server) handleMessageRaw(w http.ResponseWriter, r *http.Request) {
	sha, ok := s.pathSHA(w, r)
	if !ok {
		return
	}
	d, err := s.store.GetMessageDetail(r.Context(), sha)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	f, err := s.blobs.Open(d.StoredPath)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "cannot read message", err)
		return
	}
	defer func() { _ = f.Close() }()
	h := w.Header()
	h.Set("Content-Type", "message/rfc822")
	h.Set("Content-Disposition", disposition("attachment", sha[:16]+".eml"))
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	http.ServeContent(w, r, "", time.Time{}, f)
}

// inlineTypes may be shown in the browser. Everything else is downloaded as
// application/octet-stream, so a mail cannot deliver active content (HTML,
// SVG, scripts) under this origin.
var inlineTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
}

func (s *Server) handleMessagePart(w http.ResponseWriter, r *http.Request) {
	sha, ok := s.pathSHA(w, r)
	if !ok {
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 0 {
		s.fail(w, r, http.StatusBadRequest, "invalid part index", nil)
		return
	}
	d, err := s.store.GetMessageDetail(r.Context(), sha)
	if err != nil {
		s.failStore(w, r, err)
		return
	}
	f, err := s.blobs.Open(d.StoredPath)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "cannot read message", err)
		return
	}
	defer func() { _ = f.Close() }()
	p, data, err := mime.OpenPart(f, n, maxPartSize)
	if errors.Is(err, mime.ErrNoPart) {
		s.fail(w, r, http.StatusNotFound, "no such part", nil)
		return
	}
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "cannot read part", err)
		return
	}
	name := p.Filename
	if name == "" {
		name = fmt.Sprintf("part-%d", p.Index)
	}
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Content-Length", strconv.Itoa(len(data)))
	if inlineTypes[p.ContentType] {
		h.Set("Content-Type", p.ContentType)
		h.Set("Content-Disposition", disposition("inline", name))
	} else {
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Content-Disposition", disposition("attachment", name))
	}
	// Safe: active types are served as octet-stream attachments (see
	// inlineTypes) and the CSP sandboxes the response.
	_, _ = w.Write(data) //nolint:gosec // see comment above
}

func (s *Server) parseStored(path string) (*mime.Message, error) {
	f, err := s.blobs.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return mime.Parse(f), nil
}

// disposition builds a Content-Disposition header, RFC 2231-encoding
// non-ASCII file names.
func disposition(kind, filename string) string {
	if v := gomime.FormatMediaType(kind, map[string]string{"filename": filename}); v != "" {
		return v
	}
	return kind
}
