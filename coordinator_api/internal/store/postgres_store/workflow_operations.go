package postgres_store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (ps PostgresDbStore) CreateWorkflowInstance(ctx context.Context, wf *models.WorkflowInstance) error {
	if err := ps.getDB(ctx).Create(wf).Error; err != nil {
		return fmt.Errorf("failed to create workflow instance: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) CreateWorkflowInstanceForTrigger(ctx context.Context, wf *models.WorkflowInstance) error {
	result := ps.getDB(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "parent_job_id"},
			{Name: "trigger_operation_id"},
			{Name: "workflow_security_id"},
		},
		TargetWhere: clause.Where{Exprs: []clause.Expression{
			clause.Expr{SQL: "trigger_operation_id IS NOT NULL AND trigger_operation_id <> ''"},
		}},
		DoNothing: true,
	}).Create(wf)
	if result.Error != nil {
		return fmt.Errorf("failed to create workflow instance for trigger: %w", result.Error)
	}
	if result.RowsAffected > 0 {
		return nil
	}
	existing, err := ps.GetWorkflowInstanceByTriggerOperation(ctx, derefWorkflowString(wf.ParentJobID), wf.TriggerOperationID, wf.WorkflowSecurityID)
	if err != nil {
		return err
	}
	*wf = *existing
	return nil
}

func derefWorkflowString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (ps PostgresDbStore) GetWorkflowInstance(ctx context.Context, workflowID string) (*models.WorkflowInstance, error) {
	if !isValidUUID(workflowID) {
		return nil, store.ErrNotFound
	}
	var wf models.WorkflowInstance
	if err := ps.getDB(ctx).Where("workflow_id = ?", workflowID).First(&wf).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get workflow %s: %w", workflowID, err)
	}
	return &wf, nil
}

// GetWorkflowInstanceByParentJobAndName is the find-or-create key for
// multi-workflow spawning: a single eval (parent_job_id) owns at most one
// workflow per name, so reprocessing the same triggers reuses it rather than
// duplicating. Returns store.ErrNotFound when none exists.
func (ps PostgresDbStore) GetWorkflowInstanceByParentJobAndName(ctx context.Context, parentJobID, name string) (*models.WorkflowInstance, error) {
	if !isValidUUID(parentJobID) {
		return nil, store.ErrNotFound
	}
	var wf models.WorkflowInstance
	if err := ps.getDB(ctx).Where("parent_job_id = ? AND name = ?", parentJobID, name).First(&wf).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get workflow for parent %s name %q: %w", parentJobID, name, err)
	}
	return &wf, nil
}

func (ps PostgresDbStore) GetWorkflowInstanceByTriggerOperation(ctx context.Context, parentJobID, operationID, securityID string) (*models.WorkflowInstance, error) {
	if !isValidUUID(parentJobID) || strings.TrimSpace(operationID) == "" {
		return nil, store.ErrNotFound
	}
	var wf models.WorkflowInstance
	if err := ps.getDB(ctx).Where("parent_job_id = ? AND trigger_operation_id = ? AND workflow_security_id = ?", parentJobID, operationID, securityID).First(&wf).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get workflow for trigger operation %q: %w", operationID, err)
	}
	return &wf, nil
}

func (ps PostgresDbStore) ListWorkflowDescendants(ctx context.Context, rootWorkflowID string) ([]models.WorkflowInstance, error) {
	if !isValidUUID(rootWorkflowID) {
		return nil, store.ErrNotFound
	}
	var workflows []models.WorkflowInstance
	if err := ps.getDB(ctx).
		Where("workflow_id = ? OR root_workflow_id = ?", rootWorkflowID, rootWorkflowID).
		Order("created_at ASC").
		Find(&workflows).Error; err != nil {
		return nil, fmt.Errorf("failed to list workflow graph: %w", err)
	}
	return workflows, nil
}

func (ps PostgresDbStore) ListWorkflowNodesBySubtree(ctx context.Context, workflowID string) ([]models.WorkflowNode, error) {
	if !isValidUUID(workflowID) {
		return nil, store.ErrNotFound
	}
	var nodes []models.WorkflowNode
	if err := ps.getDB(ctx).
		Raw(`
WITH RECURSIVE workflow_tree AS (
	SELECT workflow_id FROM workflow_instances WHERE workflow_id = ?
	UNION ALL
	SELECT child.workflow_id
	FROM workflow_instances child
	JOIN workflow_tree parent ON child.parent_workflow_id = parent.workflow_id
)
SELECT wn.*
FROM workflow_nodes wn
JOIN workflow_tree tree ON tree.workflow_id = wn.workflow_id
ORDER BY wn.created_at ASC`, workflowID).
		Scan(&nodes).Error; err != nil {
		return nil, fmt.Errorf("failed to list workflow subtree nodes: %w", err)
	}
	return nodes, nil
}

func (ps PostgresDbStore) UpdateWorkflowInstance(ctx context.Context, wf *models.WorkflowInstance) error {
	result := ps.getDB(ctx).Save(wf)
	if result.Error != nil {
		return fmt.Errorf("failed to update workflow %s: %w", wf.WorkflowID, result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (ps PostgresDbStore) CreateWorkflowNode(ctx context.Context, node *models.WorkflowNode) error {
	if err := ps.getDB(ctx).Create(node).Error; err != nil {
		return fmt.Errorf("failed to create workflow node: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) UpdateWorkflowNode(ctx context.Context, node *models.WorkflowNode) error {
	result := ps.getDB(ctx).Save(node)
	if result.Error != nil {
		return fmt.Errorf("failed to update workflow node %s: %w", node.NodeID, result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (ps PostgresDbStore) ListWorkflowNodes(ctx context.Context, workflowID string) ([]models.WorkflowNode, error) {
	var nodes []models.WorkflowNode
	if err := ps.getDB(ctx).Where("workflow_id = ?", workflowID).Order("created_at ASC").Find(&nodes).Error; err != nil {
		return nil, fmt.Errorf("failed to list workflow nodes: %w", err)
	}
	return nodes, nil
}

func (ps PostgresDbStore) GetWorkflowNodeByJobID(ctx context.Context, jobID string) (*models.WorkflowNode, error) {
	if !isValidUUID(jobID) {
		return nil, store.ErrNotFound
	}
	var node models.WorkflowNode
	if err := ps.getDB(ctx).Where("job_id = ?", jobID).First(&node).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get workflow node for job %s: %w", jobID, err)
	}
	return &node, nil
}

func (ps PostgresDbStore) GetWorkflowVars(ctx context.Context, workflowID string) (map[string]models.JSONB, error) {
	var rows []models.WorkflowVar
	if err := ps.getDB(ctx).Where("workflow_id = ?", workflowID).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to get workflow vars: %w", err)
	}
	vars := make(map[string]models.JSONB, len(rows))
	for _, row := range rows {
		vars[row.Key] = row.Value
	}
	return vars, nil
}

func (ps PostgresDbStore) UpsertWorkflowVar(ctx context.Context, v *models.WorkflowVar) error {
	existing := models.WorkflowVar{}
	err := ps.getDB(ctx).Where("workflow_id = ? AND key = ?", v.WorkflowID, v.Key).First(&existing).Error
	if err == nil {
		existing.Value = v.Value
		existing.ValueHash = v.ValueHash
		existing.SourceNodeID = v.SourceNodeID
		existing.SourceJobID = v.SourceJobID
		return ps.getDB(ctx).Save(&existing).Error
	}
	if err != gorm.ErrRecordNotFound {
		return fmt.Errorf("failed to check workflow var: %w", err)
	}
	if err := ps.getDB(ctx).Create(v).Error; err != nil {
		return fmt.Errorf("failed to create workflow var: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) CreateWorkflowEvent(ctx context.Context, event *models.WorkflowEvent) error {
	if err := ps.getDB(ctx).Create(event).Error; err != nil {
		return fmt.Errorf("failed to create workflow event: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) ListWorkflowEvents(ctx context.Context, workflowID string, limit, offset int) ([]models.WorkflowEvent, error) {
	var events []models.WorkflowEvent
	if err := ps.getDB(ctx).
		Where("workflow_id = ?", workflowID).
		Order("created_at ASC").
		Limit(limit).
		Offset(offset).
		Find(&events).Error; err != nil {
		return nil, fmt.Errorf("failed to list workflow events: %w", err)
	}
	return events, nil
}

func (ps PostgresDbStore) GetLastSuccessfulWorkflowNodeDuration(ctx context.Context, wf *models.WorkflowInstance, nodeName string) (*int64, error) {
	if wf == nil || strings.TrimSpace(wf.Name) == "" || strings.TrimSpace(nodeName) == "" {
		return nil, nil
	}

	query := ps.getDB(ctx).
		Table("workflow_nodes wn").
		Select("wn.last_successful_duration_ms").
		Joins("JOIN workflow_instances wi ON wi.workflow_id = wn.workflow_id").
		Where("wi.workflow_id <> ?", wf.WorkflowID).
		Where("wi.name = ?", wf.Name).
		Where("wi.status = ?", "success").
		Where("wn.name = ?", nodeName).
		Where("wn.status = ?", "completed").
		Where("wn.last_successful_duration_ms IS NOT NULL")

	if wf.ProjectID != nil && *wf.ProjectID != "" {
		query = query.Where("wi.project_id = ?", *wf.ProjectID)
	} else {
		query = query.Where("wi.project_id IS NULL")
	}
	if wf.VCSRepo != "" {
		query = query.Where("wi.vcs_repo = ?", wf.VCSRepo)
	}

	var duration sql.NullInt64
	if err := query.Order("wn.completed_at DESC NULLS LAST, wn.updated_at DESC").Limit(1).Scan(&duration).Error; err != nil {
		return nil, fmt.Errorf("failed to get previous workflow node duration: %w", err)
	}
	if !duration.Valid {
		return nil, nil
	}
	return &duration.Int64, nil
}

func (ps PostgresDbStore) ListWorkflowSummaries(ctx context.Context, filters map[string]interface{}, limit, offset int) ([]models.WorkflowSummary, error) {
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	var whereWorkflow []string
	var whereLoose []string
	var workflowArgs []interface{}
	var looseArgs []interface{}

	if userID, ok := filters["user_id"]; ok {
		whereWorkflow = append(whereWorkflow, "wi.user_id = ?")
		whereLoose = append(whereLoose, "j.user_id = ?")
		workflowArgs = append(workflowArgs, userID)
		looseArgs = append(looseArgs, userID)
	}
	if orgIDs, ok := filters["org_ids"].([]string); ok && len(orgIDs) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(orgIDs)), ",")
		whereWorkflow = append(whereWorkflow, "wi.org_id IN ("+placeholders+")")
		whereLoose = append(whereLoose, "j.org_id IN ("+placeholders+")")
		for _, orgID := range orgIDs {
			workflowArgs = append(workflowArgs, orgID)
			looseArgs = append(looseArgs, orgID)
		}
	}
	if projectID, ok := filters["project_id"]; ok {
		whereWorkflow = append(whereWorkflow, "wi.project_id = ?")
		whereLoose = append(whereLoose, "j.project_id = ?")
		workflowArgs = append(workflowArgs, projectID)
		looseArgs = append(looseArgs, projectID)
	}
	if workflowID, ok := filters["workflow_id"]; ok {
		whereWorkflow = append(whereWorkflow, "wi.workflow_id = ?")
		whereLoose = append(whereLoose, "j.job_id = ?")
		workflowArgs = append(workflowArgs, workflowID)
		looseArgs = append(looseArgs, workflowID)
	}
	if _, gettingOne := filters["workflow_id"]; !gettingOne {
		whereWorkflow = append(whereWorkflow, "wi.parent_workflow_id IS NULL")
	}
	if status, ok := filters["status"].(string); ok && status != "" {
		workflowStatus := status
		looseStatus := status
		if status == "completed" {
			workflowStatus = "success"
		}
		if status == "success" {
			looseStatus = "completed"
		}
		whereWorkflow = append(whereWorkflow, "wi.status = ?")
		whereLoose = append(whereLoose, "j.status = ?")
		workflowArgs = append(workflowArgs, workflowStatus)
		looseArgs = append(looseArgs, looseStatus)
	}

	workflowClause := ""
	if len(whereWorkflow) > 0 {
		workflowClause = "WHERE " + strings.Join(whereWorkflow, " AND ")
	}
	looseClause := "WHERE j.workflow_id IS NULL"
	if _, gettingOne := filters["workflow_id"]; !gettingOne {
		// A loose evaluation job that creates one root workflow is the entry
		// step of that workflow. Keep evaluations with zero or many workflows
		// as separate rows because no single workflow can represent them.
		looseClause += ` AND (
			SELECT COUNT(*) FROM workflow_instances spawned
			WHERE spawned.parent_job_id = j.job_id
			  AND spawned.parent_workflow_id IS NULL
		) <> 1`
	}
	if len(whereLoose) > 0 {
		looseClause += " AND " + strings.Join(whereLoose, " AND ")
	}

	query := fmt.Sprintf(`
WITH workflow_rows AS (
	SELECT
		wi.workflow_id,
		'workflow' AS kind,
		wi.name,
		wi.status,
		wi.user_id,
		wi.project_id,
		wi.created_at,
		wi.updated_at,
		wi.completed_at,
		wi.queue_name,
		COALESCE(wi.vcs_repo, '') AS vcs_repo,
		wi.pr_number,
		COALESCE(wi.commit_sha, '') AS commit_sha,
		COUNT(wn.node_id)::int AS job_count,
		COUNT(*) FILTER (WHERE wn.status = 'running')::int AS running_count,
		COUNT(*) FILTER (WHERE wn.status = 'completed')::int AS completed_count,
		COUNT(*) FILTER (WHERE wn.status IN ('failed', 'timeout', 'cancelled'))::int AS failed_count,
		COUNT(*) FILTER (WHERE wn.status = 'skipped')::int AS skipped_count,
		NULL::uuid AS loose_job_id,
		NULL::int AS loose_job_exit,
		COALESCE(wi.last_error, '') AS decision_summary
		,wi.parent_job_id
		,wi.root_workflow_id
		,wi.parent_workflow_id
		,wi.origin_job_id
		,COALESCE(wi.origin_type, '') AS origin_type
		,COALESCE(wi.trigger_operation_id, '') AS trigger_operation_id
		,COALESCE(wi.trigger_type, '') AS trigger_type
	FROM workflow_instances wi
	LEFT JOIN workflow_nodes wn ON wn.workflow_id = wi.workflow_id
	%s
	GROUP BY wi.workflow_id
),
loose_rows AS (
	SELECT
		j.job_id AS workflow_id,
		'job' AS kind,
		j.name,
		j.status,
		j.user_id,
		j.project_id,
		j.created_at,
		j.updated_at,
		j.completed_at,
		j.queue_name,
		COALESCE(j.vcs_repo, '') AS vcs_repo,
		j.pr_number,
		COALESCE(j.commit_sha, '') AS commit_sha,
		1 AS job_count,
		CASE WHEN j.status = 'running' THEN 1 ELSE 0 END AS running_count,
		CASE WHEN j.status = 'completed' THEN 1 ELSE 0 END AS completed_count,
		CASE WHEN j.status IN ('failed', 'timeout', 'cancelled') THEN 1 ELSE 0 END AS failed_count,
		0 AS skipped_count,
		j.job_id AS loose_job_id,
		j.exit_code AS loose_job_exit,
		COALESCE(j.last_error, '') AS decision_summary
		,j.parent_job_id
		,NULL::uuid AS root_workflow_id
		,NULL::uuid AS parent_workflow_id
		,j.parent_job_id AS origin_job_id
		,'' AS origin_type
		,'' AS trigger_operation_id
		,'' AS trigger_type
	FROM jobs j
	%s
)
SELECT * FROM workflow_rows
UNION ALL
SELECT * FROM loose_rows
ORDER BY created_at DESC
LIMIT ? OFFSET ?`, workflowClause, looseClause)

	args := append(workflowArgs, looseArgs...)
	args = append(args, limit, offset)
	var summaries []models.WorkflowSummary
	if err := ps.getDB(ctx).Raw(query, args...).Scan(&summaries).Error; err != nil {
		return nil, fmt.Errorf("failed to list workflow summaries: %w", err)
	}
	return summaries, nil
}

func (ps PostgresDbStore) GetWorkflowSummary(ctx context.Context, workflowID string) (*models.WorkflowSummary, error) {
	if !isValidUUID(workflowID) {
		return nil, store.ErrNotFound
	}
	rows, err := ps.ListWorkflowSummaries(ctx, map[string]interface{}{"workflow_id": workflowID}, 1, 0)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].WorkflowID == workflowID {
			rootID := workflowID
			if rows[i].RootWorkflowID != nil && *rows[i].RootWorkflowID != "" {
				rootID = *rows[i].RootWorkflowID
			}
			graph, graphErr := ps.ListWorkflowDescendants(ctx, rootID)
			if graphErr != nil {
				return nil, graphErr
			}
			descendantOf := map[string]bool{workflowID: true}
			for _, child := range graph {
				if child.WorkflowID == workflowID {
					continue
				}
				if child.ParentWorkflowID == nil || !descendantOf[*child.ParentWorkflowID] {
					continue
				}
				descendantOf[child.WorkflowID] = true
				childRows, childErr := ps.ListWorkflowSummaries(ctx, map[string]interface{}{"workflow_id": child.WorkflowID}, 1, 0)
				if childErr != nil || len(childRows) == 0 {
					continue
				}
				rows[i].Children = append(rows[i].Children, childRows[0])
			}
			for _, child := range rows[i].Children {
				rows[i].JobCount += child.JobCount
				rows[i].RunningCount += child.RunningCount
				rows[i].CompletedCount += child.CompletedCount
				rows[i].FailedCount += child.FailedCount
				rows[i].SkippedCount += child.SkippedCount
			}
			return &rows[i], nil
		}
	}
	return nil, store.ErrNotFound
}
