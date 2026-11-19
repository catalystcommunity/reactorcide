package priority

import (
	"context"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/refclass"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/shedding"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/sirupsen/logrus"
)

// The store surfaces that Assign reads. Each is optional: a store without
// one gives fewer facts, and the built-in lanes still apply.
type jobGetter interface {
	GetJobByID(ctx context.Context, jobID string) (*models.Job, error)
}

type workflowGetter interface {
	GetWorkflowInstance(ctx context.Context, workflowID string) (*models.WorkflowInstance, error)
}

// noActorShiftSources are the event sources whose actor starts work again
// that another actor started: an approval, a rerun command, a retry. Their
// actor's groups do not shift the priority (docs/priority.md).
var noActorShiftSources = map[string]bool{
	models.CIEventSourceRetry:    true,
	models.CIEventSourceRerun:    true,
	models.CIEventSourceApproval: true,
}

// LoadLanes returns the effective lanes of an organization and project. A
// store without lanes, or a failed read, gives the built-in lanes.
func LoadLanes(ctx context.Context, st any, orgID string, projectID *string) []Lane {
	ps, ok := st.(store.PriorityStore)
	if !ok || orgID == "" {
		return Effective(nil, nil)
	}
	orgLanes, err := ps.ListPriorityLanes(ctx, orgID, nil)
	if err != nil {
		logrus.WithError(err).WithField("org_id", orgID).Warn("Could not read the priority lanes; using the built-in lanes")
		return Effective(nil, nil)
	}
	var projectLanes []models.PriorityLane
	if projectID != nil && *projectID != "" {
		projectLanes, err = ps.ListPriorityLanes(ctx, orgID, projectID)
		if err != nil {
			logrus.WithError(err).WithField("project_id", *projectID).Warn("Could not read the project priority lanes; using the organization lanes")
			projectLanes = nil
		}
	}
	return Effective(orgLanes, projectLanes)
}

// LoadGroupShifts returns the group shifts for the facts. A store without
// groups, or a failed read, gives no shifts.
func LoadGroupShifts(ctx context.Context, st any, facts Facts) GroupShifts {
	ps, ok := st.(store.PriorityStore)
	if !ok || facts.OrgID == "" {
		return GroupShifts{}
	}
	var shifts GroupShifts
	if facts.ActorPrincipalID != "" && facts.ActorPrincipalID != models.SystemPrincipalID {
		actor, err := ps.ListPrincipalGroupShifts(ctx, facts.OrgID, facts.ActorPrincipalID)
		if err != nil {
			logrus.WithError(err).Warn("Could not read the group priority shifts of the actor")
		}
		shifts.Actor = actor
	}
	// WP7 hook: the owner group of the workflow shifts the priority too.
	if len(facts.OwnerGroupIDs) > 0 {
		owner, err := ps.ListGroupShifts(ctx, facts.OrgID, facts.OwnerGroupIDs)
		if err != nil {
			logrus.WithError(err).Warn("Could not read the priority shifts of the owner groups")
		}
		shifts.Owner = owner
	}
	return shifts
}

// FactsForJob collects the trusted facts of a job that is about to be
// stored. It reads the CI event, the workflow, and the parent job from the
// store when the job does not carry the value itself.
func FactsForJob(ctx context.Context, st any, job *models.Job) Facts {
	facts := Facts{OrgID: job.OwnershipOrgID(), ProjectID: job.ProjectID, JobKind: models.PriorityJobKindWork}
	if kind, _ := job.JobEnvVars["REACTORCIDE_JOB_KIND"].(string); strings.TrimSpace(kind) == "eval" {
		facts.JobKind = models.PriorityJobKindEval
	}
	var event *models.CIEvent
	if job.EventID != nil && *job.EventID != "" {
		if events, ok := st.(store.CIEventStore); ok {
			if value, err := events.GetCIEvent(ctx, *job.EventID); err == nil {
				event = value
			}
		}
	}
	var parent *models.Job
	if job.ParentJobID != nil && *job.ParentJobID != "" {
		if jobs, ok := st.(jobGetter); ok {
			if value, err := jobs.GetJobByID(ctx, *job.ParentJobID); err == nil {
				parent = value
			}
		}
	}

	// Ref classes: the job, else the workflow, the event, the parent job (the
	// order of the database trigger that fills jobs.ref_classes).
	classes := []string(job.RefClasses)
	if len(classes) == 0 && job.WorkflowID != nil && *job.WorkflowID != "" {
		if workflows, ok := st.(workflowGetter); ok {
			if wf, err := workflows.GetWorkflowInstance(ctx, *job.WorkflowID); err == nil && wf != nil {
				classes = wf.RefClasses
			}
		}
	}
	if len(classes) == 0 && event != nil {
		classes = event.RefClasses
	}
	if len(classes) == 0 && parent != nil {
		classes = parent.RefClasses
	}
	if len(classes) == 0 && job.PRNumber != nil && *job.PRNumber > 0 {
		// A store without CI events: the coordinator set the pull request
		// from the signed webhook (vcs.JobMetadata).
		classes = []string{"pr"}
	}
	facts.RefClasses = refclass.Normalize(classes)

	if event != nil {
		facts.EventType = event.EventType
		facts.EventSource = event.Source
		if event.Source == models.CIEventSourceManual {
			facts.EventType = "manual"
		}
		facts.Draft = event.Summary.Draft && refclass.Has(facts.RefClasses, "pr")
	}
	// The actor whose groups shift the priority: the actor of the event,
	// else the actor of the job (the submitter), else the actor of the
	// parent job. The actor of a rerun, retry or approval event started
	// work again that another actor started: no actor shift.
	if event == nil || !noActorShiftSources[event.Source] {
		switch {
		case event != nil && event.ActorPrincipalID != nil && *event.ActorPrincipalID != "":
			facts.ActorPrincipalID = *event.ActorPrincipalID
		case job.ActorPrincipalID != nil && *job.ActorPrincipalID != "":
			facts.ActorPrincipalID = *job.ActorPrincipalID
		case parent != nil && parent.ActorPrincipalID != nil:
			facts.ActorPrincipalID = *parent.ActorPrincipalID
		}
	}

	if requested, ok := RequestedOf(job.PriorityReason); ok {
		facts.Requested = requested
	} else {
		facts.Requested = job.Priority
	}
	facts.Shed = shedding.State(ctx, st, facts.OrgID).Actions
	facts.OwnerGroupIDs = ownerGroupIDs(ctx, st, job, facts.OrgID)
	return facts
}

// ownerGroupStore resolves owner group names to groups (WP7).
type ownerGroupStore interface {
	ListGroupsByOrg(ctx context.Context, orgID string) ([]models.Group, error)
}

// vcsTeamGroupStore is the optional store surface of groups linked to a VCS
// team (plan B3); the same method as authz.VCSTeamGroupStore.
type vcsTeamGroupStore interface {
	ListGroupsLinkedToVCSTeam(ctx context.Context, orgID, team string) ([]models.Group, error)
}

// ownerGroupIDs returns the groups of the trusted owner snapshot of the
// job's workflow (WP7): the Reactorcide group of that name in the org, or the
// groups linked to the VCS team. Extra owner subjects give no group.
func ownerGroupIDs(ctx context.Context, st any, job *models.Job, orgID string) []string {
	ownership := job.Ownership()
	if !ownership.Known() && job.WorkflowID != nil && *job.WorkflowID != "" {
		if workflows, ok := st.(workflowGetter); ok {
			if wf, err := workflows.GetWorkflowInstance(ctx, *job.WorkflowID); err == nil && wf != nil {
				ownership = wf.Ownership()
			}
		}
	}
	group := ownership.GroupName()
	if group == "" || orgID == "" {
		return nil
	}
	if team, ok := strings.CutPrefix(group, "vcs_team:"); ok {
		teams, ok := st.(vcsTeamGroupStore)
		if !ok {
			return nil
		}
		groups, err := teams.ListGroupsLinkedToVCSTeam(ctx, orgID, team)
		if err != nil {
			return nil
		}
		ids := []string{}
		for _, g := range groups {
			ids = append(ids, g.GroupID)
		}
		return ids
	}
	groupsStore, ok := st.(ownerGroupStore)
	if !ok {
		return nil
	}
	groups, err := groupsStore.ListGroupsByOrg(ctx, orgID)
	if err != nil {
		return nil
	}
	for _, g := range groups {
		if g.Name == group {
			return []string{g.GroupID}
		}
	}
	return nil
}

// Assign computes the priority of a new job and writes it into the job
// (Priority, PriorityLane, PriorityReason). Call it on every submit path
// before the job is stored and submitted to Corndogs. The job's Priority
// before the call is the requested priority (from the job specification or
// the submitter).
func Assign(ctx context.Context, st any, job *models.Job) Decision {
	if job == nil {
		return Decision{}
	}
	facts := FactsForJob(ctx, st, job)
	decision := Compute(LoadLanes(ctx, st, facts.OrgID, facts.ProjectID), LoadGroupShifts(ctx, st, facts), facts)
	decision.Apply(job)
	return decision
}

// AssignRetry gives a retry the priority of its original job (Retain). A
// retry lane can change the lane; the re-runner's groups never apply. An
// original without a recorded decision (made before lanes) keeps its stored
// priority, and the retry gets no lane.
func AssignRetry(ctx context.Context, st any, original, retry *models.Job) Decision {
	if retry == nil {
		return Decision{}
	}
	facts := FactsForJob(ctx, st, retry)
	facts.Retry = true
	decision, ok := Retain(original, LoadLanes(ctx, st, facts.OrgID, facts.ProjectID), facts)
	if !ok {
		if original != nil {
			retry.Priority = original.Priority
		}
		retry.PriorityLane = ""
		retry.PriorityReason = nil
		return Decision{Lane: "", Base: retry.Priority, Final: retry.Priority, Shifts: []models.PriorityShift{}}
	}
	decision.Apply(retry)
	return decision
}
