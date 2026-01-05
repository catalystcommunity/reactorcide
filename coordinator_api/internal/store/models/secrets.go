package models

import (
	"time"
)

// MasterKey represents a master encryption key used to encrypt org keys.
// Multiple master keys can be active simultaneously to support zero-downtime rotation.
type MasterKey struct {
	KeyID       string    `gorm:"primaryKey;type:uuid;default:generate_ulid()" json:"key_id"`
	CreatedAt   time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	Name        string    `gorm:"type:text;not null;uniqueIndex" json:"name"`
	IsActive    bool      `gorm:"not null;default:true" json:"is_active"`
	IsPrimary   bool      `gorm:"not null;default:false" json:"is_primary"`
	Description string    `gorm:"type:text" json:"description,omitempty"`
	// KeyMaterial stores the 32-byte key for auto-generated keys.
	// NULL for env-var-provided keys (those keys live only in the environment).
	KeyMaterial []byte `gorm:"type:bytea" json:"-"`
}

// TableName specifies the table name for the model
func (MasterKey) TableName() string {
	return "master_keys"
}

// OrgEncryptionKey stores an organization's encryption key, encrypted with a master key.
// Each org can have multiple entries - one per active master key for rotation support.
type OrgEncryptionKey struct {
	ID           string    `gorm:"primaryKey;type:uuid;default:generate_ulid()" json:"id"`
	CreatedAt    time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UserID       string    `gorm:"type:uuid;not null" json:"user_id"` // Will become org_id when orgs are added
	MasterKeyID  string    `gorm:"type:uuid;not null" json:"master_key_id"`
	EncryptedKey []byte    `gorm:"type:bytea;not null" json:"-"`
	Salt         []byte    `gorm:"type:bytea;not null" json:"-"`

	// Relationships
	User      User      `gorm:"foreignKey:UserID" json:"user,omitempty"`
	MasterKey MasterKey `gorm:"foreignKey:MasterKeyID" json:"master_key,omitempty"`
}

// TableName specifies the table name for the model
func (OrgEncryptionKey) TableName() string {
	return "org_encryption_keys"
}

// Secret represents an individual encrypted secret value.
type Secret struct {
	SecretID       string    `gorm:"primaryKey;type:uuid;default:generate_ulid()" json:"secret_id"`
	CreatedAt      time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt      time.Time `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
	UserID         string    `gorm:"type:uuid;not null" json:"user_id"` // Ownership
	Namespace      string    `gorm:"type:text;not null" json:"namespace"`
	Path           string    `gorm:"type:text;not null" json:"path"`
	Key            string    `gorm:"type:text;not null" json:"key"`
	EncryptedValue []byte    `gorm:"type:bytea;not null" json:"-"`

	// Relationships
	User User `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

// TableName specifies the table name for the model
func (Secret) TableName() string {
	return "secrets"
}
