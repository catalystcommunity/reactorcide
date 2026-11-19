package test

// Postgres-backed coverage for WP13b (plan item D1, docs/priority.md):
// migration 000064, the lane and group shift store, and the priority that
// jobs store and keep on retry.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobcontrol"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/principals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/priority"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
)

const lanesMigrationVersion = 64

func TestPriorityLanesMigration(t *testing.T) {
	db := newScratchDBAt(t, "wp13b_migrate", 63)
	var orgID string
	require.NoError(t, db.QueryRow(`INSERT INTO organizations (name, display_name) VALUES ('wp13b-org', 'WP13b') RETURNING org_id`).Scan(&orgID))
	var groupID string
	require.NoError(t, db.QueryRow(`INSERT INTO groups (org_id, name) VALUES ($1, 'release') RETURNING group_id`, orgID).Scan(&groupID))
	var jobID string
	require.NoError(t, db.QueryRow(`INSERT INTO jobs (org_id, name, job_command, priority) VALUES ($1, 'old', 'true', 10) RETURNING job_id`, orgID).Scan(&jobID))

	require.NoError(t, goose.UpTo(db, "migrations", lanesMigrationVersion))

	var shift int
	require.NoError(t, db.QueryRow(`SELECT priority_shift FROM groups WHERE group_id = $1`, groupID).Scan(&shift))
	assert.Zero(t, shift, "an existing group has no shift")
	var lane string
	var priorityValue int
	var reasonNull bool
	require.NoError(t, db.QueryRow(`SELECT priority_lane, priority, priority_reason IS NULL FROM jobs WHERE job_id = $1`, jobID).
		Scan(&lane, &priorityValue, &reasonNull))
	assert.Equal(t, "", lane)
	assert.Equal(t, 10, priorityValue, "an existing job keeps its priority")
	assert.True(t, reasonNull)

	_, err := db.Exec(`UPDATE groups SET priority_shift = 51 WHERE group_id = $1`, groupID)
	assert.Error(t, err)
	_, err = db.Exec(`UPDATE groups SET priority_shift = -50 WHERE group_id = $1`, groupID)
	assert.NoError(t, err)
	_, err = db.Exec(`INSERT INTO priority_lanes (org_id, name, base) VALUES ($1, 'ok', 100)`, orgID)
	assert.NoError(t, err)
	for _, bad := range []string{
		`INSERT INTO priority_lanes (org_id, name, base) VALUES ($1, 'big', 501)`,
		`INSERT INTO priority_lanes (org_id, name, base) VALUES ($1, 'Bad Name', 1)`,
		`INSERT INTO priority_lanes (org_id, name, base, job_kind) VALUES ($1, 'kind', 1, 'deploy')`,
		`INSERT INTO priority_lanes (org_id, name, base) VALUES ($1, 'ok', 1)`,
	} {
		_, err = db.Exec(bad, orgID)
		assert.Error(t, err, bad)
	}

	require.NoError(t, goose.DownTo(db, "migrations", 63))
	var columns int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM information_schema.columns
		WHERE (table_name = 'groups' AND column_name = 'priority_shift')
		   OR (table_name = 'jobs' AND column_name IN ('priority_lane', 'priority_reason'))`).Scan(&columns))
	assert.Zero(t, columns)
	var tables int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_name = 'priority_lanes'`).Scan(&tables))
	assert.Zero(t, tables)
	require.NoError(t, db.QueryRow(`SELECT priority FROM jobs WHERE job_id = $1`, jobID).Scan(&priorityValue))
	assert.Equal(t, 10, priorityValue, "Down keeps the stored priority")
}

func TestPriorityLaneStore(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		ps := postgres_store.PostgresStore
		org := newPrincipalOrg(t, ctx, uniqueName("lanes"))
		project := &models.Project{Name: uniqueName("lanes-app"), RepoURL: "github.com/lanes/" + uniqueName("app"), OrgID: org.OrgID}
		require.NoError(t, ps.CreateProject(ctx, project))
		no := false

		orgLane := &models.PriorityLane{OrgID: org.OrgID, Name: "pr-ready", Position: 400, Base: -50, ClassesAny: []string{"PR"}, Draft: &no, Enabled: true}
		require.NoError(t, ps.UpsertPriorityLane(ctx, orgLane))
		require.NotEmpty(t, orgLane.LaneID)
		assert.Equal(t, []string{"pr"}, []string(orgLane.ClassesAny), "classes are normalized")

		projectLane := &models.PriorityLane{OrgID: org.OrgID, ProjectID: &project.ProjectID, Name: "pr-ready", Position: 400, Base: -20,
			ClassesAny: []string{"pr"}, EventTypes: []string{"pull_request_opened"}, JobKind: "work", Enabled: true}
		require.NoError(t, ps.UpsertPriorityLane(ctx, projectLane))
		assert.NotEqual(t, orgLane.LaneID, projectLane.LaneID, "the same name in another scope is another lane")

		// Replace keeps the lane ID and writes every field.
		firstID := orgLane.LaneID
		orgLane.Base = -60
		orgLane.Draft = nil
		orgLane.Retry = true
		require.NoError(t, ps.UpsertPriorityLane(ctx, orgLane))
		assert.Equal(t, firstID, orgLane.LaneID)
		got, err := ps.GetPriorityLane(ctx, org.OrgID, nil, "pr-ready")
		require.NoError(t, err)
		assert.Equal(t, -60, got.Base)
		assert.Nil(t, got.Draft)
		assert.True(t, got.Retry)

		orgLanes, err := ps.ListPriorityLanes(ctx, org.OrgID, nil)
		require.NoError(t, err)
		require.Len(t, orgLanes, 1)
		projectLanes, err := ps.ListPriorityLanes(ctx, org.OrgID, &project.ProjectID)
		require.NoError(t, err)
		require.Len(t, projectLanes, 1)
		assert.Equal(t, []string{"pull_request_opened"}, []string(projectLanes[0].EventTypes))

		// The effective lanes through the store: the project lane wins.
		lanes := priority.LoadLanes(ctx, ps, org.OrgID, &project.ProjectID)
		d := priority.Compute(lanes, priority.GroupShifts{}, priority.Facts{JobKind: "work", RefClasses: []string{"pr"}, EventType: "pull_request_opened"})
		assert.Equal(t, -20, d.Final)
		assert.Equal(t, priority.ScopeProject, d.Scope)

		err = ps.UpsertPriorityLane(ctx, &models.PriorityLane{OrgID: org.OrgID, Name: "Bad Name", Base: 1, Enabled: true})
		assert.ErrorIs(t, err, store.ErrInvalidInput)
		err = ps.UpsertPriorityLane(ctx, &models.PriorityLane{OrgID: org.OrgID, Name: "big", Base: 900, Enabled: true})
		assert.ErrorIs(t, err, store.ErrInvalidInput)

		require.NoError(t, ps.DeletePriorityLane(ctx, org.OrgID, &project.ProjectID, "pr-ready"))
		assert.ErrorIs(t, ps.DeletePriorityLane(ctx, org.OrgID, &project.ProjectID, "pr-ready"), store.ErrNotFound)
		_, err = ps.GetPriorityLane(ctx, org.OrgID, nil, "pr-ready")
		assert.NoError(t, err, "deleting the project lane keeps the organization lane")
	})
}

func TestGroupPriorityShiftStore(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		ps := postgres_store.PostgresStore
		du := &DataUtils{db: tx}
		member, err := du.CreateUser(DataSetup{"Username": uniqueName("shift-member"), "Email": "m@example.com"})
		require.NoError(t, err)
		org := newPrincipalOrg(t, ctx, uniqueName("shifts"))
		release := &models.Group{OrgID: org.OrgID, Name: "release"}
		bots := &models.Group{OrgID: org.OrgID, Name: "bots"}
		plain := &models.Group{OrgID: org.OrgID, Name: "plain"}
		for _, group := range []*models.Group{release, bots, plain} {
			require.NoError(t, ps.CreateGroup(ctx, group))
			require.NoError(t, ps.AddGroupMemberPrincipal(ctx, group.GroupID, member.UserID))
		}
		require.NoError(t, ps.SetGroupPriorityShift(ctx, release.GroupID, 20))
		require.NoError(t, ps.SetGroupPriorityShift(ctx, bots.GroupID, -30))
		assert.ErrorIs(t, ps.SetGroupPriorityShift(ctx, plain.GroupID, 51), store.ErrInvalidInput)
		assert.ErrorIs(t, ps.SetGroupPriorityShift(ctx, "00000000-0000-0000-0000-00000000dead", 1), store.ErrNotFound)

		shifts, err := ps.ListPrincipalGroupShifts(ctx, org.OrgID, member.UserID)
		require.NoError(t, err)
		require.Len(t, shifts, 2, "a group with shift 0 is not listed")
		assert.Equal(t, "bots", shifts[0].Name)
		assert.Equal(t, -30, shifts[0].Shift)
		assert.Equal(t, 20, shifts[1].Shift)

		other := newPrincipalOrg(t, ctx, uniqueName("shifts-other"))
		none, err := ps.ListPrincipalGroupShifts(ctx, other.OrgID, member.UserID)
		require.NoError(t, err)
		assert.Empty(t, none, "groups of another organization do not apply")

		byID, err := ps.ListGroupShifts(ctx, org.OrgID, []string{release.GroupID, plain.GroupID, "not-a-uuid"})
		require.NoError(t, err)
		require.Len(t, byID, 1)
		assert.Equal(t, "release", byID[0].Name)

		groups, err := ps.ListGroupsByOrg(ctx, org.OrgID)
		require.NoError(t, err)
		for _, group := range groups {
			if group.Name == "release" {
				assert.Equal(t, 20, group.PriorityShift)
				// A UI rename (Save of the full row) keeps the shift.
				group.Description = "release team"
				require.NoError(t, ps.UpdateGroup(ctx, &group))
			}
		}
		reloaded, err := ps.GetGroupByID(ctx, release.GroupID)
		require.NoError(t, err)
		assert.Equal(t, 20, reloaded.PriorityShift)
	})
}

// The whole path on a real database: an event with an actor, an org lane,
// group shifts, the stored decision, and a retry by another actor.
func TestPriorityAssignAndRetry(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		ps := postgres_store.PostgresStore
		du := &DataUtils{db: tx}
		author, err := du.CreateUser(DataSetup{"Username": uniqueName("prio-author"), "Email": "a@example.com"})
		require.NoError(t, err)
		rerunner, err := du.CreateUser(DataSetup{"Username": uniqueName("prio-rerunner"), "Email": "r@example.com"})
		require.NoError(t, err)
		org := newPrincipalOrg(t, ctx, uniqueName("prio"))
		project := &models.Project{Name: uniqueName("prio-app"), RepoURL: "github.com/prio/" + uniqueName("app"), OrgID: org.OrgID}
		require.NoError(t, ps.CreateProject(ctx, project))

		devs := &models.Group{OrgID: org.OrgID, Name: "devs"}
		admins := &models.Group{OrgID: org.OrgID, Name: "admins"}
		require.NoError(t, ps.CreateGroup(ctx, devs))
		require.NoError(t, ps.CreateGroup(ctx, admins))
		require.NoError(t, ps.AddGroupMemberPrincipal(ctx, devs.GroupID, author.UserID))
		require.NoError(t, ps.AddGroupMemberPrincipal(ctx, admins.GroupID, rerunner.UserID))
		require.NoError(t, ps.SetGroupPriorityShift(ctx, devs.GroupID, 10))
		require.NoError(t, ps.SetGroupPriorityShift(ctx, admins.GroupID, 50))
		require.NoError(t, ps.UpsertPriorityLane(ctx, &models.PriorityLane{OrgID: org.OrgID, Name: "pr-ready", Position: 400, Base: -60,
			ClassesAny: []string{"pr"}, Draft: boolRef(false), Enabled: true}))

		key := uniqueName("prio-delivery")
		event, _, err := ps.RecordCIEvent(ctx, &models.CIEvent{OrgID: org.OrgID, ProjectID: &project.ProjectID, Source: "webhook",
			Provider: "github", EventType: "pull_request_opened", Repo: "prio/app", IdempotencyKey: &key, SemanticKey: key,
			RefClasses: []string{"pr"}, ActorPrincipalID: &author.UserID})
		require.NoError(t, err)

		job := &models.Job{OrgID: org.OrgID, ProjectID: &project.ProjectID, Name: "build", JobCommand: "true", EventID: &event.EventID,
			Priority: 5, Status: "failed"}
		d := priority.Assign(ctx, ps, job)
		require.NoError(t, ps.CreateJob(ctx, job))
		assert.Equal(t, "pr-ready", d.Lane)
		assert.Equal(t, -60+10+5, d.Final, "org lane base, the author's group, and the request")

		stored, err := ps.GetJobByID(ctx, job.JobID)
		require.NoError(t, err)
		assert.Equal(t, -45, stored.Priority)
		assert.Equal(t, "pr-ready", stored.PriorityLane)
		require.NotNil(t, stored.PriorityReason, "the reason is stored as jsonb")
		assert.Equal(t, "priority", stored.PriorityReason.Kind)
		require.NotNil(t, stored.PriorityReason.Final)
		assert.Equal(t, -45, *stored.PriorityReason.Final)
		assert.Equal(t, priority.ScopeOrg, stored.PriorityReason.Value)

		// A retry by a member of a +50 group keeps the original priority.
		actor := principals.Actor{PrincipalID: rerunner.UserID, Identity: "user:" + rerunner.UserID}
		retried, err := jobcontrol.RetryJob(principals.WithActor(ctx, actor), ps, nil, stored)
		require.NoError(t, err)
		retriedStored, err := ps.GetJobByID(ctx, retried.JobID)
		require.NoError(t, err)
		assert.Equal(t, rerunner.UserID, *retriedStored.ActorPrincipalID)
		assert.Equal(t, -45, retriedStored.Priority, "the re-runner's groups do not apply")
		assert.Equal(t, "pr-ready", retriedStored.PriorityLane)
		require.NotNil(t, retriedStored.PriorityReason)
		assert.Contains(t, retriedStored.PriorityReason.Detail, "kept from job "+stored.JobID)

		explanation := priority.ExplainJob(ctx, ps, stored)
		require.NotNil(t, explanation.Stored)
		assert.Equal(t, -45, explanation.Stored.Final)
		assert.Equal(t, author.UserID, explanation.Facts.ActorPrincipalID)
	})
}

func boolRef(value bool) *bool { return &value }

// The no-login flow through the real router: the admin token manages lanes
// and a group shift, explains facts, and a REST submit records its lane.
func TestPriorityLanesAPI(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		du := &DataUtils{db: tx}
		admin, err := du.CreateUser(DataSetup{"Username": uniqueName("prio-admin"), "Email": "pa@example.com"})
		require.NoError(t, err)
		auth, err := createTokenAuthHeader(ctx, tx, admin.UserID)
		require.NoError(t, err)
		orgName := uniqueName("prio-api")
		org := newPrincipalOrg(t, ctx, orgName)
		project := &models.Project{Name: uniqueName("prio-api-app"), RepoURL: "github.com/prio/" + uniqueName("api"), OrgID: org.OrgID}
		require.NoError(t, postgres_store.PostgresStore.CreateProject(ctx, project))

		rr := principalRequest(t, ctx, http.MethodPut, "/api/v1/organizations/"+orgName+"/priority-lanes/manual", auth, map[string]any{"base": 30})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodPut, "/api/v1/projects/"+project.ProjectID+"/priority-lanes/hotfix", auth,
			map[string]any{"base": 200, "position": 5, "match": map[string]any{"classes_any": []string{"hotfix"}}})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodPut, "/api/v1/projects/"+project.ProjectID+"/priority-lanes/new", auth, map[string]any{"base": 1})
		require.Equal(t, http.StatusBadRequest, rr.Code, "a new lane needs a match")

		rr = principalRequest(t, ctx, http.MethodGet, "/api/v1/projects/"+project.ProjectID+"/priority-lanes", auth, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		var listed struct {
			Lanes     []models.PriorityLane `json:"lanes"`
			Inherited []models.PriorityLane `json:"inherited"`
			Effective []priority.Lane       `json:"effective"`
		}
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &listed))
		require.Len(t, listed.Lanes, 1)
		require.Len(t, listed.Inherited, 1)
		assert.Equal(t, "hotfix", listed.Effective[0].Name)

		group := &models.Group{OrgID: org.OrgID, Name: "release"}
		require.NoError(t, postgres_store.PostgresStore.CreateGroup(ctx, group))
		require.NoError(t, postgres_store.PostgresStore.AddGroupMemberPrincipal(ctx, group.GroupID, admin.UserID))
		rr = principalRequest(t, ctx, http.MethodPut, "/api/v1/organizations/"+orgName+"/groups/release/priority-shift", auth, map[string]any{"shift": 15})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

		rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/organizations/"+orgName+"/priority/explain", auth,
			map[string]any{"ref_classes": []string{"manual"}, "actor": admin.UserID, "requested": 4})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		var explained priority.Explanation
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &explained))
		assert.Equal(t, "manual", explained.Decision.Lane)
		assert.Equal(t, 30+15+4, explained.Decision.Final)

		// A REST submit: a manual job of the organization, with a request.
		type submittedJob struct {
			JobID          string                 `json:"job_id"`
			Priority       int                    `json:"priority"`
			PriorityLane   string                 `json:"priority_lane"`
			PriorityReason *models.DecisionReason `json:"priority_reason"`
		}
		submit := func() submittedJob {
			rr := principalRequest(t, ctx, http.MethodPost, "/api/v1/jobs", auth, map[string]any{
				"name": "manual-build", "job_command": "true", "source_type": "copy", "source_path": "./src",
				"org": orgName, "priority": 4,
			})
			require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
			var submitted submittedJob
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &submitted))
			return submitted
		}
		first := submit()
		assert.Equal(t, "manual", first.PriorityLane)
		assert.Equal(t, 30+4, first.Priority, "the actor of the token is in no group yet")
		stored, err := postgres_store.PostgresStore.GetJobByID(ctx, first.JobID)
		require.NoError(t, err)
		assert.Equal(t, first.Priority, stored.Priority)
		assert.Equal(t, "manual", stored.PriorityLane)
		require.NotNil(t, stored.ActorPrincipalID)

		// Put the actor of the token in the group: the next submit gets the shift.
		require.NoError(t, postgres_store.PostgresStore.AddGroupMemberPrincipal(ctx, group.GroupID, *stored.ActorPrincipalID))
		second := submit()
		assert.Equal(t, 30+15+4, second.Priority, "org lane base, the submitter's group, and the request")
		require.NotNil(t, second.PriorityReason)
		assert.Contains(t, second.PriorityReason.Detail, "group release +15")

		rr = principalRequest(t, ctx, http.MethodDelete, "/api/v1/organizations/"+orgName+"/priority-lanes/manual", auth, nil)
		require.Equal(t, http.StatusNoContent, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodGet, "/api/v1/organizations/"+orgName+"/priority-lanes", "", nil)
		assert.Equal(t, http.StatusUnauthorized, rr.Code)
	})
}
