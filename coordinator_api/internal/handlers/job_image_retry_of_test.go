package handlers

import (
	"encoding/json"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// TestJobResponseCarriesImageRetryOf: the REST job response names the earlier
// image of a retry or a re-run that changed the image, and omits it
// otherwise.
func TestJobResponseCarriesImageRetryOf(t *testing.T) {
	alias := "win"
	previous := "win (r.example/win@sha256:old)"
	h := &JobHandler{}
	data, err := json.Marshal(h.jobToResponse(&models.Job{JobID: "j", RunnerImage: "r.example/win@sha256:new", ImageAlias: &alias, ImageRetryOf: &previous}))
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	if body["image_retry_of"] != previous {
		t.Fatalf("image_retry_of = %v, want %q", body["image_retry_of"], previous)
	}
	data, _ = json.Marshal(h.jobToResponse(&models.Job{JobID: "k", RunnerImage: "alpine:3"}))
	body = map[string]any{}
	_ = json.Unmarshal(data, &body)
	if _, ok := body["image_retry_of"]; ok {
		t.Fatalf("image_retry_of present for a job with an unchanged image")
	}
}
