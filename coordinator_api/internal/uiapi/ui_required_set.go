package uiapi

import (
	"context"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/principals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/requiredset"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// The required set of a project (docs/required-set.md). The authorization
// is in requiredset.Service, the same as for REST and the CLI. A session, a
// user token, a service principal token, and the admin token all work.

type uiProjectByID interface {
	GetProjectByID(ctx context.Context, projectID string) (*models.Project, error)
}

// requiredSetScope resolves the caller and the project.
func (s *UiService) requiredSetScope(ctx context.Context, projectID string) (context.Context, *requiredset.Service, requiredset.Caller, *models.Project, error) {
	ctx, id, user, err := s.deps.requireManagementIdentity(ctx)
	if err != nil {
		return ctx, nil, requiredset.Caller{}, nil, err
	}
	projects, ok := s.deps.Store.(uiProjectByID)
	if !ok {
		return ctx, nil, requiredset.Caller{}, nil, NewServiceError("unavailable", "the required set is not available")
	}
	project, err := projects.GetProjectByID(ctx, strings.TrimSpace(projectID))
	if err != nil {
		return ctx, nil, requiredset.Caller{}, nil, NewServiceError("not_found", "project not found")
	}
	actor := principals.FromCredential(checkauth.GetPrincipalFromContext(ctx), user)
	caller := requiredset.Caller{Identity: id, Actor: actor}
	service := &requiredset.Service{Store: s.deps.Store, Record: func(ctx context.Context, orgID, action, subjectType, subjectID string, details models.JSONB) {
		s.recordAudit(ctx, orgID, action, subjectType, subjectID, details)
	}}
	return ctx, service, caller, project, nil
}

func requiredSetError(err error) error {
	code := requiredset.ErrorCode(err)
	if code == "internal" {
		return NewServiceError("internal", "the required set operation failed")
	}
	return NewServiceError(code, err.Error())
}

func optionalTimeText(value *time.Time) *string {
	if value == nil {
		return nil
	}
	text := formatTime(*value)
	return &text
}

func optionalID(value *string) *string {
	if value == nil || *value == "" {
		return nil
	}
	copied := *value
	return &copied
}

func nameOf(described map[string]principals.Summary, id *string) *string {
	if id == nil {
		return nil
	}
	if summary, ok := described[*id]; ok && summary.DisplayName != "" {
		name := summary.DisplayName
		return &name
	}
	return nil
}

func requiredSetRowToWire(row requiredset.Row, described map[string]principals.Summary) csilapi.RequiredSetWorkflow {
	wire := csilapi.RequiredSetWorkflow{WorkflowSecurityId: row.WorkflowSecurityID, Name: optionalText(row.Name),
		BranchPattern: row.BranchPattern, State: row.State, LastSeen: optionalTimeText(row.LastSeen)}
	if entry := row.Entry; entry != nil {
		added := entry.AddedAt
		wire.InSet, wire.Source, wire.AddedBy, wire.AddedAt = true, optionalText(entry.Source), optionalID(entry.AddedBy), optionalTimeText(&added)
		wire.AddedByName, wire.Reason = nameOf(described, entry.AddedBy), optionalText(entry.Reason)
		if row.State == requiredset.StateQuarantined {
			wire.QuarantinedUntil, wire.QuarantineReason = optionalTimeText(entry.QuarantinedUntil), optionalText(entry.QuarantineReason)
			wire.QuarantinedBy, wire.QuarantinedByName = optionalID(entry.QuarantinedBy), nameOf(described, entry.QuarantinedBy)
		}
	}
	return wire
}

func requiredSetProposalToWire(proposal models.RequiredSetProposal, described map[string]principals.Summary) csilapi.RequiredSetProposal {
	return csilapi.RequiredSetProposal{ProposalId: proposal.ProposalID, WorkflowSecurityId: proposal.WorkflowSecurityID,
		BranchPattern: proposal.BranchPattern, Action: proposal.Action, State: proposal.State, Reason: optionalText(proposal.Reason),
		ProposedBy: optionalID(proposal.ProposedBy), ProposedByName: nameOf(described, proposal.ProposedBy),
		ProposedAt: formatTime(proposal.ProposedAt), DecidedBy: optionalID(proposal.DecidedBy),
		DecidedByName: nameOf(described, proposal.DecidedBy), DecidedAt: optionalTimeText(proposal.DecidedAt),
		DecisionReason: optionalText(proposal.DecisionReason)}
}

func approverRulesToWire(rules []models.RequiredSetApproverRule) []csilapi.RequiredSetApproverRule {
	result := make([]csilapi.RequiredSetApproverRule, 0, len(rules))
	for _, rule := range rules {
		result = append(result, csilapi.RequiredSetApproverRule{Branches: append([]string{}, rule.Branches...), Approvers: append([]string{}, rule.Approvers...)})
	}
	return result
}

func (s *UiService) describeRequiredSet(ctx context.Context, view *requiredset.View) map[string]principals.Summary {
	ids := []string{}
	add := func(id *string) {
		if id != nil && *id != "" {
			ids = append(ids, *id)
		}
	}
	for _, row := range view.Rows {
		if row.Entry != nil {
			add(row.Entry.AddedBy)
			add(row.Entry.QuarantinedBy)
		}
	}
	for _, proposal := range view.Proposals {
		add(proposal.ProposedBy)
		add(proposal.DecidedBy)
	}
	return principals.Describe(ctx, s.deps.Store, ids)
}

func (s *UiService) requiredSetResponse(ctx context.Context, service *requiredset.Service, caller requiredset.Caller, project *models.Project) (csilapi.GetRequiredSetResponse, error) {
	view, err := service.Get(ctx, caller, project)
	if err != nil {
		return csilapi.GetRequiredSetResponse{}, requiredSetError(err)
	}
	described := s.describeRequiredSet(ctx, view)
	response := csilapi.GetRequiredSetResponse{Enabled: view.Enabled, EnabledAt: optionalTimeText(view.EnabledAt),
		Workflows: []csilapi.RequiredSetWorkflow{}, Proposals: []csilapi.RequiredSetProposal{},
		Approvers: append([]string{}, view.Approvers...), ApproversSource: view.ApproversSource,
		ApproverRules: approverRulesToWire(view.ApproverRules), ApproversInherit: view.ApproversInherit,
		ApproverRulesInherit: view.RulesInherit, MaxQuarantineDays: int64(view.MaxQuarantineDays),
		CanPropose: view.Permissions.Propose, CanManage: view.Permissions.Manage,
		CanApproveAllBranches: view.Permissions.ApproveAllBranches}
	for _, row := range view.Rows {
		response.Workflows = append(response.Workflows, requiredSetRowToWire(row, described))
	}
	for _, proposal := range view.Proposals {
		response.Proposals = append(response.Proposals, requiredSetProposalToWire(proposal, described))
	}
	return response, nil
}

func (s *UiService) GetRequiredSet(ctx context.Context, req csilapi.GetRequiredSetRequest) (csilapi.GetRequiredSetResponse, error) {
	ctx, service, caller, project, err := s.requiredSetScope(ctx, req.ProjectId)
	if err != nil {
		return csilapi.GetRequiredSetResponse{}, err
	}
	return s.requiredSetResponse(ctx, service, caller, project)
}

func (s *UiService) SetRequiredSetEnabled(ctx context.Context, req csilapi.SetRequiredSetEnabledRequest) (csilapi.SetRequiredSetEnabledResponse, error) {
	ctx, service, caller, project, err := s.requiredSetScope(ctx, req.ProjectId)
	if err != nil {
		return csilapi.SetRequiredSetEnabledResponse{}, err
	}
	seeded, err := service.SetEnabled(ctx, caller, project, req.Enabled)
	if err != nil {
		return csilapi.SetRequiredSetEnabledResponse{}, requiredSetError(err)
	}
	return csilapi.SetRequiredSetEnabledResponse{Enabled: req.Enabled, Seeded: append([]string{}, seeded...)}, nil
}

func (s *UiService) SetRequiredSetApprovers(ctx context.Context, req csilapi.SetRequiredSetApproversRequest) (csilapi.GetRequiredSetResponse, error) {
	ctx, service, caller, project, err := s.requiredSetScope(ctx, req.ProjectId)
	if err != nil {
		return csilapi.GetRequiredSetResponse{}, err
	}
	var approvers []string
	if !req.InheritApprovers {
		approvers = append([]string{}, req.Approvers...)
	}
	var rules *[]models.RequiredSetApproverRule
	if !req.InheritRules {
		list := make([]models.RequiredSetApproverRule, 0, len(req.ApproverRules))
		for _, rule := range req.ApproverRules {
			list = append(list, models.RequiredSetApproverRule{Branches: append([]string{}, rule.Branches...), Approvers: append([]string{}, rule.Approvers...)})
		}
		rules = &list
	}
	if err := service.SetProjectApprovers(ctx, caller, project, approvers, rules); err != nil {
		return csilapi.GetRequiredSetResponse{}, requiredSetError(err)
	}
	return s.requiredSetResponse(ctx, service, caller, project)
}

func (s *UiService) ProposeRequiredSetChange(ctx context.Context, req csilapi.ProposeRequiredSetChangeRequest) (csilapi.ProposeRequiredSetChangeResponse, error) {
	ctx, service, caller, project, err := s.requiredSetScope(ctx, req.ProjectId)
	if err != nil {
		return csilapi.ProposeRequiredSetChangeResponse{}, err
	}
	proposal, err := service.Propose(ctx, caller, project, requiredset.ProposeRequest{Action: req.Action,
		WorkflowSecurityID: req.WorkflowSecurityId, BranchPattern: textValue(req.BranchPattern), Reason: textValue(req.Reason),
		Approve: req.Approve != nil && *req.Approve})
	if err != nil {
		return csilapi.ProposeRequiredSetChangeResponse{}, requiredSetError(err)
	}
	return csilapi.ProposeRequiredSetChangeResponse{Proposal: requiredSetProposalToWire(*proposal, nil)}, nil
}

func (s *UiService) DecideRequiredSetProposal(ctx context.Context, req csilapi.DecideRequiredSetProposalRequest) (csilapi.DecideRequiredSetProposalResponse, error) {
	ctx, service, caller, project, err := s.requiredSetScope(ctx, req.ProjectId)
	if err != nil {
		return csilapi.DecideRequiredSetProposalResponse{}, err
	}
	state := map[string]string{"approve": models.RequiredSetProposalApproved, "reject": models.RequiredSetProposalRejected,
		"withdraw": models.RequiredSetProposalWithdrawn}[req.Decision]
	if state == "" {
		return csilapi.DecideRequiredSetProposalResponse{}, NewServiceError("invalid_argument", "the decision must be approve, reject, or withdraw")
	}
	proposal, err := service.Decide(ctx, caller, project, req.ProposalId, state, textValue(req.Reason))
	if err != nil {
		return csilapi.DecideRequiredSetProposalResponse{}, requiredSetError(err)
	}
	return csilapi.DecideRequiredSetProposalResponse{Proposal: requiredSetProposalToWire(*proposal, nil)}, nil
}

func (s *UiService) QuarantineRequiredWorkflow(ctx context.Context, req csilapi.QuarantineRequiredWorkflowRequest) (csilapi.QuarantineRequiredWorkflowResponse, error) {
	ctx, service, caller, project, err := s.requiredSetScope(ctx, req.ProjectId)
	if err != nil {
		return csilapi.QuarantineRequiredWorkflowResponse{}, err
	}
	until, err := requiredset.ParseExpiryText(req.Until, time.Now().UTC())
	if err != nil {
		return csilapi.QuarantineRequiredWorkflowResponse{}, NewServiceError("invalid_argument", err.Error())
	}
	entry, err := service.Quarantine(ctx, caller, project, requiredset.QuarantineRequest{WorkflowSecurityID: req.WorkflowSecurityId,
		BranchPattern: textValue(req.BranchPattern), Until: until, Reason: req.Reason})
	if err != nil {
		return csilapi.QuarantineRequiredWorkflowResponse{}, requiredSetError(err)
	}
	row := requiredset.Row{WorkflowSecurityID: entry.WorkflowSecurityID, BranchPattern: entry.BranchPattern,
		State: requiredset.StateQuarantined, Entry: entry}
	return csilapi.QuarantineRequiredWorkflowResponse{Workflow: requiredSetRowToWire(row, nil)}, nil
}

func (s *UiService) UnquarantineRequiredWorkflow(ctx context.Context, req csilapi.UnquarantineRequiredWorkflowRequest) (csilapi.UnquarantineRequiredWorkflowResponse, error) {
	ctx, service, caller, project, err := s.requiredSetScope(ctx, req.ProjectId)
	if err != nil {
		return csilapi.UnquarantineRequiredWorkflowResponse{}, err
	}
	if err := service.Unquarantine(ctx, caller, project, req.WorkflowSecurityId, textValue(req.BranchPattern)); err != nil {
		return csilapi.UnquarantineRequiredWorkflowResponse{}, requiredSetError(err)
	}
	return csilapi.UnquarantineRequiredWorkflowResponse{Done: true}, nil
}
