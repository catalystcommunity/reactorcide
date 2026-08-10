package worker

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobtelemetry"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker/vmrunner"
	"oras.land/oras-go/v2/registry/remote/credentials"
)

// isVMBackendImplemented reports whether the "vm" backend has a real
// VMLifecycle on the running OS. It's a plain runtime.GOOS check rather
// than a build-tagged file because the answer ("darwin or windows") is
// itself cross-platform knowledge -- vmrunner.newVMLifecycle (the
// build-tagged piece) is what actually enforces this at construction time;
// this just lets GetSupportedBackends/IsBackendImplemented report it
// without constructing a VMRunner.
func isVMBackendImplemented() bool {
	return runtime.GOOS == "darwin" || runtime.GOOS == "windows"
}

// VMConfig holds operator-level configuration for the "vm" JobRunner
// backend: which ImageSource to use and where its images live/cache
// (LocalImageSource's pre-placed ImageDir, or the OCI-backed
// OCIImageSource) and the SSH credentials used to reach a booted guest.
// Mirrors BuilderConfig's env-var-driven, zero-value-is-valid shape.
type VMConfig struct {
	// ImageSource selects the ImageSource implementation: "local" (default,
	// vmrunner.LocalImageSource) or "oci" (vmrunner.OCIImageSource, pulling
	// base images from an OCI registry).
	ImageSource string

	// ImageDir is the directory relative image references resolve under
	// when ImageSource is "local" (see vmrunner.LocalImageSource).
	ImageDir string

	// OCIImageCacheDir is the local content-addressed cache directory
	// vmrunner.OCIImageSource pulls base images into when ImageSource is
	// "oci" (see vmrunner.NewOCIImageSource).
	OCIImageCacheDir string

	// OCIRegistryAuthFile is a Docker-compatible credential file. One file can
	// hold credentials for several registry hosts.
	OCIRegistryAuthFile string

	// OCIPlainHTTPRegistries lists registry hosts (as they appear in an
	// image reference, e.g. "registry.internal:5000") to reach over plain
	// HTTP instead of HTTPS when ImageSource is "oci" -- for a local/dev
	// registry that doesn't terminate TLS. Loopback hosts are already
	// treated as plain HTTP without being listed here (see
	// vmrunner.WithPlainHTTPRegistries).
	OCIPlainHTTPRegistries []string

	// SSHUser is the guest OS account SSHTransport authenticates as.
	SSHUser string

	// SSHPassword authenticates via SSH password auth when set.
	SSHPassword string

	// SSHPrivateKeyPEM authenticates via SSH public-key auth when set,
	// taking precedence over SSHPassword. Loaded from the file at
	// REACTORCIDE_VM_SSH_PRIVATE_KEY_FILE, never itself taken directly from
	// an env var so its contents can't leak into a printed environment.
	SSHPrivateKeyPEM []byte

	// SSHHostPublicKey pins the guest SSH server key when configured.
	SSHHostPublicKey []byte

	MetricsDir      string
	MetricsInterval time.Duration
}

// LoadVMConfig resolves VMConfig from environment variables. This is the
// single source of truth for "vm" backend operator knobs, following
// LoadBuilderConfig's pattern.
//
// Env vars:
//   - REACTORCIDE_VM_IMAGE_SOURCE              (default: "local"; "oci"
//     pulls base images from an OCI registry via vmrunner.OCIImageSource)
//   - REACTORCIDE_VM_IMAGE_DIR                 (default: current working
//     directory; only used when REACTORCIDE_VM_IMAGE_SOURCE is "local")
//   - REACTORCIDE_VM_IMAGE_CACHE_DIR           (default:
//     vmrunner.DefaultImageCacheDir(), i.e. ~/.cache/reactorcide/vm-images;
//     only used when REACTORCIDE_VM_IMAGE_SOURCE is "oci")
//   - REACTORCIDE_VM_REGISTRY_AUTH_FILE         (default:
//     ~/.config/reactorcide/oci-auth.json; Docker-compatible credentials for
//     several registry hosts)
//   - REACTORCIDE_VM_IMAGE_REGISTRY_PLAIN_HTTP (optional, comma-separated
//     registry hosts to reach over plain HTTP instead of HTTPS; only used
//     when REACTORCIDE_VM_IMAGE_SOURCE is "oci")
//   - REACTORCIDE_VM_SSH_USER              (default: "reactorcide")
//   - REACTORCIDE_VM_SSH_PASSWORD          (optional, no default)
//   - REACTORCIDE_VM_SSH_PRIVATE_KEY_FILE  (optional; path to a PEM private
//     key file, read at load time -- the key material never passes through
//     an env var directly)
//   - REACTORCIDE_VM_SSH_HOST_KEY_FILE     (optional; authorized_keys-format
//     guest SSH host public key)
//   - REACTORCIDE_VM_METRICS_DIR            (optional local JSONL debug output)
//   - REACTORCIDE_VM_METRICS_INTERVAL       (default: 5s)
func LoadVMConfig() (VMConfig, error) {
	imageSource := os.Getenv("REACTORCIDE_VM_IMAGE_SOURCE")
	if imageSource == "" {
		imageSource = "local"
	}

	imageDir := os.Getenv("REACTORCIDE_VM_IMAGE_DIR")
	if imageDir == "" {
		imageDir = "."
	}

	ociImageCacheDir := os.Getenv("REACTORCIDE_VM_IMAGE_CACHE_DIR")
	if ociImageCacheDir == "" {
		ociImageCacheDir = vmrunner.DefaultImageCacheDir()
	}
	ociRegistryAuthFile := os.Getenv("REACTORCIDE_VM_REGISTRY_AUTH_FILE")
	if ociRegistryAuthFile == "" {
		ociRegistryAuthFile = vmrunner.DefaultRegistryAuthFile()
	}

	var ociPlainHTTPRegistries []string
	if raw := os.Getenv("REACTORCIDE_VM_IMAGE_REGISTRY_PLAIN_HTTP"); raw != "" {
		for _, h := range strings.Split(raw, ",") {
			if h = strings.TrimSpace(h); h != "" {
				ociPlainHTTPRegistries = append(ociPlainHTTPRegistries, h)
			}
		}
	}

	sshUser := os.Getenv("REACTORCIDE_VM_SSH_USER")
	if sshUser == "" {
		sshUser = "reactorcide"
	}
	metricsDir := os.Getenv("REACTORCIDE_VM_METRICS_DIR")
	metricsInterval := 5 * time.Second
	if raw := strings.TrimSpace(os.Getenv("REACTORCIDE_VM_METRICS_INTERVAL")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			return VMConfig{}, fmt.Errorf("invalid REACTORCIDE_VM_METRICS_INTERVAL %q", raw)
		}
		metricsInterval = parsed
	}

	cfg := VMConfig{
		ImageSource:            imageSource,
		ImageDir:               imageDir,
		OCIImageCacheDir:       ociImageCacheDir,
		OCIRegistryAuthFile:    ociRegistryAuthFile,
		OCIPlainHTTPRegistries: ociPlainHTTPRegistries,
		SSHUser:                sshUser,
		SSHPassword:            os.Getenv("REACTORCIDE_VM_SSH_PASSWORD"),
		MetricsDir:             metricsDir,
		MetricsInterval:        metricsInterval,
	}

	if keyFile := os.Getenv("REACTORCIDE_VM_SSH_PRIVATE_KEY_FILE"); keyFile != "" {
		key, err := os.ReadFile(keyFile)
		if err != nil {
			return VMConfig{}, fmt.Errorf("read REACTORCIDE_VM_SSH_PRIVATE_KEY_FILE: %w", err)
		}
		cfg.SSHPrivateKeyPEM = key
	}
	if hostKeyFile := os.Getenv("REACTORCIDE_VM_SSH_HOST_KEY_FILE"); hostKeyFile != "" {
		hostKey, err := os.ReadFile(hostKeyFile)
		if err != nil {
			return VMConfig{}, fmt.Errorf("read REACTORCIDE_VM_SSH_HOST_KEY_FILE: %w", err)
		}
		cfg.SSHHostPublicKey = hostKey
	}

	return cfg, nil
}

// buildVMImageSource constructs the vmrunner.ImageSource cfg.ImageSource
// selects. This is the one place that translates VMConfig's ImageSource knob
// into a concrete vmrunner.ImageSource, keeping LocalImageSource and
// OCIImageSource swappable purely via configuration.
func buildVMImageSource(cfg VMConfig) (vmrunner.ImageSource, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.ImageSource)) {
	case "", "local":
		return vmrunner.NewLocalImageSource(cfg.ImageDir), nil
	case "oci":
		credentialStore, err := credentials.NewStore(cfg.OCIRegistryAuthFile, credentials.StoreOptions{})
		if err != nil {
			return nil, fmt.Errorf("open VM registry credential file %q: %w", cfg.OCIRegistryAuthFile, err)
		}
		return vmrunner.NewOCIImageSource(
			cfg.OCIImageCacheDir,
			vmrunner.WithCredentialStore(credentialStore),
			vmrunner.WithPlainHTTPRegistries(cfg.OCIPlainHTTPRegistries...),
		)
	default:
		return nil, fmt.Errorf(
			"unsupported REACTORCIDE_VM_IMAGE_SOURCE %q (supported: local, oci)",
			cfg.ImageSource,
		)
	}
}

// newVMRunner builds the "vm" JobRunner backend: a vmrunner.VMRunner (this
// platform's VMLifecycle, the SSH GuestTransport, and a config-driven ImageSource,
// local or OCI per REACTORCIDE_VM_IMAGE_SOURCE) wrapped in vmRunnerAdapter
// so it satisfies JobRunner.
//
// vmrunner deliberately does not import this package (see its package doc
// for why: this package needs to import vmrunner to build the "vm"
// backend, so the reverse import would cycle), so this adapter is the one
// place that translates between worker.JobConfig and vmrunner.JobConfig.
func newVMRunner() (JobRunner, error) {
	cfg, err := LoadVMConfig()
	if err != nil {
		return nil, fmt.Errorf("load vm backend config: %w", err)
	}

	creds := vmrunner.GuestCreds{
		User:          cfg.SSHUser,
		Password:      cfg.SSHPassword,
		PrivateKeyPEM: cfg.SSHPrivateKeyPEM,
		HostPublicKey: cfg.SSHHostPublicKey,
	}

	images, err := buildVMImageSource(cfg)
	if err != nil {
		return nil, fmt.Errorf("build vm backend image source: %w", err)
	}

	inner, err := vmrunner.NewDefaultWithImages(images, creds, vmrunner.WithMetrics(cfg.MetricsDir, cfg.MetricsInterval))
	if err != nil {
		return nil, err
	}
	adapter := &vmRunnerAdapter{inner: inner, guestUser: cfg.SSHUser}
	if ociImages, ok := images.(*vmrunner.OCIImageSource); ok {
		adapter.ociImages = ociImages
	}
	return adapter, nil
}

// vmRunnerAdapter wraps a *vmrunner.VMRunner to satisfy JobRunner exactly,
// translating *JobConfig <-> *vmrunner.JobConfig at each call.
type vmRunnerAdapter struct {
	inner     *vmrunner.VMRunner
	guestUser string
	ociImages *vmrunner.OCIImageSource
}

func (a *vmRunnerAdapter) SpawnJob(ctx context.Context, config *JobConfig) (string, error) {
	return a.inner.SpawnJob(ctx, toVMJobConfig(config, a.guestUser, runtime.GOOS))
}

func (a *vmRunnerAdapter) StreamLogs(ctx context.Context, jobID string) (io.ReadCloser, io.ReadCloser, error) {
	return a.inner.StreamLogs(ctx, jobID)
}

func (a *vmRunnerAdapter) WaitForCompletion(ctx context.Context, jobID string) (int, error) {
	return a.inner.WaitForCompletion(ctx, jobID)
}

func (a *vmRunnerAdapter) Stop(ctx context.Context, jobID string, grace time.Duration) error {
	return a.inner.Stop(ctx, jobID, grace)
}

func (a *vmRunnerAdapter) Cleanup(ctx context.Context, jobID string) error {
	return a.inner.Cleanup(ctx, jobID)
}

func (a *vmRunnerAdapter) SampleResources(ctx context.Context, jobID string, options ResourceSampleOptions) (ResourceSnapshot, error) {
	sample, err := a.inner.SampleResource(ctx, jobID, options.IncludeStorage)
	if err != nil {
		return ResourceSnapshot{}, err
	}
	snapshot := ResourceSnapshot{ObservedAt: sample.Timestamp.UTC()}
	add := func(name, unit, kind string, value int64, labels ...jobtelemetry.Label) {
		id := int64(len(snapshot.Series))
		snapshot.Series = append(snapshot.Series, jobtelemetry.SeriesDefinition{SeriesID: id, Name: name, Unit: unit, Kind: kind, Labels: labels})
		snapshot.Values = append(snapshot.Values, jobtelemetry.Value{SeriesID: id, Value: value})
	}
	jobLabels := []jobtelemetry.Label{{Key: "scope", Value: "job"}}
	add("cpu.utilization", "millicores", "gauge", int64(sample.CPUPercent*10), jobLabels...)
	if sample.CPUCount > 0 {
		add("cpu.capacity", "millicores", "gauge", int64(sample.CPUCount)*1000, jobLabels...)
	}
	add("memory.usage", "bytes", "gauge", int64(sample.MemoryUsedBytes), jobLabels...)
	add("memory.limit", "bytes", "gauge", int64(sample.MemoryTotalBytes), jobLabels...)
	if sample.MemoryCommittedBytes > 0 {
		add("memory.committed", "bytes", "gauge", int64(sample.MemoryCommittedBytes), jobLabels...)
	}
	if sample.SwapUsedBytes > 0 {
		add("memory.swap.usage", "bytes", "gauge", int64(sample.SwapUsedBytes), jobLabels...)
	}
	if options.IncludeStorage {
		storageLabels := []jobtelemetry.Label{{Key: "scope", Value: "job"}, {Key: "volume", Value: "rootfs"}, {Key: "kind", Value: "rootfs"}}
		add("storage.used", "bytes", "gauge", int64(sample.StorageUsedBytes), storageLabels...)
		add("storage.capacity", "bytes", "gauge", int64(sample.StorageTotalBytes), storageLabels...)
	}
	return snapshot, nil
}

func (a *vmRunnerAdapter) PrefetchImages(ctx context.Context, imageRefs []string) error {
	if a.ociImages == nil {
		if len(imageRefs) == 0 {
			return nil
		}
		return fmt.Errorf("VM image prefetch requires REACTORCIDE_VM_IMAGE_SOURCE=oci")
	}
	for _, imageRef := range imageRefs {
		if _, err := a.ociImages.Resolve(ctx, imageRef); err != nil {
			return fmt.Errorf("prefetch VM image %q: %w", imageRef, err)
		}
	}
	return nil
}

func (a *vmRunnerAdapter) PruneImages(ctx context.Context, maxUnused time.Duration, now time.Time) (int, error) {
	if a.ociImages == nil {
		return 0, nil
	}
	return a.ociImages.Prune(ctx, maxUnused, now)
}

// toVMJobConfig translates process inputs and credential files into the
// platform-neutral VM transport model. Host bind mounts and runtime
// capabilities have no native-guest equivalent.
func toVMJobConfig(config *JobConfig, guestUser, hostOS string) *vmrunner.JobConfig {
	env := make(map[string]string, len(config.Env))
	for key, value := range config.Env {
		env[key] = value
	}
	platform := vmrunner.GuestPlatformPOSIX
	workingDir := config.WorkingDir
	if workingDir == "" {
		workingDir = "/job"
	}
	vcsDir := ""
	if config.VCSAuth != nil {
		vcsDir = config.VCSAuth.ContainerDir
	}
	if hostOS == "windows" {
		platform = vmrunner.GuestPlatformWindows
		workingDir = windowsGuestPath(workingDir, guestUser)
		for _, key := range []string{
			"HOME",
			"REACTORCIDE_CODE_DIR",
			"REACTORCIDE_JOB_DIR",
			"REACTORCIDE_WORKING_DIR",
			"REACTORCIDE_TRIGGERS_FILE",
			"REACTORCIDE_VCS_AUTH_DIR",
			"GIT_CONFIG_GLOBAL",
		} {
			if value := env[key]; value != "" {
				env[key] = windowsGuestPath(value, guestUser)
			}
		}
		if vcsDir != "" {
			vcsDir = `C:/reactorcide/job/.reactorcide/vcs-auth`
			env["REACTORCIDE_VCS_AUTH_DIR"] = vcsDir
			env["GIT_CONFIG_GLOBAL"] = vcsDir + "/gitconfig"
		}
	}
	if env["HOME"] == "" || env["HOME"] == "/home/runner" {
		if guestUser == "" {
			guestUser = "reactorcide"
		}
		if platform == vmrunner.GuestPlatformWindows {
			env["HOME"] = `C:/Users/` + guestUser
		} else {
			env["HOME"] = "/Users/" + guestUser
		}
	}
	files := []vmrunner.GuestFile{}
	if config.VCSAuth != nil {
		files = append(files,
			vmrunner.GuestFile{Path: vcsDir + "/gitconfig", Data: []byte(config.VCSAuth.GitConfig), Mode: 0o600},
			vmrunner.GuestFile{Path: vcsDir + "/credentials", Data: []byte(config.VCSAuth.Credentials), Mode: 0o600},
		)
	}
	trees := []vmrunner.GuestTree{}
	if config.SourceDir != "" {
		destination := config.SourceMountPath
		if destination == "" {
			destination = "/job/src"
		}
		if platform == vmrunner.GuestPlatformWindows {
			destination = windowsGuestPath(destination, guestUser)
		}
		trees = append(trees, vmrunner.GuestTree{
			SourcePath:  config.SourceDir,
			Destination: destination,
		})
	}
	return &vmrunner.JobConfig{
		Image:       config.Image,
		Command:     config.Command,
		Env:         env,
		Platform:    platform,
		WorkingDir:  workingDir,
		Files:       files,
		Trees:       trees,
		CPURequest:  config.CPURequest,
		CPULimit:    config.CPULimit,
		MemoryLimit: config.MemoryLimit,
		JobID:       config.JobID,
	}
}

func windowsGuestPath(path, guestUser string) string {
	if guestUser == "" {
		guestUser = "reactorcide"
	}
	path = strings.ReplaceAll(path, `\`, "/")
	switch {
	case path == "/job":
		return `C:/reactorcide/job`
	case strings.HasPrefix(path, "/job/"):
		return `C:/reactorcide/job/` + strings.TrimPrefix(path, "/job/")
	case path == "/home/runner":
		return `C:/Users/` + guestUser
	case strings.HasPrefix(path, "/home/runner/"):
		return `C:/Users/` + guestUser + "/" + strings.TrimPrefix(path, "/home/runner/")
	default:
		return path
	}
}

var _ JobRunner = (*vmRunnerAdapter)(nil)
var _ VMImageCacheManager = (*vmRunnerAdapter)(nil)
