package worker

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/stretchr/testify/require"
)

type secretGrantMockStore struct {
	MockStore
	grants []models.SecretGrant
}

func (s *secretGrantMockStore) ListSecretGrantsForJob(ctx context.Context, userID string, projectID *string, jobName string) ([]models.SecretGrant, error) {
	return s.grants, nil
}

func TestAuthorizeSecretAccess_AllowsJobScopedSecrets(t *testing.T) {
	projectID := "project-1"
	job := &models.Job{
		JobID:     "job-1",
		UserID:    "user-1",
		ProjectID: &projectID,
		Name:      "deploy",
		JobFile:   ".reactorcide/jobs/deploy.yaml",
	}
	jp := &JobProcessor{store: &MockStore{}}

	require.NoError(t, jp.authorizeSecretAccess(context.Background(), job, "jobs/job-1", "token"))
	require.NoError(t, jp.authorizeSecretAccess(context.Background(), job, "projects/project-1/jobs/.reactorcide/jobs/deploy.yaml", "token"))
}

func TestAuthorizeSecretAccess_RequiresMatchingGrantForSharedSecrets(t *testing.T) {
	projectID := "project-1"
	job := &models.Job{
		JobID:     "job-1",
		UserID:    "user-1",
		ProjectID: &projectID,
		Name:      "deploy",
		JobFile:   ".reactorcide/jobs/deploy.yaml",
	}
	jp := &JobProcessor{store: &secretGrantMockStore{
		grants: []models.SecretGrant{{
			GrantID:           "grant-1",
			Name:              "deploy-production",
			UserID:            "user-1",
			ProjectID:         &projectID,
			SecretPathMatch:   models.SecretGrantMatchPrefix,
			SecretPathPattern: "deploy/production",
			JobNameMatch:      models.SecretGrantMatchExact,
			JobNamePattern:    "deploy",
		}},
	}}

	require.NoError(t, jp.authorizeSecretAccess(context.Background(), job, "deploy/production/db", "password"))
	require.Error(t, jp.authorizeSecretAccess(context.Background(), job, "deploy/staging/db", "password"))
}

func TestAuthorizeSecretAccess_SupportsGrantPatterns(t *testing.T) {
	projectID := "project-1"
	job := &models.Job{
		JobID:     "job-1",
		UserID:    "user-1",
		ProjectID: &projectID,
		Name:      "release-linux-amd64",
	}
	jp := &JobProcessor{store: &secretGrantMockStore{
		grants: []models.SecretGrant{{
			GrantID:           "grant-1",
			Name:              "release-registry",
			UserID:            "user-1",
			ProjectID:         &projectID,
			SecretPathMatch:   models.SecretGrantMatchGlob,
			SecretPathPattern: "catalystcommunity/*",
			JobNameMatch:      models.SecretGrantMatchPrefix,
			JobNamePattern:    "release-",
		}},
	}}

	require.NoError(t, jp.authorizeSecretAccess(context.Background(), job, "catalystcommunity/registry", "password"))

	job.Name = "test-linux-amd64"
	require.Error(t, jp.authorizeSecretAccess(context.Background(), job, "catalystcommunity/registry", "password"))
}
