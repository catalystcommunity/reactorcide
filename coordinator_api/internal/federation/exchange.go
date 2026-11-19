package federation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

// Store is the store surface of the token exchange.
type Store interface {
	GetFederationIssuerByURL(ctx context.Context, orgID, issuerURL string) (*models.FederationIssuer, error)
	ListFederationMappings(ctx context.Context, orgID, issuerID string) ([]models.FederationMapping, error)
	GetPrincipal(ctx context.Context, principalID string) (*models.Principal, error)
	// CreateFederatedToken creates the api_tokens row and its
	// federation_issued_tokens record in one transaction, and records the
	// federated identity of the subject.
	CreateFederatedToken(ctx context.Context, token *models.APIToken, record *models.FederationIssuedToken, identityValue string) error
	DeleteExpiredFederatedTokens(ctx context.Context, before time.Time) (int64, error)
}

// ExchangeRequest is one token exchange.
type ExchangeRequest struct {
	OrgID string
	// Token is the external JWT. It is never logged or stored.
	Token string
	// PrincipalID optionally limits the mappings to one principal.
	PrincipalID string
	// Capabilities optionally narrows the capabilities of the mapping.
	Capabilities []string
}

// ExchangeResult is a successful exchange. Token is the Reactorcide token
// value: show it to the caller one time only.
type ExchangeResult struct {
	Token        string
	TokenInfo    *models.APIToken
	Issuer       *models.FederationIssuer
	Mapping      *models.FederationMapping
	Principal    *models.Principal
	Claims       *Claims
	ExpiresAt    time.Time
	Capabilities []string
}

// ExchangeError is a refused exchange. Reason is safe for the audit record
// and the caller. Issuer and Subject are set when they are known.
type ExchangeError struct {
	Reason   error
	IssuerID string
	Issuer   string
	Subject  string
	// RetryAfter is set for ErrRateLimited.
	RetryAfter time.Duration
}

func (e *ExchangeError) Error() string { return e.Reason.Error() }
func (e *ExchangeError) Unwrap() error { return e.Reason }

// ErrCapabilities is a request for a capability that the mapping does not give.
var ErrCapabilities = errors.New("the requested capabilities are not in the mapping")

// ErrRateLimited is an exchange that the issuer limit refused.
var ErrRateLimited = errors.New("too many token exchanges for this issuer; retry later")

// Exchanger verifies external tokens and issues Reactorcide tokens.
type Exchanger struct {
	Store    Store
	Verifier *Verifier
	Now      func() time.Time
	// AllowIssuer, when set, is called after the external token is
	// verified and before a token is issued. It returns false (and the wait)
	// when the issuer is over its limit. Only verified tokens count, so a
	// false "iss" claim cannot use the limit of an issuer.
	AllowIssuer func(issuer *models.FederationIssuer) (bool, time.Duration)
}

func (x *Exchanger) now() time.Time {
	if x.Now != nil {
		return x.Now()
	}
	return time.Now()
}

// Exchange verifies req.Token and issues a short-lived token for the mapped
// service principal. The issued token has the capabilities of the mapping
// (or the requested subset), and it always intersects them with the roles of
// the principal at each use (api_tokens.intersect_roles).
func (x *Exchanger) Exchange(ctx context.Context, req ExchangeRequest) (*ExchangeResult, error) {
	refuse := func(reason error, issuer *models.FederationIssuer, issuerURL, subject string) error {
		out := &ExchangeError{Reason: reason, Issuer: issuerURL, Subject: subject}
		if issuer != nil {
			out.IssuerID = issuer.IssuerID
		}
		return out
	}
	issuerURL, err := UnverifiedIssuer(req.Token)
	if err != nil {
		return nil, refuse(err, nil, "", "")
	}
	issuer, err := x.Store.GetFederationIssuerByURL(ctx, req.OrgID, issuerURL)
	if err != nil || issuer == nil {
		return nil, refuse(ErrUnknownIssuer, nil, issuerURL, "")
	}
	claims, err := x.Verifier.Verify(ctx, issuer, req.Token)
	if err != nil {
		return nil, refuse(err, issuer, issuerURL, "")
	}
	if x.AllowIssuer != nil {
		if ok, wait := x.AllowIssuer(issuer); !ok {
			out := refuse(ErrRateLimited, issuer, issuerURL, claims.Subject).(*ExchangeError)
			out.RetryAfter = wait
			return nil, out
		}
	}
	mappings, err := x.Store.ListFederationMappings(ctx, req.OrgID, issuer.IssuerID)
	if err != nil {
		return nil, err
	}
	mapping, err := SelectMapping(mappings, claims.All, req.PrincipalID)
	if err != nil {
		return nil, refuse(err, issuer, issuerURL, claims.Subject)
	}
	principal, err := x.Store.GetPrincipal(ctx, mapping.PrincipalID)
	if err != nil || principal == nil || !principal.IsActive() || principal.Kind != models.PrincipalKindService ||
		principal.OrgID == nil || *principal.OrgID != req.OrgID || principal.PrincipalID != mapping.PrincipalID {
		return nil, refuse(ErrPrincipal, issuer, issuerURL, claims.Subject)
	}
	granted, err := tokencaps.New(mapping.Capabilities...)
	if err != nil || len(granted.Slice()) == 0 {
		return nil, refuse(ErrCapabilities, issuer, issuerURL, claims.Subject)
	}
	if len(req.Capabilities) > 0 {
		requested, err := tokencaps.New(req.Capabilities...)
		if err != nil || !requested.IsSubsetOf(granted) {
			return nil, refuse(ErrCapabilities, issuer, issuerURL, claims.Subject)
		}
		granted = requested
	}

	now := x.now().UTC()
	expiresAt := now.Add(time.Duration(mapping.TokenTTLSeconds) * time.Second)
	// The Reactorcide token never outlives the external token.
	if claims.Expiry.Before(expiresAt) {
		expiresAt = claims.Expiry.UTC()
	}
	value, err := newTokenValue()
	if err != nil {
		return nil, err
	}
	orgID := req.OrgID
	principalID := principal.PrincipalID
	token := &models.APIToken{
		TokenHash: checkauth.HashAPIToken(value), Name: fmt.Sprintf("federation:%s/%s", issuer.Name, mapping.Name),
		ExpiresAt: &expiresAt, IsActive: true, SubjectType: "service_token", OwnerOrgID: &orgID,
		OrganizationIDs: []string{orgID}, Capabilities: granted.Slice(),
		PrincipalID: &principalID, IntersectRoles: true,
	}
	mappingID, issuerID := mapping.MappingID, issuer.IssuerID
	record := &models.FederationIssuedToken{MappingID: &mappingID, IssuerID: &issuerID, Subject: claims.Subject, ExpiresAt: expiresAt}
	identity := models.NormalizeIdentityValue(models.IdentityTypeFederated, issuer.IssuerURL+"|"+claims.Subject)
	if err := x.Store.CreateFederatedToken(ctx, token, record, identity); err != nil {
		return nil, err
	}
	// Remove the rows of expired exchange tokens. A failure here does not
	// affect the exchange.
	_, _ = x.Store.DeleteExpiredFederatedTokens(ctx, now.Add(-time.Hour))
	return &ExchangeResult{Token: value, TokenInfo: token, Issuer: issuer, Mapping: mapping, Principal: principal,
		Claims: claims, ExpiresAt: expiresAt, Capabilities: granted.Slice()}, nil
}

func newTokenValue() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating token: %w", err)
	}
	return "rcf_" + hex.EncodeToString(raw), nil
}
