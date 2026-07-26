package models

import (
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
)

// WorkerPool maps to worker_pools (coredb/migrations/000022_workers.sql).
// Pools are an admin grouping/ownership surface only -- they group workers
// for display and (future) permission scoping, and own enrollment tokens.
// Pools do NOT participate in characteristic matching (WORKERS_PLAN.md
// "Workers -- registration, auth, protocol").
type WorkerPool struct {
	PoolID      string    `gorm:"column:pool_id;primaryKey;type:uuid;default:generate_ulid()" json:"pool_id"`
	OrgID       *string   `gorm:"column:org_id;type:uuid" json:"org_id,omitempty"`
	Name        string    `gorm:"column:name;type:text;not null" json:"name"`
	Description string    `gorm:"column:description;type:text" json:"description,omitempty"`
	CreatedAt   time.Time `gorm:"column:created_at;autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at;autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
}

// TableName specifies the table name for the model.
func (WorkerPool) TableName() string {
	return "worker_pools"
}

// PoolEnrollmentToken maps to pool_enrollment_tokens -- the only
// human-handled worker credential. Durable and rotatable: many rows may be
// active per pool at once (mirrors project_webhook_secrets/
// project_vcs_credentials -- see postgres_store/rotation_operations.go).
// Only TokenHash (SHA-256 via internal/checkauth.HashAPIToken, see
// internal/workerauth.HashToken) is ever persisted; the raw token is
// returned to the admin exactly once at creation
// (internal/workerauth.GenerateEnrollmentToken) and never stored.
type PoolEnrollmentToken struct {
	TokenID       string     `gorm:"column:token_id;primaryKey;type:uuid;default:generate_ulid()" json:"token_id"`
	PoolID        string     `gorm:"column:pool_id;type:uuid;not null" json:"pool_id"`
	Name          string     `gorm:"column:name;type:text" json:"name,omitempty"`
	TokenHash     []byte     `gorm:"column:token_hash;type:bytea;not null" json:"-"`
	IsActive      bool       `gorm:"column:is_active;not null;default:true" json:"is_active"`
	LastUsedAt    *time.Time `gorm:"column:last_used_at" json:"last_used_at,omitempty"`
	CreatedAt     time.Time  `gorm:"column:created_at;autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	DeactivatedAt *time.Time `gorm:"column:deactivated_at" json:"deactivated_at,omitempty"`
}

// TableName specifies the table name for the model.
func (PoolEnrollmentToken) TableName() string {
	return "pool_enrollment_tokens"
}

// Worker status values -- must stay in sync with the workers.status CHECK
// constraint in coredb/migrations/000022_workers.sql. This Go-level enum is
// documentation/consumer-convenience only, same caveat as models.Job's
// Status field: models here are hand-matched to SQL rather than
// AutoMigrated.
const (
	WorkerStatusActive      = "active"
	WorkerStatusQuarantined = "quarantined"
	WorkerStatusDisabled    = "disabled"
)

// Worker maps to workers -- one row per enrolled worker process, upserted by
// WorkerKey on each Register call (postgres_store.UpsertWorkerByKey).
// WorkerKey is a stable, worker-provided identifier generated and persisted
// locally by the worker process (like a host GUID), distinct from WorkerID
// (this row's own ULID primary key, which is regenerated if the row is ever
// recreated).
type Worker struct {
	WorkerID  string `gorm:"column:worker_id;primaryKey;type:uuid;default:generate_ulid()" json:"worker_id"`
	PoolID    string `gorm:"column:pool_id;type:uuid;not null" json:"pool_id"`
	WorkerKey string `gorm:"column:worker_key;type:text;not null" json:"worker_key"`
	Hostname  string `gorm:"column:hostname;type:text" json:"hostname,omitempty"`
	OS        string `gorm:"column:os;type:text;not null" json:"os"`
	Arch      string `gorm:"column:arch;type:text;not null" json:"arch"`

	// Characteristics is this worker's full characteristic set (os, arch,
	// and free-form custom kv pairs -- WORKERS_PLAN.md "Characteristics &
	// matching"), as sent on Register/RequestJob. Uses
	// characteristics.Characteristics' own type-preserving JSON
	// MarshalJSON/UnmarshalJSON via GORM's json serializer, exactly like
	// models.Queue.Characteristics and models.Job.Characteristics -- so a
	// StringValue("1") and an IntValue(1) never round-trip into each other,
	// and worker characteristics (which may hold list Values) use the same
	// wire encoding as job/queue characteristics (which may not).
	Characteristics characteristics.Characteristics `gorm:"column:characteristics;type:jsonb;serializer:json;not null" json:"characteristics"`

	WorkerVersion string     `gorm:"column:worker_version;type:text" json:"worker_version,omitempty"`
	Status        string     `gorm:"column:status;type:text;not null;default:'active';check:status IN ('active','quarantined','disabled')" json:"status"`
	LastSeenAt    *time.Time `gorm:"column:last_seen_at" json:"last_seen_at,omitempty"`
	EnrolledAt    *time.Time `gorm:"column:enrolled_at" json:"enrolled_at,omitempty"`
	CreatedAt     time.Time  `gorm:"column:created_at;autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
}

// TableName specifies the table name for the model.
func (Worker) TableName() string {
	return "workers"
}

// WorkerSession maps to worker_sessions -- an ephemeral, process-lifetime-
// only session minted on Register and refreshed by heartbeat
// (internal/workerauth.WorkerSessions), never shown to a human after
// minting. Only TokenHash is ever persisted, mirroring
// models.UISession/internal/auth.Sessions exactly.
type WorkerSession struct {
	SessionID  string     `gorm:"column:session_id;primaryKey;type:uuid;default:generate_ulid()" json:"session_id"`
	WorkerID   string     `gorm:"column:worker_id;type:uuid;not null" json:"worker_id"`
	TokenHash  []byte     `gorm:"column:token_hash;type:bytea;not null" json:"-"`
	ExpiresAt  time.Time  `gorm:"column:expires_at;not null" json:"expires_at"`
	LastSeenAt time.Time  `gorm:"column:last_seen_at;autoCreateTime:false;default:timezone('utc', now())" json:"last_seen_at"`
	CreatedAt  time.Time  `gorm:"column:created_at;autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	RevokedAt  *time.Time `gorm:"column:revoked_at" json:"revoked_at,omitempty"`
}

// TableName specifies the table name for the model.
func (WorkerSession) TableName() string {
	return "worker_sessions"
}

// IsExpired returns true if the session has passed its expiry time.
func (s *WorkerSession) IsExpired() bool {
	return time.Now().After(s.ExpiresAt)
}

// IsRevoked returns true if the session has been explicitly revoked.
func (s *WorkerSession) IsRevoked() bool {
	return s.RevokedAt != nil
}

// IsValid returns true if the session may still be used to authenticate.
func (s *WorkerSession) IsValid() bool {
	return !s.IsExpired() && !s.IsRevoked()
}

// WorkerLease maps to worker_leases -- a display/audit ledger row per
// worker<->job claim, created when RequestJob hands a job to a worker. Its
// lifetime tracks the corndogs task timeout/heartbeat, not an invented
// lease TTL: there is deliberately no deadline column here (WORKERS_PLAN.md
// "Workers", "Schema", and the "Still worth a quick confirm" note on
// whether this table is even needed -- kept as a slim table for
// multi-lease-per-worker display/audit).
type WorkerLease struct {
	LeaseID    string     `gorm:"column:lease_id;primaryKey;type:uuid;default:generate_ulid()" json:"lease_id"`
	WorkerID   string     `gorm:"column:worker_id;type:uuid;not null" json:"worker_id"`
	JobID      string     `gorm:"column:job_id;type:uuid;not null" json:"job_id"`
	QueueUUID  *string    `gorm:"column:queue_uuid;type:uuid" json:"queue_uuid,omitempty"`
	AcquiredAt time.Time  `gorm:"column:acquired_at;not null;default:timezone('utc', now())" json:"acquired_at"`
	ReleasedAt *time.Time `gorm:"column:released_at" json:"released_at,omitempty"`
	Outcome    string     `gorm:"column:outcome;type:text" json:"outcome,omitempty"`
}

// TableName specifies the table name for the model.
func (WorkerLease) TableName() string {
	return "worker_leases"
}

// IsActive returns true if this lease has not yet been released.
func (l *WorkerLease) IsActive() bool {
	return l.ReleasedAt == nil
}
