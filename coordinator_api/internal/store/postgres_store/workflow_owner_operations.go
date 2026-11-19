package postgres_store

import (
	"context"
	"errors"
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
)

// LatestWorkflowOwnership returns the owner snapshot of the newest workflow
// instance of (project, workflow security ID) that read its owner from a
// TRUSTED ref (owner_trusted, source ci; migration 000053). A snapshot of a
// push to another branch or of a pull request head never counts.
// store.ErrNotFound when there is none.
func (ps PostgresDbStore) LatestWorkflowOwnership(ctx context.Context, projectID, workflowSecurityID string) (*models.WorkflowOwnership, error) {
	if !isValidUUID(projectID) || workflowSecurityID == "" {
		return nil, store.ErrNotFound
	}
	var row models.WorkflowInstance
	err := ps.getDB(ctx).
		Select("workflow_id", "owner_group", "owner_subjects", "owner_contact", "owner_source", "owner_ci_sha", "owner_trusted", "owner_ref_classes").
		Where("project_id = ? AND workflow_security_id = ? AND owner_group IS NOT NULL AND owner_trusted AND owner_source = ?",
			projectID, workflowSecurityID, models.OwnerSourceCI).
		Order("created_at DESC").
		Take(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to read workflow owner: %w", err)
	}
	ownership := row.Ownership()
	return &ownership, nil
}

// refreshOwnership reads the owner snapshot that the database trigger gave a
// new job (copied from its workflow).
func refreshJobOwnership(db *gorm.DB, job *models.Job) {
	if job.OwnerGroup != nil || job.JobID == "" || job.WorkflowID == nil || *job.WorkflowID == "" {
		return
	}
	var row models.Job
	if db.Select("job_id", "owner_group", "owner_subjects", "owner_contact").Where("job_id = ?", job.JobID).Take(&row).Error == nil {
		job.OwnerGroup, job.OwnerSubjects, job.OwnerContact = row.OwnerGroup, row.OwnerSubjects, row.OwnerContact
	}
}

// refreshWorkflowOwnership reads the owner snapshot that the database
// trigger gave a new child workflow (copied from its parent).
func refreshWorkflowOwnership(db *gorm.DB, wf *models.WorkflowInstance) {
	// gorm RETURNING can fill owner_group but not owner_trusted, so a child
	// always reads the full snapshot back.
	if wf.WorkflowID == "" || wf.ParentWorkflowID == nil || *wf.ParentWorkflowID == "" {
		return
	}
	var row models.WorkflowInstance
	if db.Select("workflow_id", "owner_group", "owner_subjects", "owner_contact", "owner_source", "owner_ci_sha", "owner_trusted", "owner_ref_classes").
		Where("workflow_id = ?", wf.WorkflowID).Take(&row).Error == nil {
		wf.OwnerGroup, wf.OwnerSubjects, wf.OwnerContact = row.OwnerGroup, row.OwnerSubjects, row.OwnerContact
		wf.OwnerSource, wf.OwnerCISHA = row.OwnerSource, row.OwnerCISHA
		wf.OwnerTrusted, wf.OwnerRefClasses = row.OwnerTrusted, row.OwnerRefClasses
	}
}

// ListLatestWorkflowOwnerships returns the latest trusted owner snapshot of
// every workflow security ID in a project that has one (the same rule as
// LatestWorkflowOwnership).
func (ps PostgresDbStore) ListLatestWorkflowOwnerships(ctx context.Context, projectID string) (map[string]models.WorkflowOwnership, error) {
	result := map[string]models.WorkflowOwnership{}
	if !isValidUUID(projectID) {
		return result, nil
	}
	var rows []models.WorkflowInstance
	err := ps.getDB(ctx).Raw(`
SELECT DISTINCT ON (workflow_security_id)
       workflow_id, workflow_security_id, owner_group, owner_subjects, owner_contact, owner_source, owner_ci_sha,
       owner_trusted, owner_ref_classes
  FROM workflow_instances
 WHERE project_id = ? AND owner_group IS NOT NULL AND owner_trusted AND owner_source = 'ci'
 ORDER BY workflow_security_id, created_at DESC`, projectID).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list workflow owners: %w", err)
	}
	for i := range rows {
		result[rows[i].WorkflowSecurityID] = rows[i].Ownership()
	}
	return result, nil
}
