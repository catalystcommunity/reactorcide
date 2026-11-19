package jobcontrol

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/imagealias"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// retryAliasStore adds image aliases to retryMockStore, so that a retry
// and a rerun resolve the image of the earlier run again
// (imagealias.ResolveRetry).
type retryAliasStore struct {
	*retryMockStore
	aliases []models.ImageAlias
}

func (s *retryAliasStore) visible(org string, project *string, a models.ImageAlias) bool {
	return a.OrgID == org && (a.ProjectID == nil || (project != nil && *a.ProjectID == *project))
}

func (s *retryAliasStore) ResolveImageAlias(_ context.Context, org string, project *string, name string) (*models.ImageAlias, error) {
	var orgAlias *models.ImageAlias
	for i := range s.aliases {
		a := s.aliases[i]
		if a.Name != name || !s.visible(org, project, a) {
			continue
		}
		if a.ProjectID != nil {
			return &a, nil
		}
		orgAlias = &a
	}
	if orgAlias == nil {
		return nil, store.ErrNotFound
	}
	return orgAlias, nil
}

func (s *retryAliasStore) ListImageAliasesWithDigest(_ context.Context, org string, project *string, digest string, platform models.ImagePlatform) ([]models.ImageAlias, error) {
	var out []models.ImageAlias
	for _, a := range s.aliases {
		if s.visible(org, project, a) && a.HasDigest(digest) && platform.Matches(&a) {
			out = append(out, a)
		}
	}
	return out, nil
}

func (s *retryAliasStore) CreateImageAlias(context.Context, *models.ImageAlias) error { return nil }
func (s *retryAliasStore) UpdateImageAlias(context.Context, *models.ImageAlias) error { return nil }
func (s *retryAliasStore) GetImageAlias(context.Context, string, *string, string) (*models.ImageAlias, error) {
	return nil, store.ErrNotFound
}
func (s *retryAliasStore) GetImageAliasByID(context.Context, string) (*models.ImageAlias, error) {
	return nil, store.ErrNotFound
}
func (s *retryAliasStore) ListImageAliases(context.Context, string, *string) ([]models.ImageAlias, error) {
	return nil, nil
}
func (s *retryAliasStore) ListOrgImageAliases(context.Context, string) ([]models.ImageAlias, error) {
	return nil, nil
}
func (s *retryAliasStore) DeleteImageAlias(context.Context, string, *string, string) error {
	return nil
}
func (s *retryAliasStore) SetImageAliasPending(context.Context, string, string, string) (*models.ImageAlias, error) {
	return nil, nil
}
func (s *retryAliasStore) ClearImageAliasPending(context.Context, string, string) (*models.ImageAlias, error) {
	return nil, nil
}
func (s *retryAliasStore) PromoteImageAliasPending(context.Context, string, string) (*models.ImageAlias, bool, error) {
	return nil, false, nil
}

var _ store.ImageAliasStore = (*retryAliasStore)(nil)

var (
	retryDigestOld = "sha256:" + strings.Repeat("1", 64)
	retryDigestNew = "sha256:" + strings.Repeat("2", 64)
)

func retryVMAlias(project *string) models.ImageAlias {
	return models.ImageAlias{OrgID: "user-1", ProjectID: project, Name: "win", Kind: models.ImageAliasKindVM, OS: "windows", Arch: "amd64",
		Characteristics: characteristics.Characteristics{},
		ActiveRef:       "registry.example.com/vm/win:new@" + retryDigestNew, ActiveDigest: retryDigestNew}
}

func retryVMJob(id string, project *string) *models.Job {
	return &models.Job{JobID: id, UserID: "user-1", ProjectID: project, Status: "failed", JobCommand: "echo hi",
		RunnerImage: "registry.example.com/vm/win:old@" + retryDigestOld, ImageAlias: strPtr("win"),
		Characteristics: characteristics.Characteristics{
			"os":                            characteristics.StringValue("windows"),
			imagealias.DigestCharacteristic: characteristics.StringValue(retryDigestOld),
		}}
}

// TestRetryJob_AfterAliasUpdate: a retry of a job that named an alias uses
// the current pin of the alias (owner decision 2026-11-18), records the
// image of the earlier run, and is refused only when the alias is gone or
// is now for another platform.
func TestRetryJob_AfterAliasUpdate(t *testing.T) {
	project := strPtr("proj-1")
	st := &retryAliasStore{retryMockStore: newRetryMockStore(), aliases: []models.ImageAlias{retryVMAlias(nil)}}
	job := st.addJob(retryVMJob("orig", project))
	oldDisplay := imagealias.DisplayJob(job)

	newJob, err := RetryJob(context.Background(), st, corndogs.NewMockClient(), job)
	if err != nil {
		t.Fatalf("the alias exists: the retry uses its current pin: %v", err)
	}
	wantRef := "registry.example.com/vm/win:new@" + retryDigestNew
	if newJob.RunnerImage != wantRef || newJob.ImageAlias == nil || *newJob.ImageAlias != "win" ||
		newJob.Characteristics[imagealias.DigestCharacteristic] != characteristics.StringValue(retryDigestNew) {
		t.Fatalf("the retry must use the current pin: %+v", newJob)
	}
	if got := imagealias.DisplayJob(newJob); got != "win ("+wantRef+")" {
		t.Fatalf("image display = %q", got)
	}
	if newJob.ImageRetryOf == nil || *newJob.ImageRetryOf != oldDisplay {
		t.Fatalf("image_retry_of = %v, want %q", newJob.ImageRetryOf, oldDisplay)
	}
	if job.Characteristics[imagealias.DigestCharacteristic] != characteristics.StringValue(retryDigestOld) {
		t.Fatal("the retry must not change the earlier job")
	}

	// The organization alias of the same name is for another arch, and the
	// project has no alias: refused.
	st.aliases[0].Arch = "arm64"
	job.Characteristics["arch"] = characteristics.StringValue("amd64")
	_, err = RetryJob(context.Background(), st, corndogs.NewMockClient(), job)
	if !errors.Is(err, ErrImageGone) || !strings.Contains(err.Error(), `image alias "win" is now a vm image for windows/arm64`) {
		t.Fatalf("want ErrImageGone for another platform, got %v", err)
	}
	// A project alias of the same name and platform replaces it.
	st.aliases = append(st.aliases, retryVMAlias(project))
	if _, err := RetryJob(context.Background(), st, corndogs.NewMockClient(), job); err != nil {
		t.Fatalf("the project alias of the same name: %v", err)
	}

	// Every alias deleted: refused, with a message that names the alias.
	st.aliases = nil
	_, err = RetryJob(context.Background(), st, corndogs.NewMockClient(), job)
	if !errors.Is(err, ErrImageGone) || !strings.Contains(err.Error(), `image alias "win" does not exist any more`) {
		t.Fatalf("want ErrImageGone naming the alias, got %v", err)
	}
}

// TestRetryJob_DirectReferenceAfterAliasUpdate: a retry of a job that named
// a pinned reference resolves through the alias that pinned its digest at
// creation (Job.ImagePinAlias). While the alias still pins the digest, the
// job keeps it. After an update, the job gets the current pin and shows
// "alias (ref)".
func TestRetryJob_DirectReferenceAfterAliasUpdate(t *testing.T) {
	alias := retryVMAlias(nil)
	alias.ActiveRef, alias.ActiveDigest = "registry.example.com/vm/win:old@"+retryDigestOld, retryDigestOld
	st := &retryAliasStore{retryMockStore: newRetryMockStore(), aliases: []models.ImageAlias{alias}}
	direct := retryVMJob("direct", nil)
	direct.ImageAlias, direct.ImagePinAlias = nil, strPtr("win")
	job := st.addJob(direct)

	kept, err := RetryJob(context.Background(), st, corndogs.NewMockClient(), job)
	if err != nil {
		t.Fatal(err)
	}
	if kept.RunnerImage != job.RunnerImage || kept.ImageAlias != nil || kept.ImagePinAlias == nil || *kept.ImagePinAlias != "win" || kept.ImageRetryOf != nil {
		t.Fatalf("the alias still pins the digest: the retry keeps it: %+v", kept)
	}

	// The alias gets a new pin: the retry uses it.
	st.aliases[0] = retryVMAlias(nil)
	updated, err := RetryJob(context.Background(), st, corndogs.NewMockClient(), job)
	if err != nil {
		t.Fatal(err)
	}
	wantRef := "registry.example.com/vm/win:new@" + retryDigestNew
	if updated.RunnerImage != wantRef || updated.Characteristics[imagealias.DigestCharacteristic] != characteristics.StringValue(retryDigestNew) {
		t.Fatalf("the retry must use the current pin: %+v", updated)
	}
	if got := imagealias.DisplayJob(updated); got != "win ("+wantRef+")" {
		t.Fatalf("image display = %q", got)
	}
	if updated.ImageRetryOf == nil || *updated.ImageRetryOf != job.RunnerImage {
		t.Fatalf("image_retry_of = %v, want %q", updated.ImageRetryOf, job.RunnerImage)
	}

	// The alias was deleted: refused, naming the alias.
	st.aliases = nil
	_, err = RetryJob(context.Background(), st, corndogs.NewMockClient(), job)
	if !errors.Is(err, ErrImageGone) || !strings.Contains(err.Error(), `image alias "win" does not exist any more`) {
		t.Fatalf("want ErrImageGone naming the alias, got %v", err)
	}
}

// TestRerunWorkflow_AfterAliasUpdate: a workflow rerun resolves the image
// of the earlier run again through its alias. A node whose alias is gone
// is refused (not submitted to wait forever); with the alias, the node gets
// the current pin.
func TestRerunWorkflow_AfterAliasUpdate(t *testing.T) {
	base, old := newRetryWorkflowFixture(t)
	st := &retryAliasStore{retryMockStore: base}
	base.nodes["node-old-1"].JobSpec["characteristics"] = map[string]any{"os": "windows"}
	oldJob := base.jobs["job-old-1"]
	vm := retryVMJob("job-old-1", nil)
	oldJob.RunnerImage, oldJob.ImageAlias, oldJob.Characteristics = vm.RunnerImage, vm.ImageAlias, vm.Characteristics

	mockCorndogs := corndogs.NewMockClient()
	newWf, err := RetryWorkflow(context.Background(), st, mockCorndogs, old.WorkflowID)
	if newWf == nil {
		t.Fatal("the retry must create the new instance")
	}
	if err != nil && !strings.Contains(err.Error(), "does not exist any more") {
		t.Fatalf("err = %v", err)
	}
	nodes, lerr := st.ListWorkflowNodes(context.Background(), newWf.WorkflowID)
	err = lerr
	if err != nil || len(nodes) != 1 {
		t.Fatalf("nodes = %+v err = %v", nodes, err)
	}
	if nodes[0].JobID != nil || len(mockCorndogs.SubmitTaskToQueueCalls) != 0 {
		t.Fatalf("a node whose alias is gone must not get a job: %+v", nodes[0])
	}
	if nodes[0].Status == "submitted" || !strings.Contains(nodes[0].DecisionReason, `image alias "win" does not exist any more`) {
		t.Fatalf("node = %+v", nodes[0])
	}

	// With the alias (now pinned to a new digest), the rerun submits the
	// node with the current pin.
	base2, old2 := newRetryWorkflowFixture(t)
	st2 := &retryAliasStore{retryMockStore: base2, aliases: []models.ImageAlias{retryVMAlias(nil)}}
	base2.nodes["node-old-1"].JobSpec["characteristics"] = map[string]any{"os": "windows"}
	j := base2.jobs["job-old-1"]
	j.RunnerImage, j.ImageAlias, j.Characteristics = vm.RunnerImage, vm.ImageAlias, vm.Characteristics
	newWf2, err := RetryWorkflow(context.Background(), st2, corndogs.NewMockClient(), old2.WorkflowID)
	if err != nil {
		t.Fatal(err)
	}
	nodes, _ = st2.ListWorkflowNodes(context.Background(), newWf2.WorkflowID)
	if len(nodes) != 1 || nodes[0].JobID == nil {
		t.Fatalf("nodes = %+v", nodes)
	}
	created := base2.jobs[*nodes[0].JobID]
	if created.RunnerImage != "registry.example.com/vm/win:new@"+retryDigestNew ||
		created.Characteristics[imagealias.DigestCharacteristic] != characteristics.StringValue(retryDigestNew) {
		t.Fatalf("created job = %+v", created)
	}
	if created.ImageRetryOf == nil || *created.ImageRetryOf != "win ("+vm.RunnerImage+")" {
		t.Fatalf("image_retry_of = %v", created.ImageRetryOf)
	}
}
