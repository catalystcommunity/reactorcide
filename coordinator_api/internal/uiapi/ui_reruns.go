package uiapi

import (
	"context"
	"errors"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobcontrol"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/principals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/runshifts"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// ManualRunInput is a manual run request (see handlers.ManualRunRequest).
type ManualRunInput struct {
	Ref      string
	PRNumber int
	Workflow string
}

// ManualRunOutput is the CI event of a manual run.
type ManualRunOutput struct {
	EventID, Status, EvalJobID, Ref, CommitSHA, Workflow string
	PRNumber                                             int
}

// ErrManualRunFiltered is returned by a ManualRunStarter when the project's
// event settings do not process the event of the ref.
var ErrManualRunFiltered = errors.New("the project event settings do not process events for this ref")

// ManualRunStarter starts a manual run on a ref. The router wires the
// webhook handler through an adapter in internal/handlers.
type ManualRunStarter interface {
	StartManualRunFromUI(ctx context.Context, project *models.Project, input ManualRunInput, actor principals.Actor) (ManualRunOutput, error)
}

// shiftsFromWire converts the CSIL shifts to models.RunShifts with the same
// parser as every other channel.
func shiftsFromWire(wire *csilapi.RunShifts) (models.RunShifts, error) {
	if wire == nil {
		return models.RunShifts{}, nil
	}
	raw := map[string]any{}
	for key, value := range map[string]*string{"cpu": wire.Cpu, "cpu_request": wire.CpuRequest, "memory": wire.Memory,
		"storage": wire.Storage, "storage_request": wire.StorageRequest, "timeout": wire.Timeout} {
		if value != nil && *value != "" {
			raw[key] = *value
		}
	}
	shifts, err := runshifts.ParseMap(raw)
	if err != nil {
		return models.RunShifts{}, NewServiceError("invalid_argument", err.Error())
	}
	return shifts, nil
}

// shiftsToWire is the wire form of recorded shifts, or nil.
func shiftsToWire(shifts *models.RunShifts) *csilapi.RunShifts {
	if shifts == nil || shifts.IsZero() {
		return nil
	}
	wire := &csilapi.RunShifts{Cpu: optionalText(shifts.CPULimit), CpuRequest: optionalText(shifts.CPURequest),
		Memory: optionalText(shifts.MemoryLimit), Storage: optionalText(shifts.StorageLimit),
		StorageRequest: optionalText(shifts.StorageRequest)}
	if shifts.TimeoutSeconds > 0 {
		wire.Timeout = optionalText((time.Duration(shifts.TimeoutSeconds) * time.Second).String())
	}
	return wire
}

func rerunLimitsToWire(limits *models.RerunShiftLimits) *csilapi.RerunShiftLimits {
	if limits == nil {
		return nil
	}
	wire := &csilapi.RerunShiftLimits{MaxCpu: optionalText(limits.MaxCPU), MaxMemory: optionalText(limits.MaxMemory),
		MaxStorage: optionalText(limits.MaxStorage)}
	if limits.MaxTimeoutSeconds > 0 {
		value := int64(limits.MaxTimeoutSeconds)
		wire.MaxTimeoutSeconds = &value
	}
	return wire
}

func rerunLimitsFromWire(wire *csilapi.RerunShiftLimits) *models.RerunShiftLimits {
	if wire == nil {
		return nil
	}
	limits := &models.RerunShiftLimits{}
	if wire.MaxCpu != nil {
		limits.MaxCPU = *wire.MaxCpu
	}
	if wire.MaxMemory != nil {
		limits.MaxMemory = *wire.MaxMemory
	}
	if wire.MaxStorage != nil {
		limits.MaxStorage = *wire.MaxStorage
	}
	if wire.MaxTimeoutSeconds != nil {
		limits.MaxTimeoutSeconds = int(*wire.MaxTimeoutSeconds)
	}
	return limits
}

// RerunWorkflow runs a finished workflow again as a new instance with its
// recorded authority and optional shifts. Authorization is Caps.Retry, as for
// RetryWorkflow.
func (s *UiService) RerunWorkflow(ctx context.Context, req csilapi.RerunWorkflowRequest) (csilapi.RerunWorkflowResponse, error) {
	if err := requireNonEmpty("workflow_instance_id", req.WorkflowInstanceId, 64); err != nil {
		return csilapi.RerunWorkflowResponse{}, err
	}
	wig, ok := s.deps.Store.(workflowInstanceGetter)
	if !ok {
		return csilapi.RerunWorkflowResponse{}, NewServiceError("internal", "workflows are not supported by this server's store configuration")
	}
	wf, err := wig.GetWorkflowInstance(ctx, req.WorkflowInstanceId)
	if err != nil {
		return csilapi.RerunWorkflowResponse{}, mapStoreErr(err, "workflow not found")
	}
	id, _ := s.deps.resolveIdentity(ctx)
	if err := s.requireWorkflowVisible(ctx, id, wf); err != nil {
		return csilapi.RerunWorkflowResponse{}, err
	}
	caps, err := s.deps.Resolver.Capabilities(ctx, id, workflowScope(wf))
	if err != nil {
		return csilapi.RerunWorkflowResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	if !caps.Retry {
		return csilapi.RerunWorkflowResponse{}, NewServiceError("forbidden", "you do not have permission to re-run this workflow")
	}
	shifts, err := shiftsFromWire(req.Shifts)
	if err != nil {
		return csilapi.RerunWorkflowResponse{}, err
	}
	newWf, err := jobcontrol.RerunWorkflow(s.deps.actorContext(ctx), s.deps.Store, s.deps.CorndogsClient, req.WorkflowInstanceId, shifts)
	if err != nil {
		return csilapi.RerunWorkflowResponse{}, mapJobControlErr(err)
	}
	return csilapi.RerunWorkflowResponse{WorkflowInstanceId: newWf.WorkflowID, Status: newWf.Status}, nil
}

// StartRun starts a new evaluated run on a ref of a project. Authorization is
// the retry tier of the project; a token must also have jobs:submit.
func (s *UiService) StartRun(ctx context.Context, req csilapi.StartRunRequest) (csilapi.StartRunResponse, error) {
	id, _, credential := s.deps.resolveIdentityDetails(ctx)
	project, err := s.visibleProject(ctx, id, req.ProjectId)
	if err != nil {
		return csilapi.StartRunResponse{}, err
	}
	projectID := project.ProjectID
	caps, err := s.deps.Resolver.Capabilities(ctx, id, authz.Scope{ProjectID: &projectID})
	if err != nil {
		return csilapi.StartRunResponse{}, NewServiceError("internal", "an internal error occurred")
	}
	if !caps.Retry || (credential != nil && !credential.HasCapability(tokencaps.JobsSubmit)) {
		return csilapi.StartRunResponse{}, NewServiceError("forbidden", "you do not have permission to start a run in this project")
	}
	if s.deps.ManualRuns == nil {
		return csilapi.StartRunResponse{}, NewServiceError("unavailable", "manual runs are not available")
	}
	if reason := s.shedManualRunRefusal(ctx, id, project.OwnershipOrgID()); reason != "" {
		return csilapi.StartRunResponse{}, NewServiceError("unavailable", reason)
	}
	input := ManualRunInput{}
	if req.Ref != nil {
		input.Ref = *req.Ref
	}
	if req.PrNumber != nil {
		input.PRNumber = int(*req.PrNumber)
	}
	if req.Workflow != nil {
		input.Workflow = *req.Workflow
	}
	actorCtx := s.deps.actorContext(ctx)
	out, err := s.deps.ManualRuns.StartManualRunFromUI(actorCtx, project, input, principals.ActorOf(actorCtx))
	switch {
	case errors.Is(err, ErrManualRunFiltered):
		return csilapi.StartRunResponse{}, NewServiceError("failed_precondition", err.Error())
	case errors.Is(err, store.ErrInvalidInput):
		return csilapi.StartRunResponse{}, NewServiceError("invalid_argument", err.Error())
	case errors.Is(err, store.ErrNotFound):
		return csilapi.StartRunResponse{}, NewServiceError("not_found", err.Error())
	case err != nil:
		return csilapi.StartRunResponse{}, NewServiceError("unavailable", "the run could not be started: "+err.Error())
	}
	response := csilapi.StartRunResponse{EventId: out.EventID, Status: out.Status, EvalJobId: optionalText(out.EvalJobID),
		Ref: out.Ref, CommitSha: out.CommitSHA, Workflow: optionalText(out.Workflow)}
	if out.PRNumber > 0 {
		value := int64(out.PRNumber)
		response.PrNumber = &value
	}
	return response, nil
}
