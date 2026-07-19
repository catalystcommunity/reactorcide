package postgres_store

import (
	"context"
	"fmt"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
)

// This file implements SQL-side visibility filtering for job/workflow list
// endpoints (UI_AUTH_PLAN.md's Visibility section; see the code-review
// finding this fixes, referenced from internal/handlers/job_handler.go's
// jobsVisibleToStore doc comment): ListJobs/ListWorkflows used to fetch a
// LIMIT/OFFSET page and THEN filter it down to visible rows in Go
// (authz.FilterVisibleJobs/FilterVisibleWorkflowSummaries), which can return
// short or empty pages even when more visible rows exist past the offset,
// and reported Total as the filtered page length rather than a real count.
// ListJobsVisibleTo/ListWorkflowSummariesVisibleTo push the exact same
// visibility predicate authz.CanViewJob evaluates in Go
// (internal/authz/visibility.go's canViewOwned/canViewProject/
// canViewPrivate) into the SQL WHERE clause instead, so LIMIT/OFFSET and
// COUNT(*) both operate on the already-visibility-filtered row set.
//
// This package intentionally does not import internal/authz (authz depends
// on store via the narrow RoleStore interface, not the other way around —
// see authz/resolver.go's doc comment). Callers resolve "is this viewer a
// global admin" once via authz.Resolver.IsGlobalAdmin and pass the bool in.

// visibilityJoins returns the LEFT JOIN clauses needed to evaluate
// visibilityPredicateSQL for an "owned resource" row aliased entAlias (must
// expose <entAlias>.project_id and <entAlias>.user_id — both jobs and
// workflow_instances do). projAlias/projOwnerAlias/entOwnerAlias name the
// three additionally-joined tables the predicate references: the resource's
// project (if any), that project's owning org (user) row, and the
// resource's own owning-org (user) row.
func visibilityJoins(entAlias, projAlias, projOwnerAlias, entOwnerAlias string) []string {
	return []string{
		fmt.Sprintf("LEFT JOIN projects %s ON %s.project_id = %s.project_id", projAlias, projAlias, entAlias),
		fmt.Sprintf("LEFT JOIN users %s ON %s.user_id = %s.user_id", projOwnerAlias, projOwnerAlias, projAlias),
		fmt.Sprintf("LEFT JOIN users %s ON %s.user_id = %s.user_id", entOwnerAlias, entOwnerAlias, entAlias),
	}
}

// visibilityPredicateSQL returns the boolean SQL expression mirroring
// authz.visibilityBatch.canViewOwned (internal/authz/visibility.go) for an
// owned-resource row exposed via entAlias/projAlias/projOwnerAlias/
// entOwnerAlias (see visibilityJoins). The returned expression contains
// exactly 8 `?` placeholders, ALL of which must be bound to the same
// viewer's user ID, in order — see visibilityArgs.
//
// Clause-by-clause mapping back to internal/authz/visibility.go:
//
//   - "public via project": mirrors canViewProject's early return
//     `!project.IsEffectivelyPrivate(orgIsPrivate)` — project isn't private
//     and its owning org isn't private.
//   - "public via org, no project": mirrors canViewOwned's no-project (or
//     project-deleted) fallback early return `!orgIsPrivate`, using the
//     resource's own owning-org row.
//   - "private via project": mirrors canViewProject's private branch, which
//     calls canViewPrivate(project.UserID, &project.ProjectID):
//     project-owner self-match, an org/admin role_assignment on the
//     project's owning org (direct or via group membership), or ANY
//     project-scoped role_assignment (direct or via group) — Go's
//     hasProjectRole(RoleOwner) check is a subset of hasAnyProjectRole, so
//     the two collapse into a single EXISTS here.
//   - "private via org, no project": mirrors canViewOwned's no-project
//     fallback calling canViewPrivate(ownerUserID, nil): owner self-match,
//     an org/admin role_assignment on the resource's own owning org (direct
//     or via group) — no project EXISTS clause, since projectID is nil in
//     this branch.
//
// Global-admin and anonymous-caller handling are deliberately NOT part of
// this predicate: callers short-circuit it entirely for a global admin
// (pass isGlobalAdmin=true to the exported List*VisibleTo functions, which
// skip calling this at all), and REST callers reaching these methods are
// always authenticated (no anonymous viewerID reaches here).
func visibilityPredicateSQL(entAlias, projAlias, projOwnerAlias, entOwnerAlias string) string {
	return fmt.Sprintf(`(
		( %[1]s.project_id IS NOT NULL AND NOT (%[1]s.is_private OR COALESCE(%[2]s.is_private, false)) )
		OR ( %[1]s.project_id IS NULL AND NOT COALESCE(%[3]s.is_private, false) )
		OR (
			%[1]s.project_id IS NOT NULL
			AND (%[1]s.is_private OR COALESCE(%[2]s.is_private, false))
			AND (
				(%[1]s.user_id IS NOT NULL AND %[1]s.user_id = ?)
				OR (%[1]s.user_id IS NOT NULL AND EXISTS (
					SELECT 1 FROM role_assignments ra
					WHERE ra.scope_type = 'org' AND ra.scope_id = %[1]s.user_id AND ra.role = 'admin'
					AND (
						(ra.principal_type = 'user' AND ra.principal_id = ?)
						OR (ra.principal_type = 'group' AND ra.principal_id IN (
							SELECT gm.group_id FROM group_members gm WHERE gm.user_id = ?))
					)
				))
				OR EXISTS (
					SELECT 1 FROM role_assignments ra
					WHERE ra.scope_type = 'project' AND ra.scope_id = %[1]s.project_id
					AND (
						(ra.principal_type = 'user' AND ra.principal_id = ?)
						OR (ra.principal_type = 'group' AND ra.principal_id IN (
							SELECT gm.group_id FROM group_members gm WHERE gm.user_id = ?))
					)
				)
			)
		)
		OR (
			%[4]s.project_id IS NULL
			AND COALESCE(%[3]s.is_private, false)
			AND (
				%[4]s.user_id = ?
				OR EXISTS (
					SELECT 1 FROM role_assignments ra
					WHERE ra.scope_type = 'org' AND ra.scope_id = %[4]s.user_id AND ra.role = 'admin'
					AND (
						(ra.principal_type = 'user' AND ra.principal_id = ?)
						OR (ra.principal_type = 'group' AND ra.principal_id IN (
							SELECT gm.group_id FROM group_members gm WHERE gm.user_id = ?))
					)
				)
			)
		)
	)`, projAlias, projOwnerAlias, entOwnerAlias, entAlias)
}

// visibilityArgs returns the 8 identical viewerID bindings
// visibilityPredicateSQL's 8 `?` placeholders need, in order.
func visibilityArgs(viewerID string) []interface{} {
	args := make([]interface{}, 8)
	for i := range args {
		args[i] = viewerID
	}
	return args
}

// ListJobsVisibleTo lists jobs visible to viewerID (see authz.CanViewJob),
// applying filters and SQL-side visibility together so pagination
// (limit/offset) and the returned total count both operate on the
// already-visibility-filtered row set. filters honors the same keys as
// ListJobs (status, user_id, queue_name, source_type, project_id,
// workflow_id). isGlobalAdmin, resolved once by the caller via
// authz.Resolver.IsGlobalAdmin, bypasses the visibility predicate entirely
// (a global admin sees every row that matches filters).
func (ps PostgresDbStore) ListJobsVisibleTo(ctx context.Context, viewerID string, isGlobalAdmin bool, filters map[string]interface{}, limit, offset int) ([]models.Job, int64, error) {
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	// build is called twice (once for the count, once for the page) rather
	// than reused, so clause state from one call (e.g. Select/Order/Limit)
	// can never leak into the other.
	build := func() *gorm.DB {
		q := ps.getDB(ctx).Table("jobs j")
		for _, join := range visibilityJoins("j", "p", "proj_owner", "job_owner") {
			q = q.Joins(join)
		}
		for key, value := range filters {
			switch key {
			case "status":
				q = q.Where("j.status = ?", value)
			case "user_id":
				q = q.Where("j.user_id = ?", value)
			case "queue_name":
				q = q.Where("j.queue_name = ?", value)
			case "source_type":
				q = q.Where("j.source_type = ?", value)
			case "project_id":
				q = q.Where("j.project_id = ?", value)
			case "workflow_id":
				q = q.Where("j.workflow_id = ?", value)
			}
		}
		if !isGlobalAdmin {
			q = q.Where(visibilityPredicateSQL("j", "p", "proj_owner", "job_owner"), visibilityArgs(viewerID)...)
		}
		return q
	}

	var total int64
	if err := build().Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to count visible jobs: %w", err)
	}

	var jobs []models.Job
	if err := build().Select("j.*").Order("j.created_at DESC").Limit(limit).Offset(offset).Find(&jobs).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to list visible jobs: %w", err)
	}

	return jobs, total, nil
}

// ListWorkflowSummariesVisibleTo is ListWorkflowSummaries' (workflow_operations.go)
// counterpart for SQL-side visibility filtering: same "workflow_instances
// UNION ALL loose (project-less/workflow-less) jobs" shape, with the same
// visibilityPredicateSQL applied to each branch (mirroring
// authz.CanViewWorkflowSummary / CanViewJob for the loose-job branch), and
// LIMIT/OFFSET plus COUNT(*) evaluated over the combined, already-filtered
// result so pagination and Total are both exact.
func (ps PostgresDbStore) ListWorkflowSummariesVisibleTo(ctx context.Context, viewerID string, isGlobalAdmin bool, filters map[string]interface{}, limit, offset int) ([]models.WorkflowSummary, int64, error) {
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

	if !isGlobalAdmin {
		whereWorkflow = append(whereWorkflow, visibilityPredicateSQL("wi", "wip", "wipo", "wio"))
		workflowArgs = append(workflowArgs, visibilityArgs(viewerID)...)
		whereLoose = append(whereLoose, visibilityPredicateSQL("j", "ljp", "ljpo", "ljo"))
		looseArgs = append(looseArgs, visibilityArgs(viewerID)...)
	}

	workflowClause := ""
	if len(whereWorkflow) > 0 {
		workflowClause = "WHERE " + strings.Join(whereWorkflow, " AND ")
	}
	looseClause := "WHERE j.workflow_id IS NULL"
	if len(whereLoose) > 0 {
		looseClause += " AND " + strings.Join(whereLoose, " AND ")
	}

	cte := fmt.Sprintf(`
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
	FROM workflow_instances wi
	LEFT JOIN projects wip ON wip.project_id = wi.project_id
	LEFT JOIN users wipo ON wipo.user_id = wip.user_id
	LEFT JOIN users wio ON wio.user_id = wi.user_id
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
	FROM jobs j
	LEFT JOIN projects ljp ON ljp.project_id = j.project_id
	LEFT JOIN users ljpo ON ljpo.user_id = ljp.user_id
	LEFT JOIN users ljo ON ljo.user_id = j.user_id
	%s
),
combined AS (
	SELECT * FROM workflow_rows
	UNION ALL
	SELECT * FROM loose_rows
)`, workflowClause, looseClause)

	args := append(append([]interface{}{}, workflowArgs...), looseArgs...)

	var total int64
	if err := ps.getDB(ctx).Raw(cte+"\nSELECT COUNT(*) FROM combined", args...).Scan(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to count visible workflow summaries: %w", err)
	}

	listArgs := append(append([]interface{}{}, args...), limit, offset)
	var summaries []models.WorkflowSummary
	if err := ps.getDB(ctx).Raw(cte+"\nSELECT * FROM combined ORDER BY created_at DESC LIMIT ? OFFSET ?", listArgs...).Scan(&summaries).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to list visible workflow summaries: %w", err)
	}

	return summaries, total, nil
}
