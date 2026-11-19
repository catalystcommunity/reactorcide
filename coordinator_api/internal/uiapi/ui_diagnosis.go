package uiapi

import (
	"context"
	"errors"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/diagnosis"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/principals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
)

// diagnosisService builds the diagnosis service on the UI store. The
// publisher puts entries in the rolling pull request comment.
func (s *UiService) diagnosisService() *diagnosis.Service {
	return diagnosis.NewService(s.deps.Store, vcs.NewDiagnosisPublisher(s.deps.Store, config.VCSBaseURL))
}

// diagnosisTarget loads the target of a request and checks that the caller
// can view it. An unknown and a hidden target give the same "not_found".
func (s *UiService) diagnosisTarget(ctx context.Context, service *diagnosis.Service, target diagnosis.Target) (*diagnosis.Resolved, error) {
	if !service.Available() {
		return nil, NewServiceError("unavailable", "this coordinator does not store diagnosis entries")
	}
	resolved, err := service.Resolve(ctx, target)
	if err != nil {
		if diagnosis.IsValidationError(err) {
			return nil, NewServiceError("invalid_argument", err.Error())
		}
		return nil, mapStoreErr(err, "target not found")
	}
	identity, _ := s.deps.resolveIdentity(ctx)
	var visible bool
	if resolved.Job != nil {
		visible, err = s.deps.Resolver.CanViewJob(ctx, identity, resolved.Job)
	} else {
		visible, err = s.deps.Resolver.CanViewWorkflowInstance(ctx, identity, resolved.Workflow)
	}
	if err != nil {
		return nil, NewServiceError("internal", "an internal error occurred")
	}
	if !visible {
		return nil, NewServiceError("not_found", "target not found")
	}
	return resolved, nil
}

func (s *UiService) ListDiagnoses(ctx context.Context, req csilapi.ListDiagnosesRequest) (csilapi.ListDiagnosesResponse, error) {
	target := diagnosis.Target{JobID: derefOr(req.JobId, ""), WorkflowID: derefOr(req.WorkflowId, "")}
	if (target.JobID == "") == (target.WorkflowID == "") {
		return csilapi.ListDiagnosesResponse{}, NewServiceError("invalid_argument", "name exactly one of job_id and workflow_id")
	}
	service := s.diagnosisService()
	resolved, err := s.diagnosisTarget(ctx, service, target)
	if err != nil {
		return csilapi.ListDiagnosesResponse{}, err
	}
	reports, err := service.List(ctx, resolved)
	if err != nil {
		return csilapi.ListDiagnosesResponse{}, NewServiceError("internal", "could not read the diagnosis entries")
	}
	out := make([]csilapi.DiagnosisSummary, 0, len(reports))
	for _, report := range reports {
		out = append(out, diagnosisToWire(report))
	}
	return csilapi.ListDiagnosesResponse{Diagnoses: out}, nil
}

func (s *UiService) PutDiagnosis(ctx context.Context, req csilapi.PutDiagnosisRequest) (csilapi.PutDiagnosisResponse, error) {
	identity, user, credential := s.deps.resolveIdentityDetails(ctx)
	if identity.Anonymous || (identity.UserID == "" && identity.Token == nil) {
		return csilapi.PutDiagnosisResponse{}, NewServiceError("unauthorized", "a valid session or API token is required to post a diagnosis")
	}
	service := s.diagnosisService()
	resolved, err := s.diagnosisTarget(ctx, service, diagnosis.Target{JobID: derefOr(req.JobId, ""),
		WorkflowID: derefOr(req.WorkflowId, ""), NodeName: derefOr(req.NodeName, "")})
	if err != nil {
		return csilapi.PutDiagnosisResponse{}, err
	}
	allowed, err := s.deps.Resolver.CanPostDiagnosis(ctx, identity, resolved.OrgID, resolved.ProjectID)
	if err != nil {
		return csilapi.PutDiagnosisResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	if !allowed {
		return csilapi.PutDiagnosisResponse{}, NewServiceError("forbidden",
			"posting a diagnosis needs the diagnosis:write capability and a role in the scope of the job")
	}
	in := diagnosis.Input{Title: req.Title, Body: derefOr(req.Body, ""), Confidence: derefOr(req.Confidence, ""),
		Category: derefOr(req.Category, "")}
	for _, link := range req.Links {
		in.Links = append(in.Links, models.DiagnosisLink{Name: link.Name, URL: link.Url})
	}
	if req.SuggestedShifts != nil {
		in.SuggestedShifts = map[string]any{}
		for key, value := range map[string]*string{"cpu": req.SuggestedShifts.Cpu, "cpu_request": req.SuggestedShifts.CpuRequest,
			"memory": req.SuggestedShifts.Memory, "storage": req.SuggestedShifts.Storage,
			"storage_request": req.SuggestedShifts.StorageRequest, "timeout": req.SuggestedShifts.Timeout} {
			if value != nil && *value != "" {
				in.SuggestedShifts[key] = *value
			}
		}
	}
	author := principals.FromCredential(credential, user).PrincipalID
	report, created, err := service.Post(ctx, resolved, author, in)
	if err != nil {
		var limited *diagnosis.RateLimitError
		switch {
		case errors.As(err, &limited):
			return csilapi.PutDiagnosisResponse{}, NewServiceError("rate_limited", limited.Error())
		case diagnosis.IsValidationError(err):
			return csilapi.PutDiagnosisResponse{}, NewServiceError("invalid_argument", err.Error())
		case errors.Is(err, diagnosis.ErrNoPrincipal):
			return csilapi.PutDiagnosisResponse{}, NewServiceError("forbidden", err.Error())
		case errors.Is(err, store.ErrNotFound):
			return csilapi.PutDiagnosisResponse{}, NewServiceError("not_found", "target not found")
		}
		return csilapi.PutDiagnosisResponse{}, NewServiceError("internal", "could not store the diagnosis entry")
	}
	return csilapi.PutDiagnosisResponse{Diagnosis: diagnosisToWire(*report), Created: created}, nil
}

// diagnosisToWire is the wire form of an entry.
func diagnosisToWire(report models.DiagnosisReport) csilapi.DiagnosisSummary {
	out := csilapi.DiagnosisSummary{DiagnosisId: report.DiagnosisID, TargetType: report.TargetType, JobId: report.JobID,
		WorkflowId: report.WorkflowID, NodeName: optionalText(report.NodeName),
		Author: csilapi.ActorSummary{PrincipalId: report.AuthorPrincipalID, Kind: report.AuthorKind, DisplayName: report.AuthorDisplayName},
		Title:  report.Title, Body: report.Body, Confidence: report.Confidence, Category: report.Category,
		Links: []csilapi.DiagnosisLinkEntry{}, SuggestedShifts: shiftsToWire(report.SuggestedShifts),
		SuggestedShiftsText: diagnosis.ShiftsText(report.SuggestedShifts), Revision: int64(report.Revision),
		CreatedAt: formatTime(report.CreatedAt), UpdatedAt: formatTime(report.UpdatedAt)}
	for _, link := range report.Links {
		out.Links = append(out.Links, csilapi.DiagnosisLinkEntry{Name: link.Name, Url: link.URL})
	}
	return out
}

// DeleteDiagnosis deletes the entry of the caller for a target, with the
// same rules as DELETE .../diagnosis (diagnosis.Service.Delete): the caller
// must see the target, and only the author deletes its entry.
func (s *UiService) DeleteDiagnosis(ctx context.Context, req csilapi.DeleteDiagnosisRequest) (csilapi.DeleteDiagnosisResponse, error) {
	identity, user, credential := s.deps.resolveIdentityDetails(ctx)
	if identity.Anonymous || (identity.UserID == "" && identity.Token == nil) {
		return csilapi.DeleteDiagnosisResponse{}, NewServiceError("unauthorized", "a valid session or API token is required to delete a diagnosis")
	}
	target := diagnosis.Target{JobID: derefOr(req.JobId, ""), WorkflowID: derefOr(req.WorkflowId, ""), NodeName: derefOr(req.NodeName, "")}
	if (target.JobID == "") == (target.WorkflowID == "") {
		return csilapi.DeleteDiagnosisResponse{}, NewServiceError("invalid_argument", "name exactly one of job_id and workflow_id")
	}
	service := s.diagnosisService()
	resolved, err := s.diagnosisTarget(ctx, service, target)
	if err != nil {
		return csilapi.DeleteDiagnosisResponse{}, err
	}
	author := principals.FromCredential(credential, user).PrincipalID
	removed, err := service.Delete(ctx, resolved, author)
	switch {
	case err == nil:
		return csilapi.DeleteDiagnosisResponse{Deleted: true, DiagnosisId: optionalText(removed.DiagnosisID)}, nil
	case errors.Is(err, diagnosis.ErrDeleteUnavailable):
		return csilapi.DeleteDiagnosisResponse{}, NewServiceError("unavailable", err.Error())
	case errors.Is(err, diagnosis.ErrNoPrincipal):
		return csilapi.DeleteDiagnosisResponse{}, NewServiceError("forbidden", err.Error())
	case errors.Is(err, store.ErrNotFound):
		return csilapi.DeleteDiagnosisResponse{}, NewServiceError("not_found", "you have no diagnosis entry for this target")
	}
	return csilapi.DeleteDiagnosisResponse{}, NewServiceError("internal", "could not delete the diagnosis entry")
}
