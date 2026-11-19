package handlers

import (
	"net/http"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/oidcissuer"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
)

// federationRoutes holds the handlers of inbound federation and the OIDC
// issuer (WP4). createAppMux makes it and mounts its routes.
type federationRoutes struct {
	federation *FederationHandler
	oidc       *OIDCHandler
}

func newFederationRoutes(appStore store.Store, keys *secrets.MasterKeyManager) *federationRoutes {
	return &federationRoutes{federation: NewFederationHandler(appStore), oidc: NewOIDCHandler(appStore, keys)}
}

// register mounts the routes that are not under /api/v1/organizations/.
//
//   - POST /api/v1/federation/token: public token exchange (no Reactorcide
//     credential; the external JWT is the credential).
//   - ISSUER_PATH/.well-known/openid-configuration and
//     ISSUER_PATH/.well-known/jwks.json: public, only when the issuer is on.
//   - POST /api/v1/oidc/token: job tokens only.
//   - /api/v1/oidc/keys...: global admin (secrets:manage).
func (f *federationRoutes) register(mux *http.ServeMux, authMiddleware func(http.Handler) http.Handler) {
	mux.HandleFunc("/api/v1/federation/token", f.federation.Exchange)
	if f.oidc.Issuer != nil {
		base := f.oidc.IssuerPath()
		mux.HandleFunc(base+oidcissuer.DiscoveryPath, f.oidc.Discovery)
		mux.HandleFunc(base+oidcissuer.JWKSPath, f.oidc.JWKS)
	}
	mux.Handle("/api/v1/oidc/token", authMiddleware(http.HandlerFunc(f.oidc.Token)))
	keys := func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/api/v1/oidc/keys"), "/")
		authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f.oidc.ServeKeys(w, r, rest) })).ServeHTTP(w, r)
	}
	mux.HandleFunc("/api/v1/oidc/keys", keys)
	mux.HandleFunc("/api/v1/oidc/keys/", keys)
}

// serveOrg handles /api/v1/organizations/{org}/federation/... and
// /api/v1/organizations/{org}/oidc-settings[...]. It reports false for other
// paths.
func (f *federationRoutes) serveOrg(w http.ResponseWriter, r *http.Request, path string,
	transactionMiddleware, authMiddleware func(http.Handler) http.Handler) bool {
	parts := strings.SplitN(path, "/", 3)
	if len(parts) < 2 || (parts[1] != "federation" && parts[1] != "oidc-settings") {
		return false
	}
	rest := ""
	if len(parts) == 3 {
		rest = parts[2]
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if parts[1] == "federation" {
			f.federation.ServeFederation(w, r, parts[0], rest)
			return
		}
		f.oidc.ServeSettings(w, r, parts[0], rest)
	})
	transactionMiddleware(authMiddleware(handler)).ServeHTTP(w, r)
	return true
}
