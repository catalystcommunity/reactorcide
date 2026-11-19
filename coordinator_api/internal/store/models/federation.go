package models

import (
	"time"

	"github.com/lib/pq"
)

// Federation and OIDC statuses.
const (
	FederationStatusActive   = "active"
	FederationStatusDisabled = "disabled"
)

// OIDC issuer settings (oidc_settings.issuer).
const (
	OIDCIssuerInherit = "inherit"
	OIDCIssuerAllow   = "allow"
	OIDCIssuerDeny    = "deny"
)

// FederationIssuer is a trusted external OIDC issuer of one organization.
type FederationIssuer struct {
	IssuerID         string         `gorm:"primaryKey;type:uuid;default:generate_ulid()" json:"issuer_id"`
	OrgID            string         `gorm:"type:uuid;not null" json:"-"`
	Name             string         `gorm:"type:text;not null" json:"name"`
	IssuerURL        string         `gorm:"type:text;not null" json:"issuer_url"`
	JWKSURL          string         `gorm:"column:jwks_url;type:text;not null;default:''" json:"jwks_url,omitempty"`
	Audiences        pq.StringArray `gorm:"type:text[];not null" json:"audiences"`
	Algorithms       pq.StringArray `gorm:"type:text[];not null" json:"algorithms"`
	ClockSkewSeconds int            `gorm:"not null;default:60" json:"clock_skew_seconds"`
	Status           string         `gorm:"type:text;not null;default:'active'" json:"status"`
	CreatedAt        time.Time      `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt        time.Time      `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
}

func (FederationIssuer) TableName() string { return "federation_issuers" }

// IsActive reports whether the issuer accepts tokens.
func (i *FederationIssuer) IsActive() bool {
	return i != nil && (i.Status == "" || i.Status == FederationStatusActive)
}

// FederationCondition is one claim condition of a mapping.
type FederationCondition struct {
	// Claim is a top-level claim name, for example "sub" or "repository".
	Claim string `json:"claim"`
	// Op is "equals" (exact string match) or "glob" (path.Match syntax).
	Op    string `json:"op"`
	Value string `json:"value"`
}

// Federation condition operators.
const (
	FederationOpEquals = "equals"
	FederationOpGlob   = "glob"
)

// FederationMapping maps claim conditions of an issuer to a service principal.
type FederationMapping struct {
	MappingID       string                `gorm:"primaryKey;type:uuid;default:generate_ulid()" json:"mapping_id"`
	IssuerID        string                `gorm:"type:uuid;not null" json:"issuer_id"`
	OrgID           string                `gorm:"type:uuid;not null" json:"-"`
	Name            string                `gorm:"type:text;not null" json:"name"`
	PrincipalID     string                `gorm:"type:uuid;not null" json:"principal_id"`
	Conditions      []FederationCondition `gorm:"type:jsonb;serializer:json;not null" json:"conditions"`
	Capabilities    pq.StringArray        `gorm:"type:text[];not null" json:"capabilities"`
	TokenTTLSeconds int                   `gorm:"column:token_ttl_seconds;not null;default:900" json:"token_ttl_seconds"`
	Status          string                `gorm:"type:text;not null;default:'active'" json:"status"`
	CreatedAt       time.Time             `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt       time.Time             `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
}

func (FederationMapping) TableName() string { return "federation_mappings" }

// IsActive reports whether the mapping can match.
func (m *FederationMapping) IsActive() bool {
	return m != nil && (m.Status == "" || m.Status == FederationStatusActive)
}

// FederationIssuedToken records an api_tokens row made by a token exchange.
type FederationIssuedToken struct {
	TokenID   string    `gorm:"primaryKey;type:uuid" json:"token_id"`
	MappingID *string   `gorm:"type:uuid" json:"mapping_id,omitempty"`
	IssuerID  *string   `gorm:"type:uuid" json:"issuer_id,omitempty"`
	Subject   string    `gorm:"type:text;not null" json:"subject"`
	ExpiresAt time.Time `gorm:"not null" json:"expires_at"`
	CreatedAt time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
}

func (FederationIssuedToken) TableName() string { return "federation_issued_tokens" }

// OIDCSigningKey is one signing key of the Reactorcide OIDC issuer.
type OIDCSigningKey struct {
	KID                 string     `gorm:"column:kid;primaryKey;type:text" json:"kid"`
	Algorithm           string     `gorm:"type:text;not null" json:"algorithm"`
	PublicJWK           JSONB      `gorm:"column:public_jwk;type:jsonb;not null" json:"public_jwk"`
	MasterKeyID         string     `gorm:"type:uuid;not null" json:"-"`
	EncryptedPrivateKey []byte     `gorm:"type:bytea;not null" json:"-"`
	CreatedAt           time.Time  `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	ActivatesAt         time.Time  `gorm:"not null" json:"activates_at"`
	RetiresAt           *time.Time `json:"retires_at,omitempty"`
}

func (OIDCSigningKey) TableName() string { return "oidc_signing_keys" }

// OIDCSettings is the OIDC issuer setting of an organization (ProjectID nil)
// or of a project.
type OIDCSettings struct {
	SettingID      string                       `gorm:"primaryKey;type:uuid;default:generate_ulid()" json:"-"`
	OrgID          string                       `gorm:"type:uuid;not null" json:"-"`
	ProjectID      *string                      `gorm:"type:uuid" json:"-"`
	Issuer         string                       `gorm:"type:text;not null;default:'inherit'" json:"issuer"`
	AudienceClaims map[string]map[string]string `gorm:"type:jsonb;serializer:json;not null" json:"audience_claims"`
	UpdatedAt      time.Time                    `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
}

func (OIDCSettings) TableName() string { return "oidc_settings" }
