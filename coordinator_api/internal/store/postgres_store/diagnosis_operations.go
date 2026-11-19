package postgres_store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

var _ store.DiagnosisStore = PostgresDbStore{}

// diagnosisUpsertRow is the RETURNING row of the upsert: the entry and
// whether the statement inserted it.
type diagnosisUpsertRow struct {
	models.DiagnosisReport
	Inserted bool `gorm:"column:inserted"`
}

func (ps PostgresDbStore) UpsertDiagnosisReport(ctx context.Context, report *models.DiagnosisReport) (bool, error) {
	if report == nil || !isValidUUID(report.OrgID) || !isValidUUID(report.AuthorPrincipalID) {
		return false, store.ErrInvalidInput
	}
	var conflict string
	switch report.TargetType {
	case models.DiagnosisTargetJob:
		if report.JobID == nil || !isValidUUID(*report.JobID) {
			return false, store.ErrInvalidInput
		}
		conflict = "(job_id, author_principal_id) WHERE target_type = 'job'"
	case models.DiagnosisTargetWorkflow:
		if report.WorkflowID == nil || !isValidUUID(*report.WorkflowID) || report.JobID != nil {
			return false, store.ErrInvalidInput
		}
		conflict = "(workflow_id, author_principal_id) WHERE target_type = 'workflow'"
	default:
		return false, store.ErrInvalidInput
	}
	links := report.Links
	if links == nil {
		links = []models.DiagnosisLink{}
	}
	linksJSON, err := json.Marshal(links)
	if err != nil {
		return false, err
	}
	var shiftsJSON *string
	if report.SuggestedShifts != nil && !report.SuggestedShifts.IsZero() {
		raw, err := json.Marshal(report.SuggestedShifts)
		if err != nil {
			return false, err
		}
		value := string(raw)
		shiftsJSON = &value
	}
	// The conflict target is one of two fixed strings above, never input.
	query := fmt.Sprintf(`INSERT INTO diagnosis_reports (org_id, project_id, target_type, job_id, workflow_id, node_name,
		author_principal_id, author_kind, author_display_name, title, body, confidence, category, links, suggested_shifts)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?::jsonb)
	ON CONFLICT %s DO UPDATE SET
		org_id = EXCLUDED.org_id, project_id = EXCLUDED.project_id, workflow_id = EXCLUDED.workflow_id,
		node_name = EXCLUDED.node_name, author_kind = EXCLUDED.author_kind,
		author_display_name = EXCLUDED.author_display_name, title = EXCLUDED.title, body = EXCLUDED.body,
		confidence = EXCLUDED.confidence, category = EXCLUDED.category, links = EXCLUDED.links,
		suggested_shifts = EXCLUDED.suggested_shifts, revision = diagnosis_reports.revision + 1,
		updated_at = timezone('utc', now())
	RETURNING *, (xmax = 0) AS inserted`, conflict)
	var row diagnosisUpsertRow
	result := ps.getDB(ctx).Raw(query, report.OrgID, report.ProjectID, report.TargetType, report.JobID, report.WorkflowID,
		report.NodeName, report.AuthorPrincipalID, report.AuthorKind, report.AuthorDisplayName, report.Title, report.Body,
		report.Confidence, report.Category, string(linksJSON), shiftsJSON).Scan(&row)
	if result.Error != nil {
		return false, fmt.Errorf("store diagnosis entry: %w", result.Error)
	}
	if row.DiagnosisID == "" {
		return false, fmt.Errorf("store diagnosis entry: no row returned")
	}
	*report = row.DiagnosisReport
	if report.Links == nil {
		report.Links = []models.DiagnosisLink{}
	}
	return row.Inserted, nil
}

func (ps PostgresDbStore) ListDiagnosisReportsForJob(ctx context.Context, jobID string) ([]models.DiagnosisReport, error) {
	if !isValidUUID(jobID) {
		return nil, store.ErrNotFound
	}
	var rows []models.DiagnosisReport
	err := ps.getDB(ctx).Where("job_id = ?", jobID).Order("created_at, diagnosis_id").Find(&rows).Error
	return rows, err
}

func (ps PostgresDbStore) ListDiagnosisReportsForWorkflow(ctx context.Context, workflowID string) ([]models.DiagnosisReport, error) {
	if !isValidUUID(workflowID) {
		return nil, store.ErrNotFound
	}
	var rows []models.DiagnosisReport
	// A job entry stores the workflow of its job when it has one. The
	// sub-select also finds the entries of jobs that a node points to.
	err := ps.getDB(ctx).
		Where("workflow_id = ? OR job_id IN (SELECT job_id FROM workflow_nodes WHERE workflow_id = ? AND job_id IS NOT NULL)", workflowID, workflowID).
		Order("created_at, diagnosis_id").Find(&rows).Error
	return rows, err
}
