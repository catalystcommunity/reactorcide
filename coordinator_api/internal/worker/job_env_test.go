package worker

import (
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func TestBuildJobEnvUsesAuthoritativeSourceFields(t *testing.T) {
	t.Setenv("REACTORCIDE_JOB_API_URL", "")
	t.Setenv("REACTORCIDE_API_TOKEN", "")
	sourceType := models.SourceType("git")
	sourceURL := "https://github.com/example/source.git"
	sourceRef := "head-sha"
	ciSourceURL := "https://github.com/example/ci.git"
	ciSourceRef := "approved-ci-sha"
	job := &models.Job{
		JobID:        "job-1",
		QueueName:    "queue-1",
		SourceType:   &sourceType,
		SourceURL:    &sourceURL,
		SourceRef:    &sourceRef,
		CISourceType: &sourceType,
		CISourceURL:  &ciSourceURL,
		CISourceRef:  &ciSourceRef,
		JobEnvVars: models.JSONB{
			"REACTORCIDE_SOURCE_REF":    "stale-source-sha",
			"REACTORCIDE_CI_SOURCE_REF": "stale-ci-sha",
			"CUSTOM_VALUE":              "preserved",
		},
	}

	env := BuildJobEnv(job)

	if got := env["REACTORCIDE_SOURCE_REF"]; got != sourceRef {
		t.Fatalf("source ref = %q, want %q", got, sourceRef)
	}
	if got := env["REACTORCIDE_CI_SOURCE_REF"]; got != ciSourceRef {
		t.Fatalf("CI source ref = %q, want %q", got, ciSourceRef)
	}
	if got := env["CUSTOM_VALUE"]; got != "preserved" {
		t.Fatalf("custom value = %q, want preserved", got)
	}
}
