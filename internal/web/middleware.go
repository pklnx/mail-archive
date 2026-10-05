package web

import (
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// The server has no login yet and is meant to listen on localhost only.
// "Localhost only" alone does not protect it: any web page open in the
// browser can send requests to it (CSRF), and with DNS rebinding a page can
// even read the responses. The middleware below closes both holes:
//
//   - The Host header must be one of the allowed host names. A rebinding
//     attack uses the attacker's domain as Host, so it is rejected.
//   - State-changing requests must come from the same origin and carry a
//     JSON body, which a cross-site form or simple request cannot send.

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

func (s *Server) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")

		if !s.hostAllowed(r) {
			http.Error(w, "host not allowed", http.StatusForbidden)
			return
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
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
