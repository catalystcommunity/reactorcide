package models

import (
	"regexp"
	"strings"
	"time"
)

// Principal kinds. A principal is anything that can act in Reactorcide.
const (
	PrincipalKindHuman   = "human"
	PrincipalKindService = "service"
	PrincipalKindSystem  = "system"
)

// Principal statuses.
const (
	PrincipalStatusActive   = "active"
	PrincipalStatusDisabled = "disabled"
)

// SystemPrincipalID is the fixed UUID of the system principal "Reactorcide".
// It is the actor when no other actor exists, for example a schedule fire.
// It has no role assignments and no privileges.
const SystemPrincipalID = "00000000-0000-0000-0000-000000000001"

// SystemActorIdentity is the actor identity recorded for the system principal.
const SystemActorIdentity = "system:reactorcide"

// Identity types of principal_identities.
const (
	IdentityTypeUser      = "user"
	IdentityTypeVCS       = "vcs"
	IdentityTypeVCSStar   = "vcsstar"
	IdentityTypeLinkKeys  = "linkkeys"
	IdentityTypeToken     = "token"
	IdentityTypeFederated = "federated"
)

// Identity verification sources.
const (
	IdentityVerifiedBySystem     = "system"
	IdentityVerifiedByAdmin      = "admin"
	IdentityVerifiedByLinkKeys   = "linkkeys"
	IdentityVerifiedByWebhook    = "webhook"
	IdentityVerifiedByFederation = "federation"
	IdentityVerifiedByMigration  = "migration"
)

// PrincipalTypePrincipal is the role_assignments principal_type that names a
// principal directly (any kind).
const PrincipalTypePrincipal = "principal"

var servicePrincipalName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ValidServicePrincipalName reports whether name is a valid service
// principal name. The name is part of the subject service:ORG/NAME.
func ValidServicePrincipalName(name string) bool { return servicePrincipalName.MatchString(name) }

// Principal is one row of principals.
type Principal struct {
	PrincipalID string    `gorm:"primaryKey;type:uuid;default:generate_ulid()" json:"principal_id"`
	Kind        string    `gorm:"type:text;not null" json:"kind"`
	OrgID       *string   `gorm:"type:uuid" json:"org_id,omitempty"`
	Name        string    `gorm:"type:text;not null" json:"name"`
	DisplayName string    `gorm:"type:text;not null;default:''" json:"display_name"`
	Status      string    `gorm:"type:text;not null;default:'active'" json:"status"`
	MergedInto  *string   `gorm:"type:uuid" json:"merged_into,omitempty"`
	CreatedAt   time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt   time.Time `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
}

func (Principal) TableName() string { return "principals" }

// IsActive reports whether the principal can act.
func (p *Principal) IsActive() bool {
	return p != nil && (p.Status == "" || p.Status == PrincipalStatusActive) && p.MergedInto == nil
}

// Label is the best human-readable name of the principal.
func (p *Principal) Label() string {
	if p == nil {
		return ""
	}
	if strings.TrimSpace(p.DisplayName) != "" {
		return p.DisplayName
	}
	return p.Name
}

// PrincipalIdentity is one identity of a principal.
type PrincipalIdentity struct {
	IdentityType  string    `gorm:"primaryKey;type:text" json:"identity_type"`
	IdentityValue string    `gorm:"primaryKey;type:text" json:"identity_value"`
	PrincipalID   string    `gorm:"type:uuid;not null" json:"principal_id"`
	VerifiedBy    string    `gorm:"type:text;not null;default:'system'" json:"verified_by"`
	CreatedAt     time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
}

func (PrincipalIdentity) TableName() string { return "principal_identities" }

// String returns "TYPE:VALUE", the form recorded in actor_identity.
func (i PrincipalIdentity) String() string { return ActorIdentity(i.IdentityType, i.IdentityValue) }

// ActorIdentity returns the "TYPE:VALUE" form of an identity.
func ActorIdentity(identityType, value string) string {
	if identityType == "" || value == "" {
		return ""
	}
	return identityType + ":" + value
}

// ValidIdentityType reports whether identityType is a known identity type.
func ValidIdentityType(identityType string) bool {
	switch identityType {
	case IdentityTypeUser, IdentityTypeVCS, IdentityTypeVCSStar, IdentityTypeLinkKeys, IdentityTypeToken, IdentityTypeFederated:
		return true
	}
	return false
}

// NormalizeIdentityValue returns the stored form of an identity value. VCS,
// VCSStar, and LinkKeys values are case-insensitive and stored lowercase.
// User and token values are UUIDs (lowercase). A federated value
// (issuer|subject) keeps the case of the subject, because OIDC subjects are
// case-sensitive.
func NormalizeIdentityValue(identityType, value string) string {
	value = strings.TrimSpace(value)
	switch identityType {
	case IdentityTypeFederated:
		issuer, subject, ok := strings.Cut(value, "|")
		if !ok {
			return value
		}
		return strings.TrimRight(strings.TrimSpace(issuer), "/") + "|" + strings.TrimSpace(subject)
	default:
		return strings.ToLower(value)
	}
}

// VCSIdentityValue returns the vcs identity value of a provider login.
func VCSIdentityValue(provider, login string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	login = strings.ToLower(strings.TrimSpace(login))
	if provider == "" || login == "" {
		return ""
	}
	return provider + "/" + login
}

// ActorSummary is the display form of an actor in API responses.
type ActorSummary struct {
	PrincipalID string `json:"principal_id"`
	Kind        string `json:"kind,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	Identity    string `json:"identity,omitempty"`
}
