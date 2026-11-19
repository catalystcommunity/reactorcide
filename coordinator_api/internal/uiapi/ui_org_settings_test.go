package uiapi

import (
	"context"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/requiredset"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// settingsFakeStore adds the organization setting stores (infra retries,
// egress control, VCS owner allowlist, OIDC settings) to the fake store.
type settingsFakeStore struct {
	*fakeStore
	infraRetries map[string]int
	egress       map[string]bool
	allowlists   map[string][]string
	oidc         map[string]models.OIDCSettings
}

func newSettingsDeps(t *testing.T) (*Deps, *settingsFakeStore) {
	t.Helper()
	deps, st := newTestDeps(t)
	wrapped := &settingsFakeStore{fakeStore: st, infraRetries: map[string]int{}, egress: map[string]bool{},
		allowlists: map[string][]string{}, oidc: map[string]models.OIDCSettings{}}
	deps.Store = wrapped
	return deps, wrapped
}

func (s *settingsFakeStore) EffectiveInfraRetries(_ context.Context, orgID string, projectID *string) (int, error) {
	var project, org *int
	if projectID != nil {
		if p, err := s.GetProjectByID(context.Background(), *projectID); err == nil {
			project = p.InfraRetries
		}
	}
	if value, ok := s.infraRetries[orgID]; ok {
		org = &value
	}
	return models.EffectiveInfraRetries(project, org), nil
}

func (s *settingsFakeStore) SetOrganizationInfraRetries(_ context.Context, orgID string, value int) error {
	s.infraRetries[orgID] = value
	return nil
}

func (s *settingsFakeStore) EffectiveEgressControl(_ context.Context, orgID string, projectID *string, _ string) (bool, error) {
	if s.egress[orgID] {
		return true, nil
	}
	if projectID != nil {
		if p, err := s.GetProjectByID(context.Background(), *projectID); err == nil {
			return p.EgressControl, nil
		}
	}
	return false, nil
}

func (s *settingsFakeStore) SetOrganizationEgressControl(_ context.Context, orgID string, on bool) error {
	s.egress[orgID] = on
	return nil
}

func (s *settingsFakeStore) GetOrganizationVCSOwnerAllowlist(_ context.Context, orgID string) ([]string, error) {
	return s.allowlists[orgID], nil
}

func (s *settingsFakeStore) SetOrganizationVCSOwnerAllowlist(_ context.Context, orgID string, owners []string) ([]string, error) {
	s.allowlists[orgID] = owners
	return owners, nil
}

func (s *settingsFakeStore) GetOIDCSettings(_ context.Context, orgID, projectID string) (*models.OIDCSettings, error) {
	value, ok := s.oidc[orgID+"/"+projectID]
	if !ok {
		return nil, nil
	}
	return &value, nil
}

func (s *settingsFakeStore) SaveOIDCSettings(_ context.Context, settings *models.OIDCSettings) error {
	projectID := ""
	if settings.ProjectID != nil {
		projectID = *settings.ProjectID
	}
	s.oidc[settings.OrgID+"/"+projectID] = *settings
	return nil
}

func seedSettingsOrg(t *testing.T, st *settingsFakeStore) models.Organization {
	t.Helper()
	org := models.Organization{Name: "acme", Status: models.OrganizationStatusActive}
	if err := st.CreateOrganization(context.Background(), &org); err != nil {
		t.Fatal(err)
	}
	return org
}

func TestUpdateOrgInfraRetriesAndEgressControl(t *testing.T) {
	deps, st := newSettingsDeps(t)
	org := seedSettingsOrg(t, st)
	st.putUser(models.User{UserID: "root"})
	st.putUser(models.User{UserID: "org-admin"})
	seedGlobalAdmin(st.fakeStore, "root")
	seedOrgAdmin(st.fakeStore, "org-admin", org.OrgID)
	ui := NewUiService(deps)

	// update-org is a global admin operation, as before.
	_, err := ui.UpdateOrg(mintSessionCtx(t, deps, "org-admin"), csilapi.UpdateOrgRequest{Name: "acme", InfraRetries: int64Ptr(3)})
	requireCode(t, err, "forbidden")

	ctx := mintSessionCtx(t, deps, "root")
	_, err = ui.UpdateOrg(ctx, csilapi.UpdateOrgRequest{Name: "acme", InfraRetries: int64Ptr(9)})
	requireCode(t, err, "invalid_argument")

	resp, err := ui.UpdateOrg(ctx, csilapi.UpdateOrgRequest{Name: "acme", InfraRetries: int64Ptr(3), EgressControl: boolPtr(true)})
	requireOK(t, err)
	if st.infraRetries[org.OrgID] != 3 || !st.egress[org.OrgID] {
		t.Fatalf("stored infra_retries=%d egress=%v", st.infraRetries[org.OrgID], st.egress[org.OrgID])
	}
	if resp.Org.InfraRetries == nil || *resp.Org.InfraRetries != 3 || resp.Org.EgressControl == nil || !*resp.Org.EgressControl {
		t.Fatalf("summary = %+v", resp.Org)
	}
}

func TestUpdateProjectInfraRetriesAndEgressControl(t *testing.T) {
	deps, st := newSettingsDeps(t)
	org := seedSettingsOrg(t, st)
	st.infraRetries[org.OrgID] = 2
	st.putUser(models.User{UserID: "owner"})
	project := st.putProject(models.Project{UserID: strPtr(org.OrgID), OrgID: org.OrgID})
	seedProjectOwner(st.fakeStore, "owner", project.ProjectID)
	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, "owner")

	resp, err := ui.UpdateProject(ctx, csilapi.UpdateProjectRequest{ProjectId: project.ProjectID, InfraRetries: int64Ptr(0), EgressControl: boolPtr(true)})
	requireOK(t, err)
	if resp.Project.InfraRetries == nil || *resp.Project.InfraRetries != 0 || resp.Project.EffectiveInfraRetries == nil || *resp.Project.EffectiveInfraRetries != 0 {
		t.Fatalf("infra retries = %v effective %v", resp.Project.InfraRetries, resp.Project.EffectiveInfraRetries)
	}
	if resp.Project.EgressControl == nil || !*resp.Project.EgressControl || resp.Project.EffectiveEgressControl == nil || !*resp.Project.EffectiveEgressControl {
		t.Fatalf("egress = %v effective %v", resp.Project.EgressControl, resp.Project.EffectiveEgressControl)
	}

	resp, err = ui.UpdateProject(ctx, csilapi.UpdateProjectRequest{ProjectId: project.ProjectID, InfraRetries: int64Ptr(4), InfraRetriesInherit: boolPtr(true)})
	requireOK(t, err)
	if resp.Project.InfraRetries != nil || resp.Project.EffectiveInfraRetries == nil || *resp.Project.EffectiveInfraRetries != 2 {
		t.Fatalf("inherit: infra retries = %v effective %v", resp.Project.InfraRetries, resp.Project.EffectiveInfraRetries)
	}
	_, err = ui.UpdateProject(ctx, csilapi.UpdateProjectRequest{ProjectId: project.ProjectID, InfraRetries: int64Ptr(6)})
	requireCode(t, err, "invalid_argument")
}

func TestVcsOwnerAllowlistNeedsGlobalAdmin(t *testing.T) {
	deps, st := newSettingsDeps(t)
	org := seedSettingsOrg(t, st)
	st.putUser(models.User{UserID: "root"})
	st.putUser(models.User{UserID: "org-admin"})
	seedGlobalAdmin(st.fakeStore, "root")
	seedOrgAdmin(st.fakeStore, "org-admin", org.OrgID)
	ui := NewUiService(deps)

	_, err := ui.GetVcsOwnerAllowlist(mintSessionCtx(t, deps, "org-admin"), csilapi.GetVcsOwnerAllowlistRequest{Organization: "acme"})
	requireCode(t, err, "forbidden")
	_, err = ui.PutVcsOwnerAllowlist(mintSessionCtx(t, deps, "org-admin"), csilapi.PutVcsOwnerAllowlistRequest{Organization: "acme", Owners: []string{"acme"}})
	requireCode(t, err, "forbidden")

	ctx := mintSessionCtx(t, deps, "root")
	_, err = ui.PutVcsOwnerAllowlist(ctx, csilapi.PutVcsOwnerAllowlistRequest{Organization: "acme", Owners: []string{"not valid"}})
	requireCode(t, err, "invalid_argument")
	resp, err := ui.PutVcsOwnerAllowlist(ctx, csilapi.PutVcsOwnerAllowlistRequest{Organization: "acme", Owners: []string{" Acme-Corp "}})
	requireOK(t, err)
	if len(resp.Owners) != 1 || resp.Owners[0] != "acme-corp" {
		t.Fatalf("owners = %v", resp.Owners)
	}
	got, err := ui.GetVcsOwnerAllowlist(ctx, csilapi.GetVcsOwnerAllowlistRequest{Organization: "acme"})
	requireOK(t, err)
	if len(got.Owners) != 1 || got.Owners[0] != "acme-corp" {
		t.Fatalf("read owners = %v", got.Owners)
	}
	_, err = ui.GetVcsOwnerAllowlist(ctx, csilapi.GetVcsOwnerAllowlistRequest{Organization: "nope"})
	requireCode(t, err, "not_found")
}

func TestOidcSettings(t *testing.T) {
	deps, st := newSettingsDeps(t)
	org := seedSettingsOrg(t, st)
	st.putUser(models.User{UserID: "admin"})
	st.putUser(models.User{UserID: "member"})
	st.putUser(models.User{UserID: "stranger"})
	seedOrgAdmin(st.fakeStore, "admin", org.OrgID)
	st.grantRole(models.PrincipalTypeUser, "member", models.ScopeTypeOrg, &org.OrgID, models.RoleMember)
	project := st.putProject(models.Project{UserID: strPtr(org.OrgID), OrgID: org.OrgID})
	other := st.putProject(models.Project{UserID: strPtr("other-org"), OrgID: "other-org"})
	ui := NewUiService(deps)

	_, err := ui.GetOidcSettings(mintSessionCtx(t, deps, "stranger"), csilapi.GetOidcSettingsRequest{Organization: "acme"})
	requireCode(t, err, "forbidden")

	member := mintSessionCtx(t, deps, "member")
	view, err := ui.GetOidcSettings(member, csilapi.GetOidcSettingsRequest{Organization: "acme"})
	requireOK(t, err)
	if view.Issuer != models.OIDCIssuerAllow || view.CanManage || view.IssuerConfigured || view.EffectiveEnabled {
		t.Fatalf("member view = %+v", view)
	}
	_, err = ui.PutOidcSettings(member, csilapi.PutOidcSettingsRequest{Organization: "acme", Issuer: strPtr("deny")})
	requireCode(t, err, "forbidden")

	admin := mintSessionCtx(t, deps, "admin")
	_, err = ui.PutOidcSettings(admin, csilapi.PutOidcSettingsRequest{Organization: "acme", Issuer: strPtr("inherit")})
	requireCode(t, err, "invalid_argument")
	_, err = ui.PutOidcSettings(admin, csilapi.PutOidcSettingsRequest{Organization: "acme", ProjectId: strPtr(other.ProjectID), Issuer: strPtr("deny")})
	requireCode(t, err, "not_found")

	claims := []csilapi.OidcAudienceClaims{{Audience: "sts.amazonaws.com", Claims: []csilapi.OidcClaim{{Name: "team", Value: "payments"}}}}
	resp, err := ui.PutOidcSettings(admin, csilapi.PutOidcSettingsRequest{Organization: "acme", ProjectId: strPtr(project.ProjectID),
		Issuer: strPtr("inherit"), AudienceClaims: claims})
	requireOK(t, err)
	if resp.Issuer != "inherit" || len(resp.AudienceClaims) != 1 || resp.AudienceClaims[0].Claims[0].Value != "payments" || !resp.CanManage {
		t.Fatalf("project settings = %+v", resp)
	}
	stored := st.oidc[org.OrgID+"/"+project.ProjectID]
	if stored.AudienceClaims["sts.amazonaws.com"]["team"] != "payments" {
		t.Fatalf("stored = %+v", stored)
	}
	dup := []csilapi.OidcAudienceClaims{{Audience: "a"}, {Audience: "a"}}
	_, err = ui.PutOidcSettings(admin, csilapi.PutOidcSettingsRequest{Organization: "acme", AudienceClaims: dup})
	requireCode(t, err, "invalid_argument")
}

func TestOidcKeysNeedGlobalAdmin(t *testing.T) {
	deps, st := newSettingsDeps(t)
	org := seedSettingsOrg(t, st)
	st.putUser(models.User{UserID: "root"})
	st.putUser(models.User{UserID: "admin"})
	seedGlobalAdmin(st.fakeStore, "root")
	seedOrgAdmin(st.fakeStore, "admin", org.OrgID)
	ui := NewUiService(deps)

	_, err := ui.ListOidcKeys(mintSessionCtx(t, deps, "admin"), csilapi.ListOidcKeysRequest{})
	requireCode(t, err, "forbidden")
	root := mintSessionCtx(t, deps, "root")
	keys, err := ui.ListOidcKeys(root, csilapi.ListOidcKeysRequest{})
	requireOK(t, err)
	if keys.IssuerConfigured || len(keys.Keys) != 0 {
		t.Fatalf("keys with no issuer = %+v", keys)
	}
	_, err = ui.RotateOidcKey(root, csilapi.RotateOidcKeyRequest{})
	requireCode(t, err, "unavailable")
	_, err = ui.RetireOidcKey(root, csilapi.RetireOidcKeyRequest{Kid: "k1"})
	requireCode(t, err, "unavailable")

	now := time.Now()
	retired := now.Add(-time.Hour)
	if oidcKeyState(models.OIDCSigningKey{KID: "a", ActivatesAt: now.Add(-2 * time.Hour), RetiresAt: &retired}, "b", now) != "retired" ||
		oidcKeyState(models.OIDCSigningKey{KID: "b", ActivatesAt: now.Add(-time.Hour)}, "b", now) != "signing" ||
		oidcKeyState(models.OIDCSigningKey{KID: "c", ActivatesAt: now.Add(time.Hour)}, "b", now) != "pending" ||
		oidcKeyState(models.OIDCSigningKey{KID: "d", ActivatesAt: now.Add(-3 * time.Hour)}, "b", now) != "published" {
		t.Fatal("key states")
	}
}

func TestSetShedActionsValidation(t *testing.T) {
	deps, st := newTestDeps(t)
	st.putUser(models.User{UserID: "member"})
	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, "member")
	_, err := ui.SetShedActions(ctx, csilapi.SetShedActionsRequest{OrgId: "org-1"})
	requireCode(t, err, "invalid_argument")
	_, err = ui.SetShedActions(ctx, csilapi.SetShedActionsRequest{OrgId: "org-1", Reset: boolPtr(true), Degraded: &csilapi.ShedActions{}})
	requireCode(t, err, "invalid_argument")
	_, err = ui.SetShedActions(ctx, csilapi.SetShedActionsRequest{OrgId: "org-1", Reset: boolPtr(true)})
	requireCode(t, err, "forbidden")
	if got := shedActionsFromWire(csilapi.ShedActions{SkipDrafts: true, RefuseManualRuns: true}); !got.SkipDrafts || !got.RefuseManualRuns || got.PauseSchedules {
		t.Fatalf("actions = %+v", got)
	}
}

func TestSecretGrantDelivery(t *testing.T) {
	deps, st := newTestDeps(t)
	st.putUser(models.User{UserID: "admin"})
	seedOrgAdmin(st, "admin", "org-1")
	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, "admin")

	_, err := ui.CreateSecretGrant(ctx, csilapi.CreateSecretGrantRequest{OrgId: "org-1", Name: "npm", SecretPathMatch: "prefix",
		SecretPathPattern: "ci/npm", Delivery: strPtr("proxy")})
	requireCode(t, err, "invalid_argument")
	_, err = ui.CreateSecretGrant(ctx, csilapi.CreateSecretGrantRequest{OrgId: "org-1", Name: "npm", SecretPathMatch: "prefix",
		SecretPathPattern: "ci/npm", Delivery: strPtr("smoke")})
	requireCode(t, err, "invalid_argument")

	created, err := ui.CreateSecretGrant(ctx, csilapi.CreateSecretGrantRequest{OrgId: "org-1", Name: "npm", SecretPathMatch: "prefix",
		SecretPathPattern: "ci/npm", Delivery: strPtr("proxy"), ProxyHosts: []string{"Registry.NPMjs.org"},
		ProxyHeader: strPtr("Authorization"), ProxyHeaderFormat: strPtr("Bearer {}")})
	requireOK(t, err)
	g := created.Grant
	if derefOr(g.Delivery, "") != "proxy" || len(g.ProxyHosts) != 1 || g.ProxyHosts[0] != "registry.npmjs.org" || derefOr(g.ProxyHeader, "") != "Authorization" {
		t.Fatalf("created grant = %+v", g)
	}

	// An update with no delivery field keeps the delivery.
	updated, err := ui.UpdateSecretGrant(ctx, csilapi.UpdateSecretGrantRequest{GrantId: g.GrantId, Description: strPtr("npm token")})
	requireOK(t, err)
	if derefOr(updated.Grant.Delivery, "") != "proxy" || len(updated.Grant.ProxyHosts) != 1 {
		t.Fatalf("kept delivery = %+v", updated.Grant)
	}
	// Empty hosts with proxy delivery are refused; env delivery clears nothing it does not name.
	_, err = ui.UpdateSecretGrant(ctx, csilapi.UpdateSecretGrantRequest{GrantId: g.GrantId, ProxyHosts: []string{}})
	requireCode(t, err, "invalid_argument")
	updated, err = ui.UpdateSecretGrant(ctx, csilapi.UpdateSecretGrantRequest{GrantId: g.GrantId, Delivery: strPtr("env"), ProxyHosts: []string{}})
	requireOK(t, err)
	if derefOr(updated.Grant.Delivery, "") != "env" || len(updated.Grant.ProxyHosts) != 0 {
		t.Fatalf("env grant = %+v", updated.Grant)
	}
}

func TestSetRequiredState(t *testing.T) {
	decision := csilapi.CIEventWorkflowDecision{WorkflowSecurityId: "build"}
	setRequiredState(&decision, requiredset.EventClassifier{})
	if decision.RequiredState != nil || decision.RequiredLabel != nil {
		t.Fatalf("a project with no required set labels nothing: %+v", decision)
	}
	now := time.Now().UTC()
	until := now.Add(48 * time.Hour)
	classifier := requiredset.EventClassifier{Now: now, Target: requiredset.Target{Branch: "main", Known: true},
		Set: requiredset.Set{Enabled: true, Entries: []models.RequiredWorkflow{
			{WorkflowSecurityID: "build"},
			{WorkflowSecurityID: "lint", QuarantinedUntil: &until},
		}}}
	for id, want := range map[string]string{"build": "required", "lint": "quarantined", "docs": "advisory"} {
		d := csilapi.CIEventWorkflowDecision{WorkflowSecurityId: id}
		setRequiredState(&d, classifier)
		if d.RequiredState == nil || *d.RequiredState != want {
			t.Fatalf("%s state = %v, want %s", id, d.RequiredState, want)
		}
		if want == "required" && d.RequiredLabel != nil {
			t.Fatalf("a required workflow has no label: %v", *d.RequiredLabel)
		}
		if want != "required" && d.RequiredLabel == nil {
			t.Fatalf("%s needs a label", id)
		}
	}
}

func TestWorkflowOwnershipTrusted(t *testing.T) {
	deps, st := newTestDeps(t)
	group := "payments"
	wf := st.putWorkflow(models.WorkflowInstance{OwnerGroup: &group, OwnerTrusted: true, OwnerSource: "ci"})
	ui := NewUiService(deps)
	owner := ui.workflowOwnership(context.Background(), wf.WorkflowID)
	if owner == nil || owner.Trusted == nil || !*owner.Trusted || derefOr(owner.Group, "") != "payments" {
		t.Fatalf("owner = %+v", owner)
	}
	// A job snapshot does not record trust, so it has no flag.
	job := ownershipToWire((&models.Job{OwnerGroup: &group}).Ownership())
	if job == nil || job.Trusted != nil {
		t.Fatalf("job owner = %+v", job)
	}
}

func int64Ptr(v int64) *int64 { return &v }

func TestDeleteDiagnosisArguments(t *testing.T) {
	deps, st := newTestDeps(t)
	st.putUser(models.User{UserID: "user-1"})
	ui := NewUiService(deps)
	_, err := ui.DeleteDiagnosis(anonCtx(), csilapi.DeleteDiagnosisRequest{JobId: strPtr("j")})
	requireCode(t, err, "unauthorized")
	ctx := mintSessionCtx(t, deps, "user-1")
	_, err = ui.DeleteDiagnosis(ctx, csilapi.DeleteDiagnosisRequest{})
	requireCode(t, err, "invalid_argument")
	_, err = ui.DeleteDiagnosis(ctx, csilapi.DeleteDiagnosisRequest{JobId: strPtr("j"), WorkflowId: strPtr("w")})
	requireCode(t, err, "invalid_argument")
	// The fake store keeps no diagnosis entries.
	_, err = ui.DeleteDiagnosis(ctx, csilapi.DeleteDiagnosisRequest{JobId: strPtr("j")})
	requireCode(t, err, "unavailable")
}

func TestListJobsFailureClassFilter(t *testing.T) {
	deps, st := newTestDeps(t)
	seedGlobalAdmin(st, "root")
	st.putUser(models.User{UserID: "root"})
	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, "root")
	_, err := ui.ListJobs(ctx, csilapi.ListJobsRequest{FailureClass: strPtr("cosmic-rays")})
	requireCode(t, err, "invalid_argument")
	_, err = ui.ListJobs(ctx, csilapi.ListJobsRequest{FailureClass: strPtr(models.FailureClasses[0])})
	requireOK(t, err)
}

func TestGetCiPolicyBuiltIn(t *testing.T) {
	deps, st := newTestDeps(t)
	secure := models.Organization{Name: "secure-org", Status: models.OrganizationStatusActive, DefaultsProfile: models.DefaultsProfileSecure}
	legacy := models.Organization{Name: "legacy-org", Status: models.OrganizationStatusActive, DefaultsProfile: models.DefaultsProfileLegacy}
	requireOK(t, st.CreateOrganization(context.Background(), &secure))
	requireOK(t, st.CreateOrganization(context.Background(), &legacy))
	st.putUser(models.User{UserID: "root"})
	seedGlobalAdmin(st, "root")
	securePrj := st.putProject(models.Project{UserID: strPtr(secure.OrgID), OrgID: secure.OrgID})
	legacyPrj := st.putProject(models.Project{UserID: strPtr(legacy.OrgID), OrgID: legacy.OrgID})
	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, "root")

	resp, err := ui.GetCiPolicy(ctx, csilapi.GetCiPolicyRequest{ProjectId: securePrj.ProjectID})
	requireOK(t, err)
	if resp.BuiltIn == nil || !*resp.BuiltIn || resp.Policy.PolicyId != "" || resp.Policy.Revision == "" {
		t.Fatalf("built-in policy = %+v built_in=%v", resp.Policy, resp.BuiltIn)
	}
	_, err = ui.GetCiPolicy(ctx, csilapi.GetCiPolicyRequest{ProjectId: legacyPrj.ProjectID})
	requireCode(t, err, "not_found")
}
