package models

import "time"

// Artifact kinds (docs/artifacts-and-tests.md).
const (
	ArtifactKindFile       = "file"
	ArtifactKindTestReport = "test_report"
	ArtifactKindProvenance = "provenance"
)

// Artifact upload states.
const (
	ArtifactStatusUploading = "uploading"
	ArtifactStatusComplete  = "complete"
	ArtifactStatusFailed    = "failed"
)

// JobArtifact is one file that a job published. The coordinator computes
// SizeBytes and SHA256 from the bytes that it stored; the worker cannot set
// them.
type JobArtifact struct {
	ArtifactID        string     `gorm:"column:artifact_id;primaryKey;type:uuid;default:generate_ulid()" json:"artifact_id"`
	JobID             string     `gorm:"column:job_id;type:uuid;not null" json:"job_id"`
	Name              string     `gorm:"type:text;not null" json:"name"`
	Path              string     `gorm:"type:text;not null" json:"path"`
	Kind              string     `gorm:"type:text;not null;default:'file'" json:"kind"`
	MediaType         string     `gorm:"type:text;not null" json:"media_type"`
	SizeBytes         int64      `gorm:"column:size_bytes;not null;default:0" json:"size_bytes"`
	SHA256            string     `gorm:"column:sha256;type:text;not null;default:''" json:"sha256,omitempty"`
	Status            string     `gorm:"type:text;not null;default:'uploading'" json:"status"`
	StatusReason      string     `gorm:"type:text;not null;default:''" json:"status_reason,omitempty"`
	ArtifactNamespace string     `gorm:"column:artifact_namespace;type:text;not null;default:''" json:"artifact_namespace,omitempty"`
	StoragePrefix     string     `gorm:"type:text;not null" json:"-"`
	ChunkCount        int        `gorm:"not null;default:0" json:"-"`
	HashState         []byte     `gorm:"type:bytea" json:"-"`
	CreatedAt         time.Time  `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	CompletedAt       *time.Time `json:"completed_at,omitempty"`
}

func (JobArtifact) TableName() string { return "job_artifacts" }

// Test result states.
const (
	TestStatusPass  = "pass"
	TestStatusFail  = "fail"
	TestStatusError = "error"
	TestStatusSkip  = "skip"
)

// TestResult is one test case of a JUnit report of a job.
type TestResult struct {
	ResultID   string    `gorm:"column:result_id;primaryKey;type:uuid;default:generate_ulid()" json:"result_id"`
	JobID      string    `gorm:"column:job_id;type:uuid;not null" json:"job_id"`
	ProjectID  *string   `gorm:"column:project_id;type:uuid" json:"project_id,omitempty"`
	ArtifactID *string   `gorm:"column:artifact_id;type:uuid" json:"artifact_id,omitempty"`
	TestID     string    `gorm:"column:test_id;type:text;not null" json:"test_id"`
	Suite      string    `gorm:"type:text;not null;default:''" json:"suite"`
	Classname  string    `gorm:"type:text;not null;default:''" json:"classname"`
	Name       string    `gorm:"type:text;not null" json:"name"`
	Status     string    `gorm:"type:text;not null" json:"status"`
	DurationMs int64     `gorm:"column:duration_ms;not null;default:0" json:"duration_ms"`
	Message    string    `gorm:"type:text;not null;default:''" json:"message,omitempty"`
	File       string    `gorm:"type:text;not null;default:''" json:"file,omitempty"`
	Line       *int      `json:"line,omitempty"`
	CommitSHA  string    `gorm:"column:commit_sha;type:text;not null;default:''" json:"commit_sha,omitempty"`
	CreatedAt  time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
}

func (TestResult) TableName() string { return "test_results" }

// Failed reports whether the result is a failure or an error.
func (r TestResult) Failed() bool {
	return r.Status == TestStatusFail || r.Status == TestStatusError
}

// Quarantine policies of a workflow node (job YAML `quarantine_policy`).
const (
	// QuarantinePolicyReport (the default, also "") keeps a failed job
	// failed. Quarantined failures show as quarantined in the reports.
	QuarantinePolicyReport = "report"
	// QuarantinePolicyIgnoreQuarantined makes the workflow node a success
	// when the job failed and every failed test of its reports is
	// quarantined. The job status and exit code do not change.
	QuarantinePolicyIgnoreQuarantined = "ignore_quarantined"
)

// ValidQuarantinePolicy reports whether value is a known policy ("" is the
// default).
func ValidQuarantinePolicy(value string) bool {
	return value == "" || value == QuarantinePolicyReport || value == QuarantinePolicyIgnoreQuarantined
}

// JobTestSummary is the test summary of one job.
type JobTestSummary struct {
	JobID               string   `gorm:"column:job_id;primaryKey;type:uuid" json:"job_id"`
	Reports             int      `gorm:"not null;default:0" json:"reports"`
	Total               int      `gorm:"not null;default:0" json:"total"`
	Passed              int      `gorm:"not null;default:0" json:"passed"`
	Failed              int      `gorm:"not null;default:0" json:"failed"`
	Errored             int      `gorm:"not null;default:0" json:"errored"`
	Skipped             int      `gorm:"not null;default:0" json:"skipped"`
	QuarantinedFailures int      `gorm:"not null;default:0" json:"quarantined_failures"`
	Flaky               int      `gorm:"not null;default:0" json:"flaky"`
	Truncated           bool     `gorm:"not null;default:false" json:"truncated"`
	ParseErrors         []string `gorm:"type:jsonb;serializer:json;not null" json:"parse_errors"`
	QuarantinePolicy    string   `gorm:"type:text;not null;default:''" json:"quarantine_policy,omitempty"`
	QuarantineOverride  bool     `gorm:"not null;default:false" json:"quarantine_override"`
	// TestExit: the workflow output of the job declared test_exit: true.
	// The quarantine override applies only then.
	TestExit  bool      `gorm:"column:test_exit;not null;default:false" json:"test_exit"`
	CreatedAt time.Time `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
}

func (JobTestSummary) TableName() string { return "job_test_summaries" }

// UnquarantinedFailures is the number of failed or errored tests that no
// quarantine covers.
func (s JobTestSummary) UnquarantinedFailures() int {
	n := s.Failed + s.Errored - s.QuarantinedFailures
	if n < 0 {
		return 0
	}
	return n
}

// TestFlakiness is the flakiness record of one test in one project.
type TestFlakiness struct {
	ProjectID    string     `gorm:"column:project_id;primaryKey;type:uuid" json:"project_id"`
	TestID       string     `gorm:"column:test_id;primaryKey;type:text" json:"test_id"`
	Suite        string     `gorm:"type:text;not null;default:''" json:"suite"`
	Classname    string     `gorm:"type:text;not null;default:''" json:"classname"`
	Name         string     `gorm:"type:text;not null;default:''" json:"name"`
	FlakyCount   int        `gorm:"not null;default:0" json:"flaky_count"`
	TotalRuns    int        `gorm:"not null;default:0" json:"total_runs"`
	LastFlakyAt  *time.Time `json:"last_flaky_at,omitempty"`
	LastFlakySHA string     `gorm:"column:last_flaky_sha;type:text;not null;default:''" json:"last_flaky_sha,omitempty"`
	LastSeenAt   time.Time  `json:"last_seen_at"`
}

func (TestFlakiness) TableName() string { return "test_flakiness" }

// TestQuarantine removes one test of a project from failure counts until
// ExpiresAt.
type TestQuarantine struct {
	QuarantineID         string     `gorm:"column:quarantine_id;primaryKey;type:uuid;default:generate_ulid()" json:"quarantine_id"`
	OrgID                string     `gorm:"column:org_id;type:uuid;not null" json:"-"`
	ProjectID            string     `gorm:"column:project_id;type:uuid;not null" json:"project_id"`
	TestID               string     `gorm:"column:test_id;type:text;not null" json:"test_id"`
	Suite                string     `gorm:"type:text;not null;default:''" json:"suite"`
	Classname            string     `gorm:"type:text;not null;default:''" json:"classname"`
	Name                 string     `gorm:"type:text;not null;default:''" json:"name"`
	OwnerPrincipalID     string     `gorm:"column:owner_principal_id;type:uuid;not null" json:"owner_principal_id"`
	Reason               string     `gorm:"type:text;not null" json:"reason"`
	ExpiresAt            time.Time  `gorm:"not null" json:"expires_at"`
	CreatedByPrincipalID *string    `gorm:"column:created_by_principal_id;type:uuid" json:"created_by_principal_id,omitempty"`
	CreatedBy            string     `gorm:"type:text;not null;default:''" json:"created_by,omitempty"`
	CreatedAt            time.Time  `gorm:"autoCreateTime:false;default:timezone('utc', now())" json:"created_at"`
	RemovedAt            *time.Time `json:"removed_at,omitempty"`
	RemovedBy            string     `gorm:"type:text;not null;default:''" json:"removed_by,omitempty"`
}

func (TestQuarantine) TableName() string { return "test_quarantines" }

// ActiveAt reports whether the quarantine applies at now.
func (q TestQuarantine) ActiveAt(now time.Time) bool {
	return q.RemovedAt == nil && q.ExpiresAt.After(now)
}
