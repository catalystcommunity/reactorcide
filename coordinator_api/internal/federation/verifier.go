// Package federation verifies tokens of external OIDC issuers and maps them
// to service principals (inbound federation, docs/federation.md).
//
// The package uses go-jose for every cryptographic operation. It does no
// custom cryptography.
package federation

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/transportsecurity"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// Reasons for a refused token. The reason goes to the audit record and to
// the caller. It never contains the token.
var (
	ErrMalformedToken   = errors.New("the token is not a signed JWT")
	ErrAlgorithm        = errors.New("the token uses an algorithm that the issuer does not allow")
	ErrUnknownIssuer    = errors.New("the token issuer is not registered for the organization")
	ErrIssuerDisabled   = errors.New("the issuer is disabled")
	ErrKeys             = errors.New("the signing keys of the issuer are not available")
	ErrUnknownKey       = errors.New("the token key is not in the JWKS of the issuer")
	ErrSignature        = errors.New("the token signature is not valid")
	ErrIssuerMismatch   = errors.New("the token issuer does not match")
	ErrAudience         = errors.New("the token audience is not allowed")
	ErrExpired          = errors.New("the token is expired")
	ErrNotYetValid      = errors.New("the token is not valid yet")
	ErrIssuedInFuture   = errors.New("the token was issued in the future")
	ErrMissingClaim     = errors.New("the token must have exp, iat, and sub")
	ErrLifetimeTooLong  = errors.New("the token lifetime is too long")
	ErrNotMapped        = errors.New("no claim mapping matches the token")
	ErrAmbiguousMapping = errors.New("more than one claim mapping matches the token")
	ErrPrincipal        = errors.New("the mapped principal is not an active service principal of the organization")
)

// MaxTokenLifetime is the longest exp - now that a federated token can have.
// A token that lives longer is a static credential, not a workload token.
const MaxTokenLifetime = 24 * time.Hour

// AllowedAlgorithms are the only signature algorithms that federation
// accepts. "none" and the HMAC algorithms are never accepted: an HMAC key is
// a shared secret, and a public JWKS cannot hold it.
var AllowedAlgorithms = map[string]jose.SignatureAlgorithm{
	"RS256": jose.RS256,
	"ES256": jose.ES256,
	"EdDSA": jose.EdDSA,
}

// ValidAlgorithm reports whether alg is an allowed algorithm name.
func ValidAlgorithm(alg string) bool {
	_, ok := AllowedAlgorithms[alg]
	return ok
}

// Claims is a verified token.
type Claims struct {
	Issuer   string
	Subject  string
	Audience []string
	Expiry   time.Time
	IssuedAt time.Time
	ID       string
	// All holds every claim of the token, for the mapping conditions.
	All map[string]any
}

// KeySource returns the keys of an issuer.
type KeySource interface {
	Keys(ctx context.Context, issuer *models.FederationIssuer, refresh bool) (*jose.JSONWebKeySet, error)
}

// Verifier verifies tokens against registered issuers.
type Verifier struct {
	Keys KeySource
	// Now returns the current time. Tests replace it.
	Now func() time.Time
}

// NewVerifier returns a verifier that uses keys.
func NewVerifier(keys KeySource) *Verifier {
	return &Verifier{Keys: keys, Now: time.Now}
}

// UnverifiedIssuer returns the "iss" claim of a token without verification.
// Use it only to select the registered issuer. Verify checks the value again
// after the signature is verified.
func UnverifiedIssuer(raw string) (string, error) {
	parsed, err := jwt.ParseSigned(strings.TrimSpace(raw), allAlgorithms())
	if err != nil {
		return "", ErrMalformedToken
	}
	var claims jwt.Claims
	if err := parsed.UnsafeClaimsWithoutVerification(&claims); err != nil {
		return "", ErrMalformedToken
	}
	if claims.Issuer == "" {
		return "", ErrMalformedToken
	}
	return claims.Issuer, nil
}

// headerAlgorithm returns the "alg" of a compact JWS header, or "". It only
// reads the header to give a clear refusal reason.
func headerAlgorithm(raw string) string {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return ""
	}
	decoded, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return ""
	}
	var header struct {
		Alg string `json:"alg"`
	}
	if json.Unmarshal(decoded, &header) != nil {
		return ""
	}
	return header.Alg
}

func allAlgorithms() []jose.SignatureAlgorithm {
	return []jose.SignatureAlgorithm{jose.RS256, jose.ES256, jose.EdDSA}
}

// Verify verifies the signature and the standard claims of raw against
// issuer. It returns the claims of a valid token.
func (v *Verifier) Verify(ctx context.Context, issuer *models.FederationIssuer, raw string) (*Claims, error) {
	if !issuer.IsActive() {
		return nil, ErrIssuerDisabled
	}
	algorithms := []jose.SignatureAlgorithm{}
	for _, name := range issuer.Algorithms {
		if alg, ok := AllowedAlgorithms[name]; ok {
			algorithms = append(algorithms, alg)
		}
	}
	if len(algorithms) == 0 {
		return nil, ErrAlgorithm
	}
	raw = strings.TrimSpace(raw)
	parsed, err := jwt.ParseSigned(raw, algorithms)
	if err != nil {
		// Find out whether the token is a JWT with a refused algorithm, for a
		// clear reason.
		if _, anyErr := jwt.ParseSigned(raw, allAlgorithms()); anyErr == nil || headerAlgorithm(raw) != "" {
			return nil, ErrAlgorithm
		}
		return nil, ErrMalformedToken
	}
	if len(parsed.Headers) != 1 {
		return nil, ErrMalformedToken
	}
	header := parsed.Headers[0]
	key, err := v.findKey(ctx, issuer, header)
	if err != nil {
		return nil, err
	}
	var std jwt.Claims
	all := map[string]any{}
	if err := parsed.Claims(key, &std, &all); err != nil {
		return nil, ErrSignature
	}
	now := v.now()
	skew := time.Duration(issuer.ClockSkewSeconds) * time.Second
	if std.Issuer != issuer.IssuerURL {
		return nil, ErrIssuerMismatch
	}
	if !audienceAllowed(std.Audience, issuer.Audiences) {
		return nil, ErrAudience
	}
	if std.Expiry == nil || std.IssuedAt == nil || std.Subject == "" {
		return nil, ErrMissingClaim
	}
	if now.Add(-skew).After(std.Expiry.Time()) {
		return nil, ErrExpired
	}
	if std.NotBefore != nil && now.Add(skew).Before(std.NotBefore.Time()) {
		return nil, ErrNotYetValid
	}
	if now.Add(skew).Before(std.IssuedAt.Time()) {
		return nil, ErrIssuedInFuture
	}
	if std.Expiry.Time().Sub(now) > MaxTokenLifetime {
		return nil, ErrLifetimeTooLong
	}
	return &Claims{Issuer: std.Issuer, Subject: std.Subject, Audience: []string(std.Audience),
		Expiry: std.Expiry.Time(), IssuedAt: std.IssuedAt.Time(), ID: std.ID, All: all}, nil
}

func (v *Verifier) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

func audienceAllowed(audience jwt.Audience, allowed []string) bool {
	for _, value := range allowed {
		if value != "" && audience.Contains(value) {
			return true
		}
	}
	return false
}

// findKey selects the verification key for header. An unknown key ID makes
// one forced refresh of the JWKS, for a key that the issuer rotated in.
func (v *Verifier) findKey(ctx context.Context, issuer *models.FederationIssuer, header jose.Header) (*jose.JSONWebKey, error) {
	for attempt := 0; attempt < 2; attempt++ {
		set, err := v.Keys.Keys(ctx, issuer, attempt == 1)
		if err != nil {
			return nil, ErrKeys
		}
		if key := selectKey(set, header); key != nil {
			return key, nil
		}
	}
	return nil, ErrUnknownKey
}

func selectKey(set *jose.JSONWebKeySet, header jose.Header) *jose.JSONWebKey {
	if set == nil {
		return nil
	}
	var candidates []jose.JSONWebKey
	if header.KeyID != "" {
		candidates = set.Key(header.KeyID)
	} else {
		candidates = set.Keys
	}
	var match *jose.JSONWebKey
	for i := range candidates {
		key := candidates[i]
		if !key.Valid() || !key.IsPublic() || (key.Use != "" && key.Use != "sig") {
			continue
		}
		if key.Algorithm != "" && key.Algorithm != header.Algorithm {
			continue
		}
		if match != nil {
			// Without a key ID, two usable keys make the choice ambiguous.
			return nil
		}
		match = &key
	}
	return match
}

// JWKSCache fetches and caches the JWKS of issuers. It reads the JWKS URL
// from the discovery document when the issuer has no JWKS URL. The fetch
// uses HTTPS unless the coordinator runs with an explicit insecure option.
type JWKSCache struct {
	Client        *http.Client
	AllowInsecure bool
	// TTL is how long a fetched JWKS stays valid.
	TTL time.Duration
	// MinRefresh is the shortest time between two forced refreshes of one
	// JWKS. It stops a token with a random key ID from making a fetch for
	// each request.
	MinRefresh time.Duration
	Now        func() time.Time

	mu      sync.Mutex
	entries map[string]*jwksEntry
}

type jwksEntry struct {
	set       *jose.JSONWebKeySet
	fetchedAt time.Time
}

// NewJWKSCache returns a cache with default settings.
func NewJWKSCache(allowInsecure bool) *JWKSCache {
	return &JWKSCache{
		Client:        &http.Client{Timeout: 10 * time.Second},
		AllowInsecure: allowInsecure,
		TTL:           10 * time.Minute,
		MinRefresh:    30 * time.Second,
		Now:           time.Now,
	}
}

const maxDocumentBytes = 1 << 20

func (c *JWKSCache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Keys returns the cached JWKS of issuer. refresh forces a new fetch unless
// the last fetch is newer than MinRefresh.
func (c *JWKSCache) Keys(ctx context.Context, issuer *models.FederationIssuer, refresh bool) (*jose.JSONWebKeySet, error) {
	cacheKey := issuer.IssuerURL + "\x00" + issuer.JWKSURL
	now := c.now()
	c.mu.Lock()
	if c.entries == nil {
		c.entries = map[string]*jwksEntry{}
	}
	entry := c.entries[cacheKey]
	if entry != nil {
		age := now.Sub(entry.fetchedAt)
		if (!refresh && age < c.TTL) || (refresh && age < c.MinRefresh) {
			set := entry.set
			c.mu.Unlock()
			return set, nil
		}
	}
	c.mu.Unlock()

	set, err := c.fetch(ctx, issuer)
	if err != nil {
		// Keep a stale JWKS for a short outage of the issuer. A refresh for
		// an unknown key fails closed because the stale set lacks the key.
		if entry != nil {
			return entry.set, nil
		}
		return nil, err
	}
	c.mu.Lock()
	c.entries[cacheKey] = &jwksEntry{set: set, fetchedAt: now}
	c.mu.Unlock()
	return set, nil
}

func (c *JWKSCache) fetch(ctx context.Context, issuer *models.FederationIssuer) (*jose.JSONWebKeySet, error) {
	jwksURL := strings.TrimSpace(issuer.JWKSURL)
	if jwksURL == "" {
		discovered, err := c.discover(ctx, issuer.IssuerURL)
		if err != nil {
			return nil, err
		}
		jwksURL = discovered
	}
	var set jose.JSONWebKeySet
	if err := c.getJSON(ctx, jwksURL, "federation JWKS", &set); err != nil {
		return nil, err
	}
	return &set, nil
}

// discover reads the jwks_uri of an issuer from its discovery document. The
// document must name the same issuer.
func (c *JWKSCache) discover(ctx context.Context, issuerURL string) (string, error) {
	var doc struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	discoveryURL := strings.TrimRight(issuerURL, "/") + "/.well-known/openid-configuration"
	if err := c.getJSON(ctx, discoveryURL, "federation discovery", &doc); err != nil {
		return "", err
	}
	if doc.Issuer != issuerURL {
		return "", fmt.Errorf("the discovery document names issuer %q, not %q", doc.Issuer, issuerURL)
	}
	if doc.JWKSURI == "" {
		return "", errors.New("the discovery document has no jwks_uri")
	}
	return doc.JWKSURI, nil
}

func (c *JWKSCache) getJSON(ctx context.Context, rawURL, purpose string, dest any) error {
	if err := transportsecurity.ValidateURL(rawURL, c.AllowInsecure, purpose); err != nil {
		return err
	}
	client := transportsecurity.HTTPClient(c.Client, c.AllowInsecure, purpose)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", purpose, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned status %d", purpose, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDocumentBytes+1))
	if err != nil {
		return fmt.Errorf("%s: %w", purpose, err)
	}
	if len(body) > maxDocumentBytes {
		return fmt.Errorf("%s is too large", purpose)
	}
	if err := json.Unmarshal(body, dest); err != nil {
		return fmt.Errorf("%s is not valid JSON: %w", purpose, err)
	}
	return nil
}
