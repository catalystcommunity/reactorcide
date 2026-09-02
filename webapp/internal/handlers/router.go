package handlers

import (
	"net/http"
	"strings"

	"github.com/catalystcommunity/reactorcide/webapp/internal/config"
	"github.com/catalystcommunity/reactorcide/webapp/internal/embedui"
	"github.com/catalystcommunity/reactorcide/webapp/internal/uiclient"
)

// NewRouter builds the webapp's HTTP surface.
//
// The webapp is a bridge, not an application. It owns exactly three things:
//
//  1. The SPA bundle, compiled into this binary (embedui).
//  2. The CSIL-RPC bridge, which swaps the browser's HttpOnly session cookie
//     for the envelope credential the coordinator authorizes against (see
//     rpc_bridge.go).
//  3. The session cookie itself: login, the IDP callback, logout and the
//     one-time bootstrap. These stay server-side endpoints because a CSIL op
//     returns a typed value and cannot set a Set-Cookie header, and because a
//     session token must never be readable by page JavaScript.
//
// Everything else the UI does is a CSIL call through the bridge, authorized by
// the coordinator against the real caller. There is no longer any route here
// that reads application data, and this process no longer holds a coordinator
// API token at all — see the note on config.APIToken's removal.
func NewRouter() http.Handler {
	mux := http.NewServeMux()
	uiClients := uiclient.NewWithTransport(&uiclient.CSILRPCTransport{
		BaseURL:                config.APIUrl,
		AllowInsecureTransport: config.AllowInsecureTransport,
	})
	webHandler := NewWebHandler(uiClients)
	wsProxy := NewWSProxy()
	bridge := NewRPCBridge()

	// Health check at root for k8s probes.
	mux.HandleFunc("GET /", webHandler.HealthCheck)

	// The CSIL-RPC bridge. Every read and every mutation the SPA performs goes
	// through this one route.
	//
	// Registered with an explicit method: a methodless pattern matches every
	// method, which Go's ServeMux then reports as conflicting with the "GET /"
	// health check above. The bridge re-checks the method itself, so a request
	// that somehow arrives another way is still refused rather than mishandled.
	mux.Handle("POST "+RPCBridgePath, bridge)

	// The SPA's single multiplexed event socket, proxied under the browser's
	// own session.
	mux.HandleFunc("GET /app/ws", wsProxy.UIStream)

	// Auth endpoints. These own the session cookie, which is why they are not
	// CSIL calls through the bridge.
	mux.HandleFunc("GET /app/auth/config", webHandler.AuthConfig)
	mux.HandleFunc("POST /app/auth/login", webHandler.LoginSubmit)
	mux.HandleFunc("GET /app/auth/callback", webHandler.AuthCallback)
	mux.HandleFunc("POST /app/auth/logout", webHandler.Logout)
	mux.HandleFunc("POST /app/auth/bootstrap", webHandler.BootstrapSubmit)
	// The SPA needs to know who it is rendering for before it can decide what
	// to show. This is a hint only: the coordinator re-checks every operation.
	mux.HandleFunc("GET /app/auth/session", webHandler.SessionInfoJSON)

	// The SPA itself. Hashed assets are served from the embedded filesystem;
	// every other /app/ path returns the shell so client-side routing survives
	// a hard refresh or a pasted deep link.
	assets := embedui.FileServer()
	mux.HandleFunc("GET /app/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/app/")
		if path != "" && assetExists(path) {
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/" + path
			assets.ServeHTTP(w, r2)
			return
		}
		serveAppShell(w)
	})

	return mux
}

// assetExists reports whether path names a real file in the embedded bundle.
// Anything else is a client-side route.
func assetExists(path string) bool {
	f, err := embedui.Assets().Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	return err == nil && !info.IsDir()
}

// serveAppShell writes index.html.
//
// no-store rather than a short cache: a cached shell keeps referencing asset
// filenames that no longer exist after a deploy, which presents to the user as
// a blank page that reloading does not fix.
func serveAppShell(w http.ResponseWriter) {
	shell, built := embedui.IndexHTML()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if !built {
		// A binary shipped without a UI build is a build-pipeline failure. Say
		// so with a 503 rather than serving a page that looks merely broken.
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_, _ = w.Write(shell)
}
