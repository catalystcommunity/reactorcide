package uiapi

import (
	"context"
	"errors"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// GetEvent returns one CI event with the eval's decision for every workflow
// definition it considered.
//
// Visibility follows the event's project, the same rule that makes its
// workflows and jobs visible. An event with no project falls back to its eval
// job. An event the caller cannot view reports "not found", as GetJob does.
func (s *UiService) GetEvent(ctx context.Context, req csilapi.GetEventRequest) (csilapi.GetEventResponse, error) {
	if err := requireNonEmpty("event_id", req.EventId, 64); err != nil {
		return csilapi.GetEventResponse{}, err
	}
	events, ok := s.deps.Store.(store.CIEventStore)
	if !ok {
		return csilapi.GetEventResponse{}, NewServiceError("internal", "CI events are not available on this server")
	}
	event, err := events.GetCIEvent(ctx, req.EventId)
	if err != nil {
		return csilapi.GetEventResponse{}, mapStoreErr(err, "event not found")
	}
	identity, _ := s.deps.resolveIdentity(ctx)
	visible, err := s.canViewEvent(ctx, identity, event)
	if err != nil {
		return csilapi.GetEventResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	if !visible {
		return csilapi.GetEventResponse{}, NewServiceError("not_found", "event not found")
	}
	decisions, err := events.ListCIEventWorkflowDecisions(ctx, event.EventID)
	if err != nil {
		return csilapi.GetEventResponse{}, NewServiceError("internal", "failed to read event decisions")
	}
	response := csilapi.GetEventResponse{
		Event:       ciEventToDetail(event),
		Decisions:   make([]csilapi.CIEventWorkflowDecision, 0, len(decisions)),
		WorkflowIds: []string{},
	}
	for i := range decisions {
		response.Decisions = append(response.Decisions, decisionToCSIL(&decisions[i]))
		if decisions[i].WorkflowID != nil && *decisions[i].WorkflowID != "" {
			response.WorkflowIds = append(response.WorkflowIds, *decisions[i].WorkflowID)
		}
	}
	return response, nil
}

func (s *UiService) canViewEvent(ctx context.Context, identity authz.Identity, event *models.CIEvent) (bool, error) {
	if event.ProjectID != nil && *event.ProjectID != "" {
		project, err := s.deps.Store.GetProjectByID(ctx, *event.ProjectID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return false, nil
			}
			return false, err
		}
		return s.deps.Resolver.CanViewProject(ctx, identity, project)
	}
	if event.EvalJobID != nil && *event.EvalJobID != "" {
		job, err := s.deps.Store.GetJobByID(ctx, *event.EvalJobID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return false, nil
			}
			return false, err
		}
		return s.deps.Resolver.CanViewJob(ctx, identity, job)
	}
	return false, nil
}

// workflowWhy returns the decision that created a workflow, or nil. A missing
// decision is normal (a workflow from before events, or from a direct
// submit), so every failure here means "no explanation", never an error.
func (s *UiService) workflowWhy(ctx context.Context, workflowID string) *csilapi.CIEventWorkflowDecision {
	events, ok := s.deps.Store.(store.CIEventStore)
	if !ok {
		return nil
	}
	decision, err := events.GetCIEventWorkflowDecisionByWorkflow(ctx, workflowID)
	if err != nil || decision == nil {
		return nil
	}
	converted := decisionToCSIL(decision)
	return &converted
}

func ciEventToDetail(event *models.CIEvent) csilapi.CIEventDetail {
	detail := csilapi.CIEventDetail{
		EventId:             event.EventID,
		Source:              event.Source,
		Provider:            event.Provider,
		EventType:           event.EventType,
		Repo:                event.Repo,
		ProjectId:           event.ProjectID,
		Ref:                 optionalText(event.Ref),
		Branch:              optionalText(event.Branch),
		CommitSha:           event.CommitSHA,
		BaseSha:             optionalText(event.BaseSHA),
		Actor:               optionalText(event.Actor),
		Action:              optionalText(event.Summary.Action),
		ScheduleId:          event.ScheduleID,
		ReceivedAt:          formatTime(event.ReceivedAt),
		Status:              event.Status,
		EvalJobId:           event.EvalJobID,
		DuplicateOfEventId:  event.DuplicateOfEventID,
		ChangedFilesCount:   int64(event.Summary.ChangedFilesCount),
		ChangedFilesPreview: append([]string{}, event.Summary.ChangedFilesPreview...),
	}
	if event.PRNumber != nil {
		n := int64(*event.PRNumber)
		detail.PrNumber = &n
	}
	detail.DeliveryId = optionalText(event.DeliveryID)
	detail.ChecksState = optionalText(event.ChecksState)
	detail.ChecksDescription = optionalText(event.ChecksDescription)
	if event.ChecksPublishedAt != nil {
		published := formatTime(*event.ChecksPublishedAt)
		detail.ChecksPublishedAt = &published
	}
	return detail
}

func decisionToCSIL(decision *models.CIEventWorkflowDecision) csilapi.CIEventWorkflowDecision {
	out := csilapi.CIEventWorkflowDecision{
		WorkflowSecurityId: decision.WorkflowSecurityID,
		WorkflowName:       decision.WorkflowName,
		SourceFile:         optionalText(decision.SourceFile),
		Decision:           decision.Decision,
		Reasons:            make([]csilapi.CIDecisionReason, 0, len(decision.Reasons)),
		WorkflowId:         decision.WorkflowID,
	}
	for _, reason := range decision.Reasons {
		out.Reasons = append(out.Reasons, reasonToCSIL(reason))
	}
	return out
}

func reasonToCSIL(reason models.DecisionReason) csilapi.CIDecisionReason {
	out := csilapi.CIDecisionReason{
		Kind:           reason.Kind,
		Matched:        reason.Matched,
		Value:          optionalText(reason.Value),
		Detail:         optionalText(reason.Detail),
		RuleId:         optionalText(reason.RuleID),
		CiOrigin:       optionalText(reason.CIOrigin),
		Profile:        optionalText(reason.Profile),
		WorkerClass:    optionalText(reason.WorkerClass),
		PolicyRevision: optionalText(reason.PolicyRevision),
	}
	if len(reason.Patterns) > 0 {
		out.Patterns = append([]string{}, reason.Patterns...)
	}
	if len(reason.Include) > 0 {
		out.PathsInclude = append([]string{}, reason.Include...)
	}
	if len(reason.Exclude) > 0 {
		out.PathsExclude = append([]string{}, reason.Exclude...)
	}
	if len(reason.MatchedFiles) > 0 {
		out.MatchedFiles = append([]string{}, reason.MatchedFiles...)
	}
	if reason.MatchedCount != nil {
		n := int64(*reason.MatchedCount)
		out.MatchedCount = &n
	}
	if len(reason.Reasons) > 0 {
		out.Reasons = append([]string{}, reason.Reasons...)
	}
	out.Condition = optionalText(reason.Condition)
	for _, dep := range reason.Deps {
		out.Deps = append(out.Deps, csilapi.CIDependencyOutcome{Name: dep.Name, Status: dep.Status})
	}
	out.Required = reason.Required
	out.ScheduleId = optionalText(reason.ScheduleID)
	out.FireTime = optionalText(reason.FireTime)
	return out
}

func optionalText(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
