package uiapi

import (
	"context"
	"sort"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// The fake's counterparts to postgres_store/visibility_operations.go's
// ListJobsVisibleTo / ListWorkflowSummariesVisibleTo, plus the workflow-summary
// read model GetWorkflow needs.
//
// These deliberately evaluate visibility through the REAL authz.Resolver over
// this same fake store rather than reimplementing the rule. Postgres pushes the
// predicate into SQL and the fake evaluates it in Go, but both must answer
// identically or the tests in ui_jobs_workflows_test.go are testing a fiction.
// Re-deriving the rule here would let the fake drift into agreeing with a bug.

func (f *fakeStore) jobsSnapshot() []models.Job {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]models.Job, 0, len(f.jobs))
	for _, j := range f.jobs {
		out = append(out, j)
	}
	// Map iteration order is random; a list op's pagination must be stable.
	sort.SliceStable(out, func(i, j int) bool { return out[i].JobID < out[j].JobID })
	return out
}

func jobMatchesFilters(job *models.Job, filters map[string]interface{}) bool {
	for key, want := range filters {
		switch key {
		case "status":
			if job.Status != want.(string) {
				return false
			}
		case "queue_name":
			if job.QueueName != want.(string) {
				return false
			}
		case "project_id":
			if job.ProjectID == nil || *job.ProjectID != want.(string) {
				return false
			}
		case "workflow_id":
			if job.WorkflowID == nil || *job.WorkflowID != want.(string) {
				return false
			}
		}
	}
	return true
}

func paginate[T any](rows []T, limit, offset int) []T {
	if offset >= len(rows) {
		return []T{}
	}
	rows = rows[offset:]
	if limit > 0 && limit < len(rows) {
		rows = rows[:limit]
	}
	return rows
}

func (f *fakeStore) ListJobsVisibleTo(ctx context.Context, viewerID string, isGlobalAdmin bool, filters map[string]interface{}, limit, offset int) ([]models.Job, int64, error) {
	// The real query binds viewerID into comparisons against jobs.user_id,
	// projects.user_id, role_assignments.principal_id and group_members.user_id
	// -- all `uuid`. An empty string there is a PostgreSQL type error, not a
	// false, so the fake refuses it too rather than quietly answering
	// differently from the database. See fakes_sqltypes_test.go.
	//
	// Anonymous is expressed by binding NULL, which reaches this method as an
	// empty viewerID together with isGlobalAdmin=false; the caller
	// (UiService.viewerScope) is what maps it, so only a NON-anonymous empty
	// value is a bug.
	if viewerID != "" {
		assertUUIDBindable("jobs.user_id (viewer binding)", viewerID)
	}
	assertFiltersBindable(filters)

	resolver := authz.NewResolver(f)
	identity := viewerIdentity(viewerID)

	visible := make([]models.Job, 0)
	for _, job := range f.jobsSnapshot() {
		if !jobMatchesFilters(&job, filters) {
			continue
		}
		if isGlobalAdmin {
			visible = append(visible, job)
			continue
		}
		ok, err := resolver.CanViewJob(ctx, identity, &job)
		if err != nil {
			return nil, 0, err
		}
		if ok {
			visible = append(visible, job)
		}
	}
	// Total counts the whole visible set, not the page -- the property that
	// makes pagination correct, and the one a naive "filter the page"
	// implementation gets wrong.
	return paginate(visible, limit, offset), int64(len(visible)), nil
}

func (f *fakeStore) ListWorkflowSummariesVisibleTo(ctx context.Context, viewerID string, isGlobalAdmin bool, filters map[string]interface{}, limit, offset int) ([]models.WorkflowSummary, int64, error) {
	if viewerID != "" {
		assertUUIDBindable("workflow_instances.user_id (viewer binding)", viewerID)
	}
	assertFiltersBindable(filters)

	resolver := authz.NewResolver(f)
	identity := viewerIdentity(viewerID)

	visible := make([]models.WorkflowSummary, 0)
	for _, summary := range f.workflowSummariesSnapshot() {
		if want, ok := filters["status"]; ok && summary.Status != want.(string) {
			continue
		}
		if want, ok := filters["project_id"]; ok {
			if summary.ProjectID == nil || *summary.ProjectID != want.(string) {
				continue
			}
		}
		if isGlobalAdmin {
			visible = append(visible, summary)
			continue
		}
		ok, err := resolver.CanViewWorkflowSummary(ctx, identity, &summary)
		if err != nil {
			return nil, 0, err
		}
		if ok {
			visible = append(visible, summary)
		}
	}
	return paginate(visible, limit, offset), int64(len(visible)), nil
}

// workflowSummariesSnapshot projects the fake's workflow instances into the
// denormalized summary read model. Only the fields the UI ops actually read are
// filled in.
func (f *fakeStore) workflowSummariesSnapshot() []models.WorkflowSummary {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]models.WorkflowSummary, 0, len(f.workflows))
	for _, wf := range f.workflows {
		out = append(out, workflowInstanceToSummary(wf))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].WorkflowID < out[j].WorkflowID })
	return out
}

func workflowInstanceToSummary(wf models.WorkflowInstance) models.WorkflowSummary {
	return models.WorkflowSummary{
		WorkflowID:  wf.WorkflowID,
		Kind:        "workflow",
		Name:        wf.Name,
		Status:      wf.Status,
		UserID:      wf.UserID,
		OrgID:       wf.OrgID,
		ProjectID:   wf.ProjectID,
		CreatedAt:   wf.CreatedAt,
		UpdatedAt:   wf.UpdatedAt,
		QueueName:   wf.QueueName,
		VCSRepo:     wf.VCSRepo,
		OriginType:  wf.OriginType,
		TriggerType: wf.TriggerType,
	}
}

// assertFiltersBindable checks the filter values that reach uuid columns.
// status and queue_name are text and are deliberately not checked.
func assertFiltersBindable(filters map[string]interface{}) {
	for _, key := range []string{"project_id", "workflow_id"} {
		if value, ok := filters[key]; ok {
			if text, isText := value.(string); isText {
				assertUUIDBindable(key, text)
			}
		}
	}
}

func (f *fakeStore) GetWorkflowSummary(_ context.Context, workflowID string) (*models.WorkflowSummary, error) {
	assertUUIDBindable("workflow_instances.workflow_id", workflowID)
	f.mu.Lock()
	defer f.mu.Unlock()
	wf, ok := f.workflows[workflowID]
	if !ok {
		return nil, store.ErrNotFound
	}
	summary := workflowInstanceToSummary(wf)
	return &summary, nil
}

// putWorkflowNodes seeds a workflow's DAG rows directly, for test setup.
func (f *fakeStore) putWorkflowNodes(workflowID string, nodes []models.WorkflowNode) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nodes[workflowID] = append([]models.WorkflowNode{}, nodes...)
}

// viewerIdentity mirrors UiService.viewerScope's contract: an empty viewer id
// is the anonymous caller, not a user whose id happens to be "".
func viewerIdentity(viewerID string) authz.Identity {
	if viewerID == "" {
		return authz.AnonymousIdentity()
	}
	return authz.UserIdentity(viewerID)
}
