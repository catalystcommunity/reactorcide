package backlog

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/prometheus/client_golang/prometheus"
)

const gi = int64(1024 * 1024 * 1024)

func queue(id, uuid, name, org string, chars characteristics.Characteristics) models.Queue {
	return models.Queue{QueueID: id, QueueUUID: uuid, DisplayName: name, OrgID: org, WorkerClass: "default", Characteristics: chars}
}

func fixture() ([]models.Queue, []models.WaitingJobGroup, time.Time) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	queues := []models.Queue{
		queue("q1", "uuid-east", "east", "org-a", characteristics.Characteristics{"os": characteristics.StringValue("linux"), "region": characteristics.StringValue("us-east-1")}),
		queue("q2", "uuid-west", "west", "org-a", characteristics.Characteristics{"os": characteristics.StringValue("linux"), "region": characteristics.StringValue("us-west-2")}),
		queue("q3", "uuid-idle", "idle", "org-b", characteristics.Characteristics{"os": characteristics.StringValue("linux")}),
	}
	groups := []models.WaitingJobGroup{
		{QueueName: "uuid-east", ResourceCPURequest: "1", ResourceMemoryLimit: "4Gi", Count: 3, OldestCreatedAt: now.Add(-10 * time.Minute)},
		{QueueName: "uuid-east", ResourceCPURequest: "500m", ResourceMemoryLimit: "1Gi", Count: 2, OldestCreatedAt: now.Add(-30 * time.Minute)},
		{QueueName: "uuid-west", ResourceCPURequest: "", ResourceMemoryLimit: "", Count: 1, OldestCreatedAt: now.Add(-time.Minute)},
		{QueueName: "reactorcide-jobs", ResourceCPURequest: "2", ResourceMemoryLimit: "2Gi", Count: 1, OldestCreatedAt: now.Add(-time.Hour)},
	}
	return queues, groups, now
}

func TestComputeAggregatesQueues(t *testing.T) {
	queues, groups, now := fixture()
	snapshot := Compute(queues, groups, nil, now)

	byUUID := map[string]QueueBacklog{}
	for _, q := range snapshot.Queues {
		byUUID[q.QueueUUID] = q
	}
	east := byUUID["uuid-east"]
	if east.Count != 5 || east.CPURequestMillicores != 4000 || east.MemoryLimitBytes != 14*gi || east.OldestAgeSeconds != 1800 {
		t.Fatalf("east = %+v", east)
	}
	if east.Characteristics["region"] != "us-east-1" || !east.Known || east.DisplayName != "east" {
		t.Fatalf("east metadata = %+v", east)
	}
	west := byUUID["uuid-west"]
	if west.Count != 1 || west.CPURequestMillicores != 1000 || west.MemoryLimitBytes != 4*gi {
		t.Fatalf("west uses the job defaults: %+v", west)
	}
	idle := byUUID["uuid-idle"]
	if idle.Count != 0 || idle.OldestCreatedAt != nil || idle.OldestAgeSeconds != 0 {
		t.Fatalf("an idle queue must report zero: %+v", idle)
	}
	legacy, ok := byUUID["reactorcide-jobs"]
	if !ok || legacy.Known || legacy.Count != 1 {
		t.Fatalf("waiting jobs of an unknown queue must show in the global view: %+v", legacy)
	}
	if snapshot.Queues[0].QueueUUID != "uuid-east" {
		t.Fatalf("queues must sort by count, first is %s", snapshot.Queues[0].QueueUUID)
	}
	if snapshot.Totals.Count != 7 || snapshot.Totals.OldestAgeSeconds != 3600 {
		t.Fatalf("totals = %+v", snapshot.Totals)
	}
}

func TestComputeGroupsByCharacteristic(t *testing.T) {
	queues, groups, now := fixture()
	snapshot := Compute(queues, groups, nil, now)
	find := func(key, value string) CharacteristicBacklog {
		for _, c := range snapshot.ByCharacteristic {
			if c.Key == key && c.Value == value {
				return c
			}
		}
		t.Fatalf("no characteristic %s=%s in %+v", key, value, snapshot.ByCharacteristic)
		return CharacteristicBacklog{}
	}
	if linux := find("os", "linux"); linux.Queues != 3 || linux.Count != 6 || linux.OldestAgeSeconds != 1800 {
		t.Fatalf("os=linux = %+v", linux)
	}
	if east := find("region", "us-east-1"); east.Queues != 1 || east.Count != 5 || east.CPURequestMillicores != 4000 {
		t.Fatalf("region=us-east-1 = %+v", east)
	}
	if west := find("region", "us-west-2"); west.Count != 1 {
		t.Fatalf("region=us-west-2 = %+v", west)
	}
}

func TestComputeFilterHidesOtherOrgsAndUnknownQueues(t *testing.T) {
	queues, groups, now := fixture()
	snapshot := Compute(queues, groups, func(q models.Queue) bool { return q.OrgID == "org-b" }, now)
	if len(snapshot.Queues) != 1 || snapshot.Queues[0].QueueUUID != "uuid-idle" || snapshot.Totals.Count != 0 {
		t.Fatalf("filtered snapshot = %+v", snapshot)
	}
}

type fakeSource struct {
	queues []models.Queue
	groups []models.WaitingJobGroup
	err    error
}

func (f fakeSource) ListQueues(context.Context, int, int) ([]models.Queue, error) {
	return f.queues, nil
}

func (f fakeSource) ListWaitingJobGroups(context.Context) ([]models.WaitingJobGroup, error) {
	return f.groups, f.err
}

// gauges gathers one metric family as "queue|queue_name" -> value.
func gauges(t *testing.T, registry *prometheus.Registry, name string) map[string]float64 {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := map[string]string{}
			for _, pair := range metric.GetLabel() {
				labels[pair.GetName()] = pair.GetValue()
			}
			out[labels["queue"]+"|"+labels["queue_name"]] = metric.GetGauge().GetValue()
		}
	}
	return out
}

func TestCollectorExportsGauges(t *testing.T) {
	queues, groups, _ := fixture()
	collector := &Collector{}
	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)
	if got := gauges(t, registry, "reactorcide_queue_backlog_jobs"); len(got) != 0 {
		t.Fatalf("no series before the first read, got %v", got)
	}
	if err := collector.Refresh(context.Background(), fakeSource{queues: queues, groups: groups}); err != nil {
		t.Fatal(err)
	}
	jobs := gauges(t, registry, "reactorcide_queue_backlog_jobs")
	want := map[string]float64{"reactorcide-jobs|": 1, "uuid-east|east": 5, "uuid-idle|idle": 0, "uuid-west|west": 1}
	if len(jobs) != len(want) {
		t.Fatalf("jobs = %v", jobs)
	}
	for key, value := range want {
		if jobs[key] != value {
			t.Fatalf("jobs[%s] = %v, want %v", key, jobs[key], value)
		}
	}
	if cpu := gauges(t, registry, "reactorcide_queue_backlog_cpu_request_cores"); cpu["uuid-east|east"] != 4 {
		t.Fatalf("cpu = %v", cpu)
	}
	if memory := gauges(t, registry, "reactorcide_queue_backlog_memory_limit_bytes"); memory["uuid-east|east"] != float64(14*gi) {
		t.Fatalf("memory = %v", memory)
	}
	if age := gauges(t, registry, "reactorcide_queue_backlog_oldest_age_seconds"); age["uuid-idle|idle"] != 0 {
		t.Fatalf("age = %v", age)
	}

	// A failed read keeps the previous snapshot.
	if err := collector.Refresh(context.Background(), fakeSource{err: errors.New("db down")}); err == nil {
		t.Fatal("expected the read error")
	}
	if got := gauges(t, registry, "reactorcide_queue_backlog_jobs"); len(got) != 4 {
		t.Fatalf("series after a failed read = %v", got)
	}
}
