// Package embedui serves the SolidJS single-page application from the webapp
// binary.
//
// The built assets are compiled in rather than read from disk because the
// deployment image is FROM scratch with one static binary (see
// Dockerfile.web.dev). A separate asset server or a volume mount would be a new
// moving part in production for no benefit.
package embedui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// dist holds the Vite build output. `all:` is required so Vite's hashed asset
// files are included: the default embed pattern skips names beginning with "_"
// or ".", and Vite emits both.
//
// A placeholder index.html is committed so this package compiles before anyone
// has run the UI build. `tools build-ui` overwrites this directory.
//
//go:embed all:dist
var dist embed.FS

// Assets returns the built SPA rooted at dist/.
func Assets() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// Only reachable if the embed directive above stops matching, which is
		// a build-time mistake rather than a runtime condition.
		panic("embedui: dist directory is missing from the binary: " + err.Error())
	}
	return sub
}

// IndexHTML returns the SPA shell, and whether a real build is present.
//
// The placeholder is detectable on purpose: serving a "the UI has not been
// built" page beats serving a blank screen, and it makes a broken image obvious
// in seconds rather than after reading logs.
func IndexHTML() ([]byte, bool) {
	data, err := fs.ReadFile(dist, "dist/index.html")
	if err != nil {
		return []byte(placeholderHTML), false
	}
	return data, !strings.Contains(string(data), placeholderMarker)
}

const placeholderMarker = "reactorcide-ui-not-built"

const placeholderHTML = `<!doctype html>
<meta charset="utf-8">
<title>Reactorcide</title>
<p id="reactorcide-ui-not-built">The web UI has not been built into this binary.
Run <code>./tools build-ui</code> and rebuild.</p>
`

// FileServer serves the built assets with cache headers matched to how Vite
// names things.
//
// Hashed files under assets/ are immutable for a year: their name changes
// whenever their content does, so a stale cached copy is impossible. Everything
// else, index.html above all, is no-store — an index cached by a proxy would
// keep pointing at asset names that no longer exist after a deploy, which
// presents as a blank page that a reload does not fix.
func FileServer() http.Handler {
	assets := Assets()
	fileServer := http.FileServer(http.FS(assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(strings.TrimPrefix(r.URL.Path, "/"), "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-store")
		}
		fileServer.ServeHTTP(w, r)
	})
}
