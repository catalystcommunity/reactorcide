package jobcontrol

import (
	"context"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/imagealias"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

var retryImageDigest = "sha256:" + strings.Repeat("a", 64)

func aliasResolvedJob(jobID string) *models.Job {
	return &models.Job{
		JobID: jobID, UserID: "user-1", Status: "failed",
		RunnerImage: "oci://registry.example.com/vm/win:2026.11@" + retryImageDigest,
		ImageAlias:  strPtr("windows-vs2022"),
		Characteristics: characteristics.Characteristics{
			"os":                            characteristics.StringValue("windows"),
			imagealias.DigestCharacteristic: characteristics.StringValue(retryImageDigest),
		},
	}
}

// TestRetryJob_KeepsResolvedImage checks that a job retry copies the
// resolved reference, the alias name, and the image_digest characteristic.
func TestRetryJob_KeepsResolvedImage(t *testing.T) {
	original := aliasResolvedJob("job-alias")
	clone := cloneJobForRetry(original)
	if clone.RunnerImage != original.RunnerImage || clone.ImageAlias == nil || *clone.ImageAlias != "windows-vs2022" {
		t.Fatalf("retry image = %q alias = %v", clone.RunnerImage, clone.ImageAlias)
	}
	if clone.ImageAlias == original.ImageAlias {
		t.Fatal("the alias pointer must be a copy")
	}
	if v, _ := clone.Characteristics[imagealias.DigestCharacteristic].(characteristics.StringValue); string(v) != retryImageDigest {
		t.Fatalf("retry digest = %v", clone.Characteristics)
	}
}

// TestRetryWorkflow_KeepsResolvedImage checks that a workflow retry gives
// the new node the resolved image of the earlier job, and that the new job
// gets it without a new resolution. The node spec names the alias; the
// test store has no aliases, so a new resolution would keep the bare name.
func TestRetryWorkflow_KeepsResolvedImage(t *testing.T) {
	st, old := newRetryWorkflowFixture(t)
	st.addNode(&models.WorkflowNode{
		NodeID: "node-old-2", WorkflowID: old.WorkflowID, Name: "win", DisplayName: "win",
		Status: "failed", JobID: strPtr("job-old-2"),
		JobSpec: models.JSONB{"job_name": "win", "job_command": "build.ps1", "container_image": "windows-vs2022",
			"characteristics": map[string]any{"os": "windows"}},
	})
	oldJob := aliasResolvedJob("job-old-2")
	oldJob.WorkflowID, oldJob.WorkflowNodeID = strPtr(old.WorkflowID), strPtr("node-old-2")
	st.addJob(oldJob)

	newWf, err := RetryWorkflow(context.Background(), st, corndogs.NewMockClient(), old.WorkflowID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	nodes, err := st.ListWorkflowNodes(context.Background(), newWf.WorkflowID)
	if err != nil {
		t.Fatal(err)
	}
	var node *models.WorkflowNode
	for i := range nodes {
		if nodes[i].Name == "win" {
			node = &nodes[i]
		}
	}
	if node == nil || node.CarriedImageRef == nil || *node.CarriedImageRef != oldJob.RunnerImage ||
		node.CarriedImageAlias == nil || *node.CarriedImageAlias != "windows-vs2022" ||
		node.CarriedImageDigest == nil || *node.CarriedImageDigest != retryImageDigest {
		t.Fatalf("carried node values = %+v", node)
	}
	if node.JobID == nil {
		t.Fatal("the node was not submitted")
	}
	job, err := st.GetJobByID(context.Background(), *node.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.RunnerImage != oldJob.RunnerImage || job.ImageAlias == nil || *job.ImageAlias != "windows-vs2022" {
		t.Fatalf("retried job image = %q alias = %v", job.RunnerImage, job.ImageAlias)
	}
	if v, _ := job.Characteristics[imagealias.DigestCharacteristic].(characteristics.StringValue); string(v) != retryImageDigest {
		t.Fatalf("retried job characteristics = %v", job.Characteristics)
	}

	// A node whose old job had no image carries nothing.
	for i := range nodes {
		if nodes[i].Name == "build" && nodes[i].CarriedImageRef != nil {
			t.Fatalf("build node carried %q", *nodes[i].CarriedImageRef)
		}
	}
}
