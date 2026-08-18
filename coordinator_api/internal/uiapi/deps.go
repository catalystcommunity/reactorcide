package uiapi

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/auth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

// Deps is the shared dependency bag every ReactorcideAuth/ReactorcideUi op
// implementation uses: the store, the authz role
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
	ObjectStore    objects.ObjectStore

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

// resolveIdentity resolves the CSIL-RPC envelope's auth value as a UI session
// or an API token. An invalid value resolves to an anonymous identity. Callers
// that require a UI session must also require a non-nil user.
func (d *Deps) resolveIdentity(ctx context.Context) (authz.Identity, *models.User) {
	id, user, _ := d.resolveIdentityDetails(ctx)
	return id, user
}

func (d *Deps) resolveIdentityDetails(ctx context.Context) (authz.Identity, *models.User, *checkauth.Principal) {
	token, ok := AuthTokenFromContext(ctx)
	if !ok || token == "" {
		return authz.AnonymousIdentity(), nil, nil
	}
	user, _, err := d.Sessions.ResolveSession(ctx, token)
	if err == nil && user != nil {
		return authz.IdentityFromUser(user), user, nil
	}
	apiToken, tokenUser, err := d.Store.ValidateAPIToken(ctx, token)
	if err != nil || apiToken == nil {
		return authz.AnonymousIdentity(), nil, nil
	}
	capabilities, err := tokencaps.New(apiToken.Capabilities...)
	if err != nil {
		return authz.AnonymousIdentity(), nil, nil
	}
	principal := &checkauth.Principal{
		CredentialID: apiToken.TokenID, CredentialType: apiToken.SubjectType,
		UserID: apiToken.UserID, AllOrganizations: apiToken.AllOrganizations,
		OrganizationIDs: append([]string(nil), apiToken.OrganizationIDs...),
		AllCapabilities: apiToken.AllCapabilities, Capabilities: capabilities,
	}
	if apiToken.OwnerOrgID != nil {
		principal.OwnerOrgID = *apiToken.OwnerOrgID
	}
	if apiToken.BoundJobID != nil {
		principal.BoundJobID = *apiToken.BoundJobID
	}
	return authz.IdentityFromPrincipal(principal, tokenUser), tokenUser, principal
}

func (d *Deps) requireManagementIdentity(ctx context.Context) (context.Context, authz.Identity, *models.User, error) {
	id, user, principal := d.resolveIdentityDetails(ctx)
	if id.Anonymous || (id.UserID == "" && id.Token == nil) {
		return ctx, authz.Identity{}, nil, NewServiceError("unauthorized", "a valid session or API token is required for this operation")
	}
	if principal != nil {
		ctx = checkauth.SetPrincipalContext(ctx, principal)
		ctx = checkauth.SetUserContext(ctx, user)
		ctx = checkauth.SetVerifiedContext(ctx, true)
	}
	return ctx, id, user, nil
}

// requireUser resolves the caller like resolveIdentity, but returns a
// ServiceError "unauthorized" instead of an anonymous identity when no valid
// session is present. Use for ops the permission matrix never grants to an
// anonymous caller under any auth mode (e.g. every management op).
func (d *Deps) requireUser(ctx context.Context) (authz.Identity, *models.User, error) {
	id, user, principal := d.resolveIdentityDetails(ctx)
	if user == nil || principal != nil {
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
