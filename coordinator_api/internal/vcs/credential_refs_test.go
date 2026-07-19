package vcs

import (
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func TestActiveWebhookSecretsNewestFirst_ReversesOldestFirstInput(t *testing.T) {
	// Store methods return rows ordered oldest-first (created_at ASC).
	// Verification should try the newest active secret first.
	oldest := models.ProjectWebhookSecret{ID: "oldest", SecretRef: "webhooks/proj:old"}
	middle := models.ProjectWebhookSecret{ID: "middle", SecretRef: "webhooks/proj:mid"}
	newest := models.ProjectWebhookSecret{ID: "newest", SecretRef: "webhooks/proj:new"}

	got := ActiveWebhookSecretsNewestFirst([]models.ProjectWebhookSecret{oldest, middle, newest})

	if len(got) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(got))
	}
	if got[0].ID != "newest" || got[1].ID != "middle" || got[2].ID != "oldest" {
		t.Fatalf("expected newest-first order, got %v", []string{got[0].ID, got[1].ID, got[2].ID})
	}
}

func TestActiveWebhookSecretsNewestFirst_EmptyInput(t *testing.T) {
	got := ActiveWebhookSecretsNewestFirst(nil)
	if len(got) != 0 {
		t.Fatalf("expected empty result, got %v", got)
	}
}

func TestActiveWebhookSecretsNewestFirst_SingleRow(t *testing.T) {
	only := models.ProjectWebhookSecret{ID: "only"}
	got := ActiveWebhookSecretsNewestFirst([]models.ProjectWebhookSecret{only})
	if len(got) != 1 || got[0].ID != "only" {
		t.Fatalf("expected single row unchanged, got %v", got)
	}
}

func TestHighestPrecedenceActiveVCSCredential_PicksMostRecentlyCreated(t *testing.T) {
	oldest := models.ProjectVCSCredential{ID: "oldest", SecretRef: "vcs/proj:old"}
	newest := models.ProjectVCSCredential{ID: "newest", SecretRef: "vcs/proj:new"}

	row, ok := HighestPrecedenceActiveVCSCredential([]models.ProjectVCSCredential{oldest, newest})
	if !ok {
		t.Fatal("expected ok=true")
	}
	if row.ID != "newest" {
		t.Fatalf("expected highest-precedence row to be the most recently created (last) row, got %q", row.ID)
	}
}

func TestHighestPrecedenceActiveVCSCredential_EmptyInput(t *testing.T) {
	_, ok := HighestPrecedenceActiveVCSCredential(nil)
	if ok {
		t.Fatal("expected ok=false for empty input")
	}
}
