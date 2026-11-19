package handlers

import (
	"context"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

type imagePolicyOnlyStore struct {
	store.Store
	policy *models.ImagePolicy
}

func (s imagePolicyOnlyStore) GetImagePolicy(context.Context, string) (*models.ImagePolicy, error) {
	return s.policy, nil
}
func (s imagePolicyOnlyStore) SetImagePolicy(context.Context, *models.ImagePolicy) error { return nil }

func TestImagePolicyRefusalAtSubmit(t *testing.T) {
	st := imagePolicyOnlyStore{policy: &models.ImagePolicy{Enabled: true, AllowedPrefixes: []string{"ghcr.io/acme/"}}}
	evil := "docker.io/evil/x:1"
	if reason := imagePolicyRefusal(context.Background(), st, &models.Job{OrgID: "o", ContainerImage: &evil}); !strings.Contains(reason, "allowed registry prefix") {
		t.Fatalf("reason %q", reason)
	}
	good := "ghcr.io/acme/tool:1"
	if reason := imagePolicyRefusal(context.Background(), st, &models.Job{OrgID: "o", ContainerImage: &good}); reason != "" {
		t.Fatalf("an allowed image was refused: %q", reason)
	}
	off := imagePolicyOnlyStore{policy: &models.ImagePolicy{}}
	if reason := imagePolicyRefusal(context.Background(), off, &models.Job{OrgID: "o", ContainerImage: &evil}); reason != "" {
		t.Fatalf("a disabled policy refused: %q", reason)
	}
}
