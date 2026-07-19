package test

// UI-auth / RBAC / visibility / credential-rotation integration tests
// (UI_AUTH_PLAN.md Task J). These run against the real, testcontainers-backed
// Postgres from setup_test.go and the real coordinator router/store/CSIL
// dispatcher wiring (handlers.GetAppMux(), internal/uiapi, internal/auth,
// internal/authz) — no mocks or fakes. Unlike most tests in this package,
// most CSIL-RPC and webhook writes here are NOT wrapped in a rollback-able
// transaction (uiapi's dispatcher and webhook_handler.go's rotation lookups
// both intentionally bypass request-scoped transactions — see this file's
// per-test comments), so they commit for real against the shared test
// container; each test uses unique, randomized names so it never collides
// with another test's data, and nothing here needs manual cleanup since the
// whole container is discarded when the test binary exits.
//
// TestUIAuthMigrationRoundTrip is deliberately the first test declared in
// this file (Go runs a single file's tests top-to-bottom) because it drops
// and recreates the 000017/000018 tables in an *isolated* scratch database —
// isolated precisely so it can never race with this file's other tests,
// which commit real 'cancelling'-status jobs and other 000017/000018 rows
// against the *shared* test database that would otherwise violate the
// down-migration's constraints if the tables were shared.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/auth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/handlers"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
	"github.com/catalystcommunity/reactorcide/coredb"
)

// uniqueName returns a per-call-unique suffix for test fixtures (project
// repo URLs, group names, trusted-identity domains, ...) so these tests
// never collide with each other or with leftover data from a previous run
// against the same long-lived test container.
var uniqueCounter int64

func uniqueName(prefix string) string {
	uniqueCounter++
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), uniqueCounter)
}

// requireDataStore asserts store.AppStore implements uiapi.DataStore — the
// same type assertion handlers/router.go performs to decide whether to wire
// the real CSIL implementations or the unimplemented stubs. Every test in
// this file depends on the real implementations being wired, so failing
// fast here with a clear message beats a confusing "unimplemented"
// ServiceError deep in a later assertion.
func requireDataStore(t *testing.T) uiapi.DataStore {
	t.Helper()
	ds, ok := store.AppStore.(uiapi.DataStore)
	require.True(t, ok, "store.AppStore must implement uiapi.DataStore for the real CSIL UI service to be wired")
	return ds
}

// mintSessionForUser mints a real ui_sessions-backed session token for
// userID by calling internal/auth directly, bypassing the LinkKeys login
// flow entirely (no local-rp/rp identity provider is available in this test
// environment). This is legitimate for testing purposes only insofar as it
// exercises exactly the same Sessions.MintSession/ResolveSession machinery
// AuthService.CompleteLogin/BootstrapAdmin use — what's skipped is only the
// LinkKeys protocol handshake that would normally produce the VerifiedIdentity
// feeding into provisioning, not the session/authorization machinery itself.
func mintSessionForUser(t *testing.T, userID string) string {
	t.Helper()
	sessions := auth.NewSessions(requireDataStore(t))
	token, err := sessions.MintSession(context.Background(), userID)
	require.NoError(t, err)
	require.NotEmpty(t, token)
	return token
}

func createTestUser(t *testing.T) *models.User {
	t.Helper()
	du := &DataUtils{db: testDB}
	user, err := du.CreateUser(DataSetup{})
	require.NoError(t, err)
	return user
}

func createTestProject(t *testing.T, orgID string, repoURL string, isPrivate bool) *models.Project {
	t.Helper()
	project := &models.Project{
		UserID:            &orgID,
		Name:              uniqueName("project"),
		RepoURL:           repoURL,
		Enabled:           true,
		IsPrivate:         isPrivate,
		TargetBranches:    pq.StringArray{"main"},
		AllowedEventTypes: pq.StringArray{"push"},
	}
	require.NoError(t, testDB.Create(project).Error)
	return project
}

func createTestJob(t *testing.T, userID, status string) *models.Job {
	t.Helper()
	du := &DataUtils{db: testDB}
	job, err := du.CreateJob(DataSetup{
		"UserID":     userID,
		"Status":     status,
		"JobCommand": "true",
	})
	require.NoError(t, err)
	return job
}

// ---------------------------------------------------------------------
// (a) Migration round trip: 000017/000018 apply and roll back cleanly,
// in an isolated scratch database so it can never race with this file's
// other tests committing real rows (including 'cancelling'-status jobs)
// against the shared test database.
// ---------------------------------------------------------------------

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var exists bool
	err := db.QueryRow(
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1)`,
		name,
	).Scan(&exists)
	require.NoError(t, err)
	return exists
}

func TestUIAuthMigrationRoundTrip(t *testing.T) {
	baseURI := os.Getenv("TEST_DB_URI")
	require.NotEmpty(t, baseURI, "TEST_DB_URI must be set by TestMain")

	u, err := url.Parse(baseURI)
	require.NoError(t, err)

	dbName := fmt.Sprintf("uiauth_migrate_%d", time.Now().UnixNano())

	adminDB, err := sql.Open("postgres", baseURI)
	require.NoError(t, err)
	defer adminDB.Close()

	_, err = adminDB.Exec(fmt.Sprintf(`CREATE DATABASE %q`, dbName))
	require.NoError(t, err, "creating isolated scratch database for migration round trip")
	defer func() {
		_, _ = adminDB.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS %q`, dbName))
	}()

	scratchURL := *u
	scratchURL.Path = "/" + dbName
	scratchDB, err := sql.Open("postgres", scratchURL.String())
	require.NoError(t, err)
	defer scratchDB.Close()

	goose.SetBaseFS(coredb.Migrations)
	require.NoError(t, goose.SetDialect("postgres"))

	// Up to latest: 000017_ui_auth_rbac.sql and 000018_credential_rotation.sql
	// must apply cleanly from a schema that already has everything through
	// 000016 available via goose's normal ordered-migration application.
	require.NoError(t, goose.Up(scratchDB, "migrations"), "goose Up to latest must apply 000017/000018 cleanly")
	for _, table := range []string{
		"global_settings", "groups", "group_members", "role_assignments",
		"ui_sessions", "auth_identities", "auth_credentials",
		"auth_trusted_identities", "auth_trusted_domain_patterns", "auth_login_attempts",
		"project_webhook_secrets", "project_vcs_credentials",
	} {
		assert.True(t, tableExists(t, scratchDB, table), "table %q should exist after Up", table)
	}

	// A job with status='cancelling' must be accepted by the post-000017
	// jobs_status_check constraint.
	var userID string
	require.NoError(t, scratchDB.QueryRow(
		`INSERT INTO users (username, email, password, salt) VALUES ('migtest', 'migtest@example.com', '\x00', '\x00') RETURNING user_id`,
	).Scan(&userID))
	_, err = scratchDB.Exec(
		`INSERT INTO jobs (user_id, name, job_command, status) VALUES ($1, 'migtest-job', 'true', 'cancelling')`,
		userID,
	)
	assert.NoError(t, err, "'cancelling' must be a valid jobs.status per the 000017 CHECK constraint")

	// The 000017 Down migration restores the pre-'cancelling' CHECK
	// constraint, which the row inserted above would now violate — resolve
	// it to a terminal status first, exactly as a real graceful-cancel flow
	// eventually would, so the round trip below exercises a clean rollback
	// rather than a data-migration concern that's out of scope here.
	_, err = scratchDB.Exec(`UPDATE jobs SET status = 'cancelled' WHERE user_id = $1`, userID)
	require.NoError(t, err)

	// DownTo 16: 000018 then 000017 roll back cleanly.
	require.NoError(t, goose.DownTo(scratchDB, "migrations", 16), "goose DownTo 16 must roll back 000018/000017 cleanly")
	for _, table := range []string{
		"role_assignments", "ui_sessions", "project_webhook_secrets", "project_vcs_credentials",
	} {
		assert.False(t, tableExists(t, scratchDB, table), "table %q should not exist after DownTo 16", table)
	}

	// Up again: must re-apply cleanly (proves the Down migrations left no
	// residue that would break a subsequent Up, e.g. a dangling enum value
	// or leftover index).
	require.NoError(t, goose.Up(scratchDB, "migrations"), "goose Up must re-apply 000017/000018 cleanly after DownTo 16")
	for _, table := range []string{"role_assignments", "project_webhook_secrets"} {
		assert.True(t, tableExists(t, scratchDB, table), "table %q should exist again after re-Up", table)
	}
}

// ---------------------------------------------------------------------
// (b) Mode "none": CSIL auth config, anonymous cancel-job success, kill-job
// and set-secret forbidden, and the 'cancelling' transition on a running job
// (the CHECK-constraint fix this task's migration exercises).
// ---------------------------------------------------------------------

func TestUIAuthModeNonePermissions(t *testing.T) {
	require.Equal(t, config.UIAuthModeNone, config.UIAuthMode, "this test assumes the default REACTORCIDE_UI_AUTH_MODE=none")

	handlers.ResetAppMux()
	defer handlers.ResetAppMux()
	mux := handlers.GetAppMux()

	user := createTestUser(t)
	submittedJob := createTestJob(t, user.UserID, "submitted")
	runningJob := createTestJob(t, user.UserID, "running")
	anotherJob := createTestJob(t, user.UserID, "submitted")

	t.Run("get-auth-config reports mode none", func(t *testing.T) {
		resp, svcErr := csilCall(t, mux, "ReactorcideAuth", "get-auth-config",
			csilapi.EncodeGetAuthConfigRequest, csilapi.DecodeGetAuthConfigResponse,
			csilapi.GetAuthConfigRequest{}, "")
		require.Nil(t, svcErr)
		assert.Equal(t, "none", resp.AuthMode)
	})

	t.Run("begin-login reports login_disabled", func(t *testing.T) {
		_, svcErr := csilCall(t, mux, "ReactorcideAuth", "begin-login",
			csilapi.EncodeBeginLoginRequest, csilapi.DecodeBeginLoginResponse,
			csilapi.BeginLoginRequest{}, "")
		require.NotNil(t, svcErr)
		assert.Equal(t, "login_disabled", svcErr.Code)
	})

	t.Run("anonymous cancel-job succeeds on a submitted job", func(t *testing.T) {
		resp, svcErr := csilCall(t, mux, "ReactorcideUi", "cancel-job",
			csilapi.EncodeCancelJobRequest, csilapi.DecodeCancelJobResponse,
			csilapi.CancelJobRequest{JobId: submittedJob.JobID}, "")
		require.Nil(t, svcErr)
		assert.Equal(t, "cancelled", resp.Status)

		job, err := store.AppStore.GetJobByID(context.Background(), submittedJob.JobID)
		require.NoError(t, err)
		assert.Equal(t, "cancelled", job.Status)
	})

	t.Run("anonymous cancel-job on a running job transitions to cancelling without a CHECK violation", func(t *testing.T) {
		resp, svcErr := csilCall(t, mux, "ReactorcideUi", "cancel-job",
			csilapi.EncodeCancelJobRequest, csilapi.DecodeCancelJobResponse,
			csilapi.CancelJobRequest{JobId: runningJob.JobID}, "")
		require.Nil(t, svcErr, "cancelling a running job must not fail (this is the fixed jobs_status_check constraint)")
		assert.Equal(t, "cancelling", resp.Status)

		job, err := store.AppStore.GetJobByID(context.Background(), runningJob.JobID)
		require.NoError(t, err)
		assert.Equal(t, "cancelling", job.Status)
	})

	t.Run("anonymous kill-job is forbidden", func(t *testing.T) {
		_, svcErr := csilCall(t, mux, "ReactorcideUi", "kill-job",
			csilapi.EncodeKillJobRequest, csilapi.DecodeKillJobResponse,
			csilapi.KillJobRequest{JobId: anotherJob.JobID}, "")
		require.NotNil(t, svcErr, "anonymous kill-job must be denied")
		assert.Equal(t, "forbidden", svcErr.Code)

		job, err := store.AppStore.GetJobByID(context.Background(), anotherJob.JobID)
		require.NoError(t, err)
		assert.Equal(t, "submitted", job.Status, "a denied kill-job must not mutate the job")
	})

	t.Run("anonymous retry-job succeeds on a failed job and yields a new job row", func(t *testing.T) {
		failedJob := createTestJob(t, user.UserID, "failed")

		resp, svcErr := csilCall(t, mux, "ReactorcideUi", "retry-job",
			csilapi.EncodeRetryJobRequest, csilapi.DecodeRetryJobResponse,
			csilapi.RetryJobRequest{JobId: failedJob.JobID}, "")
		require.Nil(t, svcErr, "anonymous retry-job must be allowed in mode none, same tier as cancel")
		require.NotEmpty(t, resp.JobId)
		assert.NotEqual(t, failedJob.JobID, resp.JobId, "retry must create a distinct new job row")

		newJob, err := store.AppStore.GetJobByID(context.Background(), resp.JobId)
		require.NoError(t, err)
		require.NotNil(t, newJob.ParentJobID)
		assert.Equal(t, failedJob.JobID, *newJob.ParentJobID)
		assert.Equal(t, resp.Status, newJob.Status)

		// The original job is left untouched (retry is additive, not a
		// mutation of the failed job it retries).
		original, err := store.AppStore.GetJobByID(context.Background(), failedJob.JobID)
		require.NoError(t, err)
		assert.Equal(t, "failed", original.Status)
	})

	t.Run("anonymous set-secret is denied", func(t *testing.T) {
		_, svcErr := csilCall(t, mux, "ReactorcideUi", "set-secret",
			csilapi.EncodeSetSecretRequest, csilapi.DecodeSetSecretResponse,
			csilapi.SetSecretRequest{OrgId: user.UserID, Path: "some/path", Key: "k", Value: "v"}, "")
		require.NotNil(t, svcErr, "anonymous set-secret must be denied")
		// Deps.requireUser rejects an anonymous (no-session) caller with
		// "unauthorized" before the capability check ever runs (see
		// uiapi/ui_secrets.go's SetSecret / deps.go's requireUser) — every
		// other write op in this service behaves the same way. UI_AUTH_PLAN's
		// permission-matrix table labels this row "no" without distinguishing
		// unauthorized from forbidden; this assertion pins the actual,
		// intentional code so a future regression to "forbidden" (or to
		// silently succeeding) is caught either way.
		assert.Equal(t, "unauthorized", svcErr.Code)
	})
}

// ---------------------------------------------------------------------
// (c) Bootstrap flow + a compact permission-matrix spot check over the real
// DB and real authz.Resolver (not the uiapi unit fakes).
// ---------------------------------------------------------------------

func TestUIAuthBootstrapAndPermissionMatrix(t *testing.T) {
	oldToken := config.BootstrapAdminToken
	config.BootstrapAdminToken = uniqueName("bootstrap-token")
	handlers.ResetAppMux()
	defer func() {
		config.BootstrapAdminToken = oldToken
		handlers.ResetAppMux()
	}()
	mux := handlers.GetAppMux()

	// 1. Wrong token never mints a session ("unauthorized", not an oracle
	// distinguishing "wrong token" from "feature disabled" — see
	// LoginService.BootstrapAdminSession's doc comment).
	_, svcErr := csilCall(t, mux, "ReactorcideAuth", "bootstrap-admin",
		csilapi.EncodeBootstrapAdminRequest, csilapi.DecodeBootstrapAdminResponse,
		csilapi.BootstrapAdminRequest{BootstrapToken: "definitely-wrong"}, "")
	require.NotNil(t, svcErr)
	assert.Equal(t, "unauthorized", svcErr.Code)

	// 2. Correct token mints a session.
	bootstrapResp, svcErr := csilCall(t, mux, "ReactorcideAuth", "bootstrap-admin",
		csilapi.EncodeBootstrapAdminRequest, csilapi.DecodeBootstrapAdminResponse,
		csilapi.BootstrapAdminRequest{BootstrapToken: config.BootstrapAdminToken}, "")
	require.Nil(t, svcErr)
	require.NotEmpty(t, bootstrapResp.SessionToken)
	adminToken := bootstrapResp.SessionToken

	authResp, svcErr := csilCall(t, mux, "ReactorcideAuth", "authenticate",
		csilapi.EncodeAuthenticateRequest, csilapi.DecodeAuthenticateResponse,
		csilapi.AuthenticateRequest{}, adminToken)
	require.Nil(t, svcErr)
	require.True(t, authResp.Authenticated)
	require.NotNil(t, authResp.Identity)
	require.True(t, authResp.Identity.IsGlobalAdmin, "bootstrap-admin session must resolve as global admin")
	adminOrgID := authResp.Identity.UserId

	// 3. The bootstrap-admin session can create a project; global admin
	// implies org admin of any org, and new_projects_private defaults false.
	repoURL := "github.com/uiauth-test/" + uniqueName("bootstrap-project")
	createProjResp, svcErr := csilCall(t, mux, "ReactorcideUi", "create-project",
		csilapi.EncodeCreateProjectRequest, csilapi.DecodeCreateProjectResponse,
		csilapi.CreateProjectRequest{OrgId: adminOrgID, Name: uniqueName("bootstrap-proj"), RepoUrl: repoURL}, adminToken)
	require.Nil(t, svcErr)
	assert.False(t, createProjResp.Project.IsPrivate, "new_projects_private defaults false")

	// 4. Set a global setting.
	newTrue := true
	settingsResp, svcErr := csilCall(t, mux, "ReactorcideUi", "update-global-settings",
		csilapi.EncodeUpdateGlobalSettingsRequest, csilapi.DecodeUpdateGlobalSettingsResponse,
		csilapi.UpdateGlobalSettingsRequest{NewProjectsPrivate: &newTrue}, adminToken)
	require.Nil(t, svcErr)
	assert.True(t, settingsResp.NewProjectsPrivate)
	// Restore it so this test's side effect doesn't change defaults for any
	// other test in this file that runs afterward.
	newFalse := false
	_, svcErr = csilCall(t, mux, "ReactorcideUi", "update-global-settings",
		csilapi.EncodeUpdateGlobalSettingsRequest, csilapi.DecodeUpdateGlobalSettingsResponse,
		csilapi.UpdateGlobalSettingsRequest{NewProjectsPrivate: &newFalse}, adminToken)
	require.Nil(t, svcErr)

	// 5. Add a trusted identity + a trusted domain pattern.
	identDomain := uniqueName("trusted") + ".example.com"
	_, svcErr = csilCall(t, mux, "ReactorcideUi", "add-trusted-identity",
		csilapi.EncodeAddTrustedIdentityRequest, csilapi.DecodeAddTrustedIdentityResponse,
		csilapi.AddTrustedIdentityRequest{Domain: identDomain}, adminToken)
	require.Nil(t, svcErr)

	pattern := "^.*\\." + uniqueName("regex") + "\\.example\\.org$"
	_, svcErr = csilCall(t, mux, "ReactorcideUi", "add-trusted-domain-pattern",
		csilapi.EncodeAddTrustedDomainPatternRequest, csilapi.DecodeAddTrustedDomainPatternResponse,
		csilapi.AddTrustedDomainPatternRequest{Pattern: pattern}, adminToken)
	require.Nil(t, svcErr)

	// 6. Create a group.
	groupResp, svcErr := csilCall(t, mux, "ReactorcideUi", "create-group",
		csilapi.EncodeCreateGroupRequest, csilapi.DecodeCreateGroupResponse,
		csilapi.CreateGroupRequest{OrgId: adminOrgID, Name: uniqueName("group")}, adminToken)
	require.Nil(t, svcErr)
	require.NotEmpty(t, groupResp.Group.GroupId)

	// 7. Assign org-admin role (scoped to the bootstrap admin's own org) to
	// another, unrelated user.
	otherUser := createTestUser(t)
	assignResp, svcErr := csilCall(t, mux, "ReactorcideUi", "assign-role",
		csilapi.EncodeAssignRoleRequest, csilapi.DecodeAssignRoleResponse,
		csilapi.AssignRoleRequest{
			PrincipalType: models.PrincipalTypeUser,
			PrincipalId:   otherUser.UserID,
			ScopeType:     models.ScopeTypeOrg,
			ScopeId:       &adminOrgID,
			Role:          models.RoleAdmin,
		}, adminToken)
	require.Nil(t, svcErr)
	require.NotEmpty(t, assignResp.Assignment.AssignmentId)

	// 8. The newly granted role is functionally effective: a session for
	// otherUser (minted directly, bypassing login — see mintSessionForUser)
	// can now also create a project in adminOrgID.
	otherToken := mintSessionForUser(t, otherUser.UserID)
	repoURL2 := "github.com/uiauth-test/" + uniqueName("granted-role-project")
	_, svcErr = csilCall(t, mux, "ReactorcideUi", "create-project",
		csilapi.EncodeCreateProjectRequest, csilapi.DecodeCreateProjectResponse,
		csilapi.CreateProjectRequest{OrgId: adminOrgID, Name: uniqueName("granted-proj"), RepoUrl: repoURL2}, otherToken)
	require.Nil(t, svcErr, "the role granted in step 7 must actually confer org-admin capability")

	// 9. Spot check: the same operations are denied to a session-less
	// (anonymous) caller.
	_, svcErr = csilCall(t, mux, "ReactorcideUi", "create-project",
		csilapi.EncodeCreateProjectRequest, csilapi.DecodeCreateProjectResponse,
		csilapi.CreateProjectRequest{OrgId: adminOrgID, Name: uniqueName("anon-proj"), RepoUrl: "github.com/uiauth-test/" + uniqueName("anon")}, "")
	require.NotNil(t, svcErr)
	assert.Equal(t, "unauthorized", svcErr.Code)

	_, svcErr = csilCall(t, mux, "ReactorcideUi", "update-global-settings",
		csilapi.EncodeUpdateGlobalSettingsRequest, csilapi.DecodeUpdateGlobalSettingsResponse,
		csilapi.UpdateGlobalSettingsRequest{NewProjectsPrivate: &newTrue}, "")
	require.NotNil(t, svcErr)
	assert.Equal(t, "unauthorized", svcErr.Code)

	_, svcErr = csilCall(t, mux, "ReactorcideUi", "add-trusted-identity",
		csilapi.EncodeAddTrustedIdentityRequest, csilapi.DecodeAddTrustedIdentityResponse,
		csilapi.AddTrustedIdentityRequest{Domain: "anon-attempt.example.com"}, "")
	require.NotNil(t, svcErr)
	assert.Equal(t, "unauthorized", svcErr.Code)
}

// ---------------------------------------------------------------------
// (d) Visibility: public project visible to a second authenticated user via
// list-projects; private project invisible (not_found on get).
// ---------------------------------------------------------------------

func TestUIAuthProjectVisibility(t *testing.T) {
	handlers.ResetAppMux()
	defer handlers.ResetAppMux()
	mux := handlers.GetAppMux()

	org := createTestUser(t)
	publicProject := createTestProject(t, org.UserID, "github.com/uiauth-test/"+uniqueName("public"), false)
	privateProject := createTestProject(t, org.UserID, "github.com/uiauth-test/"+uniqueName("private"), true)

	viewer := createTestUser(t)
	viewerToken := mintSessionForUser(t, viewer.UserID)

	listResp, svcErr := csilCall(t, mux, "ReactorcideUi", "list-projects",
		csilapi.EncodeListProjectsRequest, csilapi.DecodeListProjectsResponse,
		csilapi.ListProjectsRequest{OrgId: &org.UserID}, viewerToken)
	require.Nil(t, svcErr)

	var sawPublic, sawPrivate bool
	for _, p := range listResp.Projects {
		if p.ProjectId == publicProject.ProjectID {
			sawPublic = true
		}
		if p.ProjectId == privateProject.ProjectID {
			sawPrivate = true
		}
	}
	assert.True(t, sawPublic, "an unrelated authenticated user must see the public project")
	assert.False(t, sawPrivate, "an unrelated authenticated user must not see the private project")

	_, svcErr = csilCall(t, mux, "ReactorcideUi", "get-project",
		csilapi.EncodeGetProjectRequest, csilapi.DecodeGetProjectResponse,
		csilapi.GetProjectRequest{ProjectId: privateProject.ProjectID}, viewerToken)
	require.NotNil(t, svcErr, "an invisible private project must not be gettable")
	assert.Equal(t, "not_found", svcErr.Code, "private-but-existing must report not_found, not forbidden")

	getResp, svcErr := csilCall(t, mux, "ReactorcideUi", "get-project",
		csilapi.EncodeGetProjectRequest, csilapi.DecodeGetProjectResponse,
		csilapi.GetProjectRequest{ProjectId: publicProject.ProjectID}, viewerToken)
	require.Nil(t, svcErr)
	assert.Equal(t, publicProject.ProjectID, getResp.Project.ProjectId)
}

// ---------------------------------------------------------------------
// (e) Webhook secret rotation: two active secrets, deliver a signed webhook
// with the OLDER one, assert 200 and that only the matching row's
// last_used_at got stamped.
// ---------------------------------------------------------------------

func TestUIAuthWebhookSecretRotation(t *testing.T) {
	oldVCSEnabled := config.VCSEnabled
	oldDefaultUserID := config.DefaultUserID
	org := createTestUser(t)
	config.VCSEnabled = true
	config.DefaultUserID = org.UserID
	handlers.ResetAppMux()
	defer func() {
		config.VCSEnabled = oldVCSEnabled
		config.DefaultUserID = oldDefaultUserID
		handlers.ResetAppMux()
	}()
	mux := handlers.GetAppMux()

	// Secrets require an org encryption key before any Set/AddWebhookSecret
	// call can succeed (secrets.MasterKeyManager.GetOrgEncryptionKey returns
	// ErrNotInitialized otherwise — the same one-time-per-org step the
	// REST POST /api/v1/secrets/init endpoint performs). LoadOrCreateMasterKeys
	// is idempotent (env -> DB -> auto-generate), so this reuses whatever
	// master keys the router's own startup already established.
	keyManager, err := secrets.LoadOrCreateMasterKeys(store.GetDB())
	require.NoError(t, err)
	require.NoError(t, keyManager.InitializeOrgSecrets(store.GetDB(), org.UserID))

	repoName := uniqueName("rotation-repo")
	repoURL := "github.com/test-org/" + repoName
	project := createTestProject(t, org.UserID, repoURL, false)

	orgToken := mintSessionForUser(t, org.UserID) // org.UserID == config.DefaultUserID: self-org admin capability

	const oldSecretValue = "old-webhook-secret-fake-value"
	const newSecretValue = "new-webhook-secret-fake-value"

	oldSecretResp, svcErr := csilCall(t, mux, "ReactorcideUi", "add-webhook-secret",
		csilapi.EncodeAddWebhookSecretRequest, csilapi.DecodeAddWebhookSecretResponse,
		csilapi.AddWebhookSecretRequest{ProjectId: project.ProjectID, Provider: "github", Name: "old", Value: oldSecretValue}, orgToken)
	require.Nil(t, svcErr, "add-webhook-secret (old) failed: %+v", svcErr)

	// Ensure the two rows have distinct created_at ordering regardless of
	// the store's sort granularity.
	time.Sleep(20 * time.Millisecond)

	newSecretResp, svcErr := csilCall(t, mux, "ReactorcideUi", "add-webhook-secret",
		csilapi.EncodeAddWebhookSecretRequest, csilapi.DecodeAddWebhookSecretResponse,
		csilapi.AddWebhookSecretRequest{ProjectId: project.ProjectID, Provider: "github", Name: "new", Value: newSecretValue}, orgToken)
	require.Nil(t, svcErr, "add-webhook-secret (new) failed: %+v", svcErr)

	payload := map[string]any{
		"ref":     "refs/heads/main",
		"before":  "0000000000000000000000000000000000000000",
		"after":   "1111111111111111111111111111111111111111",
		"commits": []any{},
		"pusher":  map[string]any{"name": "tester", "email": "tester@example.com"},
		"repository": map[string]any{
			"full_name":      "test-org/" + repoName,
			"clone_url":      "https://" + repoURL + ".git",
			"html_url":       "https://" + repoURL,
			"default_branch": "main",
		},
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	mac := hmac.New(sha256.New, []byte(oldSecretValue))
	mac.Write(body)
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// "issues" is deliberately not "push"/"pull_request"/"ping": signature
	// verification (what this test checks) happens before event-type
	// dispatch, so this still exercises it fully, but GenericEventFromGitHub
	// maps it to EventUnknown, which short-circuits before job creation and
	// the outbound commit-status update — keeping this test hermetic (no
	// real network call to the GitHub API).
	req.Header.Set("X-GitHub-Event", "issues")
	req.Header.Set("X-Hub-Signature-256", signature)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "webhook signed with the older active secret must be accepted: %s", rr.Body.String())

	// webhook_handler.go's rotation lookups/touches use context.Background()
	// explicitly (bypassing any per-request transaction), so these are
	// already-committed reads.
	oldRow, err := requireDataStore(t).GetProjectWebhookSecretByID(context.Background(), oldSecretResp.Secret.Id)
	require.NoError(t, err)
	newRow, err := requireDataStore(t).GetProjectWebhookSecretByID(context.Background(), newSecretResp.Secret.Id)
	require.NoError(t, err)

	assert.NotNil(t, oldRow.LastUsedAt, "the matching (older) secret's last_used_at must be stamped")
	assert.Nil(t, newRow.LastUsedAt, "the non-matching (newer) secret's last_used_at must remain unstamped")
}
