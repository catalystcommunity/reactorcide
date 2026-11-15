package postgres_store

import (
	"context"
	"errors"
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var _ store.CIEventStore = PostgresDbStore{}

// RecordCIEvent stores an incoming event under a transaction lock on its
// semantic key, so two concurrent deliveries of one change are decided one
// after the other. The idempotency key is enforced by a unique constraint,
// not by a check first.
func (ps PostgresDbStore) RecordCIEvent(ctx context.Context, event *models.CIEvent) (*models.CIEvent, store.CIEventRecordOutcome, error) {
	if event == nil {
		return nil, "", store.ErrInvalidInput
	}
	if event.Status == "" {
		event.Status = models.CIEventStatusReceived
	}
	var result *models.CIEvent
	outcome := store.CIEventCreated
	err := ps.getDB(ctx).Transaction(func(tx *gorm.DB) error {
		lockKey := event.SemanticKey
		if lockKey == "" && event.IdempotencyKey != nil {
			lockKey = *event.IdempotencyKey
		}
		if lockKey != "" {
			if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?)::bigint)", "ci_event:"+lockKey).Error; err != nil {
				return fmt.Errorf("acquiring event lock: %w", err)
			}
		}
		if event.IdempotencyKey != nil && *event.IdempotencyKey != "" {
			var existing models.CIEvent
			err := tx.Where("idempotency_key = ?", *event.IdempotencyKey).First(&existing).Error
			if err == nil {
				result, outcome = &existing, store.CIEventRedelivery
				return nil
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("checking event delivery: %w", err)
			}
		}
		if event.SemanticKey != "" && event.ProjectID != nil && *event.ProjectID != "" {
			setting, err := duplicateEventsSetting(tx, *event.ProjectID)
			if err != nil {
				return err
			}
			if setting == models.DuplicateEventsSkip {
				var first models.CIEvent
				err := tx.Where("project_id = ? AND semantic_key = ? AND status <> ?",
					*event.ProjectID, event.SemanticKey, models.CIEventStatusDuplicate).
					Order("received_at ASC").First(&first).Error
				if err == nil {
					firstID := first.EventID
					event.DuplicateOfEventID = &firstID
					event.Status = models.CIEventStatusDuplicate
					outcome = store.CIEventDuplicate
				} else if !errors.Is(err, gorm.ErrRecordNotFound) {
					return fmt.Errorf("checking duplicate event: %w", err)
				}
			}
		}
		// ON CONFLICT DO NOTHING covers a delivery whose key was inserted by a
		// transaction that did not hold the same semantic lock (a different
		// semantic key for the same delivery ID cannot happen from one
		// provider, but the constraint is the authority either way).
		created := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "idempotency_key"}}, DoNothing: true}).Create(event)
		if created.Error != nil {
			return fmt.Errorf("creating event: %w", created.Error)
		}
		if created.RowsAffected == 0 && event.IdempotencyKey != nil {
			var existing models.CIEvent
			if err := tx.Where("idempotency_key = ?", *event.IdempotencyKey).First(&existing).Error; err != nil {
				return fmt.Errorf("loading existing event: %w", err)
			}
			result, outcome = &existing, store.CIEventRedelivery
			return nil
		}
		result = event
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return result, outcome, nil
}

func duplicateEventsSetting(tx *gorm.DB, projectID string) (string, error) {
	var row struct {
		ProjectSetting string
		OrgSetting     *string
	}
	err := tx.Raw(`SELECT p.duplicate_events AS project_setting, o.duplicate_events AS org_setting
		FROM projects p LEFT JOIN organizations o ON o.org_id = p.org_id
		WHERE p.project_id = ?`, projectID).Scan(&row).Error
	if err != nil {
		return "", fmt.Errorf("reading duplicate event setting: %w", err)
	}
	orgSetting := ""
	if row.OrgSetting != nil {
		orgSetting = *row.OrgSetting
	}
	return models.EffectiveDuplicateEvents(row.ProjectSetting, orgSetting), nil
}

func (ps PostgresDbStore) GetCIEvent(ctx context.Context, eventID string) (*models.CIEvent, error) {
	if !isValidUUID(eventID) {
		return nil, store.ErrNotFound
	}
	var event models.CIEvent
	if err := ps.getDB(ctx).Where("event_id = ?", eventID).First(&event).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get event: %w", err)
	}
	return &event, nil
}

func (ps PostgresDbStore) AttachCIEventEvalJob(ctx context.Context, eventID, jobID string) error {
	return ps.updateCIEvent(ctx, eventID, map[string]interface{}{
		"eval_job_id": jobID, "status": models.CIEventStatusEvaluating,
	})
}

func (ps PostgresDbStore) SetCIEventStatus(ctx context.Context, eventID, status string) error {
	return ps.updateCIEvent(ctx, eventID, map[string]interface{}{"status": status})
}

func (ps PostgresDbStore) SetCIEventSummary(ctx context.Context, eventID string, summary models.CIEventSummary) error {
	if !isValidUUID(eventID) {
		return store.ErrNotFound
	}
	// A struct update, so the json serializer on Summary applies.
	result := ps.getDB(ctx).Model(&models.CIEvent{}).Where("event_id = ?", eventID).
		Select("summary").Updates(&models.CIEvent{Summary: summary})
	if result.Error != nil {
		return fmt.Errorf("failed to update event summary: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (ps PostgresDbStore) updateCIEvent(ctx context.Context, eventID string, values map[string]interface{}) error {
	if !isValidUUID(eventID) {
		return store.ErrNotFound
	}
	values["updated_at"] = gorm.Expr("timezone('utc', now())")
	result := ps.getDB(ctx).Model(&models.CIEvent{}).Where("event_id = ?", eventID).Updates(values)
	if result.Error != nil {
		return fmt.Errorf("failed to update event: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (ps PostgresDbStore) UpsertCIEventWorkflowDecisions(ctx context.Context, decisions []models.CIEventWorkflowDecision) error {
	if len(decisions) == 0 {
		return nil
	}
	for i := range decisions {
		if decisions[i].Reasons == nil {
			decisions[i].Reasons = []models.DecisionReason{}
		}
	}
	err := ps.getDB(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "event_id"}, {Name: "workflow_security_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"workflow_name": gorm.Expr("EXCLUDED.workflow_name"),
			"source_file":   gorm.Expr("EXCLUDED.source_file"),
			"decision":      gorm.Expr("EXCLUDED.decision"),
			"reasons":       gorm.Expr("EXCLUDED.reasons"),
			// A later write without a workflow keeps the link an earlier write made.
			"workflow_id": gorm.Expr("COALESCE(EXCLUDED.workflow_id, ci_event_workflow_decisions.workflow_id)"),
			"updated_at":  gorm.Expr("timezone('utc', now())"),
		}),
	}).Create(&decisions).Error
	if err != nil {
		return fmt.Errorf("failed to store workflow decisions: %w", err)
	}
	return nil
}

func (ps PostgresDbStore) ListCIEventWorkflowDecisions(ctx context.Context, eventID string) ([]models.CIEventWorkflowDecision, error) {
	if !isValidUUID(eventID) {
		return nil, store.ErrNotFound
	}
	var decisions []models.CIEventWorkflowDecision
	if err := ps.getDB(ctx).Where("event_id = ?", eventID).
		Order("workflow_name ASC, workflow_security_id ASC").Find(&decisions).Error; err != nil {
		return nil, fmt.Errorf("failed to list workflow decisions: %w", err)
	}
	return decisions, nil
}

func (ps PostgresDbStore) GetCIEventWorkflowDecisionByWorkflow(ctx context.Context, workflowID string) (*models.CIEventWorkflowDecision, error) {
	if !isValidUUID(workflowID) {
		return nil, store.ErrNotFound
	}
	var decision models.CIEventWorkflowDecision
	if err := ps.getDB(ctx).Where("workflow_id = ? AND decision <> ?", workflowID, models.WorkflowDecisionCarried).First(&decision).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get workflow decision: %w", err)
	}
	return &decision, nil
}

func (ps PostgresDbStore) LatestCIEventActorForPR(ctx context.Context, projectID string, prNumber int, headSHA string) (string, error) {
	var event models.CIEvent
	err := ps.getDB(ctx).
		Where("project_id = ? AND pr_number = ? AND commit_sha = ? AND source = ? AND actor <> ''",
			projectID, prNumber, headSHA, models.CIEventSourceWebhook).
		Order("received_at DESC").First(&event).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return event.Actor, nil
}
