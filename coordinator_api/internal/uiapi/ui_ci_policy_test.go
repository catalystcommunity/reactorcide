package uiapi

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/cipolicy"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// testCIPolicy stays authored as YAML because that is what an operator writes
// and it is far more readable than a nested struct literal. testPolicyDocument
// converts it to the wire type, which also exercises the same YAML -> typed
// conversion path cmd/policy.go uses.
const testCIPolicy = `version: 1
defaults:
  ci_source: base
  profile: standard
head_ci:
  - id: tests
    actors:
      any: [repository_write]
    workflows: [tests]
    paths: [.reactorcide/**]
    events: [pull_request_opened, pull_request_updated]
    head_repository: same
    use:
      ci_source: head
      profile: pr-untrusted
      workers: default
`

// testPolicyDocument parses authored YAML into the structured wire document.
func testPolicyDocument(t *testing.T, document string) csilapi.CiPolicyDocument {
	t.Helper()
	parsed, err := cipolicy.ParseDocument([]byte(document))
	if err != nil {
		t.Fatalf("parse test policy: %v", err)
	}
	canonical, err := cipolicy.CanonicalDocument(parsed)
	if err != nil {
		t.Fatalf("canonicalize test policy: %v", err)
	}
	var wire csilapi.CiPolicyDocument
	if err := json.Unmarshal(canonical, &wire); err != nil {
		t.Fatalf("decode test policy: %v", err)
	}
	return wire
}

// TestCIPolicyDocumentRoundTripsEveryField guards the JSON round trip both
// ci_policy_convert.go and cmd/policy.go rely on. A field added to
// cipolicy.Policy but not to CiPolicyDocument (or given a different json tag)
// is silently dropped on the wire -- in a SECURITY policy -- and this is what
// catches that.
func TestCIPolicyDocumentRoundTripsEveryField(t *testing.T) {
	wire := testPolicyDocument(t, testCIPolicy)

	back, err := policyDocumentToPolicy(wire)
	if err != nil {
		t.Fatalf("policyDocumentToPolicy: %v", err)
	}
	original, err := cipolicy.ParseDocument([]byte(testCIPolicy))
	if err != nil {
		t.Fatalf("ParseDocument: %v", err)
	}

	originalCanonical, err := cipolicy.CanonicalDocument(original)
	if err != nil {
		t.Fatalf("CanonicalDocument(original): %v", err)
	}
	roundTripCanonical, err := cipolicy.CanonicalDocument(back)
	if err != nil {
		t.Fatalf("CanonicalDocument(round trip): %v", err)
	}
	if string(originalCanonical) != string(roundTripCanonical) {
		t.Fatalf("policy changed across the wire round trip:\n original   = %s\n round trip = %s",
			originalCanonical, roundTripCanonical)
	}
}

func TestCIPolicyLifecycle(t *testing.T) {
	deps, st := newTestDeps(t)
	admin := st.putUser(models.User{UserID: "admin-1"})
	seedOrgAdmin(st, admin.UserID, "org-1")
	project := st.putProject(models.Project{OrgID: "org-1", UserID: &admin.UserID, Name: "application"})
	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, admin.UserID)

	put, err := ui.PutCiPolicy(ctx, csilapi.PutCiPolicyRequest{ProjectId: project.ProjectID, Document: testPolicyDocument(t, testCIPolicy)})
	requireOK(t, err)
	if put.Policy.Revision == "" || put.Policy.ProjectId != project.ProjectID {
		t.Fatalf("policy = %+v", put.Policy)
	}

	got, err := ui.GetCiPolicy(ctx, csilapi.GetCiPolicyRequest{ProjectId: project.ProjectID})
	requireOK(t, err)
	if got.Policy.Revision != put.Policy.Revision {
		t.Fatalf("revision = %q, want %q", got.Policy.Revision, put.Policy.Revision)
	}

	wrong := "wrong-revision"
	_, err = ui.PutCiPolicy(ctx, csilapi.PutCiPolicyRequest{ProjectId: project.ProjectID, Document: testPolicyDocument(t, testCIPolicy), ExpectedRevision: &wrong})
	requireCode(t, err, "conflict")

	deleted, err := ui.DeleteCiPolicy(ctx, csilapi.DeleteCiPolicyRequest{ProjectId: project.ProjectID, ExpectedRevision: &put.Policy.Revision})
	requireOK(t, err)
	if !deleted.Deleted {
		t.Fatal("Deleted = false")
	}
	_, err = ui.GetCiPolicy(ctx, csilapi.GetCiPolicyRequest{ProjectId: project.ProjectID})
	requireCode(t, err, "not_found")
}

func TestCIPolicyAllowsScopedPolicyManagementToken(t *testing.T) {
	deps, st := newTestDeps(t)
	project := st.putProject(models.Project{OrgID: "org-1", Name: "application"})
	st.apiTokens["policy-token"] = models.APIToken{
		TokenID: "token-1", SubjectType: "instance_token", OrganizationIDs: []string{"org-1"},
		Capabilities: []string{tokencaps.PoliciesManage},
	}
	ui := NewUiService(deps)
	ctx := WithAuthToken(context.Background(), "policy-token")

	put, err := ui.PutCiPolicy(ctx, csilapi.PutCiPolicyRequest{ProjectId: project.ProjectID, Document: testPolicyDocument(t, testCIPolicy)})
	requireOK(t, err)
	if put.Policy.Revision == "" {
		t.Fatal("policy revision is empty")
	}
}

func TestCIPolicyRejectsTokenWithoutPolicyCapability(t *testing.T) {
	deps, st := newTestDeps(t)
	project := st.putProject(models.Project{OrgID: "org-1", Name: "application"})
	st.apiTokens["project-token"] = models.APIToken{
		TokenID: "token-1", SubjectType: "instance_token", OrganizationIDs: []string{"org-1"},
		Capabilities: []string{tokencaps.ProjectsManage},
	}
	ui := NewUiService(deps)
	ctx := WithAuthToken(context.Background(), "project-token")

	_, err := ui.PutCiPolicy(ctx, csilapi.PutCiPolicyRequest{ProjectId: project.ProjectID, Document: testPolicyDocument(t, testCIPolicy)})
	requireCode(t, err, "forbidden")
}

// TestCIPolicyRejectsInvalidRuleID checks that server-side semantic validation
// still runs on a document that arrived over the typed wire.
//
// This test replaces an older one that appended an unknown `policy_maintainers`
// key to the YAML. That case is no longer reachable: an unknown key cannot be
// expressed in csilapi.CiPolicyDocument at all, so the typed wire rejects it at
// decode rather than in the service. What a typed document CAN still carry is a
// well-shaped field holding an invalid value, and cipolicy.validatePolicy is
// what must catch that -- so that is what is asserted here.
func TestCIPolicyRejectsInvalidRuleID(t *testing.T) {
	deps, st := newTestDeps(t)
	admin := st.putUser(models.User{UserID: "admin-1"})
	seedOrgAdmin(st, admin.UserID, "org-1")
	project := st.putProject(models.Project{OrgID: "org-1", UserID: &admin.UserID, Name: "application"})
	ui := NewUiService(deps)
	ctx := mintSessionCtx(t, deps, admin.UserID)

	document := testPolicyDocument(t, testCIPolicy)
	// securityIDPattern is ^[a-z0-9][a-z0-9-]{0,62}$ -- uppercase and spaces
	// are both refused.
	document.HeadCi[0].Id = "Not A Valid ID"

	_, err := ui.PutCiPolicy(ctx, csilapi.PutCiPolicyRequest{ProjectId: project.ProjectID, Document: document})
	requireCode(t, err, "invalid_argument")
}
