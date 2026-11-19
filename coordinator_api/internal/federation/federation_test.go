package federation

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testIssuer is an httptest OIDC issuer with a discovery document and a
// JWKS that a test can change.
type testIssuer struct {
	server      *httptest.Server
	mu          sync.Mutex
	keys        []jose.JSONWebKey
	jwksFetches atomic.Int32
	discoveries atomic.Int32
}

func newTestIssuer(t *testing.T) *testIssuer {
	t.Helper()
	ti := &testIssuer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		ti.discoveries.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": ti.server.URL, "jwks_uri": ti.server.URL + "/keys"})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		ti.jwksFetches.Add(1)
		ti.mu.Lock()
		set := jose.JSONWebKeySet{Keys: append([]jose.JSONWebKey(nil), ti.keys...)}
		ti.mu.Unlock()
		_ = json.NewEncoder(w).Encode(set)
	})
	ti.server = httptest.NewTLSServer(mux)
	t.Cleanup(ti.server.Close)
	return ti
}

func (ti *testIssuer) publish(key jose.JSONWebKey) {
	ti.mu.Lock()
	defer ti.mu.Unlock()
	ti.keys = append(ti.keys, key.Public())
}

func (ti *testIssuer) cache() *JWKSCache {
	cache := NewJWKSCache(false)
	cache.Client = ti.server.Client()
	return cache
}

func (ti *testIssuer) record() *models.FederationIssuer {
	return &models.FederationIssuer{IssuerID: "00000000-0000-0000-0000-0000000000aa", Name: "test", IssuerURL: ti.server.URL,
		Audiences: []string{"reactorcide"}, Algorithms: []string{"ES256", "RS256"}, ClockSkewSeconds: 30,
		Status: models.FederationStatusActive}
}

func ecKey(t *testing.T, kid string) jose.JSONWebKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	return jose.JSONWebKey{Key: key, KeyID: kid, Algorithm: "ES256", Use: "sig"}
}

func rsaKey(t *testing.T, kid string) jose.JSONWebKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return jose.JSONWebKey{Key: key, KeyID: kid, Algorithm: "RS256", Use: "sig"}
}

func sign(t *testing.T, key jose.JSONWebKey, claims map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.SignatureAlgorithm(key.Algorithm), Key: key},
		(&jose.SignerOptions{}).WithType("JWT"))
	require.NoError(t, err)
	token, err := jwt.Signed(signer).Claims(claims).Serialize()
	require.NoError(t, err)
	return token
}

func baseClaims(issuer string, now time.Time) map[string]any {
	return map[string]any{
		"iss": issuer, "sub": "repo:acme/app:ref:refs/heads/main", "aud": "reactorcide",
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
		"repository": "acme/app", "run_number": 42,
	}
}

func TestVerifier(t *testing.T) {
	ti := newTestIssuer(t)
	key := ecKey(t, "k1")
	ti.publish(key)
	rsaSigner := rsaKey(t, "r1")
	ti.publish(rsaSigner)
	now := time.Now()
	verifier := NewVerifier(ti.cache())
	ctx := context.Background()

	t.Run("valid token through discovery", func(t *testing.T) {
		claims, err := verifier.Verify(ctx, ti.record(), sign(t, key, baseClaims(ti.server.URL, now)))
		require.NoError(t, err)
		assert.Equal(t, "repo:acme/app:ref:refs/heads/main", claims.Subject)
		assert.Equal(t, "acme/app", claims.All["repository"])
	})

	cases := []struct {
		name   string
		mutate func(map[string]any)
		want   error
	}{
		{"expired", func(c map[string]any) {
			c["exp"] = now.Add(-time.Hour).Unix()
			c["iat"] = now.Add(-2 * time.Hour).Unix()
		}, ErrExpired},
		{"wrong audience", func(c map[string]any) { c["aud"] = "someone-else" }, ErrAudience},
		{"wrong issuer", func(c map[string]any) { c["iss"] = "https://evil.example.com" }, ErrIssuerMismatch},
		{"not yet valid", func(c map[string]any) { c["nbf"] = now.Add(time.Hour).Unix() }, ErrNotYetValid},
		{"issued in the future", func(c map[string]any) { c["iat"] = now.Add(time.Hour).Unix() }, ErrIssuedInFuture},
		{"no exp", func(c map[string]any) { delete(c, "exp") }, ErrMissingClaim},
		{"no sub", func(c map[string]any) { delete(c, "sub") }, ErrMissingClaim},
		{"too long", func(c map[string]any) { c["exp"] = now.Add(48 * time.Hour).Unix() }, ErrLifetimeTooLong},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims := baseClaims(ti.server.URL, now)
			tc.mutate(claims)
			_, err := verifier.Verify(ctx, ti.record(), sign(t, key, claims))
			assert.ErrorIs(t, err, tc.want)
		})
	}

	t.Run("within clock skew", func(t *testing.T) {
		claims := baseClaims(ti.server.URL, now)
		claims["exp"] = now.Add(-10 * time.Second).Unix()
		_, err := verifier.Verify(ctx, ti.record(), sign(t, key, claims))
		assert.NoError(t, err)
	})

	t.Run("an algorithm the issuer does not allow", func(t *testing.T) {
		rsa := rsaSigner
		record := ti.record()
		record.Algorithms = []string{"ES256"}
		_, err := verifier.Verify(ctx, record, sign(t, rsa, baseClaims(ti.server.URL, now)))
		assert.ErrorIs(t, err, ErrAlgorithm)
		record.Algorithms = []string{"RS256"}
		_, err = verifier.Verify(ctx, record, sign(t, rsa, baseClaims(ti.server.URL, now)))
		assert.NoError(t, err)
	})

	t.Run("HMAC and none are refused", func(t *testing.T) {
		hmac := jose.JSONWebKey{Key: []byte("0123456789abcdef0123456789abcdef"), KeyID: "k1", Algorithm: "HS256"}
		_, err := verifier.Verify(ctx, ti.record(), sign(t, hmac, baseClaims(ti.server.URL, now)))
		assert.ErrorIs(t, err, ErrAlgorithm)
		// alg "none": header {"alg":"none","typ":"JWT"}
		none := "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0." + "eyJzdWIiOiJ4In0."
		_, err = verifier.Verify(ctx, ti.record(), none)
		assert.ErrorIs(t, err, ErrAlgorithm)
		_, err = verifier.Verify(ctx, ti.record(), "not-a-jwt")
		assert.ErrorIs(t, err, ErrMalformedToken)
	})

	t.Run("a key that is not in the JWKS", func(t *testing.T) {
		other := ecKey(t, "k1") // same kid, other key material
		_, err := verifier.Verify(ctx, ti.record(), sign(t, other, baseClaims(ti.server.URL, now)))
		assert.ErrorIs(t, err, ErrSignature)
		unknown := ecKey(t, "unknown")
		_, err = verifier.Verify(ctx, ti.record(), sign(t, unknown, baseClaims(ti.server.URL, now)))
		assert.ErrorIs(t, err, ErrUnknownKey)
	})

	t.Run("a disabled issuer", func(t *testing.T) {
		record := ti.record()
		record.Status = models.FederationStatusDisabled
		_, err := verifier.Verify(ctx, record, sign(t, key, baseClaims(ti.server.URL, now)))
		assert.ErrorIs(t, err, ErrIssuerDisabled)
	})
}

func TestJWKSCache(t *testing.T) {
	ti := newTestIssuer(t)
	key := ecKey(t, "k1")
	ti.publish(key)
	clock := time.Now()
	cache := ti.cache()
	cache.Now = func() time.Time { return clock }
	verifier := &Verifier{Keys: cache, Now: func() time.Time { return clock }}
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		_, err := verifier.Verify(ctx, ti.record(), sign(t, key, baseClaims(ti.server.URL, clock)))
		require.NoError(t, err)
	}
	assert.Equal(t, int32(1), ti.jwksFetches.Load(), "a cached JWKS is used again")

	// The issuer rotates in a new key. An unknown kid forces one refresh.
	clock = clock.Add(time.Minute)
	rotated := ecKey(t, "k2")
	ti.publish(rotated)
	_, err := verifier.Verify(ctx, ti.record(), sign(t, rotated, baseClaims(ti.server.URL, clock)))
	require.NoError(t, err)
	assert.Equal(t, int32(2), ti.jwksFetches.Load())

	// A random unknown kid does not make a fetch for each request.
	for i := 0; i < 3; i++ {
		_, err = verifier.Verify(ctx, ti.record(), sign(t, ecKey(t, "random"), baseClaims(ti.server.URL, clock)))
		assert.ErrorIs(t, err, ErrUnknownKey)
	}
	assert.Equal(t, int32(2), ti.jwksFetches.Load())

	// The TTL expires.
	clock = clock.Add(11 * time.Minute)
	_, err = verifier.Verify(ctx, ti.record(), sign(t, key, baseClaims(ti.server.URL, clock)))
	require.NoError(t, err)
	assert.Equal(t, int32(3), ti.jwksFetches.Load())

	// An explicit JWKS URL skips discovery.
	discoveries := ti.discoveries.Load()
	record := ti.record()
	record.JWKSURL = ti.server.URL + "/keys"
	_, err = verifier.Verify(ctx, record, sign(t, key, baseClaims(ti.server.URL, clock)))
	require.NoError(t, err)
	assert.Equal(t, discoveries, ti.discoveries.Load())
}

func TestJWKSTransportRule(t *testing.T) {
	plain := httptest.NewServer(http.NotFoundHandler())
	defer plain.Close()
	record := &models.FederationIssuer{IssuerURL: plain.URL, JWKSURL: plain.URL + "/keys", Audiences: []string{"x"},
		Algorithms: []string{"ES256"}, Status: models.FederationStatusActive}
	_, err := NewJWKSCache(false).Keys(context.Background(), record, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--allow-insecure-transport")
	_, err = NewJWKSCache(true).Keys(context.Background(), record, false)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "--allow-insecure-transport")
}

func TestDiscoveryIssuerMismatch(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewTLSServer(mux)
	defer server.Close()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": "https://other.example.com", "jwks_uri": server.URL + "/keys"})
	})
	cache := NewJWKSCache(false)
	cache.Client = server.Client()
	_, err := cache.Keys(context.Background(), &models.FederationIssuer{IssuerURL: server.URL}, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "discovery document names issuer")
}

func TestMappings(t *testing.T) {
	claims := map[string]any{"sub": "repo:acme/app:ref:refs/heads/main", "repository": "acme/app", "run": float64(42), "list": []any{"a"}}
	cond := func(claim, op, value string) models.FederationCondition {
		return models.FederationCondition{Claim: claim, Op: op, Value: value}
	}
	assert.True(t, Matches([]models.FederationCondition{cond("sub", "glob", "repo:acme/app:ref:refs/heads/*")}, claims))
	assert.True(t, Matches([]models.FederationCondition{cond("sub", "glob", "repo:acme/*"), cond("repository", "equals", "acme/app")}, claims))
	assert.False(t, Matches([]models.FederationCondition{cond("sub", "glob", "repo:acme/*"), cond("repository", "equals", "acme/other")}, claims))
	assert.True(t, Matches([]models.FederationCondition{cond("run", "equals", "42")}, claims))
	assert.False(t, Matches([]models.FederationCondition{cond("list", "equals", "a")}, claims), "a list never matches")
	assert.False(t, Matches([]models.FederationCondition{cond("missing", "equals", "")}, claims))
	assert.False(t, Matches(nil, claims))

	mappings := []models.FederationMapping{
		{MappingID: "m1", PrincipalID: "p1", Status: "active", Conditions: []models.FederationCondition{cond("sub", "equals", "repo:acme/app:ref:refs/heads/main")}},
		{MappingID: "m2", PrincipalID: "p2", Status: "active", Conditions: []models.FederationCondition{cond("sub", "glob", "repo:acme/app:*")}},
		{MappingID: "m3", PrincipalID: "p3", Status: "disabled", Conditions: []models.FederationCondition{cond("sub", "glob", "*main")}},
	}
	_, err := SelectMapping(mappings, claims, "")
	assert.ErrorIs(t, err, ErrAmbiguousMapping)
	got, err := SelectMapping(mappings, claims, "p2")
	require.NoError(t, err)
	assert.Equal(t, "m2", got.MappingID)
	_, err = SelectMapping(mappings, claims, "p3")
	assert.ErrorIs(t, err, ErrNotMapped, "a disabled mapping never matches")
	_, err = SelectMapping(mappings[:2], map[string]any{"sub": "repo:other/app"}, "")
	assert.ErrorIs(t, err, ErrNotMapped)
}

func TestGlob(t *testing.T) {
	for _, tc := range []struct {
		pattern, value string
		want           bool
	}{
		{"*", "", true},
		{"a*c", "abc", true},
		{"a*c", "a/b:c", true},
		{"a?c", "abc", true},
		{"a?c", "ac", false},
		{"repo:acme/*:ref:refs/heads/main", "repo:acme/app:ref:refs/heads/main", true},
		{"repo:acme/*:ref:refs/heads/main", "repo:acme/app:ref:refs/heads/main2", false},
		{"*x*y", "axbxcy", true},
		{"abc", "abcd", false},
	} {
		assert.Equal(t, tc.want, Glob(tc.pattern, tc.value), "%s ~ %s", tc.pattern, tc.value)
	}
}

func TestValidateConditions(t *testing.T) {
	ok := []models.FederationCondition{{Claim: "sub", Op: "glob", Value: "repo:acme/*"}}
	assert.NoError(t, ValidateConditions(ok))
	assert.Error(t, ValidateConditions(nil))
	assert.Error(t, ValidateConditions([]models.FederationCondition{{Claim: "repository", Op: "equals", Value: "acme/app"}}), "needs sub")
	assert.Error(t, ValidateConditions([]models.FederationCondition{{Claim: "sub", Op: "glob", Value: "*"}}), "sub must not be any")
	// A sub glob must fix the owner of the subject.
	for _, refused := range []string{"repo:*", "repo:acme*", "repo:*/app:ref:refs/heads/main", "*:ref:refs/heads/main", "repo::*", "repo:?cme/*", ":acme/*"} {
		assert.Error(t, ValidateConditions([]models.FederationCondition{{Claim: "sub", Op: "glob", Value: refused}}), refused)
	}
	for _, accepted := range []string{"repo:acme/*", "repo:acme/app:*", "project_path:group/app:ref_type:branch:ref:*", "repo:acme/app:ref:refs/heads/main"} {
		assert.NoError(t, ValidateConditions([]models.FederationCondition{{Claim: "sub", Op: "glob", Value: accepted}}), accepted)
	}
	// An equals condition on another claim fixes the owner instead.
	assert.NoError(t, ValidateConditions([]models.FederationCondition{{Claim: "sub", Op: "glob", Value: "repo:*"},
		{Claim: "repository_owner_id", Op: "equals", Value: "12345"}}))
	assert.Error(t, ValidateConditions([]models.FederationCondition{{Claim: "sub", Op: "glob", Value: "repo:*"},
		{Claim: "repository", Op: "glob", Value: "acme/*"}}), "a glob on another claim does not fix the owner")
	assert.Error(t, ValidateConditions([]models.FederationCondition{{Claim: "sub", Op: "regex", Value: "x"}}))
	assert.Error(t, ValidateConditions([]models.FederationCondition{{Claim: "bad claim", Op: "equals", Value: "x"}}))
	parsed, err := ParseCondition("repository=acme/app")
	require.NoError(t, err)
	assert.Equal(t, models.FederationCondition{Claim: "repository", Op: "equals", Value: "acme/app"}, parsed)
	parsed, err = ParseCondition("sub~repo:acme/*")
	require.NoError(t, err)
	assert.Equal(t, "glob", parsed.Op)
	parsed, err = ParseCondition("sub=a~b")
	require.NoError(t, err)
	assert.Equal(t, models.FederationCondition{Claim: "sub", Op: "equals", Value: "a~b"}, parsed)
	_, err = ParseCondition("novalue")
	assert.Error(t, err)
}

// fakeStore is an in-memory exchange store.
type fakeStore struct {
	issuer    *models.FederationIssuer
	mappings  []models.FederationMapping
	principal *models.Principal
	tokens    []*models.APIToken
	identity  string
}

func (f *fakeStore) GetFederationIssuerByURL(_ context.Context, orgID, url string) (*models.FederationIssuer, error) {
	if f.issuer == nil || f.issuer.OrgID != orgID || f.issuer.IssuerURL != url {
		return nil, errors.New("not found")
	}
	return f.issuer, nil
}
func (f *fakeStore) ListFederationMappings(context.Context, string, string) ([]models.FederationMapping, error) {
	return f.mappings, nil
}
func (f *fakeStore) GetPrincipal(context.Context, string) (*models.Principal, error) {
	return f.principal, nil
}
func (f *fakeStore) CreateFederatedToken(_ context.Context, token *models.APIToken, _ *models.FederationIssuedToken, identity string) error {
	token.TokenID = "token-1"
	f.tokens = append(f.tokens, token)
	f.identity = identity
	return nil
}
func (f *fakeStore) DeleteExpiredFederatedTokens(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func TestExchange(t *testing.T) {
	ti := newTestIssuer(t)
	key := ecKey(t, "k1")
	ti.publish(key)
	orgID := "00000000-0000-0000-0000-0000000000bb"
	issuer := ti.record()
	issuer.OrgID = orgID
	principal := &models.Principal{PrincipalID: "p1", Kind: models.PrincipalKindService, OrgID: &orgID, Name: "deployer", Status: "active"}
	fs := &fakeStore{issuer: issuer, principal: principal, mappings: []models.FederationMapping{{
		MappingID: "m1", Name: "main", PrincipalID: "p1", Status: "active", TokenTTLSeconds: 900,
		Capabilities: []string{"jobs:submit", "jobs:read"},
		Conditions:   []models.FederationCondition{{Claim: "sub", Op: "glob", Value: "repo:acme/app:ref:refs/heads/*"}},
	}}}
	now := time.Now()
	x := &Exchanger{Store: fs, Verifier: NewVerifier(ti.cache())}
	ctx := context.Background()

	result, err := x.Exchange(ctx, ExchangeRequest{OrgID: orgID, Token: sign(t, key, baseClaims(ti.server.URL, now))})
	require.NoError(t, err)
	assert.NotEmpty(t, result.Token)
	assert.Equal(t, "p1", *fs.tokens[0].PrincipalID)
	assert.True(t, fs.tokens[0].IntersectRoles)
	assert.False(t, fs.tokens[0].AllCapabilities)
	assert.ElementsMatch(t, []string{"jobs:submit", "jobs:read"}, fs.tokens[0].Capabilities)
	assert.Equal(t, ti.server.URL+"|repo:acme/app:ref:refs/heads/main", fs.identity)
	// The token never outlives the external token (exp = now+5m < 15m TTL).
	assert.WithinDuration(t, now.Add(5*time.Minute), result.ExpiresAt, 2*time.Second)

	// A requested subset narrows the capabilities; a superset is refused.
	result, err = x.Exchange(ctx, ExchangeRequest{OrgID: orgID, Token: sign(t, key, baseClaims(ti.server.URL, now)), Capabilities: []string{"jobs:read"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"jobs:read"}, result.Capabilities)
	_, err = x.Exchange(ctx, ExchangeRequest{OrgID: orgID, Token: sign(t, key, baseClaims(ti.server.URL, now)), Capabilities: []string{"tokens:manage"}})
	assert.ErrorIs(t, err, ErrCapabilities)

	// An unmapped subject.
	claims := baseClaims(ti.server.URL, now)
	claims["sub"] = "repo:acme/other:ref:refs/heads/main"
	_, err = x.Exchange(ctx, ExchangeRequest{OrgID: orgID, Token: sign(t, key, claims)})
	var exchangeErr *ExchangeError
	require.ErrorAs(t, err, &exchangeErr)
	assert.ErrorIs(t, err, ErrNotMapped)
	assert.Equal(t, "repo:acme/other:ref:refs/heads/main", exchangeErr.Subject)

	// An issuer of another organization.
	_, err = x.Exchange(ctx, ExchangeRequest{OrgID: "00000000-0000-0000-0000-0000000000cc", Token: sign(t, key, baseClaims(ti.server.URL, now))})
	assert.ErrorIs(t, err, ErrUnknownIssuer)

	// A disabled principal.
	principal.Status = models.PrincipalStatusDisabled
	_, err = x.Exchange(ctx, ExchangeRequest{OrgID: orgID, Token: sign(t, key, baseClaims(ti.server.URL, now))})
	assert.ErrorIs(t, err, ErrPrincipal)
}

// The issuer limit counts only verified tokens, and a refusal issues no token.
func TestExchangeIssuerLimit(t *testing.T) {
	ti := newTestIssuer(t)
	key := ecKey(t, "k1")
	ti.publish(key)
	orgID := "00000000-0000-0000-0000-0000000000bb"
	issuer := ti.record()
	issuer.OrgID = orgID
	principal := &models.Principal{PrincipalID: "p1", Kind: models.PrincipalKindService, OrgID: &orgID, Name: "deployer", Status: "active"}
	fs := &fakeStore{issuer: issuer, principal: principal, mappings: []models.FederationMapping{{
		MappingID: "m1", Name: "main", PrincipalID: "p1", Status: "active", TokenTTLSeconds: 900,
		Capabilities: []string{"jobs:read"},
		Conditions:   []models.FederationCondition{{Claim: "sub", Op: "glob", Value: "repo:acme/app:ref:refs/heads/*"}},
	}}}
	calls := 0
	x := &Exchanger{Store: fs, Verifier: NewVerifier(ti.cache()), AllowIssuer: func(*models.FederationIssuer) (bool, time.Duration) {
		calls++
		return calls <= 1, 30 * time.Second
	}}
	ctx := context.Background()
	now := time.Now()

	// A token with a bad signature does not count.
	other := ecKey(t, "other")
	_, err := x.Exchange(ctx, ExchangeRequest{OrgID: orgID, Token: sign(t, other, baseClaims(ti.server.URL, now))})
	require.Error(t, err)
	assert.Equal(t, 0, calls)

	_, err = x.Exchange(ctx, ExchangeRequest{OrgID: orgID, Token: sign(t, key, baseClaims(ti.server.URL, now))})
	require.NoError(t, err)
	_, err = x.Exchange(ctx, ExchangeRequest{OrgID: orgID, Token: sign(t, key, baseClaims(ti.server.URL, now))})
	assert.ErrorIs(t, err, ErrRateLimited)
	var refusal *ExchangeError
	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, 30*time.Second, refusal.RetryAfter)
	assert.Len(t, fs.tokens, 1, "a refused exchange issues no token")
}
