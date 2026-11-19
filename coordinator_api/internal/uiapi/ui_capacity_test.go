package uiapi

import (
	"context"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/resourcehistory"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// backlogFakeStore adds the backlog source methods to the UI fake store.
type backlogFakeStore struct {
	*fakeStore
	groups []models.WaitingJobGroup
}

func (b *backlogFakeStore) ListWaitingJobGroups(context.Context) ([]models.WaitingJobGroup, error) {
	return b.groups, nil
}

func TestAddQueueBacklogDetail(t *testing.T) {
	deps, st := newTestDeps(t)
	st.queues["q1"] = models.Queue{QueueID: "q1", QueueUUID: "uuid-east", Characteristics: characteristics.Characteristics{
		"os": characteristics.StringValue("linux"), "region": characteristics.StringValue("us-east-1"),
	}}
	deps.Store = &backlogFakeStore{fakeStore: st, groups: []models.WaitingJobGroup{
		{QueueName: "uuid-east", ResourceCPURequest: "2", ResourceMemoryLimit: "1Gi", Count: 3, OldestCreatedAt: time.Now().Add(-90 * time.Second)},
	}}
	service := NewUiService(deps)
	summaries := []csilapi.QueueSummary{{QueueId: "q1", QueueUuid: "uuid-east"}, {QueueId: "q2", QueueUuid: "uuid-other"}}
	byChar := service.addQueueBacklogDetail(context.Background(), summaries)
	if summaries[0].BacklogCpuRequestMillicores == nil || *summaries[0].BacklogCpuRequestMillicores != 6000 {
		t.Fatalf("cpu = %v", summaries[0].BacklogCpuRequestMillicores)
	}
	if *summaries[0].BacklogMemoryLimitBytes != 3<<30 || *summaries[0].BacklogOldestAgeSeconds < 89 {
		t.Fatalf("summary = %+v", summaries[0])
	}
	if summaries[1].BacklogCpuRequestMillicores != nil {
		t.Fatal("a queue with no backlog row keeps empty fields")
	}
	var region *csilapi.CharacteristicBacklog
	for i := range byChar {
		if byChar[i].Key == "region" {
			region = &byChar[i]
		}
	}
	if region == nil || region.Value != "us-east-1" || region.Count != 3 {
		t.Fatalf("by characteristic = %+v", byChar)
	}

	// The plain fake store has no backlog support: nothing changes.
	plain, _ := newTestDeps(t)
	if got := NewUiService(plain).addQueueBacklogDetail(context.Background(), summaries[1:]); got != nil {
		t.Fatalf("got %+v", got)
	}
}

func TestWorkerAdmissionAndSuggestionToCsil(t *testing.T) {
	if workerAdmissionToCsil(nil) != nil {
		t.Fatal("no report must map to no field")
	}
	reported := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	got := workerAdmissionToCsil(&models.WorkerAdmission{Mode: "resources", CapacityCPUMillicores: 8000, HeadroomMemoryBytes: 5, ReportedAt: reported})
	if got.Mode != "resources" || got.CapacityCpuMillicores != 8000 || got.HeadroomMemoryBytes != 5 || got.ReportedAt != "2026-11-16T12:00:00Z" {
		t.Fatalf("admission = %+v", got)
	}

	insufficient := resourceSuggestionToCsil(resourcehistory.Suggestion{Runs: 2, MinRuns: 5})
	if insufficient.Sufficient || insufficient.CpuRequest != nil || insufficient.Runs != 2 {
		t.Fatalf("suggestion = %+v", insufficient)
	}
	sufficient := resourceSuggestionToCsil(resourcehistory.Suggestion{Runs: 9, MinRuns: 5, Sufficient: true, CPURequest: "750m", CPURequestMillicores: 750, MemoryLimit: "1Gi", MemoryLimitBytes: 1 << 30})
	if *sufficient.CpuRequest != "750m" || *sufficient.MemoryLimitBytes != 1<<30 {
		t.Fatalf("suggestion = %+v", sufficient)
	}
}
