package models

import (
	"time"

	"github.com/lib/pq"
)

// Outbound events (docs/outbound-events.md, coredb/migrations/000058).

// Outbound event types. Keep this list equal to the triggers of migration
// 000058 and to docs/outbound-events.md.
const (
	OutboundEventNodePreStart     = "node.pre_start"
	OutboundEventNodeStarted      = "node.started"
	OutboundEventNodePostComplete = "node.post_complete"
	OutboundEventNodeSkipped      = "node.skipped"
	OutboundEventEventEvaluated   = "event.evaluated"
	OutboundEventEventAdmitted    = "event.admitted"
	OutboundEventEventNotAdmitted = "event.not_admitted"
	OutboundEventWorkflowComplete = "workflow.completed"
	// OutboundEventTest is the type of a test send. It never goes through
	// the outbox.
	OutboundEventTest = "test.ping"
)

// OutboundEventTypes lists the event types that a subscription can filter on.
var OutboundEventTypes = []string{
	OutboundEventNodePreStart, OutboundEventNodeStarted, OutboundEventNodePostComplete, OutboundEventNodeSkipped,
	OutboundEventEventEvaluated, OutboundEventEventAdmitted, OutboundEventEventNotAdmitted,
	OutboundEventWorkflowComplete,
	// Pipeline owner alerts (observability.go, docs/observability.md). The
	// rollup loop writes them to the outbox; no trigger does.
	OutboundEventAlertFiring, OutboundEventAlertResolved,
}

// Subscription scopes.
const (
	SubscriptionScopeOrg        = "org"
	SubscriptionScopeProject    = "project"
	SubscriptionScopeWorkflow   = "workflow"
	SubscriptionScopeOwnerGroup = "owner_group"
)

// Sink types. Only the webhook exists now (D27).
const (
	SinkTypeWebhook = "webhook"
)

// Delivery statuses.
const (
	DeliveryStatusPending    = "pending"
	DeliveryStatusQueued     = "queued"
	DeliveryStatusSending    = "sending"
	DeliveryStatusDelivered  = "delivered"
	DeliveryStatusDeadLetter = "dead_letter"
)

// Attempt results.
const (
	DeliveryAttemptDelivered = "delivered"
	DeliveryAttemptFailed    = "failed"
	// DeliveryAttemptRefused: the destination is not permitted (for example a
	// private address). The delivery goes to dead_letter at once.
	DeliveryAttemptRefused = "refused"
)

// EventOutbox is one phase event that a trigger recorded in the transaction
// of its status change.
type EventOutbox struct {
	OutboxID           int64          `gorm:"column:outbox_id;primaryKey;autoIncrement" json:"outbox_id"`
	EventID            string         `gorm:"column:event_id;type:uuid;default:generate_ulid()" json:"event_id"`
	SchemaVersion      int            `gorm:"column:schema_version;not null;default:1" json:"schema_version"`
	EventType          string         `gorm:"column:event_type;type:text;not null" json:"event_type"`
	OccurredAt         time.Time      `gorm:"column:occurred_at;autoCreateTime:false" json:"occurred_at"`
	OrgID              string         `gorm:"column:org_id;type:uuid;not null" json:"org_id"`
	ProjectID          *string        `gorm:"column:project_id;type:uuid" json:"project_id,omitempty"`
	CIEventID          *string        `gorm:"column:ci_event_id;type:uuid" json:"ci_event_id,omitempty"`
	WorkflowID         *string        `gorm:"column:workflow_id;type:uuid" json:"workflow_id,omitempty"`
	WorkflowSecurityID string         `gorm:"column:workflow_security_id;type:text;not null;default:''" json:"workflow_security_id"`
	NodeID             *string        `gorm:"column:node_id;type:uuid" json:"node_id,omitempty"`
	JobID              *string        `gorm:"column:job_id;type:uuid" json:"job_id,omitempty"`
	Status             string         `gorm:"column:status;type:text;not null;default:''" json:"status"`
	Result             string         `gorm:"column:result;type:text;not null;default:''" json:"result"`
	FailureClass       string         `gorm:"column:failure_class;type:text;not null;default:''" json:"failure_class"`
	ActorPrincipalID   *string        `gorm:"column:actor_principal_id;type:uuid" json:"actor_principal_id,omitempty"`
	Ref                string         `gorm:"column:ref;type:text;not null;default:''" json:"ref"`
	CommitSHA          string         `gorm:"column:commit_sha;type:text;not null;default:''" json:"commit_sha"`
	RefClasses         pq.StringArray `gorm:"column:ref_classes;type:text[]" json:"ref_classes"`
	OwnerGroup         string         `gorm:"column:owner_group;type:text;not null;default:''" json:"owner_group"`
	Details            JSONB          `gorm:"column:details;type:jsonb" json:"details"`
	OrderKey           string         `gorm:"column:order_key;type:text;not null" json:"order_key"`
	Sequence           *int64         `gorm:"column:sequence" json:"sequence,omitempty"`
	Payload            JSONB          `gorm:"column:payload;type:jsonb" json:"payload,omitempty"`
	DispatchedAt       *time.Time     `gorm:"column:dispatched_at" json:"dispatched_at,omitempty"`
}

func (EventOutbox) TableName() string { return "event_outbox" }

// OutboxEventFacts is an outbox row with the names and facts that the
// dispatcher reads from other tables to build the payload.
type OutboxEventFacts struct {
	EventOutbox
	OrgName          string `gorm:"column:org_name"`
	ProjectName      string `gorm:"column:project_name"`
	WorkflowName     string `gorm:"column:workflow_name"`
	WorkflowSecID    string `gorm:"column:wf_security_id"`
	NodeName         string `gorm:"column:node_name"`
	JobName          string `gorm:"column:job_name"`
	ActorKind        string `gorm:"column:actor_kind"`
	ActorDisplayName string `gorm:"column:actor_display_name"`
	EventRef         string `gorm:"column:event_ref"`
	EventSHA         string `gorm:"column:event_sha"`
	VCSRepo          string `gorm:"column:vcs_repo"`
}

// EventSubscription is a subscription to outbound events.
type EventSubscription struct {
	SubscriptionID          string         `gorm:"column:subscription_id;primaryKey;type:uuid;default:generate_ulid()" json:"subscription_id"`
	OrgID                   string         `gorm:"column:org_id;type:uuid;not null" json:"org_id"`
	Name                    string         `gorm:"column:name;type:text;not null" json:"name"`
	Description             string         `gorm:"column:description;type:text;not null;default:''" json:"description"`
	OwnerPrincipalID        string         `gorm:"column:owner_principal_id;type:uuid;not null" json:"owner_principal_id"`
	Scope                   string         `gorm:"column:scope;type:text;not null" json:"scope"`
	ProjectID               *string        `gorm:"column:project_id;type:uuid" json:"project_id,omitempty"`
	WorkflowSecurityID      string         `gorm:"column:workflow_security_id;type:text;not null;default:''" json:"workflow_security_id,omitempty"`
	OwnerGroup              string         `gorm:"column:owner_group;type:text;not null;default:''" json:"owner_group,omitempty"`
	EventTypes              pq.StringArray `gorm:"column:event_types;type:text[];not null;default:'{}'" json:"event_types"`
	RefClassesAny           pq.StringArray `gorm:"column:ref_classes_any;type:text[];not null;default:'{}'" json:"ref_classes_any"`
	Results                 pq.StringArray `gorm:"column:results;type:text[];not null;default:'{}'" json:"results"`
	ActorKinds              pq.StringArray `gorm:"column:actor_kinds;type:text[];not null;default:'{}'" json:"actor_kinds"`
	FailureClasses          pq.StringArray `gorm:"column:failure_classes;type:text[];not null;default:'{}'" json:"failure_classes"` // 000065: empty matches every event
	SinkType                string         `gorm:"column:sink_type;type:text;not null" json:"sink_type"`
	SinkConfig              JSONB          `gorm:"column:sink_config;type:jsonb;not null;default:'{}'" json:"sink_config"`
	SigningSecretCiphertext []byte         `gorm:"column:signing_secret_ciphertext;type:bytea;not null" json:"-"`
	SigningSecretKeyName    string         `gorm:"column:signing_secret_key_name;type:text;not null" json:"-"`
	MaxAttempts             int            `gorm:"column:max_attempts;not null;default:0" json:"max_attempts"`
	Enabled                 bool           `gorm:"column:enabled;not null;default:true" json:"enabled"`
	CreatedBy               *string        `gorm:"column:created_by;type:uuid" json:"created_by,omitempty"`
	CreatedAt               time.Time      `gorm:"column:created_at;autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt               time.Time      `gorm:"column:updated_at;autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`

	// OwnerActive is read with the subscription for the dispatcher: false
	// when the owner principal is disabled. It is not a column.
	OwnerActive bool `gorm:"column:owner_active;->;-:migration" json:"-"`
}

func (EventSubscription) TableName() string { return "event_subscriptions" }

// WebhookURL returns the sink URL of a webhook subscription.
func (s *EventSubscription) WebhookURL() string {
	if s == nil || s.SinkConfig == nil {
		return ""
	}
	value, _ := s.SinkConfig["url"].(string)
	return value
}

// EventDelivery is the delivery of one event to one subscription.
type EventDelivery struct {
	DeliveryID     string     `gorm:"column:delivery_id;primaryKey;type:uuid;default:generate_ulid()" json:"delivery_id"`
	SubscriptionID string     `gorm:"column:subscription_id;type:uuid;not null" json:"subscription_id"`
	OutboxID       int64      `gorm:"column:outbox_id;not null" json:"outbox_id"`
	EventID        string     `gorm:"column:event_id;type:uuid;not null" json:"event_id"`
	EventType      string     `gorm:"column:event_type;type:text;not null" json:"event_type"`
	OrderKey       string     `gorm:"column:order_key;type:text;not null" json:"order_key"`
	Sequence       int64      `gorm:"column:sequence;not null" json:"sequence"`
	Status         string     `gorm:"column:status;type:text;not null;default:'pending'" json:"status"`
	Attempts       int        `gorm:"column:attempts;not null;default:0" json:"attempts"`
	Generation     int        `gorm:"column:generation;not null;default:0" json:"generation"`
	TaskKey        string     `gorm:"column:task_key;type:text;not null;default:''" json:"-"`
	TaskID         string     `gorm:"column:task_id;type:text;not null;default:''" json:"task_id,omitempty"`
	NextAttemptAt  time.Time  `gorm:"column:next_attempt_at;autoCreateTime:false;default:timezone('utc', now())" json:"next_attempt_at"`
	LastHTTPStatus *int       `gorm:"column:last_http_status" json:"last_http_status,omitempty"`
	LastLatencyMs  *int       `gorm:"column:last_latency_ms" json:"last_latency_ms,omitempty"`
	LastError      string     `gorm:"column:last_error;type:text;not null;default:''" json:"last_error"`
	QueuedAt       *time.Time `gorm:"column:queued_at" json:"queued_at,omitempty"`
	DeliveredAt    *time.Time `gorm:"column:delivered_at" json:"delivered_at,omitempty"`
	DeadLetteredAt *time.Time `gorm:"column:dead_lettered_at" json:"dead_lettered_at,omitempty"`
	CreatedAt      time.Time  `gorm:"column:created_at;autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt      time.Time  `gorm:"column:updated_at;autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`
}

func (EventDelivery) TableName() string { return "event_deliveries" }

// IsOpen reports whether the delivery is not final.
func (d *EventDelivery) IsOpen() bool {
	return d.Status == DeliveryStatusPending || d.Status == DeliveryStatusQueued || d.Status == DeliveryStatusSending
}

// EventDeliveryAttempt is one send of a delivery to its sink.
type EventDeliveryAttempt struct {
	AttemptID  int64     `gorm:"column:attempt_id;primaryKey;autoIncrement" json:"attempt_id"`
	DeliveryID string    `gorm:"column:delivery_id;type:uuid;not null" json:"delivery_id"`
	Attempt    int       `gorm:"column:attempt;not null" json:"attempt"`
	StartedAt  time.Time `gorm:"column:started_at;not null" json:"started_at"`
	FinishedAt time.Time `gorm:"column:finished_at;not null" json:"finished_at"`
	Result     string    `gorm:"column:result;type:text;not null" json:"result"`
	HTTPStatus *int      `gorm:"column:http_status" json:"http_status,omitempty"`
	LatencyMs  int       `gorm:"column:latency_ms;not null;default:0" json:"latency_ms"`
	Error      string    `gorm:"column:error;type:text;not null;default:''" json:"error"`
}

func (EventDeliveryAttempt) TableName() string { return "event_delivery_attempts" }

// OutboxDispatch is the result of the fan-out of one outbox row: its
// payload and one delivery for each matching subscription. The dispatcher
// writes all of it in one transaction.
type OutboxDispatch struct {
	OutboxID int64
	OrderKey string
	// Payload builds the payload from the sequence that the store assigns.
	Payload func(sequence int64) JSONB
	// SubscriptionIDs are the matching subscriptions.
	SubscriptionIDs []string
	EventID         string
	EventType       string
}

// DeliveryWork is what the delivery loop needs to send one delivery.
type DeliveryWork struct {
	Delivery     EventDelivery
	Subscription EventSubscription
	Payload      JSONB
	OccurredAt   time.Time
}

// DeliveryAttemptResult records the result of one send.
type DeliveryAttemptResult struct {
	DeliveryID string
	TaskKey    string
	StartedAt  time.Time
	FinishedAt time.Time
	Result     string
	HTTPStatus *int
	LatencyMs  int
	Error      string
	// NextStatus is pending (retry at NextAttemptAt), delivered, or
	// dead_letter.
	NextStatus    string
	NextAttemptAt time.Time
}
