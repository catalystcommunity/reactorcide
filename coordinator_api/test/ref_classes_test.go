package test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobcontrol"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/refclass"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/catalystcommunity/reactorcide/coredb"
)

const refClassesMigrationVersion = 49

func newRefClassesMigrationDB(t *testing.T) *sql.DB {
	t.Helper()
	baseURI := os.Getenv("TEST_DB_URI")
	require.NotEmpty(t, baseURI)
	parsedURI, err := url.Parse(baseURI)
	require.NoError(t, err)
	databaseName := fmt.Sprintf("ref_classes_migrate_%d", time.Now().UnixNano())
	adminDB, err := sql.Open("postgres", baseURI)
	require.NoError(t, err)
	_, err = adminDB.Exec(fmt.Sprintf(`CREATE DATABASE %q`, databaseName))
	require.NoError(t, err)
	scratchURI := *parsedURI
	scratchURI.Path = "/" + databaseName
	scratchDB, err := sql.Open("postgres", scratchURI.String())
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = scratchDB.Close()
		_, _ = adminDB.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS %q`, databaseName))
		_ = adminDB.Close()
	})
	goose.SetBaseFS(coredb.Migrations)
	require.NoError(t, goose.SetDialect("postgres"))
	require.NoError(t, goose.UpTo(scratchDB, "migrations", refClassesMigrationVersion-1))
	return scratchDB
}

func classesOf(t *testing.T, db *sql.DB, query string, args ...any) []string {
	t.Helper()
	var value pq.StringArray
	require.NoError(t, db.QueryRow(query, args...).Scan(&value), query)
	return []string(value)
}

func TestRefClassesMigrationBackfillAndDown(t *testing.T) {
	db := newRefClassesMigrationDB(t)
	var orgID, projectID string
	require.NoError(t, db.QueryRow(`INSERT INTO organizations (name) VALUES ('rc-acme') RETURNING org_id`).Scan(&orgID))
	require.NoError(t, db.QueryRow(`INSERT INTO projects (name, repo_url, org_id) VALUES ('app', 'github.com/acme/app', $1)
		RETURNING project_id`, orgID).Scan(&projectID))
	insertEvent := func(source, eventType string, pr any) string {
		var id string
		require.NoError(t, db.QueryRow(`INSERT INTO ci_events (org_id, project_id, source, provider, event_type, branch, pr_number)
			VALUES ($1, $2, $3, 'github', $4, 'main', $5) RETURNING event_id`, orgID, projectID, source, eventType, pr).Scan(&id))
		return id
	}
	push := insertEvent("webhook", "push", nil)
	tag := insertEvent("webhook", "tag_created", nil)
	samePR := insertEvent("webhook", "pull_request_opened", 1)
	forkPR := insertEvent("webhook", "pull_request_updated", 2)
	schedule := insertEvent("schedule", "schedule", nil)
	manual := insertEvent("manual", "push", nil)
	insertJob := func(eventID any, env string) string {
		var id string
		require.NoError(t, db.QueryRow(`INSERT INTO jobs (org_id, project_id, name, job_command, event_id, job_env_vars)
			VALUES ($1, $2, 'eval', 'x', $3, $4::jsonb) RETURNING job_id`, orgID, projectID, eventID, env).Scan(&id))
		return id
	}
	forkEval := insertJob(forkPR, `{"REACTORCIDE_IS_FORK_PR": "true"}`)
	sameEval := insertJob(samePR, `{}`)
	looseJob := insertJob(nil, `{}`)
	var workflowID string
	require.NoError(t, db.QueryRow(`INSERT INTO workflow_instances (org_id, project_id, name, workflow_security_id, event_id)
		VALUES ($1, $2, 'ci', 'ci', $3) RETURNING workflow_id`, orgID, projectID, forkPR).Scan(&workflowID))
	var nodeJob string
	require.NoError(t, db.QueryRow(`INSERT INTO jobs (org_id, project_id, name, job_command, workflow_id)
		VALUES ($1, $2, 'build', 'x', $3) RETURNING job_id`, orgID, projectID, workflowID).Scan(&nodeJob))

	require.NoError(t, goose.UpTo(db, "migrations", refClassesMigrationVersion))

	assert.Equal(t, []string{}, classesOf(t, db, `SELECT ref_classes FROM ci_events WHERE event_id = $1`, push),
		"the default branch of an old push is not known")
	assert.Equal(t, []string{"tag"}, classesOf(t, db, `SELECT ref_classes FROM ci_events WHERE event_id = $1`, tag))
	assert.Equal(t, []string{"pr"}, classesOf(t, db, `SELECT ref_classes FROM ci_events WHERE event_id = $1`, samePR))
	assert.Equal(t, []string{"fork", "pr"}, classesOf(t, db, `SELECT ref_classes FROM ci_events WHERE event_id = $1`, forkPR))
	assert.Equal(t, []string{"schedule"}, classesOf(t, db, `SELECT ref_classes FROM ci_events WHERE event_id = $1`, schedule))
	assert.Equal(t, []string{"manual"}, classesOf(t, db, `SELECT ref_classes FROM ci_events WHERE event_id = $1`, manual))
	assert.Equal(t, []string{"fork", "pr"}, classesOf(t, db, `SELECT ref_classes FROM jobs WHERE job_id = $1`, forkEval))
	assert.Equal(t, []string{"pr"}, classesOf(t, db, `SELECT ref_classes FROM jobs WHERE job_id = $1`, sameEval))
	assert.Equal(t, []string{}, classesOf(t, db, `SELECT ref_classes FROM jobs WHERE job_id = $1`, looseJob))
	assert.Equal(t, []string{"fork", "pr"}, classesOf(t, db, `SELECT ref_classes FROM workflow_instances WHERE workflow_id = $1`, workflowID))
	assert.Equal(t, []string{"fork", "pr"}, classesOf(t, db, `SELECT ref_classes FROM jobs WHERE job_id = $1`, nodeJob),
		"a workflow job inherits from its workflow")

	// The triggers: a new child job inherits; an update with NULL keeps.
	var child string
	require.NoError(t, db.QueryRow(`INSERT INTO jobs (org_id, project_id, name, job_command, parent_job_id)
		VALUES ($1, $2, 'child', 'x', $3) RETURNING job_id`, orgID, projectID, forkEval).Scan(&child))
	assert.Equal(t, []string{"fork", "pr"}, classesOf(t, db, `SELECT ref_classes FROM jobs WHERE job_id = $1`, child))
	// Classes are immutable after insert: an update with NULL, '{}', or
	// other classes keeps the stored classes.
	for _, value := range []string{`NULL`, `'{}'`, `ARRAY['production']`} {
		_, err := db.Exec(`UPDATE jobs SET ref_classes = `+value+` WHERE job_id = $1`, child)
		require.NoError(t, err)
		assert.Equal(t, []string{"fork", "pr"}, classesOf(t, db, `SELECT ref_classes FROM jobs WHERE job_id = $1`, child), value)
		_, err = db.Exec(`UPDATE workflow_instances SET ref_classes = `+value+` WHERE workflow_id = $1`, workflowID)
		require.NoError(t, err)
		assert.Equal(t, []string{"fork", "pr"}, classesOf(t, db, `SELECT ref_classes FROM workflow_instances WHERE workflow_id = $1`, workflowID), value)
		_, err = db.Exec(`UPDATE ci_events SET ref_classes = `+value+`, ref_class_reasons = '[]' WHERE event_id = $1`, forkPR)
		require.NoError(t, err)
		assert.Equal(t, []string{"fork", "pr"}, classesOf(t, db, `SELECT ref_classes FROM ci_events WHERE event_id = $1`, forkPR), value)
	}

	require.NoError(t, goose.DownTo(db, "migrations", refClassesMigrationVersion-1))
	assert.Zero(t, count(t, db, `SELECT count(*) FROM information_schema.columns WHERE column_name IN ('ref_classes', 'ref_classes_any', 'ref_classes_none', 'ref_class_facts')`))
	assert.Zero(t, count(t, db, `SELECT count(*) FROM information_schema.tables WHERE table_name = 'ref_class_rules'`))
	// The rows stay usable after Down.
	var after string
	require.NoError(t, db.QueryRow(`INSERT INTO jobs (org_id, project_id, name, job_command, parent_job_id)
		VALUES ($1, $2, 'after', 'x', $3) RETURNING job_id`, orgID, projectID, forkEval).Scan(&after))
}

func TestRefClassRuleStore(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		ps := postgres_store.PostgresStore
		org := newPrincipalOrg(t, ctx, "rc-rules")
		project := &models.Project{Name: "rc-app", RepoURL: "github.com/rc/app", OrgID: org.OrgID}
		require.NoError(t, ps.CreateProject(ctx, project))
		yes := true
		require.NoError(t, ps.UpsertRefClassRule(ctx, &models.RefClassRule{OrgID: org.OrgID, Name: "production", Position: 1,
			EventTypes: []string{"push"}, IsDefaultBranch: &yes, Classes: []string{"production"}, Enabled: true}))
		projectRule := &models.RefClassRule{OrgID: org.OrgID, ProjectID: &project.ProjectID, Name: "release",
			BranchGlob: "release/*", Classes: []string{"release"}, Enabled: true}
		require.NoError(t, ps.UpsertRefClassRule(ctx, projectRule))
		require.NotEmpty(t, projectRule.RuleID)

		// The same name in another scope is another rule.
		require.NoError(t, ps.UpsertRefClassRule(ctx, &models.RefClassRule{OrgID: org.OrgID, Name: "release", TagGlob: "v*",
			Classes: []string{"release"}, Enabled: true, Position: 5}))

		// Replace keeps the rule ID.
		firstID := projectRule.RuleID
		projectRule.Classes = []string{"release", "candidate"}
		require.NoError(t, ps.UpsertRefClassRule(ctx, projectRule))
		assert.Equal(t, firstID, projectRule.RuleID)
		assert.Equal(t, []string{"candidate", "release"}, []string(projectRule.Classes))

		orgRules, err := ps.ListRefClassRules(ctx, org.OrgID, nil)
		require.NoError(t, err)
		require.Len(t, orgRules, 2)
		effective, err := ps.ListEffectiveRefClassRules(ctx, org.OrgID, &project.ProjectID)
		require.NoError(t, err)
		require.Len(t, effective, 3)
		assert.Nil(t, effective[0].ProjectID)
		assert.Nil(t, effective[1].ProjectID)
		assert.NotNil(t, effective[2].ProjectID, "project rules come after the organization rules")

		result := refclass.Classify(models.RefClassFacts{Source: "webhook", EventType: "push", Branch: "release/2"}, effective)
		assert.Equal(t, []string{"candidate", "release"}, result.Classes)

		err = ps.UpsertRefClassRule(ctx, &models.RefClassRule{OrgID: org.OrgID, Name: "Bad Name", Classes: []string{"x"}})
		assert.ErrorIs(t, err, store.ErrInvalidInput)

		require.NoError(t, ps.DeleteRefClassRule(ctx, org.OrgID, &project.ProjectID, "release"))
		assert.ErrorIs(t, ps.DeleteRefClassRule(ctx, org.OrgID, &project.ProjectID, "release"), store.ErrNotFound)
		_, err = ps.GetRefClassRule(ctx, org.OrgID, nil, "release")
		assert.NoError(t, err, "the organization rule with the same name stays")
	})
}

// Classes flow from the event to the eval job, the workflow, its jobs,
// child jobs, and retries, and a save of a struct without classes keeps them.
func TestRefClassesInheritance(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		ps := postgres_store.PostgresStore
		org := newPrincipalOrg(t, ctx, "rc-inherit")
		project := &models.Project{Name: "rc-inherit-app", RepoURL: "github.com/rc/inherit", OrgID: org.OrgID}
		require.NoError(t, ps.CreateProject(ctx, project))
		key := "rc-inherit-delivery"
		event, _, err := ps.RecordCIEvent(ctx, &models.CIEvent{OrgID: org.OrgID, ProjectID: &project.ProjectID, Source: "webhook",
			Provider: "github", EventType: "pull_request_opened", Repo: "rc/inherit", IdempotencyKey: &key, SemanticKey: key,
			RefClasses: []string{"fork", "pr"}, RefClassFacts: &models.RefClassFacts{EventType: "pull_request_opened", IsPR: true, IsFork: true},
			RefClassReasons: []models.RefClassReason{{Class: "pr", RuleID: "builtin:pull-request", Scope: "builtin"}}})
		require.NoError(t, err)
		stored, err := ps.GetCIEvent(ctx, event.EventID)
		require.NoError(t, err)
		assert.Equal(t, []string{"fork", "pr"}, []string(stored.RefClasses))
		require.NotNil(t, stored.RefClassFacts)
		assert.True(t, stored.RefClassFacts.IsFork)

		eval := &models.Job{OrgID: org.OrgID, ProjectID: &project.ProjectID, Name: "eval", JobCommand: "x", EventID: &event.EventID,
			Status: "completed"}
		require.NoError(t, ps.CreateJob(ctx, eval))
		assert.Equal(t, []string{"fork", "pr"}, []string(eval.RefClasses), "the eval job inherits from its event")

		wf := &models.WorkflowInstance{OrgID: org.OrgID, ProjectID: &project.ProjectID, Name: "ci", WorkflowSecurityID: "ci",
			ParentJobID: &eval.JobID, EventID: &event.EventID}
		require.NoError(t, ps.CreateWorkflowInstance(ctx, wf))
		assert.Equal(t, []string{"fork", "pr"}, []string(wf.RefClasses))

		node := &models.Job{OrgID: org.OrgID, ProjectID: &project.ProjectID, Name: "build", JobCommand: "x",
			WorkflowID: &wf.WorkflowID, ParentJobID: &eval.JobID, Status: "failed"}
		require.NoError(t, ps.CreateJob(ctx, node))
		assert.Equal(t, []string{"fork", "pr"}, []string(node.RefClasses))

		// A job struct that never read its classes does not erase them.
		blank := *node
		blank.RefClasses = nil
		blank.Notes = "changed"
		require.NoError(t, ps.UpdateJob(ctx, &blank))
		reloaded, err := ps.GetJobByID(ctx, node.JobID)
		require.NoError(t, err)
		assert.Equal(t, []string{"fork", "pr"}, []string(reloaded.RefClasses))

		// A trigger child (a job of a job) inherits from the parent.
		child := &models.Job{OrgID: org.OrgID, ProjectID: &project.ProjectID, Name: "child", JobCommand: "x", ParentJobID: &node.JobID}
		require.NoError(t, ps.CreateJob(ctx, child))
		assert.Equal(t, []string{"fork", "pr"}, []string(child.RefClasses))

		// A retry keeps the classes of the original.
		retried, err := jobcontrol.RetryJob(ctx, ps, nil, reloaded)
		require.NoError(t, err)
		retriedStored, err := ps.GetJobByID(ctx, retried.JobID)
		require.NoError(t, err)
		assert.Equal(t, []string{"fork", "pr"}, []string(retriedStored.RefClasses))
		env := worker.BuildJobEnv(retriedStored)
		assert.Equal(t, "fork,pr", env["REACTORCIDE_REF_CLASSES"])

		// Workflow summaries have the classes.
		summary, err := ps.GetWorkflowSummary(ctx, wf.WorkflowID)
		require.NoError(t, err)
		assert.Equal(t, []string{"fork", "pr"}, []string(summary.RefClasses))
		visible, _, err := ps.ListWorkflowSummariesVisibleTo(ctx, "", true, map[string]interface{}{}, 50, 0)
		require.NoError(t, err)
		found := false
		for _, row := range visible {
			if row.WorkflowID == wf.WorkflowID {
				found = true
				assert.Equal(t, []string{"fork", "pr"}, []string(row.RefClasses))
			}
		}
		assert.True(t, found)
	})
}

// The no-login flow: the admin token manages org and project rules and
// explains an event through REST.
func TestRefClassRulesAPI(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		du := &DataUtils{db: tx}
		admin, err := du.CreateUser(DataSetup{"Username": "rc-admin", "Email": "rc@example.com"})
		require.NoError(t, err)
		auth, err := createTokenAuthHeader(ctx, tx, admin.UserID)
		require.NoError(t, err)
		org := newPrincipalOrg(t, ctx, "rc-api")
		project := &models.Project{Name: "rc-api-app", RepoURL: "github.com/rc/api", OrgID: org.OrgID}
		require.NoError(t, postgres_store.PostgresStore.CreateProject(ctx, project))

		rr := principalRequest(t, ctx, http.MethodPut, "/api/v1/organizations/rc-api/ref-class-rules/production", auth,
			map[string]any{"classes": []string{"production"}, "event_types": []string{"push"}, "is_default_branch": true})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodPut, "/api/v1/projects/"+project.ProjectID+"/ref-class-rules/hotfix", auth,
			map[string]any{"classes": []string{"hotfix"}, "branch_glob": "hotfix/*"})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodPut, "/api/v1/projects/rc-api-app/ref-class-rules/bad", auth,
			map[string]any{"classes": []string{"Not Valid"}})
		require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())

		rr = principalRequest(t, ctx, http.MethodGet, "/api/v1/projects/rc-api-app/ref-class-rules", auth, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		var listed struct {
			Rules     []models.RefClassRule `json:"rules"`
			Inherited []models.RefClassRule `json:"inherited"`
			Builtin   map[string][]string   `json:"builtin"`
		}
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &listed))
		require.Len(t, listed.Rules, 1)
		require.Len(t, listed.Inherited, 1)
		assert.Equal(t, []string{"default"}, listed.Builtin["builtin:default-branch"])

		rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/projects/"+project.ProjectID+"/ref-classes/explain", auth,
			map[string]any{"facts": map[string]any{"source": "webhook", "event_type": "push", "branch": "main", "default_branch": "main"}})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		var explained refclass.EventExplanation
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &explained))
		assert.Equal(t, []string{"default", "production"}, explained.Classes)

		// A manual submit on main gets "manual", never "production".
		rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/organizations/rc-api/ref-classes/explain", auth,
			map[string]any{"facts": map[string]any{"source": "manual", "event_type": "push", "branch": "main", "default_branch": "main"}})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &explained))
		assert.Equal(t, []string{"manual"}, explained.Classes)

		rr = principalRequest(t, ctx, http.MethodDelete, "/api/v1/projects/"+project.ProjectID+"/ref-class-rules/hotfix", auth, nil)
		require.Equal(t, http.StatusNoContent, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodDelete, "/api/v1/projects/"+project.ProjectID+"/ref-class-rules/hotfix", auth, nil)
		require.Equal(t, http.StatusNotFound, rr.Code, rr.Body.String())

		// No token: refused.
		rr = principalRequest(t, ctx, http.MethodGet, "/api/v1/organizations/rc-api/ref-class-rules", "", nil)
		assert.Equal(t, http.StatusUnauthorized, rr.Code, rr.Body.String())
	})
}

// A secret grant with ref class conditions persists them, and the worker
// authorization applies them to the stored job.
func TestSecretGrantRefClassConditionsStore(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		ps := postgres_store.PostgresStore
		org := newPrincipalOrg(t, ctx, "rc-grants")
		project := &models.Project{Name: "rc-grants-app", RepoURL: "github.com/rc/grants", OrgID: org.OrgID}
		require.NoError(t, ps.CreateProject(ctx, project))
		grant := &models.SecretGrant{Name: "deploy", UserID: org.OrgID, ProjectID: &project.ProjectID,
			SecretPathMatch: models.SecretGrantMatchPrefix, SecretPathPattern: "deploy/prod", JobNameMatch: models.SecretGrantMatchAny,
			RefClassesAny: []string{"production"}, RefClassesNone: []string{"fork"}}
		require.NoError(t, ps.CreateSecretGrant(ctx, grant))
		loaded, err := ps.GetSecretGrantByID(ctx, grant.GrantID)
		require.NoError(t, err)
		assert.Equal(t, []string{"production"}, []string(loaded.RefClassesAny))
		assert.Equal(t, []string{"fork"}, []string(loaded.RefClassesNone))

		prodJob := &models.Job{OrgID: org.OrgID, ProjectID: &project.ProjectID, Name: "deploy", JobCommand: "x",
			RefClasses: []string{"default", "production"}}
		require.NoError(t, ps.CreateJob(ctx, prodJob))
		prJob := &models.Job{OrgID: org.OrgID, ProjectID: &project.ProjectID, Name: "deploy", JobCommand: "x",
			RefClasses: []string{"pr"}}
		require.NoError(t, ps.CreateJob(ctx, prJob))
		for _, tc := range []struct {
			job     *models.Job
			allowed bool
		}{{prodJob, true}, {prJob, false}} {
			job, err := ps.GetJobByID(ctx, tc.job.JobID)
			require.NoError(t, err)
			err = worker.AuthorizeSecretAccess(ctx, ps, job, "deploy/prod", "token")
			assert.Equal(t, tc.allowed, err == nil, "job %v: %v", job.RefClasses, err)
		}
	})
}
