package worker

import (
	"testing"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	corev1 "k8s.io/api/core/v1"
)

func TestApplyWorkflowShifts(t *testing.T) {
	job := &models.Job{ResourceMemoryLimit: "4Gi", TimeoutSeconds: 60, ExecutionProfile: "standard", CISHA: "base"}
	if err := applyWorkflowShifts(job, &models.WorkflowInstance{}); err != nil || job.Shifts != nil {
		t.Fatalf("no shifts: %v %+v", err, job.Shifts)
	}
	wf := &models.WorkflowInstance{Shifts: &models.RunShifts{MemoryLimit: "8Gi", TimeoutSeconds: 120}}
	if err := applyWorkflowShifts(job, wf); err != nil {
		t.Fatal(err)
	}
	if job.ResourceMemoryLimit != "8Gi" || job.TimeoutSeconds != 120 || job.ExecutionProfile != "standard" || job.CISHA != "base" {
		t.Fatalf("job = %+v", job)
	}
}

func TestEnforceProfileLimits(t *testing.T) {
	ceiling := 600
	profile := &models.ExecutionProfile{Name: "p", TimeoutCeilingSeconds: &ceiling,
		ResourceCeilings: models.JSONB{"memory_limit": "8Gi", "storage_limit": "10Gi"}}
	if err := EnforceProfileLimits(&models.Job{TimeoutSeconds: 600, ResourceMemoryLimit: "8Gi", ResourceStorageLimit: "10Gi"}, profile); err != nil {
		t.Fatal(err)
	}
	for _, job := range []*models.Job{{TimeoutSeconds: 601}, {ResourceMemoryLimit: "9Gi"}, {ResourceStorageLimit: "11Gi"}} {
		if err := EnforceProfileLimits(job, profile); err == nil {
			t.Errorf("%+v: expected a ceiling error", job)
		}
	}
}

func TestApplyEphemeralStorage(t *testing.T) {
	reqs := corev1.ResourceRequirements{}
	applyEphemeralStorage(&reqs, &JobConfig{StorageRequest: "1Gi", StorageLimit: "2Gi"}, logging.Log.WithField("test", true))
	request := reqs.Requests[corev1.ResourceEphemeralStorage]
	limit := reqs.Limits[corev1.ResourceEphemeralStorage]
	if request.Value() != 1<<30 || limit.Value() != 2<<30 {
		t.Fatalf("requests %v limits %v", reqs.Requests, reqs.Limits)
	}
	empty := corev1.ResourceRequirements{}
	applyEphemeralStorage(&empty, &JobConfig{}, logging.Log.WithField("test", true))
	if empty.Requests != nil || empty.Limits != nil {
		t.Fatalf("no storage set something: %+v", empty)
	}
}
