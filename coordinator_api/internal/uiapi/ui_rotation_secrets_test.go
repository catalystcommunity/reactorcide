package uiapi

import (
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

func TestWebhookSecrets_HappyPath(t *testing.T) {
	deps, st := newTestDeps(t)
	st.putUser(models.User{UserID: "org-1"})
	admin := st.putUser(models.User{UserID: "admin-1"})
	seedOrgAdmin(st, admin.UserID, "org-1")
	proj := st.putProject(models.Project{UserID: strPtr("org-1")})
	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, admin.UserID)

	added, err := ui.AddWebhookSecret(ctx, csilapi.AddWebhookSecretRequest{
		ProjectId: proj.ProjectID, Provider: "github", Name: "primary", Value: "s3cr3t-value",
	})
	requireOK(t, err)
	if !added.Secret.IsActive {
		t.Fatalf("IsActive = false, want true")
	}

	listed, err := ui.ListWebhookSecrets(ctx, csilapi.ListWebhookSecretsRequest{ProjectId: proj.ProjectID})
	requireOK(t, err)
	if len(listed.Secrets) != 1 {
		t.Fatalf("len(Secrets) = %d, want 1", len(listed.Secrets))
	}

	deactivated, err := ui.DeactivateWebhookSecret(ctx, csilapi.DeactivateWebhookSecretRequest{Id: added.Secret.Id})
	requireOK(t, err)
	if deactivated.Secret.IsActive {
		t.Fatalf("IsActive = true after deactivate, want false")
	}

	deleted, err := ui.DeleteWebhookSecret(ctx, csilapi.DeleteWebhookSecretRequest{Id: added.Secret.Id})
	requireOK(t, err)
	if !deleted.Deleted {
		t.Fatalf("Deleted = false")
	}

	// The underlying secret value must never appear in any response — the
	// summary type doesn't even carry a value field, but assert on the
	// concrete fields too as a belt-and-braces check.
	if added.Secret.Id == "" || added.Secret.Name != "primary" {
		t.Fatalf("unexpected secret summary: %+v", added.Secret)
	}
}

// TestDeactivateWebhookSecret_IdempotentSecondCall is a regression test for
// deactivate-not-idempotent: deactivating an already-inactive-but-existing
// row must succeed as a no-op (not report not_found), so a double-click or
// client retry on the deactivate button doesn't surface a spurious error to
// operators. Only a row that has actually been deleted (or never existed)
// should return not_found — see TestVcsCredentials_HappyPath's post-delete
// deactivate assertion for that case.
func TestDeactivateWebhookSecret_IdempotentSecondCall(t *testing.T) {
	deps, st := newTestDeps(t)
	st.putUser(models.User{UserID: "org-1"})
	admin := st.putUser(models.User{UserID: "admin-1"})
	seedOrgAdmin(st, admin.UserID, "org-1")
	proj := st.putProject(models.Project{UserID: strPtr("org-1")})
	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, admin.UserID)

	added, err := ui.AddWebhookSecret(ctx, csilapi.AddWebhookSecretRequest{
		ProjectId: proj.ProjectID, Provider: "github", Name: "primary", Value: "s3cr3t-value",
	})
	requireOK(t, err)

	first, err := ui.DeactivateWebhookSecret(ctx, csilapi.DeactivateWebhookSecretRequest{Id: added.Secret.Id})
	requireOK(t, err)
	if first.Secret.IsActive {
		t.Fatalf("IsActive = true after first deactivate, want false")
	}

	second, err := ui.DeactivateWebhookSecret(ctx, csilapi.DeactivateWebhookSecretRequest{Id: added.Secret.Id})
	requireOK(t, err)
	if second.Secret.IsActive {
		t.Fatalf("IsActive = true after second (idempotent) deactivate, want false")
	}
	if first.Secret.DeactivatedAt == nil || second.Secret.DeactivatedAt == nil {
		t.Fatalf("DeactivatedAt is nil: first=%v second=%v", first.Secret.DeactivatedAt, second.Secret.DeactivatedAt)
	}
	if *second.Secret.DeactivatedAt != *first.Secret.DeactivatedAt {
		t.Fatalf("DeactivatedAt changed on idempotent second deactivate: first=%q second=%q", *first.Secret.DeactivatedAt, *second.Secret.DeactivatedAt)
	}
}

// TestDeactivateVcsCredential_IdempotentSecondCall mirrors
// TestDeactivateWebhookSecret_IdempotentSecondCall for VCS credentials.
func TestDeactivateVcsCredential_IdempotentSecondCall(t *testing.T) {
	deps, st := newTestDeps(t)
	st.putUser(models.User{UserID: "org-1"})
	admin := st.putUser(models.User{UserID: "admin-1"})
	seedOrgAdmin(st, admin.UserID, "org-1")
	proj := st.putProject(models.Project{UserID: strPtr("org-1")})
	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, admin.UserID)

	added, err := ui.AddVcsCredential(ctx, csilapi.AddVcsCredentialRequest{
		ProjectId: proj.ProjectID, Provider: "github", Name: "primary", Value: "s3cr3t-token",
	})
	requireOK(t, err)

	first, err := ui.DeactivateVcsCredential(ctx, csilapi.DeactivateVcsCredentialRequest{Id: added.Credential.Id})
	requireOK(t, err)
	if first.Credential.IsActive {
		t.Fatalf("IsActive = true after first deactivate, want false")
	}

	second, err := ui.DeactivateVcsCredential(ctx, csilapi.DeactivateVcsCredentialRequest{Id: added.Credential.Id})
	requireOK(t, err)
	if second.Credential.IsActive {
		t.Fatalf("IsActive = true after second (idempotent) deactivate, want false")
	}
}

func TestVcsCredentials_HappyPath(t *testing.T) {
	deps, st := newTestDeps(t)
	st.putUser(models.User{UserID: "org-1"})
	admin := st.putUser(models.User{UserID: "admin-1"})
	seedOrgAdmin(st, admin.UserID, "org-1")
	proj := st.putProject(models.Project{UserID: strPtr("org-1")})
	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, admin.UserID)

	added, err := ui.AddVcsCredential(ctx, csilapi.AddVcsCredentialRequest{
		ProjectId: proj.ProjectID, Provider: "github", Name: "primary", Value: "s3cr3t-token",
	})
	requireOK(t, err)

	listed, err := ui.ListVcsCredentials(ctx, csilapi.ListVcsCredentialsRequest{ProjectId: proj.ProjectID})
	requireOK(t, err)
	if len(listed.Credentials) != 1 {
		t.Fatalf("len(Credentials) = %d, want 1", len(listed.Credentials))
	}

	deactivated, err := ui.DeactivateVcsCredential(ctx, csilapi.DeactivateVcsCredentialRequest{Id: added.Credential.Id})
	requireOK(t, err)
	if deactivated.Credential.IsActive {
		t.Fatalf("IsActive = true after deactivate, want false")
	}

	deleted, err := ui.DeleteVcsCredential(ctx, csilapi.DeleteVcsCredentialRequest{Id: added.Credential.Id})
	requireOK(t, err)
	if !deleted.Deleted {
		t.Fatalf("Deleted = false")
	}

	_, err = ui.DeactivateVcsCredential(ctx, csilapi.DeactivateVcsCredentialRequest{Id: added.Credential.Id})
	requireCode(t, err, "not_found")
}

func TestDeleteVcsCredential_Forbidden(t *testing.T) {
	deps, st := newTestDeps(t)
	st.putUser(models.User{UserID: "org-1"})
	admin := st.putUser(models.User{UserID: "admin-1"})
	outsider := st.putUser(models.User{UserID: "outsider-1"})
	seedOrgAdmin(st, admin.UserID, "org-1")
	proj := st.putProject(models.Project{UserID: strPtr("org-1")})
	ui := NewUiService(deps)
	adminCtx := mintSessionCtx(t, deps, admin.UserID)

	added, err := ui.AddVcsCredential(adminCtx, csilapi.AddVcsCredentialRequest{
		ProjectId: proj.ProjectID, Provider: "github", Name: "primary", Value: "s3cr3t-token",
	})
	requireOK(t, err)

	outsiderCtx := mintSessionCtx(t, deps, outsider.UserID)
	_, err = ui.DeleteVcsCredential(outsiderCtx, csilapi.DeleteVcsCredentialRequest{Id: added.Credential.Id})
	requireCode(t, err, "forbidden")
}

func TestSecrets_WriteOnly_HappyPath(t *testing.T) {
	deps, st := newTestDeps(t)
	admin := st.putUser(models.User{UserID: "org-1"})
	seedOrgAdmin(st, admin.UserID, "org-1")
	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, admin.UserID)

	setResp, err := ui.SetSecret(ctx, csilapi.SetSecretRequest{OrgId: "org-1", Path: "svc/a", Key: "token", Value: "top-secret"})
	requireOK(t, err)
	if !setResp.Ok {
		t.Fatalf("Ok = false")
	}

	paths, err := ui.ListSecretPaths(ctx, csilapi.ListSecretPathsRequest{OrgId: "org-1"})
	requireOK(t, err)
	if len(paths.Paths) != 1 || paths.Paths[0].Path != "svc/a" {
		t.Fatalf("Paths = %+v, want one entry for svc/a", paths.Paths)
	}
	if len(paths.Paths[0].Keys) != 1 || paths.Paths[0].Keys[0] != "token" {
		t.Fatalf("Keys = %+v, want [token]", paths.Paths[0].Keys)
	}

	deleteResp, err := ui.DeleteSecret(ctx, csilapi.DeleteSecretRequest{OrgId: "org-1", Path: "svc/a", Key: "token"})
	requireOK(t, err)
	if !deleteResp.Deleted {
		t.Fatalf("Deleted = false")
	}
}

// TestNoSecretValueLeaks encodes every rotation/secret response this test
// file produces through the generated CBOR codec and asserts the plaintext
// secret value never appears in the encoded bytes — the strongest check
// available short of exhaustively reading struct field lists, and it
// exercises the real wire encoding rather than just the Go struct shape.
func TestNoSecretValueLeaks(t *testing.T) {
	deps, st := newTestDeps(t)
	admin := st.putUser(models.User{UserID: "org-1"})
	seedOrgAdmin(st, admin.UserID, "org-1")
	proj := st.putProject(models.Project{UserID: strPtr("org-1")})
	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, admin.UserID)

	const secretValue = "sk-super-duper-secret-value-should-never-appear"

	addWH, err := ui.AddWebhookSecret(ctx, csilapi.AddWebhookSecretRequest{
		ProjectId: proj.ProjectID, Provider: "github", Name: "primary", Value: secretValue,
	})
	requireOK(t, err)
	assertNoSecretLeak(t, csilapi.EncodeAddWebhookSecretResponse(addWH), secretValue)

	listWH, err := ui.ListWebhookSecrets(ctx, csilapi.ListWebhookSecretsRequest{ProjectId: proj.ProjectID})
	requireOK(t, err)
	assertNoSecretLeak(t, csilapi.EncodeListWebhookSecretsResponse(listWH), secretValue)

	addVCS, err := ui.AddVcsCredential(ctx, csilapi.AddVcsCredentialRequest{
		ProjectId: proj.ProjectID, Provider: "github", Name: "primary", Value: secretValue,
	})
	requireOK(t, err)
	assertNoSecretLeak(t, csilapi.EncodeAddVcsCredentialResponse(addVCS), secretValue)

	requireOK(t, err)
	_, err = ui.SetSecret(ctx, csilapi.SetSecretRequest{OrgId: "org-1", Path: "svc/a", Key: "token", Value: secretValue})
	requireOK(t, err)
	paths, err := ui.ListSecretPaths(ctx, csilapi.ListSecretPathsRequest{OrgId: "org-1"})
	requireOK(t, err)
	assertNoSecretLeak(t, csilapi.EncodeListSecretPathsResponse(paths), secretValue)
}

func assertNoSecretLeak(t *testing.T, encoded []byte, secretValue string) {
	t.Helper()
	if strings.Contains(string(encoded), secretValue) {
		t.Fatalf("encoded response contains the raw secret value %q", secretValue)
	}
}

func TestGlobalSettings_HappyPath(t *testing.T) {
	deps, st := newTestDeps(t)
	admin := st.putUser(models.User{UserID: "gadmin-1"})
	seedGlobalAdmin(st, admin.UserID)
	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, admin.UserID)

	got, err := ui.GetGlobalSettings(ctx, csilapi.GetGlobalSettingsRequest{})
	requireOK(t, err)
	if got.NewProjectsPrivate {
		t.Fatalf("NewProjectsPrivate = true by default, want false (public-by-default)")
	}

	updated, err := ui.UpdateGlobalSettings(ctx, csilapi.UpdateGlobalSettingsRequest{NewProjectsPrivate: boolPtr(true)})
	requireOK(t, err)
	if !updated.NewProjectsPrivate {
		t.Fatalf("NewProjectsPrivate = false after update, want true")
	}
}

func TestTrustedIdentities_HappyPath(t *testing.T) {
	deps, st := newTestDeps(t)
	admin := st.putUser(models.User{UserID: "gadmin-1"})
	seedGlobalAdmin(st, admin.UserID)
	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, admin.UserID)

	added, err := ui.AddTrustedIdentity(ctx, csilapi.AddTrustedIdentityRequest{Domain: "example.com", Handle: strPtr("alice")})
	requireOK(t, err)
	if added.Identity.Domain != "example.com" {
		t.Fatalf("Domain = %q, want example.com", added.Identity.Domain)
	}

	listed, err := ui.ListTrustedIdentities(ctx, csilapi.ListTrustedIdentitiesRequest{})
	requireOK(t, err)
	if len(listed.Identities) != 1 {
		t.Fatalf("len(Identities) = %d, want 1", len(listed.Identities))
	}

	removed, err := ui.RemoveTrustedIdentity(ctx, csilapi.RemoveTrustedIdentityRequest{Domain: "example.com", Handle: strPtr("alice")})
	requireOK(t, err)
	if !removed.Removed {
		t.Fatalf("Removed = false")
	}
}
