package store

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// DiagnosisStore stores diagnosis entries (WP17, docs/diagnosis.md).
type DiagnosisStore interface {
	// UpsertDiagnosisReport creates the entry of report.AuthorPrincipalID
	// for its target, or replaces it. It fills report from the stored row.
	// created is true for a new entry.
	UpsertDiagnosisReport(ctx context.Context, report *models.DiagnosisReport) (created bool, err error)
	// ListDiagnosisReportsForJob returns the entries of one job, oldest
	// first.
	ListDiagnosisReportsForJob(ctx context.Context, jobID string) ([]models.DiagnosisReport, error)
	// ListDiagnosisReportsForWorkflow returns the entries of a workflow and
	// of the jobs of its nodes, oldest first.
	ListDiagnosisReportsForWorkflow(ctx context.Context, workflowID string) ([]models.DiagnosisReport, error)
}

// DiagnosisDeleteStore removes diagnosis entries. It is separate from
// DiagnosisStore so that fakes need not implement it.
type DiagnosisDeleteStore interface {
	// DeleteDiagnosisReport removes the entry of authorPrincipalID for a
	// job (jobID) or a workflow (workflowID; jobID empty), and its pull
	// request comment section (report entry entryKey, the section is then
	// removed at the next render). It returns the removed entry, or
	// ErrNotFound when the author has no entry for the target.
	DeleteDiagnosisReport(ctx context.Context, jobID, workflowID, authorPrincipalID string, entryKey func(*models.DiagnosisReport) string) (*models.DiagnosisReport, error)
}
