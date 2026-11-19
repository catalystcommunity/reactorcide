package uiapi

import (
	"context"
	"math"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/principals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/reporting"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// Report modes, the agreement report, and path acceptances
// (docs/reporting-and-ownership.md). The authorization is in
// reporting.Service, the same as for REST and the CLI.

func (s *UiService) reportingScope(ctx context.Context, projectID string) (context.Context, *reporting.Service, reporting.Caller, *models.Project, error) {
	ctx, id, user, err := s.deps.requireManagementIdentity(ctx)
	if err != nil {
		return ctx, nil, reporting.Caller{}, nil, err
	}
	projects, ok := s.deps.Store.(uiProjectByID)
	if !ok {
		return ctx, nil, reporting.Caller{}, nil, NewServiceError("unavailable", "reporting settings are not available")
	}
	project, err := projects.GetProjectByID(ctx, strings.TrimSpace(projectID))
	if err != nil {
		return ctx, nil, reporting.Caller{}, nil, NewServiceError("not_found", "project not found")
	}
	caller := reporting.Caller{Identity: id, Actor: principals.FromCredential(checkauth.GetPrincipalFromContext(ctx), user)}
	service := &reporting.Service{Store: s.deps.Store, Record: func(ctx context.Context, orgID, action, subjectType, subjectID string, details models.JSONB) {
		s.recordAudit(ctx, orgID, action, subjectType, subjectID, details)
	}}
	return ctx, service, caller, project, nil
}

func reportingError(err error) error {
	code := reporting.ErrorCode(err)
	if code == "internal" {
		return NewServiceError("internal", "the reporting operation failed")
	}
	return NewServiceError(code, err.Error())
}

func pathOwnersToWire(rules []models.PathOwnerRule) []csilapi.PathOwnerRule {
	out := make([]csilapi.PathOwnerRule, 0, len(rules))
	for _, rule := range rules {
		out = append(out, csilapi.PathOwnerRule{Pattern: rule.Pattern, Owners: append([]string{}, rule.Owners...)})
	}
	return out
}

func pathOwnersFromWire(rules []csilapi.PathOwnerRule) []models.PathOwnerRule {
	out := make([]models.PathOwnerRule, 0, len(rules))
	for _, rule := range rules {
		out = append(out, models.PathOwnerRule{Pattern: rule.Pattern, Owners: append([]string{}, rule.Owners...)})
	}
	return out
}

func reportingSettingsToWire(view *reporting.SettingsView) csilapi.GetReportingSettingsResponse {
	e := view.Effective
	out := csilapi.GetReportingSettingsResponse{
		Effective: csilapi.ReportingSettings{ReportMode: e.ReportMode, ReportModeSource: e.ReportModeSource,
			AgreementContexts: append([]string{}, e.AgreementContexts...), AgreementContextsSource: e.AgreementContextsSource,
			CodeownersImport: e.CodeownersImport, CodeownersImportSource: e.CodeownersImportSource,
			CodeownersCacheTtlSeconds: int64(e.CodeownersCacheTTLSeconds), PathAcceptance: e.PathAcceptance,
			PathAcceptanceSource: e.PathAcceptanceSource, PathOwners: pathOwnersToWire(e.PathOwners), PathOwnersSource: e.PathOwnersSource},
		CanManageReporting: view.Permissions.ManageReporting, CanManageOwnership: view.Permissions.ManageOwnership,
	}
	if p := view.Project; p != nil {
		out.ProjectReportMode, out.ProjectCodeownersImport, out.ProjectPathAcceptance = p.ReportMode, p.CodeownersImport, p.PathAcceptance
		if p.AgreementContexts != nil {
			out.ProjectAgreementContexts = append([]string{}, (*p.AgreementContexts)...)
		}
		if p.CodeownersCacheTTLSeconds != nil {
			value := int64(*p.CodeownersCacheTTLSeconds)
			out.ProjectCodeownersCacheTtlSeconds = &value
		}
		if p.PathOwners != nil {
			out.ProjectPathOwners = pathOwnersToWire(*p.PathOwners)
		}
	}
	return out
}

// GetReportingSettings returns the reporting settings of a project.
func (s *UiService) GetReportingSettings(ctx context.Context, req csilapi.GetReportingSettingsRequest) (csilapi.GetReportingSettingsResponse, error) {
	ctx, service, caller, project, err := s.reportingScope(ctx, req.ProjectId)
	if err != nil {
		return csilapi.GetReportingSettingsResponse{}, err
	}
	view, err := service.GetSettings(ctx, caller, project)
	if err != nil {
		return csilapi.GetReportingSettingsResponse{}, reportingError(err)
	}
	return reportingSettingsToWire(view), nil
}

// UpdateReportingSettings changes the reporting settings of a project.
func (s *UiService) UpdateReportingSettings(ctx context.Context, req csilapi.UpdateReportingSettingsRequest) (csilapi.GetReportingSettingsResponse, error) {
	ctx, service, caller, project, err := s.reportingScope(ctx, req.ProjectId)
	if err != nil {
		return csilapi.GetReportingSettingsResponse{}, err
	}
	update := reporting.ProjectSettingsUpdate{ReportMode: req.ReportMode, CodeownersImport: req.CodeownersImport,
		PathAcceptance: req.PathAcceptance, Inherit: req.Inherit}
	if req.AgreementContexts != nil {
		contexts := append([]string{}, req.AgreementContexts...)
		update.AgreementContexts = &contexts
	}
	if req.CodeownersCacheTtlSeconds != nil {
		value := int(*req.CodeownersCacheTtlSeconds)
		update.CodeownersCacheTTLSeconds = &value
	}
	if req.PathOwners != nil {
		rules := pathOwnersFromWire(req.PathOwners)
		update.PathOwners = &rules
	}
	view, err := service.UpdateProjectSettings(ctx, caller, project, update)
	if err != nil {
		return csilapi.GetReportingSettingsResponse{}, reportingError(err)
	}
	return reportingSettingsToWire(view), nil
}

// CommitURL returns the web link of a commit, or "" when the provider is
// not known.
func CommitURL(provider, repo, sha string) string {
	if repo == "" || sha == "" {
		return ""
	}
	switch strings.ToLower(provider) {
	case "github":
		return "https://github.com/" + repo + "/commit/" + sha
	case "gitlab":
		return "https://gitlab.com/" + repo + "/-/commit/" + sha
	}
	return ""
}

func agreementRowToWire(row models.CIAgreement) csilapi.AgreementRow {
	out := csilapi.AgreementRow{CommitSha: row.CommitSHA, EventId: optionalID(row.EventID), Branch: optionalText(row.Branch),
		ReactorcideResult: row.ReactorcideResult, OtherResult: optionalText(row.OtherResult), Agree: row.Agree, Status: row.Status,
		ReactorcideCompletedAt: formatTime(row.ReactorcideCompletedAt), ObservedAt: formatTimePtr(row.ObservedAt),
		LastError: optionalText(row.LastError), CommitUrl: optionalText(CommitURL(row.Provider, row.Repository, row.CommitSHA)),
		OtherResults: []csilapi.AgreementOtherResult{}}
	if row.PRNumber != nil {
		value := int64(*row.PRNumber)
		out.PrNumber = &value
	}
	for _, other := range row.OtherResults {
		out.OtherResults = append(out.OtherResults, csilapi.AgreementOtherResult{Name: other.Name, Kind: other.Kind, State: other.State,
			Raw: optionalText(other.Raw), TargetUrl: optionalText(other.TargetURL)})
	}
	return out
}

// GetAgreementReport returns the agreement report of a project.
func (s *UiService) GetAgreementReport(ctx context.Context, req csilapi.GetAgreementReportRequest) (csilapi.GetAgreementReportResponse, error) {
	ctx, service, caller, project, err := s.reportingScope(ctx, req.ProjectId)
	if err != nil {
		return csilapi.GetAgreementReportResponse{}, err
	}
	var since *time.Time
	if req.Since != nil && strings.TrimSpace(*req.Since) != "" {
		value := strings.TrimSpace(*req.Since)
		if parsed, parseErr := time.Parse(time.RFC3339, value); parseErr == nil {
			since = &parsed
		} else if duration, ok := parseDays(value); ok {
			at := time.Now().UTC().Add(-duration)
			since = &at
		} else {
			return csilapi.GetAgreementReportResponse{}, NewServiceError("invalid_argument", "since must be an RFC 3339 time or a duration")
		}
	}
	limit := 0
	if req.Limit != nil {
		limit = int(*req.Limit)
	}
	view, err := service.GetAgreement(ctx, caller, project, since, limit)
	if err != nil {
		return csilapi.GetAgreementReportResponse{}, reportingError(err)
	}
	summary := view.Summary
	out := csilapi.GetAgreementReportResponse{Contexts: append([]string{}, view.Contexts...), Total: int64(summary.Total),
		Compared: int64(summary.Compared), Agreed: int64(summary.Agreed), Disagreed: int64(summary.Disagreed),
		Pending: int64(summary.Pending), NoOther: int64(summary.NoOther),
		ReactorcideFailedOtherPassed: int64(summary.FalseFailures), ReactorcidePassedOtherFailed: int64(summary.MissedFailures),
		Disagreements: []csilapi.AgreementRow{}, Rows: []csilapi.AgreementRow{}}
	if summary.Rate != nil {
		points := int64(math.Round(*summary.Rate * 10000))
		out.RateBasisPoints = &points
	}
	for _, row := range summary.Disagreements {
		out.Disagreements = append(out.Disagreements, agreementRowToWire(row))
	}
	for _, row := range view.Rows {
		out.Rows = append(out.Rows, agreementRowToWire(row))
	}
	return out, nil
}

// parseDays reads "7d" or a Go duration ("72h").
func parseDays(value string) (time.Duration, bool) {
	if days, ok := strings.CutSuffix(value, "d"); ok {
		var count int
		for _, c := range days {
			if c < '0' || c > '9' {
				return 0, false
			}
			count = count*10 + int(c-'0')
			if count > 3650 {
				return 0, false
			}
		}
		if days == "" {
			return 0, false
		}
		return time.Duration(count) * 24 * time.Hour, true
	}
	duration, err := time.ParseDuration(value)
	return duration, err == nil && duration >= 0
}

func pathAcceptanceToWire(row models.PathAcceptance, described map[string]principals.Summary) csilapi.PathAcceptanceSummary {
	return csilapi.PathAcceptanceSummary{AcceptanceId: row.AcceptanceID, WorkflowSecurityId: row.WorkflowSecurityID,
		PathPattern: row.PathPattern, OwnerSubject: row.OwnerSubject, State: row.State, Reason: optionalText(row.Reason),
		RequestedBy: optionalID(row.RequestedBy), RequestedByName: nameOf(described, row.RequestedBy), RequestedAt: formatTimePtr(row.RequestedAt),
		AcceptedBy: optionalID(row.AcceptedBy), AcceptedByName: nameOf(described, row.AcceptedBy), AcceptedAt: formatTimePtr(row.AcceptedAt),
		RevokedBy: optionalID(row.RevokedBy), RevokedByName: nameOf(described, row.RevokedBy), RevokedAt: formatTimePtr(row.RevokedAt),
		DecisionReason: optionalText(row.DecisionReason)}
}

func (s *UiService) describeAcceptances(ctx context.Context, rows []models.PathAcceptance) map[string]principals.Summary {
	ids := []string{}
	for _, row := range rows {
		for _, id := range []*string{row.RequestedBy, row.AcceptedBy, row.RevokedBy} {
			if id != nil && *id != "" {
				ids = append(ids, *id)
			}
		}
	}
	if len(ids) == 0 {
		return map[string]principals.Summary{}
	}
	return principals.Describe(ctx, s.deps.Store, ids)
}

// ListPathAcceptances lists the path acceptances of a project.
func (s *UiService) ListPathAcceptances(ctx context.Context, req csilapi.ListPathAcceptancesRequest) (csilapi.ListPathAcceptancesResponse, error) {
	ctx, service, caller, project, err := s.reportingScope(ctx, req.ProjectId)
	if err != nil {
		return csilapi.ListPathAcceptancesResponse{}, err
	}
	state := ""
	if req.State != nil {
		state = strings.TrimSpace(*req.State)
	}
	view, err := service.ListAcceptances(ctx, caller, project, state)
	if err != nil {
		return csilapi.ListPathAcceptancesResponse{}, reportingError(err)
	}
	described := s.describeAcceptances(ctx, view.Rows)
	out := csilapi.ListPathAcceptancesResponse{PathAcceptance: view.Mode, OwnershipSource: view.Source,
		Rows: []csilapi.PathAcceptanceSummary{}, CanManageOwnership: view.Permissions.ManageOwnership}
	for _, row := range view.Rows {
		out.Rows = append(out.Rows, pathAcceptanceToWire(row, described))
	}
	return out, nil
}

// RequestPathAcceptance stores a request (or an acceptance by an owner).
func (s *UiService) RequestPathAcceptance(ctx context.Context, req csilapi.RequestPathAcceptanceRequest) (csilapi.PathAcceptanceResponse, error) {
	ctx, service, caller, project, err := s.reportingScope(ctx, req.ProjectId)
	if err != nil {
		return csilapi.PathAcceptanceResponse{}, err
	}
	request := reporting.AcceptanceRequest{WorkflowSecurityID: req.WorkflowSecurityId, PathPattern: req.PathPattern,
		OwnerSubject: req.OwnerSubject}
	if req.Reason != nil {
		request.Reason = *req.Reason
	}
	if req.Accept != nil {
		request.Accept = *req.Accept
	}
	row, err := service.RequestAcceptance(ctx, caller, project, request)
	if err != nil {
		return csilapi.PathAcceptanceResponse{}, reportingError(err)
	}
	return csilapi.PathAcceptanceResponse{Acceptance: pathAcceptanceToWire(*row, s.describeAcceptances(ctx, []models.PathAcceptance{*row}))}, nil
}

// DecidePathAcceptance accepts, rejects, or revokes a row.
func (s *UiService) DecidePathAcceptance(ctx context.Context, req csilapi.DecidePathAcceptanceRequest) (csilapi.PathAcceptanceResponse, error) {
	ctx, service, caller, project, err := s.reportingScope(ctx, req.ProjectId)
	if err != nil {
		return csilapi.PathAcceptanceResponse{}, err
	}
	reason := ""
	if req.Reason != nil {
		reason = *req.Reason
	}
	row, err := service.DecideAcceptance(ctx, caller, project, req.AcceptanceId, strings.TrimSpace(req.Decision), reason)
	if err != nil {
		return csilapi.PathAcceptanceResponse{}, reportingError(err)
	}
	return csilapi.PathAcceptanceResponse{Acceptance: pathAcceptanceToWire(*row, s.describeAcceptances(ctx, []models.PathAcceptance{*row}))}, nil
}

// Stubs of the reporting operations.

func (StubUi) GetReportingSettings(ctx context.Context, req csilapi.GetReportingSettingsRequest) (csilapi.GetReportingSettingsResponse, error) {
	return csilapi.GetReportingSettingsResponse{}, ErrUnimplemented("ReactorcideUi/get-reporting-settings")
}

func (StubUi) UpdateReportingSettings(ctx context.Context, req csilapi.UpdateReportingSettingsRequest) (csilapi.GetReportingSettingsResponse, error) {
	return csilapi.GetReportingSettingsResponse{}, ErrUnimplemented("ReactorcideUi/update-reporting-settings")
}

func (StubUi) GetAgreementReport(ctx context.Context, req csilapi.GetAgreementReportRequest) (csilapi.GetAgreementReportResponse, error) {
	return csilapi.GetAgreementReportResponse{}, ErrUnimplemented("ReactorcideUi/get-agreement-report")
}

func (StubUi) ListPathAcceptances(ctx context.Context, req csilapi.ListPathAcceptancesRequest) (csilapi.ListPathAcceptancesResponse, error) {
	return csilapi.ListPathAcceptancesResponse{}, ErrUnimplemented("ReactorcideUi/list-path-acceptances")
}

func (StubUi) RequestPathAcceptance(ctx context.Context, req csilapi.RequestPathAcceptanceRequest) (csilapi.PathAcceptanceResponse, error) {
	return csilapi.PathAcceptanceResponse{}, ErrUnimplemented("ReactorcideUi/request-path-acceptance")
}

func (StubUi) DecidePathAcceptance(ctx context.Context, req csilapi.DecidePathAcceptanceRequest) (csilapi.PathAcceptanceResponse, error) {
	return csilapi.PathAcceptanceResponse{}, ErrUnimplemented("ReactorcideUi/decide-path-acceptance")
}
