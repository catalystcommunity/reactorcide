package oidcissuer

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type memKeyStore struct {
	mu   sync.Mutex
	keys []models.OIDCSigningKey
}

func (m *memKeyStore) ListOIDCSigningKeys(context.Context) ([]models.OIDCSigningKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]models.OIDCSigningKey(nil), m.keys...), nil
}
func (m *memKeyStore) CreateOIDCSigningKey(_ context.Context, key *models.OIDCSigningKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keys = append(m.keys, *key)
	return nil
}
func (m *memKeyStore) RetireOIDCSigningKeys(_ context.Context, keep string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.keys {
		if m.keys[i].KID != keep && (m.keys[i].RetiresAt == nil || m.keys[i].RetiresAt.After(at)) {
			value := at
			m.keys[i].RetiresAt = &value
		}
	}
	return nil
}
func (m *memKeyStore) SetOIDCSigningKeyRetirement(_ context.Context, kid string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.keys {
		if m.keys[i].KID == kid {
			value := at
			m.keys[i].RetiresAt = &value
			return nil
		}
	}
	return errors.New("not found")
}
func (m *memKeyStore) MasterKeyIDByName(_ context.Context, name string) (string, error) {
	return "id-" + name, nil
}
func (m *memKeyStore) MasterKeyNameByID(_ context.Context, id string) (string, error) {
	return id[len("id-"):], nil
}
func (m *memKeyStore) WithOIDCKeyLock(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func masterKeys(t *testing.T) *secrets.MasterKeyManager {
	t.Helper()
	raw := make([]byte, 32)
	_, err := rand.Read(raw)
	require.NoError(t, err)
	t.Setenv(secrets.MasterKeysEnvVar, "test:"+base64.StdEncoding.EncodeToString(raw))
	mgr, err := secrets.LoadMasterKeys()
	require.NoError(t, err)
	return mgr
}

func verify(t *testing.T, issuer *Issuer, token string, audience string) map[string]any {
	t.Helper()
	set, err := issuer.JWKS(context.Background())
	require.NoError(t, err)
	parsed, err := jwt.ParseSigned(token, []jose.SignatureAlgorithm{jose.ES256, jose.RS256})
	require.NoError(t, err)
	keys := set.Key(parsed.Headers[0].KeyID)
	require.Len(t, keys, 1, "the signing key is in the JWKS")
	var std jwt.Claims
	all := map[string]any{}
	require.NoError(t, parsed.Claims(keys[0].Key, &std, &all))
	require.NoError(t, std.ValidateWithLeeway(jwt.Expected{Issuer: issuer.IssuerValue(), AnyAudience: []string{audience}, Time: issuer.now()}, 0))
	return all
}

func TestIssuerSignAndJWKS(t *testing.T) {
	for _, alg := range []string{AlgorithmES256, AlgorithmRS256} {
		t.Run(alg, func(t *testing.T) {
			store := &memKeyStore{}
			issuer := New("https://ci.example.com/api/v1/oidc/", alg, store, masterKeys(t))
			ctx := context.Background()
			token, err := issuer.Sign(ctx, "sts.example.com", map[string]any{"sub": "x", "iss": "forged"}, time.Now().Add(5*time.Minute))
			require.NoError(t, err)
			require.Len(t, store.keys, 1, "the first key is made on demand")
			claims := verify(t, issuer, token.Value, "sts.example.com")
			assert.Equal(t, "https://ci.example.com/api/v1/oidc", claims["iss"], "Sign sets iss")
			assert.Equal(t, "x", claims["sub"])
			assert.NotEmpty(t, claims["jti"])
			// The stored private key is encrypted, and a fresh issuer (another
			// replica) decrypts it.
			assert.NotContains(t, string(store.keys[0].EncryptedPrivateKey), "PRIVATE")
			replica := New(issuer.URL, alg, store, issuer.Crypto)
			again, err := replica.Sign(ctx, "a", map[string]any{"sub": "y"}, time.Now().Add(time.Minute))
			require.NoError(t, err)
			assert.Equal(t, token.KeyID, again.KeyID)
			verify(t, replica, again.Value, "a")
		})
	}
}

func TestIssuerRotationOverlap(t *testing.T) {
	store := &memKeyStore{}
	clock := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	issuer := New("https://ci.example.com", AlgorithmES256, store, masterKeys(t))
	issuer.Now = func() time.Time { return clock }
	ctx := context.Background()

	first, err := issuer.Sign(ctx, "aud", map[string]any{"sub": "s"}, clock.Add(10*time.Minute))
	require.NoError(t, err)

	rotated, err := issuer.Rotate(ctx, false)
	require.NoError(t, err)
	set, err := issuer.JWKS(ctx)
	require.NoError(t, err)
	assert.Len(t, set.Keys, 2, "the new key is published before use")
	assert.Len(t, set.Key(rotated.KID), 1)

	// During the publish lead the old key still signs.
	clock = clock.Add(30 * time.Minute)
	during, err := issuer.Sign(ctx, "aud", map[string]any{"sub": "s"}, clock.Add(10*time.Minute))
	require.NoError(t, err)
	assert.Equal(t, first.KeyID, during.KeyID)

	// After the lead the new key signs; the old key stays in the JWKS.
	clock = clock.Add(31 * time.Minute)
	after, err := issuer.Sign(ctx, "aud", map[string]any{"sub": "s"}, clock.Add(10*time.Minute))
	require.NoError(t, err)
	assert.Equal(t, rotated.KID, after.KeyID)
	set, err = issuer.JWKS(ctx)
	require.NoError(t, err)
	assert.Len(t, set.Keys, 2, "the old key overlaps for tokens it signed")
	assert.Len(t, set.Key(during.KeyID), 1)

	// After RetireAfter the old key leaves the JWKS.
	clock = clock.Add(25 * time.Hour)
	set, err = issuer.JWKS(ctx)
	require.NoError(t, err)
	require.Len(t, set.Keys, 1)
	assert.Equal(t, rotated.KID, set.Keys[0].KeyID)
}

func TestIssuerAutomaticRotationAndRetire(t *testing.T) {
	store := &memKeyStore{}
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	issuer := New("https://ci.example.com", AlgorithmES256, store, masterKeys(t))
	issuer.Now = func() time.Time { return clock }
	issuer.RotationPeriod = 24 * time.Hour
	ctx := context.Background()
	_, err := issuer.JWKS(ctx)
	require.NoError(t, err)
	require.Len(t, store.keys, 1)

	clock = clock.Add(25 * time.Hour)
	set, err := issuer.JWKS(ctx)
	require.NoError(t, err)
	assert.Len(t, store.keys, 2, "an old key starts an automatic rotation")
	assert.Len(t, set.Keys, 2)
	_, err = issuer.JWKS(ctx)
	require.NoError(t, err)
	assert.Len(t, store.keys, 2, "one rotation only")

	// A compromised key is retired at once; a new key signs at once.
	activeKey := active(store.keys, clock)
	require.NotNil(t, activeKey)
	require.NoError(t, issuer.Retire(ctx, activeKey.KID, clock))
	require.NoError(t, issuer.Retire(ctx, store.keys[1].KID, clock))
	token, err := issuer.Sign(ctx, "aud", map[string]any{"sub": "s"}, clock.Add(time.Minute))
	require.NoError(t, err)
	assert.NotEqual(t, activeKey.KID, token.KeyID)
	set, err = issuer.JWKS(ctx)
	require.NoError(t, err)
	assert.Empty(t, set.Key(activeKey.KID), "a retired key is not published")
}

func TestIssuerDisabled(t *testing.T) {
	issuer := New("", "", &memKeyStore{}, nil)
	assert.False(t, issuer.Enabled())
	_, err := issuer.JWKS(context.Background())
	assert.ErrorIs(t, err, ErrDisabled)
	_, err = issuer.Sign(context.Background(), "a", nil, time.Now())
	assert.ErrorIs(t, err, ErrDisabled)
}

func TestDiscovery(t *testing.T) {
	issuer := New("https://ci.example.com/api/v1/oidc", "", &memKeyStore{}, nil)
	doc := issuer.Discovery()
	assert.Equal(t, "https://ci.example.com/api/v1/oidc", doc["issuer"])
	assert.Equal(t, "https://ci.example.com/api/v1/oidc/.well-known/jwks.json", doc["jwks_uri"])
	assert.Contains(t, doc["claims_supported"], "ci_origin")
	assert.Contains(t, doc["id_token_signing_alg_values_supported"], "ES256")
}

func TestClaims(t *testing.T) {
	production := JobFacts{Org: "acme", Project: "app", WorkflowSecurityID: "deploy", Node: "release",
		Ref: "refs/heads/main", SHA: "abc", EventType: "push", CIOrigin: "base", ExecutionProfile: "trusted",
		RefClasses: []string{"production", "default"}, ActorPrincipalID: "p1", ActorKind: "human"}
	claims := Claims(production, map[string]string{"team": "platform", "sub": "forged"})
	assert.Equal(t, "org:acme:project:app:workflow:deploy:node:release:ref:refs/heads/main", claims["sub"])
	assert.Equal(t, []string{"default", "production"}, claims["ref_classes"])
	assert.Equal(t, "platform", claims["team"])
	assert.Equal(t, "", claims["owner_group"])
	assert.NotContains(t, claims, "pr_number")

	// A fork pull request job never gets the claims of a production job,
	// even when the fork branch is named "main".
	fork := JobFacts{Org: "acme", Project: "app", WorkflowSecurityID: "deploy", Node: "release",
		Ref: "refs/pull/7/head", HeadRef: "main", BaseRef: "main", EventType: "pull_request_opened", PRNumber: 7,
		CIOrigin: "base", ExecutionProfile: "untrusted", RefClasses: []string{"fork", "pr"}}
	forkClaims := Claims(fork, nil)
	assert.Equal(t, "org:acme:project:app:workflow:deploy:node:release:ref:refs/pull/7/head", forkClaims["sub"])
	assert.NotEqual(t, claims["sub"], forkClaims["sub"])
	assert.Equal(t, "untrusted", forkClaims["execution_profile"])
	assert.Equal(t, []string{"fork", "pr"}, forkClaims["ref_classes"])
	assert.Equal(t, 7, forkClaims["pr_number"])

	// A ":" in a part is escaped, so a node name cannot forge a ref part.
	tricky := JobFacts{Org: "acme", Project: "app", Node: "x:ref:refs/heads/main", Ref: "refs/pull/1/head"}
	assert.Equal(t, "org:acme:project:app:workflow::node:x%3Aref%3Arefs/heads/main:ref:refs/pull/1/head", Subject(tricky))
}

func TestAudienceClaims(t *testing.T) {
	assert.NoError(t, ValidateAudienceClaims(map[string]map[string]string{"*": {"team": "a"}, "sts.amazonaws.com": {"env": "prod"}}))
	assert.Error(t, ValidateAudienceClaims(map[string]map[string]string{"x": {"sub": "forged"}}))
	assert.Error(t, ValidateAudienceClaims(map[string]map[string]string{"x": {"Bad-Name": "v"}}))
	merged := ExtraClaimsFor("sts", map[string]map[string]string{"*": {"team": "org", "env": "o"}, "sts": {"env": "org-sts"}},
		map[string]map[string]string{"*": {"team": "project"}})
	assert.Equal(t, map[string]string{"team": "project", "env": "org-sts"}, merged)
	assert.True(t, ValidAudience("sts.amazonaws.com"))
	assert.False(t, ValidAudience(""))
	assert.False(t, ValidAudience("*"))
	assert.False(t, ValidAudience("a b"))
}
