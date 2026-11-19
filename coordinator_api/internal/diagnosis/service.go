package diagnosis

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/ratelimit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/runshifts"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// ErrNoPrincipal is returned when the caller has no active principal. A
// diagnosis entry always has an author principal.
var ErrNoPrincipal = errors.New("this credential has no active principal; a diagnosis entry needs an author principal")

// RateLimitError is returned when the author is over the diagnosis post
// limit. REST answers 429 with Retry-After.
type RateLimitError struct{ Wait time.Duration }

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("the principal is over the diagnosis post limit; retry after %d seconds", ratelimit.RetryAfterSeconds(e.Wait))
}

var (
	limiterOnce sync.Once
	limiterMu   sync.RWMutex
	limiter     *ratelimit.Limiter
)

// currentLimiter returns the per-principal post limiter. It is built from
// the environment the first time (config_diagnosis.go).
func currentLimiter() *ratelimit.Limiter {
	limiterOnce.Do(func() {
		limiterMu.Lock()
		if limiter == nil {
			limiter = ratelimit.New(config.DiagnosisRateLimitPerMinute, config.DiagnosisRateLimitBurst)
		}
		limiterMu.Unlock()
	})
	limiterMu.RLock()
	defer limiterMu.RUnlock()
	return limiter
}

// SetRateLimiter replaces the post limiter (nil: no limit). Tests use it.
func SetRateLimiter(l *ratelimit.Limiter) {
	limiterOnce.Do(func() {})
	limiterMu.Lock()
	limiter = l
	limiterMu.Unlock()
}

// JobReader reads a job.
type JobReader interface {
	GetJobByID(ctx context.Context, jobID string) (*models.Job, error)
}

// WorkflowReader reads a workflow and its nodes.
type WorkflowReader interface {
	GetWorkflowInstance(ctx context.Context, workflowID string) (*models.WorkflowInstance, error)
	ListWorkflowNodes(ctx context.Context, workflowID string) ([]models.WorkflowNode, error)
}

// PrincipalReader reads a principal.
type PrincipalReader interface {
	GetPrincipal(ctx context.Context, principalID string) (*models.Principal, error)
}

// Target names what an entry is about: a job, a node of a workflow
// (WorkflowID and NodeName), or a workflow (WorkflowID alone).
type Target struct {
	JobID      string
	WorkflowID string
	NodeName   string
}

// Resolved is a target with its rows. Job is set for a job or node target.
// Workflow is the workflow target, or the workflow of the job when it has
// one.
type Resolved struct {
	TargetType string
	Job        *models.Job
	Workflow   *models.WorkflowInstance
	NodeName   string
	OrgID      string
	ProjectID  *string
}

// SubjectName is the name of the target for display: the node, the job, or
// the workflow.
func (r *Resolved) SubjectName() string {
	switch {
	case r.NodeName != "":
		return r.NodeName
	case r.Job != nil:
		return r.Job.Name
	case r.Workflow != nil:
		return r.Workflow.Name
	}
	return ""
}

// PageURL is the UI page of the target under baseURL, or "".
func (r *Resolved) PageURL(baseURL string) string {
	baseURL = strings.TrimRight(baseURL, "/")
	if baseURL == "" {
		return ""
	}
	if r.Job != nil {
		return baseURL + "/app/jobs/" + r.Job.JobID
	}
	if r.Workflow != nil {
		return baseURL + "/app/workflows/" + r.Workflow.WorkflowID
	}
	return ""
}

// Publisher puts an entry in the rolling pull request comment of its
// target, when the target has a pull request. The vcs package provides it.
type Publisher func(ctx context.Context, target *Resolved, report *models.DiagnosisReport) error

// Service validates, stores, and publishes diagnosis entries. The caller
// authorizes the request (authz.Resolver.CanPostDiagnosis) between Resolve
// and Post.
type Service struct {
	Store      store.DiagnosisStore
	Jobs       JobReader
	Workflows  WorkflowReader
	Principals PrincipalReader
	Publish    Publisher
	// Audit is the store for audit records (optional).
	Audit any
}

// NewService builds a service from one store value that has the readers.
// A reader that the value does not have stays nil.
func NewService(value any, publish Publisher) *Service {
	s := &Service{Publish: publish, Audit: value}
	s.Store, _ = value.(store.DiagnosisStore)
	s.Jobs, _ = value.(JobReader)
	s.Workflows, _ = value.(WorkflowReader)
	s.Principals, _ = value.(PrincipalReader)
	return s
}

// Available reports whether the service can store entries.
func (s *Service) Available() bool {
	return s != nil && s.Store != nil && s.Jobs != nil && s.Workflows != nil
}

// Resolve loads the target. It returns store.ErrNotFound for an unknown job,
// workflow, or node, and a ValidationError for a target that is not valid.
func (s *Service) Resolve(ctx context.Context, t Target) (*Resolved, error) {
	t.JobID, t.WorkflowID, t.NodeName = strings.TrimSpace(t.JobID), strings.TrimSpace(t.WorkflowID), strings.TrimSpace(t.NodeName)
	switch {
	case t.JobID != "" && (t.WorkflowID != "" || t.NodeName != ""):
		return nil, invalid("name a job, a workflow node, or a workflow; not more than one")
	case t.JobID != "":
		job, err := s.Jobs.GetJobByID(ctx, t.JobID)
		if err != nil || job == nil {
			return nil, store.ErrNotFound
		}
		resolved := &Resolved{TargetType: models.DiagnosisTargetJob, Job: job, OrgID: job.OwnershipOrgID(), ProjectID: job.ProjectID}
		if job.WorkflowID != nil && *job.WorkflowID != "" {
			if wf, err := s.Workflows.GetWorkflowInstance(ctx, *job.WorkflowID); err == nil && wf != nil {
				resolved.Workflow = wf
				if job.WorkflowNodeID != nil {
					if nodes, err := s.Workflows.ListWorkflowNodes(ctx, wf.WorkflowID); err == nil {
						for _, node := range nodes {
							if node.NodeID == *job.WorkflowNodeID {
								resolved.NodeName = node.Name
							}
						}
					}
				}
			}
		}
		return resolved, nil
	case t.WorkflowID != "":
		wf, err := s.Workflows.GetWorkflowInstance(ctx, t.WorkflowID)
		if err != nil || wf == nil {
			return nil, store.ErrNotFound
		}
		if t.NodeName == "" {
			return &Resolved{TargetType: models.DiagnosisTargetWorkflow, Workflow: wf, OrgID: wf.OwnershipOrgID(), ProjectID: wf.ProjectID}, nil
		}
		nodes, err := s.Workflows.ListWorkflowNodes(ctx, wf.WorkflowID)
		if err != nil {
			return nil, err
		}
		for _, node := range nodes {
			if node.Name != t.NodeName {
				continue
			}
			if node.JobID == nil || *node.JobID == "" {
				return nil, invalid("the node %q has no job", t.NodeName)
			}
			job, err := s.Jobs.GetJobByID(ctx, *node.JobID)
			if err != nil || job == nil {
				return nil, store.ErrNotFound
			}
			return &Resolved{TargetType: models.DiagnosisTargetJob, Job: job, Workflow: wf, NodeName: node.Name,
				OrgID: job.OwnershipOrgID(), ProjectID: job.ProjectID}, nil
		}
		return nil, store.ErrNotFound
	}
	return nil, invalid("a job, a workflow node, or a workflow is required")
}

// Post stores the entry of authorPrincipalID for the target, or replaces
// it. created is true for a new entry. The pull request comment is updated
// on a best-effort basis: a failure there does not fail the post.
func (s *Service) Post(ctx context.Context, target *Resolved, authorPrincipalID string, in Input) (*models.DiagnosisReport, bool, error) {
	if target == nil {
		return nil, false, store.ErrNotFound
	}
	authorPrincipalID = strings.TrimSpace(authorPrincipalID)
	if authorPrincipalID == "" {
		return nil, false, ErrNoPrincipal
	}
	if allowed, wait := currentLimiter().Allow(authorPrincipalID); !allowed {
		return nil, false, &RateLimitError{Wait: wait}
	}
	clean, err := in.Validate()
	if err != nil {
		return nil, false, err
	}
	report := &models.DiagnosisReport{OrgID: target.OrgID, ProjectID: target.ProjectID, TargetType: target.TargetType,
		NodeName: target.NodeName, AuthorPrincipalID: authorPrincipalID, Title: clean.Title, Body: clean.Body,
		Confidence: clean.Confidence, Category: clean.Category, Links: clean.Links, SuggestedShifts: clean.SuggestedShifts}
	if s.Principals != nil {
		principal, err := s.Principals.GetPrincipal(ctx, authorPrincipalID)
		if err != nil || principal == nil || !principal.IsActive() {
			return nil, false, ErrNoPrincipal
		}
		report.AuthorKind = principal.Kind
		report.AuthorDisplayName = principal.Label()
	}
	if report.AuthorKind == "" {
		report.AuthorKind = models.PrincipalKindService
	}
	if target.Job != nil {
		jobID := target.Job.JobID
		report.JobID = &jobID
		report.TargetType = models.DiagnosisTargetJob
	}
	if target.Workflow != nil {
		workflowID := target.Workflow.WorkflowID
		report.WorkflowID = &workflowID
	}
	created, err := s.Store.UpsertDiagnosisReport(ctx, report)
	if err != nil {
		return nil, false, err
	}
	details := models.JSONB{"target_type": report.TargetType, "author_principal_id": report.AuthorPrincipalID,
		"revision": report.Revision, "created": created}
	if report.JobID != nil {
		details["job_id"] = *report.JobID
	}
	if report.WorkflowID != nil {
		details["workflow_id"] = *report.WorkflowID
	}
	if report.SuggestedShifts != nil {
		details["suggested_shifts"] = runshifts.ToMap(*report.SuggestedShifts)
	}
	audit.Record(ctx, s.Audit, report.OrgID, "diagnosis.post", "diagnosis", report.DiagnosisID, details)
	if s.Publish != nil {
		_ = s.Publish(ctx, target, report)
	}
	return report, created, nil
}

// List returns the entries of the target. A workflow target also returns
// the entries of the jobs of its nodes.
func (s *Service) List(ctx context.Context, target *Resolved) ([]models.DiagnosisReport, error) {
	if target == nil {
		return nil, store.ErrNotFound
	}
	if target.TargetType == models.DiagnosisTargetWorkflow && target.Workflow != nil {
		return s.Store.ListDiagnosisReportsForWorkflow(ctx, target.Workflow.WorkflowID)
	}
	if target.Job != nil {
		return s.Store.ListDiagnosisReportsForJob(ctx, target.Job.JobID)
	}
	return nil, store.ErrNotFound
}
