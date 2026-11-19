package worker

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/stretchr/testify/require"
)

type secretGrantMockStore struct {
	MockStore
	grants          []models.SecretGrant
	expectedOrgID   string
	expectedProject string
	// projectDisabled makes the project of the job disabled.
	projectDisabled bool
}

func (s *secretGrantMockStore) GetProjectByID(_ context.Context, projectID string) (*models.Project, error) {
	return &models.Project{ProjectID: projectID, Enabled: !s.projectDisabled}, nil
}

func (s *secretGrantMockStore) ListSecretGrantsForJob(ctx context.Context, userID string, projectID *string, jobName string) ([]models.SecretGrant, error) {
	if s.expectedOrgID != "" && userID != s.expectedOrgID {
		return nil, nil
	}
	if s.expectedProject != "" && (projectID == nil || *projectID != s.expectedProject) {
		return nil, nil
	}
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
	require.NoError(t, AuthorizeSecretAccess(context.Background(), nil, job, "jobs/job-1", "token"))
	require.NoError(t, AuthorizeSecretAccess(context.Background(), nil, job, "projects/project-1/jobs/.reactorcide/jobs/deploy.yaml", "token"))
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
	grantStore := &secretGrantMockStore{
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
	}

	require.NoError(t, AuthorizeSecretAccess(context.Background(), grantStore, job, "deploy/production/db", "password"))
	require.Error(t, AuthorizeSecretAccess(context.Background(), grantStore, job, "deploy/staging/db", "password"))
}

func TestAuthorizeSecretAccess_SupportsGrantPatterns(t *testing.T) {
	projectID := "project-1"
	job := &models.Job{
		JobID:     "job-1",
		UserID:    "user-1",
		ProjectID: &projectID,
		Name:      "release-linux-amd64",
	}
	grantStore := &secretGrantMockStore{
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
	}

	require.NoError(t, AuthorizeSecretAccess(context.Background(), grantStore, job, "catalystcommunity/registry", "password"))

	job.Name = "test-linux-amd64"
	require.Error(t, AuthorizeSecretAccess(context.Background(), grantStore, job, "catalystcommunity/registry", "password"))
}

// secretProfileMockStore also serves execution profiles so the profile gate
// (DenySecrets, SecretPathAllowlist) applies before grant matching.
type secretProfileMockStore struct {
	secretGrantMockStore
	profiles map[string]*models.ExecutionProfile
}

func (s *secretProfileMockStore) GetExecutionProfile(_ context.Context, orgID, name string) (*models.ExecutionProfile, error) {
	profile, ok := s.profiles[name]
	if !ok || profile.OrgID != orgID {
		return nil, errProfileNotFound
	}
	copied := *profile
	return &copied, nil
}

var errProfileNotFound = &profileNotFoundError{}

type profileNotFoundError struct{}

func (*profileNotFoundError) Error() string { return "profile not found" }

// A trusted base node in a mixed-trust workflow receives only the secrets
// that its exact grant, profile, and CI origin allow. An untrusted node in
// the same workflow never receives a secret because its profile denies them,
// even when a broad grant exists.
func TestAuthorizeSecretAccess_MixedTrustNodeBoundary(t *testing.T) {
	projectID := "project-1"
	grantStore := &secretProfileMockStore{
		secretGrantMockStore: secretGrantMockStore{
			grants: []models.SecretGrant{{
				GrantID: "grant-1", Name: "asset-cache", UserID: "org-1", ProjectID: &projectID,
				SecretPathMatch: models.SecretGrantMatchExact, SecretPathPattern: "catalyst/asset-cache",
				JobNameMatch: models.SecretGrantMatchExact, JobNamePattern: "asset-prepare",
				ExecutionProfiles: []string{"standard"}, CIOrigins: []string{"base"},
			}},
		},
		profiles: map[string]*models.ExecutionProfile{
			"standard":     {OrgID: "org-1", Name: "standard"},
			"pr-untrusted": {OrgID: "org-1", Name: "pr-untrusted", DenySecrets: true},
		},
	}

	trusted := &models.Job{JobID: "job-1", OrgID: "org-1", UserID: "org-1", ProjectID: &projectID,
		Name: "asset-prepare", ExecutionProfile: "standard", CIOrigin: "base"}
	require.NoError(t, AuthorizeSecretAccess(context.Background(), grantStore, trusted, "catalyst/asset-cache", "secret_key"))
	// The exact grant does not cover other paths or other node names.
	require.Error(t, AuthorizeSecretAccess(context.Background(), grantStore, trusted, "catalyst/other", "secret_key"))
	seal := *trusted
	seal.Name = "asset-seal"
	require.Error(t, AuthorizeSecretAccess(context.Background(), grantStore, &seal, "catalyst/asset-cache", "secret_key"))

	// An untrusted head node is denied by its profile before any grant match.
	untrusted := &models.Job{JobID: "job-2", OrgID: "org-1", UserID: "org-1", ProjectID: &projectID,
		Name: "asset-prepare", ExecutionProfile: "pr-untrusted", CIOrigin: "head"}
	err := AuthorizeSecretAccess(context.Background(), grantStore, untrusted, "catalyst/asset-cache", "secret_key")
	require.ErrorContains(t, err, "denied by execution profile")
}

func TestAuthorizeSecretAccess_RequiresOrganizationProjectProfileAndProvenance(t *testing.T) {
	projectID := "project-1"
	job := &models.Job{JobID: "job-1", OrgID: "org-1", ProjectID: &projectID, Name: "deploy",
		ExecutionProfile: "standard", CIOrigin: "base"}
	grantStore := &secretGrantMockStore{expectedOrgID: "org-1", expectedProject: projectID,
		grants: []models.SecretGrant{{GrantID: "grant-1", Name: "deploy", SecretPathMatch: models.SecretGrantMatchPrefix,
			SecretPathPattern: "deploy/production", JobNameMatch: models.SecretGrantMatchExact, JobNamePattern: "deploy",
			ExecutionProfiles: []string{"standard"}, CIOrigins: []string{"base"}}}}

	require.NoError(t, AuthorizeSecretAccess(context.Background(), grantStore, job, "deploy/production/db", "password"))

	job.OrgID = "org-2"
	require.Error(t, AuthorizeSecretAccess(context.Background(), grantStore, job, "deploy/production/db", "password"))
	job.OrgID = "org-1"
	otherProject := "project-2"
	job.ProjectID = &otherProject
	require.Error(t, AuthorizeSecretAccess(context.Background(), grantStore, job, "deploy/production/db", "password"))
	job.ProjectID = &projectID
	job.ExecutionProfile = "pr-untrusted"
	require.Error(t, AuthorizeSecretAccess(context.Background(), grantStore, job, "deploy/production/db", "password"))
	job.ExecutionProfile = "standard"
	job.CIOrigin = "head"
	require.Error(t, AuthorizeSecretAccess(context.Background(), grantStore, job, "deploy/production/db", "password"))
}

// An org-wide grant does not reach a job of a disabled project (for example
// a project that a GitHub App registered and that waits for approval). A
// grant of the project itself still applies.
func TestAuthorizeSecretAccess_OrgWideGrantSkipsDisabledProject(t *testing.T) {
	projectID := "project-1"
	job := &models.Job{JobID: "job-1", OrgID: "org-1", ProjectID: &projectID, Name: "build"}
	orgWide := models.SecretGrant{GrantID: "grant-org", Name: "org-wide", SecretPathMatch: models.SecretGrantMatchPrefix,
		SecretPathPattern: "shared"}
	grantStore := &secretGrantMockStore{grants: []models.SecretGrant{orgWide}}
	require.NoError(t, AuthorizeSecretAccess(context.Background(), grantStore, job, "shared/npm", "token"))

	grantStore.projectDisabled = true
	require.Error(t, AuthorizeSecretAccess(context.Background(), grantStore, job, "shared/npm", "token"))

	projectGrant := orgWide
	projectGrant.GrantID, projectGrant.ProjectID = "grant-project", &projectID
	grantStore.grants = append(grantStore.grants, projectGrant)
	require.NoError(t, AuthorizeSecretAccess(context.Background(), grantStore, job, "shared/npm", "token"))
}
