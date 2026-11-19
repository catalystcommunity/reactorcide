package diagnosis

import (
	"context"
	"errors"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// ErrDeleteUnavailable is a store that cannot remove entries.
var ErrDeleteUnavailable = errors.New("removing a diagnosis entry is not available")

// Delete removes the entry of authorPrincipalID for the target, and its pull
// request comment section. A principal can remove only its own entry: the
// entry key is (target, author). It returns store.ErrNotFound when the
// author has no entry for the target.
func (s *Service) Delete(ctx context.Context, target *Resolved, authorPrincipalID string) (*models.DiagnosisReport, error) {
	if target == nil {
		return nil, store.ErrNotFound
	}
	authorPrincipalID = strings.TrimSpace(authorPrincipalID)
	if authorPrincipalID == "" {
		return nil, ErrNoPrincipal
	}
	deleter, ok := s.Store.(store.DiagnosisDeleteStore)
	if !ok {
		return nil, ErrDeleteUnavailable
	}
	jobID, workflowID := "", ""
	switch {
	case target.Job != nil:
		jobID = target.Job.JobID
	case target.Workflow != nil:
		workflowID = target.Workflow.WorkflowID
	default:
		return nil, store.ErrNotFound
	}
	removed, err := deleter.DeleteDiagnosisReport(ctx, jobID, workflowID, authorPrincipalID, ReportEntryKey)
	if err != nil {
		return nil, err
	}
	details := models.JSONB{"target_type": removed.TargetType, "author_principal_id": removed.AuthorPrincipalID,
		"revision": removed.Revision}
	if removed.JobID != nil {
		details["job_id"] = *removed.JobID
	}
	if removed.WorkflowID != nil {
		details["workflow_id"] = *removed.WorkflowID
	}
	audit.Record(ctx, s.Audit, removed.OrgID, "diagnosis.delete", "diagnosis", removed.DiagnosisID, details)
	return removed, nil
}
