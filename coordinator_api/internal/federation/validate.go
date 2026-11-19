package federation

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/transportsecurity"
)

// Limits of issuer and mapping records.
const (
	MaxAudiences        = 16
	DefaultClockSkew    = 60
	MaxClockSkew        = 600
	DefaultTokenTTL     = 900
	MinTokenTTL         = 60
	MaxTokenTTL         = 3600
	defaultIssuerStatus = models.FederationStatusActive
	issuerPurpose       = "federation issuer"
	jwksPurpose         = "federation JWKS"
)

// DefaultAlgorithms are the algorithms of an issuer that names none.
var DefaultAlgorithms = []string{"RS256", "ES256"}

// NormalizeIssuer validates an issuer record and fills its defaults. The
// issuer URL and the JWKS URL must use HTTPS, unless allowInsecure comes
// from the explicit serve option.
func NormalizeIssuer(issuer *models.FederationIssuer, allowInsecure bool) error {
	issuer.Name = strings.TrimSpace(issuer.Name)
	if !models.ValidServicePrincipalName(issuer.Name) {
		return fmt.Errorf("name must be lowercase letters, digits, and hyphens")
	}
	issuer.IssuerURL = strings.TrimSpace(issuer.IssuerURL)
	if err := validateURL(issuer.IssuerURL, allowInsecure, issuerPurpose); err != nil {
		return err
	}
	issuer.JWKSURL = strings.TrimSpace(issuer.JWKSURL)
	if issuer.JWKSURL != "" {
		if err := validateURL(issuer.JWKSURL, allowInsecure, jwksPurpose); err != nil {
			return err
		}
	}
	audiences := []string{}
	seen := map[string]bool{}
	for _, audience := range issuer.Audiences {
		audience = strings.TrimSpace(audience)
		if audience == "" || seen[audience] {
			continue
		}
		if len(audience) > 512 {
			return fmt.Errorf("an audience is too long")
		}
		seen[audience] = true
		audiences = append(audiences, audience)
	}
	if len(audiences) == 0 || len(audiences) > MaxAudiences {
		return fmt.Errorf("an issuer needs 1 to %d audiences", MaxAudiences)
	}
	issuer.Audiences = audiences
	if len(issuer.Algorithms) == 0 {
		issuer.Algorithms = append([]string(nil), DefaultAlgorithms...)
	}
	algorithms := []string{}
	seenAlg := map[string]bool{}
	for _, alg := range issuer.Algorithms {
		alg = strings.TrimSpace(alg)
		if !ValidAlgorithm(alg) {
			return fmt.Errorf("algorithm %q is not allowed (use RS256, ES256, or EdDSA)", alg)
		}
		if !seenAlg[alg] {
			seenAlg[alg] = true
			algorithms = append(algorithms, alg)
		}
	}
	issuer.Algorithms = algorithms
	if issuer.ClockSkewSeconds < 0 || issuer.ClockSkewSeconds > MaxClockSkew {
		return fmt.Errorf("clock skew must be 0 to %d seconds", MaxClockSkew)
	}
	if issuer.Status == "" {
		issuer.Status = defaultIssuerStatus
	}
	if issuer.Status != models.FederationStatusActive && issuer.Status != models.FederationStatusDisabled {
		return fmt.Errorf("status must be active or disabled")
	}
	return nil
}

func validateURL(raw string, allowInsecure bool, purpose string) error {
	if err := transportsecurity.ValidateURL(raw, allowInsecure, purpose); err != nil {
		return err
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%s URL must not have user info, a query, or a fragment", purpose)
	}
	return nil
}

// CapabilityLimit is the authority of the caller that makes a mapping: a
// mapping cannot give a capability that the caller does not have.
type CapabilityLimit struct {
	All          bool
	Capabilities tokencaps.Set
}

// NormalizeMapping validates a mapping record and fills its defaults. The
// caller resolves and checks the principal.
func NormalizeMapping(mapping *models.FederationMapping, limit CapabilityLimit) error {
	mapping.Name = strings.TrimSpace(mapping.Name)
	if !models.ValidServicePrincipalName(mapping.Name) {
		return fmt.Errorf("name must be lowercase letters, digits, and hyphens")
	}
	if err := ValidateConditions(mapping.Conditions); err != nil {
		return err
	}
	if len(mapping.Capabilities) == 0 {
		return fmt.Errorf("a mapping needs at least one capability")
	}
	requested, err := tokencaps.New(mapping.Capabilities...)
	if err != nil {
		return err
	}
	if !limit.All && !requested.IsSubsetOf(limit.Capabilities) {
		return fmt.Errorf("a mapping cannot give a capability that the caller does not have")
	}
	mapping.Capabilities = requested.Slice()
	if mapping.TokenTTLSeconds == 0 {
		mapping.TokenTTLSeconds = DefaultTokenTTL
	}
	if mapping.TokenTTLSeconds < MinTokenTTL || mapping.TokenTTLSeconds > MaxTokenTTL {
		return fmt.Errorf("token TTL must be %d to %d seconds", MinTokenTTL, MaxTokenTTL)
	}
	if mapping.Status == "" {
		mapping.Status = models.FederationStatusActive
	}
	if mapping.Status != models.FederationStatusActive && mapping.Status != models.FederationStatusDisabled {
		return fmt.Errorf("status must be active or disabled")
	}
	return nil
}
