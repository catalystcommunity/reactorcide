package models

import (
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
)

// Queue maps to the queues table (coredb/migrations/000020_queues.sql). A
// queue's identity in Corndogs is exactly its QueueUUID string; this row is
// the source of truth for what characteristics that UUID represents. See
// WORKERS_PLAN.md "Queues" and internal/store/postgres_store/
// queue_operations.go's FindOrCreateQueueByCharacteristics.
type Queue struct {
	QueueID   string    `gorm:"column:queue_id;primaryKey;type:uuid;default:generate_ulid()" json:"queue_id"`
	CreatedAt time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt time.Time `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`

	// QueueUUID is the Corndogs queue name -- literally this string, nothing
	// else. Distinct from QueueID (the row's own ULID primary key) so the
	// routing identifier can be regenerated/reasoned about independently of
	// row identity, though in practice it is set once at creation and never
	// changes (characteristics, and therefore the queue a job resolves to,
	// are immutable per WORKERS_PLAN.md "Operator control").
	QueueUUID string `gorm:"column:queue_uuid;type:uuid;not null;default:gen_random_uuid()" json:"queue_uuid"`

	// Characteristics is this queue's immutable characteristic set. Uses
	// characteristics.Characteristics' own type-preserving JSON
	// MarshalJSON/UnmarshalJSON (see internal/characteristics/wire.go) via
	// GORM's json serializer, so a StringValue("1") and an IntValue(1) never
	// round-trip into each other.
	Characteristics characteristics.Characteristics `gorm:"column:characteristics;type:jsonb;serializer:json;not null" json:"characteristics"`

	// CharacteristicsHash is characteristics.Hash(Characteristics) -- a
	// canonicalized, type-sensitive SHA-256 hex digest. This is what
	// find-or-create queries key on; it is computed in Go
	// (internal/characteristics.Hash), never in SQL, so there is exactly one
	// implementation of "these characteristic sets are the same queue".
	CharacteristicsHash string  `gorm:"column:characteristics_hash;type:text;not null" json:"characteristics_hash"`
	DisplayName         string  `gorm:"column:display_name;type:text" json:"display_name,omitempty"`
	IsDefault           bool    `gorm:"column:is_default;not null;default:false" json:"is_default"`
	OrgID               *string `gorm:"column:org_id;type:uuid" json:"org_id,omitempty"`
}

// TableName specifies the table name for the model.
func (Queue) TableName() string {
	return "queues"
}
