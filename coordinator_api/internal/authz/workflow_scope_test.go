package authz

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

// Workflow-scoped roles (B5): allowed for the owned workflow only; another
// workflow of the same project is refused; project scope does not change.
func TestCapabilities_WorkflowScope(t *testing.T) {
	origMode := config.UIAuthMode
	defer func() { config.UIAuthMode = origMode }()
	config.UIAuthMode = config.UIAuthModeLocalRP

	const (
		orgID     = "org-1"
		projectID = "proj-1"
		roleUser  = "user-workflow-role"
		groupUser = "user-in-owner-group"
		stranger  = "user-stranger"
		principal = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"
	)
	fs := newFakeStore()
	fs.projects[projectID] = &models.Project{ProjectID: projectID, UserID: strPtr(orgID)}
	fs.groupsByUser[groupUser] = []models.Group{{GroupID: "g-pay", OrgID: orgID, Name: "payments-ci"}}
	// A group of the same name in ANOTHER org gives nothing.
	fs.groupsByUser[stranger] = []models.Group{{GroupID: "g-other", OrgID: "org-2", Name: "payments-ci"}}
	fs.assignments = []models.RoleAssignment{{PrincipalType: models.PrincipalTypeUser, PrincipalID: roleUser,
		ScopeType: models.ScopeTypeWorkflow, ScopeID: strPtr(projectID), ScopeWorkflow: strPtr("payments"), Role: models.RoleOwner}}
	r := NewResolver(fs)

	group := "payments-ci"
	paymentsOwner := models.WorkflowOwnership{Group: &group, Trusted: true}
	payments := Scope{ProjectID: strPtr(projectID), Workflow: &WorkflowScope{SecurityID: "payments", Owner: paymentsOwner}}
	docs := Scope{ProjectID: strPtr(projectID), Workflow: &WorkflowScope{SecurityID: "docs", Owner: models.WorkflowOwnership{}}}
	project := Scope{ProjectID: strPtr(projectID)}
	ownerCaps := Caps{Cancel: true, Retry: true, ManageWorkflow: true}

	check := func(name string, id Identity, scope Scope, want Caps) {
		t.Helper()
		got, err := r.Capabilities(context.Background(), id, scope)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != want {
			t.Errorf("%s: Capabilities() = %+v, want %+v", name, got, want)
		}
	}
	check("workflow role, own workflow", UserIdentity(roleUser), payments, ownerCaps)
	check("workflow role, other workflow of the project", UserIdentity(roleUser), docs, Caps{})
	check("workflow role, project scope unchanged", UserIdentity(roleUser), project, Caps{})
	check("owner group member, own workflow", UserIdentity(groupUser), payments, ownerCaps)
	check("owner group member, other workflow", UserIdentity(groupUser), docs, Caps{})
	check("owner group member, project scope unchanged", UserIdentity(groupUser), project, Caps{})
	check("same group name in another org", UserIdentity(stranger), payments, Caps{})
	check("unknown owner gives nothing", UserIdentity(groupUser),
		Scope{ProjectID: strPtr(projectID), Workflow: &WorkflowScope{SecurityID: "payments"}}, Caps{})

	// A snapshot from an untrusted ref (a feature-branch push, a pull
	// request head) gives nothing, also when it names the caller's group.
	check("untrusted snapshot gives nothing", UserIdentity(groupUser),
		Scope{ProjectID: strPtr(projectID), Workflow: &WorkflowScope{SecurityID: "payments", Owner: models.WorkflowOwnership{Group: &group}}}, Caps{})

	subjectOwner := models.WorkflowOwnership{Group: strPtr(""), Subjects: []string{"principal:" + principal}, Trusted: true}
	check("extra owner subject principal", UserIdentity(principal),
		Scope{ProjectID: strPtr(projectID), Workflow: &WorkflowScope{SecurityID: "x", Owner: subjectOwner}}, ownerCaps)

	// A user-delegated token intersects as usual: jobs:read gives no cancel.
	readOnly, _ := tokencaps.New(tokencaps.JobsRead)
	token := &checkauth.Principal{CredentialType: "user_token", UserID: groupUser, OrganizationIDs: []string{orgID}, Capabilities: readOnly}
	check("delegated read-only token", Identity{UserID: groupUser, Token: token}, payments, Caps{})
	control, _ := tokencaps.New(tokencaps.WorkflowsControl)
	token2 := &checkauth.Principal{CredentialType: "user_token", UserID: groupUser, OrganizationIDs: []string{orgID}, Capabilities: control}
	check("delegated workflows:control token", Identity{UserID: groupUser, Token: token2}, payments, ownerCaps)

	if owner, err := r.IsWorkflowOwner(context.Background(), UserIdentity(roleUser), projectID, WorkflowScope{SecurityID: "payments"}); err != nil || !owner {
		t.Fatalf("IsWorkflowOwner(role holder) = %v, %v", owner, err)
	}
	if owner, _ := r.IsWorkflowOwner(context.Background(), UserIdentity(roleUser), projectID, WorkflowScope{SecurityID: "docs"}); owner {
		t.Fatal("a workflow role must not reach another workflow")
	}
}
