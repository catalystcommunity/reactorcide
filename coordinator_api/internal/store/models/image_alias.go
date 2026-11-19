package models

import (
	"fmt"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/imageref"
)

// Image alias kinds. They must agree with the image_aliases.kind CHECK
// constraint (coredb/migrations/000068_image_aliases.sql).
const (
	ImageAliasKindVM        = "vm"
	ImageAliasKindContainer = "container"
)

// Worker image states. They must agree with the workers.image_state CHECK
// constraint (migration 000068).
const (
	WorkerImageStateUpdating = "updating"
	WorkerImageStateUpdated  = "updated"
	WorkerImageStateFailed   = "failed"
)

// ImageAlias maps to image_aliases (migration 000068). An alias gives a name
// to one pinned image reference. ProjectID nil is an organization alias. A
// project alias with the same name replaces the organization alias for that
// project.
//
// ActiveRef and ActiveDigest are the pin that new jobs get. PendingRef and
// PendingDigest are the next pin during a rollout (owner decision I10).
type ImageAlias struct {
	AliasID   string  `gorm:"column:alias_id;primaryKey;type:uuid;default:generate_ulid()" json:"alias_id"`
	OrgID     string  `gorm:"column:org_id;type:uuid;not null" json:"org_id"`
	ProjectID *string `gorm:"column:project_id;type:uuid" json:"project_id,omitempty"`
	Name      string  `gorm:"column:name;type:text;not null" json:"name"`
	Kind      string  `gorm:"column:kind;type:text;not null" json:"kind"`
	OS        string  `gorm:"column:os;type:text;not null" json:"os"`
	Arch      string  `gorm:"column:arch;type:text;not null" json:"arch"`
	// Characteristics are more worker characteristics that a worker must
	// satisfy to get the image.
	Characteristics characteristics.Characteristics `gorm:"column:characteristics;type:jsonb;serializer:json;not null" json:"characteristics"`

	ActiveRef     string     `gorm:"column:active_ref;type:text;not null" json:"active_ref"`
	ActiveDigest  string     `gorm:"column:active_digest;type:text;not null" json:"active_digest"`
	PendingRef    *string    `gorm:"column:pending_ref;type:text" json:"pending_ref,omitempty"`
	PendingDigest *string    `gorm:"column:pending_digest;type:text" json:"pending_digest,omitempty"`
	PendingSince  *time.Time `gorm:"column:pending_since" json:"pending_since,omitempty"`

	CreatedAt time.Time `gorm:"column:created_at;autoCreateTime:false;default:now()" json:"created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at;autoUpdateTime:false;default:now()" json:"updated_at"`
	UpdatedBy string    `gorm:"column:updated_by;type:text;not null;default:''" json:"updated_by,omitempty"`
}

// TableName specifies the table name for the model.
func (ImageAlias) TableName() string {
	return "image_aliases"
}

// IsProjectAlias reports whether the alias belongs to a project.
func (a *ImageAlias) IsProjectAlias() bool { return a.ProjectID != nil && *a.ProjectID != "" }

// HasDigest reports whether digest is the active or the pending digest of
// the alias.
func (a *ImageAlias) HasDigest(digest string) bool {
	if digest == "" {
		return false
	}
	return a.ActiveDigest == digest || (a.PendingDigest != nil && *a.PendingDigest == digest)
}

// Validate checks the values that the image_aliases constraints check, and
// that each digest agrees with the digest of its reference. A VM alias must
// have a reference that the job image rules accept for VM jobs.
func (a *ImageAlias) Validate() error {
	if a.OrgID == "" {
		return fmt.Errorf("image alias: org_id is required")
	}
	if err := imageref.ValidateAliasName(a.Name); err != nil {
		return err
	}
	if a.Kind != ImageAliasKindVM && a.Kind != ImageAliasKindContainer {
		return fmt.Errorf("image alias %q: kind %q is not valid: use vm or container", a.Name, a.Kind)
	}
	if strings.TrimSpace(a.OS) == "" || strings.TrimSpace(a.Arch) == "" {
		return fmt.Errorf("image alias %q: os and arch are required", a.Name)
	}
	if err := checkPin(a.ActiveRef, a.ActiveDigest); err != nil {
		return fmt.Errorf("image alias %q: active reference: %w", a.Name, err)
	}
	if (a.PendingRef == nil) != (a.PendingDigest == nil) {
		return fmt.Errorf("image alias %q: pending_ref and pending_digest must be set together", a.Name)
	}
	if a.PendingRef != nil {
		if err := checkPin(*a.PendingRef, *a.PendingDigest); err != nil {
			return fmt.Errorf("image alias %q: pending reference: %w", a.Name, err)
		}
	}
	for key, value := range a.Characteristics {
		if value != nil && value.IsList() {
			return fmt.Errorf("image alias %q: characteristic %q must be a scalar value", a.Name, key)
		}
	}
	return nil
}

func checkPin(ref, digest string) error {
	parsed, err := imageref.ParsePinned(ref)
	if err != nil {
		return err
	}
	if parsed.Digest != digest {
		return fmt.Errorf("digest %q does not agree with the digest of reference %q", digest, ref)
	}
	return nil
}

// ImagePlatform is the kind and the platform of the image of a job. An
// empty Arch is "any arch" (a job without an arch characteristic).
type ImagePlatform struct {
	Kind string
	OS   string
	Arch string
}

// Matches reports whether alias has this kind and platform.
func (p ImagePlatform) Matches(alias *ImageAlias) bool {
	return alias != nil && alias.Kind == p.Kind && alias.OS == p.OS && (p.Arch == "" || alias.Arch == p.Arch)
}

// WorkerImageState is the image state of a worker from its newest heartbeat
// image_set_report (migration 000068). Version is the image set version of
// the report.
type WorkerImageState struct {
	State        string         `json:"state"`
	Version      int64          `json:"version"`
	Detail       string         `json:"detail,omitempty"`
	ReadyDigests []string       `json:"ready_digests"`
	Failed       []ImageFailure `json:"failed,omitempty"`
}

// ImageFailure is one image that a worker could not pull (migration
// 000071). Permanent is true when a retry with the same reference cannot
// help (not found, digest mismatch, unauthorized or denied, reference not
// valid).
type ImageFailure struct {
	Digest    string `json:"digest"`
	Permanent bool   `json:"permanent"`
	Detail    string `json:"detail"`
}

// FailureFor returns the failure of digest in failures, or nil.
func FailureFor(failures []ImageFailure, digest string) *ImageFailure {
	for i := range failures {
		if failures[i].Digest == digest {
			return &failures[i]
		}
	}
	return nil
}

// Worker runtimes that a worker sends at Register (WorkerInfo.runtime).
// They must agree with the workers.image_runtime CHECK constraint
// (migration 000069).
const (
	WorkerRuntimeVM         = "vm"
	WorkerRuntimeDocker     = "docker"
	WorkerRuntimeContainerd = "containerd"
	WorkerRuntimeKubernetes = "kubernetes"
)

// ValidWorkerRuntime reports whether runtime is a known worker runtime.
func ValidWorkerRuntime(runtime string) bool {
	switch runtime {
	case WorkerRuntimeVM, WorkerRuntimeDocker, WorkerRuntimeContainerd, WorkerRuntimeKubernetes:
		return true
	}
	return false
}

// PoolClassGrant is one worker class that a pool can serve
// (worker_class_pools joined with worker_classes).
type PoolClassGrant struct {
	PoolID    string `gorm:"column:pool_id"`
	OrgID     string `gorm:"column:org_id"`
	ClassName string `gorm:"column:class_name"`
}

// DrainingImage is an image that a job that is not terminal still uses. The
// image set of a serving worker keeps the digest until the job ends
// (VM_IMAGES_PLAN.md "Rollout").
//
// AliasName is "" for a VM job with a direct pinned reference (no alias).
// Such a row has the WorkerClass and the Characteristics of the job, so
// that the image set goes to each worker that could run the job.
type DrainingImage struct {
	OrgID           string                          `gorm:"column:org_id"`
	ProjectID       *string                         `gorm:"column:project_id"`
	AliasName       string                          `gorm:"column:image_alias"`
	Ref             string                          `gorm:"column:ref"`
	Digest          string                          `gorm:"column:digest"`
	WorkerClass     string                          `gorm:"column:worker_class"`
	Characteristics characteristics.Characteristics `gorm:"column:characteristics;serializer:json"`
}
