package postgres_store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Required set storage (docs/required-set.md).

func (ps PostgresDbStore) GetProjectRequiredSetSettings(ctx context.Context, projectID string) (*models.ProjectRequiredSetSettings, error) {
	var settings models.ProjectRequiredSetSettings
	result := ps.getDB(ctx).Where("project_id = ?", projectID).Limit(1).Find(&settings)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to get required set settings: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return &models.ProjectRequiredSetSettings{ProjectID: projectID}, nil
	}
	return &settings, nil
}

func (ps PostgresDbStore) GetOrgRequiredSetSettings(ctx context.Context, orgID string) (*models.OrgRequiredSetSettings, error) {
	var settings models.OrgRequiredSetSettings
	result := ps.getDB(ctx).Where("org_id = ?", orgID).Limit(1).Find(&settings)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to get organization required set settings: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return &models.OrgRequiredSetSettings{OrgID: orgID, ApproverRules: []models.RequiredSetApproverRule{},
			MaxQuarantineDays: models.DefaultMaxQuarantineDays}, nil
	}
	if settings.ApproverRules == nil {
		settings.ApproverRules = []models.RequiredSetApproverRule{}
	}
	return &settings, nil
}

// knownWorkflowsQuery selects every workflow security ID of a project: the
// root workflows it ran and the eval decisions of its events. An invalid
// workflow file with no ID has a "file:" key; it is not a workflow.
const knownWorkflowsQuery = `
SELECT workflow_security_id, (array_agg(name ORDER BY seen DESC))[1] AS name, max(seen) AS last_seen
  FROM (
    SELECT w.workflow_security_id, w.name, w.created_at AS seen
      FROM workflow_instances w
     WHERE w.project_id = @project AND w.parent_workflow_id IS NULL AND w.workflow_security_id <> ''
    UNION ALL
    SELECT d.workflow_security_id, d.workflow_name AS name, d.created_at AS seen
      FROM ci_event_workflow_decisions d
      JOIN ci_events e ON e.event_id = d.event_id
     WHERE e.project_id = @project AND d.workflow_security_id <> '' AND d.workflow_security_id NOT LIKE 'file:%'
  ) seen
 GROUP BY workflow_security_id
 ORDER BY workflow_security_id`

func (ps PostgresDbStore) ListKnownWorkflows(ctx context.Context, projectID string) ([]models.KnownWorkflow, error) {
	rows, err := ps.getDB(ctx).Raw(knownWorkflowsQuery, map[string]any{"project": projectID}).Rows()
	if err != nil {
		return nil, fmt.Errorf("failed to list known workflows: %w", err)
	}
	defer rows.Close()
	result := []models.KnownWorkflow{}
	for rows.Next() {
		var known models.KnownWorkflow
		if err := rows.Scan(&known.WorkflowSecurityID, &known.Name, &known.LastSeen); err != nil {
			return nil, fmt.Errorf("failed to read known workflows: %w", err)
		}
		result = append(result, known)
	}
	return result, rows.Err()
}

func (ps PostgresDbStore) SetRequiredSetEnabled(ctx context.Context, projectID, orgID string, enabled bool, by *string) ([]string, error) {
	var seeded []string
	err := ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		settings := models.ProjectRequiredSetSettings{ProjectID: projectID, OrgID: orgID, Enabled: enabled, UpdatedAt: now}
		updates := map[string]any{"enabled": enabled, "updated_at": now}
		if enabled {
			settings.EnabledAt, settings.EnabledBy = &now, by
			updates["enabled_at"], updates["enabled_by"] = now, by
		}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "project_id"}},
			DoUpdates: clause.Assignments(updates)}).Create(&settings).Error; err != nil {
			return fmt.Errorf("failed to set required set state: %w", err)
		}
		if !enabled {
			return nil
		}
		// Every known workflow goes into the set, so nothing changes at the
		// moment the set turns on. Entries that already exist stay.
		rows, err := tx.Raw(`INSERT INTO project_required_workflows
		        (project_id, org_id, workflow_security_id, branch_pattern, source, added_by, added_at, reason)
		    SELECT @project, @org, known.workflow_security_id, '', 'seed', @by, @now, 'known when the required set was turned on'
		      FROM (`+knownWorkflowsQuery+`) known
		    ON CONFLICT (project_id, workflow_security_id, branch_pattern) DO NOTHING
		    RETURNING workflow_security_id`,
			map[string]any{"project": projectID, "org": orgID, "by": by, "now": now}).Rows()
		if err != nil {
			return fmt.Errorf("failed to seed the required set: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			seeded = append(seeded, id)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return seeded, nil
}

func (ps PostgresDbStore) SetProjectRequiredSetApprovers(ctx context.Context, projectID, orgID string, approvers []string, rules *[]models.RequiredSetApproverRule) error {
	var approverValue any
	if approvers != nil {
		approverValue = pq.StringArray(approvers)
	}
	var rulesValue any
	if rules != nil {
		encoded, err := json.Marshal(*rules)
		if err != nil {
			return fmt.Errorf("failed to encode approver rules: %w", err)
		}
		rulesValue = string(encoded)
	}
	err := ps.getDB(ctx).Exec(`INSERT INTO project_required_set_settings (project_id, org_id, approvers, approver_rules, updated_at)
	    VALUES (@project, @org, @approvers, CAST(@rules AS jsonb), timezone('utc', now()))
	    ON CONFLICT (project_id) DO UPDATE SET approvers = EXCLUDED.approvers, approver_rules = EXCLUDED.approver_rules,
	        updated_at = EXCLUDED.updated_at`,
		map[string]any{"project": projectID, "org": orgID, "approvers": approverValue, "rules": rulesValue}).Error
	if err != nil {
		return fmt.Errorf("failed to set required set approvers: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) UpsertOrgRequiredSetSettings(ctx context.Context, settings *models.OrgRequiredSetSettings) error {
	settings.UpdatedAt = time.Now().UTC()
	if settings.ApproverRules == nil {
		settings.ApproverRules = []models.RequiredSetApproverRule{}
	}
	if settings.MaxQuarantineDays == 0 {
		settings.MaxQuarantineDays = models.DefaultMaxQuarantineDays
	}
	err := ps.getDB(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "org_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"approvers", "approver_rules", "max_quarantine_days", "updated_at"})}).
		Create(settings).Error
	if err != nil {
		return fmt.Errorf("failed to save organization required set settings: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) ListRequiredWorkflows(ctx context.Context, projectID string) ([]models.RequiredWorkflow, error) {
	entries := []models.RequiredWorkflow{}
	if err := ps.getDB(ctx).Where("project_id = ?", projectID).
		Order("workflow_security_id, branch_pattern").Find(&entries).Error; err != nil {
		return nil, fmt.Errorf("failed to list required workflows: %w", err)
	}
	return entries, nil
}

func (ps PostgresDbStore) SetRequiredWorkflowQuarantine(ctx context.Context, projectID, securityID, branchPattern string, until *time.Time, reason string, by *string) error {
	updates := map[string]any{"quarantined_until": nil, "quarantine_reason": "", "quarantined_by": nil, "quarantined_at": nil}
	if until != nil {
		updates = map[string]any{"quarantined_until": until.UTC(), "quarantine_reason": reason, "quarantined_by": by,
			"quarantined_at": time.Now().UTC()}
	}
	result := ps.getDB(ctx).Model(&models.RequiredWorkflow{}).
		Where("project_id = ? AND workflow_security_id = ? AND branch_pattern = ?", projectID, securityID, branchPattern).
		Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("failed to set quarantine: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (ps PostgresDbStore) CreateRequiredSetProposal(ctx context.Context, proposal *models.RequiredSetProposal) error {
	proposal.State = models.RequiredSetProposalPending
	if proposal.ProposedAt.IsZero() {
		proposal.ProposedAt = time.Now().UTC()
	}
	// A savepoint: a duplicate pending proposal must not abort the
	// transaction of the caller.
	err := ps.getDB(ctx).Transaction(func(tx *gorm.DB) error { return tx.Create(proposal).Error })
	if err != nil {
		if isUniqueViolation(err) {
			return store.ErrAlreadyExists
		}
		return fmt.Errorf("failed to create required set proposal: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) GetRequiredSetProposal(ctx context.Context, proposalID string) (*models.RequiredSetProposal, error) {
	var proposal models.RequiredSetProposal
	if err := ps.getDB(ctx).Where("proposal_id = ?", proposalID).Take(&proposal).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) || strings.Contains(err.Error(), "invalid input syntax for type uuid") {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get required set proposal: %w", err)
	}
	return &proposal, nil
}

func (ps PostgresDbStore) ListRequiredSetProposals(ctx context.Context, projectID, state string, limit int) ([]models.RequiredSetProposal, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := ps.getDB(ctx).Where("project_id = ?", projectID)
	if state != "" {
		query = query.Where("state = ?", state)
	}
	proposals := []models.RequiredSetProposal{}
	if err := query.Order("proposed_at DESC, proposal_id DESC").Limit(limit).Find(&proposals).Error; err != nil {
		return nil, fmt.Errorf("failed to list required set proposals: %w", err)
	}
	return proposals, nil
}

func (ps PostgresDbStore) DecideRequiredSetProposal(ctx context.Context, proposalID, state string, by *string, reason string) (*models.RequiredSetProposal, error) {
	switch state {
	case models.RequiredSetProposalApproved, models.RequiredSetProposalRejected, models.RequiredSetProposalWithdrawn:
	default:
		return nil, store.ErrInvalidInput
	}
	var decided models.RequiredSetProposal
	err := ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		// The conditional update makes two concurrent decisions safe: only
		// one of them finds the proposal pending.
		result := tx.Model(&decided).Clauses(clause.Returning{}).
			Where("proposal_id = ? AND state = ?", proposalID, models.RequiredSetProposalPending).
			Updates(map[string]any{"state": state, "decided_by": by, "decided_at": now, "decision_reason": reason})
		if result.Error != nil {
			return fmt.Errorf("failed to decide required set proposal: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			var count int64
			if err := tx.Model(&models.RequiredSetProposal{}).Where("proposal_id = ?", proposalID).Count(&count).Error; err != nil || count == 0 {
				return store.ErrNotFound
			}
			return store.ErrConflict
		}
		if state != models.RequiredSetProposalApproved {
			return nil
		}
		switch decided.Action {
		case models.RequiredSetActionPromote:
			proposalID := decided.ProposalID
			entry := models.RequiredWorkflow{ProjectID: decided.ProjectID, OrgID: decided.OrgID,
				WorkflowSecurityID: decided.WorkflowSecurityID, BranchPattern: decided.BranchPattern,
				Source: models.RequiredWorkflowSourcePromotion, AddedBy: decided.ProposedBy, AddedAt: now,
				Reason: decided.Reason, ProposalID: &proposalID}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&entry).Error; err != nil {
				return fmt.Errorf("failed to add required workflow: %w", err)
			}
		case models.RequiredSetActionDemote:
			result := tx.Where("project_id = ? AND workflow_security_id = ? AND branch_pattern = ?",
				decided.ProjectID, decided.WorkflowSecurityID, decided.BranchPattern).Delete(&models.RequiredWorkflow{})
			if result.Error != nil {
				return fmt.Errorf("failed to remove required workflow: %w", result.Error)
			}
			if result.RowsAffected == 0 {
				return store.ErrNotFound
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &decided, nil
}
