package handlers

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs/githubapp"
)

type createProjectRecorder struct {
	*MockStore
	created *models.Project
}

func (s *createProjectRecorder) CreateProject(_ context.Context, project *models.Project) error {
	copy := *project
	s.created = &copy
	return nil
}

func TestGitHubAppRegistryUsesDeploymentRunnerImage(t *testing.T) {
	st := &createProjectRecorder{MockStore: &MockStore{}}
	registry := defaultImageRegistry{StoreRegistry: githubapp.StoreRegistry{Store: st}, image: "registry.example/runner:1"}
	if err := registry.CreateProject(context.Background(), &models.Project{Name: "app", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if st.created == nil || st.created.DefaultRunnerImage != "registry.example/runner:1" {
		t.Fatalf("created = %+v, want the deployment runner image", st.created)
	}
	// A project with an image keeps it; no setting keeps the database default.
	if err := registry.CreateProject(context.Background(), &models.Project{Name: "b", Enabled: true, DefaultRunnerImage: "own:1"}); err != nil {
		t.Fatal(err)
	}
	if st.created.DefaultRunnerImage != "own:1" {
		t.Fatalf("image = %q", st.created.DefaultRunnerImage)
	}
	empty := defaultImageRegistry{StoreRegistry: githubapp.StoreRegistry{Store: st}}
	if err := empty.CreateProject(context.Background(), &models.Project{Name: "c", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if st.created.DefaultRunnerImage != "" {
		t.Fatalf("image = %q, want empty (the database default)", st.created.DefaultRunnerImage)
	}
}
