package worker

import (
	"testing"

	"github.com/catalystcommunity/app-utils-go/logging"
)

// TestDockerResourceLimits covers the translation from JobConfig's
// Kubernetes-style quantity fields to Docker's HostConfig resource knobs
// (NanoCPUs, CPUShares, Memory) applied in DockerRunner.SpawnJob.
func TestDockerResourceLimits(t *testing.T) {
	logger := logging.Log.WithField("test", "TestDockerResourceLimits")

	tests := []struct {
		name            string
		config          *JobConfig
		wantNanoCPUs    int64
		wantCPUShares   int64
		wantMemoryBytes int64
	}{
		{
			name:            "cpu limit whole core",
			config:          &JobConfig{CPULimit: "2"},
			wantNanoCPUs:    2_000_000_000,
			wantCPUShares:   0,
			wantMemoryBytes: 0,
		},
		{
			name:            "cpu limit millicores",
			config:          &JobConfig{CPULimit: "500m"},
			wantNanoCPUs:    500_000_000,
			wantCPUShares:   0,
			wantMemoryBytes: 0,
		},
		{
			name:            "cpu request maps to cpu shares",
			config:          &JobConfig{CPURequest: "1"},
			wantNanoCPUs:    0,
			wantCPUShares:   1024,
			wantMemoryBytes: 0,
		},
		{
			name:            "small cpu request floors at docker minimum shares",
			config:          &JobConfig{CPURequest: "1m"},
			wantNanoCPUs:    0,
			wantCPUShares:   2,
			wantMemoryBytes: 0,
		},
		{
			name:            "memory limit binary suffix",
			config:          &JobConfig{MemoryLimit: "4Gi"},
			wantNanoCPUs:    0,
			wantCPUShares:   0,
			wantMemoryBytes: 4 * 1024 * 1024 * 1024,
		},
		{
			name:            "memory limit decimal suffix",
			config:          &JobConfig{MemoryLimit: "512Mi"},
			wantNanoCPUs:    0,
			wantCPUShares:   0,
			wantMemoryBytes: 512 * 1024 * 1024,
		},
		{
			// GB is not valid k8s quantity grammar but IS valid in our own
			// parser (see WORKERS_PLAN.md "Resources") -- this is the
			// end-to-end round trip the custom parser exists for.
			name:            "memory limit GB suffix (rejected by k8s parser, accepted by ours)",
			config:          &JobConfig{MemoryLimit: "4GB"},
			wantNanoCPUs:    0,
			wantCPUShares:   0,
			wantMemoryBytes: 4 * 1000 * 1000 * 1000,
		},
		{
			name:            "memory limit GB equals G suffix",
			config:          &JobConfig{MemoryLimit: "4G"},
			wantNanoCPUs:    0,
			wantCPUShares:   0,
			wantMemoryBytes: 4 * 1000 * 1000 * 1000,
		},
		{
			name: "defaults applied together",
			config: &JobConfig{
				CPURequest:  "1",
				CPULimit:    "2",
				MemoryLimit: "4Gi",
			},
			wantNanoCPUs:    2_000_000_000,
			wantCPUShares:   1024,
			wantMemoryBytes: 4 * 1024 * 1024 * 1024,
		},
		{
			name:            "all empty leaves docker defaults (zero values)",
			config:          &JobConfig{},
			wantNanoCPUs:    0,
			wantCPUShares:   0,
			wantMemoryBytes: 0,
		},
		{
			name:            "invalid cpu limit is ignored, not fatal",
			config:          &JobConfig{CPULimit: "not-a-quantity"},
			wantNanoCPUs:    0,
			wantCPUShares:   0,
			wantMemoryBytes: 0,
		},
		{
			name:            "invalid memory limit is ignored, not fatal",
			config:          &JobConfig{MemoryLimit: "not-a-quantity"},
			wantNanoCPUs:    0,
			wantCPUShares:   0,
			wantMemoryBytes: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nanoCPUs, cpuShares, memBytes := dockerResourceLimits(tt.config, logger)
			if nanoCPUs != tt.wantNanoCPUs {
				t.Errorf("nanoCPUs = %d, want %d", nanoCPUs, tt.wantNanoCPUs)
			}
			if cpuShares != tt.wantCPUShares {
				t.Errorf("cpuShares = %d, want %d", cpuShares, tt.wantCPUShares)
			}
			if memBytes != tt.wantMemoryBytes {
				t.Errorf("memBytes = %d, want %d", memBytes, tt.wantMemoryBytes)
			}
		})
	}
}
