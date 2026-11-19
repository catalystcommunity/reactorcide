package test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/requiredset"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/catalystcommunity/reactorcide/coredb"
)

// requiredSetProject makes an organization, a project, two root workflows,
// and one eval decision of a third workflow.
func requiredSetProject(t *testing.T, ctx context.Context, tx *gorm.DB, name string) (*models.Organization, *models.Project, *models.CIEvent) {
	t.Helper()
	org := newPrincipalOrg(t, ctx, name)
	project := &models.Project{Name: name + "-app", RepoURL: "github.com/" + name + "/app", OrgID: org.OrgID}
	require.NoError(t, postgres_store.PostgresStore.CreateProject(ctx, project))
	projectID := project.ProjectID
	event := &models.CIEvent{OrgID: org.OrgID, ProjectID: &projectID, Source: models.CIEventSourceWebhook, Provider: "github",
		EventType: "push", Repo: name + "/app", Branch: "main", CommitSHA: "abc123", Status: models.CIEventStatusEvaluated}
	require.NoError(t, tx.Create(event).Error)
	eventID := event.EventID
	for _, id := range []string{"build", "test"} {
		require.NoError(t, tx.Create(&models.WorkflowInstance{OrgID: org.OrgID, ProjectID: &projectID, Name: id,
			WorkflowSecurityID: id, Status: "success", EventID: &eventID}).Error)
	}
	require.NoError(t, tx.Create(&models.CIEventWorkflowDecision{EventID: eventID, WorkflowSecurityID: "deploy", WorkflowName: "Deploy",
		Decision: models.WorkflowDecisionNotAdmitted, Reasons: []models.DecisionReason{}}).Error)
	require.NoError(t, tx.Create(&models.CIEventWorkflowDecision{EventID: eventID, WorkflowSecurityID: "file:.reactorcide/workflows/bad.yaml",
		Decision: models.WorkflowDecisionInvalid, Reasons: []models.DecisionReason{}}).Error)
	return org, project, event
}

func TestRequiredSetStore(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		ps := postgres_store.PostgresStore
		org, project, event := requiredSetProject(t, ctx, tx, "rs-store")

		settings, err := ps.GetProjectRequiredSetSettings(ctx, project.ProjectID)
		require.NoError(t, err)
		assert.False(t, settings.Enabled, "the set is off with no row")
		orgSettings, err := ps.GetOrgRequiredSetSettings(ctx, org.OrgID)
		require.NoError(t, err)
		assert.Equal(t, 30, orgSettings.MaxQuarantineDays)

		known, err := ps.ListKnownWorkflows(ctx, project.ProjectID)
		require.NoError(t, err)
		ids := []string{}
		for _, item := range known {
			ids = append(ids, item.WorkflowSecurityID)
		}
		assert.Equal(t, []string{"build", "deploy", "test"}, ids, "invalid-file keys are not workflows")

		// Turning the set on seeds every known workflow, so nothing changes.
		seeded, err := ps.SetRequiredSetEnabled(ctx, project.ProjectID, org.OrgID, true, nil)
		require.NoError(t, err)
		sort.Strings(seeded)
		assert.Equal(t, []string{"build", "deploy", "test"}, seeded)
		set := requiredset.Load(ctx, ps, project.ProjectID)
		require.True(t, set.Enabled)
		classifier := requiredset.ForEvent(ctx, ps, event)
		for _, id := range []string{"build", "test", "deploy"} {
			assert.True(t, classifier.Classify(id).Blocking(), id)
		}
		assert.False(t, classifier.Classify("lint").Blocking(), "a new workflow is advisory")

		// A promotion: one pending proposal per change.
		proposal := &models.RequiredSetProposal{ProjectID: project.ProjectID, OrgID: org.OrgID, WorkflowSecurityID: "lint",
			Action: models.RequiredSetActionPromote, Reason: "stable now"}
		require.NoError(t, ps.CreateRequiredSetProposal(ctx, proposal))
		assert.ErrorIs(t, ps.CreateRequiredSetProposal(ctx, &models.RequiredSetProposal{ProjectID: project.ProjectID, OrgID: org.OrgID,
			WorkflowSecurityID: "lint", Action: models.RequiredSetActionPromote}), store.ErrAlreadyExists)
		decided, err := ps.DecideRequiredSetProposal(ctx, proposal.ProposalID, models.RequiredSetProposalApproved, nil, "ok")
		require.NoError(t, err)
		assert.Equal(t, models.RequiredSetProposalApproved, decided.State)
		_, err = ps.DecideRequiredSetProposal(ctx, proposal.ProposalID, models.RequiredSetProposalRejected, nil, "")
		assert.ErrorIs(t, err, store.ErrConflict)
		assert.True(t, requiredset.ForEvent(ctx, ps, event).Classify("lint").Blocking())

		// Re-enable keeps entries and adds only new ones.
		seeded, err = ps.SetRequiredSetEnabled(ctx, project.ProjectID, org.OrgID, true, nil)
		require.NoError(t, err)
		assert.Empty(t, seeded)

		// Quarantine, expiry at read time, and clear.
		until := time.Now().UTC().Add(time.Hour)
		require.NoError(t, ps.SetRequiredWorkflowQuarantine(ctx, project.ProjectID, "build", "", &until, "flaky", nil))
		got := requiredset.ForEvent(ctx, ps, event).Classify("build")
		assert.Equal(t, requiredset.StateQuarantined, got.State)
		past := time.Now().UTC().Add(-time.Minute)
		require.NoError(t, ps.SetRequiredWorkflowQuarantine(ctx, project.ProjectID, "build", "", &past, "flaky", nil))
		assert.True(t, requiredset.ForEvent(ctx, ps, event).Classify("build").Blocking(), "an expired quarantine restores the entry")
		require.NoError(t, ps.SetRequiredWorkflowQuarantine(ctx, project.ProjectID, "build", "", nil, "", nil))
		assert.ErrorIs(t, ps.SetRequiredWorkflowQuarantine(ctx, project.ProjectID, "missing", "", &until, "x", nil), store.ErrNotFound)

		// A demotion removes the entry.
		demote := &models.RequiredSetProposal{ProjectID: project.ProjectID, OrgID: org.OrgID, WorkflowSecurityID: "test",
			Action: models.RequiredSetActionDemote}
		require.NoError(t, ps.CreateRequiredSetProposal(ctx, demote))
		_, err = ps.DecideRequiredSetProposal(ctx, demote.ProposalID, models.RequiredSetProposalApproved, nil, "")
		require.NoError(t, err)
		assert.False(t, requiredset.ForEvent(ctx, ps, event).Classify("test").Blocking())

		// Approver settings: explicit values, then inherit (NULL).
		rules := []models.RequiredSetApproverRule{{Branches: []string{"release/*"}, Approvers: []string{"reactorcide_group:release"}}}
		require.NoError(t, ps.SetProjectRequiredSetApprovers(ctx, project.ProjectID, org.OrgID, []string{"reactorcide_group:ci"}, &rules))
		settings, err = ps.GetProjectRequiredSetSettings(ctx, project.ProjectID)
		require.NoError(t, err)
		assert.True(t, settings.Enabled, "approver changes keep the state")
		assert.Equal(t, []string{"reactorcide_group:ci"}, []string(settings.Approvers))
		require.NotNil(t, settings.ApproverRules)
		assert.Equal(t, rules, *settings.ApproverRules)
		require.NoError(t, ps.SetProjectRequiredSetApprovers(ctx, project.ProjectID, org.OrgID, nil, nil))
		settings, err = ps.GetProjectRequiredSetSettings(ctx, project.ProjectID)
		require.NoError(t, err)
		assert.Nil(t, settings.Approvers)
		assert.Nil(t, settings.ApproverRules)

		require.NoError(t, ps.UpsertOrgRequiredSetSettings(ctx, &models.OrgRequiredSetSettings{OrgID: org.OrgID,
			Approvers: []string{"reactorcide_group:owners"}, ApproverRules: rules, MaxQuarantineDays: 14}))
		orgSettings, err = ps.GetOrgRequiredSetSettings(ctx, org.OrgID)
		require.NoError(t, err)
		assert.Equal(t, 14, orgSettings.MaxQuarantineDays)
		assert.Equal(t, rules, orgSettings.ApproverRules)

		// Off: every workflow blocks again.
		_, err = ps.SetRequiredSetEnabled(ctx, project.ProjectID, org.OrgID, false, nil)
		require.NoError(t, err)
		assert.True(t, requiredset.ForEvent(ctx, ps, event).Classify("lint-new").Blocking())
	})
}

func TestRequiredSetMigrationDown(t *testing.T) {
	baseURI := os.Getenv("TEST_DB_URI")
	require.NotEmpty(t, baseURI)
	parsed, err := url.Parse(baseURI)
	require.NoError(t, err)
	name := fmt.Sprintf("required_set_migrate_%d", time.Now().UnixNano())
	admin, err := sql.Open("postgres", baseURI)
	require.NoError(t, err)
	_, err = admin.Exec(fmt.Sprintf(`CREATE DATABASE %q`, name))
	require.NoError(t, err)
	scratch := *parsed
	scratch.Path = "/" + name
	db, err := sql.Open("postgres", scratch.String())
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = db.Close()
		_, _ = admin.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS %q`, name))
		_ = admin.Close()
	})
	goose.SetBaseFS(coredb.Migrations)
	require.NoError(t, goose.SetDialect("postgres"))
	require.NoError(t, goose.UpTo(db, "migrations", 52))
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_name LIKE '%required%'`).Scan(&count))
	assert.Equal(t, 4, count)
	require.NoError(t, goose.DownTo(db, "migrations", 51))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_name LIKE '%required%'`).Scan(&count))
	assert.Equal(t, 0, count)
}

func requiredSetAudits(t *testing.T, tx *gorm.DB, projectID string) []string {
	t.Helper()
	var events []models.AuditEvent
	require.NoError(t, tx.Where("action LIKE 'required_set.%' AND details->>'project_id' = ?", projectID).
		Order("created_at, audit_event_id").Find(&events).Error)
	actions := []string{}
	for _, event := range events {
		actions = append(actions, event.Action)
	}
	return actions
}

// The no-login flow through REST with the admin token, then the principal
// rules: a member proposes, only an approver approves.
func TestRequiredSetAPI(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		du := &DataUtils{db: tx}
		adminUser, err := du.CreateUser(DataSetup{"Username": "rs-admin", "Email": "rs@example.com"})
		require.NoError(t, err)
		adminAuth, err := createTokenAuthHeader(ctx, tx, adminUser.UserID)
		require.NoError(t, err)
		org, project, event := requiredSetProject(t, ctx, tx, "rs-api")
		base := "/api/v1/projects/" + project.ProjectID + "/required-set"

		rr := principalRequest(t, ctx, http.MethodGet, base, adminAuth, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		var view struct {
			Enabled   bool `json:"enabled"`
			Workflows []struct {
				WorkflowSecurityID string     `json:"workflow_security_id"`
				State              string     `json:"state"`
				InSet              bool       `json:"in_set"`
				QuarantinedUntil   *time.Time `json:"quarantined_until"`
			} `json:"workflows"`
			Permissions struct {
				Manage bool `json:"manage"`
			} `json:"permissions"`
		}
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &view))
		assert.False(t, view.Enabled)
		assert.True(t, view.Permissions.Manage)
		for _, row := range view.Workflows {
			assert.Equal(t, "required", row.State, "set off: everything is required")
		}

		// A legacy org with no stored CI policy runs the head's own CI for a
		// pull request, which can remove a required workflow: refused.
		require.NoError(t, tx.Exec(`UPDATE organizations SET defaults_profile = 'legacy' WHERE org_id = ?`, org.OrgID).Error)
		rr = principalRequest(t, ctx, http.MethodPost, base+"/enable", adminAuth, nil)
		require.Equal(t, http.StatusConflict, rr.Code, rr.Body.String())
		assert.Contains(t, rr.Body.String(), "trusted CI definitions")
		require.NoError(t, tx.Exec(`UPDATE organizations SET defaults_profile = 'secure' WHERE org_id = ?`, org.OrgID).Error)

		rr = principalRequest(t, ctx, http.MethodPost, base+"/enable", adminAuth, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		assert.Contains(t, rr.Body.String(), "deploy")

		// Propose and approve in one step (the admin token is an approver).
		rr = principalRequest(t, ctx, http.MethodPost, base+"/proposals", adminAuth,
			map[string]any{"action": "promote", "workflow": "lint", "reason": "ready", "approve": true})
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		assert.Contains(t, rr.Body.String(), `"state":"approved"`)
		assert.True(t, requiredset.ForEvent(ctx, postgres_store.PostgresStore, event).Classify("lint").Blocking())

		// A quarantine needs an expiry within the maximum.
		rr = principalRequest(t, ctx, http.MethodPost, base+"/quarantine", adminAuth, map[string]any{"workflow": "build", "reason": "flaky"})
		assert.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodPost, base+"/quarantine", adminAuth, map[string]any{"workflow": "build", "reason": "flaky", "duration": "31d"})
		assert.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodPost, base+"/quarantine", adminAuth, map[string]any{"workflow": "build", "reason": "flaky", "duration": "2d"})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		assert.False(t, requiredset.ForEvent(ctx, postgres_store.PostgresStore, event).Classify("build").Blocking())
		rr = principalRequest(t, ctx, http.MethodGet, base, adminAuth, nil)
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &view))
		for _, row := range view.Workflows {
			if row.WorkflowSecurityID == "build" {
				assert.Equal(t, "quarantined", row.State)
				require.NotNil(t, row.QuarantinedUntil, "the expiry is shown")
			}
		}
		rr = principalRequest(t, ctx, http.MethodPost, base+"/unquarantine", adminAuth, map[string]any{"workflow": "build"})
		require.Equal(t, http.StatusNoContent, rr.Code, rr.Body.String())

		// A removal is a demotion proposal with the same approvers.
		rr = principalRequest(t, ctx, http.MethodPost, base+"/proposals", adminAuth, map[string]any{"action": "demote", "workflow": "test"})
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		var proposal models.RequiredSetProposal
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &proposal))
		assert.Equal(t, models.RequiredSetProposalPending, proposal.State)
		rr = principalRequest(t, ctx, http.MethodPost, base+"/proposals/"+proposal.ProposalID+"/approve", adminAuth, nil)
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodPost, base+"/proposals/"+proposal.ProposalID+"/approve", adminAuth, nil)
		assert.Equal(t, http.StatusConflict, rr.Code, rr.Body.String())

		// Principals: a service principal with a member role proposes; only
		// a member of the approver group approves.
		orgID := org.OrgID
		makeBotWith := func(name string, capabilities ...string) string {
			rr := principalRequest(t, ctx, http.MethodPost, "/api/v1/organizations/rs-api/principals", adminAuth, map[string]string{"name": name})
			require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
			rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/organizations/rs-api/principals/"+name+"/tokens", adminAuth,
				map[string]any{"name": "ci", "capabilities": capabilities})
			require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
			var issued struct {
				Token string `json:"token"`
			}
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &issued))
			rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/organizations/rs-api/principals/"+name+"/roles", adminAuth,
				map[string]string{"role": "member"})
			require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
			return "Bearer " + issued.Token
		}
		makeBot := func(name string) string {
			return makeBotWith(name, tokencaps.ProjectsRead, tokencaps.PoliciesManage)
		}
		proposer := makeBot("rs-proposer")
		approver := makeBot("rs-approver")
		// A token issued with read capabilities only: its group membership
		// does not let it propose or approve.
		reader := makeBotWith("rs-reader", tokencaps.ProjectsRead)
		group := &models.Group{OrgID: orgID, Name: "ci-owners"}
		require.NoError(t, postgres_store.PostgresStore.CreateGroup(ctx, group))
		rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/organizations/rs-api/principals/rs-approver/groups", adminAuth,
			map[string]string{"group": "ci-owners"})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodPut, "/api/v1/organizations/rs-api/required-set-settings", adminAuth,
			map[string]any{"approvers": []string{"reactorcide_group:ci-owners"}, "max_quarantine_days": 10})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())

		rr = principalRequest(t, ctx, http.MethodPost, "/api/v1/organizations/rs-api/principals/rs-reader/groups", adminAuth,
			map[string]string{"group": "ci-owners"})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodPost, base+"/proposals", reader, map[string]any{"workflow": "docs", "reason": "new"})
		assert.Equal(t, http.StatusForbidden, rr.Code, "a read-only token cannot propose: %s", rr.Body.String())

		rr = principalRequest(t, ctx, http.MethodPost, base+"/proposals", proposer, map[string]any{"workflow": "docs", "reason": "new"})
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &proposal))
		rr = principalRequest(t, ctx, http.MethodPost, base+"/proposals/"+proposal.ProposalID+"/approve", reader, nil)
		assert.Equal(t, http.StatusForbidden, rr.Code, "a read-only token in the approver group cannot approve: %s", rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodPost, base+"/proposals/"+proposal.ProposalID+"/approve", proposer, nil)
		assert.Equal(t, http.StatusForbidden, rr.Code, "a proposer outside the approver group cannot approve: %s", rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodPost, base+"/enable", proposer, nil)
		assert.Equal(t, http.StatusForbidden, rr.Code, "only an org admin turns the set on or off")
		rr = principalRequest(t, ctx, http.MethodPost, base+"/proposals/"+proposal.ProposalID+"/approve", approver, map[string]string{"reason": "ok"})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		assert.True(t, requiredset.ForEvent(ctx, postgres_store.PostgresStore, event).Classify("docs").Blocking())

		// A branch rule names other approvers for release branches.
		rr = principalRequest(t, ctx, http.MethodPut, base+"/approvers", adminAuth,
			map[string]any{"approvers": nil, "approver_rules": []map[string]any{{"branches": []string{"release/*"}, "approvers": []string{"reactorcide_group:release"}}}})
		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodPost, base+"/proposals", proposer, map[string]any{"workflow": "smoke", "branch_pattern": "release/*"})
		require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
		require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &proposal))
		rr = principalRequest(t, ctx, http.MethodPost, base+"/proposals/"+proposal.ProposalID+"/approve", approver, nil)
		assert.Equal(t, http.StatusForbidden, rr.Code, "ci-owners do not approve release/* entries: %s", rr.Body.String())
		rr = principalRequest(t, ctx, http.MethodPost, base+"/proposals/"+proposal.ProposalID+"/withdraw", proposer, nil)
		require.Equal(t, http.StatusOK, rr.Code, "the proposer withdraws: %s", rr.Body.String())

		// No token: refused.
		rr = principalRequest(t, ctx, http.MethodGet, base, "", nil)
		assert.Equal(t, http.StatusUnauthorized, rr.Code)

		want := []string{
			"required_set.enable",
			"required_set.propose", "required_set.approve", "required_set.promote",
			"required_set.quarantine", "required_set.unquarantine",
			"required_set.propose", "required_set.approve", "required_set.remove",
			"required_set.propose", "required_set.approve", "required_set.promote",
			"required_set.approvers",
			"required_set.propose", "required_set.withdraw",
		}
		got := requiredSetAudits(t, tx, project.ProjectID)
		sort.Strings(want)
		sort.Strings(got)
		assert.Equal(t, want, got)
	})
}
