package test

// Postgres-backed coverage for WP15 (migration 000061): job artifacts, test
// results, flaky test detection, test quarantine with its authorization, and
// the provenance signing keys (docs/artifacts-and-tests.md,
// docs/provenance.md).

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/artifacts"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/principals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/provenance"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/requiredset"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/testresults"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

func TestArtifactsTestsProvenanceMigration(t *testing.T) {
	db := newScratchDBAt(t, "wp15_migrate", 60)
	var orgID string
	require.NoError(t, db.QueryRow(`INSERT INTO organizations (name, display_name) VALUES ('wp15-org', 'WP15') RETURNING org_id`).Scan(&orgID))
	require.NoError(t, goose.UpTo(db, "migrations", 61))

	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_name IN
		('job_artifacts', 'test_results', 'job_test_summaries', 'test_flakiness', 'test_flaky_commits', 'test_quarantines', 'provenance_signing_keys')`).Scan(&count))
	require.Equal(t, 7, count)
	// No existing row changes; jobs keep working.
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM organizations WHERE org_id = $1`, orgID).Scan(&count))
	require.Equal(t, 1, count)

	require.NoError(t, goose.DownTo(db, "migrations", 60))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_name IN
		('job_artifacts', 'test_results', 'job_test_summaries', 'test_flakiness', 'test_flaky_commits', 'test_quarantines', 'provenance_signing_keys')`).Scan(&count))
	require.Zero(t, count)
}

func wp15Stores(t *testing.T) (store.ArtifactStore, store.TestResultStore, store.TestQuarantineStore) {
	t.Helper()
	a, ok := store.AppStore.(store.ArtifactStore)
	require.True(t, ok)
	r, ok := store.AppStore.(store.TestResultStore)
	require.True(t, ok)
	q, ok := store.AppStore.(store.TestQuarantineStore)
	require.True(t, ok)
	return a, r, q
}

func TestArtifactStorePostgres(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		artifactStore, _, _ := wp15Stores(t)
		owner := createTestUserIn(t, tx)
		job := createTestJobIn(t, tx, owner.UserID, "running")
		service := &artifacts.Service{Store: artifactStore, Objects: objects.NewMemoryObjectStore(), Limits: artifacts.DefaultLimits}

		first, err := service.AppendChunk(ctx, job, "main", artifacts.ChunkRequest{Path: "dist/app.bin", Data: []byte("hello ")})
		require.NoError(t, err)
		done, err := service.AppendChunk(ctx, job, "main", artifacts.ChunkRequest{Path: "dist/app.bin", Offset: 6, Data: []byte("world"), Final: true})
		require.NoError(t, err)
		require.Equal(t, first.ArtifactID, done.ArtifactID)
		require.Equal(t, "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9", done.SHA256)

		_, created, err := artifactStore.CreateJobArtifact(ctx, &models.JobArtifact{JobID: job.JobID, Name: "dist/app.bin", Path: "dist/app.bin", StoragePrefix: "x"})
		require.NoError(t, err)
		require.False(t, created, "the same job, kind, and path is one record")

		_, err = service.AppendChunk(ctx, job, "main", artifacts.ChunkRequest{Path: "partial", Data: []byte("x")})
		require.NoError(t, err)
		count, total, err := artifactStore.JobArtifactTotals(ctx, job.JobID)
		require.NoError(t, err)
		require.Equal(t, 2, count)
		require.EqualValues(t, 12, total)

		ok, err := artifactStore.AppendJobArtifactChunk(ctx, done.ArtifactID, 11, 20, nil, 0)
		require.NoError(t, err)
		require.False(t, ok, "a complete record takes no chunk")

		list, err := service.Finalize(ctx, job.JobID, nil)
		require.NoError(t, err)
		require.Len(t, list, 2)
		for _, a := range list {
			if a.Path == "partial" {
				require.Equal(t, models.ArtifactStatusFailed, a.Status)
			}
		}
		var key string
		require.NoError(t, tx.Raw(`SELECT artifacts_object_key FROM jobs WHERE job_id = ?`, job.JobID).Scan(&key).Error)
		require.Equal(t, "artifacts/main/"+job.JobID, key)

		_, err = service.PutComplete(ctx, job, "main", models.ArtifactKindProvenance, provenance.ArtifactPath, provenance.MediaType, []byte("{}"))
		require.NoError(t, err)
		count, _, _ = artifactStore.JobArtifactTotals(ctx, job.JobID)
		require.Equal(t, 2, count, "the provenance does not count toward the limits")

		require.NoError(t, artifactStore.DeleteJobArtifactRecords(ctx, job.JobID))
		list, _ = artifactStore.ListJobArtifacts(ctx, job.JobID)
		require.Empty(t, list)
	})
}

// The job byte limit is checked in the same step as the append: the sum of
// all artifacts of the job, with the new chunk, must stay in the limit.
func TestArtifactJobByteLimitPostgres(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		artifactStore, _, _ := wp15Stores(t)
		owner := createTestUserIn(t, tx)
		job := createTestJobIn(t, tx, owner.UserID, "running")
		a, _, err := artifactStore.CreateJobArtifact(ctx, &models.JobArtifact{JobID: job.JobID, Name: "a", Path: "a", StoragePrefix: "x"})
		require.NoError(t, err)
		b, _, err := artifactStore.CreateJobArtifact(ctx, &models.JobArtifact{JobID: job.JobID, Name: "b", Path: "b", StoragePrefix: "x"})
		require.NoError(t, err)

		ok, err := artifactStore.AppendJobArtifactChunk(ctx, a.ArtifactID, 0, 6, nil, 10)
		require.NoError(t, err)
		require.True(t, ok)
		_, err = artifactStore.AppendJobArtifactChunk(ctx, b.ArtifactID, 0, 5, nil, 10)
		require.ErrorIs(t, err, store.ErrArtifactJobLimit, "6 + 5 bytes pass the limit of 10")
		ok, err = artifactStore.AppendJobArtifactChunk(ctx, b.ArtifactID, 0, 4, nil, 10)
		require.NoError(t, err)
		require.True(t, ok, "6 + 4 bytes are in the limit")
		_, total, err := artifactStore.JobArtifactTotals(ctx, job.JobID)
		require.NoError(t, err)
		require.EqualValues(t, 10, total)
	})
}

func TestTestResultsAndFlakinessPostgres(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		_, results, _ := wp15Stores(t)
		owner := createTestUserIn(t, tx)
		project := createTestProjectIn(t, tx, owner.UserID, "https://example.invalid/"+uniqueName("repo"), false)
		run := func(sha, status string) (*models.Job, []string) {
			job := createTestJobInProjectIn(t, tx, owner.UserID, project.ProjectID, "failed")
			id := testresults.TestID(project.ProjectID, "unit", "pkg", "flaky")
			rows := []models.TestResult{
				{TestID: id, ProjectID: &project.ProjectID, Suite: "unit", Classname: "pkg", Name: "flaky", Status: status, CommitSHA: sha, Message: "m"},
				{TestID: testresults.TestID(project.ProjectID, "unit", "pkg", "stable"), ProjectID: &project.ProjectID, Suite: "unit", Classname: "pkg", Name: "stable", Status: "pass", CommitSHA: sha},
				{TestID: testresults.TestID(project.ProjectID, "unit", "pkg", "skip"), ProjectID: &project.ProjectID, Suite: "unit", Classname: "pkg", Name: "skip", Status: "skip", CommitSHA: sha},
			}
			require.NoError(t, results.ReplaceJobTestResults(ctx, job.JobID, rows, &models.JobTestSummary{Total: 3, Passed: 2, Failed: 1}))
			flaky, err := results.RecordTestRuns(ctx, project.ProjectID, sha, rows, 24*time.Hour)
			require.NoError(t, err)
			return job, flaky
		}
		_, flaky := run("sha-1", "pass")
		require.Empty(t, flaky, "one pass is not flaky")
		job2, flaky := run("sha-1", "fail")
		require.Len(t, flaky, 1, "a pass and a fail on one commit is flaky")
		_, flaky = run("sha-1", "fail")
		require.Len(t, flaky, 1)
		_, flaky = run("sha-2", "fail")
		require.Empty(t, flaky, "another commit is separate")

		id := testresults.TestID(project.ProjectID, "unit", "pkg", "flaky")
		rows, err := results.GetTestFlakiness(ctx, project.ProjectID, []string{id})
		require.NoError(t, err)
		require.Equal(t, 1, rows[id].FlakyCount, "one flaky commit counts one time, also on a retry")
		require.Equal(t, 4, rows[id].TotalRuns)
		require.Equal(t, "sha-1", rows[id].LastFlakySHA)
		stableID := testresults.TestID(project.ProjectID, "unit", "pkg", "stable")
		skipped, _ := results.GetTestFlakiness(ctx, project.ProjectID, []string{testresults.TestID(project.ProjectID, "unit", "pkg", "skip")})
		require.Empty(t, skipped, "a skipped test is not a run")
		stable, _ := results.GetTestFlakiness(ctx, project.ProjectID, []string{stableID})
		require.Zero(t, stable[stableID].FlakyCount)

		listed, err := results.ListProjectFlakyTests(ctx, project.ProjectID, time.Now().Add(-time.Hour), 10)
		require.NoError(t, err)
		require.Len(t, listed, 1)
		require.Equal(t, "flaky", listed[0].Name)

		ordered, err := results.ListJobTestResults(ctx, job2.JobID, nil, 0)
		require.NoError(t, err)
		require.Len(t, ordered, 3)
		require.Equal(t, "fail", ordered[0].Status, "failures first")
		failedOnly, _ := results.ListJobTestResults(ctx, job2.JobID, []string{"fail", "error"}, 0)
		require.Len(t, failedOnly, 1)
		summaries, err := results.ListJobTestSummaries(ctx, []string{job2.JobID, "not-a-uuid"})
		require.NoError(t, err)
		require.Equal(t, 1, summaries[job2.JobID].Failed)
		summary := summaries[job2.JobID]
		summary.Flaky = 1
		require.NoError(t, results.SaveJobTestSummary(ctx, &summary))
		got, err := results.GetJobTestSummary(ctx, job2.JobID)
		require.NoError(t, err)
		require.Equal(t, 1, got.Flaky)
		require.Equal(t, []string{}, got.ParseErrors)
	})
}

func TestTestQuarantinePostgres(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		_, _, quarantines := wp15Stores(t)
		org := newPrincipalOrg(t, ctx, uniqueName("wp15-q-org"))
		project := &models.Project{OrgID: org.OrgID, Name: "app", RepoURL: uniqueName("github.com/acme/app")}
		require.NoError(t, tx.Create(project).Error)
		ownerPrincipal := &models.Principal{Kind: models.PrincipalKindHuman, Name: uniqueName("owner")}
		require.NoError(t, tx.Create(ownerPrincipal).Error)

		var audits []string
		service := &testresults.QuarantineService{Store: store.AppStore, Record: func(_ context.Context, _, action, _, _ string, _ models.JSONB) {
			audits = append(audits, action)
		}}
		admin := requiredset.Caller{Identity: authz.Identity{Token: &checkauth.Principal{CredentialType: "instance_token",
			AllOrganizations: true, AllCapabilities: true}}}
		reader := requiredset.Caller{Identity: authz.Identity{Token: &checkauth.Principal{CredentialType: "service_token",
			OrganizationIDs: []string{org.OrgID}, Capabilities: tokencaps.Set{tokencaps.ProjectsRead: {}}}}}
		stranger := requiredset.Caller{Identity: authz.Identity{UserID: createTestUserIn(t, tx).UserID}, Actor: principals.Actor{PrincipalID: ownerPrincipal.PrincipalID}}

		req := testresults.QuarantineRequest{Suite: "unit", Classname: "pkg", Name: "flaky", Reason: "fails on arm", Until: time.Now().Add(48 * time.Hour)}
		_, err := service.Quarantine(ctx, admin, project, req)
		require.Equal(t, "invalid_argument", testresults.ErrorCode(err), "the admin token has no principal: it must name an owner")
		req.OwnerPrincipalID = ownerPrincipal.PrincipalID
		_, err = service.Quarantine(ctx, reader, project, req)
		require.Equal(t, "permission_denied", testresults.ErrorCode(err))
		_, err = service.Quarantine(ctx, stranger, project, req)
		require.Equal(t, "permission_denied", testresults.ErrorCode(err), "a user with no project role cannot quarantine")
		noExpiry := req
		noExpiry.Until = time.Time{}
		_, err = service.Quarantine(ctx, admin, project, noExpiry)
		require.Equal(t, "invalid_argument", testresults.ErrorCode(err), "an expiry is required")
		tooLong := req
		tooLong.Until = time.Now().Add(400 * 24 * time.Hour)
		_, err = service.Quarantine(ctx, admin, project, tooLong)
		require.Equal(t, "invalid_argument", testresults.ErrorCode(err))
		noReason := req
		noReason.Reason = " "
		_, err = service.Quarantine(ctx, admin, project, noReason)
		require.Equal(t, "invalid_argument", testresults.ErrorCode(err))

		first, err := service.Quarantine(ctx, admin, project, req)
		require.NoError(t, err)
		require.Equal(t, testresults.TestID(project.ProjectID, "unit", "pkg", "flaky"), first.TestID)
		second, err := service.Quarantine(ctx, admin, project, req)
		require.NoError(t, err, "a new quarantine of the same test replaces the old one")
		active, err := service.List(ctx, reader, project, false)
		require.NoError(t, err)
		require.Len(t, active, 1)
		require.Equal(t, second.QuarantineID, active[0].QuarantineID)
		all, _ := quarantines.ListTestQuarantines(ctx, project.ProjectID, true, time.Now())
		require.Len(t, all, 2)

		// An expired quarantine does not apply, with no action.
		later, _ := quarantines.ListTestQuarantines(ctx, project.ProjectID, false, time.Now().Add(72*time.Hour))
		require.Empty(t, later)

		require.Equal(t, "permission_denied", testresults.ErrorCode(service.Unquarantine(ctx, reader, project, second.QuarantineID, "")))
		require.NoError(t, service.Unquarantine(ctx, admin, project, second.QuarantineID, ""))
		require.Equal(t, "not_found", testresults.ErrorCode(service.Unquarantine(ctx, admin, project, second.QuarantineID, "")))
		active, _ = service.List(ctx, admin, project, false)
		require.Empty(t, active)
		require.Equal(t, []string{"test.quarantine", "test.quarantine", "test.unquarantine"}, audits)
	})
}

func TestProvenanceKeyStorePostgres(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		keys := testMasterKeys(t, tx)
		signer := provenance.NewSigner(store.AppStore, keys, "https://ci.example/api/v1/oidc")
		require.NotNil(t, signer)
		kid, _, err := signer.ActiveSigner(ctx)
		require.NoError(t, err)
		jwks, err := signer.JWKS(ctx)
		require.NoError(t, err)
		require.Len(t, jwks.Keys, 1)
		require.Equal(t, kid, jwks.Keys[0].KeyID)
		var oidcKeys int64
		require.NoError(t, tx.Table("oidc_signing_keys").Where("kid = ?", kid).Count(&oidcKeys).Error)
		require.Zero(t, oidcKeys, "a provenance key is never an OIDC key")
		rotated, err := signer.Rotate(ctx, true)
		require.NoError(t, err)
		require.NotEqual(t, kid, rotated.KID)
		jwks, _ = signer.JWKS(ctx)
		require.Len(t, jwks.Keys, 2, "the old key stays in the JWKS for an overlap")
		require.True(t, strings.HasPrefix(signer.IssuerValue(), "https://"))
	})
}
