package oidcissuer

import (
	"context"
	"fmt"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/refclass"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// FactsStore is the store surface that LoadJobFacts reads.
type FactsStore interface {
	GetOrganizationByID(ctx context.Context, orgID string) (*models.Organization, error)
	GetProjectByID(ctx context.Context, projectID string) (*models.Project, error)
	GetWorkflowInstance(ctx context.Context, workflowID string) (*models.WorkflowInstance, error)
	GetCIEvent(ctx context.Context, eventID string) (*models.CIEvent, error)
	GetPrincipal(ctx context.Context, principalID string) (*models.Principal, error)
}

// EventTypeAPI is the event_type of a job that no CI event started (an API
// submit).
const EventTypeAPI = "api"

// EventTypeManual is the event_type of a job of a manual event: an API eval
// submit or a manual run.
const EventTypeManual = "manual"

// LoadJobFacts reads the facts of job from coordinator records only. The job
// environment is never a source: a job cannot change its own claims.
func LoadJobFacts(ctx context.Context, st FactsStore, job *models.Job) (JobFacts, error) {
	facts := JobFacts{
		OrgID: job.OwnershipOrgID(), JobID: job.JobID, Node: job.WorkflowNodeName,
		CIOrigin: job.CIOrigin, ExecutionProfile: job.ExecutionProfile, PolicyRevision: job.PolicyRevision,
		// owner_group: the owner snapshot of the job (WP7), read from the
		// trusted CI definition when the workflow was created. The claim is
		// always present; "" when the workflow has no owner or it is unknown.
		OwnerGroup: job.Ownership().GroupName(),
		RefClasses: refclass.ForJob(job),
	}
	org, err := st.GetOrganizationByID(ctx, facts.OrgID)
	if err != nil || org == nil {
		return facts, fmt.Errorf("oidc: the organization of the job is not found")
	}
	facts.Org = org.Name
	if job.ProjectID != nil && *job.ProjectID != "" {
		project, err := st.GetProjectByID(ctx, *job.ProjectID)
		if err != nil || project == nil {
			return facts, fmt.Errorf("oidc: the project of the job is not found")
		}
		facts.ProjectID = project.ProjectID
		facts.Project = project.Name
	}
	if job.VCSRepo != nil {
		facts.Repository = *job.VCSRepo
	}
	if job.CommitSHA != nil {
		facts.SHA = *job.CommitSHA
	}
	if job.WorkflowID != nil && *job.WorkflowID != "" {
		workflow, err := st.GetWorkflowInstance(ctx, *job.WorkflowID)
		if err == nil && workflow != nil {
			facts.WorkflowID = workflow.WorkflowID
			facts.WorkflowName = workflow.Name
			facts.WorkflowSecurityID = workflow.WorkflowSecurityID
			if facts.Repository == "" {
				facts.Repository = workflow.VCSRepo
			}
			if facts.SHA == "" {
				facts.SHA = workflow.CommitSHA
			}
			if facts.CIOrigin == "" {
				facts.CIOrigin = workflow.CIOrigin
			}
			if facts.ExecutionProfile == "" {
				facts.ExecutionProfile = workflow.ExecutionProfile
			}
			if facts.PolicyRevision == "" {
				facts.PolicyRevision = workflow.PolicyRevision
			}
			if len(facts.RefClasses) == 0 {
				facts.RefClasses = refclass.ForWorkflow(workflow)
			}
			if facts.OwnerGroup == "" {
				facts.OwnerGroup = workflow.Ownership().GroupName()
			}
		}
	}
	var event *models.CIEvent
	if job.EventID != nil && *job.EventID != "" {
		event, _ = st.GetCIEvent(ctx, *job.EventID)
	}
	if event != nil {
		facts.EventID = event.EventID
		facts.EventType = event.EventType
		if event.Source == models.CIEventSourceManual {
			// Every manual event has the event type "manual", also a
			// coordinator-resolved manual run that the coordinator records
			// as a push or a pull request event.
			facts.EventType = EventTypeManual
		}
		if facts.Repository == "" {
			facts.Repository = event.Repo
		}
		if facts.SHA == "" {
			facts.SHA = event.CommitSHA
		}
		if len(facts.RefClasses) == 0 {
			facts.RefClasses = refclass.ForEvent(event)
		}
		facts.Ref, facts.HeadRef, facts.BaseRef, facts.SubmittedRef = eventRefs(event)
		if event.PRNumber != nil {
			facts.PRNumber = *event.PRNumber
		}
	} else {
		facts.EventType = EventTypeAPI
		if job.SourceRef != nil {
			// Only the submitter states this ref, and it can state
			// "refs/heads/main". Thus the ref claim (and the ref part of
			// the subject) stays empty, and the stated ref goes only to
			// submitted_ref, not normalized.
			facts.SubmittedRef = *job.SourceRef
		}
	}
	if job.ActorPrincipalID != nil && *job.ActorPrincipalID != "" {
		facts.ActorPrincipalID = *job.ActorPrincipalID
		if actor, err := st.GetPrincipal(ctx, *job.ActorPrincipalID); err == nil && actor != nil {
			facts.ActorPrincipalID = actor.PrincipalID
			facts.ActorKind = actor.Kind
		}
	}
	if facts.RefClasses == nil {
		facts.RefClasses = []string{}
	}
	return facts, nil
}

// eventRefs returns the ref, head ref, base ref, and submitted ref claims of
// an event.
//
// A pull request event always has the ref refs/pull/N/head. The head branch
// name goes only to head_ref: a fork can name its branch "main", and that
// must never look like refs/heads/main.
//
// A manual event that an API client submitted (an eval job with
// REACTORCIDE_JOB_KIND=eval) has a ref that only the submitter states. Its
// ref is empty, so its subject never matches a consumer rule for a real ref,
// and the stated ref goes only to submitted_ref, not normalized. A manual
// run that the coordinator started (POST /api/v1/projects/{id}/runs) has a
// ref and a commit that the coordinator got from the VCS, so it keeps the
// real ref; its event_type is still "manual".
func eventRefs(event *models.CIEvent) (ref, headRef, baseRef, submittedRef string) {
	if event.Source == models.CIEventSourceManual && !coordinatorResolvedManual(event) {
		submitted := event.Ref
		if submitted == "" {
			submitted = event.Branch
		}
		return "", "", "", submitted
	}
	if event.PRNumber != nil && *event.PRNumber > 0 {
		return fmt.Sprintf("refs/pull/%d/head", *event.PRNumber), event.Ref, event.Branch, ""
	}
	if strings.HasPrefix(event.Ref, "refs/") {
		return event.Ref, "", "", ""
	}
	if event.Branch != "" {
		return "refs/heads/" + event.Branch, "", "", ""
	}
	return event.Ref, "", "", ""
}

// coordinatorResolvedManual reports whether a manual event is a manual run
// that the coordinator resolved from the VCS. The coordinator records such
// a run with the provider and the event type of the synthesized VCS event
// (push, tag_created, or pull_request_updated). An API eval submit always
// has the event type "manual" and no provider.
func coordinatorResolvedManual(event *models.CIEvent) bool {
	return event.Provider != "" && event.EventType != models.CIEventSourceManual
}
