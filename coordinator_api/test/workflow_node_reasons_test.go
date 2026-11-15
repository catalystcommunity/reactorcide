package test

// Postgres-backed coverage for workflow_nodes.decision_reasons (migration
// 000043): structured node reasons survive create, update and list.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/postgres_store"
)

func TestWorkflowNodeDecisionReasonsPersist(t *testing.T) {
	RunTransactionalTest(t, func(ctx context.Context, tx *gorm.DB) {
		du := &DataUtils{db: tx}
		user, err := du.CreateUser(DataSetup{})
		require.NoError(t, err)
		wf := &models.WorkflowInstance{UserID: user.UserID, Name: "pr", Status: "running", QueueName: "reactorcide-jobs"}
		require.NoError(t, postgres_store.PostgresStore.CreateWorkflowInstance(ctx, wf))

		no, yes := false, true
		count := 0
		node := &models.WorkflowNode{
			WorkflowID: wf.WorkflowID, Name: "docs", DisplayName: "docs", Status: "skipped",
			DecisionReasons: []models.DecisionReason{{Kind: "paths", Matched: &no, Include: []string{"docs/**"}, MatchedCount: &count}},
		}
		require.NoError(t, postgres_store.PostgresStore.CreateWorkflowNode(ctx, node))

		plain := &models.WorkflowNode{WorkflowID: wf.WorkflowID, Name: "build", DisplayName: "build", Status: "pending"}
		require.NoError(t, postgres_store.PostgresStore.CreateWorkflowNode(ctx, plain))
		plain.Status = "submitted"
		plain.DecisionReasons = []models.DecisionReason{{Kind: "dependency", Matched: &yes, Condition: "always",
			Deps: []models.DependencyOutcome{{Name: "lint", Status: "failed"}}}}
		require.NoError(t, postgres_store.PostgresStore.UpdateWorkflowNode(ctx, plain))

		nodes, err := postgres_store.PostgresStore.ListWorkflowNodes(ctx, wf.WorkflowID)
		require.NoError(t, err)
		byName := map[string]models.WorkflowNode{}
		for _, n := range nodes {
			byName[n.Name] = n
		}
		require.Len(t, byName["docs"].DecisionReasons, 1)
		assert.Equal(t, "paths", byName["docs"].DecisionReasons[0].Kind)
		assert.Equal(t, []string{"docs/**"}, byName["docs"].DecisionReasons[0].Include)
		require.NotNil(t, byName["docs"].DecisionReasons[0].MatchedCount)
		assert.Equal(t, 0, *byName["docs"].DecisionReasons[0].MatchedCount)

		require.Len(t, byName["build"].DecisionReasons, 1)
		dep := byName["build"].DecisionReasons[0]
		assert.Equal(t, "always", dep.Condition)
		assert.Equal(t, []models.DependencyOutcome{{Name: "lint", Status: "failed"}}, dep.Deps)
	})
}
