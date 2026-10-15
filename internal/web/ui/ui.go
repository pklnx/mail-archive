// Package ui embeds the web UI built from web/ (see web/package.json).
//
// The dist directory is produced by `make web` (pnpm build). The repository
// only contains dist/.keep, so plain `go build` works and serves a hint page
// until the UI is built; Docker images and CI always build it.
package ui

import (
	"embed"
	"io/fs"
	"maps"
	"net/http"
	"slices"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// contentSecurityPolicy for the UI: only same-origin scripts, styles and
// requests; mail HTML is shown in a sandboxed iframe from the same origin.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"frame-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// rootFiles are served from the build root (web/public): the web app manifest
// and its icons, which browsers fetch by fixed names. Everything else that is
// not under /assets/ gets index.html.
var rootFiles = map[string]string{
	"/manifest.webmanifest":  "application/manifest+json",
	"/icon-192.png":          "image/png",
	"/icon-512.png":          "image/png",
	"/icon-maskable-512.png": "image/png",
	"/apple-touch-icon.png":  "image/png",
}

// RootPaths are the request paths of the rootFiles, for the server's routes.
func RootPaths() []string {
	return slices.Sorted(maps.Keys(rootFiles))
}

// Handler serves index.html at "/", the hashed build assets at "/assets/" and
// the rootFiles.
func Handler() http.Handler {
	files, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	index, err := fs.ReadFile(files, "index.html")
	if err != nil {
		index = nil // UI not built
	}
	assets := http.FileServerFS(files)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Frame-Options", "DENY")
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			// File names contain a content hash, so they never change.
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
			assets.ServeHTTP(w, r)
			return
		}
		h.Set("Cache-Control", "no-cache")
		if typ, ok := rootFiles[r.URL.Path]; ok {
			if data, err := fs.ReadFile(files, strings.TrimPrefix(r.URL.Path, "/")); err == nil {
				h.Set("Content-Type", typ)
				_, _ = w.Write(data)
				return
			}
		}
		if index == nil {
			h.Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("The web UI is not built. Run `make web` and rebuild, or use the Docker image.\n"))
			return
		}
		h.Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	})
}
