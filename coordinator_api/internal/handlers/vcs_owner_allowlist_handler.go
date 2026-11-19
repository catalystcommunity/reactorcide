package handlers

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

// vcsOwnerAllowlistRoutes serves the VCS owner allowlist of an organization
// (docs/principals.md "Groups linked to VCS teams"):
//
//	GET|PUT /api/v1/organizations/NAME/vcs-owner-allowlist
//
// The group VCS sync of the organization may use the GLOBAL VCS credential
// only for the owners on the list. Only a global admin (or the admin API
// token) reads or changes it: the list lets one tenant read the teams of a
// VCS owner with the shared credential.
type vcsOwnerAllowlistRoutes struct {
	BaseHandler
	store store.Store
}

func newVCSOwnerAllowlistRoutes(appStore store.Store) *vcsOwnerAllowlistRoutes {
	return &vcsOwnerAllowlistRoutes{store: appStore}
}

type vcsOwnerAllowlistWriter interface {
	vcsOwnerAllowlistStore
	SetOrganizationVCSOwnerAllowlist(ctx context.Context, orgID string, owners []string) ([]string, error)
}

type vcsOwnerAllowlistBody struct {
	Owners []string `json:"owners"`
}

var vcsOwnerPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,38}$`)

// isGlobalAdministrator: a global admin principal token or the admin API
// token with capability, or a session of a user with the admin role.
func isGlobalAdministrator(ctx context.Context, appStore store.Store, capability string) bool {
	if principal := checkauth.GetPrincipalFromContext(ctx); principal != nil {
		return principalAllowsGlobal(ctx, appStore, capability)
	}
	user := checkauth.GetUserFromContext(ctx)
	if user == nil || !user.IsActive() {
		return false
	}
	for _, role := range user.Roles {
		if role == "admin" {
			return true
		}
	}
	return false
}

func (v *vcsOwnerAllowlistRoutes) serveOrg(w http.ResponseWriter, r *http.Request, path string,
	transactionMiddleware, authMiddleware func(http.Handler) http.Handler) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 || parts[1] != "vcs-owner-allowlist" {
		return false
	}
	transactionMiddleware(authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v.serve(w, r, parts[0])
	}))).ServeHTTP(w, r)
	return true
}

func (v *vcsOwnerAllowlistRoutes) serve(w http.ResponseWriter, r *http.Request, name string) {
	lists, ok := v.store.(vcsOwnerAllowlistWriter)
	orgs, okOrgs := v.store.(organizationStore)
	if !ok || !okOrgs {
		v.respondWithError(w, http.StatusNotImplemented, errors.New("the VCS owner allowlist is not supported by this store"))
		return
	}
	capability := tokencaps.OrganizationsRead
	if r.Method != http.MethodGet {
		capability = tokencaps.OrganizationsManage
	}
	if !isGlobalAdministrator(r.Context(), v.store, capability) {
		v.respondWithJSON(w, http.StatusForbidden, ErrorResponse{Error: "forbidden",
			Message: "only a global admin can read or change the VCS owner allowlist"})
		return
	}
	org, err := orgs.GetOrganizationByName(r.Context(), name)
	if err != nil || org == nil {
		v.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		owners, err := lists.GetOrganizationVCSOwnerAllowlist(r.Context(), org.OrgID)
		if err != nil {
			v.respondWithError(w, http.StatusInternalServerError, err)
			return
		}
		v.respondWithJSON(w, http.StatusOK, vcsOwnerAllowlistBody{Owners: owners})
	case http.MethodPut:
		var body vcsOwnerAllowlistBody
		if err := decodeStrict(r, &body); err != nil {
			v.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid_input", Message: `the body must be {"owners": ["org-login", ...]}`})
			return
		}
		for i, owner := range body.Owners {
			body.Owners[i] = strings.ToLower(strings.TrimSpace(owner))
			if !vcsOwnerPattern.MatchString(body.Owners[i]) {
				v.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid_input",
					Message: "an owner is a VCS organization login: lowercase letters, digits, and '-'"})
				return
			}
		}
		before, _ := lists.GetOrganizationVCSOwnerAllowlist(r.Context(), org.OrgID)
		owners, err := lists.SetOrganizationVCSOwnerAllowlist(r.Context(), org.OrgID, body.Owners)
		if err != nil {
			v.respondWithError(w, http.StatusInternalServerError, err)
			return
		}
		audit.Record(r.Context(), v.store, org.OrgID, "organization.vcs_owner_allowlist", "organization", org.OrgID,
			models.JSONB{"from": before, "to": owners})
		v.respondWithJSON(w, http.StatusOK, vcsOwnerAllowlistBody{Owners: owners})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
