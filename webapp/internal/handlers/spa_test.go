package handlers

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/webapp/internal/embedui"
)

// uiIsBuilt reports whether this binary carries a real SPA build.
//
// A checkout that has not run `./tools build-ui` embeds only a placeholder, and
// that is a supported state: the Go build must work without the node toolchain.
// The routing behaviour is asserted either way; only the body differs.
func uiIsBuilt() bool {
	_, built := embedui.IndexHTML()
	return built
}

// TestAppShellServedForClientRoutes covers deep links and hard refreshes: a
// path the SPA routes client-side must return the shell, not a 404.
func TestAppShellServedForClientRoutes(t *testing.T) {
	router := NewRouter()

	for _, path := range []string{"/app/", "/app/jobs", "/app/workflows/01ABCDEF", "/app/projects/new"} {
		t.Run(path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))

			if uiIsBuilt() {
				if recorder.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200 (the SPA shell)", recorder.Code)
				}
				if !strings.Contains(recorder.Body.String(), `<div id="root">`) {
					t.Errorf("body does not look like the SPA shell: %.200s", recorder.Body.String())
				}
			} else {
				// A binary shipped without a UI build is a build-pipeline
				// failure, and says so with a 503 rather than serving a page
				// that merely looks broken.
				if recorder.Code != http.StatusServiceUnavailable {
					t.Fatalf("status = %d, want 503 when no UI is embedded", recorder.Code)
				}
				if !strings.Contains(recorder.Body.String(), "has not been built") {
					t.Errorf("the placeholder should say what is wrong, got: %.200s", recorder.Body.String())
				}
			}

			if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store: a cached shell references "+
					"asset names that vanish on the next deploy", got)
			}
		})
	}
}

// TestHashedAssetsAreImmutablyCacheable is the other half: Vite hashes these
// filenames, so a stale copy is impossible and a long cache is free.
func TestHashedAssetsAreImmutablyCacheable(t *testing.T) {
	router := NewRouter()

	// Find a real hashed asset from the embedded bundle rather than guessing a
	// name, which would rot the moment the build output changed.
	entries, err := assetNames("assets")
	if err != nil || len(entries) == 0 {
		t.Skip("no built assets embedded; run ./tools build-ui")
	}

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/app/assets/"+entries[0], nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for %s", recorder.Code, entries[0])
	}
	if got := recorder.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("Cache-Control = %q, want an immutable long cache for a hashed asset", got)
	}
}

func TestUnknownAssetPathFallsBackToTheShell(t *testing.T) {
	router := NewRouter()
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/app/not-a-real-file.js", nil))

	// A missing file under /app/ is treated as a client route. That is correct:
	// the router cannot tell a typo'd asset from a route it does not know, and
	// the SPA renders its own not-found page.
	want := http.StatusOK
	if !uiIsBuilt() {
		want = http.StatusServiceUnavailable
	}
	if recorder.Code != want {
		t.Errorf("status = %d, want %d (the shell)", recorder.Code, want)
	}
}

// assetNames lists files in one directory of the embedded bundle.
func assetNames(dir string) ([]string, error) {
	entries, err := fs.ReadDir(embedui.Assets(), dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}
