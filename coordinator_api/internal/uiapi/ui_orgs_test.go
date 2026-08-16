package uiapi

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

func TestDeleteOrgAllowsAdministratorOfBothOrganizations(t *testing.T) {
	deps, st := newTestDeps(t)
	admin := st.putUser(models.User{UserID: "admin-1"})
	requireOK(t, st.CreateOrganization(context.Background(), &models.Organization{OrgID: "org-default", Name: "default"}))
	requireOK(t, st.CreateOrganization(context.Background(), &models.Organization{OrgID: "org-linked", Name: "linked"}))
	seedOrgAdmin(st, admin.UserID, "org-default")
	seedOrgAdmin(st, admin.UserID, "org-linked")

	response, err := NewUiService(deps).DeleteOrg(
		mintSessionCtx(t, deps, admin.UserID),
		csilapi.DeleteOrgRequest{Name: "default", Replacement: "linked", Confirm: true},
	)
	requireOK(t, err)
	if response.Replacement.Name != "linked" || !response.Replacement.IsDefault {
		t.Fatalf("replacement = %#v, want linked default organization", response.Replacement)
	}
	if _, err := st.GetOrganizationByName(context.Background(), "default"); err != store.ErrNotFound {
		t.Fatalf("deleted organization lookup error = %v, want store.ErrNotFound", err)
	}
}

func TestDeleteOrgRequiresAdministratorOfReplacement(t *testing.T) {
	deps, st := newTestDeps(t)
	admin := st.putUser(models.User{UserID: "admin-1"})
	requireOK(t, st.CreateOrganization(context.Background(), &models.Organization{OrgID: "org-default", Name: "default"}))
	requireOK(t, st.CreateOrganization(context.Background(), &models.Organization{OrgID: "org-linked", Name: "linked"}))
	seedOrgAdmin(st, admin.UserID, "org-default")

	_, err := NewUiService(deps).DeleteOrg(
		mintSessionCtx(t, deps, admin.UserID),
		csilapi.DeleteOrgRequest{Name: "default", Replacement: "linked", Confirm: true},
	)
	requireCode(t, err, "forbidden")
}

func TestDeleteOrgRequiresConfirmation(t *testing.T) {
	deps, st := newTestDeps(t)
	admin := st.putUser(models.User{UserID: "admin-1"})

	_, err := NewUiService(deps).DeleteOrg(
		mintSessionCtx(t, deps, admin.UserID),
		csilapi.DeleteOrgRequest{Name: "default", Replacement: "linked"},
	)
	requireCode(t, err, "invalid_argument")
}
