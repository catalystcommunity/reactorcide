package store

import (
	"context"
	"errors"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// ErrArtifactJobLimit: a chunk would make the artifacts of the job larger
// than the byte limit.
var ErrArtifactJobLimit = errors.New("the artifacts of the job are larger than the limit")

// ArtifactStore stores the artifact records of jobs (WP15,
// docs/artifacts-and-tests.md). The bytes are in the object store.
type ArtifactStore interface {
	// CreateJobArtifact inserts a record. When a record with the same job,
	// kind, and path exists, it returns that record and created=false.
	CreateJobArtifact(ctx context.Context, artifact *models.JobArtifact) (record *models.JobArtifact, created bool, err error)
	GetJobArtifact(ctx context.Context, artifactID string) (*models.JobArtifact, error)
	ListJobArtifacts(ctx context.Context, jobID string) ([]models.JobArtifact, error)
	// JobArtifactTotals returns the number of records and the sum of their
	// sizes for the job, without the provenance record.
	JobArtifactTotals(ctx context.Context, jobID string) (count int, bytes int64, err error)
	// AppendJobArtifactChunk records one more stored chunk. It changes the
	// record only while it is uploading and has expectedSize bytes, so two
	// replicas cannot both append. It returns false when the guard fails.
	//
	// maxJobBytes > 0 is the byte limit of all artifacts of the job (without
	// the provenance record). The check and the change are one atomic step:
	// two concurrent uploads of one job cannot both pass the limit. It
	// returns ErrArtifactJobLimit when the chunk would pass the limit.
	AppendJobArtifactChunk(ctx context.Context, artifactID string, expectedSize, newSize int64, hashState []byte, maxJobBytes int64) (bool, error)
	CompleteJobArtifact(ctx context.Context, artifactID, sha256 string, size int64) error
	FailJobArtifact(ctx context.Context, artifactID, reason string) error
	// FailUploadingJobArtifacts fails every record of the job that is not
	// complete. It returns the number of changed records.
	FailUploadingJobArtifacts(ctx context.Context, jobID, reason string) (int, error)
	SetJobArtifactKind(ctx context.Context, artifactID, kind, mediaType string) error
	// SetJobArtifactsObjectKey records the object store prefix of the job
	// artifacts on the job (jobs.artifacts_object_key).
	SetJobArtifactsObjectKey(ctx context.Context, jobID, key string) error
	// DeleteJobArtifactRecords removes the artifact records of the job (the
	// telemetry retention removes the bytes).
	DeleteJobArtifactRecords(ctx context.Context, jobID string) error
}

// TestResultStore stores the test results of jobs and the flakiness of
// tests (WP15).
type TestResultStore interface {
	// ReplaceJobTestResults replaces the results and the summary of a job.
	ReplaceJobTestResults(ctx context.Context, jobID string, results []models.TestResult, summary *models.JobTestSummary) error
	// SaveJobTestSummary replaces the summary of a job only.
	SaveJobTestSummary(ctx context.Context, summary *models.JobTestSummary) error
	GetJobTestSummary(ctx context.Context, jobID string) (*models.JobTestSummary, error)
	// ListJobTestSummaries returns the summary of each job ID that has one.
	ListJobTestSummaries(ctx context.Context, jobIDs []string) (map[string]models.JobTestSummary, error)
	// ListJobTestResults returns the results of a job, failures and errors
	// first. An empty statuses list returns all states. limit <= 0 means
	// the default of 1000.
	ListJobTestResults(ctx context.Context, jobID string, statuses []string, limit int) ([]models.TestResult, error)
	// RecordTestRuns adds the results of one job (already stored with
	// ReplaceJobTestResults) to the flakiness records of the project. It
	// returns the test IDs that are flaky on commitSHA: they passed and
	// failed on that commit since now-window.
	RecordTestRuns(ctx context.Context, projectID, commitSHA string, results []models.TestResult, window time.Duration) ([]string, error)
	ListProjectFlakyTests(ctx context.Context, projectID string, since time.Time, limit int) ([]models.TestFlakiness, error)
	GetTestFlakiness(ctx context.Context, projectID string, testIDs []string) (map[string]models.TestFlakiness, error)
}

// TestQuarantineStore stores test quarantines (WP15, plan item A4).
type TestQuarantineStore interface {
	// CreateTestQuarantine ends the current quarantine of the same test
	// (if any) and inserts the new one.
	CreateTestQuarantine(ctx context.Context, quarantine *models.TestQuarantine) error
	GetTestQuarantine(ctx context.Context, quarantineID string) (*models.TestQuarantine, error)
	// ListTestQuarantines lists the quarantines of a project, newest
	// first. Without includeEnded it lists only the quarantines that apply
	// at now.
	ListTestQuarantines(ctx context.Context, projectID string, includeEnded bool, now time.Time) ([]models.TestQuarantine, error)
	EndTestQuarantine(ctx context.Context, quarantineID, removedBy string, at time.Time) error
}

// ProvenanceKeyStore stores the provenance signing keys. It has the same
// shape as the OIDC key store, on a separate table.
type ProvenanceKeyStore interface {
	ListProvenanceSigningKeys(ctx context.Context) ([]models.OIDCSigningKey, error)
	CreateProvenanceSigningKey(ctx context.Context, key *models.OIDCSigningKey) error
	RetireProvenanceSigningKeys(ctx context.Context, keep string, retiresAt time.Time) error
	SetProvenanceSigningKeyRetirement(ctx context.Context, kid string, retiresAt time.Time) error
	WithProvenanceKeyLock(ctx context.Context, fn func(ctx context.Context) error) error
	MasterKeyIDByName(ctx context.Context, name string) (string, error)
	MasterKeyNameByID(ctx context.Context, keyID string) (string, error)
}
