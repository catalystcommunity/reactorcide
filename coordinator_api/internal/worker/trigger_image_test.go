package worker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/imagealias"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// imageAliasFake is an in-memory store.ImageAliasStore (organization
// aliases only). Embed it in a test store to add image aliases.
type imageAliasFake struct {
	aliases []models.ImageAlias
}

// aliasMockStore adds image aliases to MockStore.
type aliasMockStore struct {
	*MockStore
	*imageAliasFake
}

func (s *imageAliasFake) ResolveImageAlias(_ context.Context, org string, project *string, name string) (*models.ImageAlias, error) {
	for i := range s.aliases {
		if s.aliases[i].OrgID == org && s.aliases[i].Name == name && s.aliases[i].ProjectID == nil {
			a := s.aliases[i]
			return &a, nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *imageAliasFake) ListImageAliasesWithDigest(_ context.Context, org string, _ *string, digest string, platform models.ImagePlatform) ([]models.ImageAlias, error) {
	var out []models.ImageAlias
	for _, a := range s.aliases {
		if a.OrgID == org && a.HasDigest(digest) && platform.Matches(&a) {
			out = append(out, a)
		}
	}
	return out, nil
}

func (s *imageAliasFake) CreateImageAlias(context.Context, *models.ImageAlias) error { return nil }
func (s *imageAliasFake) UpdateImageAlias(context.Context, *models.ImageAlias) error { return nil }
func (s *imageAliasFake) GetImageAlias(context.Context, string, *string, string) (*models.ImageAlias, error) {
	return nil, store.ErrNotFound
}
func (s *imageAliasFake) GetImageAliasByID(context.Context, string) (*models.ImageAlias, error) {
	return nil, store.ErrNotFound
}
func (s *imageAliasFake) ListImageAliases(context.Context, string, *string) ([]models.ImageAlias, error) {
	return nil, nil
}
func (s *imageAliasFake) ListOrgImageAliases(context.Context, string) ([]models.ImageAlias, error) {
	return nil, nil
}
func (s *imageAliasFake) DeleteImageAlias(context.Context, string, *string, string) error { return nil }
func (s *imageAliasFake) SetImageAliasPending(context.Context, string, string, string) (*models.ImageAlias, error) {
	return nil, nil
}
func (s *imageAliasFake) ClearImageAliasPending(context.Context, string, string) (*models.ImageAlias, error) {
	return nil, nil
}
func (s *imageAliasFake) PromoteImageAliasPending(context.Context, string, string) (*models.ImageAlias, bool, error) {
	return nil, false, nil
}

var _ store.ImageAliasStore = (*aliasMockStore)(nil)

// TestTriggerChildInheritsVMImageAfterPromotion: a triggered VM child that
// names no image inherits the resolved image of its parent (reference,
// alias, digest). After a promotion the parent reference is no longer the
// pin of any alias; the child must not resolve it again (that refuses it)
// and must not get the new digest.
func TestTriggerChildInheritsVMImageAfterPromotion(t *testing.T) {
	oldDigest := "sha256:" + strings.Repeat("1", 64)
	newDigest := "sha256:" + strings.Repeat("2", 64)
	oldRef := "registry.example.com/vm/win:old@" + oldDigest
	tmpDir := t.TempDir()
	writeTriggersFile(t, tmpDir, triggersFile{Type: "trigger_job", Jobs: []triggerJobSpec{{
		JobName: "child", JobCommand: "build.ps1", SourceType: "git", SourceURL: "https://github.com/org/repo.git", SourceRef: "abc",
	}}})

	var created []models.Job
	st := &aliasMockStore{MockStore: &MockStore{CreateJobFunc: func(_ context.Context, job *models.Job) error {
		job.JobID = "child-id"
		created = append(created, *job)
		return nil
	}}, imageAliasFake: &imageAliasFake{aliases: []models.ImageAlias{{
		OrgID: "org-1", Name: "win", Kind: models.ImageAliasKindVM, OS: "windows", Arch: "amd64",
		Characteristics: characteristics.Characteristics{},
		// Promoted: the active pin is the new digest now.
		ActiveRef: "registry.example.com/vm/win:new@" + newDigest, ActiveDigest: newDigest,
	}}}}
	parent := &models.Job{JobID: "parent-id", UserID: "user-1", OrgID: "org-1", QueueName: "q", Status: "running",
		RunnerImage: oldRef, ImageAlias: strPtrTest("win"),
		Characteristics: characteristics.Characteristics{
			"os":                            characteristics.StringValue("windows"),
			imagealias.DigestCharacteristic: characteristics.StringValue(oldDigest),
		}}
	if err := NewTriggerProcessor(st, corndogs.NewMockClient()).ProcessTriggers(context.Background(), tmpDir, parent); err != nil {
		t.Fatalf("the child must be created: %v", err)
	}
	if len(created) != 1 {
		t.Fatalf("created = %d", len(created))
	}
	child := created[0]
	if child.RunnerImage != oldRef || child.ImageAlias == nil || *child.ImageAlias != "win" ||
		child.Characteristics[imagealias.DigestCharacteristic] != characteristics.StringValue(oldDigest) {
		t.Fatalf("child image = %q alias = %v chars = %v", child.RunnerImage, child.ImageAlias, child.Characteristics)
	}
	if _, ok := parent.Characteristics[imagealias.DigestCharacteristic]; !ok {
		t.Fatal("the parent map must not change")
	}

	// The alias of the parent was deleted: the child is refused (no worker
	// can get the image), not created to wait forever.
	st.aliases = nil
	created = nil
	_ = NewTriggerProcessor(st, corndogs.NewMockClient()).ProcessTriggers(context.Background(), tmpDir, parent)
	if len(created) != 0 {
		t.Fatalf("a child whose image is gone must not be created: %+v", created)
	}
}

func strPtrTest(s string) *string { return &s }

// aliasWorkflowStore is the workflow runtime store with image aliases.
type aliasWorkflowStore struct {
	*workflowRuntimeProfileStore
	*imageAliasFake
}

// TestWorkflowNodeImageConflictIsRecordedOnTheRun: a workflow job whose
// characteristics conflict with its image alias is refused at creation,
// and the refusal is recorded on the node, the workflow, and the workflow
// outcome (which the check report uses). It is not dropped silently.
func TestWorkflowNodeImageConflictIsRecordedOnTheRun(t *testing.T) {
	baseStore := newWorkflowRuntimeStore()
	st := &aliasWorkflowStore{
		workflowRuntimeProfileStore: &workflowRuntimeProfileStore{
			workflowRuntimeStore: baseStore,
			profile:              &models.ExecutionProfile{OrgID: "org-1", Name: "standard", RuntimeCapabilities: []string{}},
		},
		imageAliasFake: &imageAliasFake{aliases: []models.ImageAlias{{
			OrgID: "org-1", Name: "win-vs", Kind: models.ImageAliasKindVM, OS: "windows", Arch: "amd64",
			Characteristics: characteristics.Characteristics{"vs": characteristics.StringValue("2022")},
			ActiveRef:       "registry.example.com/vm/win:1@sha256:" + strings.Repeat("3", 64), ActiveDigest: "sha256:" + strings.Repeat("3", 64),
		}}},
	}
	parent := &models.Job{JobID: "eval-job", UserID: "user-1", OrgID: "org-1", ExecutionProfile: "standard", WorkerClass: "default", QueueName: "queue-1"}
	baseStore.GetJobByIDFunc = func(_ context.Context, jobID string) (*models.Job, error) {
		if jobID != parent.JobID {
			return nil, store.ErrNotFound
		}
		copy := *parent
		return &copy, nil
	}
	baseStore.CreateJobFunc = func(_ context.Context, job *models.Job) error {
		job.JobID = "job-" + job.Name
		return nil
	}
	payload := triggersFile{Type: "trigger_job", TriggerType: "runnerlib", OperationID: "operation-1",
		Workflows: []triggerWorkflowSpec{{Name: "windows build", ID: "windows-build", Jobs: []triggerJobSpec{{
			JobName: "build-windows", ContainerImage: "win-vs",
			Characteristics: map[string]interface{}{"os": "windows", "vs": "2019"},
		}}}}}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewTriggerProcessor(st, nil).ProcessTriggersFromDataWithOutcomes(context.Background(), data, "", parent)
	if err != nil {
		t.Fatalf("trigger processing failed: %v", err)
	}
	if len(result.Workflows) != 1 || len(result.CreatedJobIDs) != 0 {
		t.Fatalf("outcomes = %#v, created = %v", result.Workflows, result.CreatedJobIDs)
	}
	outcome := result.Workflows[0]
	if outcome.Disposition != "not_admitted" || outcome.Status != "failed" {
		t.Fatalf("outcome = %#v", outcome)
	}
	for _, want := range []string{`image alias "win-vs"`, "vs=2022", "vs=2019", "Set the job characteristic"} {
		if !strings.Contains(outcome.Reason, want) {
			t.Fatalf("outcome reason %q does not contain %q", outcome.Reason, want)
		}
	}
	if wf := baseStore.workflows[outcome.WorkflowID]; wf == nil || !strings.Contains(wf.LastError, `image alias "win-vs"`) {
		t.Fatalf("workflow last error = %+v", wf)
	}
}
