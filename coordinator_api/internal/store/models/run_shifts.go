package models

import (
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/resources"
)

// RunShifts are the only changes that a retry or a re-run can make to a job
// (owner decisions D5, D18, D25): the CPU request and limit, the memory
// limit, the ephemeral storage request and limit, and the timeout. A run with
// shifts keeps the authority of the original run. Each value is absolute: it
// replaces the value of the original job. An empty value keeps the original
// value. internal/runshifts parses, validates and applies shifts.
type RunShifts struct {
	CPURequest     string `json:"cpu_request,omitempty"`
	CPULimit       string `json:"cpu_limit,omitempty"`
	MemoryLimit    string `json:"memory_limit,omitempty"`
	StorageRequest string `json:"storage_request,omitempty"`
	StorageLimit   string `json:"storage_limit,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

// IsZero reports whether the shifts change nothing.
func (s RunShifts) IsZero() bool {
	return s == RunShifts{}
}

// Ptr returns nil for zero shifts, else a copy.
func (s RunShifts) Ptr() *RunShifts {
	if s.IsZero() {
		return nil
	}
	cp := s
	return &cp
}

// Merge returns base with each value that next sets replaced. A retry of a
// job that has shifts keeps them and adds its own.
func (s RunShifts) Merge(next RunShifts) RunShifts {
	out := s
	if next.CPURequest != "" {
		out.CPURequest = next.CPURequest
	}
	if next.CPULimit != "" {
		out.CPULimit = next.CPULimit
	}
	if next.MemoryLimit != "" {
		out.MemoryLimit = next.MemoryLimit
	}
	if next.StorageRequest != "" {
		out.StorageRequest = next.StorageRequest
	}
	if next.StorageLimit != "" {
		out.StorageLimit = next.StorageLimit
	}
	if next.TimeoutSeconds != 0 {
		out.TimeoutSeconds = next.TimeoutSeconds
	}
	return out
}

// RerunShiftLimits are the largest values that a shift can set. It is an
// organization setting with a project override (ci_trust_settings). An empty
// value has no limit beyond the worker class, the queue and the execution
// profile ceilings.
type RerunShiftLimits struct {
	// MaxCPU bounds the CPU request and the CPU limit.
	MaxCPU string `json:"max_cpu,omitempty" yaml:"max_cpu,omitempty"`
	// MaxMemory bounds the memory limit.
	MaxMemory string `json:"max_memory,omitempty" yaml:"max_memory,omitempty"`
	// MaxStorage bounds the storage request and the storage limit.
	MaxStorage string `json:"max_storage,omitempty" yaml:"max_storage,omitempty"`
	// MaxTimeoutSeconds bounds the timeout.
	MaxTimeoutSeconds int `json:"max_timeout_seconds,omitempty" yaml:"max_timeout_seconds,omitempty"`
}

// Over returns base with each value that override sets replaced.
func (l RerunShiftLimits) Over(override RerunShiftLimits) RerunShiftLimits {
	out := l
	if override.MaxCPU != "" {
		out.MaxCPU = override.MaxCPU
	}
	if override.MaxMemory != "" {
		out.MaxMemory = override.MaxMemory
	}
	if override.MaxStorage != "" {
		out.MaxStorage = override.MaxStorage
	}
	if override.MaxTimeoutSeconds != 0 {
		out.MaxTimeoutSeconds = override.MaxTimeoutSeconds
	}
	return out
}

// Validate checks that each limit is a valid quantity.
func (l RerunShiftLimits) Validate() error {
	for name, value := range map[string]string{"max_cpu": l.MaxCPU} {
		if value != "" {
			if _, err := resources.ParseCPU(value); err != nil {
				return fmt.Errorf("rerun_shift_limits.%s: %w", name, err)
			}
		}
	}
	for name, value := range map[string]string{"max_memory": l.MaxMemory, "max_storage": l.MaxStorage} {
		if value != "" {
			if _, err := resources.ParseMemory(value); err != nil {
				return fmt.Errorf("rerun_shift_limits.%s: %w", name, err)
			}
		}
	}
	if l.MaxTimeoutSeconds < 0 {
		return fmt.Errorf("rerun_shift_limits.max_timeout_seconds must not be negative")
	}
	return nil
}

// LooserThan lists each way that l allows larger shifts than current. An
// empty value is "no limit".
func (l RerunShiftLimits) LooserThan(current RerunShiftLimits) []string {
	var reasons []string
	compare := func(name, next, cur string, parse func(string) (int64, error)) {
		if cur == "" {
			return
		}
		if next == "" {
			reasons = append(reasons, "removes the re-run shift limit "+name)
			return
		}
		n, errN := parse(next)
		c, errC := parse(cur)
		if errN == nil && errC == nil && n > c {
			reasons = append(reasons, fmt.Sprintf("raises the re-run shift limit %s from %s to %s", name, cur, next))
		}
	}
	compare("max_cpu", l.MaxCPU, current.MaxCPU, resources.ParseCPU)
	compare("max_memory", l.MaxMemory, current.MaxMemory, resources.ParseMemory)
	compare("max_storage", l.MaxStorage, current.MaxStorage, resources.ParseMemory)
	if current.MaxTimeoutSeconds > 0 && (l.MaxTimeoutSeconds == 0 || l.MaxTimeoutSeconds > current.MaxTimeoutSeconds) {
		reasons = append(reasons, "raises or removes the re-run shift limit max_timeout_seconds")
	}
	return reasons
}
