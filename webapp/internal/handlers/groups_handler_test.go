package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/webapp/internal/uiclient/csilapi"
)

func TestOrgGroupsPage_ForbiddenWithoutManageGroupsCap(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	withCapabilities(fc, csilapi.GetCapabilitiesResponse{ManageGroups: false})
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodGet, "/app/org/groups?org_id=org-1", nil)
	rec := httptest.NewRecorder()
	h.withSession(h.OrgGroupsPage)(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `action="/app/org/groups"`) {
		t.Errorf("create-group form must not render for an incapable session, got: %s", rec.Body.String())
	}
}

func TestOrgGroupsPage_RendersForCapableSession(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	withCapabilities(fc, csilapi.GetCapabilitiesResponse{ManageGroups: true})
	fc.handle("ReactorcideUi", "list-groups", func(payload []byte, _ string, _ bool) ([]byte, string, bool) {
		req, err := csilapi.DecodeListGroupsRequest(payload)
		if err != nil || req.OrgId != "org-1" {
			return fakeServiceErrorPayload("bad_request", "unexpected org_id"), "ServiceError", true
		}
		resp := csilapi.ListGroupsResponse{Groups: []csilapi.GroupSummary{{GroupId: "g1", OrgId: "org-1", Name: "on-call"}}}
		return csilapi.EncodeListGroupsResponse(resp), "ListGroupsResponse", false
	})
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodGet, "/app/org/groups?org_id=org-1", nil)
	rec := httptest.NewRecorder()
	h.withSession(h.OrgGroupsPage)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "on-call") {
		t.Errorf("expected group name in list, got: %s", body)
	}
	if !strings.Contains(body, `action="/app/org/groups"`) {
		t.Errorf("expected create-group form to render, got: %s", body)
	}
}

func TestOrgGroupsPage_RendersMembersWithRemoveButton(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	withCapabilities(fc, csilapi.GetCapabilitiesResponse{ManageGroups: true})
	fc.handle("ReactorcideUi", "list-groups", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		resp := csilapi.ListGroupsResponse{Groups: []csilapi.GroupSummary{{GroupId: "g1", OrgId: "org-1", Name: "on-call"}}}
		return csilapi.EncodeListGroupsResponse(resp), "ListGroupsResponse", false
	})
	fc.handle("ReactorcideUi", "list-group-members", func(payload []byte, _ string, _ bool) ([]byte, string, bool) {
		req, err := csilapi.DecodeListGroupMembersRequest(payload)
		if err != nil || req.GroupId != "g1" {
			return fakeServiceErrorPayload("bad_request", "unexpected group_id"), "ServiceError", true
		}
		resp := csilapi.ListGroupMembersResponse{Members: []csilapi.GroupMemberEntry{{UserId: "u1", Username: "alice"}}}
		return csilapi.EncodeListGroupMembersResponse(resp), "ListGroupMembersResponse", false
	})
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodGet, "/app/org/groups?org_id=org-1", nil)
	rec := httptest.NewRecorder()
	h.withSession(h.OrgGroupsPage)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "alice") {
		t.Errorf("expected member username in the rendered page, got: %s", body)
	}
	if !strings.Contains(body, `action="/app/org/groups/g1/members/remove"`) {
		t.Errorf("expected a per-member remove form, got: %s", body)
	}
}

func TestGroupMemberRemove_HappyPathHitsFakeWithFields(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	var seen csilapi.RemoveGroupMemberRequest
	fc.handle("ReactorcideUi", "remove-group-member", func(payload []byte, _ string, _ bool) ([]byte, string, bool) {
		req, err := csilapi.DecodeRemoveGroupMemberRequest(payload)
		if err != nil {
			return fakeServiceErrorPayload("bad_request", err.Error()), "ServiceError", true
		}
		seen = req
		return csilapi.EncodeRemoveGroupMemberResponse(csilapi.RemoveGroupMemberResponse{Removed: true}), "RemoveGroupMemberResponse", false
	})
	h := newTestWebHandler(t, fc)

	form := strings.NewReader("org_id=org-1&user_id=u1")
	req := httptest.NewRequest(http.MethodPost, "/app/org/groups/g1/members/remove", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "g1")
	rec := httptest.NewRecorder()
	h.withSession(h.GroupMemberRemove)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if seen.GroupId != "g1" || seen.UserId != "u1" {
		t.Errorf("fake coordinator did not receive expected fields: %+v", seen)
	}
}

func TestGroupCreate_HappyPathHitsFakeWithFields(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	var seen csilapi.CreateGroupRequest
	fc.handle("ReactorcideUi", "create-group", func(payload []byte, _ string, _ bool) ([]byte, string, bool) {
		req, err := csilapi.DecodeCreateGroupRequest(payload)
		if err != nil {
			return fakeServiceErrorPayload("bad_request", err.Error()), "ServiceError", true
		}
		seen = req
		resp := csilapi.CreateGroupResponse{Group: csilapi.GroupSummary{GroupId: "g2", OrgId: req.OrgId, Name: req.Name}}
		return csilapi.EncodeCreateGroupResponse(resp), "CreateGroupResponse", false
	})
	h := newTestWebHandler(t, fc)

	form := strings.NewReader("org_id=org-1&name=on-call&description=oncall+rotation")
	req := httptest.NewRequest(http.MethodPost, "/app/org/groups", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.withSession(h.GroupCreate)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if seen.OrgId != "org-1" || seen.Name != "on-call" {
		t.Errorf("fake coordinator did not receive expected fields: %+v", seen)
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/app/org/groups?org_id=org-1") {
		t.Errorf("Location = %q, want a redirect back to the org's groups page", loc)
	}
}

func TestGroupCreate_ConflictFlashesMessage(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	fc.handle("ReactorcideUi", "create-group", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		return fakeServiceErrorPayload("conflict", "a group with this name already exists"), "ServiceError", true
	})
	h := newTestWebHandler(t, fc)

	form := strings.NewReader("org_id=org-1&name=on-call")
	req := httptest.NewRequest(http.MethodPost, "/app/org/groups", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.withSession(h.GroupCreate)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "err=") {
		t.Errorf("Location = %q, want an err flash", loc)
	}
}

func TestOrgRolesPage_ForbiddenWithoutManageGroupsCap(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	withCapabilities(fc, csilapi.GetCapabilitiesResponse{ManageGroups: false})
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodGet, "/app/org/roles?org_id=org-1", nil)
	rec := httptest.NewRecorder()
	h.withSession(h.OrgRolesPage)(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

func TestOrgRolesPage_DefaultsToOrgScopeAndRenders(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	withCapabilities(fc, csilapi.GetCapabilitiesResponse{ManageGroups: true})
	var sawScopeType, sawScopeID string
	fc.handle("ReactorcideUi", "list-role-assignments", func(payload []byte, _ string, _ bool) ([]byte, string, bool) {
		req, err := csilapi.DecodeListRoleAssignmentsRequest(payload)
		if err != nil {
			return fakeServiceErrorPayload("bad_request", err.Error()), "ServiceError", true
		}
		if req.ScopeType != nil {
			sawScopeType = *req.ScopeType
		}
		if req.ScopeId != nil {
			sawScopeID = *req.ScopeId
		}
		resp := csilapi.ListRoleAssignmentsResponse{Assignments: []csilapi.RoleAssignment{
			{AssignmentId: "a1", PrincipalType: "user", PrincipalId: "u1", ScopeType: "org", Role: "admin"},
		}}
		return csilapi.EncodeListRoleAssignmentsResponse(resp), "ListRoleAssignmentsResponse", false
	})
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodGet, "/app/org/roles?org_id=org-1", nil)
	rec := httptest.NewRecorder()
	h.withSession(h.OrgRolesPage)(rec, req)

	if sawScopeType != "org" || sawScopeID != "org-1" {
		t.Errorf("expected default scope_type=org scope_id=org-1, got scope_type=%q scope_id=%q", sawScopeType, sawScopeID)
	}
	if !strings.Contains(rec.Body.String(), "u1") {
		t.Errorf("expected principal id in the assignments table, got: %s", rec.Body.String())
	}
}

func TestRoleAssign_HappyPathHitsFakeWithFields(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	var seen csilapi.AssignRoleRequest
	fc.handle("ReactorcideUi", "assign-role", func(payload []byte, _ string, _ bool) ([]byte, string, bool) {
		req, err := csilapi.DecodeAssignRoleRequest(payload)
		if err != nil {
			return fakeServiceErrorPayload("bad_request", err.Error()), "ServiceError", true
		}
		seen = req
		resp := csilapi.AssignRoleResponse{Assignment: csilapi.RoleAssignment{AssignmentId: "a1", PrincipalType: req.PrincipalType, PrincipalId: req.PrincipalId, ScopeType: req.ScopeType, ScopeId: req.ScopeId, Role: req.Role}}
		return csilapi.EncodeAssignRoleResponse(resp), "AssignRoleResponse", false
	})
	h := newTestWebHandler(t, fc)

	form := strings.NewReader("org_id=org-1&principal_type=user&principal_id=u1&scope_type=org&scope_id=org-1&role=member")
	req := httptest.NewRequest(http.MethodPost, "/app/org/roles", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.withSession(h.RoleAssign)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if seen.PrincipalType != "user" || seen.PrincipalId != "u1" || seen.ScopeType != "org" || seen.Role != "member" {
		t.Errorf("fake coordinator did not receive expected fields: %+v", seen)
	}
	if seen.ScopeId == nil || *seen.ScopeId != "org-1" {
		t.Errorf("expected scope_id=org-1, got %v", seen.ScopeId)
	}
}
