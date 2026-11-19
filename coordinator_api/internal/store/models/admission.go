package models

import "time"

// WorkerAdmission is the newest admission report of a worker in resources
// admission mode (workers.admission, migration 000059). A worker in the
// default concurrency mode has none.
type WorkerAdmission struct {
	Mode                  string    `json:"mode"`
	CapacityCPUMillicores int64     `json:"capacity_cpu_millicores"`
	CapacityMemoryBytes   int64     `json:"capacity_memory_bytes"`
	HeadroomCPUMillicores int64     `json:"headroom_cpu_millicores"`
	HeadroomMemoryBytes   int64     `json:"headroom_memory_bytes"`
	ReportedAt            time.Time `json:"reported_at"`
}

// WaitingJob is the admission view of one job that waits for a worker
// (status submitted or queued).
type WaitingJob struct {
	JobID               string    `gorm:"column:job_id" json:"job_id"`
	QueueName           string    `gorm:"column:queue_name" json:"queue_name"`
	Priority            int       `gorm:"column:priority" json:"priority"`
	CreatedAt           time.Time `gorm:"column:created_at" json:"created_at"`
	ResourceCPURequest  string    `gorm:"column:resource_cpu_request" json:"resource_cpu_request"`
	ResourceMemoryLimit string    `gorm:"column:resource_memory_limit" json:"resource_memory_limit"`
}

// WaitingJobGroup is the count and the oldest creation time of the waiting
// jobs of one queue with one resource shape. The backlog signal sums these
// groups; a queue has few distinct shapes, so the groups stay few.
type WaitingJobGroup struct {
	QueueName           string    `gorm:"column:queue_name" json:"queue_name"`
	ResourceCPURequest  string    `gorm:"column:resource_cpu_request" json:"resource_cpu_request"`
	ResourceMemoryLimit string    `gorm:"column:resource_memory_limit" json:"resource_memory_limit"`
	Count               int64     `gorm:"column:count" json:"count"`
	OldestCreatedAt     time.Time `gorm:"column:oldest_created_at" json:"oldest_created_at"`
}
