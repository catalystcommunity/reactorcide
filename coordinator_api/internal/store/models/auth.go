package models

import (
	"time"
)

// UISession is an opaque, server-side session for the management UI. Only
// the SHA-256 hash of the session token is ever persisted.
type UISession struct {
	SessionID  string     `gorm:"primaryKey;type:uuid;default:generate_ulid()" json:"session_id"`
	TokenHash  []byte     `gorm:"type:bytea;not null" json:"-"`
	UserID     string     `gorm:"type:uuid;not null" json:"user_id"`
	CreatedAt  time.Time  `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	ExpiresAt  time.Time  `gorm:"not null" json:"expires_at"`
	LastSeenAt time.Time  `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"last_seen_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`

	User User `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

// TableName specifies the table name for the model.
func (UISession) TableName() string {
	return "ui_sessions"
}

// IsExpired returns true if the session has passed its expiry time.
func (s *UISession) IsExpired() bool {
	return time.Now().After(s.ExpiresAt)
}

// IsRevoked returns true if the session has been explicitly revoked.
func (s *UISession) IsRevoked() bool {
	return s.RevokedAt != nil
}

// IsValid returns true if the session may still be used to authenticate.
func (s *UISession) IsValid() bool {
	return !s.IsExpired() && !s.IsRevoked()
}

// AuthIdentity is a LinkKeys subject (uuid@domain or handle@domain) linked to
// a local users row. Created on first successful login.
type AuthIdentity struct {
	IdentityID  string     `gorm:"primaryKey;type:uuid;default:generate_ulid()" json:"identity_id"`
	UserID      string     `gorm:"type:uuid;not null" json:"user_id"`
	Subject     string     `gorm:"type:text;not null" json:"subject"`
	Handle      string     `gorm:"type:text" json:"handle,omitempty"`
	Domain      string     `gorm:"type:text;not null" json:"domain"`
	DisplayName string     `gorm:"type:text" json:"display_name,omitempty"`
	CreatedAt   time.Time  `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`

	User User `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

// TableName specifies the table name for the model.
func (AuthIdentity) TableName() string {
	return "auth_identities"
}

// Well-known auth_credentials row names.
const (
	AuthCredentialLocalRPIdentity = "local_rp_identity"
	AuthCredentialRPAPIKey        = "rp_api_key"
)

// AuthCredential stores an encrypted-at-rest auth secret (e.g. the local-RP
// identity bundle or the RP API key), Fernet-encrypted under a master key —
// mirroring the org_encryption_keys convention.
type AuthCredential struct {
	Name           string    `gorm:"primaryKey;type:text" json:"name"`
	MasterKeyID    string    `gorm:"type:uuid;not null" json:"master_key_id"`
	EncryptedValue []byte    `gorm:"type:bytea;not null" json:"-"`
	CreatedAt      time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt      time.Time `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`

	MasterKey MasterKey `gorm:"foreignKey:MasterKeyID" json:"master_key,omitempty"`
}

// TableName specifies the table name for the model.
func (AuthCredential) TableName() string {
	return "auth_credentials"
}

// Source values for auth_trusted_identities.
const (
	TrustedIdentitySourceConfig = "config"
	TrustedIdentitySourceAdmin  = "admin"
)

// AuthTrustedIdentity is an exact-match admission-list entry. A row with an
// empty Handle is a bare-domain wildcard that matches any handle at that
// domain.
type AuthTrustedIdentity struct {
	Domain    string    `gorm:"primaryKey;type:text" json:"domain"`
	Handle    string    `gorm:"primaryKey;type:text;default:''" json:"handle"`
	Source    string    `gorm:"type:text;not null;default:'admin'" json:"source"`
	CreatedAt time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
}

// TableName specifies the table name for the model.
func (AuthTrustedIdentity) TableName() string {
	return "auth_trusted_identities"
}

// Matches reports whether this trusted-identity row admits the given
// domain/handle login. An empty Handle on the row is a bare-domain wildcard
// that matches any handle at that domain (including an empty handle).
func (t AuthTrustedIdentity) Matches(domain, handle string) bool {
	if t.Domain != domain {
		return false
	}
	if t.Handle == "" {
		return true
	}
	return t.Handle == handle
}

// AuthTrustedDomainPattern is an RE2 (Go regexp) admission-list pattern
// matched against the login domain.
type AuthTrustedDomainPattern struct {
	PatternID   string    `gorm:"primaryKey;type:uuid;default:generate_ulid()" json:"pattern_id"`
	Pattern     string    `gorm:"type:text;not null" json:"pattern"`
	Description string    `gorm:"type:text" json:"description,omitempty"`
	CreatedBy   *string   `gorm:"type:uuid" json:"created_by,omitempty"`
	CreatedAt   time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
}

// TableName specifies the table name for the model.
func (AuthTrustedDomainPattern) TableName() string {
	return "auth_trusted_domain_patterns"
}

// AuthLoginAttempt is a single-use, sha256-keyed pending LinkKeys login. The
// row is deleted atomically when consumed (fetch+delete).
type AuthLoginAttempt struct {
	AttemptHash  []byte    `gorm:"primaryKey;type:bytea" json:"-"`
	PendingLogin []byte    `gorm:"type:bytea;not null" json:"-"`
	CreatedAt    time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	ExpiresAt    time.Time `gorm:"not null" json:"expires_at"`
}

// TableName specifies the table name for the model.
func (AuthLoginAttempt) TableName() string {
	return "auth_login_attempts"
}

// IsExpired returns true if the pending login attempt has passed its expiry.
func (a *AuthLoginAttempt) IsExpired() bool {
	return time.Now().After(a.ExpiresAt)
}
