// Package oidcissuer makes Reactorcide a generic OIDC issuer for jobs
// (docs/oidc.md). A running job asks for a token for an audience; the
// coordinator signs a JWT with claims that describe the job. Consumers (a
// cloud provider, a secret store, a cache) verify it with the public JWKS.
//
// The signing keys stay with the coordinator. They are stored encrypted under
// a master key. The package uses go-jose for every cryptographic operation.
package oidcissuer

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// Signing algorithms of the issuer.
const (
	AlgorithmES256 = "ES256"
	AlgorithmRS256 = "RS256"
)

// Token lifetimes.
const (
	DefaultTokenTTL = 10 * time.Minute
	MinTokenTTL     = time.Minute
	MaxTokenTTL     = time.Hour
)

// ErrDisabled is returned when the issuer URL is not configured.
var ErrDisabled = errors.New("the OIDC issuer is not configured")

// KeyStore is the store surface of the signing keys.
type KeyStore interface {
	ListOIDCSigningKeys(ctx context.Context) ([]models.OIDCSigningKey, error)
	CreateOIDCSigningKey(ctx context.Context, key *models.OIDCSigningKey) error
	// RetireOIDCSigningKeys sets retires_at on every key that has no
	// retirement yet, except the key named keep.
	RetireOIDCSigningKeys(ctx context.Context, keep string, retiresAt time.Time) error
	SetOIDCSigningKeyRetirement(ctx context.Context, kid string, retiresAt time.Time) error
	MasterKeyIDByName(ctx context.Context, name string) (string, error)
	MasterKeyNameByID(ctx context.Context, keyID string) (string, error)
	// WithOIDCKeyLock runs fn while it holds a database lock for key
	// changes, so that two replicas do not make two keys at once.
	WithOIDCKeyLock(ctx context.Context, fn func(ctx context.Context) error) error
}

// Encryptor encrypts private keys at rest. secrets.MasterKeyManager
// implements it.
type Encryptor interface {
	EncryptWithPrimary(plaintext []byte) (keyName string, ciphertext []byte, err error)
	DecryptWithKey(keyName string, ciphertext []byte) ([]byte, error)
}

// Issuer signs job tokens and publishes its keys.
type Issuer struct {
	// URL is the "iss" value. Empty disables the issuer.
	URL       string
	Algorithm string
	Store     KeyStore
	Crypto    Encryptor
	// PublishLead is how long a rotated key is in the JWKS before the
	// issuer signs with it.
	PublishLead time.Duration
	// RetireAfter is how long an old key stays in the JWKS after the next
	// key starts to sign. It must be longer than MaxTokenTTL plus the JWKS
	// cache time of consumers.
	RetireAfter time.Duration
	// RotationPeriod makes the issuer rotate automatically when the newest
	// key is older than this. Zero turns automatic rotation off.
	RotationPeriod time.Duration
	Now            func() time.Time

	mu      sync.Mutex
	signers map[string]crypto.Signer
}

// New returns an issuer with default timings.
func New(url, algorithm string, store KeyStore, encryptor Encryptor) *Issuer {
	if algorithm == "" {
		algorithm = AlgorithmES256
	}
	return &Issuer{URL: strings.TrimSpace(url), Algorithm: algorithm, Store: store, Crypto: encryptor,
		PublishLead: time.Hour, RetireAfter: 24 * time.Hour, RotationPeriod: 90 * 24 * time.Hour, Now: time.Now}
}

// Enabled reports whether the issuer is configured.
func (i *Issuer) Enabled() bool { return i != nil && i.URL != "" && i.Store != nil && i.Crypto != nil }

func (i *Issuer) now() time.Time {
	if i.Now != nil {
		return i.Now().UTC()
	}
	return time.Now().UTC()
}

// JWKSPath and DiscoveryPath are the paths under the issuer URL.
const (
	DiscoveryPath = "/.well-known/openid-configuration"
	JWKSPath      = "/.well-known/jwks.json"
)

// Discovery returns the OpenID Provider metadata document.
func (i *Issuer) Discovery() map[string]any {
	base := strings.TrimRight(i.URL, "/")
	return map[string]any{
		"issuer":                                base,
		"jwks_uri":                              base + JWKSPath,
		"response_types_supported":              []string{"id_token"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{AlgorithmES256, AlgorithmRS256},
		"scopes_supported":                      []string{"openid"},
		"claims_supported":                      ClaimNames(),
	}
}

// IssuerValue returns the "iss" claim (the URL without a trailing slash).
func (i *Issuer) IssuerValue() string { return strings.TrimRight(i.URL, "/") }

// JWKS returns the public keys that consumers must accept now: every key
// that is not retired, including a key that does not sign yet.
func (i *Issuer) JWKS(ctx context.Context) (*jose.JSONWebKeySet, error) {
	if !i.Enabled() {
		return nil, ErrDisabled
	}
	keys, err := i.ensureKeys(ctx)
	if err != nil {
		return nil, err
	}
	now := i.now()
	set := &jose.JSONWebKeySet{Keys: []jose.JSONWebKey{}}
	for _, key := range keys {
		if key.RetiresAt != nil && !key.RetiresAt.After(now) {
			continue
		}
		var jwk jose.JSONWebKey
		raw, _ := json.Marshal(key.PublicJWK)
		if err := jwk.UnmarshalJSON(raw); err != nil {
			return nil, fmt.Errorf("oidc: key %s has an invalid public JWK: %w", key.KID, err)
		}
		set.Keys = append(set.Keys, jwk)
	}
	return set, nil
}

// ensureKeys returns the keys. It makes the first key when there is none, and
// it starts an automatic rotation when the newest key is too old.
func (i *Issuer) ensureKeys(ctx context.Context) ([]models.OIDCSigningKey, error) {
	keys, err := i.Store.ListOIDCSigningKeys(ctx)
	if err != nil {
		return nil, err
	}
	if active(keys, i.now()) != nil && !i.rotationDue(keys) {
		return keys, nil
	}
	err = i.Store.WithOIDCKeyLock(ctx, func(ctx context.Context) error {
		keys, err = i.Store.ListOIDCSigningKeys(ctx)
		if err != nil {
			return err
		}
		now := i.now()
		if active(keys, now) == nil {
			// The first key (or a recovery after every key retired) signs at
			// once: no consumer can trust an issuer that it never saw.
			_, err := i.createKey(ctx, now)
			return err
		}
		if i.rotationDue(keys) {
			_, err := i.rotateLocked(ctx, now.Add(i.PublishLead))
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return i.Store.ListOIDCSigningKeys(ctx)
}

// rotationDue reports whether the newest key is older than RotationPeriod.
func (i *Issuer) rotationDue(keys []models.OIDCSigningKey) bool {
	if i.RotationPeriod <= 0 || len(keys) == 0 {
		return false
	}
	newest := keys[0]
	for _, key := range keys[1:] {
		if key.ActivatesAt.After(newest.ActivatesAt) {
			newest = key
		}
	}
	return i.now().Sub(newest.ActivatesAt) > i.RotationPeriod
}

// active returns the key that signs now: the newest key whose activation has
// passed and that is not retired.
func active(keys []models.OIDCSigningKey, now time.Time) *models.OIDCSigningKey {
	var found *models.OIDCSigningKey
	for idx := range keys {
		key := &keys[idx]
		if key.ActivatesAt.After(now) || (key.RetiresAt != nil && !key.RetiresAt.After(now)) {
			continue
		}
		if found == nil || key.ActivatesAt.After(found.ActivatesAt) {
			found = key
		}
	}
	return found
}

// Rotate adds a new key. It is in the JWKS at once and signs after
// PublishLead (or at once when immediate is true, for a compromised key).
// The keys before it retire RetireAfter after the new key starts to sign.
func (i *Issuer) Rotate(ctx context.Context, immediate bool) (*models.OIDCSigningKey, error) {
	if !i.Enabled() {
		return nil, ErrDisabled
	}
	var created *models.OIDCSigningKey
	err := i.Store.WithOIDCKeyLock(ctx, func(ctx context.Context) error {
		activates := i.now().Add(i.PublishLead)
		if immediate {
			activates = i.now()
		}
		var err error
		created, err = i.rotateLocked(ctx, activates)
		return err
	})
	return created, err
}

func (i *Issuer) rotateLocked(ctx context.Context, activates time.Time) (*models.OIDCSigningKey, error) {
	key, err := i.createKey(ctx, activates)
	if err != nil {
		return nil, err
	}
	if err := i.Store.RetireOIDCSigningKeys(ctx, key.KID, activates.Add(i.RetireAfter)); err != nil {
		return nil, err
	}
	return key, nil
}

// Retire removes a key from the JWKS at the given time (now for a
// compromised key). A retired key never signs.
func (i *Issuer) Retire(ctx context.Context, kid string, at time.Time) error {
	if !i.Enabled() {
		return ErrDisabled
	}
	return i.Store.SetOIDCSigningKeyRetirement(ctx, kid, at.UTC())
}

// Keys returns the key records (without private keys), newest first.
func (i *Issuer) Keys(ctx context.Context) ([]models.OIDCSigningKey, error) {
	if !i.Enabled() {
		return nil, ErrDisabled
	}
	keys, err := i.ensureKeys(ctx)
	if err != nil {
		return nil, err
	}
	sort.Slice(keys, func(a, b int) bool { return keys[a].ActivatesAt.After(keys[b].ActivatesAt) })
	return keys, nil
}

func (i *Issuer) createKey(ctx context.Context, activates time.Time) (*models.OIDCSigningKey, error) {
	var signer crypto.Signer
	switch i.Algorithm {
	case AlgorithmES256:
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, err
		}
		signer = key
	case AlgorithmRS256:
		key, err := rsa.GenerateKey(rand.Reader, 3072)
		if err != nil {
			return nil, err
		}
		signer = key
	default:
		return nil, fmt.Errorf("oidc: signing algorithm %q is not supported (use ES256 or RS256)", i.Algorithm)
	}
	kidBytes := make([]byte, 16)
	if _, err := rand.Read(kidBytes); err != nil {
		return nil, err
	}
	kid := hex.EncodeToString(kidBytes)
	der, err := x509.MarshalPKCS8PrivateKey(signer)
	if err != nil {
		return nil, err
	}
	keyName, ciphertext, err := i.Crypto.EncryptWithPrimary(der)
	if err != nil {
		return nil, fmt.Errorf("oidc: encrypting the signing key: %w", err)
	}
	masterKeyID, err := i.Store.MasterKeyIDByName(ctx, keyName)
	if err != nil {
		return nil, fmt.Errorf("oidc: resolving master key: %w", err)
	}
	public := jose.JSONWebKey{Key: signer.Public(), KeyID: kid, Algorithm: i.Algorithm, Use: "sig"}
	publicJSON, err := public.MarshalJSON()
	if err != nil {
		return nil, err
	}
	var publicMap models.JSONB
	if err := json.Unmarshal(publicJSON, &publicMap); err != nil {
		return nil, err
	}
	record := &models.OIDCSigningKey{KID: kid, Algorithm: i.Algorithm, PublicJWK: publicMap, MasterKeyID: masterKeyID,
		EncryptedPrivateKey: ciphertext, CreatedAt: i.now(), ActivatesAt: activates.UTC()}
	if err := i.Store.CreateOIDCSigningKey(ctx, record); err != nil {
		return nil, err
	}
	i.mu.Lock()
	if i.signers == nil {
		i.signers = map[string]crypto.Signer{}
	}
	i.signers[kid] = signer
	i.mu.Unlock()
	return record, nil
}

func (i *Issuer) signerFor(ctx context.Context, key *models.OIDCSigningKey) (crypto.Signer, error) {
	i.mu.Lock()
	signer := i.signers[key.KID]
	i.mu.Unlock()
	if signer != nil {
		return signer, nil
	}
	name, err := i.Store.MasterKeyNameByID(ctx, key.MasterKeyID)
	if err != nil {
		return nil, fmt.Errorf("oidc: resolving master key of key %s: %w", key.KID, err)
	}
	der, err := i.Crypto.DecryptWithKey(name, key.EncryptedPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("oidc: decrypting key %s: %w", key.KID, err)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("oidc: parsing key %s: %w", key.KID, err)
	}
	signer, ok := parsed.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("oidc: key %s is not a signing key", key.KID)
	}
	i.mu.Lock()
	if i.signers == nil {
		i.signers = map[string]crypto.Signer{}
	}
	i.signers[key.KID] = signer
	i.mu.Unlock()
	return signer, nil
}

// Token is a signed job token.
type Token struct {
	Value     string
	KeyID     string
	ID        string
	ExpiresAt time.Time
}

// Sign signs claims for one audience. It sets iss, aud, iat, nbf, exp, and
// jti; a claim in claims with one of those names is replaced.
func (i *Issuer) Sign(ctx context.Context, audience string, claims map[string]any, expiresAt time.Time) (*Token, error) {
	if !i.Enabled() {
		return nil, ErrDisabled
	}
	keys, err := i.ensureKeys(ctx)
	if err != nil {
		return nil, err
	}
	now := i.now()
	key := active(keys, now)
	if key == nil {
		return nil, errors.New("oidc: no signing key is active")
	}
	signer, err := i.signerFor(ctx, key)
	if err != nil {
		return nil, err
	}
	joseSigner, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.SignatureAlgorithm(key.Algorithm),
		Key: jose.JSONWebKey{Key: signer, KeyID: key.KID, Algorithm: key.Algorithm}},
		(&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		return nil, err
	}
	jtiBytes := make([]byte, 16)
	if _, err := rand.Read(jtiBytes); err != nil {
		return nil, err
	}
	jti := hex.EncodeToString(jtiBytes)
	body := make(map[string]any, len(claims)+6)
	for name, value := range claims {
		body[name] = value
	}
	body["iss"] = i.IssuerValue()
	body["aud"] = audience
	body["iat"] = now.Unix()
	body["nbf"] = now.Unix()
	body["exp"] = expiresAt.Unix()
	body["jti"] = jti
	value, err := jwt.Signed(joseSigner).Claims(body).Serialize()
	if err != nil {
		return nil, err
	}
	return &Token{Value: value, KeyID: key.KID, ID: jti, ExpiresAt: expiresAt}, nil
}
