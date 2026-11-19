package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

// TestListJobs_UserTokenNeverExceedsTheToken: a user-delegated token lists
// jobs only with jobs:read, and only in the organizations of the token.
func TestListJobs_UserTokenNeverExceedsTheToken(t *testing.T) {
	ms := newRoleAwareMockStore()
	caller := &models.User{UserID: "caller", Roles: []string{"user"}}
	ms.allJobs = []models.Job{
		{JobID: "in-a", UserID: "caller", OrgID: "org-a", Status: "completed"},
		{JobID: "in-b", UserID: "caller", OrgID: "org-b", Status: "completed"},
	}
	h := NewJobHandler(ms, nil)
	list := func(capabilities ...string) (int, map[string]bool) {
		set, _ := tokencaps.New(capabilities...)
		principal := &checkauth.Principal{CredentialType: "user_token", UserID: "caller", OrganizationIDs: []string{"org-a"}, Capabilities: set}
		req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
		ctx := checkauth.SetUserContext(req.Context(), caller)
		ctx = checkauth.SetPrincipalContext(ctx, principal)
		rr := httptest.NewRecorder()
		h.ListJobs(rr, req.WithContext(ctx))
		ids := map[string]bool{}
		if rr.Code == http.StatusOK {
			var resp ListJobsResponse
			_ = json.Unmarshal(rr.Body.Bytes(), &resp)
			for _, job := range resp.Jobs {
				ids[job.JobID] = true
			}
		}
		return rr.Code, ids
	}
	if code, _ := list(tokencaps.WorkersRead); code != http.StatusForbidden {
		t.Fatalf("token without jobs:read: status %d, want 403", code)
	}
	code, ids := list(tokencaps.JobsRead)
	if code != http.StatusOK || !ids["in-a"] || ids["in-b"] {
		t.Fatalf("jobs:read token for org-a: status %d, jobs %v, want only in-a", code, ids)
	}
}
