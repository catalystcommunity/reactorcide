package uiapi

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/auth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// Deps is the shared dependency bag every ReactorcideAuth/ReactorcideUi op
// implementation (Task G) is built against: the store, the authz role
// resolver, the auth session/login/admission machinery, the secrets master
// key manager (for rotation/secret ops), and the corndogs client (for
// cancel/kill). One Deps is constructed once at startup (see
// handlers/router.go) and shared by both AuthService and UiService.
type Deps struct {
	Store          DataStore
	Resolver       *authz.Resolver
	Sessions       *auth.Sessions
	LoginService   *auth.LoginService
	Admission      *auth.Admission
	KeyManager     *secrets.MasterKeyManager
	CorndogsClient corndogs.ClientInterface

	// SecretsProvider resolves a secrets.Provider scoped to an org.
	// NewDeps wires this to the real DB-backed default
	// (defaultSecretsProviderForOrg); tests substitute an in-memory
	// secrets.Provider fake here directly (no interface indirection needed)
	// so rotation/secret-value ops are exercisable without a live Postgres.
	SecretsProvider func(ctx context.Context, orgID string) (secrets.Provider, error)
}

// NewDeps constructs a Deps. backend selects the login flow (NewNoneBackend
// for REACTORCIDE_UI_AUTH_MODE=none, or a *LocalRPBackend/*RPBackend
// matching the configured mode); keyManager may be nil if secrets aren't
// configured (secret/rotation-value ops will fail with a ServiceError
// "internal" in that case rather than panic).
func NewDeps(store DataStore, backend auth.LoginBackend, keyManager *secrets.MasterKeyManager, corndogsClient corndogs.ClientInterface) *Deps {
	d := &Deps{
		Store:          store,
		Resolver:       authz.NewResolver(store),
		Sessions:       auth.NewSessions(store),
		LoginService:   auth.NewLoginService(store, backend),
		Admission:      auth.NewAdmission(store),
		KeyManager:     keyManager,
		CorndogsClient: corndogsClient,
	}
	d.SecretsProvider = d.defaultSecretsProviderForOrg
	return d
}

// resolveIdentity resolves the CSIL-RPC envelope's auth token (if any) into
// an authz.Identity and, when a session was successfully resolved, the
// underlying *models.User. A missing/empty auth field, an unknown token, or
// an expired/revoked session all resolve to (AnonymousIdentity, nil, nil) —
// callers that require a logged-in identity must check the returned user
// themselves (see requireUser) rather than treating a resolution error as
// fatal, since anonymous callers are legitimate for many ops (e.g.
// get-auth-config, cancel-job in mode none).
func (d *Deps) resolveIdentity(ctx context.Context) (authz.Identity, *models.User) {
	token, ok := AuthTokenFromContext(ctx)
	if !ok || token == "" {
		return authz.AnonymousIdentity(), nil
	}
	user, _, err := d.Sessions.ResolveSession(ctx, token)
	if err != nil || user == nil {
		return authz.AnonymousIdentity(), nil
	}
	return authz.IdentityFromUser(user), user
}

// requireUser resolves the caller like resolveIdentity, but returns a
// ServiceError "unauthorized" instead of an anonymous identity when no valid
// session is present. Use for ops the permission matrix never grants to an
// anonymous caller under any auth mode (e.g. every management op).
func (d *Deps) requireUser(ctx context.Context) (authz.Identity, *models.User, error) {
	id, user := d.resolveIdentity(ctx)
	if user == nil {
		return authz.Identity{}, nil, NewServiceError("unauthorized", "a valid session is required for this operation")
	}
	return id, user, nil
}

// defaultSecretsProviderForOrg builds a secrets.Provider scoped to orgID,
// mirroring handlers/router.go's makeTokenResolver and
// handlers/secrets_handler.go's getProvider: resolve the org's encryption
// key under the configured master keys, then wrap the request-scoped (or
// global, outside a transaction) DB handle in a DatabaseProvider. Returns a
// ServiceErr "internal" if secrets aren't configured on this server (no
// master key manager) rather than a nil-pointer panic. This is Deps'
// production SecretsProvider; see that field's doc comment for how tests
// substitute it.
func (d *Deps) defaultSecretsProviderForOrg(ctx context.Context, orgID string) (secrets.Provider, error) {
	if d.KeyManager == nil {
		return nil, NewServiceError("internal", "secrets are not configured on this server")
	}
	db := store.GetDBFromContext(ctx)
	if db == nil {
		return nil, NewServiceError("internal", "database is not available")
	}
	orgKey, err := d.KeyManager.GetOrgEncryptionKey(db, orgID)
	if err != nil {
		return nil, NewServiceError("internal", "failed to resolve org encryption key")
	}
	provider, err := secrets.NewDatabaseProvider(db, orgID, orgKey)
	if err != nil {
		return nil, NewServiceError("internal", "failed to build secrets provider")
	}
	return provider, nil
}
