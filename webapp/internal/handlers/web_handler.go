package handlers

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/catalystcommunity/reactorcide/webapp/internal/uiclient"
	"github.com/catalystcommunity/reactorcide/webapp/internal/uiclient/csilapi"
)

// WebHandler owns the small set of routes that cannot be CSIL calls: the health
// check, and the endpoints whose real effect is a Set-Cookie header.
//
// It used to render every page of the UI from html/template, which meant it
// also had to fetch the data for every page — and it fetched jobs, workflows
// and projects over REST using a coordinator SERVICE token, which the
// coordinator's visibility filter then let through wholesale. All of that is
// gone. The SPA fetches its own data through the bridge under the caller's own
// session, so this type no longer reads application data at all.
type WebHandler struct {
	uiClients *uiclient.Clients

	authConfigMu  sync.Mutex
	authConfigVal csilapi.GetAuthConfigResponse
	authConfigErr error
	authConfigAt  time.Time
}

func NewWebHandler(uiClients *uiclient.Clients) *WebHandler {
	return &WebHandler{uiClients: uiClients}
}

// HealthCheck answers the Kubernetes probes at /.
func (h *WebHandler) HealthCheck(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.Redirect(w, r, "/app/", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// authConfigJSON is the shape GET /app/auth/config returns: what login modes
// the deployment offers, so the SPA can render the right sign-in affordance
// (or none) before anybody is authenticated.
type authConfigJSON struct {
	AuthMode           string `json:"auth_mode"`
	LoginEnabled       bool   `json:"login_enabled"`
	BootstrapAvailable bool   `json:"bootstrap_available"`
	HasGlobalAdmin     bool   `json:"has_global_admin"`
}

// AuthConfig serves the deployment's auth configuration.
func (h *WebHandler) AuthConfig(w http.ResponseWriter, r *http.Request) {
	out := authConfigJSON{AuthMode: "none"}
	if cfg, err := h.getAuthConfig(r.Context()); err == nil {
		out.AuthMode = cfg.AuthMode
		out.LoginEnabled = cfg.AuthMode != "" && cfg.AuthMode != "none"
		out.BootstrapAvailable = cfg.BootstrapAdminAvailable
		out.HasGlobalAdmin = cfg.HasGlobalAdmin
	}
	writeJSON(w, out)
}

// sessionJSON is the display-only summary the SPA renders its shell from.
//
// Every field is a HINT. The coordinator re-authorizes every operation, so a
// client that lies to itself about these only changes what it draws, never what
// it is allowed to do. Capabilities are included so the SPA can avoid rendering
// buttons that would fail, which is a courtesy, not a control.
type sessionJSON struct {
	LoggedIn      bool   `json:"logged_in"`
	UserID        string `json:"user_id,omitempty"`
	DisplayName   string `json:"display_name,omitempty"`
	IsGlobalAdmin bool   `json:"is_global_admin"`

	Capabilities csilapi.GetCapabilitiesResponse `json:"capabilities"`
}

// SessionInfoJSON serves the current session summary.
func (h *WebHandler) SessionInfoJSON(w http.ResponseWriter, r *http.Request) {
	si := h.resolveSession(r)
	writeJSON(w, sessionJSON{
		LoggedIn:      si.LoggedIn,
		UserID:        si.UserID,
		DisplayName:   si.DisplayName,
		IsGlobalAdmin: si.IsGlobalAdmin,
		Capabilities:  si.Caps,
	})
}

// writeJSON writes v with no-store, since every response from this file is
// session-specific.
func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
