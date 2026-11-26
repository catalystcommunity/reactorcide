package uiapi

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/approvals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/profiles"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
	"github.com/lib/pq"
)

type uiTrustStore interface {
	store.CITrustSettingsStore
	store.CIApprovalStore
	GetOrganizationByName(context.Context, string) (*models.Organization, error)
	GetExecutionProfile(context.Context, string, string) (*models.ExecutionProfile, error)
}

func (s *UiService) trustStore() (uiTrustStore, error) {
	value, ok := s.deps.Store.(uiTrustStore)
	if !ok {
		return nil, NewServiceError("internal", "CI trust settings are not available")
	}
	return value, nil
}

func (s *UiService) visibleProject(ctx context.Context, id authz.Identity, projectID string) (*models.Project, error) {
	if err := requireNonEmpty("project_id", projectID, 64); err != nil {
		return nil, err
	}
	project, err := s.deps.Store.GetProjectByID(ctx, projectID)
	if err != nil {
		return nil, mapStoreErr(err, "project not found")
	}
	visible, err := s.deps.Resolver.CanViewProject(ctx, id, project)
	if err != nil {
		return nil, NewServiceError("internal", "an internal error occurred")
	}
	if !visible {
		return nil, NewServiceError("not_found", "project not found")
	}
	return project, nil
}

func approvalActionToWire(action models.CIApproval) csilapi.CiApprovalAction {
	wire := csilapi.CiApprovalAction{ApprovalId: action.ApprovalID, Kind: action.Kind, Action: action.Action,
		HeadSha: action.HeadSHA, Person: action.Person, Subjects: append([]string{}, action.Subjects...),
		Channel: action.Channel, CreatedAt: action.CreatedAt.UTC().Format(time.RFC3339)}
	if action.Label != "" {
		label := action.Label
		wire.Label = &label
	}
	return wire
}

// PrCommand runs a pull request command through the one approval path. The
// caller acts as their Reactorcide user, or as their linked VCS account.
func (s *UiService) PrCommand(ctx context.Context, req csilapi.PrCommandRequest) (csilapi.PrCommandResponse, error) {
	ctx, id, _, authErr := s.deps.requireManagementIdentity(ctx)
	if authErr != nil {
		return csilapi.PrCommandResponse{}, authErr
	}
	// The person: a user (a session or a user token), or a service
	// principal token, as in REST. A global token (the admin token, or a
	// wide service principal token issued on a no-login instance) acts as
	// the instance administrator in every auth mode: it needs no user.
	instanceAdmin := id.Token.IsWideInstanceToken()
	if id.UserID == "" && id.PrincipalID == "" && !instanceAdmin {
		return csilapi.PrCommandResponse{}, NewServiceError("forbidden", "a pull request command needs a person: use a session, a user token, or a service principal token")
	}
	project, err := s.visibleProject(ctx, id, req.ProjectId)
	if err != nil {
		return csilapi.PrCommandResponse{}, err
	}
	// A token needs workflows:control for the organization of the project,
	// as the REST approvals route.
	orgID := project.OwnershipOrgID()
	if err := requireTokenGrant(id, &orgID, tokencaps.WorkflowsControl); err != nil {
		return csilapi.PrCommandResponse{}, err
	}
	if s.deps.Approvals == nil {
		return csilapi.PrCommandResponse{}, NewServiceError("unavailable", "pull request commands are not available")
	}
	if req.PrNumber <= 0 {
		return csilapi.PrCommandResponse{}, NewServiceError("invalid_argument", "pr_number must be positive")
	}
	command := approvals.Command{Name: strings.TrimSpace(req.Command)}
	if req.Label != nil {
		command.Label = strings.TrimSpace(*req.Label)
	}
	if req.Shifts != nil {
		if command.Name != approvals.CommandRetry && command.Name != approvals.CommandRerun {
			return csilapi.PrCommandResponse{}, NewServiceError("invalid_argument", "only retry and rerun take shifts")
		}
		shifts, err := shiftsFromWire(req.Shifts)
		if err != nil {
			return csilapi.PrCommandResponse{}, err
		}
		command.Shifts = shifts
	}
	request := approvals.Request{Project: project, PRNumber: int(req.PrNumber), Command: command,
		Channel: models.CIApprovalChannelUI, UserID: id.UserID, PrincipalID: id.PrincipalID, InstanceAdmin: instanceAdmin}
	if instanceAdmin {
		request.PrincipalID = id.Token.PrincipalID
		if request.PrincipalID == "" {
			request.TokenID = id.Token.CredentialID
		}
	}
	if req.HeadSha != nil {
		request.HeadSHA = strings.TrimSpace(*req.HeadSha)
	}
	result, err := s.deps.Approvals.Execute(ctx, request)
	if err != nil {
		return csilapi.PrCommandResponse{}, mapApprovalErr(err)
	}
	response := csilapi.PrCommandResponse{Message: result.Message, Recorded: []csilapi.CiApprovalAction{}}
	if result.Effect != "" {
		effect := result.Effect
		response.Effect = &effect
	}
	for _, action := range result.Recorded {
		response.Recorded = append(response.Recorded, approvalActionToWire(action))
	}
	return response, nil
}

func mapApprovalErr(err error) error {
	switch {
	case errors.Is(err, approvals.ErrNotAllowed):
		return NewServiceError("forbidden", err.Error())
	case errors.Is(err, approvals.ErrHeadChanged):
		return NewServiceError("conflict", err.Error())
	case errors.Is(err, approvals.ErrPullRequestClosed):
		return NewServiceError("failed_precondition", err.Error())
	}
	return NewServiceError("invalid_argument", err.Error())
}

// GetPrApprovals returns the approval actions and the required-review state
// of the current head of a pull request.
func (s *UiService) GetPrApprovals(ctx context.Context, req csilapi.GetPrApprovalsRequest) (csilapi.GetPrApprovalsResponse, error) {
	ctx, id, _, authErr := s.deps.requireManagementIdentity(ctx)
	if authErr != nil {
		return csilapi.GetPrApprovalsResponse{}, authErr
	}
	project, err := s.visibleProject(ctx, id, req.ProjectId)
	if err != nil {
		return csilapi.GetPrApprovalsResponse{}, err
	}
	trust, err := s.trustStore()
	if err != nil {
		return csilapi.GetPrApprovalsResponse{}, err
	}
	if s.deps.Approvals == nil {
		return csilapi.GetPrApprovalsResponse{}, NewServiceError("unavailable", "pull request approvals are not available")
	}
	reviews, err := s.deps.Approvals.HeadReviews(ctx, project, int(req.PrNumber))
	if err != nil {
		return csilapi.GetPrApprovalsResponse{}, NewServiceError("unavailable", "could not read the pull request: "+err.Error())
	}
	actions, err := trust.ListCIApprovalsForHead(ctx, project.ProjectID, int(req.PrNumber), reviews.HeadSHA)
	if err != nil {
		return csilapi.GetPrApprovalsResponse{}, NewServiceError("internal", "failed to read approvals")
	}
	response := csilapi.GetPrApprovalsResponse{HeadSha: reviews.HeadSHA, Actions: []csilapi.CiApprovalAction{},
		Reviews: []csilapi.RequiredReviewState{}}
	for _, action := range actions {
		response.Actions = append(response.Actions, approvalActionToWire(action))
	}
	for _, review := range reviews.Reviews {
		state := csilapi.RequiredReviewState{Id: review.ID, Scope: review.Scope, Applies: review.Applies,
			Required: int64(review.Required), Accepted: append([]string{}, review.Accepted...), Satisfied: review.Satisfied,
			Accept: append([]string{}, review.Accept...)}
		if review.Description != "" {
			description := review.Description
			state.Description = &description
		}
		response.Reviews = append(response.Reviews, state)
	}
	if reviews.State != "" {
		value, summary := reviews.State, reviews.Summary
		response.ReviewsState, response.ReviewsSummary = &value, &summary
	}
	return response, nil
}

// trustScope resolves the organization and, for a project request, the
// project of a settings call, and checks that the caller manages them.
// canLoosen is true for an org admin and for an API token with policies:manage.
func (s *UiService) trustScope(ctx context.Context, id authz.Identity, store uiTrustStore, organization string, projectID *string) (*models.Organization, *models.Project, bool, error) {
	org, err := store.GetOrganizationByName(ctx, organization)
	if err != nil {
		return nil, nil, false, mapStoreErr(err, "organization not found")
	}
	orgAdmin, err := s.deps.Resolver.IsOrgAdmin(ctx, id, org.OrgID)
	if err != nil {
		return nil, nil, false, NewServiceError("internal", "an internal error occurred")
	}
	// An API token with policies:manage for the organization manages the
	// settings too, like the CI policy (for example an admin token when there
	// are no users).
	policyToken := id.Token != nil && id.Token.HasOrganization(org.OrgID) && id.Token.HasCapability(tokencaps.PoliciesManage)
	// A token never exceeds itself: a token with an owner needs both the
	// role and policies:manage; a token with no owner (the admin token)
	// acts by policies:manage alone.
	if id.Token != nil {
		if tokenHasNoRoles(id) {
			orgAdmin = false
		} else {
			orgAdmin = orgAdmin && policyToken
			policyToken = false
		}
	}
	if projectID == nil {
		if !orgAdmin && !policyToken {
			return nil, nil, false, NewServiceError("forbidden", "organization CI trust settings need an organization administrator")
		}
		return org, nil, true, nil
	}
	project, err := s.deps.Store.GetProjectByID(ctx, *projectID)
	if err != nil || project.OwnershipOrgID() != org.OrgID {
		return nil, nil, false, NewServiceError("not_found", "project not found")
	}
	if err := s.deps.Resolver.RequireProjectPolicyManager(ctx, id, project.ProjectID); err != nil {
		return nil, nil, false, mapPermissionErr(err)
	}
	return org, project, orgAdmin || policyToken, nil
}

func trustToWire(row *models.CITrustSettings) csilapi.CiTrustSettings {
	wire := csilapi.CiTrustSettings{}
	if row == nil {
		return wire
	}
	if row.RunGate != nil {
		wire.RunGate = append([]string{}, row.RunGate...)
	}
	wire.GatedRunAuthority = row.GatedRunAuthority
	if row.RunApprovers != nil {
		wire.RunApprovers = append([]string{}, row.RunApprovers...)
	}
	if row.OverrideApprovers != nil {
		wire.OverrideApprovers = append([]string{}, row.OverrideApprovers...)
	}
	wire.OverrideProfile = row.OverrideProfile
	if row.RequiredReviews != nil {
		wire.RequiredReviews = []csilapi.RequiredReview{}
		for _, review := range *row.RequiredReviews {
			wire.RequiredReviews = append(wire.RequiredReviews, reviewToWire(review, ""))
		}
	}
	wire.RerunShiftLimits = rerunLimitsToWire(row.RerunShiftLimits)
	return wire
}

func reviewToWire(review models.RequiredReview, scope string) csilapi.RequiredReview {
	wire := csilapi.RequiredReview{Id: review.ID, Accept: append([]string{}, review.Accept...)}
	if review.Description != "" {
		description := review.Description
		wire.Description = &description
	}
	if len(review.Paths) > 0 {
		wire.Paths = append([]string{}, review.Paths...)
	}
	if review.Count > 0 {
		count := int64(review.Count)
		wire.Count = &count
	}
	if scope != "" {
		wire.Scope = &scope
	}
	return wire
}

func effectiveToWire(effective models.EffectiveTrustSettings) csilapi.CiTrustSettings {
	authority, profile := effective.GatedRunAuthority, effective.OverrideProfile
	wire := csilapi.CiTrustSettings{RunGate: append([]string{}, effective.RunGate...), GatedRunAuthority: &authority,
		RunApprovers: append([]string{}, effective.RunApprovers...), OverrideApprovers: append([]string{}, effective.OverrideApprovers...),
		OverrideProfile: &profile, RequiredReviews: []csilapi.RequiredReview{}}
	for _, review := range effective.RequiredReviews {
		wire.RequiredReviews = append(wire.RequiredReviews, reviewToWire(review.RequiredReview, review.Scope))
	}
	limits := effective.RerunShiftLimits
	wire.RerunShiftLimits = rerunLimitsToWire(&limits)
	return wire
}

func trustFromWire(wire csilapi.CiTrustSettings) *models.CITrustSettings {
	row := &models.CITrustSettings{GatedRunAuthority: wire.GatedRunAuthority, OverrideProfile: wire.OverrideProfile,
		RerunShiftLimits: rerunLimitsFromWire(wire.RerunShiftLimits)}
	if wire.RunGate != nil {
		row.RunGate = pq.StringArray(append([]string{}, wire.RunGate...))
	}
	if wire.RunApprovers != nil {
		row.RunApprovers = pq.StringArray(append([]string{}, wire.RunApprovers...))
	}
	if wire.OverrideApprovers != nil {
		row.OverrideApprovers = pq.StringArray(append([]string{}, wire.OverrideApprovers...))
	}
	if wire.RequiredReviews != nil {
		reviews := make([]models.RequiredReview, 0, len(wire.RequiredReviews))
		for _, review := range wire.RequiredReviews {
			value := models.RequiredReview{ID: strings.TrimSpace(review.Id), Paths: review.Paths, Accept: review.Accept}
			if review.Description != nil {
				value.Description = *review.Description
			}
			if review.Count != nil {
				value.Count = int(*review.Count)
			}
			reviews = append(reviews, value)
		}
		row.RequiredReviews = &reviews
	}
	return row
}

func (s *UiService) GetCiTrustSettings(ctx context.Context, req csilapi.GetCiTrustSettingsRequest) (csilapi.GetCiTrustSettingsResponse, error) {
	ctx, id, _, authErr := s.deps.requireManagementIdentity(ctx)
	if authErr != nil {
		return csilapi.GetCiTrustSettingsResponse{}, authErr
	}
	trust, err := s.trustStore()
	if err != nil {
		return csilapi.GetCiTrustSettingsResponse{}, err
	}
	org, project, canLoosen, err := s.trustScope(ctx, id, trust, req.Organization, req.ProjectId)
	if err != nil {
		return csilapi.GetCiTrustSettingsResponse{}, err
	}
	orgRow, err := trust.GetCITrustSettings(ctx, org.OrgID, nil)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return csilapi.GetCiTrustSettingsResponse{}, NewServiceError("internal", "failed to read CI trust settings")
	}
	response := csilapi.GetCiTrustSettingsResponse{CanLoosen: canLoosen}
	if project == nil {
		response.Settings = trustToWire(orgRow)
		response.Effective = effectiveToWire(models.EffectiveTrust(orgRow, nil))
		return response, nil
	}
	projectRow, err := trust.GetCITrustSettings(ctx, org.OrgID, &project.ProjectID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return csilapi.GetCiTrustSettingsResponse{}, NewServiceError("internal", "failed to read CI trust settings")
	}
	response.Settings = trustToWire(projectRow)
	response.Effective = effectiveToWire(models.EffectiveTrust(orgRow, projectRow))
	orgEffective := effectiveToWire(models.EffectiveTrust(orgRow, nil))
	response.OrganizationEffective = &orgEffective
	return response, nil
}

func (s *UiService) PutCiTrustSettings(ctx context.Context, req csilapi.PutCiTrustSettingsRequest) (csilapi.PutCiTrustSettingsResponse, error) {
	ctx, id, user, authErr := s.deps.requireManagementIdentity(ctx)
	if authErr != nil {
		return csilapi.PutCiTrustSettingsResponse{}, authErr
	}
	trust, err := s.trustStore()
	if err != nil {
		return csilapi.PutCiTrustSettingsResponse{}, err
	}
	org, project, canLoosen, err := s.trustScope(ctx, id, trust, req.Organization, req.ProjectId)
	if err != nil {
		return csilapi.PutCiTrustSettingsResponse{}, err
	}
	row := trustFromWire(req.Settings)
	row.OrgID = org.OrgID
	if user != nil {
		row.UpdatedBy = user.UserID
	} else if id.Token != nil {
		row.UpdatedBy = "token"
	}
	if err := row.Validate(); err != nil {
		return csilapi.PutCiTrustSettingsResponse{}, NewServiceError("invalid_argument", err.Error())
	}
	for _, name := range []*string{row.OverrideProfile} {
		if name != nil {
			if _, err := trust.GetExecutionProfile(ctx, org.OrgID, *name); err != nil {
				return csilapi.PutCiTrustSettingsResponse{}, NewServiceError("invalid_argument", "override_profile names an unknown profile")
			}
		}
	}
	orgRow, err := trust.GetCITrustSettings(ctx, org.OrgID, nil)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return csilapi.PutCiTrustSettingsResponse{}, NewServiceError("internal", "failed to read CI trust settings")
	}
	effective := models.EffectiveTrust(row, nil)
	if project != nil {
		projectID := project.ProjectID
		row.ProjectID = &projectID
		effective = models.EffectiveTrust(orgRow, row)
		if !canLoosen {
			weaker := func(a, b string) bool {
				child, childErr := trust.GetExecutionProfile(ctx, org.OrgID, a)
				parent, parentErr := trust.GetExecutionProfile(ctx, org.OrgID, b)
				return childErr == nil && parentErr == nil && profiles.WeakerOrEqual(child, parent) == nil
			}
			if reasons := models.LooserThan(effective, models.EffectiveTrust(orgRow, nil), weaker); len(reasons) > 0 {
				return csilapi.PutCiTrustSettingsResponse{}, NewServiceError("forbidden",
					"only an organization administrator can make project settings less strict than the organization: "+strings.Join(reasons, "; "))
			}
		}
	}
	if err := trust.PutCITrustSettings(ctx, row); err != nil {
		return csilapi.PutCiTrustSettingsResponse{}, NewServiceError("invalid_argument", err.Error())
	}
	subjectType, subjectID := "organization", org.OrgID
	if project != nil {
		subjectType, subjectID = "project", project.ProjectID
	}
	s.recordAudit(ctx, org.OrgID, "ci_trust_settings.update", subjectType, subjectID, models.JSONB{
		"run_gate": []string(row.RunGate), "gated_run_authority": row.GatedRunAuthority,
		"run_approvers": []string(row.RunApprovers), "override_approvers": []string(row.OverrideApprovers),
		"override_profile": row.OverrideProfile, "required_reviews": row.RequiredReviews})
	return csilapi.PutCiTrustSettingsResponse{Settings: trustToWire(row), Effective: effectiveToWire(effective)}, nil
}
