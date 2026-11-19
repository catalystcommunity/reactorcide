package checkauth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

type contextKey string

const (
	UserContextKey      contextKey = "user"
	VerifiedContextKey  contextKey = "verified"
	PrincipalContextKey contextKey = "principal"
)

type Principal struct {
	CredentialID     string
	CredentialType   string
	UserID           string
	OwnerOrgID       string
	AllOrganizations bool
	OrganizationIDs  []string
	AllCapabilities  bool
	Capabilities     tokencaps.Set
	BoundJobID       string
	// PrincipalID is the principal the token acts as (empty for a job token,
	// which acts for its job). IntersectRoles is true when the capabilities
	// of the token intersect with the live roles and groups of the principal
	// (always for a user token; for a service token issued for a principal).
	PrincipalID    string
	IntersectRoles bool
	// IssuedCapabilities are the capabilities of a service principal token
	// as issued, before authz.NewResolver(...).NarrowServicePrincipalToken
	// narrowed them to read capabilities for a non-admin principal. Nil when
	// nothing narrowed them. Use HasIssuedCapability, and only where roles
	// decide after the capability check (the required set approvals).
	IssuedCapabilities    tokencaps.Set
	IssuedAllCapabilities bool
	issuedRecorded        bool
}

// RecordIssuedCapabilities keeps the capabilities as issued before a
// narrowing changes them. A second call does nothing.
func (p *Principal) RecordIssuedCapabilities() {
	if p == nil || p.issuedRecorded {
		return
	}
	p.issuedRecorded = true
	p.IssuedAllCapabilities = p.AllCapabilities
	p.IssuedCapabilities = p.Capabilities
}

// HasIssuedCapability reports whether the token was issued with capability,
// before any narrowing by roles.
func (p *Principal) HasIssuedCapability(capability string) bool {
	if p == nil {
		return false
	}
	if !p.issuedRecorded {
		return p.HasCapability(capability)
	}
	return p.IssuedAllCapabilities || p.IssuedCapabilities.Has(capability)
}

// RolesApply reports whether live role assignments narrow this credential.
func (p *Principal) RolesApply() bool {
	return p != nil && (p.CredentialType == "user_token" || p.IntersectRoles)
}

// IsServicePrincipalToken reports whether the credential is a service or
// instance token whose authority is the intersection of its capabilities and
// the roles of its service principal.
func (p *Principal) IsServicePrincipalToken() bool {
	return p != nil && p.IntersectRoles && p.PrincipalID != "" &&
		(p.CredentialType == "service_token" || p.CredentialType == "instance_token")
}

// IsWideInstanceToken reports whether the credential is an instance token
// with all capabilities and all organizations whose roles do not narrow it:
// the admin token (reactorcide token create), or a service principal token
// issued with no capability list on a no-login instance. On a no-login
// instance (config.NoLoginMode) such a token acts as the instance
// administrator.
func (p *Principal) IsWideInstanceToken() bool {
	return p != nil && p.CredentialType == "instance_token" && p.UserID == "" && !p.IntersectRoles &&
		p.AllOrganizations && p.AllCapabilities
}

func (p *Principal) HasOrganization(orgID string) bool {
	if p == nil || orgID == "" {
		return false
	}
	if p.AllOrganizations {
		return true
	}
	for _, candidate := range p.OrganizationIDs {
		if candidate == orgID {
			return true
		}
	}
	return p.OwnerOrgID == orgID
}

func (p *Principal) HasCapability(capability string) bool {
	return p != nil && (p.AllCapabilities || p.Capabilities.Has(capability))
}

// GetUserFromContext retrieves the authenticated user from the request context
func GetUserFromContext(ctx context.Context) *models.User {
	if user, ok := ctx.Value(UserContextKey).(*models.User); ok {
		return user
	}
	return nil
}

// GetVerifiedFromContext checks if the request is verified/authenticated
func GetVerifiedFromContext(ctx context.Context) bool {
	if verified, ok := ctx.Value(VerifiedContextKey).(bool); ok {
		return verified
	}
	return false
}

// SetUserContext adds a user to the request context
func SetUserContext(ctx context.Context, user *models.User) context.Context {
	return context.WithValue(ctx, UserContextKey, user)
}

// SetVerifiedContext sets the verification status in the request context
func SetVerifiedContext(ctx context.Context, verified bool) context.Context {
	return context.WithValue(ctx, VerifiedContextKey, verified)
}

func GetPrincipalFromContext(ctx context.Context) *Principal {
	principal, _ := ctx.Value(PrincipalContextKey).(*Principal)
	return principal
}

func SetPrincipalContext(ctx context.Context, principal *Principal) context.Context {
	return context.WithValue(ctx, PrincipalContextKey, principal)
}

// ValidateAPIToken validates an API token against its stored hash
func ValidateAPIToken(tokenString string, hash []byte) bool {
	tokenHash := sha256.Sum256([]byte(tokenString))
	return subtle.ConstantTimeCompare(tokenHash[:], hash) == 1
}

// GenerateVerifier generates a SHA256 hash for verification purposes
// This is a legacy function from the original codebase, keeping for compatibility
func GenerateVerifier(userID string, salt []byte, token string) string {
	data := userID + string(salt) + token
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])
}

// VerifySession verifies a session using the legacy verification method
// This is kept for backward compatibility with existing verification patterns
func VerifySession(token, verifier, userID string, salt []byte) bool {
	expectedVerifier := GenerateVerifier(userID, salt, token)
	return subtle.ConstantTimeCompare([]byte(verifier), []byte(expectedVerifier)) == 1
}

// HashAPIToken creates a SHA256 hash of an API token for storage
func HashAPIToken(token string) []byte {
	hash := sha256.Sum256([]byte(token))
	return hash[:]
}
