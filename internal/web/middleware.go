package web

import (
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/pklnx/mail-archive/internal/config"
)

// A login alone does not protect the server: any web page open in the
// browser can send requests to it (CSRF), and with DNS rebinding a page can
// even read the responses of a server on localhost. The middleware below
// closes both holes:
//
//   - The Host header must be one of the allowed host names. A rebinding
//     attack uses the attacker's domain as Host, so it is rejected.
//   - State-changing requests must come from the same origin and carry a
//     JSON body, which a cross-site form or simple request cannot send.
//     Requests with an API key are exempt: they carry no cookie.
//   - Requests to /api that a browser makes for anything but a fetch, or a
//     navigation to one of the few routes a browser opens, are refused
//     (fetchMetadataAllowed). The HTML mail frame shares the app's origin,
//     so a mail's images and links would otherwise reach the API with the
//     session cookie.

// hostAllowed reports whether the request's Host (without port) is allowed.
func (s *Server) hostAllowed(r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(strings.ToLower(host), "[]")
	for _, h := range s.allowedHosts {
		if host == h {
			return true
		}
	}
	return false
}

// hstsValue is the Strict-Transport-Security header: one year.
const hstsValue = "max-age=31536000"

func (s *Server) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		// HSTS only where the request came over HTTPS (secureRequest): over
		// plain HTTP browsers ignore it, and behind an untrusted proxy it
		// would be wrong. No includeSubDomains or preload: the domain may
		// have other services.
		if s.secureRequest(r) {
			h.Set("Strict-Transport-Security", hstsValue)
		}

		if !s.hostAllowed(r) {
			s.log.Info("host not allowed; add it to "+config.EnvAllowedHosts+" if it is yours", "host", r.Host)
			http.Error(w, "host not allowed", http.StatusForbidden)
			return
		}
		// After the host check: a scanner or a client with a wrong Host must
		// not use up the one warning and get its address named as the proxy.
		s.warnUntrustedForwardedProto(r)
		// Before the routes: a refused request writes no audit entry.
		if !fetchMetadataAllowed(r) {
			s.log.Debug("request refused by its fetch metadata", "path", r.URL.Path,
				"dest", r.Header.Get("Sec-Fetch-Dest"), "mode", r.Header.Get("Sec-Fetch-Mode"))
			s.fail(w, r, http.StatusForbidden, "request not allowed from this context", nil)
			return
		}
		// An API key request carries its credential in a header that a
		// cross-site page cannot set, never in a cookie (see bearerKey), so
		// it needs neither Origin nor a JSON content type.
		_, keyRequest := bearerKey(r)
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if keyRequest {
				break
			}
			if !sameOrigin(r) {
				http.Error(w, "cross-origin request rejected", http.StatusForbidden)
				return
			}
			if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
				http.Error(w, "content type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin checks Origin against Host, falling back to Sec-Fetch-Site for
// clients that omit Origin. Requests with neither header are rejected.
func sameOrigin(r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		return err == nil && strings.EqualFold(u.Host, r.Host)
	}
	return r.Header.Get("Sec-Fetch-Site") == "same-origin"
}

// navigable matches the escaped paths of the only /api routes a browser
// legitimately navigates to: the mail frame (and its URL opened on its own),
// the .eml download, attachments, and the API description. The exact route
// shape, so that neither another route of a message nor a path with dot
// segments passes.
var navigable = regexp.MustCompile(`^/api/(messages/[0-9a-f]{64}/(html|raw|parts/[0-9]+)|openapi\.json)$`)

// navigationDests are the Sec-Fetch-Dest values of a navigation.
var navigationDests = map[string]bool{"document": true, "iframe": true, "frame": true, "embed": true, "object": true}

// fetchMetadataAllowed is the Fetch Metadata guard for /api. The HTML mail
// frame runs with the app's origin (it is sandboxed without scripts), so an
// image or a link in a mail is a same-origin request that carries the
// SameSite=Strict session cookie. Nobody can read the answer, but GETs
// leave audit entries in the user's name and can open IMAP connections. A
// request to /api is refused when the browser says it is
//
//   - a prefetch (Sec-Purpose),
//   - a navigation (Sec-Fetch-Mode navigate, or a Sec-Fetch-Dest of a
//     document or frame) other than a GET or HEAD of a navigable route, or
//   - any other destination than "empty" (a fetch): an image, style, font,
//     script, media and so on. The app sends none of these to /api.
//
// Without these headers (API clients, browsers without Fetch Metadata) a
// request passes, as before.
func fetchMetadataAllowed(r *http.Request) bool {
	// The cleaned, unescaped path: what the mux routes on, or redirects to.
	p := path.Clean("/" + r.URL.Path)
	if p != "/api" && !strings.HasPrefix(p, "/api/") {
		return true
	}
	if strings.Contains(strings.ToLower(r.Header.Get("Sec-Purpose")), "prefetch") {
		return false
	}
	dest := r.Header.Get("Sec-Fetch-Dest")
	if r.Header.Get("Sec-Fetch-Mode") == "navigate" || navigationDests[dest] {
		// The path as sent: an escaped character in it is not the route shape.
		return (r.Method == http.MethodGet || r.Method == http.MethodHead) && navigable.MatchString(r.URL.EscapedPath())
	}
	return dest == "" || dest == "empty"
}
