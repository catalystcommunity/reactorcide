package test

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobcontrol"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/owners"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
	"github.com/catalystcommunity/reactorcide/coredb"
)

// WP7: the owner snapshot on workflows and jobs (migration 000053).
func TestWorkflowOwnerSnapshot(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		ps := postgres_store.PostgresStore
		org := newPrincipalOrg(t, ctx, "wo-snapshot")
		project := &models.Project{Name: "wo-app", RepoURL: "github.com/wo/app", OrgID: org.OrgID}
		require.NoError(t, ps.CreateProject(ctx, project))

		group := "payments-ci"
		wf := &models.WorkflowInstance{OrgID: org.OrgID, ProjectID: &project.ProjectID, Name: "Payments", WorkflowSecurityID: "payments", Status: "failed"}
		wf.SetOwnership(models.WorkflowOwnership{Group: &group, Subjects: []string{"vcs_user:github/alice"},
			Contact: &models.WorkflowContact{Channel: "#payments-ci"}, CISHA: "base-sha", Trusted: true, RefClasses: []string{"default"}}, models.OwnerSourceCI)
		require.NoError(t, ps.CreateWorkflowInstance(ctx, wf))
		assert.True(t, wf.OwnerTrusted)

		// A job of the workflow gets the snapshot from the database trigger.
		job := &models.Job{OrgID: org.OrgID, ProjectID: &project.ProjectID, Name: "test", JobCommand: "x", WorkflowID: &wf.WorkflowID, Status: "failed"}
		require.NoError(t, ps.CreateJob(ctx, job))
		require.NotNil(t, job.OwnerGroup, "CreateJob reads the trigger-set owner back")
		assert.Equal(t, "payments-ci", *job.OwnerGroup)
		assert.Equal(t, []string{"vcs_user:github/alice"}, []string(job.OwnerSubjects))
		require.NotNil(t, job.OwnerContact)
		assert.Equal(t, "#payments-ci", job.OwnerContact.Channel)

		// The snapshot never changes on update, also from a struct that did
		// not read it, or one that tries to change it.
		blank := *job
		blank.OwnerGroup, blank.OwnerSubjects, blank.OwnerContact = nil, nil, nil
		blank.Notes = "changed"
		require.NoError(t, ps.UpdateJob(ctx, &blank))
		reloaded, err := ps.GetJobByID(ctx, job.JobID)
		require.NoError(t, err)
		require.NotNil(t, reloaded.OwnerGroup)
		assert.Equal(t, "payments-ci", *reloaded.OwnerGroup)

		changed := "attacker"
		wfEdit, err := ps.GetWorkflowInstance(ctx, wf.WorkflowID)
		require.NoError(t, err)
		wfEdit.OwnerGroup = &changed
		wfEdit.Status = "failed"
		require.NoError(t, ps.UpdateWorkflowInstance(ctx, wfEdit))
		wfStored, err := ps.GetWorkflowInstance(ctx, wf.WorkflowID)
		require.NoError(t, err)
		assert.Equal(t, "payments-ci", wfStored.Ownership().GroupName())
		assert.Equal(t, models.OwnerSourceCI, wfStored.OwnerSource)
		assert.Equal(t, "base-sha", wfStored.OwnerCISHA)

		// Outbound events (WP12) carry the owner group: event_outbox_owner_group
		// reads the owner_group column of the row.
		var principalID string
		require.NoError(t, tx.Raw(`INSERT INTO principals (kind, org_id, name) VALUES ('service', ?, 'wo-hooks') RETURNING principal_id`,
			org.OrgID).Scan(&principalID).Error)
		require.NoError(t, tx.Exec(`INSERT INTO event_subscriptions (org_id, name, owner_principal_id, scope, sink_type,
signing_secret_ciphertext, signing_secret_key_name) VALUES (?, 'wo-sub', ?, 'org', 'webhook', '\x00', 'test')`, org.OrgID, principalID).Error)
		running := *reloaded
		running.Status = "running"
		require.NoError(t, ps.UpdateJob(ctx, &running))
		var outboxOwners []string
		require.NoError(t, tx.Raw(`SELECT owner_group FROM event_outbox WHERE job_id = ?`, job.JobID).Scan(&outboxOwners).Error)
		require.NotEmpty(t, outboxOwners, "a job status change records an outbound event")
		for _, value := range outboxOwners {
			assert.Equal(t, "payments-ci", value)
		}

		// A child workflow gets the owner of its parent.
		child := &models.WorkflowInstance{OrgID: org.OrgID, ProjectID: &project.ProjectID, Name: "child", WorkflowSecurityID: "child",
			ParentWorkflowID: &wf.WorkflowID, RootWorkflowID: &wf.WorkflowID}
		require.NoError(t, ps.CreateWorkflowInstance(ctx, child))
		assert.Equal(t, "payments-ci", child.Ownership().GroupName())
		assert.Equal(t, models.OwnerSourceParent, child.OwnerSource)
		assert.True(t, child.OwnerTrusted, "a child copies the trust of its parent")
		assert.Equal(t, []string{"default"}, []string(child.OwnerRefClasses))

		// An untrusted "ci" snapshot (a feature-branch push, a pull request
		// head) is refused by the database trigger.
		forged := &models.WorkflowInstance{OrgID: org.OrgID, ProjectID: &project.ProjectID, Name: "Payments", WorkflowSecurityID: "payments"}
		attacker := "attacker"
		forged.SetOwnership(models.WorkflowOwnership{Group: &attacker, CISHA: "feature-sha"}, models.OwnerSourceCI)
		require.NoError(t, ps.CreateWorkflowInstance(ctx, forged))
		var forgedGroup sql.NullString
		require.NoError(t, tx.Raw(`SELECT owner_group FROM workflow_instances WHERE workflow_id = ?`, forged.WorkflowID).Row().Scan(&forgedGroup))
		assert.False(t, forgedGroup.Valid, "an untrusted ci snapshot must not be stored")

		// A newer trusted "prior" copy is not the latest origin; a newer
		// untrusted row with a group (written by raw SQL) never counts.
		require.NoError(t, tx.Exec(`INSERT INTO workflow_instances (org_id, project_id, name, workflow_security_id, owner_group, owner_source, owner_trusted, created_at)
			VALUES (?, ?, 'p', 'payments', 'attacker', 'prior', false, now() + interval '1 hour')`, org.OrgID, project.ProjectID).Error)

		// NULL means unknown: a workflow created with no snapshot.
		unknown := &models.WorkflowInstance{OrgID: org.OrgID, ProjectID: &project.ProjectID, Name: "Old", WorkflowSecurityID: "old"}
		require.NoError(t, ps.CreateWorkflowInstance(ctx, unknown))
		assert.Nil(t, unknown.OwnerGroup)
		var raw sql.NullString
		require.NoError(t, tx.Raw(`SELECT owner_source FROM workflow_instances WHERE workflow_id = ?`, unknown.WorkflowID).Row().Scan(&raw))
		assert.False(t, raw.Valid)

		// The latest trusted snapshot of a workflow, for quarantine and
		// approvals (owners.ForWorkflow / ForProject).
		latest, err := owners.ForWorkflow(ctx, ps, project.ProjectID, "payments")
		require.NoError(t, err)
		assert.Equal(t, "payments-ci", latest.GroupName())
		none, err := owners.ForWorkflow(ctx, ps, project.ProjectID, "old")
		require.NoError(t, err)
		assert.False(t, none.Known())
		all, err := owners.ForProject(ctx, ps, project.ProjectID)
		require.NoError(t, err)
		assert.Contains(t, all, "payments")
		assert.NotContains(t, all, "old")

		// A retry of the workflow keeps the snapshot of the original run.
		retried, err := jobcontrol.RetryWorkflow(ctx, ps, nil, wf.WorkflowID)
		require.NoError(t, err)
		retriedStored, err := ps.GetWorkflowInstance(ctx, retried.WorkflowID)
		require.NoError(t, err)
		assert.Equal(t, "payments-ci", retriedStored.Ownership().GroupName())
		assert.Equal(t, models.OwnerSourceRetry, retriedStored.OwnerSource)
	})
}

// WP7: workflow-scoped roles, and owner group members, with a real store.
func TestWorkflowScopedRoles(t *testing.T) {
	origMode := config.UIAuthMode
	defer func() { config.UIAuthMode = origMode }()
	config.UIAuthMode = config.UIAuthModeLocalRP
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		ps := postgres_store.PostgresStore
		org := newPrincipalOrg(t, ctx, "wo-roles")
		project := &models.Project{Name: "wo-roles-app", RepoURL: "github.com/wo/roles", OrgID: org.OrgID}
		require.NoError(t, ps.CreateProject(ctx, project))
		newUser := func(name string) *models.User {
			user := &models.User{Username: name, Email: name + "@example.com", Roles: pq.StringArray{"user"}}
			require.NoError(t, ps.CreateUser(ctx, user))
			return user
		}
		holder, member, stranger := newUser("wo-holder"), newUser("wo-member"), newUser("wo-stranger")
		group := &models.Group{OrgID: org.OrgID, Name: "payments-ci"}
		require.NoError(t, ps.CreateGroup(ctx, group))
		require.NoError(t, ps.AddGroupMember(ctx, group.GroupID, member.UserID))

		workflow := "payments"
		require.NoError(t, ps.CreateRoleAssignment(ctx, &models.RoleAssignment{PrincipalType: models.PrincipalTypeUser, PrincipalID: holder.UserID,
			ScopeType: models.ScopeTypeWorkflow, ScopeID: &project.ProjectID, ScopeWorkflow: &workflow, Role: models.RoleOwner}))
		listed, err := ps.ListRoleAssignmentsByScope(ctx, models.ScopeTypeWorkflow, &project.ProjectID)
		require.NoError(t, err)
		require.Len(t, listed, 1)
		assert.Equal(t, "payments", *listed[0].ScopeWorkflow)

		groupName := "payments-ci"
		owner := models.WorkflowOwnership{Group: &groupName, Trusted: true}
		resolver := authz.NewResolver(ps)
		caps := func(userID, securityID string, scoped bool) authz.Caps {
			scope := authz.Scope{ProjectID: &project.ProjectID}
			if scoped {
				scope.Workflow = &authz.WorkflowScope{SecurityID: securityID, Owner: owner}
			}
			got, capsErr := resolver.Capabilities(ctx, authz.UserIdentity(userID), scope)
			require.NoError(t, capsErr)
			return got
		}
		for _, userID := range []string{holder.UserID, member.UserID} {
			got := caps(userID, "payments", true)
			assert.True(t, got.Cancel && got.Retry && got.ManageWorkflow, "owner of payments: %+v", got)
			assert.False(t, got.Kill, "a workflow owner never kills")
			assert.Equal(t, authz.Caps{}, caps(userID, "payments", false), "project scope is unchanged")
		}
		// Another workflow of the same project: the holder's role does not
		// reach it; the group member owns it only if its owner is the group.
		owner = models.WorkflowOwnership{Group: new(string), Trusted: true}
		assert.Equal(t, authz.Caps{}, caps(holder.UserID, "docs", true))
		assert.Equal(t, authz.Caps{}, caps(member.UserID, "docs", true))
		assert.Equal(t, authz.Caps{}, caps(stranger.UserID, "payments", true))
	})
}

func TestWorkflowRoleConstraints(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		org := newPrincipalOrg(t, ctx, "wo-constraints")
		project := &models.Project{Name: "wo-c-app", RepoURL: "github.com/wo/c", OrgID: org.OrgID}
		require.NoError(t, postgres_store.PostgresStore.CreateProject(ctx, project))
		// The CHECK refuses: a workflow scope with a non-owner role, with no
		// workflow, with a bad workflow ID, and a workflow on a project scope.
		cases := []string{
			`INSERT INTO role_assignments (principal_type, principal_id, scope_type, scope_id, scope_workflow, role) VALUES ('group', generate_ulid(), 'workflow', ?, 'pay', 'member')`,
			`INSERT INTO role_assignments (principal_type, principal_id, scope_type, scope_id, role) VALUES ('group', generate_ulid(), 'workflow', ?, 'owner')`,
			`INSERT INTO role_assignments (principal_type, principal_id, scope_type, scope_id, scope_workflow, role) VALUES ('group', generate_ulid(), 'workflow', ?, 'Bad ID', 'owner')`,
			`INSERT INTO role_assignments (principal_type, principal_id, scope_type, scope_id, scope_workflow, role) VALUES ('group', generate_ulid(), 'project', ?, 'pay', 'owner')`,
		}
		for i, query := range cases {
			err := tx.Transaction(func(inner *gorm.DB) error { return inner.Exec(query, project.ProjectID).Error })
			assert.Error(t, err, "case %d must be refused", i)
		}
	})
}

// The migration is additive and its Down works.
func TestWorkflowOwnersMigrationUpAndDown(t *testing.T) {
	baseURI := os.Getenv("TEST_DB_URI")
	require.NotEmpty(t, baseURI)
	parsedURI, err := url.Parse(baseURI)
	require.NoError(t, err)
	databaseName := fmt.Sprintf("workflow_owners_migrate_%d", time.Now().UnixNano())
	adminDB, err := sql.Open("postgres", baseURI)
	require.NoError(t, err)
	_, err = adminDB.Exec(fmt.Sprintf(`CREATE DATABASE %q`, databaseName))
	require.NoError(t, err)
	scratchURI := *parsedURI
	scratchURI.Path = "/" + databaseName
	db, err := sql.Open("postgres", scratchURI.String())
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = db.Close()
		_, _ = adminDB.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS %q`, databaseName))
		_ = adminDB.Close()
	})
	goose.SetBaseFS(coredb.Migrations)
	require.NoError(t, goose.SetDialect("postgres"))
	require.NoError(t, goose.UpTo(db, "migrations", 52))

	var orgID, projectID string
	require.NoError(t, db.QueryRow(`INSERT INTO organizations (name) VALUES ('wo-mig') RETURNING org_id`).Scan(&orgID))
	require.NoError(t, db.QueryRow(`INSERT INTO projects (name, repo_url, org_id) VALUES ('app', 'github.com/wo/mig', $1) RETURNING project_id`, orgID).Scan(&projectID))
	var workflowID string
	require.NoError(t, db.QueryRow(`INSERT INTO workflow_instances (org_id, project_id, name, workflow_security_id) VALUES ($1, $2, 'Old', 'old') RETURNING workflow_id`,
		orgID, projectID).Scan(&workflowID))
	_, err = db.Exec(`INSERT INTO role_assignments (principal_type, principal_id, scope_type, scope_id, role) VALUES ('group', generate_ulid(), 'project', $1, 'owner')`, projectID)
	require.NoError(t, err)

	require.NoError(t, goose.UpTo(db, "migrations", 53))
	var owner sql.NullString
	require.NoError(t, db.QueryRow(`SELECT owner_group FROM workflow_instances WHERE workflow_id = $1`, workflowID).Scan(&owner))
	assert.False(t, owner.Valid, "nothing is backfilled: an old row is unknown")
	_, err = db.Exec(`INSERT INTO role_assignments (principal_type, principal_id, scope_type, scope_id, scope_workflow, role) VALUES ('group', generate_ulid(), 'workflow', $1, 'pay', 'owner')`, projectID)
	require.NoError(t, err)

	require.NoError(t, goose.DownTo(db, "migrations", 52))
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM role_assignments`).Scan(&count))
	assert.Equal(t, 1, count, "Down removes only the workflow roles")
	_, err = db.Exec(`INSERT INTO role_assignments (principal_type, principal_id, scope_type, scope_id, role) VALUES ('group', generate_ulid(), 'workflow', $1, 'owner')`, projectID)
	assert.Error(t, err, "after Down the workflow scope is refused again")
}
