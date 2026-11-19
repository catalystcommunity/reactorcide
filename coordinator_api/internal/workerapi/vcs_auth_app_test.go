package workerapi

import (
	"context"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
)

// appCheckoutSource is a GitHub App pool with one installed repository.
type appCheckoutSource struct {
	repo  string
	token string
	// project is the project of the binding. The real pool gives a token
	// only to a job of that project.
	project string
	asked   []string
}

func (s *appCheckoutSource) ClientForRepo(context.Context, vcs.Provider, string) (vcs.Client, bool, error) {
	return nil, false, nil
}

func (s *appCheckoutSource) CheckoutToken(_ context.Context, _ vcs.Provider, repo, projectID string) (string, bool, error) {
	s.asked = append(s.asked, repo+"@"+projectID)
	if strings.EqualFold(repo, s.repo) && projectID == s.project {
		return s.token, true, nil
	}
	return "", false, nil
}

// An App-backed repository gets an installation token in vcs_auth, before
// the project PAT. The token is only in vcs_auth.
func TestRequestJob_GitHubAppInstallationTokenInLeaseVcsAuth(t *testing.T) {
	h := newTestHarness()
	const appToken = "ghs_installation_token_value"
	source := &appCheckoutSource{repo: "example/repo", token: appToken, project: "proj-app"}
	vcs.SetAppInstallationSource(source)
	t.Cleanup(func() { vcs.SetAppInstallationSource(nil) })

	h.store.seedProject(&models.Project{ProjectID: "proj-app", RepoURL: "github.com/example/repo",
		VCSTokenSecret: "vcs/example/repo:github_pat"})
	h.secretsProvider.set("vcs/example/repo", "github_pat", "pat-should-not-be-used")
	sourceURL := "https://github.com/example/repo.git"
	projectID := "proj-app"
	job := &models.Job{UserID: "u", ProjectID: &projectID, Name: "build", JobCommand: "true", Status: "submitted", SourceURL: &sourceURL}
	token := seedQueueAndVCSJob(t, h, job, "66666666-6666-6666-6666-666666666666")

	resp, err := h.service.RequestJob(ctxWithAuth(token), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"},
	})
	if err != nil || !resp.HasLease || resp.Lease == nil || resp.Lease.VcsAuth == nil {
		t.Fatalf("RequestJob = %+v, %v; want a lease with vcs_auth", resp, err)
	}
	if resp.Lease.VcsAuth.Token != appToken || resp.Lease.VcsAuth.Username != "x-access-token" {
		t.Fatalf("vcs_auth = %+v, want the installation token", resp.Lease.VcsAuth)
	}
	for _, e := range resp.Lease.Env {
		if strings.Contains(e.Value, appToken) {
			t.Fatalf("installation token in Lease.Env %s", e.Key)
		}
	}
	assertNoSecretInCorndogsPayloads(t, h, appToken)
	assertNoSecretInLeaseJSON(t, resp.Lease, appToken)
	if len(source.asked) != 1 || source.asked[0] != "example/repo@proj-app" {
		t.Fatalf("asked for %v, want the checkout repository", source.asked)
	}
}

// A fork that no installation holds keeps the token path.
func TestRequestJob_GitHubAppForkKeepsTokenPath(t *testing.T) {
	h := newTestHarness()
	source := &appCheckoutSource{repo: "example/repo", token: "ghs_x", project: "proj-app"}
	vcs.SetAppInstallationSource(source)
	t.Cleanup(func() { vcs.SetAppInstallationSource(nil) })

	h.store.seedProject(&models.Project{ProjectID: "proj-app", RepoURL: "github.com/example/repo",
		VCSTokenSecret: "vcs/example/repo:github_pat"})
	h.secretsProvider.set("vcs/example/repo", "github_pat", "project-pat")
	sourceURL := "https://github.com/contributor/repo.git"
	projectID := "proj-app"
	job := &models.Job{UserID: "u", ProjectID: &projectID, Name: "build", JobCommand: "true", Status: "submitted", SourceURL: &sourceURL}
	token := seedQueueAndVCSJob(t, h, job, "77777777-7777-7777-7777-777777777777")

	resp, err := h.service.RequestJob(ctxWithAuth(token), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"},
	})
	if err != nil || resp.Lease == nil || resp.Lease.VcsAuth == nil {
		t.Fatalf("RequestJob = %+v, %v", resp, err)
	}
	if resp.Lease.VcsAuth.Token != "project-pat" {
		t.Fatalf("vcs_auth token = %q, want the project token path", resp.Lease.VcsAuth.Token)
	}
}

// A job of another project (another organization) that names the source URL
// of an App-bound private repository gets no installation token. The pool is
// asked with the project of the job, and the job keeps its own token path.
func TestRequestJob_GitHubAppCrossProjectSourceURLGetsNoAppToken(t *testing.T) {
	h := newTestHarness()
	const appToken = "ghs_victim_installation_token"
	source := &appCheckoutSource{repo: "victim/private", token: appToken, project: "proj-victim"}
	vcs.SetAppInstallationSource(source)
	t.Cleanup(func() { vcs.SetAppInstallationSource(nil) })

	h.store.seedProject(&models.Project{ProjectID: "proj-attacker", OrgID: "org-attacker", RepoURL: "github.com/attacker/repo"})
	sourceURL := "https://github.com/victim/private.git"
	projectID := "proj-attacker"
	job := &models.Job{UserID: "u", OrgID: "org-attacker", ProjectID: &projectID, Name: "steal", JobCommand: "true",
		Status: "submitted", SourceURL: &sourceURL}
	token := seedQueueAndVCSJob(t, h, job, "88888888-8888-8888-8888-888888888888")

	resp, err := h.service.RequestJob(ctxWithAuth(token), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"},
	})
	if err != nil || resp.Lease == nil {
		t.Fatalf("RequestJob = %+v, %v", resp, err)
	}
	if resp.Lease.VcsAuth != nil && resp.Lease.VcsAuth.Token == appToken {
		t.Fatal("a job of another project got the installation token of the victim repository")
	}
	if len(source.asked) != 1 || source.asked[0] != "victim/private@proj-attacker" {
		t.Fatalf("asked for %v, want the project of the job", source.asked)
	}
}

// A job with no project never asks the pool for a token.
func TestRequestJob_GitHubAppNoProjectGetsNoAppToken(t *testing.T) {
	h := newTestHarness()
	source := &appCheckoutSource{repo: "victim/private", token: "ghs_x", project: ""}
	vcs.SetAppInstallationSource(source)
	t.Cleanup(func() { vcs.SetAppInstallationSource(nil) })
	sourceURL := "https://github.com/victim/private.git"
	job := &models.Job{UserID: "u", Name: "steal", JobCommand: "true", Status: "submitted", SourceURL: &sourceURL}
	token := seedQueueAndVCSJob(t, h, job, "99999999-9999-9999-9999-999999999999")
	resp, err := h.service.RequestJob(ctxWithAuth(token), csilapi.RequestJobRequest{
		WorkerCharacteristics: csilapi.WorkerCharacteristics{Os: "linux", Arch: "amd64"},
	})
	if err != nil || resp.Lease == nil {
		t.Fatalf("RequestJob = %+v, %v", resp, err)
	}
	if resp.Lease.VcsAuth != nil && resp.Lease.VcsAuth.Token == "ghs_x" {
		t.Fatal("a job with no project got an installation token")
	}
	if len(source.asked) != 0 {
		t.Fatalf("asked for %v, want no request", source.asked)
	}
}
