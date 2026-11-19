package postgres_store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/ctxkey"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// WP15: job artifacts, test results, flakiness, test quarantine, and the
// provenance signing keys (migration 000061).

var (
	_ store.ArtifactStore       = PostgresDbStore{}
	_ store.TestResultStore     = PostgresDbStore{}
	_ store.TestQuarantineStore = PostgresDbStore{}
	_ store.ProvenanceKeyStore  = PostgresDbStore{}
)

// provenanceKeyLockID is the advisory lock key of provenance key changes.
const provenanceKeyLockID int64 = 0x5243_5052_4f56 // "RCPROV"

func (ps PostgresDbStore) CreateJobArtifact(ctx context.Context, artifact *models.JobArtifact) (*models.JobArtifact, bool, error) {
	if artifact == nil || !isValidUUID(artifact.JobID) || artifact.Path == "" {
		return nil, false, store.ErrInvalidInput
	}
	if artifact.Kind == "" {
		artifact.Kind = models.ArtifactKindFile
	}
	if artifact.Status == "" {
		artifact.Status = models.ArtifactStatusUploading
	}
	result := ps.getDB(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(artifact)
	if result.Error != nil {
		return nil, false, fmt.Errorf("failed to create job artifact: %w", result.Error)
	}
	if result.RowsAffected == 1 {
		return artifact, true, nil
	}
	var existing models.JobArtifact
	if err := ps.getDB(ctx).Where("job_id = ? AND kind = ? AND path = ?", artifact.JobID, artifact.Kind, artifact.Path).First(&existing).Error; err != nil {
		return nil, false, fmt.Errorf("failed to read job artifact: %w", err)
	}
	return &existing, false, nil
}

func (ps PostgresDbStore) GetJobArtifact(ctx context.Context, artifactID string) (*models.JobArtifact, error) {
	if !isValidUUID(artifactID) {
		return nil, store.ErrNotFound
	}
	var artifact models.JobArtifact
	if err := ps.getDB(ctx).Where("artifact_id = ?", artifactID).First(&artifact).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return &artifact, nil
}

func (ps PostgresDbStore) ListJobArtifacts(ctx context.Context, jobID string) ([]models.JobArtifact, error) {
	artifacts := []models.JobArtifact{}
	if !isValidUUID(jobID) {
		return artifacts, nil
	}
	if err := ps.getDB(ctx).Where("job_id = ?", jobID).Order("kind, path").Find(&artifacts).Error; err != nil {
		return nil, fmt.Errorf("failed to list job artifacts: %w", err)
	}
	return artifacts, nil
}

func (ps PostgresDbStore) JobArtifactTotals(ctx context.Context, jobID string) (int, int64, error) {
	if !isValidUUID(jobID) {
		return 0, 0, nil
	}
	var row struct {
		Count int
		Bytes int64
	}
	err := ps.getDB(ctx).Model(&models.JobArtifact{}).
		Select("count(*) AS count, coalesce(sum(size_bytes), 0) AS bytes").
		Where("job_id = ? AND kind <> ?", jobID, models.ArtifactKindProvenance).Scan(&row).Error
	if err != nil {
		return 0, 0, fmt.Errorf("failed to total job artifacts: %w", err)
	}
	return row.Count, row.Bytes, nil
}

func (ps PostgresDbStore) AppendJobArtifactChunk(ctx context.Context, artifactID string, expectedSize, newSize int64, hashState []byte, maxJobBytes int64) (bool, error) {
	if !isValidUUID(artifactID) {
		return false, nil
	}
	appended := false
	err := ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		if maxJobBytes > 0 {
			// Lock the job row: the uploads of one job take turns here, so
			// the sum below sees every committed chunk of the job (each
			// statement of a READ COMMITTED transaction reads a new
			// snapshot) and no other upload can add bytes before this one
			// commits.
			var jobIDs []string
			if err := tx.Raw(`SELECT j.job_id FROM jobs j JOIN job_artifacts a ON a.job_id = j.job_id
WHERE a.artifact_id = ? FOR UPDATE OF j`, artifactID).Scan(&jobIDs).Error; err != nil {
				return err
			}
			if len(jobIDs) == 0 {
				return nil
			}
			var total int64
			if err := tx.Model(&models.JobArtifact{}).Select("coalesce(sum(size_bytes), 0)").
				Where("job_id = ? AND kind <> ?", jobIDs[0], models.ArtifactKindProvenance).Scan(&total).Error; err != nil {
				return err
			}
			if total+(newSize-expectedSize) > maxJobBytes {
				return store.ErrArtifactJobLimit
			}
		}
		result := tx.Model(&models.JobArtifact{}).
			Where("artifact_id = ? AND status = ? AND size_bytes = ?", artifactID, models.ArtifactStatusUploading, expectedSize).
			Updates(map[string]interface{}{"size_bytes": newSize, "hash_state": hashState, "chunk_count": gorm.Expr("chunk_count + 1")})
		if result.Error != nil {
			return result.Error
		}
		appended = result.RowsAffected == 1
		return nil
	})
	if errors.Is(err, store.ErrArtifactJobLimit) {
		return false, err
	}
	if err != nil {
		return false, fmt.Errorf("failed to append job artifact chunk: %w", err)
	}
	return appended, nil
}

func (ps PostgresDbStore) CompleteJobArtifact(ctx context.Context, artifactID, sha256 string, size int64) error {
	now := time.Now().UTC()
	result := ps.getDB(ctx).Model(&models.JobArtifact{}).
		Where("artifact_id = ? AND status = ? AND size_bytes = ?", artifactID, models.ArtifactStatusUploading, size).
		Updates(map[string]interface{}{"status": models.ArtifactStatusComplete, "sha256": sha256, "hash_state": nil, "completed_at": now})
	if result.Error != nil {
		return fmt.Errorf("failed to complete job artifact: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrConflict
	}
	return nil
}

func (ps PostgresDbStore) FailJobArtifact(ctx context.Context, artifactID, reason string) error {
	return ps.getDB(ctx).Model(&models.JobArtifact{}).
		Where("artifact_id = ? AND status = ?", artifactID, models.ArtifactStatusUploading).
		Updates(map[string]interface{}{"status": models.ArtifactStatusFailed, "status_reason": truncateText(reason, 500), "hash_state": nil}).Error
}

func (ps PostgresDbStore) FailUploadingJobArtifacts(ctx context.Context, jobID, reason string) (int, error) {
	if !isValidUUID(jobID) {
		return 0, nil
	}
	result := ps.getDB(ctx).Model(&models.JobArtifact{}).
		Where("job_id = ? AND status = ?", jobID, models.ArtifactStatusUploading).
		Updates(map[string]interface{}{"status": models.ArtifactStatusFailed, "status_reason": truncateText(reason, 500), "hash_state": nil})
	return int(result.RowsAffected), result.Error
}

func (ps PostgresDbStore) SetJobArtifactKind(ctx context.Context, artifactID, kind, mediaType string) error {
	return ps.getDB(ctx).Model(&models.JobArtifact{}).Where("artifact_id = ?", artifactID).
		Updates(map[string]interface{}{"kind": kind, "media_type": mediaType}).Error
}

func (ps PostgresDbStore) SetJobArtifactsObjectKey(ctx context.Context, jobID, key string) error {
	return ps.getDB(ctx).Model(&models.Job{}).Where("job_id = ? AND coalesce(artifacts_object_key, '') = ''", jobID).
		UpdateColumn("artifacts_object_key", key).Error
}

func (ps PostgresDbStore) DeleteJobArtifactRecords(ctx context.Context, jobID string) error {
	if !isValidUUID(jobID) {
		return nil
	}
	return ps.getDB(ctx).Where("job_id = ?", jobID).Delete(&models.JobArtifact{}).Error
}

// --- test results ---

func (ps PostgresDbStore) ReplaceJobTestResults(ctx context.Context, jobID string, results []models.TestResult, summary *models.JobTestSummary) error {
	if !isValidUUID(jobID) {
		return store.ErrInvalidInput
	}
	return ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("job_id = ?", jobID).Delete(&models.TestResult{}).Error; err != nil {
			return err
		}
		if len(results) > 0 {
			rows := make([]models.TestResult, len(results))
			for i := range results {
				rows[i] = results[i]
				rows[i].ResultID = ""
				rows[i].JobID = jobID
			}
			if err := tx.CreateInBatches(rows, 500).Error; err != nil {
				return err
			}
		}
		if summary == nil {
			return tx.Where("job_id = ?", jobID).Delete(&models.JobTestSummary{}).Error
		}
		row := *summary
		row.JobID = jobID
		if row.ParseErrors == nil {
			row.ParseErrors = []string{}
		}
		row.CreatedAt = time.Now().UTC()
		return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error
	})
}

func (ps PostgresDbStore) SaveJobTestSummary(ctx context.Context, summary *models.JobTestSummary) error {
	if summary == nil || !isValidUUID(summary.JobID) {
		return store.ErrInvalidInput
	}
	row := *summary
	if row.ParseErrors == nil {
		row.ParseErrors = []string{}
	}
	row.CreatedAt = time.Now().UTC()
	return ps.getDB(ctx).Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error
}

func (ps PostgresDbStore) GetJobTestSummary(ctx context.Context, jobID string) (*models.JobTestSummary, error) {
	if !isValidUUID(jobID) {
		return nil, store.ErrNotFound
	}
	var summary models.JobTestSummary
	if err := ps.getDB(ctx).Where("job_id = ?", jobID).First(&summary).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return &summary, nil
}

func (ps PostgresDbStore) ListJobTestSummaries(ctx context.Context, jobIDs []string) (map[string]models.JobTestSummary, error) {
	out := map[string]models.JobTestSummary{}
	ids := validUUIDs(jobIDs)
	if len(ids) == 0 {
		return out, nil
	}
	var rows []models.JobTestSummary
	if err := ps.getDB(ctx).Where("job_id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to list job test summaries: %w", err)
	}
	for _, row := range rows {
		out[row.JobID] = row
	}
	return out, nil
}

func (ps PostgresDbStore) ListJobTestResults(ctx context.Context, jobID string, statuses []string, limit int) ([]models.TestResult, error) {
	results := []models.TestResult{}
	if !isValidUUID(jobID) {
		return results, nil
	}
	if limit <= 0 {
		limit = 1000
	}
	query := ps.getDB(ctx).Where("job_id = ?", jobID)
	if len(statuses) > 0 {
		query = query.Where("status IN ?", statuses)
	}
	err := query.Order(clause.Expr{SQL: "CASE status WHEN 'error' THEN 0 WHEN 'fail' THEN 1 WHEN 'skip' THEN 2 ELSE 3 END, suite, classname, name"}).
		Limit(limit).Find(&results).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list job test results: %w", err)
	}
	return results, nil
}

// RecordTestRuns updates test_flakiness for the results of one job and
// returns the tests that are flaky on the commit. A test is flaky on a
// commit when the project has a pass and a failure (fail or error) of the
// test on that commit since the window start. test_flaky_commits makes the
// count one for each commit, so a retry of a flaky commit does not count
// again.
func (ps PostgresDbStore) RecordTestRuns(ctx context.Context, projectID, commitSHA string, results []models.TestResult, window time.Duration) ([]string, error) {
	if !isValidUUID(projectID) || len(results) == 0 {
		return nil, nil
	}
	type testKey struct{ suite, classname, name string }
	runs := map[string]int{}
	names := map[string]testKey{}
	ids := []string{}
	for _, result := range results {
		if result.Status == models.TestStatusSkip {
			continue
		}
		if _, ok := runs[result.TestID]; !ok {
			ids = append(ids, result.TestID)
			names[result.TestID] = testKey{result.Suite, result.Classname, result.Name}
		}
		runs[result.TestID]++
	}
	if len(ids) == 0 {
		return nil, nil
	}
	now := time.Now().UTC()
	flaky := []string{}
	err := ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		for start := 0; start < len(ids); start += 500 {
			end := start + 500
			if end > len(ids) {
				end = len(ids)
			}
			rows := make([]models.TestFlakiness, 0, end-start)
			for _, id := range ids[start:end] {
				key := names[id]
				rows = append(rows, models.TestFlakiness{ProjectID: projectID, TestID: id, Suite: key.suite, Classname: key.classname,
					Name: key.name, TotalRuns: runs[id], LastSeenAt: now})
			}
			err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "project_id"}, {Name: "test_id"}},
				DoUpdates: clause.Assignments(map[string]interface{}{
					"total_runs":   gorm.Expr("test_flakiness.total_runs + excluded.total_runs"),
					"last_seen_at": gorm.Expr("excluded.last_seen_at"),
					"suite":        gorm.Expr("excluded.suite"),
					"classname":    gorm.Expr("excluded.classname"),
					"name":         gorm.Expr("excluded.name"),
				}),
			}).Create(&rows).Error
			if err != nil {
				return err
			}
		}
		if commitSHA == "" {
			return nil
		}
		var candidates []string
		err := tx.Raw(`SELECT test_id FROM test_results
			WHERE project_id = ? AND commit_sha = ? AND test_id = ANY(?) AND created_at >= ?
			GROUP BY test_id
			HAVING bool_or(status = 'pass') AND bool_or(status IN ('fail', 'error'))`,
			projectID, commitSHA, pq.StringArray(ids), now.Add(-window)).Scan(&candidates).Error
		if err != nil {
			return err
		}
		for _, id := range candidates {
			flaky = append(flaky, id)
			insert := tx.Exec(`INSERT INTO test_flaky_commits (project_id, test_id, commit_sha, detected_at) VALUES (?, ?, ?, ?)
				ON CONFLICT DO NOTHING`, projectID, id, commitSHA, now)
			if insert.Error != nil {
				return insert.Error
			}
			if insert.RowsAffected == 0 {
				continue
			}
			if err := tx.Model(&models.TestFlakiness{}).Where("project_id = ? AND test_id = ?", projectID, id).
				Updates(map[string]interface{}{"flaky_count": gorm.Expr("flaky_count + 1"), "last_flaky_at": now, "last_flaky_sha": commitSHA}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to record test runs: %w", err)
	}
	return flaky, nil
}

func (ps PostgresDbStore) ListProjectFlakyTests(ctx context.Context, projectID string, since time.Time, limit int) ([]models.TestFlakiness, error) {
	rows := []models.TestFlakiness{}
	if !isValidUUID(projectID) {
		return rows, nil
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	err := ps.getDB(ctx).Where("project_id = ? AND flaky_count > 0 AND last_flaky_at >= ?", projectID, since.UTC()).
		Order("last_flaky_at DESC").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list flaky tests: %w", err)
	}
	return rows, nil
}

func (ps PostgresDbStore) GetTestFlakiness(ctx context.Context, projectID string, testIDs []string) (map[string]models.TestFlakiness, error) {
	out := map[string]models.TestFlakiness{}
	if !isValidUUID(projectID) || len(testIDs) == 0 {
		return out, nil
	}
	var rows []models.TestFlakiness
	if err := ps.getDB(ctx).Where("project_id = ? AND test_id IN ?", projectID, testIDs).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to read test flakiness: %w", err)
	}
	for _, row := range rows {
		out[row.TestID] = row
	}
	return out, nil
}

// --- test quarantine ---

func (ps PostgresDbStore) CreateTestQuarantine(ctx context.Context, quarantine *models.TestQuarantine) error {
	if quarantine == nil || !isValidUUID(quarantine.ProjectID) || !isValidUUID(quarantine.OrgID) ||
		!isValidUUID(quarantine.OwnerPrincipalID) || quarantine.TestID == "" {
		return store.ErrInvalidInput
	}
	now := time.Now().UTC()
	return ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.TestQuarantine{}).
			Where("project_id = ? AND test_id = ? AND removed_at IS NULL", quarantine.ProjectID, quarantine.TestID).
			Updates(map[string]interface{}{"removed_at": now, "removed_by": "replaced"}).Error; err != nil {
			return err
		}
		quarantine.CreatedAt = now
		if err := tx.Create(quarantine).Error; err != nil {
			return fmt.Errorf("failed to create test quarantine: %w", err)
		}
		return nil
	})
}

func (ps PostgresDbStore) GetTestQuarantine(ctx context.Context, quarantineID string) (*models.TestQuarantine, error) {
	if !isValidUUID(quarantineID) {
		return nil, store.ErrNotFound
	}
	var row models.TestQuarantine
	if err := ps.getDB(ctx).Where("quarantine_id = ?", quarantineID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return &row, nil
}

func (ps PostgresDbStore) ListTestQuarantines(ctx context.Context, projectID string, includeEnded bool, now time.Time) ([]models.TestQuarantine, error) {
	rows := []models.TestQuarantine{}
	if !isValidUUID(projectID) {
		return rows, nil
	}
	query := ps.getDB(ctx).Where("project_id = ?", projectID)
	if !includeEnded {
		query = query.Where("removed_at IS NULL AND expires_at > ?", now.UTC())
	}
	if err := query.Order("created_at DESC").Limit(5000).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to list test quarantines: %w", err)
	}
	return rows, nil
}

func (ps PostgresDbStore) EndTestQuarantine(ctx context.Context, quarantineID, removedBy string, at time.Time) error {
	if !isValidUUID(quarantineID) {
		return store.ErrNotFound
	}
	result := ps.getDB(ctx).Model(&models.TestQuarantine{}).Where("quarantine_id = ? AND removed_at IS NULL", quarantineID).
		Updates(map[string]interface{}{"removed_at": at.UTC(), "removed_by": truncateText(removedBy, 200)})
	if result.Error != nil {
		return fmt.Errorf("failed to end test quarantine: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

// --- provenance signing keys ---

const provenanceKeysTable = "provenance_signing_keys"

func (ps PostgresDbStore) ListProvenanceSigningKeys(ctx context.Context) ([]models.OIDCSigningKey, error) {
	var keys []models.OIDCSigningKey
	if err := ps.getDB(ctx).Table(provenanceKeysTable).Order("activates_at DESC").Find(&keys).Error; err != nil {
		return nil, fmt.Errorf("failed to list provenance signing keys: %w", err)
	}
	return keys, nil
}

func (ps PostgresDbStore) CreateProvenanceSigningKey(ctx context.Context, key *models.OIDCSigningKey) error {
	if err := ps.getDB(ctx).Table(provenanceKeysTable).Create(key).Error; err != nil {
		return fmt.Errorf("failed to create provenance signing key: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) RetireProvenanceSigningKeys(ctx context.Context, keep string, retiresAt time.Time) error {
	return ps.getDB(ctx).Table(provenanceKeysTable).
		Where("kid <> ? AND (retires_at IS NULL OR retires_at > ?)", keep, retiresAt.UTC()).
		Update("retires_at", retiresAt.UTC()).Error
}

func (ps PostgresDbStore) SetProvenanceSigningKeyRetirement(ctx context.Context, kid string, retiresAt time.Time) error {
	result := ps.getDB(ctx).Table(provenanceKeysTable).Where("kid = ?", kid).Update("retires_at", retiresAt.UTC())
	if result.Error != nil {
		return fmt.Errorf("failed to retire provenance signing key: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (ps PostgresDbStore) WithProvenanceKeyLock(ctx context.Context, fn func(ctx context.Context) error) error {
	return ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", provenanceKeyLockID).Error; err != nil {
			return err
		}
		return fn(context.WithValue(ctx, ctxkey.TxKey(), tx))
	})
}

func validUUIDs(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if isValidUUID(value) {
			out = append(out, value)
		}
	}
	return out
}

func truncateText(value string, limit int) string {
	value = strings.ToValidUTF8(value, "")
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && (value[cut]&0xC0) == 0x80 {
		cut--
	}
	return value[:cut]
}
