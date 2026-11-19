package handlers

// Manual runs on any ref (plan item B1, D21). A principal with permission
// starts a new evaluated run for a branch, a tag, or a pull request head of a
// project. The coordinator resolves the commit through the VCS API, records a
// `manual` CI event with the principal as its actor, and evaluates it the
// same way as a webhook delivery for that ref: the run gate, the CI policy
// and the execution profile of the ref apply. The run never gets the
// authority of an earlier run.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/principals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// ManualRunRequest names the ref of a manual run. Exactly one of Ref and
// PRNumber is set.
type ManualRunRequest struct {
	// Ref is a branch name, "refs/heads/NAME", or "refs/tags/NAME".
	Ref string `json:"ref,omitempty"`
	// PRNumber is a pull request (merge request) number. The run uses its
	// current head.
	PRNumber int `json:"pr_number,omitempty"`
	// Workflow, when set, is the one workflow to run: a workflow ID or a
	// path such as ".reactorcide/workflows/build.yaml". The eval then runs it
	// without its on/paths filters, as a schedule does. Empty evaluates
	// every workflow for the event, as a push or a pull request update does.
	Workflow string `json:"workflow,omitempty"`
	// IdempotencyKey, when set, makes a repeat return the first event.
	IdempotencyKey string `json:"-"`
}

// ManualRunResult is the CI event of a manual run.
type ManualRunResult struct {
	EventID   string `json:"event_id"`
	Status    string `json:"status"`
	EvalJobID string `json:"eval_job_id,omitempty"`
	Ref       string `json:"ref"`
	CommitSHA string `json:"commit_sha"`
	PRNumber  int    `json:"pr_number,omitempty"`
	Workflow  string `json:"workflow,omitempty"`
}

// ErrManualRunFiltered is returned when the project's event settings do not
// process the event of the ref.
var ErrManualRunFiltered = errors.New("the project event settings do not process events for this ref")

// manualRunStarter is the WebhookHandler capability the REST and CSIL
// layers call.
type manualRunStarter interface {
	StartManualRun(ctx context.Context, project *models.Project, request ManualRunRequest, actor principals.Actor) (*ManualRunResult, error)
}

var _ manualRunStarter = (*WebhookHandler)(nil)

// validRunText accepts printable text with no spaces and no "..", up to max
// bytes.
func validRunText(value string, max int) bool {
	if value == "" || len(value) > max || strings.Contains(value, "..") {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// normalizeManualRunRequest checks the request and returns the ref kind
// ("branch" or "tag") and the short name.
func normalizeManualRunRequest(request ManualRunRequest) (kind, name string, err error) {
	ref := strings.TrimSpace(request.Ref)
	if (ref == "") == (request.PRNumber <= 0) {
		return "", "", fmt.Errorf("%w: give exactly one of ref and pr_number", store.ErrInvalidInput)
	}
	if request.Workflow != "" && !validRunText(request.Workflow, 255) {
		return "", "", fmt.Errorf("%w: workflow is not valid", store.ErrInvalidInput)
	}
	if request.PRNumber > 0 {
		return "pr", "", nil
	}
	switch {
	case strings.HasPrefix(ref, "refs/tags/"):
		kind, name = "tag", strings.TrimPrefix(ref, "refs/tags/")
	case strings.HasPrefix(ref, "refs/heads/"):
		kind, name = "branch", strings.TrimPrefix(ref, "refs/heads/")
	case strings.HasPrefix(ref, "refs/"):
		return "", "", fmt.Errorf("%w: ref must be a branch name, refs/heads/NAME or refs/tags/NAME", store.ErrInvalidInput)
	default:
		kind, name = "branch", ref
	}
	if !validRunText(name, 255) || strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") {
		return "", "", fmt.Errorf("%w: ref is not valid", store.ErrInvalidInput)
	}
	return kind, name, nil
}

// StartManualRun starts a new evaluated run of a ref. The actor is the
// principal that asked for it.
func (h *WebhookHandler) StartManualRun(ctx context.Context, project *models.Project, request ManualRunRequest, actor principals.Actor) (*ManualRunResult, error) {
	if project == nil {
		return nil, store.ErrNotFound
	}
	if !project.Enabled {
		return nil, fmt.Errorf("%w: project %s is disabled", store.ErrInvalidInput, project.Name)
	}
	kind, name, err := normalizeManualRunRequest(request)
	if err != nil {
		return nil, err
	}
	id := uuid.New().String()
	if key := strings.TrimSpace(request.IdempotencyKey); key != "" {
		if !validIdempotencyKey(key) {
			return nil, fmt.Errorf("%w: Idempotency-Key is not valid", store.ErrInvalidInput)
		}
		// Scoped to the project; the event key also has the provider.
		id = project.ProjectID + ":" + key
	}
	coordinator := &vcs.CoordinatorEvent{Source: models.CIEventSourceManual, ID: id,
		ActorPrincipalID: actor.PrincipalID, ActorIdentity: actor.Identity, WorkflowRef: request.Workflow}
	result := &ManualRunResult{Workflow: request.Workflow, PRNumber: request.PRNumber}

	if kind == "pr" {
		target, err := h.loadPRTarget(ctx, project, request.PRNumber)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", store.ErrNotFound, err)
		}
		if !strings.EqualFold(target.PR.State, "open") {
			return nil, fmt.Errorf("%w: pull request %d is not open", store.ErrInvalidInput, request.PRNumber)
		}
		// The sender is the earlier actor of the head, so the admission
		// facts of the head stay the same (as for a rerun). The principal
		// that asked for the run is the actor of the event.
		sender := target.PR.AuthorLogin
		if finder, ok := h.store.(store.CIEventActorStore); ok {
			if latest, err := finder.LatestCIEventActorForPR(ctx, project.ProjectID, target.PR.Number, target.PR.HeadSHA); err == nil && latest != "" {
				sender = latest
			}
		}
		current := *target.PR
		current.Action = models.CIEventSourceManual
		event := &vcs.WebhookEvent{
			Provider: target.Provider, EventType: "ci_manual", GenericEvent: vcs.EventPullRequestUpdated,
			Repository:  vcs.RepositoryInfo{FullName: target.Repo, CloneURL: target.CloneURL},
			PullRequest: &current, SenderLogin: sender, DeliveryID: "manual:" + id, Coordinator: coordinator,
		}
		if err := h.processPullRequestEvent(event, target.Client, project); err != nil {
			return nil, err
		}
		result.Ref, result.CommitSHA = target.PR.HeadRef, target.PR.HeadSHA
	} else {
		provider, repo, cloneURL, err := h.projectRepository(project)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", store.ErrInvalidInput, err)
		}
		client := h.getStatusClient(ctx, project, provider, h.vcsClients[provider])
		resolver, ok := client.(vcs.RefResolver)
		if client == nil || !ok {
			return nil, fmt.Errorf("no %s client can resolve refs for %s", provider, repo)
		}
		var sha, fullRef string
		generic := vcs.EventPush
		if kind == "tag" {
			sha, err = resolver.GetTagCommit(ctx, repo, name)
			fullRef, generic = "refs/tags/"+name, vcs.EventTagCreated
		} else {
			sha, err = resolver.GetBranchHead(ctx, repo, name)
			fullRef = "refs/heads/" + name
		}
		if errors.Is(err, vcs.ErrBranchNotFound) || errors.Is(err, vcs.ErrTagNotFound) {
			return nil, fmt.Errorf("%w: %v", store.ErrNotFound, err)
		}
		if err != nil {
			return nil, fmt.Errorf("resolving %s: %w", fullRef, err)
		}
		// The default branch is the trusted CI base of a ref with no
		// "before" commit (see pushCIBaseRef). Without it the ref's own CI
		// would be trusted, so fail closed.
		defaultBranch, err := resolver.GetDefaultBranch(ctx, repo)
		if err != nil {
			return nil, fmt.Errorf("finding the default branch of %s: %w", repo, err)
		}
		event := &vcs.WebhookEvent{
			Provider: provider, EventType: "ci_manual", GenericEvent: generic,
			Repository:  vcs.RepositoryInfo{FullName: repo, CloneURL: cloneURL, DefaultBranch: defaultBranch},
			Push:        &vcs.PushInfo{Ref: fullRef, After: sha},
			DeliveryID:  "manual:" + id,
			Coordinator: coordinator,
		}
		if err := h.processPushEvent(event, client, project); err != nil {
			return nil, err
		}
		result.Ref, result.CommitSHA = fullRef, sha
	}

	if coordinator.EventID == "" {
		return nil, ErrManualRunFiltered
	}
	result.EventID = coordinator.EventID
	result.Status = models.CIEventStatusReceived
	if events, ok := h.store.(store.CIEventStore); ok {
		if recorded, err := events.GetCIEvent(ctx, coordinator.EventID); err == nil {
			result.Status = recorded.Status
			if recorded.EvalJobID != nil {
				result.EvalJobID = *recorded.EvalJobID
			}
			if recorded.CommitSHA != "" {
				result.CommitSHA = recorded.CommitSHA
			}
		}
	}
	h.logger.WithFields(logrus.Fields{"project": project.Name, "event_id": result.EventID, "ref": result.Ref,
		"sha": result.CommitSHA, "workflow": result.Workflow, "actor": actor.PrincipalID}).Info("Manual run started")
	return result, nil
}

// ManualRunHandler serves POST /api/v1/projects/{project_id}/runs.
type ManualRunHandler struct {
	BaseHandler
	store   store.Store
	starter manualRunStarter
}

// NewManualRunHandler returns the REST handler of manual runs.
func NewManualRunHandler(appStore store.Store, starter manualRunStarter) *ManualRunHandler {
	return &ManualRunHandler{store: appStore, starter: starter}
}

// CanStartManualRun reports whether the caller may start a run in the
// project: the retry tier of the project (project owner, org admin, global
// admin, or a token with jobs:retry or workflows:control), and a token must
// also have jobs:submit.
func CanStartManualRun(ctx context.Context, appStore store.Store, project *models.Project) bool {
	principal := checkauth.GetPrincipalFromContext(ctx)
	user := checkauth.GetUserFromContext(ctx)
	if principal == nil && user == nil {
		return false
	}
	if principal != nil && !principal.HasCapability(tokencaps.JobsSubmit) {
		return false
	}
	roleStore, ok := appStore.(authz.RoleStore)
	if !ok {
		return principal != nil && principal.HasOrganization(project.OwnershipOrgID())
	}
	projectID := project.ProjectID
	caps, err := authz.NewResolver(roleStore).Capabilities(ctx, authz.IdentityFromPrincipal(principal, user), authz.Scope{ProjectID: &projectID})
	return err == nil && caps.Retry
}

// StartRun handles POST /api/v1/projects/{project_id}/runs with a
// ManualRunRequest body. The optional Idempotency-Key header makes a repeat
// return the first event.
func (h *ManualRunHandler) StartRun(w http.ResponseWriter, r *http.Request) {
	if h.starter == nil {
		h.respondWithError(w, http.StatusNotImplemented, errors.New("manual runs are not available"))
		return
	}
	project, err := h.store.GetProjectByID(r.Context(), h.getID(r, "project_id"))
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
		return
	}
	if !CanStartManualRun(r.Context(), h.store, project) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	if reason := shedManualRunRefusal(r.Context(), h.store, project.OwnershipOrgID()); reason != "" {
		respondShedRefused(w, reason)
		return
	}
	var request ManualRunRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		h.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid_input", Message: "the body must be {\"ref\": ..., \"pr_number\": ..., \"workflow\": ...}: " + err.Error()})
		return
	}
	request.IdempotencyKey = r.Header.Get("Idempotency-Key")
	result, err := h.starter.StartManualRun(principals.WithActor(r.Context(), requestActor(r.Context(), h.store)), project, request, requestActor(r.Context(), h.store))
	if err != nil {
		respondManualRunError(&h.BaseHandler, w, err)
		return
	}
	h.respondWithJSON(w, http.StatusCreated, result)
}

func respondManualRunError(h *BaseHandler, w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrManualRunFiltered):
		h.respondWithJSON(w, http.StatusUnprocessableEntity, ErrorResponse{Error: "filtered", Message: err.Error()})
	case errors.Is(err, store.ErrInvalidInput):
		h.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid_input", Message: err.Error()})
	case errors.Is(err, store.ErrNotFound):
		h.respondWithJSON(w, http.StatusNotFound, ErrorResponse{Error: "not_found", Message: err.Error()})
	default:
		h.respondWithJSON(w, http.StatusBadGateway, ErrorResponse{Error: "run_not_started", Message: err.Error()})
	}
}

// uiManualRuns adapts the webhook handler to uiapi.ManualRunStarter.
type uiManualRuns struct{ h *WebhookHandler }

func (a uiManualRuns) StartManualRunFromUI(ctx context.Context, project *models.Project, input uiapi.ManualRunInput, actor principals.Actor) (uiapi.ManualRunOutput, error) {
	result, err := a.h.StartManualRun(ctx, project, ManualRunRequest{Ref: input.Ref, PRNumber: input.PRNumber, Workflow: input.Workflow}, actor)
	if errors.Is(err, ErrManualRunFiltered) {
		return uiapi.ManualRunOutput{}, uiapi.ErrManualRunFiltered
	}
	if err != nil {
		return uiapi.ManualRunOutput{}, err
	}
	return uiapi.ManualRunOutput{EventID: result.EventID, Status: result.Status, EvalJobID: result.EvalJobID,
		Ref: result.Ref, CommitSHA: result.CommitSHA, Workflow: result.Workflow, PRNumber: result.PRNumber}, nil
}
