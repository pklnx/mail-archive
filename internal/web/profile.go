package web

import (
	"net/http"
	"regexp"

	"github.com/pklnx/mail-archive/internal/store"
)

// The UI settings take "auto" or a tag; the database checks the same. The
// UI treats a value it cannot use like "auto".
var (
	languagePattern = regexp.MustCompile(`^(auto|[a-z]{2,3}(-[A-Za-z0-9]{2,8})*)$`)
	localePattern   = regexp.MustCompile(`^(auto|[A-Za-z]{2,3}(-[A-Za-z0-9]{1,8})*)$`)
)

const maxSettingLen = 35

type profileInput struct {
	Language *string `json:"language"`
	Locale   *string `json:"locale"`
}

// handleUpdateProfile changes the user's own web UI settings. An unchanged
// value is no change and writes no audit entry.
func (s *Server) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	var in profileInput
	if !s.decode(w, r, &in) {
		return
	}
	if in.Language == nil && in.Locale == nil {
		s.fail(w, r, http.StatusBadRequest, "send language or locale", nil)
		return
	}
	if in.Language != nil && !validSetting(languagePattern, *in.Language) {
		s.fail(w, r, http.StatusBadRequest, `language must be "auto" or a language code like "de"`, nil)
		return
	}
	if in.Locale != nil && !validSetting(localePattern, *in.Locale) {
		s.fail(w, r, http.StatusBadRequest, `locale must be "auto" or a language tag like "de-AT"`, nil)
		return
	}
	sess := currentSession(r)
	change := store.ProfileChange{Language: in.Language, Locale: in.Locale}
	if _, err := s.store.UpdateProfile(withAudit(r, s.auditEntry(r, "profile.change")), sess.UserID, change); err != nil {
		s.failStore(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func validSetting(p *regexp.Regexp, v string) bool {
	return len(v) <= maxSettingLen && p.MatchString(v)
}
