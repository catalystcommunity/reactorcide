package uiapi

import (
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func TestJobSummaryCarriesFailureClass(t *testing.T) {
	class := models.FailureClassImagePull
	summary := jobToSummary(&models.Job{JobID: "j", Status: "failed", FailureClass: &class, FailureDetail: "ErrImagePull", RetryCount: 1})
	if summary.FailureClass == nil || *summary.FailureClass != class {
		t.Fatalf("failure_class = %v", summary.FailureClass)
	}
	if summary.FailureOwner == nil || *summary.FailureOwner != models.FailureOwnerInfrastructure {
		t.Fatalf("failure_owner = %v", summary.FailureOwner)
	}
	if summary.FailureDetail == nil || *summary.FailureDetail != "ErrImagePull" || summary.RetryCount == nil || *summary.RetryCount != 1 {
		t.Fatalf("detail/retries = %v %v", summary.FailureDetail, summary.RetryCount)
	}
	empty := jobToSummary(&models.Job{JobID: "k", Status: "completed"})
	if empty.FailureClass != nil || empty.FailureOwner != nil || empty.RetryCount != nil {
		t.Fatalf("a completed job carries no failure fields: %+v", empty)
	}
}

// TestJobSummaryCarriesImageRetryOf: the job metadata of the UI API names the
// earlier image of a retry or a re-run that changed the image.
func TestJobSummaryCarriesImageRetryOf(t *testing.T) {
	alias := "win"
	previous := "win (r.example/win@sha256:old)"
	summary := jobToSummary(&models.Job{JobID: "j", Status: "queued", RunnerImage: "r.example/win@sha256:new", ImageAlias: &alias, ImageRetryOf: &previous})
	if summary.ImageRetryOf == nil || *summary.ImageRetryOf != previous {
		t.Fatalf("image_retry_of = %v, want %q", summary.ImageRetryOf, previous)
	}
	if summary.ImageDisplay == nil || *summary.ImageDisplay != "win (r.example/win@sha256:new)" {
		t.Fatalf("image_display = %v", summary.ImageDisplay)
	}
	if plain := jobToSummary(&models.Job{JobID: "k", RunnerImage: "alpine:3"}); plain.ImageRetryOf != nil {
		t.Fatalf("a job with an unchanged image carries no image_retry_of: %v", *plain.ImageRetryOf)
	}
}
