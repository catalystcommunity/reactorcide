package models

import "time"

// CISchedule is a coordinator-managed schedule. It runs one workflow
// definition from the repository, on a branch, at cron times. The definition
// stays in the repository; the schedule only points at it.
type CISchedule struct {
	ScheduleID string `gorm:"column:schedule_id;primaryKey;type:uuid;default:generate_ulid()" json:"schedule_id"`
	OrgID      string `gorm:"column:org_id;type:uuid;not null" json:"org_id"`
	ProjectID  string `gorm:"column:project_id;type:uuid;not null" json:"project_id"`
	Name       string `gorm:"column:name;type:text;not null" json:"name"`
	Branch     string `gorm:"column:branch;type:text;not null" json:"branch"`
	// WorkflowRef is an explicit workflow ID, or a workflow file path under
	// .reactorcide/workflows/.
	WorkflowRef string `gorm:"column:workflow_ref;type:text;not null" json:"workflow_ref"`
	Cron        string `gorm:"column:cron;type:text;not null" json:"cron"`
	Timezone    string `gorm:"column:timezone;type:text;not null;default:'UTC'" json:"timezone"`
	Enabled     bool   `gorm:"column:enabled;not null;default:true" json:"enabled"`
	// NextFireAt is nil while the schedule is disabled.
	NextFireAt  *time.Time `gorm:"column:next_fire_at" json:"next_fire_at,omitempty"`
	LastFireAt  *time.Time `gorm:"column:last_fire_at" json:"last_fire_at,omitempty"`
	LastEventID *string    `gorm:"column:last_event_id;type:uuid" json:"last_event_id,omitempty"`
	LastError   string     `gorm:"column:last_error;type:text;not null;default:''" json:"last_error"`
	CreatedBy   *string    `gorm:"column:created_by;type:uuid" json:"created_by,omitempty"`
	CreatedAt   time.Time  `gorm:"column:created_at;autoCreateTime:false;default:now()" json:"created_at"`
	UpdatedAt   time.Time  `gorm:"column:updated_at;autoUpdateTime:false;default:now()" json:"updated_at"`
}

func (CISchedule) TableName() string { return "ci_schedules" }

// Schedule fire statuses.
const (
	ScheduleFireFiring = "firing"
	ScheduleFireFired  = "fired"
	ScheduleFireFailed = "failed"
)

// CIScheduleFire records one fire of a schedule. (schedule_id, fire_time) is
// unique, so a fire happens at most once.
type CIScheduleFire struct {
	FireID      string     `gorm:"column:fire_id;primaryKey;type:uuid;default:generate_ulid()" json:"fire_id"`
	ScheduleID  string     `gorm:"column:schedule_id;type:uuid;not null" json:"schedule_id"`
	FireTime    time.Time  `gorm:"column:fire_time;not null" json:"fire_time"`
	Status      string     `gorm:"column:status;type:text;not null;default:'firing'" json:"status"`
	EventID     *string    `gorm:"column:event_id;type:uuid" json:"event_id,omitempty"`
	Error       string     `gorm:"column:error;type:text;not null;default:''" json:"error"`
	CreatedAt   time.Time  `gorm:"column:created_at;autoCreateTime:false;default:now()" json:"created_at"`
	CompletedAt *time.Time `gorm:"column:completed_at" json:"completed_at,omitempty"`
}

func (CIScheduleFire) TableName() string { return "ci_schedule_fires" }
