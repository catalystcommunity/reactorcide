package models

import (
	"time"

	"github.com/lib/pq"
)

// Environment default scopes (docs/builds-and-caches.md).
const (
	EnvDefaultScopeOrg         = "org"
	EnvDefaultScopeWorkerClass = "worker_class"
	EnvDefaultScopeProject     = "project"
)

// EnvDefault is one non-secret environment default of an organization, a
// worker class, or a project. Precedence in the job environment: job >
// project > worker class > organization.
type EnvDefault struct {
	DefaultID     string    `gorm:"column:default_id;primaryKey;type:uuid;default:generate_ulid()" json:"-"`
	OrgID         string    `gorm:"column:org_id;type:uuid;not null" json:"-"`
	Scope         string    `gorm:"type:text;not null" json:"scope"`
	WorkerClassID *string   `gorm:"column:worker_class_id;type:uuid" json:"-"`
	ProjectID     *string   `gorm:"column:project_id;type:uuid" json:"project_id,omitempty"`
	Key           string    `gorm:"type:text;not null" json:"key"`
	Value         string    `gorm:"type:text;not null" json:"value"`
	CreatedAt     time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt     time.Time `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
	// WorkerClass is the worker class name, filled on read.
	WorkerClass string `gorm:"-" json:"worker_class,omitempty"`
}

func (EnvDefault) TableName() string { return "environment_defaults" }

// ImagePolicy is the job image policy of an organization
// (docs/builds-and-caches.md). Off by default.
type ImagePolicy struct {
	OrgID string `gorm:"column:org_id;primaryKey;type:uuid" json:"-"`
	// Enabled turns the policy on.
	Enabled bool `gorm:"not null;default:false" json:"enabled"`
	// AllowedPrefixes are the image reference prefixes that jobs may use,
	// for example "ghcr.io/example/". Empty with Enabled permits no image.
	AllowedPrefixes pq.StringArray `gorm:"type:text[];not null;default:'{}'" json:"allowed_prefixes"`
	// RequireDigest refuses an image reference with no "@sha256:" digest.
	RequireDigest bool      `gorm:"not null;default:false" json:"require_digest"`
	UpdatedAt     time.Time `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
}

func (ImagePolicy) TableName() string { return "organization_image_policies" }

// JobLink is a named link that a job publishes in its workflow output, for
// example an external build results page.
type JobLink struct {
	JobID     string    `gorm:"column:job_id;primaryKey;type:uuid" json:"-"`
	Position  int       `gorm:"not null" json:"-"`
	Name      string    `gorm:"primaryKey;type:text" json:"name"`
	URL       string    `gorm:"column:url;type:text;not null" json:"url"`
	CreatedAt time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
}

func (JobLink) TableName() string { return "job_links" }
