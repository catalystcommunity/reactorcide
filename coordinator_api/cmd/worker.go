package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/coordinatorworker"
	"github.com/google/uuid"
	"github.com/urfave/cli/v2"
)

// customCharacteristicEnvPrefix is the env var prefix a worker scans for
// custom characteristics: REACTORCIDE_WORKER_CUSTOM_<SUFFIX> becomes custom
// key strings.ToLower(<SUFFIX>). Env var names are restricted to
// [A-Z0-9_], so a characteristic key containing "." or "-" is not reachable
// this way -- use --custom for those.
const customCharacteristicEnvPrefix = "REACTORCIDE_WORKER_CUSTOM_"

// WorkerCommand runs a coordinator-mediated worker (WORKERS_PLAN.md Wave 3,
// P3): it authenticates to a coordinator over CSIL-RPC and pulls work
// through it (Register -> RequestJob -> run -> AppendLogs -> ReportResult ->
// Heartbeat, see internal/coordinatorworker). It has no corndogs, Postgres,
// or object-store dependency of its own -- the coordinator is the only
// thing that talks to those. The legacy direct-corndogs worker path (which
// polled corndogs/Postgres directly) has been removed; this is the only
// worker mode.
var WorkerCommand = &cli.Command{
	Name:  "worker",
	Usage: "Run a coordinator-mediated job worker",
	Flags: workerFlags,
	Action: func(ctx *cli.Context) error {
		return RunWorker(ctx)
	},
}

var workerFlags = []cli.Flag{
	&cli.StringFlag{
		Name:    "coordinator-url",
		Usage:   "Base URL of the coordinator this worker registers with, e.g. https://coordinator.example.com (required)",
		EnvVars: []string{"REACTORCIDE_COORDINATOR_URL"},
	},
	&cli.StringFlag{
		Name: "enrollment-token-file",
		Usage: "Path to a file containing the worker enrollment token (secret). " +
			"If unset, the token is read from REACTORCIDE_WORKER_ENROLLMENT_TOKEN instead. " +
			"The token value itself is never logged or echoed, including in --help.",
	},
	&cli.StringFlag{
		Name:    "data-dir",
		Value:   defaultWorkerDataDir(),
		Usage:   "Directory used to persist this worker's stable identity (worker_key)",
		EnvVars: []string{"REACTORCIDE_WORKER_DATA_DIR"},
	},
	&cli.StringFlag{
		Name:  "worker-key-file",
		Usage: "Path to the persisted worker_key file (default: <data-dir>/worker_key). Generated on first run if absent, reused thereafter.",
	},
	&cli.StringFlag{
		Name:    "hostname",
		Usage:   "Identifying hostname sent on Register (default: the OS-reported hostname)",
		EnvVars: []string{"REACTORCIDE_WORKER_HOSTNAME"},
	},
	&cli.StringFlag{
		Name: "os",
		Usage: "Worker \"os\" characteristic (default: auto-detected from the running binary's " +
			"OS -- linux/macos/windows). An explicit override is taken verbatim, no normalization.",
		EnvVars: []string{"REACTORCIDE_WORKER_OS"},
	},
	&cli.StringFlag{
		Name: "arch",
		Usage: "Worker \"arch\" characteristic (default: auto-detected, e.g. amd64/arm64). " +
			"An explicit override is taken verbatim, no normalization.",
		EnvVars: []string{"REACTORCIDE_WORKER_ARCH"},
	},
	&cli.StringSliceFlag{
		Name: "custom",
		Usage: "Custom worker characteristic in key=value form (repeatable). value may be a " +
			"comma-separated list; type (int, bool, or string; scalar or list) is inferred " +
			"from the value's first comma-separated element -- see docs/workers.md's " +
			"\"Custom characteristics\" section. Also settable via " +
			"REACTORCIDE_WORKER_CUSTOM_<KEY> env vars. os/arch are reserved and cannot be " +
			"set here; a key set by more than one source is a startup error.",
	},
	&cli.StringFlag{
		Name:    "worker-version",
		Usage:   "Optional worker build/version identifier sent on Register",
		EnvVars: []string{"REACTORCIDE_WORKER_VERSION"},
	},
	&cli.StringFlag{
		Name:    "container-runtime",
		Aliases: []string{"r"},
		Value:   "auto",
		Usage:   "Container runtime backend used to run leased jobs: docker, containerd, kubernetes, vm (darwin/windows only), or auto",
		EnvVars: []string{"REACTORCIDE_CONTAINER_RUNTIME"},
	},
	&cli.IntFlag{
		Name:    "concurrency",
		Aliases: []string{"c"},
		Value:   1,
		Usage:   "Number of leases this worker runs concurrently",
		EnvVars: []string{"REACTORCIDE_WORKER_CONCURRENCY"},
	},
	&cli.StringFlag{
		Name:    "workspace-dir",
		Usage:   "Base directory for each job's ephemeral workspace (bind-mounted as /job). When the worker runs in a container and drives a host container runtime, set this to a path bind-mounted identically on host and in the worker (e.g. /tmp/reactorcide-jobs); empty uses the OS temp dir.",
		EnvVars: []string{"REACTORCIDE_WORKER_WORKSPACE_DIR"},
	},
	&cli.DurationFlag{
		Name:    "metrics-interval",
		Value:   2 * time.Second,
		Usage:   "Interval between job CPU and memory samples",
		EnvVars: []string{"REACTORCIDE_WORKER_METRICS_INTERVAL"},
	},
	&cli.DurationFlag{
		Name:    "telemetry-send-interval",
		Value:   10 * time.Second,
		Usage:   "Maximum interval between telemetry batch uploads",
		EnvVars: []string{"REACTORCIDE_WORKER_TELEMETRY_SEND_INTERVAL"},
	},
	&cli.StringSliceFlag{
		Name:    "vm-image-prefetch",
		Usage:   "OCI VM image reference to pull before worker registration (repeatable)",
		EnvVars: []string{"REACTORCIDE_VM_IMAGE_PREFETCH"},
	},
	&cli.DurationFlag{
		Name:    "vm-image-max-unused",
		Value:   30 * 24 * time.Hour,
		Usage:   "Remove cached VM images that have not been used for this duration",
		EnvVars: []string{"REACTORCIDE_VM_IMAGE_MAX_UNUSED"},
	},
	&cli.DurationFlag{
		Name:    "vm-image-prune-interval",
		Value:   24 * time.Hour,
		Usage:   "Interval between VM image cache retention checks; zero disables periodic checks",
		EnvVars: []string{"REACTORCIDE_VM_IMAGE_PRUNE_INTERVAL"},
	},
}

// RunWorker wires CLI flags/env into a coordinatorworker.Config and blocks
// running the coordinator-mediated run loop until SIGINT/SIGTERM.
func RunWorker(ctx *cli.Context) error {
	coordinatorURL := strings.TrimSpace(ctx.String("coordinator-url"))
	if coordinatorURL == "" {
		return errors.New("worker: --coordinator-url (or REACTORCIDE_COORDINATOR_URL) is required")
	}

	enrollmentToken, err := loadWorkerEnrollmentToken(ctx.String("enrollment-token-file"))
	if err != nil {
		return err
	}

	workerKeyFile := strings.TrimSpace(ctx.String("worker-key-file"))
	if workerKeyFile == "" {
		workerKeyFile = filepath.Join(ctx.String("data-dir"), "worker_key")
	}
	workerKey, err := loadOrCreateWorkerKey(workerKeyFile)
	if err != nil {
		return fmt.Errorf("worker: %w", err)
	}

	hostname := strings.TrimSpace(ctx.String("hostname"))
	if hostname == "" {
		if h, err := os.Hostname(); err == nil {
			hostname = h
		}
	}

	workerOS := strings.TrimSpace(ctx.String("os"))
	if workerOS == "" {
		workerOS = detectWorkerOS()
	}
	workerArch := strings.TrimSpace(ctx.String("arch"))
	if workerArch == "" {
		workerArch = runtime.GOARCH
	}

	custom, err := gatherCustomCharacteristics(ctx.StringSlice("custom"), os.Environ())
	if err != nil {
		return fmt.Errorf("worker: %w", err)
	}

	containerRuntime := ctx.String("container-runtime")
	concurrency := ctx.Int("concurrency")

	logging.Log.WithFields(map[string]interface{}{
		"coordinator_url":   coordinatorURL,
		"hostname":          hostname,
		"os":                workerOS,
		"arch":              workerArch,
		"container_runtime": containerRuntime,
		"concurrency":       concurrency,
		"worker_key_file":   workerKeyFile,
	}).Info("Starting coordinator-mediated worker")

	runCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg := coordinatorworker.Config{
		CoordinatorURL:        coordinatorURL,
		EnrollmentToken:       enrollmentToken,
		WorkerKey:             workerKey,
		Hostname:              hostname,
		OS:                    workerOS,
		Arch:                  workerArch,
		Custom:                custom,
		WorkerVersion:         ctx.String("worker-version"),
		ContainerRuntime:      containerRuntime,
		Concurrency:           concurrency,
		WorkspaceRoot:         strings.TrimSpace(ctx.String("workspace-dir")),
		DataDir:               strings.TrimSpace(ctx.String("data-dir")),
		MetricsInterval:       ctx.Duration("metrics-interval"),
		TelemetrySendInterval: ctx.Duration("telemetry-send-interval"),
		VMImagePrefetch:       ctx.StringSlice("vm-image-prefetch"),
		VMImageMaxUnused:      ctx.Duration("vm-image-max-unused"),
		VMImagePruneInterval:  ctx.Duration("vm-image-prune-interval"),
	}

	if err := coordinatorworker.Run(runCtx, cfg); err != nil && !errors.Is(err, context.Canceled) {
		logging.Log.WithError(err).Error("Worker stopped with error")
		return err
	}
	logging.Log.Info("Worker stopped")
	return nil
}

// loadWorkerEnrollmentToken resolves the enrollment token from a file (if
// --enrollment-token-file is set) or REACTORCIDE_WORKER_ENROLLMENT_TOKEN
// otherwise. The token is read directly from disk/env -- never through a
// cli.Flag -- and is never included in any returned error or log line, so
// it cannot leak into --help, debug output, or command echoes.
func loadWorkerEnrollmentToken(tokenFile string) (string, error) {
	tokenFile = strings.TrimSpace(tokenFile)
	if tokenFile != "" {
		data, err := os.ReadFile(tokenFile)
		if err != nil {
			return "", fmt.Errorf("worker: reading --enrollment-token-file %q: %w", tokenFile, err)
		}
		token := strings.TrimSpace(string(data))
		if token == "" {
			return "", fmt.Errorf("worker: --enrollment-token-file %q is empty", tokenFile)
		}
		return token, nil
	}

	if token := strings.TrimSpace(os.Getenv("REACTORCIDE_WORKER_ENROLLMENT_TOKEN")); token != "" {
		return token, nil
	}

	return "", errors.New("worker: an enrollment token is required via --enrollment-token-file or REACTORCIDE_WORKER_ENROLLMENT_TOKEN")
}

// loadOrCreateWorkerKey reads the stable worker_key persisted at path,
// generating and persisting a new UUID there on first run. worker_key is
// not a secret (it is a stable, worker-provided identifier the coordinator
// upserts a workers row by, akin to a host GUID) but the file is still
// written with owner-only permissions.
func loadOrCreateWorkerKey(path string) (string, error) {
	if data, err := os.ReadFile(path); err == nil {
		if key := strings.TrimSpace(string(data)); key != "" {
			return key, nil
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("reading worker key file %q: %w", path, err)
	}

	key := uuid.NewString()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("creating worker key directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(key+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("persisting worker key file %q: %w", path, err)
	}
	logging.Log.WithField("worker_key_file", path).Info("Generated new worker_key")
	return key, nil
}

// detectWorkerOS maps runtime.GOOS to the worker "os" characteristic's
// default value: linux and windows pass through verbatim, darwin becomes
// "macos" (WORKERS_PLAN.md's worker characteristics section). An operator
// override via --os/REACTORCIDE_WORKER_OS is never normalized.
func detectWorkerOS() string {
	if runtime.GOOS == "darwin" {
		return "macos"
	}
	return runtime.GOOS
}

// gatherCustomCharacteristics builds a worker's custom characteristics from
// both operator-facing input surfaces -- repeated --custom key=value flags
// and REACTORCIDE_WORKER_CUSTOM_<SUFFIX> env vars (key = strings.ToLower of
// the suffix) -- parsing every value through
// characteristics.ParseCustomValue for int/bool/string and scalar/list type
// inference. environ is the process environment in os.Environ() form
// (injected as a parameter so this is testable without mutating real
// process env).
//
// This is intentionally loud on any misconfiguration rather than silently
// dropping or overwriting: a malformed value, a key supplied by more than
// one source (or twice within one source), or an attempt to set the
// reserved "os"/"arch" keys as customs all fail the worker at startup with
// an error naming the offending key/source, before any RPC to the
// coordinator is attempted.
func gatherCustomCharacteristics(flagValues []string, environ []string) ([]characteristics.KV, error) {
	out := make([]characteristics.KV, 0, len(flagValues)+len(environ))
	origins := make(map[string]string, len(flagValues)+len(environ))

	add := func(key, origin string, val characteristics.Value) error {
		if key == "os" || key == "arch" {
			return fmt.Errorf(
				"custom characteristic key %q (from %s) is reserved for the worker's os/arch fields; "+
					"set it via --os/--arch or REACTORCIDE_WORKER_OS/REACTORCIDE_WORKER_ARCH instead",
				key, origin,
			)
		}
		if prevOrigin, dup := origins[key]; dup {
			return fmt.Errorf(
				"custom characteristic key %q is set more than once (from %s and %s); "+
					"each key may only be set once, across --custom flags and REACTORCIDE_WORKER_CUSTOM_* env vars combined",
				key, prevOrigin, origin,
			)
		}
		origins[key] = origin
		out = append(out, characteristics.KV{Key: key, Value: customValueToKV(val)})
		return nil
	}

	// Env vars first, in sorted-name order for deterministic error messages
	// (os.Environ() order is unspecified).
	envNames := make([]string, 0, len(environ))
	envByName := make(map[string]string, len(environ))
	for _, entry := range environ {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || !strings.HasPrefix(name, customCharacteristicEnvPrefix) {
			continue
		}
		envNames = append(envNames, name)
		envByName[name] = value
	}
	sort.Strings(envNames)

	for _, name := range envNames {
		suffix := strings.TrimPrefix(name, customCharacteristicEnvPrefix)
		if suffix == "" {
			return nil, fmt.Errorf("env var %s has no key after the %s prefix", name, customCharacteristicEnvPrefix)
		}
		key := strings.ToLower(suffix)
		val, err := characteristics.ParseCustomValue(envByName[name])
		if err != nil {
			return nil, fmt.Errorf("env var %s: %w", name, err)
		}
		if err := add(key, fmt.Sprintf("env var %s", name), val); err != nil {
			return nil, err
		}
	}

	for _, kv := range flagValues {
		key, value, ok := strings.Cut(kv, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid --custom value %q: expected key=value", kv)
		}
		val, err := characteristics.ParseCustomValue(value)
		if err != nil {
			return nil, fmt.Errorf("--custom %s: %w", key, err)
		}
		if err := add(key, fmt.Sprintf("--custom %s", key), val); err != nil {
			return nil, err
		}
	}

	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// customValueToKV unwraps a characteristics.Value back into the plain Go
// type characteristics.KV.Value / coordinatorworker's wire encoding expect
// (string/int64/bool or a homogeneous slice of one of those) -- the closed
// Value implementations in internal/characteristics are distinct named
// types (e.g. StringValue is not string), so this conversion is required
// rather than assigning the Value directly.
func customValueToKV(v characteristics.Value) any {
	switch t := v.(type) {
	case characteristics.StringValue:
		return string(t)
	case characteristics.IntValue:
		return int64(t)
	case characteristics.BoolValue:
		return bool(t)
	case characteristics.StringListValue:
		return []string(t)
	case characteristics.IntListValue:
		return []int64(t)
	case characteristics.BoolListValue:
		return []bool(t)
	default:
		// Unreachable: characteristics.Value is a closed set and
		// ParseCustomValue only ever returns one of the above.
		panic(fmt.Sprintf("cmd: unhandled characteristics.Value implementation %T", v))
	}
}

// defaultWorkerDataDir returns ~/.config/reactorcide, falling back to a
// relative directory if the home directory can't be resolved.
func defaultWorkerDataDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".config", "reactorcide")
	}
	return ".reactorcide"
}
