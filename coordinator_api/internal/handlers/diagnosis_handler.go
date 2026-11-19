package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/diagnosis"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/metrics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/principals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/runshifts"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
)

// maxDiagnosisRequestBytes bounds the request body. The entry limits are
// smaller (diagnosis.MaxBodyBytes); this only stops a large upload early.
const maxDiagnosisRequestBytes = 64 << 10

// DiagnosisHandler serves the diagnosis entry REST API (docs/diagnosis.md):
//
//	GET|PUT|DELETE /api/v1/jobs/JOB/diagnosis
//	GET|PUT|DELETE /api/v1/workflows/WORKFLOW/diagnosis
//	GET|PUT|DELETE /api/v1/workflows/WORKFLOW/nodes/NODE/diagnosis
//
// DELETE removes the entry of the caller (its principal) for the target.
//
// GET needs jobs:read (a job) or workflows:read (a workflow) and the view
// rules of the job or workflow. PUT also needs diagnosis:write and a role in
// the scope (authz.Resolver.CanPostDiagnosis). PUT replaces the entry of the
// caller's principal: one entry for each principal and target.
type DiagnosisHandler struct {
	BaseHandler
	service  *diagnosis.Service
	resolver *authz.Resolver
}

// NewDiagnosisHandler builds the handler on appStore. The publisher puts
// entries in the rolling pull request comment.
func NewDiagnosisHandler(appStore store.Store) *DiagnosisHandler {
	return &DiagnosisHandler{
		service:  diagnosis.NewService(appStore, vcs.NewDiagnosisPublisher(appStore, config.VCSBaseURL)),
		resolver: roleStoreResolver(appStore, "DiagnosisHandler"),
	}
}

// DiagnosisView is the REST form of an entry.
type DiagnosisView struct {
	DiagnosisID string                 `json:"diagnosis_id"`
	TargetType  string                 `json:"target_type"`
	JobID       *string                `json:"job_id,omitempty"`
	WorkflowID  *string                `json:"workflow_id,omitempty"`
	NodeName    string                 `json:"node_name,omitempty"`
	Author      models.ActorSummary    `json:"author"`
	Title       string                 `json:"title"`
	Body        string                 `json:"body"`
	Confidence  string                 `json:"confidence,omitempty"`
	Category    string                 `json:"category,omitempty"`
	Links       []models.DiagnosisLink `json:"links"`
	// SuggestedShifts uses the keys of a retry body, so a tool can send it
	// as {"shifts": ...} to POST /api/v1/jobs/JOB/retry.
	SuggestedShifts map[string]any `json:"suggested_shifts,omitempty"`
	Revision        int            `json:"revision"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	// Created is set on a PUT answer: true for a new entry.
	Created *bool `json:"created,omitempty"`
}

// DiagnosisViewOf returns the REST form of report.
func DiagnosisViewOf(report models.DiagnosisReport) DiagnosisView {
	view := DiagnosisView{DiagnosisID: report.DiagnosisID, TargetType: report.TargetType, JobID: report.JobID,
		WorkflowID: report.WorkflowID, NodeName: report.NodeName,
		Author: models.ActorSummary{PrincipalID: report.AuthorPrincipalID, Kind: report.AuthorKind, DisplayName: report.AuthorDisplayName},
		Title:  report.Title, Body: report.Body, Confidence: report.Confidence, Category: report.Category, Links: report.Links,
		Revision: report.Revision, CreatedAt: report.CreatedAt, UpdatedAt: report.UpdatedAt}
	if view.Links == nil {
		view.Links = []models.DiagnosisLink{}
	}
	if report.SuggestedShifts != nil && !report.SuggestedShifts.IsZero() {
		view.SuggestedShifts = runshifts.ToMap(*report.SuggestedShifts)
	}
	return view
}

type diagnosisRequest struct {
	Title           string                 `json:"title"`
	Body            string                 `json:"body"`
	Confidence      string                 `json:"confidence"`
	Category        string                 `json:"category"`
	Links           []models.DiagnosisLink `json:"links"`
	SuggestedShifts map[string]any         `json:"suggested_shifts"`
}

// Serve handles one diagnosis route. target names the job, the workflow, or
// the node.
func (h *DiagnosisHandler) Serve(w http.ResponseWriter, r *http.Request, target diagnosis.Target) {
	if !h.service.Available() || h.resolver == nil {
		h.respondWithError(w, http.StatusServiceUnavailable, store.ErrServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.list(w, r, target)
	case http.MethodPut, http.MethodPost:
		h.put(w, r, target)
	case http.MethodDelete:
		h.remove(w, r, target)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// resolveVisible loads the target and checks that the caller can view it.
// It writes the answer and returns nil when it cannot.
func (h *DiagnosisHandler) resolveVisible(w http.ResponseWriter, r *http.Request, target diagnosis.Target) (*diagnosis.Resolved, authz.Identity) {
	ctx := r.Context()
	credential := checkauth.GetPrincipalFromContext(ctx)
	user := checkauth.GetUserFromContext(ctx)
	if credential == nil && user == nil {
		h.respondWithError(w, http.StatusUnauthorized, store.ErrUnauthorized)
		return nil, authz.Identity{}
	}
	resolved, err := h.service.Resolve(ctx, target)
	if err != nil {
		h.writeDiagnosisError(w, err)
		return nil, authz.Identity{}
	}
	identity := authz.IdentityFromPrincipal(credential, user)
	if !h.canView(r, credential, identity, resolved) {
		// Same answer as an unknown target: do not disclose that it exists.
		h.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
		return nil, authz.Identity{}
	}
	return resolved, identity
}

// canView applies the read rules of GetJob and GetWorkflow: a token needs
// the organization and jobs:read (or workflows:read); a user token also
// needs the live view rules of the user.
func (h *DiagnosisHandler) canView(r *http.Request, credential *checkauth.Principal, identity authz.Identity, target *diagnosis.Resolved) bool {
	ctx := r.Context()
	capability := tokencaps.JobsRead
	if target.Job == nil {
		capability = tokencaps.WorkflowsRead
	}
	if credential != nil {
		if credential.CredentialType == "job_token" {
			return false
		}
		if !credential.HasOrganization(target.OrgID) || !credential.HasCapability(capability) {
			return false
		}
		if credential.CredentialType != "user_token" {
			// The admin token, a service or instance token, or a service
			// principal token (already narrowed to its roles at auth).
			return true
		}
	}
	var visible bool
	var err error
	if target.Job != nil {
		visible, err = h.resolver.CanViewJob(ctx, identity, target.Job)
	} else {
		visible, err = h.resolver.CanViewWorkflowInstance(ctx, identity, target.Workflow)
	}
	return err == nil && visible
}

func (h *DiagnosisHandler) list(w http.ResponseWriter, r *http.Request, target diagnosis.Target) {
	resolved, _ := h.resolveVisible(w, r, target)
	if resolved == nil {
		return
	}
	reports, err := h.service.List(r.Context(), resolved)
	if err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	views := make([]DiagnosisView, 0, len(reports))
	for _, report := range reports {
		views = append(views, DiagnosisViewOf(report))
	}
	h.respondWithJSON(w, http.StatusOK, map[string]any{"diagnoses": views})
}

func (h *DiagnosisHandler) put(w http.ResponseWriter, r *http.Request, target diagnosis.Target) {
	resolved, identity := h.resolveVisible(w, r, target)
	if resolved == nil {
		return
	}
	ctx := r.Context()
	allowed, err := h.resolver.CanPostDiagnosis(ctx, identity, resolved.OrgID, resolved.ProjectID)
	if err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	if !allowed {
		h.respondWithJSON(w, http.StatusForbidden, ErrorResponse{Error: "forbidden",
			Message: "posting a diagnosis needs the diagnosis:write capability and a role in the scope of the job"})
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxDiagnosisRequestBytes))
	if err != nil {
		h.respondWithJSON(w, http.StatusRequestEntityTooLarge, ErrorResponse{Error: "invalid_input", Message: "the request body is too large"})
		return
	}
	var req diagnosisRequest
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		h.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid_input", Message: "the request body is not a valid diagnosis: " + err.Error()})
		return
	}
	author := principals.FromContext(ctx).PrincipalID
	report, created, err := h.service.Post(ctx, resolved, author, diagnosis.Input{Title: req.Title, Body: req.Body,
		Confidence: req.Confidence, Category: req.Category, Links: req.Links, SuggestedShifts: req.SuggestedShifts})
	if err != nil {
		h.writeDiagnosisError(w, err)
		return
	}
	// 200 for a new and for a replaced entry: the post is idempotent.
	// "created" tells which one it was.
	view := DiagnosisViewOf(*report)
	view.Created = &created
	h.respondWithJSON(w, http.StatusOK, view)
}

// remove deletes the entry of the caller for the target. Only the author
// removes an entry; the caller needs to see the target.
func (h *DiagnosisHandler) remove(w http.ResponseWriter, r *http.Request, target diagnosis.Target) {
	resolved, _ := h.resolveVisible(w, r, target)
	if resolved == nil {
		return
	}
	author := principals.FromContext(r.Context()).PrincipalID
	removed, err := h.service.Delete(r.Context(), resolved, author)
	if errors.Is(err, diagnosis.ErrDeleteUnavailable) {
		h.respondWithError(w, http.StatusServiceUnavailable, store.ErrServiceUnavailable)
		return
	}
	if err != nil {
		h.writeDiagnosisError(w, err)
		return
	}
	h.respondWithJSON(w, http.StatusOK, map[string]any{"deleted": true, "diagnosis_id": removed.DiagnosisID})
}

func (h *DiagnosisHandler) writeDiagnosisError(w http.ResponseWriter, err error) {
	var limited *diagnosis.RateLimitError
	switch {
	case errors.As(err, &limited):
		metrics.RecordRateLimited("diagnosis")
		writeTooManyRequests(w, limited.Wait, limited.Error())
	case diagnosis.IsValidationError(err):
		h.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid_input", Message: err.Error()})
	case errors.Is(err, diagnosis.ErrNoPrincipal):
		h.respondWithJSON(w, http.StatusForbidden, ErrorResponse{Error: "forbidden", Message: err.Error()})
	case errors.Is(err, store.ErrNotFound):
		h.respondWithError(w, http.StatusNotFound, err)
	default:
		h.respondWithError(w, http.StatusInternalServerError, err)
	}
}

// diagnosisTargetFromWorkflowPath reads "WORKFLOW" or "WORKFLOW/nodes/NODE"
// (the path before "/diagnosis").
func diagnosisTargetFromWorkflowPath(path string) diagnosis.Target {
	if workflowID, node, ok := strings.Cut(path, "/nodes/"); ok {
		return diagnosis.Target{WorkflowID: workflowID, NodeName: node}
	}
	return diagnosis.Target{WorkflowID: path}
}

// diagnosisJobTarget is the target of /api/v1/jobs/JOB/diagnosis.
func diagnosisJobTarget(jobID string) diagnosis.Target {
	return diagnosis.Target{JobID: jobID}
}
