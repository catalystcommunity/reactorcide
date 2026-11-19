package runshifts

import (
	"errors"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func TestParseMap_AcceptsShiftsAndAliases(t *testing.T) {
	got, err := ParseMap(map[string]any{
		"cpu": "4", "cpu_request": "500m", "memory_limit": "8Gi",
		"storage": "20Gi", "storage_request": "10Gi", "timeout": "2h",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := models.RunShifts{CPULimit: "4", CPURequest: "500m", MemoryLimit: "8Gi",
		StorageLimit: "20Gi", StorageRequest: "10Gi", TimeoutSeconds: 7200}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestParseMap_NumbersAndSeconds(t *testing.T) {
	got, err := ParseMap(map[string]any{"cpu": float64(2), "timeout_seconds": float64(90)})
	if err != nil {
		t.Fatal(err)
	}
	if got.CPULimit != "2" || got.TimeoutSeconds != 90 {
		t.Fatalf("got %+v", got)
	}
	if got, err := ParseMap(map[string]any{"timeout": "3600"}); err != nil || got.TimeoutSeconds != 3600 {
		t.Fatalf("plain seconds: %+v %v", got, err)
	}
}

func TestParseMap_RefusesOtherChanges(t *testing.T) {
	for _, key := range []string{"image", "command", "env", "job_command", "runner_image", "capabilities", "secrets", "execution_profile"} {
		_, err := ParseMap(map[string]any{key: "x"})
		if !errors.Is(err, ErrRefused) {
			t.Errorf("%s: got %v, want ErrRefused", key, err)
		}
	}
}

func TestParseMap_InvalidValues(t *testing.T) {
	cases := []map[string]any{
		{"cpu": "lots"},
		{"memory": "8 gigs"},
		{"memory": "0"},
		{"storage": "-1Gi"},
		{"timeout": "soon"},
		{"timeout": "1.5s"},
		{"timeout": "0"},
		{"timeout": "200h"},
		{"cpu": "1", "cpu_request": "2"},
		{"storage": "1Gi", "storage_request": "2Gi"},
		{"cpu": "1", "cpu_limit": "2"},
		{"timeout": "1h", "timeout_seconds": 60.0},
	}
	for _, raw := range cases {
		if _, err := ParseMap(raw); !errors.Is(err, ErrInvalid) {
			t.Errorf("%v: got %v, want ErrInvalid", raw, err)
		}
	}
}

func TestParseArgs(t *testing.T) {
	got, err := ParseArgs([]string{"memory=8Gi", "timeout=2h"})
	if err != nil {
		t.Fatal(err)
	}
	if got.MemoryLimit != "8Gi" || got.TimeoutSeconds != 7200 {
		t.Fatalf("got %+v", got)
	}
	if got, err := ParseArgs(nil); err != nil || !got.IsZero() {
		t.Fatalf("no args: %+v %v", got, err)
	}
	for _, args := range [][]string{{"please"}, {"image=evil"}, {"memory="}, {"=8Gi"}, {"memory=8Gi", "memory=9Gi"}} {
		if _, err := ParseArgs(args); err == nil {
			t.Errorf("%v: expected an error", args)
		}
	}
	if _, err := ParseArgs([]string{"image=evil"}); !errors.Is(err, ErrRefused) {
		t.Errorf("image=evil: got %v, want ErrRefused", err)
	}
}

func TestParseRequestBody(t *testing.T) {
	if got, err := ParseRequestBody(nil); err != nil || !got.IsZero() {
		t.Fatalf("empty body: %+v %v", got, err)
	}
	if got, err := ParseRequestBody([]byte(`{}`)); err != nil || !got.IsZero() {
		t.Fatalf("empty object: %+v %v", got, err)
	}
	got, err := ParseRequestBody([]byte(`{"shifts":{"memory":"8Gi","timeout":"30m"}}`))
	if err != nil || got.MemoryLimit != "8Gi" || got.TimeoutSeconds != 1800 {
		t.Fatalf("shifts: %+v %v", got, err)
	}
	for _, body := range []string{`{"image":"x"}`, `{"shifts":{"memory":"8Gi"},"job_env_vars":{"A":"1"}}`, `{"shifts":{"command":"rm"}}`} {
		if _, err := ParseRequestBody([]byte(body)); !errors.Is(err, ErrRefused) {
			t.Errorf("%s: got %v, want ErrRefused", body, err)
		}
	}
	if _, err := ParseRequestBody([]byte(`[1]`)); !errors.Is(err, ErrInvalid) {
		t.Errorf("array body: got %v, want ErrInvalid", err)
	}
}

func TestCheckLimits(t *testing.T) {
	limits := models.RerunShiftLimits{MaxCPU: "8", MaxMemory: "32Gi", MaxStorage: "100Gi", MaxTimeoutSeconds: 3600}
	ok := models.RunShifts{CPULimit: "8", CPURequest: "4", MemoryLimit: "32Gi", StorageLimit: "100Gi", TimeoutSeconds: 3600}
	if err := CheckLimits(ok, limits); err != nil {
		t.Fatalf("at the limit: %v", err)
	}
	for _, over := range []models.RunShifts{
		{CPULimit: "8500m"}, {CPURequest: "9"}, {MemoryLimit: "33Gi"}, {StorageRequest: "101Gi"}, {TimeoutSeconds: 3601},
	} {
		if err := CheckLimits(over, limits); !errors.Is(err, ErrOverLimit) {
			t.Errorf("%+v: got %v, want ErrOverLimit", over, err)
		}
	}
	// No setting: no extra limit.
	if err := CheckLimits(models.RunShifts{CPULimit: "512", TimeoutSeconds: 600000}, models.RerunShiftLimits{}); err != nil {
		t.Fatalf("no limits: %v", err)
	}
}

func TestApply_ChangesOnlyShiftFields(t *testing.T) {
	job := &models.Job{
		ResourceCPURequest: "1", ResourceCPULimit: "2", ResourceMemoryLimit: "4Gi", TimeoutSeconds: 600,
		ExecutionProfile: "pr-untrusted", CISHA: "base", CIOrigin: "base", PolicyRevision: "rev",
		JobCommand: "make", RunnerImage: "img", JobEnvVars: models.JSONB{"A": "1"},
		Shifts: &models.RunShifts{TimeoutSeconds: 900},
	}
	before := *job
	if err := Apply(job, models.RunShifts{MemoryLimit: "8Gi", StorageLimit: "20Gi"}); err != nil {
		t.Fatal(err)
	}
	if job.ResourceMemoryLimit != "8Gi" || job.ResourceStorageLimit != "20Gi" || job.TimeoutSeconds != 600 {
		t.Fatalf("resources not applied: %+v", job)
	}
	if job.ExecutionProfile != before.ExecutionProfile || job.CISHA != before.CISHA || job.CIOrigin != before.CIOrigin ||
		job.PolicyRevision != before.PolicyRevision || job.JobCommand != before.JobCommand || job.RunnerImage != before.RunnerImage ||
		job.ResourceCPURequest != "1" || job.ResourceCPULimit != "2" {
		t.Fatalf("authority or spec changed: %+v", job)
	}
	want := models.RunShifts{MemoryLimit: "8Gi", StorageLimit: "20Gi", TimeoutSeconds: 900}
	if job.Shifts == nil || *job.Shifts != want {
		t.Fatalf("recorded shifts = %+v, want merged %+v", job.Shifts, want)
	}
}

func TestApply_RefusesRequestOverLimit(t *testing.T) {
	job := &models.Job{ResourceCPURequest: "4", ResourceCPULimit: "8"}
	if err := Apply(job, models.RunShifts{CPULimit: "2"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("got %v, want ErrInvalid", err)
	}
	if job.ResourceCPULimit != "8" || job.Shifts != nil {
		t.Fatalf("a refused apply changed the job: %+v", job)
	}
}

func TestDescribe(t *testing.T) {
	got := Describe(models.RunShifts{MemoryLimit: "8Gi", TimeoutSeconds: 7200})
	if got != "memory=8Gi timeout=2h0m0s" {
		t.Fatalf("got %q", got)
	}
}

// With no rerun_shift_limits, a shift still has the default bounds. A
// configured key replaces only its own default.
func TestEffectiveLimitsDefaults(t *testing.T) {
	limits := EffectiveLimits(models.RerunShiftLimits{})
	for _, over := range []models.RunShifts{{CPULimit: "17"}, {MemoryLimit: "65Gi"}, {StorageLimit: "201Gi"}, {TimeoutSeconds: 24*3600 + 1}} {
		if err := CheckLimits(over, limits); !errors.Is(err, ErrOverLimit) {
			t.Fatalf("%+v: want ErrOverLimit, got %v", over, err)
		}
	}
	if err := CheckLimits(models.RunShifts{CPULimit: "16", MemoryLimit: "64Gi", StorageLimit: "200Gi", TimeoutSeconds: 24 * 3600}, limits); err != nil {
		t.Fatalf("values at the default bounds: %v", err)
	}
	raised := EffectiveLimits(models.RerunShiftLimits{MaxMemory: "256Gi"})
	if err := CheckLimits(models.RunShifts{MemoryLimit: "128Gi"}, raised); err != nil {
		t.Fatalf("a configured limit replaces the default: %v", err)
	}
	if err := CheckLimits(models.RunShifts{CPULimit: "32"}, raised); !errors.Is(err, ErrOverLimit) {
		t.Fatalf("other keys keep their default: %v", err)
	}
}
