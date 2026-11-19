// Package runshifts parses, validates and applies the shifts of a retry or a
// re-run (owner decisions D5, D18, D25).
//
// A shift is one of: the CPU request, the CPU limit, the memory limit, the
// ephemeral storage request, the ephemeral storage limit, and the timeout. A
// retry or a re-run that changes only shifts keeps the authority of the
// original run: the source and CI SHAs, the CI origin, the execution profile,
// the policy revision, rule and approval, the node authority, the ref
// classes and the secret grants. A request for any other change is refused
// with ErrRefused: the principal must submit a new run.
//
// Every channel (REST, CSIL, CLI and PR comments) uses the same key names:
//
//	cpu             the CPU limit (alias cpu_limit)
//	cpu_request     the CPU request
//	memory          the memory limit (alias memory_limit)
//	storage         the ephemeral storage limit (alias storage_limit)
//	storage_request the ephemeral storage request
//	timeout         a Go duration ("2h", "90m") or whole seconds ("7200")
//	timeout_seconds whole seconds
package runshifts

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/resources"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// ErrRefused is returned for a request that changes more than the allowed
// shifts (D25).
var ErrRefused = errors.New("a retry or a re-run can change only cpu, cpu_request, memory, storage, storage_request and timeout; submit a new run for other changes")

// ErrInvalid is returned for a shift with a value that is not valid.
var ErrInvalid = errors.New("invalid shift")

// ErrOverLimit is returned for a shift that is larger than the
// rerun_shift_limits setting allows.
var ErrOverLimit = errors.New("shift is over the rerun_shift_limits setting")

// MaxTimeout is the largest timeout a shift can set when no setting is
// smaller. It stops a typing error from holding a worker for a long time.
const MaxTimeout = 7 * 24 * time.Hour

// Keys is the list of accepted keys, in display order.
var Keys = []string{"cpu", "cpu_request", "memory", "storage", "storage_request", "timeout"}

var aliases = map[string]string{
	"cpu": "cpu", "cpu_limit": "cpu",
	"cpu_request": "cpu_request",
	"memory":      "memory", "memory_limit": "memory",
	"storage": "storage", "storage_limit": "storage",
	"storage_request": "storage_request",
	"timeout":         "timeout", "timeout_seconds": "timeout_seconds",
}

// refuse wraps ErrRefused with the key that is not a shift.
func refuse(key string) error {
	return fmt.Errorf("%q is not a shift: %w", key, ErrRefused)
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// ParseMap reads shifts from a decoded JSON or YAML object. An unknown key is
// refused with ErrRefused. A nil or empty map is no shifts.
func ParseMap(raw map[string]any) (models.RunShifts, error) {
	var out models.RunShifts
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	seen := map[string]string{}
	for _, key := range keys {
		value := raw[key]
		canonical, ok := aliases[strings.TrimSpace(key)]
		if !ok {
			return models.RunShifts{}, refuse(key)
		}
		group := canonical
		if group == "timeout_seconds" {
			group = "timeout"
		}
		if earlier, dup := seen[group]; dup {
			return models.RunShifts{}, invalid("%s and %s set the same shift", earlier, key)
		}
		seen[group] = key
		if value == nil {
			continue
		}
		if err := set(&out, canonical, key, value); err != nil {
			return models.RunShifts{}, err
		}
	}
	if err := Validate(out); err != nil {
		return models.RunShifts{}, err
	}
	return out, nil
}

// ParseArgs reads shifts from "key=value" words, for example the arguments
// of "/reactorcide retry memory=8Gi timeout=2h". Each word must be one shift.
func ParseArgs(args []string) (models.RunShifts, error) {
	raw := map[string]any{}
	for _, arg := range args {
		key, value, ok := strings.Cut(arg, "=")
		if !ok || key == "" || value == "" {
			return models.RunShifts{}, fmt.Errorf("%q is not a key=value shift: %w", arg, ErrRefused)
		}
		if _, dup := raw[key]; dup {
			return models.RunShifts{}, invalid("%s is given more than one time", key)
		}
		raw[key] = value
	}
	return ParseMap(raw)
}

func set(out *models.RunShifts, canonical, key string, value any) error {
	var err error
	switch canonical {
	case "cpu":
		out.CPULimit, err = resources.ParseQuantity(key, value, resources.ParseCPU)
	case "cpu_request":
		out.CPURequest, err = resources.ParseQuantity(key, value, resources.ParseCPU)
	case "memory":
		out.MemoryLimit, err = resources.ParseQuantity(key, value, resources.ParseMemory)
	case "storage":
		out.StorageLimit, err = resources.ParseQuantity(key, value, resources.ParseMemory)
	case "storage_request":
		out.StorageRequest, err = resources.ParseQuantity(key, value, resources.ParseMemory)
	case "timeout", "timeout_seconds":
		out.TimeoutSeconds, err = parseTimeout(key, value, canonical == "timeout")
	}
	if err != nil && !errors.Is(err, ErrInvalid) {
		err = fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return err
}

func parseTimeout(key string, value any, allowDuration bool) (int, error) {
	var seconds float64
	switch v := value.(type) {
	case int:
		seconds = float64(v)
	case int64:
		seconds = float64(v)
	case float64:
		seconds = v
	case string:
		text := strings.TrimSpace(v)
		if n, err := strconv.ParseInt(text, 10, 64); err == nil {
			seconds = float64(n)
		} else if allowDuration {
			d, err := time.ParseDuration(text)
			if err != nil {
				return 0, invalid("%s %q is not a duration (for example 2h or 90m) or a number of seconds", key, text)
			}
			if d%time.Second != 0 {
				return 0, invalid("%s %q must be whole seconds", key, text)
			}
			seconds = d.Seconds()
		} else {
			return 0, invalid("%s %q is not a number of seconds", key, text)
		}
	default:
		return 0, invalid("%s must be a duration or a number of seconds", key)
	}
	if seconds != float64(int64(seconds)) {
		return 0, invalid("%s must be whole seconds", key)
	}
	if seconds < 1 {
		return 0, invalid("%s must be at least 1 second", key)
	}
	if seconds > MaxTimeout.Seconds() {
		return 0, invalid("%s must not be more than %s", key, MaxTimeout)
	}
	return int(seconds), nil
}

// Validate checks each value of s and that a request is not more than the
// limit when s sets both.
func Validate(s models.RunShifts) error {
	checks := []struct {
		name, value string
		parse       func(string) (int64, error)
	}{
		{"cpu_request", s.CPURequest, resources.ParseCPU},
		{"cpu", s.CPULimit, resources.ParseCPU},
		{"memory", s.MemoryLimit, resources.ParseMemory},
		{"storage_request", s.StorageRequest, resources.ParseMemory},
		{"storage", s.StorageLimit, resources.ParseMemory},
	}
	for _, check := range checks {
		if check.value == "" {
			continue
		}
		n, err := check.parse(check.value)
		if err != nil {
			return invalid("%s %q: %v", check.name, check.value, err)
		}
		if n <= 0 {
			return invalid("%s must be more than zero", check.name)
		}
	}
	if s.TimeoutSeconds < 0 || float64(s.TimeoutSeconds) > MaxTimeout.Seconds() {
		return invalid("timeout must be from 1 second to %s", MaxTimeout)
	}
	return checkOrder(s.CPURequest, s.CPULimit, s.StorageRequest, s.StorageLimit)
}

func checkOrder(cpuRequest, cpuLimit, storageRequest, storageLimit string) error {
	if cpuRequest != "" && cpuLimit != "" {
		request, errR := resources.ParseCPU(cpuRequest)
		limit, errL := resources.ParseCPU(cpuLimit)
		if errR == nil && errL == nil && request > limit {
			return invalid("the CPU request (%s) must not be more than the CPU limit (%s)", cpuRequest, cpuLimit)
		}
	}
	if storageRequest != "" && storageLimit != "" {
		request, errR := resources.ParseMemory(storageRequest)
		limit, errL := resources.ParseMemory(storageLimit)
		if errR == nil && errL == nil && request > limit {
			return invalid("the storage request (%s) must not be more than the storage limit (%s)", storageRequest, storageLimit)
		}
	}
	return nil
}

// Default upper bounds of a shift, for each key that no rerun_shift_limits
// setting (organization or project) sets. An operator changes a bound with
// rerun_shift_limits; a larger value there permits more.
const (
	DefaultMaxCPU            = "16"
	DefaultMaxMemory         = "64Gi"
	DefaultMaxStorage        = "200Gi"
	DefaultMaxTimeoutSeconds = 24 * 60 * 60
)

// DefaultLimits returns the default upper bounds of a shift.
func DefaultLimits() models.RerunShiftLimits {
	return models.RerunShiftLimits{MaxCPU: DefaultMaxCPU, MaxMemory: DefaultMaxMemory,
		MaxStorage: DefaultMaxStorage, MaxTimeoutSeconds: DefaultMaxTimeoutSeconds}
}

// EffectiveLimits returns configured over the default bounds, key by key: a
// key that configured does not set gets its default bound. A shift is never
// without a bound.
func EffectiveLimits(configured models.RerunShiftLimits) models.RerunShiftLimits {
	return DefaultLimits().Over(configured)
}

// CheckLimits refuses a shift that is larger than limits allows. An empty
// limit has no bound; callers give EffectiveLimits, so every key has one.
func CheckLimits(s models.RunShifts, limits models.RerunShiftLimits) error {
	over := func(name, value, max string, parse func(string) (int64, error)) error {
		if value == "" || max == "" {
			return nil
		}
		v, errV := parse(value)
		m, errM := parse(max)
		if errV != nil || errM != nil {
			return invalid("%s cannot be compared with its limit", name)
		}
		if v > m {
			return fmt.Errorf("%w: %s %s is more than %s", ErrOverLimit, name, value, max)
		}
		return nil
	}
	for _, err := range []error{
		over("cpu_request", s.CPURequest, limits.MaxCPU, resources.ParseCPU),
		over("cpu", s.CPULimit, limits.MaxCPU, resources.ParseCPU),
		over("memory", s.MemoryLimit, limits.MaxMemory, resources.ParseMemory),
		over("storage_request", s.StorageRequest, limits.MaxStorage, resources.ParseMemory),
		over("storage", s.StorageLimit, limits.MaxStorage, resources.ParseMemory),
	} {
		if err != nil {
			return err
		}
	}
	if limits.MaxTimeoutSeconds > 0 && s.TimeoutSeconds > limits.MaxTimeoutSeconds {
		return fmt.Errorf("%w: timeout %ds is more than %ds", ErrOverLimit, s.TimeoutSeconds, limits.MaxTimeoutSeconds)
	}
	return nil
}

// Apply sets the shifts on job and records them in job.Shifts, merged with
// the shifts the job already has. It changes no other field: the authority of
// the job stays the same. It refuses a result where a request is more than
// its limit.
func Apply(job *models.Job, s models.RunShifts) error {
	if job == nil || s.IsZero() {
		return nil
	}
	cpuRequest, cpuLimit := job.ResourceCPURequest, job.ResourceCPULimit
	storageRequest, storageLimit := job.ResourceStorageRequest, job.ResourceStorageLimit
	if s.CPURequest != "" {
		cpuRequest = s.CPURequest
	}
	if s.CPULimit != "" {
		cpuLimit = s.CPULimit
	}
	if s.StorageRequest != "" {
		storageRequest = s.StorageRequest
	}
	if s.StorageLimit != "" {
		storageLimit = s.StorageLimit
	}
	if err := checkOrder(cpuRequest, cpuLimit, storageRequest, storageLimit); err != nil {
		return err
	}
	job.ResourceCPURequest, job.ResourceCPULimit = cpuRequest, cpuLimit
	job.ResourceStorageRequest, job.ResourceStorageLimit = storageRequest, storageLimit
	if s.MemoryLimit != "" {
		job.ResourceMemoryLimit = s.MemoryLimit
	}
	if s.TimeoutSeconds > 0 {
		job.TimeoutSeconds = s.TimeoutSeconds
	}
	merged := s
	if job.Shifts != nil {
		merged = job.Shifts.Merge(s)
	}
	job.Shifts = merged.Ptr()
	return nil
}

// Describe returns a short text of the shifts, for messages and audit, for
// example "memory=8Gi timeout=2h0m0s".
func Describe(s models.RunShifts) string {
	var parts []string
	add := func(key, value string) {
		if value != "" {
			parts = append(parts, key+"="+value)
		}
	}
	add("cpu", s.CPULimit)
	add("cpu_request", s.CPURequest)
	add("memory", s.MemoryLimit)
	add("storage", s.StorageLimit)
	add("storage_request", s.StorageRequest)
	if s.TimeoutSeconds > 0 {
		add("timeout", (time.Duration(s.TimeoutSeconds) * time.Second).String())
	}
	return strings.Join(parts, " ")
}

// ToMap returns s as an audit detail object with the canonical keys.
func ToMap(s models.RunShifts) map[string]any {
	out := map[string]any{}
	if s.CPULimit != "" {
		out["cpu"] = s.CPULimit
	}
	if s.CPURequest != "" {
		out["cpu_request"] = s.CPURequest
	}
	if s.MemoryLimit != "" {
		out["memory"] = s.MemoryLimit
	}
	if s.StorageLimit != "" {
		out["storage"] = s.StorageLimit
	}
	if s.StorageRequest != "" {
		out["storage_request"] = s.StorageRequest
	}
	if s.TimeoutSeconds > 0 {
		out["timeout_seconds"] = s.TimeoutSeconds
	}
	return out
}

// ParseRequestBody reads the JSON body of a REST retry or re-run request. An
// empty body is no shifts. The body may have only a "shifts" object: any
// other field is a change that only a new run can make, and is refused with
// ErrRefused.
func ParseRequestBody(body []byte) (models.RunShifts, error) {
	if len(strings.TrimSpace(string(body))) == 0 {
		return models.RunShifts{}, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return models.RunShifts{}, invalid("the request body must be a JSON object: %v", err)
	}
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if key != "shifts" {
			return models.RunShifts{}, fmt.Errorf("the request changes %q: %w", key, ErrRefused)
		}
	}
	shifts, ok := raw["shifts"]
	if !ok || string(shifts) == "null" {
		return models.RunShifts{}, nil
	}
	var object map[string]any
	if err := json.Unmarshal(shifts, &object); err != nil {
		return models.RunShifts{}, invalid("shifts must be a JSON object: %v", err)
	}
	return ParseMap(object)
}
