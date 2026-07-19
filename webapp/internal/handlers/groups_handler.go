package handlers

import (
	"net/http"

	"github.com/catalystcommunity/reactorcide/webapp/internal/uiclient/csilapi"
)

// orgPageBackTo builds the redirect-back URL for an org-scoped management
// page, preserving the org selection.
func orgPageBackTo(base, orgID string) string {
	if orgID == "" {
		return base
	}
	return base + "?org_id=" + orgID
}

// OrgGroupsPage renders GET /app/org/groups: gated on the ManageGroups
// capability (there is no separate "view groups" tier in the permission
// matrix, so an incapable caller sees a 403 rather than an empty list).
// Global admins get an org selector (list-orgs); other callers only ever see
// their own org. Each listed group's current member list is fetched with a
// separate list-group-members call (fine for the group counts this page
// deals with) so the page can render a real member roster with per-member
// remove buttons instead of blind remove-by-user-id.
func (h *WebHandler) OrgGroupsPage(w http.ResponseWriter, r *http.Request) {
	si := h.sessionInfo(r)
	orgID := h.resolveOrgID(r, si)
	var orgs []csilapi.OrgSummary
	if si.IsGlobalAdmin {
		orgs = h.listOrgsForSelector(r)
		if orgID == "" && len(orgs) > 0 {
			orgID = orgs[0].OrgId
		}
	}

	caps := h.capabilitiesForOrg(r, orgID)
	if !caps.ManageGroups {
		h.renderError(w, r, http.StatusForbidden, "You do not have permission to manage groups for this org", nil)
		return
	}

	msg, errMsg := flashFromQuery(r)
	data := map[string]interface{}{
		"Title":     "Groups",
		"OrgID":     orgID,
		"Orgs":      orgs,
		"IsAdmin":   si.IsGlobalAdmin,
		"FormMsg":   msg,
		"FormError": errMsg,
	}

	if orgID != "" && h.uiClients != nil {
		resp, err := h.uiClients.Ui.ListGroups(h.authContext(r), csilapi.ListGroupsRequest{OrgId: orgID})
		if err != nil {
			h.renderServiceError(w, r, err)
			return
		}
		data["Groups"] = resp.Groups

		members := make(map[string][]csilapi.GroupMemberEntry, len(resp.Groups))
		for _, g := range resp.Groups {
			if mResp, err := h.uiClients.Ui.ListGroupMembers(h.authContext(r), csilapi.ListGroupMembersRequest{GroupId: g.GroupId}); err == nil {
				members[g.GroupId] = mResp.Members
			}
		}
		data["GroupMembers"] = members
	}

	h.render(w, r, "org_groups.html", data)
}

// GroupCreate handles POST /app/org/groups.
func (h *WebHandler) GroupCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form submission", nil)
		return
	}
	orgID := formTrim(r, "org_id")
	name := formTrim(r, "name")
	backTo := orgPageBackTo("/app/org/groups", orgID)
	if orgID == "" || name == "" {
		h.redirectFlash(w, r, backTo, "org and name are required", true)
		return
	}
	if h.uiClients == nil {
		h.redirectFlash(w, r, backTo, "management is not available", true)
		return
	}

	req := csilapi.CreateGroupRequest{OrgId: orgID, Name: name, Description: formOptionalPtr(r, "description")}
	if _, err := h.uiClients.Ui.CreateGroup(h.authContext(r), req); err != nil {
		h.handleFormServiceError(w, r, err, backTo)
		return
	}
	h.redirectFlash(w, r, backTo, "Group created", false)
}

// GroupDelete handles POST /app/org/groups/{id}/delete.
func (h *WebHandler) GroupDelete(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("id")
	if groupID == "" {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form submission", nil)
		return
	}
	backTo := orgPageBackTo("/app/org/groups", formTrim(r, "org_id"))
	if h.uiClients == nil {
		h.redirectFlash(w, r, backTo, "management is not available", true)
		return
	}
	if _, err := h.uiClients.Ui.DeleteGroup(h.authContext(r), csilapi.DeleteGroupRequest{GroupId: groupID}); err != nil {
		h.handleFormServiceError(w, r, err, backTo)
		return
	}
	h.redirectFlash(w, r, backTo, "Group deleted", false)
}

// GroupMemberAdd handles POST /app/org/groups/{id}/members.
func (h *WebHandler) GroupMemberAdd(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("id")
	if groupID == "" {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form submission", nil)
		return
	}
	backTo := orgPageBackTo("/app/org/groups", formTrim(r, "org_id"))
	userID := formTrim(r, "user_id")
	if userID == "" {
		h.redirectFlash(w, r, backTo, "user_id is required", true)
		return
	}
	if h.uiClients == nil {
		h.redirectFlash(w, r, backTo, "management is not available", true)
		return
	}
	if _, err := h.uiClients.Ui.AddGroupMember(h.authContext(r), csilapi.AddGroupMemberRequest{GroupId: groupID, UserId: userID}); err != nil {
		h.handleFormServiceError(w, r, err, backTo)
		return
	}
	h.redirectFlash(w, r, backTo, "Member added", false)
}

// GroupMemberRemove handles POST /app/org/groups/{id}/members/remove.
func (h *WebHandler) GroupMemberRemove(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("id")
	if groupID == "" {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form submission", nil)
		return
	}
	backTo := orgPageBackTo("/app/org/groups", formTrim(r, "org_id"))
	userID := formTrim(r, "user_id")
	if userID == "" {
		h.redirectFlash(w, r, backTo, "user_id is required", true)
		return
	}
	if h.uiClients == nil {
		h.redirectFlash(w, r, backTo, "management is not available", true)
		return
	}
	if _, err := h.uiClients.Ui.RemoveGroupMember(h.authContext(r), csilapi.RemoveGroupMemberRequest{GroupId: groupID, UserId: userID}); err != nil {
		h.handleFormServiceError(w, r, err, backTo)
		return
	}
	h.redirectFlash(w, r, backTo, "Member removed", false)
}

// OrgRolesPage renders GET /app/org/roles: gated on ManageGroups (the
// permission matrix's "manage groups / assign roles" row is one capability —
// there is no separate ManageRoles field on GetCapabilitiesResponse).
// Defaults to listing scope_type=org assignments at the selected org (the
// coordinator requires scope_type on every list-role-assignments call); an
// optional scope filter form lets the caller look at a specific
// project/global scope they're authorized for instead.
func (h *WebHandler) OrgRolesPage(w http.ResponseWriter, r *http.Request) {
	si := h.sessionInfo(r)
	orgID := h.resolveOrgID(r, si)
	var orgs []csilapi.OrgSummary
	if si.IsGlobalAdmin {
		orgs = h.listOrgsForSelector(r)
		if orgID == "" && len(orgs) > 0 {
			orgID = orgs[0].OrgId
		}
	}

	caps := h.capabilitiesForOrg(r, orgID)
	if !caps.ManageGroups {
		h.renderError(w, r, http.StatusForbidden, "You do not have permission to manage roles for this org", nil)
		return
	}

	scopeType := r.URL.Query().Get("scope_type")
	if scopeType == "" {
		scopeType = "org"
	}
	scopeID := r.URL.Query().Get("scope_id")
	if scopeID == "" && scopeType == "org" {
		scopeID = orgID
	}

	msg, errMsg := flashFromQuery(r)
	data := map[string]interface{}{
		"Title":     "Roles",
		"OrgID":     orgID,
		"Orgs":      orgs,
		"IsAdmin":   si.IsGlobalAdmin,
		"ScopeType": scopeType,
		"ScopeID":   scopeID,
		"FormMsg":   msg,
		"FormError": errMsg,
	}

	if h.uiClients != nil {
		req := csilapi.ListRoleAssignmentsRequest{ScopeType: &scopeType}
		if scopeID != "" {
			req.ScopeId = &scopeID
		}
		resp, err := h.uiClients.Ui.ListRoleAssignments(h.authContext(r), req)
		if err != nil {
			h.renderServiceError(w, r, err)
			return
		}
		data["Assignments"] = resp.Assignments
	}

	h.render(w, r, "org_roles.html", data)
}

// RoleAssign handles POST /app/org/roles. scope_id is only sent when
// scope_type isn't "global" (a global assignment has no scope_id).
func (h *WebHandler) RoleAssign(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form submission", nil)
		return
	}
	orgID := formTrim(r, "org_id")
	backTo := orgPageBackTo("/app/org/roles", orgID)

	principalType := formTrim(r, "principal_type")
	principalID := formTrim(r, "principal_id")
	scopeType := formTrim(r, "scope_type")
	role := formTrim(r, "role")
	if principalType == "" || principalID == "" || scopeType == "" || role == "" {
		h.redirectFlash(w, r, backTo, "principal type, principal id, scope type, and role are required", true)
		return
	}
	if h.uiClients == nil {
		h.redirectFlash(w, r, backTo, "management is not available", true)
		return
	}

	req := csilapi.AssignRoleRequest{
		PrincipalType: principalType,
		PrincipalId:   principalID,
		ScopeType:     scopeType,
		Role:          role,
	}
	if scopeType != "global" {
		req.ScopeId = formOptionalPtr(r, "scope_id")
	}
	if _, err := h.uiClients.Ui.AssignRole(h.authContext(r), req); err != nil {
		h.handleFormServiceError(w, r, err, backTo)
		return
	}
	h.redirectFlash(w, r, backTo, "Role assigned", false)
}

// RoleRevoke handles POST /app/org/roles/{id}/revoke.
func (h *WebHandler) RoleRevoke(w http.ResponseWriter, r *http.Request) {
	assignmentID := r.PathValue("id")
	if assignmentID == "" {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form submission", nil)
		return
	}
	backTo := orgPageBackTo("/app/org/roles", formTrim(r, "org_id"))
	if h.uiClients == nil {
		h.redirectFlash(w, r, backTo, "management is not available", true)
		return
	}
	if _, err := h.uiClients.Ui.RevokeRole(h.authContext(r), csilapi.RevokeRoleRequest{AssignmentId: assignmentID}); err != nil {
		h.handleFormServiceError(w, r, err, backTo)
		return
	}
	h.redirectFlash(w, r, backTo, "Role revoked", false)
}
