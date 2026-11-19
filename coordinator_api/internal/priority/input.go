package priority

import (
	"context"
	"fmt"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// LaneMatch is the match facts of a lane in a request (REST, CSIL).
type LaneMatch struct {
	ClassesAny  []string `json:"classes_any,omitempty"`
	ClassesNone []string `json:"classes_none,omitempty"`
	EventTypes  []string `json:"event_types,omitempty"`
	Draft       *bool    `json:"draft,omitempty"`
	JobKind     string   `json:"job_kind,omitempty"`
	Retry       bool     `json:"retry,omitempty"`
}

// LaneInput is a request to create or replace a lane. With no match and no
// position, a lane with the name of a built-in lane (or, for a project, of
// an organization lane) keeps the match and the position of that lane: only
// the base changes. An empty match ({}) matches every job.
type LaneInput struct {
	Base        *int       `json:"base"`
	Position    *int       `json:"position,omitempty"`
	Description *string    `json:"description,omitempty"`
	Match       *LaneMatch `json:"match,omitempty"`
	// Enabled defaults to true.
	Enabled *bool `json:"enabled,omitempty"`
}

// BuildLane builds and checks a stored lane from a request. The error wraps
// store.ErrInvalidInput.
func BuildLane(ctx context.Context, st store.PriorityStore, orgID string, projectID *string, name string, input LaneInput) (models.PriorityLane, error) {
	name = strings.TrimSpace(name)
	if input.Base == nil {
		return models.PriorityLane{}, fmt.Errorf("%w: base is required", store.ErrInvalidInput)
	}
	lane := models.PriorityLane{OrgID: orgID, ProjectID: projectID, Name: name, Base: *input.Base, Enabled: true,
		ClassesAny: []string{}, ClassesNone: []string{}, EventTypes: []string{}}
	if input.Enabled != nil {
		lane.Enabled = *input.Enabled
	}
	if input.Description != nil {
		lane.Description = strings.TrimSpace(*input.Description)
	}
	if input.Match == nil || input.Position == nil {
		replaced, ok := ReplacedLane(ctx, st, orgID, projectID, name)
		if !ok && input.Match == nil {
			return models.PriorityLane{}, fmt.Errorf("%w: a new lane needs a match (an empty match is every job)", store.ErrInvalidInput)
		}
		if ok {
			if input.Match == nil {
				lane.ClassesAny, lane.ClassesNone, lane.EventTypes = replaced.ClassesAny, replaced.ClassesNone, replaced.EventTypes
				lane.Draft, lane.JobKind, lane.Retry = replaced.Draft, replaced.JobKind, replaced.Retry
			}
			if input.Position == nil {
				lane.Position = replaced.Position
			}
			if input.Description == nil {
				lane.Description = replaced.Description
			}
		}
	}
	if input.Position != nil {
		lane.Position = *input.Position
	}
	if input.Match != nil {
		lane.ClassesAny = nonNil(input.Match.ClassesAny)
		lane.ClassesNone = nonNil(input.Match.ClassesNone)
		lane.EventTypes = nonNil(input.Match.EventTypes)
		lane.Draft, lane.JobKind, lane.Retry = input.Match.Draft, strings.TrimSpace(input.Match.JobKind), input.Match.Retry
	}
	if err := ValidateLane(lane); err != nil {
		return models.PriorityLane{}, fmt.Errorf("%w: %v", store.ErrInvalidInput, err)
	}
	return lane, nil
}

// ReplacedLane returns the lane that a lane with this name replaces in the
// scope: the organization lane for a project lane, else the built-in lane.
// A stored lane of the same scope is not used: a PUT replaces it.
func ReplacedLane(ctx context.Context, st store.PriorityStore, orgID string, projectID *string, name string) (models.PriorityLane, bool) {
	if projectID != nil && st != nil {
		if orgLane, err := st.GetPriorityLane(ctx, orgID, nil, name); err == nil {
			return *orgLane, true
		}
	}
	if builtin, ok := BuiltinByName(name); ok {
		return builtin.PriorityLane, true
	}
	return models.PriorityLane{}, false
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
