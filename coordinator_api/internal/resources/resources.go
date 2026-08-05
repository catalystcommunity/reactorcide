// Package resources parses and validates per-job compute resource
// specifications: CPU request/limit and memory limit (memory is limit-only
// -- there is no memory request in reactorcide).
//
// Values are Kubernetes-flavored quantity strings ("1", "2", "1.5", "500m"
// for CPU; "4Gi", "4GB", "4G", "512Mi" for memory), but parsed by our OWN
// small grammar (ParseCPU / ParseMemory below) rather than
// k8s.io/apimachinery/pkg/api/resource -- fewer deps, and it additionally
// accepts decimal-with-"B" memory suffixes (GB/MB/...) that the k8s parser
// rejects.
//
// This package only parses/validates and applies defaults. Storing the
// values on a Job and plumbing them into a submitted job spec is owned by
// the submit path (job_handler.go et al.), which imports ParseResources and
// ApplyDefaults from here. Runners (Docker, containerd, Kubernetes) use
// CPUMillicores/MemoryBytes (or ParseCPU/ParseMemory directly) to turn the
// stored strings into the numeric values their APIs need.
package resources

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Defaults applied when a value is unspecified. These match the DB column
// defaults in coredb/migrations/000021_job_resources.sql -- a row inserted
// without explicit resources already lands on these values at the DB layer;
// ApplyDefaults exists for callers (runners, tests) that need the resolved
// values before/without a DB round trip.
const (
	DefaultCPURequest  = "1"
	DefaultCPULimit    = "2"
	DefaultMemoryLimit = "4Gi"
)

// binaryMemorySuffixes maps the 1024-based (IEC) two-letter suffixes to
// their byte multiplier. Case-sensitive, matching Kubernetes: "Ki", "Mi",
// "Gi", "Ti", "Pi".
var binaryMemorySuffixes = map[string]int64{
	"Ki": 1 << 10,
	"Mi": 1 << 20,
	"Gi": 1 << 30,
	"Ti": 1 << 40,
	"Pi": 1 << 50,
}

// decimalMemorySuffixes maps the 1000-based (SI) unit letter to its byte
// multiplier. The letter itself is case-sensitive ("K", "M", "G", "T", "P");
// an optional trailing "B"/"b" (case-insensitive) may follow it, so "4G" and
// "4GB"/"4Gb" are all equivalent.
var decimalMemorySuffixes = map[byte]int64{
	'K': 1_000,
	'M': 1_000_000,
	'G': 1_000_000_000,
	'T': 1_000_000_000_000,
	'P': 1_000_000_000_000_000,
}

// ParseCPU parses a CPU quantity string into millicores. Accepted forms:
//   - a decimal cores value: "1", "2", "1.5", "0.5" -> cores * 1000
//   - a milli value: "500m" -> 500 (the number before "m" must be an
//     integer number of millicores)
//
// Negative values and anything else are rejected with a descriptive error.
// An empty string is also rejected -- callers treat "" as "unspecified"
// and simply skip calling ParseCPU (see ParseResources/ApplyDefaults).
func ParseCPU(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("cpu: quantity must not be empty")
	}

	if strings.HasSuffix(s, "m") {
		numPart := strings.TrimSuffix(s, "m")
		if numPart == "" {
			return 0, fmt.Errorf("cpu: invalid quantity %q", s)
		}
		millicores, err := strconv.ParseInt(numPart, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("cpu: invalid quantity %q: %w", s, err)
		}
		if millicores < 0 {
			return 0, fmt.Errorf("cpu: quantity %q must not be negative", s)
		}
		return millicores, nil
	}

	cores, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("cpu: invalid quantity %q: %w", s, err)
	}
	if cores < 0 {
		return 0, fmt.Errorf("cpu: quantity %q must not be negative", s)
	}
	return int64(math.Round(cores * 1000)), nil
}

// ParseMemory parses a memory quantity string into bytes. Accepted forms:
//   - binary (1024-based) suffixes: Ki, Mi, Gi, Ti, Pi -- e.g. "4Gi" ->
//     4*1024^3, "512Mi" -> 512*1024^2
//   - decimal (1000-based) suffixes: K/KB, M/MB, G/GB, T/TB, P/PB -- e.g.
//     "4G" == "4GB" -> 4*1000^3 (the trailing "B"/"b" is optional and
//     case-insensitive; the unit letter itself is not)
//   - no suffix: a plain byte count, e.g. "1048576"
//
// Negative values and anything else are rejected with a descriptive error.
// An empty string is also rejected -- callers treat "" as "unspecified" and
// simply skip calling ParseMemory (see ParseResources/ApplyDefaults).
func ParseMemory(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("memory: quantity must not be empty")
	}

	numPart, multiplier, err := splitMemorySuffix(s)
	if err != nil {
		return 0, err
	}
	if numPart == "" {
		return 0, fmt.Errorf("memory: invalid quantity %q", s)
	}

	value, err := strconv.ParseFloat(numPart, 64)
	if err != nil {
		return 0, fmt.Errorf("memory: invalid quantity %q: %w", s, err)
	}
	if value < 0 {
		return 0, fmt.Errorf("memory: quantity %q must not be negative", s)
	}

	return int64(math.Round(value * float64(multiplier))), nil
}

// splitMemorySuffix splits s into its leading numeric part and the byte
// multiplier implied by its trailing unit suffix. A quantity with no
// recognized suffix is treated as a plain byte count (multiplier 1).
func splitMemorySuffix(s string) (numPart string, multiplier int64, err error) {
	if len(s) >= 2 {
		if m, ok := binaryMemorySuffixes[s[len(s)-2:]]; ok {
			return s[:len(s)-2], m, nil
		}
	}

	rest := s
	if last := rest[len(rest)-1]; last == 'B' || last == 'b' {
		rest = rest[:len(rest)-1]
	}
	if rest != "" {
		if m, ok := decimalMemorySuffixes[rest[len(rest)-1]]; ok {
			return rest[:len(rest)-1], m, nil
		}
	}
	if rest != s {
		// Had a trailing B/b but no recognized unit letter before it (e.g.
		// "5B") -- not part of the documented grammar.
		return "", 0, fmt.Errorf("memory: invalid suffix in quantity %q", s)
	}

	// No suffix recognized at all -- treat the whole string as a byte count.
	return s, 1, nil
}

// CPUMillicores parses a CPU quantity string the same way ParseCPU does.
// It exists so runner code (Docker/containerd/Kubernetes) reads naturally
// at the call site without reimplementing quantity parsing.
func CPUMillicores(s string) (int64, error) {
	return ParseCPU(s)
}

// MemoryBytes parses a memory quantity string the same way ParseMemory
// does. It exists so runner code reads naturally at the call site without
// reimplementing quantity parsing.
func MemoryBytes(s string) (int64, error) {
	return ParseMemory(s)
}

// ParseResources reads a job spec `resources` block shaped like:
//
//	resources:
//	  cpu:
//	    request: "1"
//	    limit: "2"
//	  memory:
//	    limit: "4Gi"
//
// (raw is the decoded YAML/JSON map for the `resources` key; a nil map is
// valid and simply yields all-empty results). Each provided value is
// validated with ParseCPU/ParseMemory. A field left out of raw is returned
// as "" ("unspecified" -- callers apply ApplyDefaults, or for the submit
// path, the DB column default applies automatically). memory.request is
// rejected: memory is limit-only in reactorcide, mirroring the DB schema
// and JobConfig/runner shape.
func ParseResources(raw map[string]any) (cpuRequest, cpuLimit, memoryLimit string, err error) {
	if raw == nil {
		return "", "", "", nil
	}

	for key := range raw {
		if key != "cpu" && key != "memory" {
			return "", "", "", fmt.Errorf("resources: unknown field %q (only \"cpu\" and \"memory\" are supported)", key)
		}
	}

	if cpuRaw, ok := raw["cpu"]; ok {
		cpuMap, ok := cpuRaw.(map[string]any)
		if !ok {
			return "", "", "", fmt.Errorf("resources.cpu must be a map with \"request\"/\"limit\" keys")
		}
		for key := range cpuMap {
			if key != "request" && key != "limit" {
				return "", "", "", fmt.Errorf("resources.cpu: unknown field %q (only \"request\" and \"limit\" are supported)", key)
			}
		}
		if v, ok := cpuMap["request"]; ok {
			if cpuRequest, err = parseQuantityField("resources.cpu.request", v, ParseCPU); err != nil {
				return "", "", "", err
			}
		}
		if v, ok := cpuMap["limit"]; ok {
			if cpuLimit, err = parseQuantityField("resources.cpu.limit", v, ParseCPU); err != nil {
				return "", "", "", err
			}
		}
	}

	if memRaw, ok := raw["memory"]; ok {
		memMap, ok := memRaw.(map[string]any)
		if !ok {
			return "", "", "", fmt.Errorf("resources.memory must be a map with a \"limit\" key")
		}
		if _, ok := memMap["request"]; ok {
			return "", "", "", fmt.Errorf("resources.memory.request is not supported: memory is limit-only")
		}
		for key := range memMap {
			if key != "limit" {
				return "", "", "", fmt.Errorf("resources.memory: unknown field %q (only \"limit\" is supported)", key)
			}
		}
		if v, ok := memMap["limit"]; ok {
			if memoryLimit, err = parseQuantityField("resources.memory.limit", v, ParseMemory); err != nil {
				return "", "", "", err
			}
		}
	}

	return cpuRequest, cpuLimit, memoryLimit, nil
}

// parseQuantityField normalizes v (a YAML/JSON scalar -- string, int, or
// float, since bare quantities like `2` decode as numbers) to a string and
// validates it with the given parser (ParseCPU or ParseMemory), returning
// the original (trimmed) string form on success so storage/round-tripping
// preserves what the user wrote (e.g. "500m" rather than a re-serialized
// value).
func parseQuantityField(name string, v any, parse func(string) (int64, error)) (string, error) {
	var s string
	switch n := v.(type) {
	case string:
		s = strings.TrimSpace(n)
	case int:
		s = strconv.Itoa(n)
	case int32:
		s = strconv.FormatInt(int64(n), 10)
	case int64:
		s = strconv.FormatInt(n, 10)
	case float32:
		s = strconv.FormatFloat(float64(n), 'f', -1, 32)
	case float64:
		s = strconv.FormatFloat(n, 'f', -1, 64)
	default:
		return "", fmt.Errorf("%s must be a string or number quantity, got %T", name, v)
	}
	if s == "" {
		return "", fmt.Errorf("%s must not be empty", name)
	}
	if _, err := parse(s); err != nil {
		return "", fmt.Errorf("%s: invalid quantity %q: %w", name, s, err)
	}
	return s, nil
}

// ApplyDefaults fills any empty resource value with the reactorcide
// defaults (cpu.request=1, cpu.limit=2, memory.limit=4Gi), for callers that
// need fully-resolved values without a DB round trip (e.g. runners, tests).
// The submit path relies on the DB column defaults for the same effect when
// persisting a Job.
func ApplyDefaults(cpuRequest, cpuLimit, memoryLimit string) (string, string, string) {
	if cpuRequest == "" {
		cpuRequest = DefaultCPURequest
	}
	if cpuLimit == "" {
		cpuLimit = DefaultCPULimit
	}
	if memoryLimit == "" {
		memoryLimit = DefaultMemoryLimit
	}
	return cpuRequest, cpuLimit, memoryLimit
}
