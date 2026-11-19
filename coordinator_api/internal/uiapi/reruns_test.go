package uiapi

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/principals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

func TestRetryJob_ShiftsOverCSIL(t *testing.T) {
	withAuthMode(t, config.UIAuthModeNone)
	deps, st := newTestDeps(t)
	st.putUser(models.User{UserID: "org-1"})
	proj := st.putProject(models.Project{UserID: strPtr("org-1")})
	job := st.putJob(models.Job{UserID: "org-1", ProjectID: &proj.ProjectID, Status: "failed", Name: "j", ResourceMemoryLimit: "4Gi"})
	ui := NewUiService(deps)

	memory, timeout := "8Gi", "30m"
	resp, err := ui.RetryJob(anonCtx(), csilapi.RetryJobRequest{JobId: job.JobID, Shifts: &csilapi.RunShifts{Memory: &memory, Timeout: &timeout}})
	requireOK(t, err)
	retried, err := st.GetJobByID(context.Background(), resp.JobId)
	requireOK(t, err)
	if retried.ResourceMemoryLimit != "8Gi" || retried.TimeoutSeconds != 1800 || retried.Shifts == nil {
		t.Fatalf("retried job = %+v", retried)
	}
	if wire := shiftsToWire(retried.Shifts); wire == nil || *wire.Memory != "8Gi" || *wire.Timeout != "30m0s" {
		t.Fatalf("wire shifts = %+v", wire)
	}

	bad := "lots"
	_, err = ui.RetryJob(anonCtx(), csilapi.RetryJobRequest{JobId: job.JobID, Shifts: &csilapi.RunShifts{Memory: &bad}})
	requireCode(t, err, "invalid_argument")
}

type fakeManualRuns struct {
	input ManualRunInput
	actor principals.Actor
}

func (f *fakeManualRuns) StartManualRunFromUI(_ context.Context, _ *models.Project, input ManualRunInput, actor principals.Actor) (ManualRunOutput, error) {
	f.input, f.actor = input, actor
	return ManualRunOutput{EventID: "e1", Status: "evaluating", Ref: "refs/heads/" + input.Ref, CommitSHA: "abc"}, nil
}

func TestStartRun_PermissionAndForwarding(t *testing.T) {
	t.Run("plain member may not start a run", func(t *testing.T) {
		withAuthMode(t, config.UIAuthModeLocalRP)
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		member := st.putUser(models.User{UserID: "member-1"})
		proj := st.putProject(models.Project{UserID: strPtr("org-1")})
		seedProjectMember(st, member.UserID, proj.ProjectID)
		runs := &fakeManualRuns{}
		deps.ManualRuns = runs
		ref := "main"
		_, err := NewUiService(deps).StartRun(mintSessionCtx(t, deps, member.UserID), csilapi.StartRunRequest{ProjectId: proj.ProjectID, Ref: &ref})
		requireCode(t, err, "forbidden")
		if runs.input.Ref != "" {
			t.Fatal("a refused run reached the starter")
		}
	})

	t.Run("project owner starts a run as the actor", func(t *testing.T) {
		withAuthMode(t, config.UIAuthModeLocalRP)
		deps, st := newTestDeps(t)
		st.putUser(models.User{UserID: "org-1"})
		owner := st.putUser(models.User{UserID: "owner-1"})
		proj := st.putProject(models.Project{UserID: strPtr("org-1")})
		seedProjectOwner(st, owner.UserID, proj.ProjectID)
		runs := &fakeManualRuns{}
		deps.ManualRuns = runs
		ref, workflow := "main", "build"
		resp, err := NewUiService(deps).StartRun(mintSessionCtx(t, deps, owner.UserID),
			csilapi.StartRunRequest{ProjectId: proj.ProjectID, Ref: &ref, Workflow: &workflow})
		requireOK(t, err)
		if resp.EventId != "e1" || runs.input.Ref != "main" || runs.input.Workflow != "build" {
			t.Fatalf("resp %+v input %+v", resp, runs.input)
		}
		if runs.actor.PrincipalID != owner.UserID {
			t.Fatalf("actor = %+v, want the owner", runs.actor)
		}
	})
}
