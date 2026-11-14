package models

import "time"

// JobResourceSummary is the resource summary of one finished job, computed
// from its stored telemetry (job roll-up series only). A nil measurement was
// not measured for that run; it is not a zero. See
// coredb/migrations/000040_job_resource_summaries.sql.
type JobResourceSummary struct {
	JobID          string    `gorm:"primaryKey;type:uuid" json:"job_id"`
	SummaryVersion int       `gorm:"not null" json:"summary_version"`
	ComputedAt     time.Time `gorm:"not null" json:"computed_at"`
	SampleCount    int       `gorm:"not null;default:0" json:"sample_count"`

	CPUAvgMillicores     *int64     `gorm:"column:cpu_avg_millicores" json:"cpu_avg_millicores,omitempty"`
	CPUP95Millicores     *int64     `gorm:"column:cpu_p95_millicores" json:"cpu_p95_millicores,omitempty"`
	CPUMaxMillicores     *int64     `gorm:"column:cpu_max_millicores" json:"cpu_max_millicores,omitempty"`
	CPUPeakAt            *time.Time `gorm:"column:cpu_peak_at" json:"cpu_peak_at,omitempty"`
	CPURequestMillicores *int64     `gorm:"column:cpu_request_millicores" json:"cpu_request_millicores,omitempty"`
	CPULimitMillicores   *int64     `gorm:"column:cpu_limit_millicores" json:"cpu_limit_millicores,omitempty"`

	MemoryAvgBytes   *int64     `gorm:"column:memory_avg_bytes" json:"memory_avg_bytes,omitempty"`
	MemoryP95Bytes   *int64     `gorm:"column:memory_p95_bytes" json:"memory_p95_bytes,omitempty"`
	MemoryMaxBytes   *int64     `gorm:"column:memory_max_bytes" json:"memory_max_bytes,omitempty"`
	MemoryPeakAt     *time.Time `gorm:"column:memory_peak_at" json:"memory_peak_at,omitempty"`
	MemoryLimitBytes *int64     `gorm:"column:memory_limit_bytes" json:"memory_limit_bytes,omitempty"`

	StorageMaxBytes *int64 `gorm:"column:storage_max_bytes" json:"storage_max_bytes,omitempty"`
}

// TableName specifies the table name for the model
func (JobResourceSummary) TableName() string {
	return "job_resource_summaries"
}

// JobRunWithSummary is one run of a job with its resource summary, or nil when
// none has been computed yet.
type JobRunWithSummary struct {
	Job     Job
	Summary *JobResourceSummary
}
