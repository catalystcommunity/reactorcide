package workerapi

import (
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// A worker never receives a coordinator background task: the queue set of
// RequestJob drops every internal queue, also one with no characteristics
// (which any worker satisfies).
func TestSatisfyingQueuesNeverIncludeInternalQueues(t *testing.T) {
	queues := []models.Queue{
		{QueueUUID: corndogs.OutboundEventsQueue},
		{QueueUUID: corndogs.InternalQueuePrefix + "other"},
		{QueueUUID: "0193a6f0-0000-7000-8000-000000000001"},
	}
	got := satisfyingQueueUUIDs(characteristics.Characteristics{}, queues)
	if len(got) != 1 || got[0] != "0193a6f0-0000-7000-8000-000000000001" {
		t.Fatalf("queue set = %v, want only the worker queue", got)
	}
	if !corndogs.IsInternalQueue(corndogs.OutboundEventsQueue) || corndogs.IsInternalQueue("reactorcide-jobs") {
		t.Fatal("IsInternalQueue classifies the wrong names")
	}
}
