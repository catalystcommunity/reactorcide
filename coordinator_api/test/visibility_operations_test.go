package test

// Postgres-backed integration coverage for
// postgres_store/visibility_operations.go's ListJobsVisibleTo (Finding 2:
// "pagination before visibility filtering breaks lists"). Runs the real SQL
// predicate against a real database and property-checks it against
// authz.Resolver.CanViewJob (the Go-side predicate it's meant to mirror
// exactly) for every seeded job, from six viewer perspectives: owner,
// stranger, a project-scoped member assigned directly, a project-scoped
// member assigned via a group, an org admin, and a global admin. Also
// covers pagination correctness (full pages, no duplicates, exact Total)
// directly against Postgres.
//
// Like ui_auth_integration_test.go's tests (whose createTestUser/
// createTestProject/uniqueName helpers this file reuses), these writes are
// NOT wrapped in a rollback-able transaction: project rows are created via
// a direct testDB.Create (see createTestProject), so this file follows the
// same non-transactional, real-commit, unique-name-per-fixture pattern
// rather than RunTransactionalTest, to avoid a rolled-back user row leaving
// a dangling FK under a project/job that was committed for real.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// jobsVisibleToStore mirrors internal/handlers/job_handler.go's narrow
// interface of the same name — declared separately here since this package
// doesn't import internal/handlers, and this test wants to call the store
// method directly rather than going through HTTP.
type jobsVisibleToStore interface {
	ListJobsVisibleTo(ctx context.Context, viewerID string, isGlobalAdmin bool, filters map[string]interface{}, limit, offset int) ([]models.Job, int64, error)
}

func requireJobsVisibleToStore(t *testing.T) jobsVisibleToStore {
	t.Helper()
	vs, ok := store.AppStore.(jobsVisibleToStore)
	require.True(t, ok, "store.AppStore must implement ListJobsVisibleTo for this test to exercise the real SQL predicate")
	return vs
}

// createTestJobInProject is createTestJob (ui_auth_integration_test.go)
// with an explicit project — needed to exercise the "job belongs to a
// project" visibility branch (project-scoped role assignments, project
// IsPrivate/owning-org cascade).
func createTestJobInProject(t *testing.T, userID, projectID, status string) *models.Job {
	t.Helper()
	du := &DataUtils{db: testDB}
	job, err := du.CreateJob(DataSetup{
		"UserID":     userID,
		"ProjectID":  &projectID,
		"Status":     status,
		"JobCommand": "true",
	})
	require.NoError(t, err)
	return job
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func TestListJobsVisibleTo_MatchesCanViewJob(t *testing.T) {
	ctx := context.Background()
	vs := requireJobsVisibleToStore(t)
	ds := requireDataStore(t)
	resolver := authz.NewResolver(store.AppStore.(authz.RoleStore))

	// --- Orgs -------------------------------------------------------------
	du := &DataUtils{db: testDB}
	publicOrg := createTestUser(t)
	privateOrg, err := du.CreateUser(DataSetup{"IsPrivate": true})
	require.NoError(t, err)

	// --- Private project under privateOrg, with a directly-assigned
	// member and a group-assigned member ------------------------------------
	privateProject := createTestProject(t, privateOrg.UserID, "github.com/vis-test/"+uniqueName("private-project"), true)

	assignedMember := createTestUser(t)
	require.NoError(t, ds.CreateRoleAssignment(ctx, &models.RoleAssignment{
		PrincipalType: models.PrincipalTypeUser,
		PrincipalID:   assignedMember.UserID,
		ScopeType:     models.ScopeTypeProject,
		ScopeID:       &privateProject.ProjectID,
		Role:          models.RoleMember,
	}))

	groupMember := createTestUser(t)
	group := &models.Group{OrgID: privateOrg.UserID, Name: uniqueName("group")}
	require.NoError(t, ds.CreateGroup(ctx, group))
	require.NoError(t, ds.AddGroupMember(ctx, group.GroupID, groupMember.UserID))
	require.NoError(t, ds.CreateRoleAssignment(ctx, &models.RoleAssignment{
		PrincipalType: models.PrincipalTypeGroup,
		PrincipalID:   group.GroupID,
		ScopeType:     models.ScopeTypeProject,
		ScopeID:       &privateProject.ProjectID,
		Role:          models.RoleMember,
	}))

	// --- An org admin of privateOrg (not the org itself) --------------------
	orgAdmin := createTestUser(t)
	require.NoError(t, ds.CreateRoleAssignment(ctx, &models.RoleAssignment{
		PrincipalType: models.PrincipalTypeUser,
		PrincipalID:   orgAdmin.UserID,
		ScopeType:     models.ScopeTypeOrg,
		ScopeID:       &privateOrg.UserID,
		Role:          models.RoleAdmin,
	}))

	// --- A global admin -------------------------------------------------
	globalAdmin := createTestUser(t)
	require.NoError(t, ds.CreateRoleAssignment(ctx, &models.RoleAssignment{
		PrincipalType: models.PrincipalTypeUser,
		PrincipalID:   globalAdmin.UserID,
		ScopeType:     models.ScopeTypeGlobal,
		Role:          models.RoleAdmin,
	}))

	// --- A stranger with no relationship to anything ------------------------
	stranger := createTestUser(t)

	// --- Seed jobs ----------------------------------------------------------
	publicJob := createTestJob(t, publicOrg.UserID, "completed")
	privateLooseJob := createTestJob(t, privateOrg.UserID, "completed")
	privateProjectJob := createTestJobInProject(t, privateOrg.UserID, privateProject.ProjectID, "completed")

	allJobIDs := []string{publicJob.JobID, privateLooseJob.JobID, privateProjectJob.JobID}

	// --- Property check: for each viewer, ListJobsVisibleTo's result must
	// exactly match authz.CanViewJob per job. ------------------------------
	type viewerCase struct {
		name          string
		id            authz.Identity
		isGlobalAdmin bool
	}
	viewers := []viewerCase{
		{"privateOrg (owner)", authz.UserIdentity(privateOrg.UserID), false},
		{"stranger", authz.UserIdentity(stranger.UserID), false},
		{"assignedMember (direct project grant)", authz.UserIdentity(assignedMember.UserID), false},
		{"groupMember (via group project grant)", authz.UserIdentity(groupMember.UserID), false},
		{"orgAdmin", authz.UserIdentity(orgAdmin.UserID), false},
		{"globalAdmin", authz.UserIdentity(globalAdmin.UserID), true},
	}

	for _, v := range viewers {
		t.Run(v.name, func(t *testing.T) {
			expected := map[string]bool{}
			for _, jobID := range allJobIDs {
				job, err := store.AppStore.GetJobByID(ctx, jobID)
				require.NoError(t, err)
				canView, err := resolver.CanViewJob(ctx, v.id, job)
				require.NoError(t, err)
				if canView {
					expected[jobID] = true
				}
			}

			got, total, err := vs.ListJobsVisibleTo(ctx, v.id.UserID, v.isGlobalAdmin, map[string]interface{}{}, 200, 0)
			require.NoError(t, err)

			gotSet := map[string]bool{}
			for _, j := range got {
				if contains(allJobIDs, j.JobID) {
					gotSet[j.JobID] = true
				}
			}
			require.Equal(t, expected, gotSet, "ListJobsVisibleTo must match authz.CanViewJob exactly for %s", v.name)

			// Other tests in this shared-container package leave rows
			// behind, so Total (a global count) can't be asserted exactly
			// here — but it must be at least the count of our own
			// expected-visible rows.
			require.GreaterOrEqual(t, total, int64(len(expected)))
		})
	}
}

// TestListJobsVisibleTo_PaginationReturnsFullPages proves pagination and
// Total are both exact against real Postgres: with more visible jobs than
// fit in one page, each page comes back full (until the last, short page)
// and every job is returned exactly once across all pages, scoped by an
// explicit user_id filter so this test's assertions aren't affected by
// other rows left behind by other tests in this shared-container package.
func TestListJobsVisibleTo_PaginationReturnsFullPages(t *testing.T) {
	ctx := context.Background()
	vs := requireJobsVisibleToStore(t)

	owner := createTestUser(t)

	const n = 5
	seeded := make([]string, 0, n)
	for i := 0; i < n; i++ {
		job := createTestJob(t, owner.UserID, "completed")
		seeded = append(seeded, job.JobID)
	}

	filters := map[string]interface{}{"user_id": owner.UserID}

	seen := map[string]bool{}
	var lastTotal int64
	for _, offset := range []int{0, 2, 4} {
		page, total, err := vs.ListJobsVisibleTo(ctx, owner.UserID, false, filters, 2, offset)
		require.NoError(t, err)
		require.Equal(t, int64(n), total, "Total must be exact at offset=%d", offset)
		lastTotal = total
		for _, j := range page {
			require.False(t, seen[j.JobID], "job %s returned on more than one page", j.JobID)
			seen[j.JobID] = true
		}
	}
	require.Equal(t, int64(len(seen)), lastTotal)
	for _, id := range seeded {
		require.True(t, seen[id], "expected job %s to be visible across the paginated set", id)
	}
}
