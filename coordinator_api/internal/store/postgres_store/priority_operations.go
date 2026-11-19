package postgres_store

import (
	"context"
	"errors"
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/priority"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/refclass"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"gorm.io/gorm"
)

// Priority lanes and group priority shifts (docs/priority.md).

func (ps PostgresDbStore) ListPriorityLanes(ctx context.Context, orgID string, projectID *string) ([]models.PriorityLane, error) {
	if !isValidUUID(orgID) {
		return []models.PriorityLane{}, nil
	}
	var lanes []models.PriorityLane
	if err := refClassScope(ps.getDB(ctx), orgID, projectID).Order("position, name").Find(&lanes).Error; err != nil {
		return nil, fmt.Errorf("failed to list priority lanes: %w", err)
	}
	return lanes, nil
}

func (ps PostgresDbStore) GetPriorityLane(ctx context.Context, orgID string, projectID *string, name string) (*models.PriorityLane, error) {
	if !isValidUUID(orgID) {
		return nil, store.ErrNotFound
	}
	var lane models.PriorityLane
	if err := refClassScope(ps.getDB(ctx), orgID, projectID).Where("name = ?", name).First(&lane).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("failed to get priority lane: %w", err)
	}
	return &lane, nil
}

func normalizeLane(lane *models.PriorityLane) {
	if lane.ProjectID != nil && *lane.ProjectID == "" {
		lane.ProjectID = nil
	}
	lane.ClassesAny = refclass.Normalize(lane.ClassesAny)
	lane.ClassesNone = refclass.Normalize(lane.ClassesNone)
	if lane.EventTypes == nil {
		lane.EventTypes = []string{}
	}
}

func (ps PostgresDbStore) UpsertPriorityLane(ctx context.Context, lane *models.PriorityLane) error {
	normalizeLane(lane)
	if err := priority.ValidateLane(*lane); err != nil {
		return fmt.Errorf("%w: %v", store.ErrInvalidInput, err)
	}
	existing, err := ps.GetPriorityLane(ctx, lane.OrgID, lane.ProjectID, lane.Name)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	db := ps.getDB(ctx)
	if existing == nil {
		if err := db.Create(lane).Error; err != nil {
			if isUniqueViolation(err) {
				return store.ErrAlreadyExists
			}
			return fmt.Errorf("failed to create priority lane: %w", err)
		}
		return db.Where("lane_id = ?", lane.LaneID).First(lane).Error
	}
	result := db.Model(&models.PriorityLane{}).Where("lane_id = ?", existing.LaneID).Updates(map[string]any{
		"position": lane.Position, "description": lane.Description, "base": lane.Base,
		"classes_any": lane.ClassesAny, "classes_none": lane.ClassesNone, "event_types": lane.EventTypes,
		"draft": lane.Draft, "job_kind": lane.JobKind, "retry": lane.Retry, "enabled": lane.Enabled,
		"updated_at": gorm.Expr("timezone('utc', now())"),
	})
	if result.Error != nil {
		return fmt.Errorf("failed to update priority lane: %w", result.Error)
	}
	return db.Where("lane_id = ?", existing.LaneID).First(lane).Error
}

func (ps PostgresDbStore) DeletePriorityLane(ctx context.Context, orgID string, projectID *string, name string) error {
	if !isValidUUID(orgID) {
		return store.ErrNotFound
	}
	result := refClassScope(ps.getDB(ctx), orgID, projectID).Where("name = ?", name).Delete(&models.PriorityLane{})
	if result.Error != nil {
		return fmt.Errorf("failed to delete priority lane: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (ps PostgresDbStore) ListPrincipalGroupShifts(ctx context.Context, orgID, principalID string) ([]models.GroupPriorityShift, error) {
	shifts := []models.GroupPriorityShift{}
	if !isValidUUID(orgID) || !isValidUUID(principalID) {
		return shifts, nil
	}
	err := ps.getDB(ctx).Raw(`SELECT g.group_id, g.name, g.priority_shift AS shift
		FROM groups g JOIN group_members m ON m.group_id = g.group_id
		WHERE g.org_id = ? AND m.principal_id = ? AND g.priority_shift <> 0
		ORDER BY g.name`, orgID, principalID).Scan(&shifts).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list group priority shifts: %w", err)
	}
	return shifts, nil
}

func (ps PostgresDbStore) ListGroupShifts(ctx context.Context, orgID string, groupIDs []string) ([]models.GroupPriorityShift, error) {
	shifts := []models.GroupPriorityShift{}
	valid := []string{}
	for _, id := range groupIDs {
		if isValidUUID(id) {
			valid = append(valid, id)
		}
	}
	if !isValidUUID(orgID) || len(valid) == 0 {
		return shifts, nil
	}
	err := ps.getDB(ctx).Raw(`SELECT group_id, name, priority_shift AS shift FROM groups
		WHERE org_id = ? AND group_id IN ? AND priority_shift <> 0 ORDER BY name`, orgID, valid).Scan(&shifts).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list group priority shifts: %w", err)
	}
	return shifts, nil
}

func (ps PostgresDbStore) SetGroupPriorityShift(ctx context.Context, groupID string, shift int) error {
	if !isValidUUID(groupID) {
		return store.ErrNotFound
	}
	if err := priority.ValidateGroupShift(shift); err != nil {
		return fmt.Errorf("%w: %v", store.ErrInvalidInput, err)
	}
	result := ps.getDB(ctx).Model(&models.Group{}).Where("group_id = ?", groupID).Updates(map[string]any{
		"priority_shift": shift, "updated_at": gorm.Expr("timezone('utc', now())"),
	})
	if result.Error != nil {
		return fmt.Errorf("failed to set group priority shift: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return store.ErrNotFound
	}
	return nil
}

var _ store.PriorityStore = PostgresDbStore{}
