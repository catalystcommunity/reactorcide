package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcsteams"
)

// groupStore holds the group operations the REST group routes use. The UI
// uses the same store operations through CSIL.
type groupStore interface {
	GetOrganizationByName(context.Context, string) (*models.Organization, error)
	GetUserByUsername(context.Context, string) (*models.User, error)
	CreateGroup(context.Context, *models.Group) error
	ListGroupsByOrg(context.Context, string) ([]models.Group, error)
	AddGroupMember(ctx context.Context, groupID, userID string) error
	RemoveGroupMember(ctx context.Context, groupID, userID string) error
	ListGroupMembers(ctx context.Context, groupID string) ([]models.User, error)
}

// GroupHandler serves organization groups to API tokens, for the CLI. Groups
// become the approver subject reactorcide_group:<name>. Every route needs
// organizations:manage for the group's organization (org admin for a user
// token), the same authority the UI group operations require.
type GroupHandler struct {
	BaseHandler
	store    groupStore
	appStore store.Store
	// teamSync syncs linked VCS teams for the sync route (group_vcs_handler.go).
	teamSync *vcsteams.Syncer
}

func NewGroupHandler(appStore store.Store) *GroupHandler {
	value, _ := appStore.(groupStore)
	return &GroupHandler{store: value, appStore: appStore}
}

type groupSummaryResponse struct {
	GroupID     string `json:"group_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// PriorityShift moves the priority of the work of the members
	// (docs/priority.md).
	PriorityShift int `json:"priority_shift"`
}

type groupPriorityShiftRequest struct {
	Shift *int `json:"shift"`
}

type groupMemberResponse struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	// The fields below come from the principal of the member. A member that
	// a linked VCS team gives can be a principal with no user.
	PrincipalID string   `json:"principal_id,omitempty"`
	Kind        string   `json:"kind,omitempty"`
	Name        string   `json:"name,omitempty"`
	Source      string   `json:"source,omitempty"`
	Manual      bool     `json:"manual,omitempty"`
	Teams       []string `json:"teams,omitempty"`
	VCSLogins   []string `json:"vcs_logins,omitempty"`
}

type groupCreateRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type groupMemberRequest struct {
	Username string `json:"username"`
}

// ServeGroups routes /api/v1/organizations/{org}/groups[/{group}/members[/{username}]].
// rest is the path after "{org}/groups".
func (h *GroupHandler) ServeGroups(w http.ResponseWriter, r *http.Request, orgName, rest string) {
	principal := checkauth.GetPrincipalFromContext(r.Context())
	// A read (GET) accepts organizations:read; a change needs
	// organizations:manage. Both need the organization admin role.
	if h.store == nil || principal == nil || !principalHasAny(principal, readOrManage(r)) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	org, err := h.store.GetOrganizationByName(r.Context(), orgName)
	if err != nil || !principalAllowsOrgAdmin(r.Context(), h.appStore, org.OrgID, readOrManage(r)) {
		h.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	parts := []string{}
	for _, part := range strings.Split(strings.Trim(rest, "/"), "/") {
		if part != "" {
			parts = append(parts, part)
		}
	}
	if h.serveTeams(w, r, org, parts) {
		return
	}
	switch {
	case len(parts) == 0 && r.Method == http.MethodGet:
		h.listGroups(w, r, org)
	case len(parts) == 0 && r.Method == http.MethodPost:
		h.createGroup(w, r, org)
	case len(parts) == 2 && parts[1] == "members" && r.Method == http.MethodGet:
		h.withGroup(w, r, org, parts[0], h.listMembers)
	case len(parts) == 2 && parts[1] == "members" && r.Method == http.MethodPost:
		h.withGroup(w, r, org, parts[0], h.addMember)
	case len(parts) == 2 && parts[1] == "priority-shift" && r.Method == http.MethodPut:
		h.withGroup(w, r, org, parts[0], h.setPriorityShift)
	case len(parts) == 3 && parts[1] == "members" && r.Method == http.MethodDelete:
		username := parts[2]
		h.withGroup(w, r, org, parts[0], func(w http.ResponseWriter, r *http.Request, org *models.Organization, group *models.Group) {
			h.removeMember(w, r, org, group, username)
		})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *GroupHandler) listGroups(w http.ResponseWriter, r *http.Request, org *models.Organization) {
	groups, err := h.store.ListGroupsByOrg(r.Context(), org.OrgID)
	if err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]groupSummaryResponse, 0, len(groups))
	for _, group := range groups {
		out = append(out, groupSummaryResponse{GroupID: group.GroupID, Name: group.Name, Description: group.Description, PriorityShift: group.PriorityShift})
	}
	h.respondWithJSON(w, http.StatusOK, map[string]any{"groups": out})
}

func (h *GroupHandler) createGroup(w http.ResponseWriter, r *http.Request, org *models.Organization) {
	var req groupCreateRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	// The name is part of an approver subject and is matched exactly, so it
	// uses the same safe form as other security IDs.
	req.Name = strings.TrimSpace(req.Name)
	if !validGroupName(req.Name) {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	if existing, _ := h.findGroup(r.Context(), org, req.Name); existing != nil {
		h.respondWithError(w, http.StatusConflict, store.ErrAlreadyExists)
		return
	}
	group := &models.Group{OrgID: org.OrgID, Name: req.Name, Description: req.Description}
	if err := h.store.CreateGroup(r.Context(), group); err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	audit.Record(r.Context(), h.appStore, org.OrgID, "group.create", "group", group.GroupID, models.JSONB{"name": group.Name})
	h.respondWithJSON(w, http.StatusCreated, groupSummaryResponse{GroupID: group.GroupID, Name: group.Name, Description: group.Description})
}

// setPriorityShift sets the priority shift of a group (docs/priority.md):
// PUT .../groups/{group}/priority-shift {"shift": N}, -50..50, 0 for none.
func (h *GroupHandler) setPriorityShift(w http.ResponseWriter, r *http.Request, org *models.Organization, group *models.Group) {
	var req groupPriorityShiftRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.Shift == nil {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	shifts, ok := h.appStore.(store.PriorityStore)
	if !ok {
		h.respondWithError(w, http.StatusNotImplemented, store.ErrNotFound)
		return
	}
	if err := shifts.SetGroupPriorityShift(r.Context(), group.GroupID, *req.Shift); err != nil {
		if errors.Is(err, store.ErrInvalidInput) {
			h.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid_input", Message: err.Error()})
			return
		}
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	audit.Record(r.Context(), h.appStore, org.OrgID, "group.priority_shift", "group", group.GroupID,
		models.JSONB{"name": group.Name, "from": group.PriorityShift, "to": *req.Shift})
	h.respondWithJSON(w, http.StatusOK, groupSummaryResponse{GroupID: group.GroupID, Name: group.Name, Description: group.Description, PriorityShift: *req.Shift})
}

func (h *GroupHandler) listMembers(w http.ResponseWriter, r *http.Request, _ *models.Organization, group *models.Group) {
	if st := h.vcsStore(); st != nil {
		details, err := st.ListGroupMemberDetails(r.Context(), group.GroupID)
		if err != nil {
			h.respondWithError(w, http.StatusInternalServerError, err)
			return
		}
		out := make([]groupMemberResponse, 0, len(details))
		for _, d := range details {
			out = append(out, groupMemberResponse{UserID: d.UserID, Username: d.Username, PrincipalID: d.PrincipalID,
				Kind: d.Kind, Name: d.Name, Source: d.Source, Manual: d.Manual, Teams: d.Teams, VCSLogins: d.VCSLogins})
		}
		h.respondWithJSON(w, http.StatusOK, map[string]any{"members": out})
		return
	}
	users, err := h.store.ListGroupMembers(r.Context(), group.GroupID)
	if err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]groupMemberResponse, 0, len(users))
	for _, user := range users {
		out = append(out, groupMemberResponse{UserID: user.UserID, Username: user.Username})
	}
	h.respondWithJSON(w, http.StatusOK, map[string]any{"members": out})
}

func (h *GroupHandler) addMember(w http.ResponseWriter, r *http.Request, org *models.Organization, group *models.Group) {
	var req groupMemberRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil || strings.TrimSpace(req.Username) == "" {
		h.respondWithError(w, http.StatusBadRequest, store.ErrInvalidInput)
		return
	}
	user, err := h.store.GetUserByUsername(r.Context(), strings.TrimSpace(req.Username))
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}
	if err := h.store.AddGroupMember(r.Context(), group.GroupID, user.UserID); err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	audit.Record(r.Context(), h.appStore, org.OrgID, "group.member_add", "group", group.GroupID, models.JSONB{"user_id": user.UserID})
	h.respondWithJSON(w, http.StatusCreated, groupMemberResponse{UserID: user.UserID, Username: user.Username})
}

func (h *GroupHandler) removeMember(w http.ResponseWriter, r *http.Request, org *models.Organization, group *models.Group, username string) {
	user, err := h.store.GetUserByUsername(r.Context(), username)
	if err != nil {
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}
	if err := h.store.RemoveGroupMember(r.Context(), group.GroupID, user.UserID); err != nil {
		if errors.Is(err, store.ErrConflict) {
			// Only a linked VCS team gives the membership.
			h.respondWithJSON(w, http.StatusConflict, map[string]string{"error": "the member comes from a linked VCS team; remove the person from the team, or unlink the team"})
			return
		}
		h.respondWithError(w, http.StatusNotFound, err)
		return
	}
	audit.Record(r.Context(), h.appStore, org.OrgID, "group.member_remove", "group", group.GroupID, models.JSONB{"user_id": user.UserID})
	w.WriteHeader(http.StatusNoContent)
}

func (h *GroupHandler) withGroup(w http.ResponseWriter, r *http.Request, org *models.Organization, name string,
	next func(http.ResponseWriter, *http.Request, *models.Organization, *models.Group)) {
	group, err := h.findGroup(r.Context(), org, name)
	if err != nil {
		h.respondWithError(w, http.StatusInternalServerError, err)
		return
	}
	if group == nil {
		h.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
		return
	}
	next(w, r, org, group)
}

func (h *GroupHandler) findGroup(ctx context.Context, org *models.Organization, name string) (*models.Group, error) {
	groups, err := h.store.ListGroupsByOrg(ctx, org.OrgID)
	if err != nil {
		return nil, err
	}
	for i := range groups {
		if groups[i].Name == name {
			return &groups[i], nil
		}
	}
	return nil, nil
}

func validGroupName(name string) bool {
	if name == "" || len(name) > 63 {
		return false
	}
	for i, c := range name {
		alnum := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
		if !alnum && (i == 0 || c != '-') {
			return false
		}
	}
	return true
}
