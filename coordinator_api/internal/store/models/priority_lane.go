package models

import (
	"time"

	"github.com/lib/pq"
)

// Job kinds that a priority lane can match.
const (
	PriorityJobKindAny  = ""
	PriorityJobKindEval = "eval"
	PriorityJobKindWork = "work"
)

// PriorityLane is an operator lane (docs/priority.md). ProjectID nil makes it
// an organization lane. A lane matches when every configured fact matches;
// an empty list or a nil value is no condition. A stored lane with the name
// of a built-in lane or of an organization lane replaces that lane in its
// scope.
type PriorityLane struct {
	LaneID      string    `gorm:"column:lane_id;primaryKey;type:uuid;default:generate_ulid()" json:"lane_id"`
	OrgID       string    `gorm:"column:org_id;type:uuid;not null" json:"org_id"`
	ProjectID   *string   `gorm:"column:project_id;type:uuid" json:"project_id,omitempty"`
	Name        string    `gorm:"type:text;not null" json:"name"`
	Position    int       `gorm:"not null;default:0" json:"position"`
	Description string    `gorm:"type:text;not null;default:''" json:"description,omitempty"`
	Base        int       `gorm:"not null" json:"base"`
	CreatedAt   time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	UpdatedAt   time.Time `gorm:"autoUpdateTime:false;default:timezone('utc', now())" json:"updated_at"`

	// Match facts.
	ClassesAny  pq.StringArray `gorm:"type:text[];not null" json:"classes_any"`
	ClassesNone pq.StringArray `gorm:"type:text[];not null" json:"classes_none"`
	EventTypes  pq.StringArray `gorm:"type:text[];not null" json:"event_types"`
	Draft       *bool          `gorm:"column:draft" json:"draft,omitempty"`
	JobKind     string         `gorm:"type:text;not null;default:''" json:"job_kind,omitempty"`
	// Retry true makes a lane for retries only. A retry keeps the lane of
	// the original job unless a retry lane matches.
	Retry   bool `gorm:"not null;default:false" json:"retry,omitempty"`
	Enabled bool `gorm:"not null" json:"enabled"`
}

func (PriorityLane) TableName() string { return "priority_lanes" }

// PriorityShift is one shift that applied to the priority of a job.
type PriorityShift struct {
	// Source is "group" (a group of the actor) or "owner_group" (the owner
	// group of the workflow), or "shed" (the shed level in force), or
	// "requested" (the priority that the job specification or the submitter
	// asked for).
	Source string `json:"source"`
	// Group is the group name for a group shift.
	Group string `json:"group,omitempty"`
	Value int    `json:"value"`
	// Applied is false for a shift that the combine rule did not use (for
	// example a group shift that is not the largest).
	Applied bool `json:"applied"`
}

// GroupPriorityShift is the priority shift of one group.
type GroupPriorityShift struct {
	GroupID string `json:"group_id"`
	Name    string `json:"name"`
	Shift   int    `json:"shift"`
}
