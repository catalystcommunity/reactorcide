package models

import (
	"time"

	"github.com/lib/pq"
)

type ExecutionProfile struct {
	ProfileID             string         `gorm:"column:profile_id;primaryKey;type:uuid;default:generate_ulid()" json:"profile_id" yaml:"profile_id,omitempty"`
	OrgID                 string         `gorm:"column:org_id;type:uuid;not null" json:"org_id" yaml:"org_id,omitempty"`
	Name                  string         `gorm:"type:text;not null" json:"name" yaml:"name"`
	DenySecrets           bool           `gorm:"not null" json:"deny_secrets" yaml:"deny_secrets"`
	SecretPathAllowlist   pq.StringArray `gorm:"type:text[]" json:"secret_path_allowlist" yaml:"secret_path_allowlist,omitempty"`
	RuntimeCapabilities   pq.StringArray `gorm:"type:text[]" json:"runtime_capabilities" yaml:"runtime_capabilities,omitempty"`
	MayRunAsRoot          bool           `gorm:"not null" json:"may_run_as_root" yaml:"may_run_as_root"`
	AllowedWorkerClasses  pq.StringArray `gorm:"type:text[]" json:"allowed_worker_classes" yaml:"allowed_worker_classes,omitempty"`
	TimeoutCeilingSeconds *int           `json:"timeout_ceiling_seconds" yaml:"timeout_ceiling_seconds,omitempty"`
	ResourceCeilings      JSONB          `gorm:"type:jsonb" json:"resource_ceilings" yaml:"resource_ceilings,omitempty"`
	CacheNamespace        *string        `json:"cache_namespace" yaml:"cache_namespace,omitempty"`
	ArtifactNamespace     *string        `json:"artifact_namespace" yaml:"artifact_namespace,omitempty"`
	TrustedCacheWrites    bool           `gorm:"not null" json:"trusted_cache_writes" yaml:"trusted_cache_writes"`
	CreatedAt             time.Time      `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at" yaml:"created_at,omitempty"`
	UpdatedAt             time.Time      `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at" yaml:"updated_at,omitempty"`
}

func (ExecutionProfile) TableName() string { return "execution_profiles" }
