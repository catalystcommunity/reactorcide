package admission

import (
	"reflect"
	"testing"
	"time"
)

const gi = int64(1024 * 1024 * 1024)

func TestParseMode(t *testing.T) {
	for raw, want := range map[string]string{"": ModeConcurrency, "concurrency": ModeConcurrency, " Resources ": ModeResources} {
		got, err := ParseMode(raw)
		if err != nil || got != want {
			t.Fatalf("ParseMode(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	if _, err := ParseMode("fair"); err == nil {
		t.Fatal("ParseMode accepted an unknown mode")
	}
}

func TestJobNeedDefaultsAndParses(t *testing.T) {
	need, err := JobNeed("", "")
	if err != nil {
		t.Fatal(err)
	}
	if need != (Resources{CPUMillicores: 1000, MemoryBytes: 4 * gi}) {
		t.Fatalf("default need = %+v", need)
	}
	need, err = JobNeed("500m", "512Mi")
	if err != nil {
		t.Fatal(err)
	}
	if need != (Resources{CPUMillicores: 500, MemoryBytes: 512 * 1024 * 1024}) {
		t.Fatalf("need = %+v", need)
	}
	if _, err := JobNeed("lots", "1Gi"); err == nil {
		t.Fatal("JobNeed accepted an invalid CPU value")
	}
}

func TestResourcesArithmetic(t *testing.T) {
	a := Resources{CPUMillicores: 2000, MemoryBytes: 4 * gi}
	b := Resources{CPUMillicores: 500, MemoryBytes: gi}
	if got := a.Sub(b); got != (Resources{CPUMillicores: 1500, MemoryBytes: 3 * gi}) {
		t.Fatalf("Sub = %+v", got)
	}
	if got := a.Add(b); got != (Resources{CPUMillicores: 2500, MemoryBytes: 5 * gi}) {
		t.Fatalf("Add = %+v", got)
	}
	if !a.Fits(b) || b.Fits(a) {
		t.Fatal("Fits is wrong")
	}
	if (Resources{CPUMillicores: 1}).IsPositive() || !a.IsPositive() {
		t.Fatal("IsPositive is wrong")
	}
}

func TestSelectQueuesFitsHeadroom(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	report := Report{
		Capacity: Resources{CPUMillicores: 8000, MemoryBytes: 32 * gi},
		Headroom: Resources{CPUMillicores: 2000, MemoryBytes: 8 * gi},
	}
	heads := []QueueHead{
		{QueueUUID: "small", Need: Resources{CPUMillicores: 1000, MemoryBytes: 4 * gi}, CreatedAt: now.Add(-time.Minute)},
		{QueueUUID: "big", Need: Resources{CPUMillicores: 4000, MemoryBytes: 8 * gi}, CreatedAt: now.Add(-time.Minute)},
		{QueueUUID: "memory", Need: Resources{CPUMillicores: 500, MemoryBytes: 16 * gi}, CreatedAt: now.Add(-time.Minute)},
	}
	got := SelectQueues([]string{"small", "big", "memory", "unknown"}, heads, report, now, 10*time.Minute)
	if !reflect.DeepEqual(got.Queues, []string{"small", "unknown"}) || got.Reserved != nil {
		t.Fatalf("selection = %+v", got)
	}
}

func TestSelectQueuesReservesForStarvingHead(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	report := Report{
		Capacity: Resources{CPUMillicores: 8000, MemoryBytes: 32 * gi},
		Headroom: Resources{CPUMillicores: 2000, MemoryBytes: 8 * gi},
	}
	big := QueueHead{QueueUUID: "big", JobID: "big-job", Need: Resources{CPUMillicores: 6000, MemoryBytes: 8 * gi}, CreatedAt: now.Add(-20 * time.Minute)}
	small := QueueHead{QueueUUID: "small", Need: Resources{CPUMillicores: 1000, MemoryBytes: gi}, CreatedAt: now.Add(-time.Minute)}
	got := SelectQueues([]string{"small", "big", "unknown"}, []QueueHead{big, small}, report, now, 10*time.Minute)
	if len(got.Queues) != 0 {
		t.Fatalf("worker must take no new work while it reserves room, got %v", got.Queues)
	}
	if got.Reserved == nil || got.Reserved.JobID != "big-job" {
		t.Fatalf("reserved = %+v", got.Reserved)
	}

	// Work that runs before the starving head (higher priority) still goes.
	urgent := QueueHead{QueueUUID: "urgent", Priority: 20, Need: Resources{CPUMillicores: 500, MemoryBytes: gi}, CreatedAt: now}
	got = SelectQueues([]string{"small", "big", "urgent"}, []QueueHead{big, small, urgent}, report, now, 10*time.Minute)
	if !reflect.DeepEqual(got.Queues, []string{"urgent"}) {
		t.Fatalf("selection = %v", got.Queues)
	}

	// Before reserveAfter, the large head does not block other queues.
	young := big
	young.CreatedAt = now.Add(-time.Minute)
	got = SelectQueues([]string{"small", "big"}, []QueueHead{young, small}, report, now, 10*time.Minute)
	if !reflect.DeepEqual(got.Queues, []string{"small"}) || got.Reserved != nil {
		t.Fatalf("selection = %+v", got)
	}

	// reserveAfter <= 0 turns the rule off.
	got = SelectQueues([]string{"small", "big"}, []QueueHead{big, small}, report, now, 0)
	if !reflect.DeepEqual(got.Queues, []string{"small"}) || got.Reserved != nil {
		t.Fatalf("selection = %+v", got)
	}

	// When the worker's headroom grows, the starving head fits and goes.
	grown := report
	grown.Headroom = Resources{CPUMillicores: 7000, MemoryBytes: 16 * gi}
	got = SelectQueues([]string{"small", "big"}, []QueueHead{big, small}, grown, now, 10*time.Minute)
	if !reflect.DeepEqual(got.Queues, []string{"small", "big"}) || got.Reserved != nil {
		t.Fatalf("selection = %+v", got)
	}
}

func TestSelectQueuesIgnoresHeadLargerThanCapacity(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	report := Report{
		Capacity: Resources{CPUMillicores: 4000, MemoryBytes: 8 * gi},
		Headroom: Resources{CPUMillicores: 2000, MemoryBytes: 4 * gi},
	}
	huge := QueueHead{QueueUUID: "huge", Need: Resources{CPUMillicores: 16000, MemoryBytes: 64 * gi}, CreatedAt: now.Add(-time.Hour)}
	small := QueueHead{QueueUUID: "small", Need: Resources{CPUMillicores: 1000, MemoryBytes: gi}, CreatedAt: now}
	got := SelectQueues([]string{"huge", "small"}, []QueueHead{huge, small}, report, now, 10*time.Minute)
	if !reflect.DeepEqual(got.Queues, []string{"small"}) || got.Reserved != nil {
		t.Fatalf("a job that can never fit this worker must not block it: %+v", got)
	}
}

func TestSelectQueuesReservesOldestStarvingHead(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	report := Report{
		Capacity: Resources{CPUMillicores: 8000, MemoryBytes: 32 * gi},
		Headroom: Resources{CPUMillicores: 1000, MemoryBytes: 2 * gi},
	}
	older := QueueHead{QueueUUID: "a", JobID: "older", Need: Resources{CPUMillicores: 4000, MemoryBytes: gi}, CreatedAt: now.Add(-30 * time.Minute)}
	newer := QueueHead{QueueUUID: "b", JobID: "newer", Need: Resources{CPUMillicores: 4000, MemoryBytes: gi}, CreatedAt: now.Add(-15 * time.Minute)}
	got := SelectQueues([]string{"b", "a"}, []QueueHead{newer, older}, report, now, 10*time.Minute)
	if got.Reserved == nil || got.Reserved.JobID != "older" {
		t.Fatalf("reserved = %+v", got.Reserved)
	}
}
