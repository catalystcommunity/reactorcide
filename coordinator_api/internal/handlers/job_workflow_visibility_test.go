package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// roleAwareMockStore extends MockStore with the authz.RoleStore methods
// (GetUserByID, GetProjectByID, ListGroupsForUser,
// ListRoleAssignmentsForPrincipal) so JobHandler/WorkflowHandler
// constructors wire up a non-nil visibility resolver (see
// handlers/authz_visibility.go's roleStoreResolver), letting these tests
// exercise Task D's additive public-visibility path end to end through the
// real HTTP handlers rather than just the authz package in isolation.
//
// It also implements jobsVisibleToStore/workflowSummaryVisibleToStore
// (ListJobsVisibleTo/ListWorkflowSummariesVisibleTo) as an in-memory fake
// over allJobs/allWorkflowSummaries: filter by the same keys the real SQL
// predicate honors, apply authz's real FilterVisibleJobs/
// FilterVisibleWorkflowSummaries (built from this same fake RoleStore) when
// the viewer isn't a global admin, then paginate and count the
// already-visibility-filtered set. This is deliberately NOT a
// re-implementation of the SQL predicate (that's covered by
// coordinator_api/test's Postgres-backed integration test) — it exists so
// ListJobs/ListWorkflows' handler-level pagination/Total wiring can be
// tested against the primary (SQL-side-visibility) code path without a
// database.
type roleAwareMockStore struct {
	*MockStore

	users       map[string]*models.User
	projects    map[string]*models.Project
	assignments []models.RoleAssignment

	allJobs              []models.Job
	allWorkflowSummaries []models.WorkflowSummary
}

func newRoleAwareMockStore() *roleAwareMockStore {
	return &roleAwareMockStore{
		MockStore: &MockStore{},
		users:     map[string]*models.User{},
		projects:  map[string]*models.Project{},
	}
}

// ListJobsVisibleTo implements jobsVisibleToStore — see the struct doc.
func (m *roleAwareMockStore) ListJobsVisibleTo(ctx context.Context, viewerID string, isGlobalAdmin bool, filters map[string]interface{}, limit, offset int) ([]models.Job, int64, error) {
	filtered := make([]models.Job, 0, len(m.allJobs))
	for _, j := range m.allJobs {
		if uid, ok := filters["user_id"].(string); ok && j.UserID != uid {
			continue
		}
		if st, ok := filters["status"].(string); ok && j.Status != st {
			continue
		}
		if pid, ok := filters["project_id"].(string); ok {
			if j.ProjectID == nil || *j.ProjectID != pid {
				continue
			}
		}
		if wid, ok := filters["workflow_id"].(string); ok {
			if j.WorkflowID == nil || *j.WorkflowID != wid {
				continue
			}
		}
		filtered = append(filtered, j)
	}

	if !isGlobalAdmin {
		resolver := authz.NewResolver(m)
		visible, err := resolver.FilterVisibleJobs(ctx, authz.UserIdentity(viewerID), filtered)
		if err != nil {
			return nil, 0, err
		}
		filtered = visible
	}

	return paginateJobs(filtered, limit, offset), int64(len(filtered)), nil
}

// ListWorkflowSummariesVisibleTo implements workflowSummaryVisibleToStore —
// see the struct doc.
func (m *roleAwareMockStore) ListWorkflowSummariesVisibleTo(ctx context.Context, viewerID string, isGlobalAdmin bool, filters map[string]interface{}, limit, offset int) ([]models.WorkflowSummary, int64, error) {
	filtered := make([]models.WorkflowSummary, 0, len(m.allWorkflowSummaries))
	for _, s := range m.allWorkflowSummaries {
		if uid, ok := filters["user_id"].(string); ok && s.UserID != uid {
			continue
		}
		if st, ok := filters["status"].(string); ok && s.Status != st {
			continue
		}
		if pid, ok := filters["project_id"].(string); ok {
			if s.ProjectID == nil || *s.ProjectID != pid {
				continue
			}
		}
		filtered = append(filtered, s)
	}

	if !isGlobalAdmin {
		resolver := authz.NewResolver(m)
		visible, err := resolver.FilterVisibleWorkflowSummaries(ctx, authz.UserIdentity(viewerID), filtered)
		if err != nil {
			return nil, 0, err
		}
		filtered = visible
	}

	total := int64(len(filtered))
	start := offset
	if start > len(filtered) {
		start = len(filtered)
	}
	end := start + limit
	if limit <= 0 || end > len(filtered) {
		end = len(filtered)
	}
	if end < start {
		end = start
	}
	return append([]models.WorkflowSummary{}, filtered[start:end]...), total, nil
}

func paginateJobs(jobs []models.Job, limit, offset int) []models.Job {
	start := offset
	if start > len(jobs) {
		start = len(jobs)
	}
	end := start + limit
	if limit <= 0 || end > len(jobs) {
		end = len(jobs)
	}
	if end < start {
		end = start
	}
	return append([]models.Job{}, jobs[start:end]...)
}

func (m *roleAwareMockStore) GetUserByID(ctx context.Context, userID string) (*models.User, error) {
	if u, ok := m.users[userID]; ok {
		return u, nil
	}
	return nil, store.ErrNotFound
}

func (m *roleAwareMockStore) GetProjectByID(ctx context.Context, projectID string) (*models.Project, error) {
	if p, ok := m.projects[projectID]; ok {
		return p, nil
	}
	return nil, store.ErrNotFound
}

func (m *roleAwareMockStore) ListGroupsForUser(ctx context.Context, userID string) ([]models.Group, error) {
	return nil, nil
}

func (m *roleAwareMockStore) ListRoleAssignmentsForPrincipal(ctx context.Context, userID string, groupIDs []string) ([]models.RoleAssignment, error) {
	var out []models.RoleAssignment
	for _, a := range m.assignments {
		if a.PrincipalType == models.PrincipalTypeUser && a.PrincipalID == userID {
			out = append(out, a)
		}
	}
	return out, nil
}

func jobRequestWithID(method, path, jobID string, user *models.User) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	ctx := checkauth.SetUserContext(req.Context(), user)
	ctx = context.WithValue(ctx, GetContextKey("job_id"), jobID)
	return req.WithContext(ctx)
}

func workflowRequestWithID(method, path, workflowID string, user *models.User) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	ctx := checkauth.SetUserContext(req.Context(), user)
	ctx = context.WithValue(ctx, GetContextKey("workflow_id"), workflowID)
	return req.WithContext(ctx)
}

// --- JobHandler.GetJob ------------------------------------------------------

func TestJobHandler_GetJob_PublicVisibleAcrossOrgs(t *testing.T) {
	ms := newRoleAwareMockStore()
	ms.users["org-owner"] = &models.User{UserID: "org-owner", IsPrivate: false}
	job := &models.Job{JobID: "job-1", UserID: "org-owner", Status: "completed"}
	ms.GetJobByIDFunc = func(ctx context.Context, jobID string) (*models.Job, error) {
		if jobID == "job-1" {
			return job, nil
		}
		return nil, store.ErrNotFound
	}

	h := NewJobHandler(ms, nil)
	if h.visibility == nil {
		t.Fatal("expected NewJobHandler to wire a visibility resolver for a RoleStore-capable store")
	}

	stranger := &models.User{UserID: "stranger", Roles: []string{"user"}}
	req := jobRequestWithID(http.MethodGet, "/api/v1/jobs/job-1", "job-1", stranger)
	rr := httptest.NewRecorder()
	h.GetJob(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for a public job viewed by a stranger, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestJobHandler_GetJob_PrivateOrgDeniedToStranger(t *testing.T) {
	ms := newRoleAwareMockStore()
	ms.users["org-owner"] = &models.User{UserID: "org-owner", IsPrivate: true}
	job := &models.Job{JobID: "job-1", UserID: "org-owner", Status: "completed"}
	ms.GetJobByIDFunc = func(ctx context.Context, jobID string) (*models.Job, error) {
		if jobID == "job-1" {
			return job, nil
		}
		return nil, store.ErrNotFound
	}

	h := NewJobHandler(ms, nil)

	stranger := &models.User{UserID: "stranger", Roles: []string{"user"}}
	req := jobRequestWithID(http.MethodGet, "/api/v1/jobs/job-1", "job-1", stranger)
	rr := httptest.NewRecorder()
	h.GetJob(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a private-org job viewed by a stranger, got %d: %s", rr.Code, rr.Body.String())
	}

	owner := &models.User{UserID: "org-owner", Roles: []string{"user"}}
	req2 := jobRequestWithID(http.MethodGet, "/api/v1/jobs/job-1", "job-1", owner)
	rr2 := httptest.NewRecorder()
	h.GetJob(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("expected 200 for the job's own owner, got %d: %s", rr2.Code, rr2.Body.String())
	}

	orgAdmin := &models.User{UserID: "org-admin", Roles: []string{"user"}}
	ms.assignments = []models.RoleAssignment{
		{PrincipalType: models.PrincipalTypeUser, PrincipalID: "org-admin", ScopeType: models.ScopeTypeOrg, ScopeID: strPtrH("org-owner"), Role: models.RoleAdmin},
	}
	req3 := jobRequestWithID(http.MethodGet, "/api/v1/jobs/job-1", "job-1", orgAdmin)
	rr3 := httptest.NewRecorder()
	h.GetJob(rr3, req3)
	if rr3.Code != http.StatusOK {
		t.Fatalf("expected 200 for an org admin of the job's owning org, got %d: %s", rr3.Code, rr3.Body.String())
	}
}

// TestJobHandler_GetJob_NoVisibilityResolver_UnchangedBehavior pins down
// that when the wired store does NOT satisfy authz.RoleStore (the plain
// MockStore used throughout the rest of this package's existing tests),
// GetJob's authorization is exactly the pre-Task-D owner-or-admin check —
// a public job from a stranger's org is still denied, proving the additive
// change never regresses (or accidentally activates) without an
// authz-capable store.
func TestJobHandler_GetJob_NoVisibilityResolver_UnchangedBehavior(t *testing.T) {
	job := &models.Job{JobID: "job-1", UserID: "org-owner", Status: "completed"}
	ms := &MockStore{
		GetJobByIDFunc: func(ctx context.Context, jobID string) (*models.Job, error) {
			if jobID == "job-1" {
				return job, nil
			}
			return nil, store.ErrNotFound
		},
	}
	h := NewJobHandler(ms, nil)
	if h.visibility != nil {
		t.Fatal("expected a plain MockStore to not satisfy authz.RoleStore")
	}

	stranger := &models.User{UserID: "stranger", Roles: []string{"user"}}
	req := jobRequestWithID(http.MethodGet, "/api/v1/jobs/job-1", "job-1", stranger)
	rr := httptest.NewRecorder()
	h.GetJob(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 (unchanged pre-authz behavior) without a RoleStore-capable store, got %d", rr.Code)
	}
}

// --- JobHandler.ListJobs -----------------------------------------------

// TestJobHandler_ListJobs_IncludesPublicJobsFromOtherOrgs exercises the
// primary SQL-side-visibility path (jobsVisibleToStore —
// roleAwareMockStore.ListJobsVisibleTo): a caller's own jobs and public
// jobs from other orgs are visible, private jobs from other orgs are not,
// and Total reflects the real visible-row count (not a page length) — see
// jobsVisibleToStore's doc comment on job_handler.go for why the OLD
// combination of a relaxed store-level ListJobs filter plus a post-query
// authz.FilterVisibleJobs pass (this test used to assert on that filter
// directly) is exactly the pagination/Total bug this fixes.
func TestJobHandler_ListJobs_IncludesPublicJobsFromOtherOrgs(t *testing.T) {
	ms := newRoleAwareMockStore()
	ms.users["org-public"] = &models.User{UserID: "org-public", IsPrivate: false}
	ms.users["org-private"] = &models.User{UserID: "org-private", IsPrivate: true}

	caller := &models.User{UserID: "caller", Roles: []string{"user"}}

	ms.allJobs = []models.Job{
		{JobID: "mine", UserID: "caller", Status: "completed"},
		{JobID: "public-other", UserID: "org-public", Status: "completed"},
		{JobID: "private-other", UserID: "org-private", Status: "completed"},
	}

	h := NewJobHandler(ms, nil)
	if _, ok := interface{}(ms).(jobsVisibleToStore); !ok {
		t.Fatal("expected roleAwareMockStore to implement jobsVisibleToStore")
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
	req = req.WithContext(checkauth.SetUserContext(req.Context(), caller))
	rr := httptest.NewRecorder()
	h.ListJobs(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp ListJobsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	gotIDs := map[string]bool{}
	for _, j := range resp.Jobs {
		gotIDs[j.JobID] = true
	}
	if !gotIDs["mine"] {
		t.Error("expected the caller's own job to be present")
	}
	if !gotIDs["public-other"] {
		t.Error("expected a public job from another org to be present")
	}
	if gotIDs["private-other"] {
		t.Error("expected a private job from another org to be absent")
	}
	if resp.Total != 2 {
		t.Errorf("expected Total to be the real visible-row count (2), got %d", resp.Total)
	}
}

// TestJobHandler_ListJobs_PaginationReturnsFullPages is Finding 2's
// pagination regression test: with more visible jobs than fit in one page,
// each page must come back FULL (limit-sized, until the last page) instead
// of short — the bug this fixes was applying LIMIT/OFFSET at the store
// layer and THEN filtering the page down to visible rows in Go, which could
// return a short (or empty) page even when more visible rows existed past
// the offset.
func TestJobHandler_ListJobs_PaginationReturnsFullPages(t *testing.T) {
	ms := newRoleAwareMockStore()
	caller := &models.User{UserID: "caller", Roles: []string{"user"}}

	for i := 0; i < 5; i++ {
		ms.allJobs = append(ms.allJobs, models.Job{
			JobID:  fmt.Sprintf("job-%d", i),
			UserID: "caller",
			Status: "completed",
		})
	}

	h := NewJobHandler(ms, nil)

	fetchPage := func(offset int) ListJobsResponse {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/jobs?limit=2&offset=%d", offset), nil)
		req = req.WithContext(checkauth.SetUserContext(req.Context(), caller))
		rr := httptest.NewRecorder()
		h.ListJobs(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("offset=%d: expected 200, got %d: %s", offset, rr.Code, rr.Body.String())
		}
		var resp ListJobsResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("offset=%d: decode response: %v", offset, err)
		}
		return resp
	}

	page1 := fetchPage(0)
	if len(page1.Jobs) != 2 {
		t.Errorf("expected a full page of 2 at offset=0, got %d", len(page1.Jobs))
	}
	if page1.Total != 5 {
		t.Errorf("expected Total=5 at offset=0, got %d", page1.Total)
	}

	page2 := fetchPage(2)
	if len(page2.Jobs) != 2 {
		t.Errorf("expected a full page of 2 at offset=2, got %d", len(page2.Jobs))
	}
	if page2.Total != 5 {
		t.Errorf("expected Total=5 at offset=2, got %d", page2.Total)
	}

	page3 := fetchPage(4)
	if len(page3.Jobs) != 1 {
		t.Errorf("expected the final short page (1 job) at offset=4, got %d", len(page3.Jobs))
	}
	if page3.Total != 5 {
		t.Errorf("expected Total=5 at offset=4, got %d", page3.Total)
	}

	seen := map[string]bool{}
	for _, resp := range []ListJobsResponse{page1, page2, page3} {
		for _, j := range resp.Jobs {
			if seen[j.JobID] {
				t.Errorf("job %s returned on more than one page", j.JobID)
			}
			seen[j.JobID] = true
		}
	}
	if len(seen) != 5 {
		t.Errorf("expected all 5 jobs to be seen across pages, saw %d", len(seen))
	}
}

// TestJobHandler_ListJobs_FallbackStrictScoping_NoBrokenMiddleState covers
// the fallback path (store doesn't implement jobsVisibleToStore): ListJobs
// must fall all the way back to the strict pre-authz own-jobs-only SQL
// scoping (parseFiltersStrict) with NO post-query filter layered on top —
// "never ship the broken middle state" of a relaxed filter uncompensated by
// SQL-side visibility. A plain MockStore never satisfies authz.RoleStore
// either, so this also exercises h.visibility == nil.
func TestJobHandler_ListJobs_FallbackStrictScoping_NoBrokenMiddleState(t *testing.T) {
	otherOrgJob := models.Job{JobID: "not-mine", UserID: "someone-else", Status: "completed"}
	myJob := models.Job{JobID: "mine", UserID: "caller", Status: "completed"}

	ms := &MockStore{
		ListJobsFunc: func(ctx context.Context, filters map[string]interface{}, limit, offset int) ([]models.Job, error) {
			uid, _ := filters["user_id"].(string)
			if uid != "caller" {
				t.Fatalf("expected the fallback path to force user_id=caller, got filters=%+v", filters)
			}
			return []models.Job{myJob}, nil
		},
	}
	_ = otherOrgJob // documents what the strict filter must exclude

	h := NewJobHandler(ms, nil)
	if h.visibility != nil {
		t.Fatal("expected a plain MockStore to not satisfy authz.RoleStore")
	}

	caller := &models.User{UserID: "caller", Roles: []string{"user"}}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
	req = req.WithContext(checkauth.SetUserContext(req.Context(), caller))
	rr := httptest.NewRecorder()
	h.ListJobs(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp ListJobsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Jobs) != 1 || resp.Jobs[0].JobID != "mine" {
		t.Fatalf("expected exactly the caller's own job, got %+v", resp.Jobs)
	}
}

// --- WorkflowHandler.GetWorkflow (loose-job fallback path) ------------------

func TestWorkflowHandler_GetWorkflow_PublicJobVisibleAcrossOrgs(t *testing.T) {
	ms := newRoleAwareMockStore()
	ms.users["org-owner"] = &models.User{UserID: "org-owner", IsPrivate: false}
	job := &models.Job{JobID: "wf-1", UserID: "org-owner", Status: "completed"}
	ms.GetJobByIDFunc = func(ctx context.Context, jobID string) (*models.Job, error) {
		if jobID == "wf-1" {
			return job, nil
		}
		return nil, store.ErrNotFound
	}

	h := NewWorkflowHandler(ms)
	if h.visibility == nil {
		t.Fatal("expected NewWorkflowHandler to wire a visibility resolver for a RoleStore-capable store")
	}

	stranger := &models.User{UserID: "stranger", Roles: []string{"user"}}
	req := workflowRequestWithID(http.MethodGet, "/api/v1/workflows/wf-1", "wf-1", stranger)
	rr := httptest.NewRecorder()
	h.GetWorkflow(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for a public loose-job workflow viewed by a stranger, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestWorkflowHandler_GetWorkflow_PrivateOrgDeniedToStranger(t *testing.T) {
	ms := newRoleAwareMockStore()
	ms.users["org-owner"] = &models.User{UserID: "org-owner", IsPrivate: true}
	job := &models.Job{JobID: "wf-1", UserID: "org-owner", Status: "completed"}
	ms.GetJobByIDFunc = func(ctx context.Context, jobID string) (*models.Job, error) {
		if jobID == "wf-1" {
			return job, nil
		}
		return nil, store.ErrNotFound
	}

	h := NewWorkflowHandler(ms)

	stranger := &models.User{UserID: "stranger", Roles: []string{"user"}}
	req := workflowRequestWithID(http.MethodGet, "/api/v1/workflows/wf-1", "wf-1", stranger)
	rr := httptest.NewRecorder()
	h.GetWorkflow(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a private-org loose-job workflow viewed by a stranger, got %d: %s", rr.Code, rr.Body.String())
	}
}

func strPtrH(s string) *string { return &s }

// --- JobHandler kill authorization (Finding 1: kill must be org-admin/
// global-admin gated, not owner-or-legacy-admin gated like cancel) --------

func killJobRequest(jobID string, user *models.User) *http.Request {
	return jobRequestWithID(http.MethodPost, "/api/v1/jobs/"+jobID+"/kill", jobID, user)
}

// TestJobHandler_KillJob_DirectOwnerCanKill_ReflexiveOrgAdmin: a job's
// direct creator (job.UserID == caller.UserID) can still kill their own
// job through a RoleStore-backed resolver — not via a special-cased
// ownership check, but because authz.Resolver.IsOrgAdmin treats a user as
// the admin of their own org by definition (users act as orgs in this
// schema; see IsOrgAdmin's doc comment), and RequireOrgAdmin(job.UserID) is
// exactly what canUserKillJob evaluates.
func TestJobHandler_KillJob_DirectOwnerCanKill_ReflexiveOrgAdmin(t *testing.T) {
	ms := newRoleAwareMockStore()
	ms.GetJobByIDFunc = func(ctx context.Context, jobID string) (*models.Job, error) {
		return &models.Job{JobID: jobID, Status: "running", UserID: "owner-1"}, nil
	}
	h := NewJobHandler(ms, nil)
	if h.visibility == nil {
		t.Fatal("expected a RoleStore-capable store to wire a visibility resolver")
	}

	owner := &models.User{UserID: "owner-1"}
	req := killJobRequest("job-1", owner)
	rr := httptest.NewRecorder()
	h.KillJob(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for a job's own direct creator killing it, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestJobHandler_CancelJob_LegacyAdminCanCancelButNotKill is the
// "owner-can-cancel-but-not-kill" case from the code-review finding: a
// caller with the legacy isAdmin bypass (user.Roles contains "admin") but
// NO RBAC org-admin/global-admin grant on the job's owning org can still
// cancel (CancelJob keeps the unchanged owner-or-legacy-admin
// canUserAccessJob check) but cannot kill (canUserKillJob, once
// h.visibility is non-nil, is RBAC-only via RequireOrgAdmin and does not
// consult the legacy Roles field at all).
func TestJobHandler_CancelJob_LegacyAdminCanCancelButNotKill(t *testing.T) {
	ms := newRoleAwareMockStore()
	ms.GetJobByIDFunc = func(ctx context.Context, jobID string) (*models.Job, error) {
		return &models.Job{JobID: jobID, Status: "running", UserID: "org-owner"}, nil
	}
	h := NewJobHandler(ms, nil)
	if h.visibility == nil {
		t.Fatal("expected a RoleStore-capable store to wire a visibility resolver")
	}

	legacyAdmin := &models.User{UserID: "legacy-admin", Roles: []string{"admin"}}

	cancelReq := jobRequestWithID(http.MethodPut, "/api/v1/jobs/job-1/cancel", "job-1", legacyAdmin)
	cancelRR := httptest.NewRecorder()
	h.CancelJob(cancelRR, cancelReq)
	if cancelRR.Code != http.StatusOK {
		t.Fatalf("expected 200: legacy admin bypass still authorizes cancel, got %d: %s", cancelRR.Code, cancelRR.Body.String())
	}

	killReq := killJobRequest("job-1", legacyAdmin)
	killRR := httptest.NewRecorder()
	h.KillJob(killRR, killReq)
	if killRR.Code != http.StatusForbidden {
		t.Fatalf("expected 403: legacy admin bypass must NOT authorize kill without an RBAC org-admin/global-admin grant, got %d: %s", killRR.Code, killRR.Body.String())
	}
}

// TestJobHandler_KillJob_OrgAdminCanKill: a caller with an explicit
// role_assignments org/admin grant scoped to the job's owning org (and no
// direct ownership) can kill.
func TestJobHandler_KillJob_OrgAdminCanKill(t *testing.T) {
	ms := newRoleAwareMockStore()
	ms.GetJobByIDFunc = func(ctx context.Context, jobID string) (*models.Job, error) {
		return &models.Job{JobID: jobID, Status: "running", UserID: "org-owner"}, nil
	}
	ms.assignments = []models.RoleAssignment{
		{PrincipalType: models.PrincipalTypeUser, PrincipalID: "org-admin", ScopeType: models.ScopeTypeOrg, ScopeID: strPtrH("org-owner"), Role: models.RoleAdmin},
	}
	h := NewJobHandler(ms, nil)

	orgAdmin := &models.User{UserID: "org-admin"}
	req := killJobRequest("job-1", orgAdmin)
	rr := httptest.NewRecorder()
	h.KillJob(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for an org admin of the job's owning org killing it, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestJobHandler_KillJob_GlobalAdminCanKill: a caller with a global/admin
// role_assignment can kill any job regardless of its owning org.
func TestJobHandler_KillJob_GlobalAdminCanKill(t *testing.T) {
	ms := newRoleAwareMockStore()
	ms.GetJobByIDFunc = func(ctx context.Context, jobID string) (*models.Job, error) {
		return &models.Job{JobID: jobID, Status: "running", UserID: "org-owner"}, nil
	}
	ms.assignments = []models.RoleAssignment{
		{PrincipalType: models.PrincipalTypeUser, PrincipalID: "global-admin", ScopeType: models.ScopeTypeGlobal, Role: models.RoleAdmin},
	}
	h := NewJobHandler(ms, nil)

	globalAdmin := &models.User{UserID: "global-admin"}
	req := killJobRequest("job-1", globalAdmin)
	rr := httptest.NewRecorder()
	h.KillJob(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for a global admin killing any job, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestJobHandler_KillJob_StrangerDenied: a caller unrelated to the job's
// owning org (no direct ownership, no RBAC grant, no legacy admin role)
// cannot kill.
func TestJobHandler_KillJob_StrangerDenied(t *testing.T) {
	ms := newRoleAwareMockStore()
	ms.GetJobByIDFunc = func(ctx context.Context, jobID string) (*models.Job, error) {
		return &models.Job{JobID: jobID, Status: "running", UserID: "org-owner"}, nil
	}
	h := NewJobHandler(ms, nil)

	stranger := &models.User{UserID: "stranger"}
	req := killJobRequest("job-1", stranger)
	rr := httptest.NewRecorder()
	h.KillJob(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for an unrelated stranger, got %d: %s", rr.Code, rr.Body.String())
	}
}
