package test

// Postgres-backed coverage for WP4: inbound federation (issuer and mapping
// administration, the token exchange end to end) and the Reactorcide OIDC
// issuer for jobs (job token claims, fork pull request claims, settings, key
// rotation with a JWKS overlap). Migration 000050.

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/federation"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/handlers"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/middleware"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// externalIssuer is an httptest HTTPS OIDC issuer (discovery + JWKS).
type externalIssuer struct {
	server *httptest.Server
	key    jose.JSONWebKey
	mu     sync.Mutex
}

func newExternalIssuer(t *testing.T) *externalIssuer {
	t.Helper()
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ei := &externalIssuer{key: jose.JSONWebKey{Key: private, KeyID: "ext-1", Algorithm: "ES256", Use: "sig"}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": ei.server.URL, "jwks_uri": ei.server.URL + "/jwks"})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{ei.key.Public()}})
	})
	ei.server = httptest.NewTLSServer(mux)
	t.Cleanup(ei.server.Close)
	return ei
}

func (ei *externalIssuer) token(t *testing.T, mutate func(map[string]any)) string {
	t.Helper()
	now := time.Now()
	claims := map[string]any{"iss": ei.server.URL, "sub": "repo:acme/app:ref:refs/heads/main", "aud": "reactorcide",
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(), "jti": "ext-jti", "repository": "acme/app"}
	if mutate != nil {
		mutate(claims)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: ei.key}, (&jose.SignerOptions{}).WithType("JWT"))
	require.NoError(t, err)
	value, err := jwt.Signed(signer).Claims(claims).Serialize()
	require.NoError(t, err)
	return value
}

func callHandler(t *testing.T, ctx context.Context, handler http.Handler, method, path, auth string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(ctx, method, path, bytes.NewReader(data))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func auditCount(t *testing.T, tx *gorm.DB, orgID, action string) int64 {
	t.Helper()
	var count int64
	require.NoError(t, tx.Model(&models.AuditEvent{}).Where("org_id = ? AND action = ?", orgID, action).Count(&count).Error)
	return count
}

func TestFederationExchangeEndToEnd(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		du := &DataUtils{db: tx}
		admin, err := du.CreateUser(DataSetup{"Username": "fed-admin", "Email": "fed@example.com"})
		require.NoError(t, err)
		adminAuth, err := createTokenAuthHeader(ctx, tx, admin.UserID)
		require.NoError(t, err)
		org := newPrincipalOrg(t, ctx, "fed-org")
		ext := newExternalIssuer(t)
		base := "/api/v1/organizations/fed-org"

		rr := principalRequest(t, ctx, http.MethodPost, base+"/principals", adminAuth, map[string]string{"name": "deployer"})
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		var principal struct {
			PrincipalID string `json:"principal_id"`
		}
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &principal))
		rr = principalRequest(t, ctx, http.MethodPost, base+"/principals/deployer/roles", adminAuth, map[string]string{"role": "admin"})
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())

		// Issuer administration (the router-mounted REST routes).
		rr = principalRequest(t, ctx, http.MethodPost, base+"/federation/issuers", adminAuth, map[string]any{
			"name": "plain", "issuer_url": "http://insecure.example.com", "audiences": []string{"reactorcide"}})
		assert.Equal(t, http.StatusBadRequest, rr.Code, "an HTTP issuer is refused without the explicit serve option")
		rr = principalRequest(t, ctx, http.MethodPost, base+"/federation/issuers", adminAuth, map[string]any{
			"name": "hs", "issuer_url": "https://hs.example.com", "audiences": []string{"reactorcide"}, "algorithms": []string{"HS256"}})
		assert.Equal(t, http.StatusBadRequest, rr.Code, "HMAC is never allowed")
		rr = principalRequest(t, ctx, http.MethodPost, base+"/federation/issuers", adminAuth, map[string]any{
			"name": "ci", "issuer_url": ext.server.URL, "audiences": []string{"reactorcide"}, "algorithms": []string{"ES256"}})
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodGet, base+"/federation/issuers", adminAuth, nil)
		require.Equal(t, http.StatusOK, rr.Code)
		assert.Contains(t, rr.Body.String(), ext.server.URL)

		// Mappings.
		rr = principalRequest(t, ctx, http.MethodPost, base+"/federation/issuers/ci/mappings", adminAuth, map[string]any{
			"name": "any", "principal": "deployer", "capabilities": []string{tokencaps.JobsSubmit},
			"conditions": []map[string]string{{"claim": "repository", "op": "equals", "value": "acme/app"}}})
		assert.Equal(t, http.StatusBadRequest, rr.Code, "a mapping needs a sub condition")
		rr = principalRequest(t, ctx, http.MethodPost, base+"/federation/issuers/ci/mappings", adminAuth, map[string]any{
			"name": "nocaps", "principal": "deployer",
			"conditions": []map[string]string{{"claim": "sub", "op": "glob", "value": "repo:acme/app:*"}}})
		assert.Equal(t, http.StatusBadRequest, rr.Code, "a mapping never gives every capability")
		rr = principalRequest(t, ctx, http.MethodPost, base+"/federation/issuers/ci/mappings", adminAuth, map[string]any{
			"name": "main", "principal": "deployer", "capabilities": []string{tokencaps.JobsSubmit, tokencaps.JobsRead},
			"token_ttl_seconds": 600,
			"conditions": []map[string]string{{"claim": "sub", "op": "glob", "value": "repo:acme/app:ref:refs/heads/*"},
				{"claim": "repository", "op": "equals", "value": "acme/app"}}})
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		assert.Contains(t, rr.Body.String(), `"principal_name":"deployer"`)

		// The exchange handler, with a JWKS client that trusts the test issuer.
		fed := handlers.NewFederationHandler(store.AppStore)
		cache := federation.NewJWKSCache(false)
		cache.Client = ext.server.Client()
		fed.Exchanger.Verifier = federation.NewVerifier(cache)
		exchange := http.HandlerFunc(fed.Exchange)

		rr = callHandler(t, ctx, exchange, http.MethodPost, "/api/v1/federation/token", "",
			map[string]any{"organization": "fed-org", "token": ext.token(t, nil)})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		var issued handlers.FederationTokenResponse
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &issued))
		assert.Equal(t, "Bearer", issued.TokenType)
		assert.Equal(t, principal.PrincipalID, issued.PrincipalID)
		assert.LessOrEqual(t, issued.ExpiresIn, int64(300), "the token never outlives the external token")
		assert.Equal(t, int64(1), auditCount(t, tx, org.OrgID, "federation.exchange"))

		identity, err := postgres_store.PostgresStore.GetPrincipalIdentity(ctx, models.IdentityTypeFederated,
			ext.server.URL+"|repo:acme/app:ref:refs/heads/main")
		require.NoError(t, err)
		assert.Equal(t, principal.PrincipalID, identity.PrincipalID)
		assert.Equal(t, models.IdentityVerifiedByFederation, identity.VerifiedBy)

		// The issued token works on the API as the service principal, with
		// the mapping capabilities intersected with the principal roles.
		submit := map[string]any{"name": "federated job", "source_type": "git", "source_url": "https://github.com/acme/app.git",
			"job_command": "echo hi", "org": "fed-org"}
		rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/jobs", "Bearer "+issued.AccessToken, submit)
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		assert.Contains(t, rr.Body.String(), principal.PrincipalID)
		rr = principalRequest(t, ctx, http.MethodPost, base+"/principals", "Bearer "+issued.AccessToken, map[string]string{"name": "escalate"})
		assert.Equal(t, http.StatusForbidden, rr.Code, "the mapping did not give organizations:manage")

		// Refusals: unmapped subject, wrong audience, unknown org. Each one is
		// audited without the token.
		for _, tc := range []struct {
			name string
			body map[string]any
		}{
			{"unmapped subject", map[string]any{"organization": "fed-org", "token": ext.token(t, func(c map[string]any) { c["sub"] = "repo:acme/other:ref:refs/heads/main" })}},
			{"wrong audience", map[string]any{"organization": "fed-org", "token": ext.token(t, func(c map[string]any) { c["aud"] = "other" })}},
			{"expired", map[string]any{"organization": "fed-org", "token": ext.token(t, func(c map[string]any) {
				c["exp"] = time.Now().Add(-time.Hour).Unix()
				c["iat"] = time.Now().Add(-2 * time.Hour).Unix()
			})}},
			{"unknown org", map[string]any{"organization": "no-such-org", "token": ext.token(t, nil)}},
		} {
			rr = callHandler(t, ctx, exchange, http.MethodPost, "/api/v1/federation/token", "", tc.body)
			assert.Equal(t, http.StatusUnauthorized, rr.Code, tc.name)
			assert.NotContains(t, rr.Body.String(), "access_token", tc.name)
		}
		assert.Equal(t, int64(3), auditCount(t, tx, org.OrgID, "federation.exchange.denied"))
		var denied []models.AuditEvent
		require.NoError(t, tx.Where("org_id = ? AND action = ?", org.OrgID, "federation.exchange.denied").Find(&denied).Error)
		subjects := []any{}
		for _, event := range denied {
			subjects = append(subjects, event.Details["subject"])
			assert.NotContains(t, event.Details, "token")
		}
		assert.Contains(t, subjects, "repo:acme/other:ref:refs/heads/main")

		// A disabled issuer accepts no token.
		rr = principalRequest(t, ctx, http.MethodPut, base+"/federation/issuers/ci", adminAuth, map[string]any{"status": "disabled"})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		rr = callHandler(t, ctx, exchange, http.MethodPost, "/api/v1/federation/token", "",
			map[string]any{"organization": "fed-org", "token": ext.token(t, nil)})
		assert.Equal(t, http.StatusUnauthorized, rr.Code)
		rr = principalRequest(t, ctx, http.MethodPut, base+"/federation/issuers/ci", adminAuth, map[string]any{"status": "active"})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

		// A disabled principal makes the exchanged token invalid at once.
		rr = principalRequest(t, ctx, http.MethodPost, base+"/principals/deployer/disable", adminAuth, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodGet, "/api/v1/jobs", "Bearer "+issued.AccessToken, nil)
		assert.Equal(t, http.StatusUnauthorized, rr.Code)
		rr = callHandler(t, ctx, exchange, http.MethodPost, "/api/v1/federation/token", "",
			map[string]any{"organization": "fed-org", "token": ext.token(t, nil)})
		assert.Equal(t, http.StatusUnauthorized, rr.Code, "a disabled principal gets no new token")

		// Expired exchange tokens are removed with their token identities.
		deleted, err := postgres_store.PostgresStore.DeleteExpiredFederatedTokens(ctx, time.Now().Add(time.Hour))
		require.NoError(t, err)
		assert.Equal(t, int64(1), deleted)
		var leftover int64
		require.NoError(t, tx.Model(&models.PrincipalIdentity{}).Where("identity_type = 'token' AND identity_value = ?", issued.TokenID).Count(&leftover).Error)
		assert.Zero(t, leftover)

		// Deleting the issuer removes its mappings.
		rr = principalRequest(t, ctx, http.MethodDelete, base+"/federation/issuers/ci", adminAuth, nil)
		require.Equal(t, http.StatusNoContent, rr.Code, rr.Body.String())
		mappings, err := postgres_store.PostgresStore.ListFederationMappings(ctx, org.OrgID, "")
		require.NoError(t, err)
		assert.Empty(t, mappings)
	})
}

func TestFederationAdminNeedsOrgAdmin(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		org := newPrincipalOrg(t, ctx, "fed-perm-org")
		value := "fed-perm-reader-token"
		token := &models.APIToken{TokenHash: checkauth.HashAPIToken(value), Name: "reader", IsActive: true, SubjectType: "service_token",
			OwnerOrgID: &org.OrgID, OrganizationIDs: []string{org.OrgID}, Capabilities: []string{tokencaps.OrganizationsRead}}
		require.NoError(t, postgres_store.PostgresStore.CreateAPIToken(ctx, token))
		rr := principalRequest(t, ctx, http.MethodGet, "/api/v1/organizations/fed-perm-org/federation/issuers", "Bearer "+value, nil)
		assert.Equal(t, http.StatusForbidden, rr.Code)
	})
}

func testMasterKeys(t *testing.T, tx *gorm.DB) *secrets.MasterKeyManager {
	t.Helper()
	raw := make([]byte, 32)
	_, err := rand.Read(raw)
	require.NoError(t, err)
	name := uniqueName("oidc-mk")
	t.Setenv(secrets.MasterKeysEnvVar, name+":"+base64.StdEncoding.EncodeToString(raw))
	keys, err := secrets.LoadMasterKeys()
	require.NoError(t, err)
	_, err = keys.RegisterMasterKey(tx, name, "oidc test")
	require.NoError(t, err)
	return keys
}

type oidcFixture struct {
	org     *models.Organization
	project *models.Project
}

func newOIDCFixture(t *testing.T, ctx context.Context, tx *gorm.DB) oidcFixture {
	t.Helper()
	org := newPrincipalOrg(t, ctx, uniqueName("oidc-org"))
	project := &models.Project{OrgID: org.OrgID, Name: "app", RepoURL: uniqueName("github.com/acme/app")}
	require.NoError(t, tx.Create(project).Error)
	return oidcFixture{org: org, project: project}
}

// runningJob makes an event, a workflow, and a running job of it, and mints
// the job token.
func (f oidcFixture) runningJob(t *testing.T, ctx context.Context, tx *gorm.DB, event *models.CIEvent, profile string, classes []string) (*models.Job, string) {
	t.Helper()
	event.OrgID = f.org.OrgID
	event.ProjectID = &f.project.ProjectID
	event.Source = models.CIEventSourceWebhook
	event.Provider = "github"
	event.Repo = "acme/app"
	event.SemanticKey = uniqueName("sk")
	require.NoError(t, tx.Create(event).Error)
	workflow := &models.WorkflowInstance{OrgID: f.org.OrgID, ProjectID: &f.project.ProjectID, Name: "Deploy",
		WorkflowSecurityID: "deploy", EventID: &event.EventID, VCSRepo: "acme/app", CommitSHA: event.CommitSHA,
		CIOrigin: "base", ExecutionProfile: profile, PolicyRevision: "rev-1", Status: "running"}
	require.NoError(t, tx.Create(workflow).Error)
	now := time.Now().UTC()
	repo, sha := "acme/app", event.CommitSHA
	job := &models.Job{OrgID: f.org.OrgID, ProjectID: &f.project.ProjectID, Name: "release", JobCommand: "true",
		WorkflowID: &workflow.WorkflowID, WorkflowNodeName: "release", EventID: &event.EventID, VCSRepo: &repo,
		CommitSHA: &sha, CIOrigin: "base", ExecutionProfile: profile, PolicyRevision: "rev-1", Status: "running",
		StartedAt: &now, TimeoutSeconds: 1800, RefClasses: classes}
	require.NoError(t, tx.Create(job).Error)
	token, err := postgres_store.PostgresStore.MintJobToken(ctx, job)
	require.NoError(t, err)
	return job, token
}

func verifyJobToken(t *testing.T, ctx context.Context, h *handlers.OIDCHandler, value, audience string) map[string]any {
	t.Helper()
	set, err := h.Issuer.JWKS(ctx)
	require.NoError(t, err)
	parsed, err := jwt.ParseSigned(value, []jose.SignatureAlgorithm{jose.ES256})
	require.NoError(t, err)
	keys := set.Key(parsed.Headers[0].KeyID)
	require.Len(t, keys, 1)
	var std jwt.Claims
	all := map[string]any{}
	require.NoError(t, parsed.Claims(keys[0].Key, &std, &all))
	require.NoError(t, std.Validate(jwt.Expected{Issuer: "https://ci.example.com/api/v1/oidc", AnyAudience: []string{audience}}))
	return all
}

func TestOIDCIssuerJobTokens(t *testing.T) {
	previous := config.OIDCIssuerURL
	config.OIDCIssuerURL = "https://ci.example.com/api/v1/oidc"
	t.Cleanup(func() { config.OIDCIssuerURL = previous })

	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		keys := testMasterKeys(t, tx)
		h := handlers.NewOIDCHandler(store.AppStore, keys)
		require.NotNil(t, h.Issuer)
		assert.Equal(t, "/api/v1/oidc", h.IssuerPath())
		auth := middleware.APITokenMiddleware(store.AppStore)
		tokenRoute := auth(http.HandlerFunc(h.Token))
		f := newOIDCFixture(t, ctx, tx)

		// Discovery and JWKS are public.
		rr := callHandler(t, ctx, http.HandlerFunc(h.Discovery), http.MethodGet, "/api/v1/oidc/.well-known/openid-configuration", "", nil)
		require.Equal(t, http.StatusOK, rr.Code)
		var discovery map[string]any
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &discovery))
		assert.Equal(t, "https://ci.example.com/api/v1/oidc", discovery["issuer"])
		assert.Equal(t, "https://ci.example.com/api/v1/oidc/.well-known/jwks.json", discovery["jwks_uri"])
		rr = callHandler(t, ctx, http.HandlerFunc(h.JWKS), http.MethodGet, "/api/v1/oidc/.well-known/jwks.json", "", nil)
		require.Equal(t, http.StatusOK, rr.Code)
		assert.NotContains(t, rr.Body.String(), `"d":`, "the JWKS never holds a private key")

		// A production push job.
		push := &models.CIEvent{EventType: "push", Ref: "refs/heads/main", Branch: "main", CommitSHA: "aaa111"}
		job, jobToken := f.runningJob(t, ctx, tx, push, "trusted", []string{"default", "production"})
		rr = callHandler(t, ctx, tokenRoute, http.MethodPost, "/api/v1/oidc/token", "Bearer "+jobToken, map[string]any{"audience": "sts.example.com"})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		var issued handlers.OIDCTokenResponse
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &issued))
		claims := verifyJobToken(t, ctx, h, issued.Token, "sts.example.com")
		assert.Equal(t, "org:"+f.org.Name+":project:app:workflow:deploy:node:release:ref:refs/heads/main", claims["sub"])
		assert.Equal(t, f.org.Name, claims["org"])
		assert.Equal(t, "app", claims["project"])
		assert.Equal(t, "acme/app", claims["repository"])
		assert.Equal(t, "deploy", claims["workflow_security_id"])
		assert.Equal(t, "Deploy", claims["workflow_name"])
		assert.Equal(t, job.JobID, claims["job_id"])
		assert.Equal(t, "aaa111", claims["sha"])
		assert.Equal(t, "push", claims["event_type"])
		assert.Equal(t, "base", claims["ci_origin"])
		assert.Equal(t, "trusted", claims["execution_profile"])
		assert.Equal(t, "rev-1", claims["policy_revision"])
		assert.Equal(t, []any{"default", "production"}, claims["ref_classes"])
		assert.Equal(t, models.SystemPrincipalID, claims["actor_principal_id"], "the trigger gave the job the system actor")
		assert.Equal(t, models.PrincipalKindSystem, claims["actor_kind"])
		assert.Equal(t, "", claims["owner_group"])
		assert.NotEmpty(t, claims["jti"])
		exp := int64(claims["exp"].(float64))
		assert.LessOrEqual(t, exp, time.Now().Add(11*time.Minute).Unix(), "the default lifetime is 10 minutes")
		assert.Equal(t, int64(1), auditCount(t, tx, f.org.OrgID, "oidc.token"))

		// A fork pull request job never gets production claims, even from a
		// fork branch named "main".
		pr := 7
		fork := &models.CIEvent{EventType: "pull_request_opened", Ref: "main", Branch: "main", CommitSHA: "bbb222", PRNumber: &pr}
		_, forkToken := f.runningJob(t, ctx, tx, fork, "untrusted", []string{"fork", "pr"})
		rr = callHandler(t, ctx, tokenRoute, http.MethodPost, "/api/v1/oidc/token", "Bearer "+forkToken, map[string]any{"audience": "sts.example.com"})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &issued))
		forkClaims := verifyJobToken(t, ctx, h, issued.Token, "sts.example.com")
		assert.Equal(t, "org:"+f.org.Name+":project:app:workflow:deploy:node:release:ref:refs/pull/7/head", forkClaims["sub"])
		assert.Equal(t, "refs/pull/7/head", forkClaims["ref"])
		assert.Equal(t, "main", forkClaims["head_ref"])
		assert.Equal(t, "untrusted", forkClaims["execution_profile"])
		assert.Equal(t, []any{"fork", "pr"}, forkClaims["ref_classes"])
		assert.Equal(t, float64(7), forkClaims["pr_number"])
		assert.NotEqual(t, claims["sub"], forkClaims["sub"])

		// Bad requests.
		rr = callHandler(t, ctx, tokenRoute, http.MethodPost, "/api/v1/oidc/token", "Bearer "+jobToken, map[string]any{"audience": ""})
		assert.Equal(t, http.StatusBadRequest, rr.Code)
		rr = callHandler(t, ctx, tokenRoute, http.MethodPost, "/api/v1/oidc/token", "Bearer "+jobToken, map[string]any{"audience": "a", "ttl_seconds": 7200})
		assert.Equal(t, http.StatusBadRequest, rr.Code)
		du := &DataUtils{db: tx}
		admin, err := du.CreateUser(DataSetup{"Username": uniqueName("oidc-admin"), "Email": "oidc@example.com"})
		require.NoError(t, err)
		adminAuth, err := createTokenAuthHeader(ctx, tx, admin.UserID)
		require.NoError(t, err)
		rr = callHandler(t, ctx, tokenRoute, http.MethodPost, "/api/v1/oidc/token", adminAuth, map[string]any{"audience": "a"})
		assert.Equal(t, http.StatusForbidden, rr.Code, "only a job token gets an OIDC token")

		// Extra claims per audience, and the org and project settings.
		settings := auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeSettings(w, r, f.org.Name, "") }))
		projectSettings := auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeSettings(w, r, f.org.Name, "projects/app") }))
		rr = callHandler(t, ctx, settings, http.MethodPut, "/", adminAuth, map[string]any{
			"audience_claims": map[string]map[string]string{"sts.example.com": {"sub": "forged"}}})
		assert.Equal(t, http.StatusBadRequest, rr.Code, "a standard claim cannot be overridden")
		rr = callHandler(t, ctx, settings, http.MethodPut, "/", adminAuth, map[string]any{"issuer": "deny",
			"audience_claims": map[string]map[string]string{"sts.example.com": {"team": "platform"}, "*": {"env": "prod"}}})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		assert.Contains(t, rr.Body.String(), `"effective_enabled":false`)
		rr = callHandler(t, ctx, tokenRoute, http.MethodPost, "/api/v1/oidc/token", "Bearer "+jobToken, map[string]any{"audience": "sts.example.com"})
		assert.Equal(t, http.StatusForbidden, rr.Code, "the org turned the issuer off")
		rr = callHandler(t, ctx, projectSettings, http.MethodPut, "/", adminAuth, map[string]any{"issuer": "allow"})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		assert.Contains(t, rr.Body.String(), `"effective_enabled":true`)
		rr = callHandler(t, ctx, tokenRoute, http.MethodPost, "/api/v1/oidc/token", "Bearer "+jobToken, map[string]any{"audience": "sts.example.com"})
		require.Equal(t, http.StatusOK, rr.Code, "a project allow wins over the org deny")
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &issued))
		claims = verifyJobToken(t, ctx, h, issued.Token, "sts.example.com")
		assert.Equal(t, "platform", claims["team"])
		assert.Equal(t, "prod", claims["env"])

		// A completed job gets no token.
		require.NoError(t, tx.Model(&models.Job{}).Where("job_id = ?", job.JobID).Update("status", "completed").Error)
		rr = callHandler(t, ctx, tokenRoute, http.MethodPost, "/api/v1/oidc/token", "Bearer "+jobToken, map[string]any{"audience": "sts.example.com"})
		assert.NotEqual(t, http.StatusOK, rr.Code)

		// Key rotation with real Postgres: the new key is published at once,
		// the old key still signs during the publish lead, and both stay in
		// the JWKS.
		keysRoute := auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeKeys(w, r, "rotate") }))
		rr = callHandler(t, ctx, keysRoute, http.MethodPost, "/api/v1/oidc/keys/rotate", "Bearer "+forkToken, map[string]any{})
		assert.Equal(t, http.StatusForbidden, rr.Code, "a job token cannot rotate keys")
		before, err := h.Issuer.JWKS(ctx)
		require.NoError(t, err)
		require.Len(t, before.Keys, 1)
		rr = callHandler(t, ctx, keysRoute, http.MethodPost, "/api/v1/oidc/keys/rotate", adminAuth, map[string]any{})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		assert.Contains(t, rr.Body.String(), `"state":"pending"`)
		assert.Contains(t, rr.Body.String(), `"state":"signing"`)
		after, err := h.Issuer.JWKS(ctx)
		require.NoError(t, err)
		assert.Len(t, after.Keys, 2)
		_, secondToken := f.runningJob(t, ctx, tx, &models.CIEvent{EventType: "push", Ref: "refs/heads/main", Branch: "main", CommitSHA: "ccc333"}, "trusted", nil)
		rr = callHandler(t, ctx, tokenRoute, http.MethodPost, "/api/v1/oidc/token", "Bearer "+secondToken, map[string]any{"audience": "a"})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &issued))
		parsed, err := jwt.ParseSigned(issued.Token, []jose.SignatureAlgorithm{jose.ES256})
		require.NoError(t, err)
		assert.Equal(t, before.Keys[0].KeyID, parsed.Headers[0].KeyID, "the old key signs until the new key activates")

		// The private key is stored encrypted.
		var stored []models.OIDCSigningKey
		require.NoError(t, tx.Find(&stored).Error)
		require.Len(t, stored, 2)
		for _, key := range stored {
			assert.NotContains(t, string(key.EncryptedPrivateKey), "PRIVATE KEY")
			assert.NotContains(t, key.PublicJWK, "d")
		}
	})
}

func TestOIDCIssuerOffByDefault(t *testing.T) {
	previous := config.OIDCIssuerURL
	config.OIDCIssuerURL = ""
	t.Cleanup(func() { config.OIDCIssuerURL = previous })
	h := handlers.NewOIDCHandler(store.AppStore, nil)
	assert.Nil(t, h.Issuer)
	rr := callHandler(t, context.Background(), http.HandlerFunc(h.JWKS), http.MethodGet, "/.well-known/jwks.json", "", nil)
	assert.Equal(t, http.StatusNotFound, rr.Code)

	config.OIDCIssuerURL = "http://ci.example.com"
	h = handlers.NewOIDCHandler(store.AppStore, &secrets.MasterKeyManager{})
	assert.Nil(t, h.Issuer, "an HTTP issuer URL is refused without the explicit serve option")
}

// TestFederationUIOperations drives the CSIL federation operations through the
// router-mounted UI service, as the Principals page does. The calls commit, so
// the test removes what it made.
func TestFederationUIOperations(t *testing.T) {
	initTestDB()
	require.NoError(t, initErr)
	adminValue := uniqueName("federation-admin-token")
	du := &DataUtils{db: testDB}
	adminToken, err := du.CreateAPIToken(DataSetup{"Name": "federation-ui-admin", "TokenHash": checkauth.HashAPIToken(adminValue),
		"IsActive": true, "SubjectType": "instance_token", "AllOrganizations": true, "AllCapabilities": true})
	require.NoError(t, err)
	readerValue := uniqueName("federation-reader-token")
	org := &models.Organization{Name: uniqueName("fed-ui-org"), DisplayName: "Fed UI"}
	require.NoError(t, postgres_store.PostgresStore.CreateOrganization(context.Background(), org))
	reader := &models.APIToken{TokenHash: checkauth.HashAPIToken(readerValue), Name: "fed-reader", IsActive: true, SubjectType: "service_token",
		OwnerOrgID: &org.OrgID, OrganizationIDs: []string{org.OrgID}, Capabilities: []string{tokencaps.OrganizationsRead}}
	require.NoError(t, postgres_store.PostgresStore.CreateAPIToken(context.Background(), reader))
	t.Cleanup(func() {
		testDB.Exec(`DELETE FROM federation_issuers WHERE org_id = ?`, org.OrgID)
		testDB.Exec(`DELETE FROM api_tokens WHERE token_id IN (?, ?)`, adminToken.TokenID, reader.TokenID)
		testDB.Exec(`DELETE FROM principals WHERE principal_id IN (?, ?) OR org_id = ?`, adminToken.TokenID, reader.TokenID, org.OrgID)
		testDB.Exec(`DELETE FROM audit_events WHERE org_id = ?`, org.OrgID)
		testDB.Exec(`DELETE FROM organizations WHERE org_id = ?`, org.OrgID)
	})
	handlers.ResetAppMux()
	defer handlers.ResetAppMux()
	mux := handlers.GetAppMux()

	_, svcErr := csilCall(t, mux, "ReactorcideUi", "list-federation-issuers", csilapi.EncodeListFederationIssuersRequest,
		csilapi.DecodeListFederationIssuersResponse, csilapi.ListFederationIssuersRequest{OrgId: org.OrgID}, readerValue)
	require.NotNil(t, svcErr, "a non-admin cannot read the federation settings")

	principal, svcErr := csilCall(t, mux, "ReactorcideUi", "create-service-principal", csilapi.EncodeCreateServicePrincipalRequest,
		csilapi.DecodeCreateServicePrincipalResponse, csilapi.CreateServicePrincipalRequest{OrgId: org.OrgID, Name: "deployer"}, adminValue)
	require.Nil(t, svcErr)

	_, svcErr = csilCall(t, mux, "ReactorcideUi", "save-federation-issuer", csilapi.EncodeSaveFederationIssuerRequest,
		csilapi.DecodeSaveFederationIssuerResponse, csilapi.SaveFederationIssuerRequest{OrgId: org.OrgID, Name: "plain",
			IssuerUrl: "http://plain.example.com", Audiences: []string{"reactorcide"}}, adminValue)
	require.NotNil(t, svcErr)
	assert.Equal(t, "invalid_argument", svcErr.Code)

	saved, svcErr := csilCall(t, mux, "ReactorcideUi", "save-federation-issuer", csilapi.EncodeSaveFederationIssuerRequest,
		csilapi.DecodeSaveFederationIssuerResponse, csilapi.SaveFederationIssuerRequest{OrgId: org.OrgID, Name: "gha",
			IssuerUrl: "https://token.actions.githubusercontent.com", Audiences: []string{"reactorcide"}}, adminValue)
	require.Nil(t, svcErr)
	assert.Equal(t, []string{"RS256", "ES256"}, saved.Issuer.Algorithms)

	mapping, svcErr := csilCall(t, mux, "ReactorcideUi", "save-federation-mapping", csilapi.EncodeSaveFederationMappingRequest,
		csilapi.DecodeSaveFederationMappingResponse, csilapi.SaveFederationMappingRequest{OrgId: org.OrgID, IssuerId: saved.Issuer.IssuerId,
			Name: "main", PrincipalId: principal.Principal.Principal.PrincipalId, Capabilities: []string{"jobs:submit"},
			Conditions: []csilapi.FederationConditionEntry{{Claim: "sub", Op: "glob", Value: "repo:acme/app:ref:refs/heads/*"}}}, adminValue)
	require.Nil(t, svcErr)
	assert.Equal(t, "deployer", mapping.Mapping.PrincipalName)
	assert.Equal(t, int64(900), mapping.Mapping.TokenTtlSeconds)

	listed, svcErr := csilCall(t, mux, "ReactorcideUi", "list-federation-issuers", csilapi.EncodeListFederationIssuersRequest,
		csilapi.DecodeListFederationIssuersResponse, csilapi.ListFederationIssuersRequest{OrgId: org.OrgID}, adminValue)
	require.Nil(t, svcErr)
	require.Len(t, listed.Issuers, 1)
	require.Len(t, listed.Mappings, 1)

	_, svcErr = csilCall(t, mux, "ReactorcideUi", "delete-federation-mapping", csilapi.EncodeDeleteFederationMappingRequest,
		csilapi.DecodeDeleteFederationMappingResponse, csilapi.DeleteFederationMappingRequest{OrgId: org.OrgID, MappingId: mapping.Mapping.MappingId}, adminValue)
	require.Nil(t, svcErr)
	_, svcErr = csilCall(t, mux, "ReactorcideUi", "delete-federation-issuer", csilapi.EncodeDeleteFederationIssuerRequest,
		csilapi.DecodeDeleteFederationIssuerResponse, csilapi.DeleteFederationIssuerRequest{OrgId: org.OrgID, IssuerId: saved.Issuer.IssuerId}, adminValue)
	require.Nil(t, svcErr)
}
