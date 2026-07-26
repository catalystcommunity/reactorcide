package worker

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker/vmrunner"
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
// (LocalImageSource's pre-placed ImageDir, or VM-2's OCI-backed
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
//   - REACTORCIDE_VM_IMAGE_REGISTRY_PLAIN_HTTP (optional, comma-separated
//     registry hosts to reach over plain HTTP instead of HTTPS; only used
//     when REACTORCIDE_VM_IMAGE_SOURCE is "oci". Registry auth itself comes
//     from the ambient docker config file -- see vmrunner.NewOCIImageSource
//     -- never from an env var, so credentials never pass through a
//     printable environment.)
//   - REACTORCIDE_VM_SSH_USER              (default: "runner")
//   - REACTORCIDE_VM_SSH_PASSWORD          (optional, no default)
//   - REACTORCIDE_VM_SSH_PRIVATE_KEY_FILE  (optional; path to a PEM private
//     key file, read at load time -- the key material never passes through
//     an env var directly)
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
		sshUser = "runner"
	}

	cfg := VMConfig{
		ImageSource:            imageSource,
		ImageDir:               imageDir,
		OCIImageCacheDir:       ociImageCacheDir,
		OCIPlainHTTPRegistries: ociPlainHTTPRegistries,
		SSHUser:                sshUser,
		SSHPassword:            os.Getenv("REACTORCIDE_VM_SSH_PASSWORD"),
	}

	if keyFile := os.Getenv("REACTORCIDE_VM_SSH_PRIVATE_KEY_FILE"); keyFile != "" {
		key, err := os.ReadFile(keyFile)
		if err != nil {
			return VMConfig{}, fmt.Errorf("read REACTORCIDE_VM_SSH_PRIVATE_KEY_FILE: %w", err)
		}
		cfg.SSHPrivateKeyPEM = key
	}

	return cfg, nil
}

// buildVMImageSource constructs the vmrunner.ImageSource cfg.ImageSource
// selects. This is the one place that translates VMConfig's ImageSource
// knob into a concrete vmrunner.ImageSource, keeping LocalImageSource and
// OCIImageSource swappable purely via configuration (see VM_RUNNERS_PLAN.md
// VM-2's "wire selection" requirement).
func buildVMImageSource(cfg VMConfig) (vmrunner.ImageSource, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.ImageSource)) {
	case "", "local":
		return vmrunner.NewLocalImageSource(cfg.ImageDir), nil
	case "oci":
		return vmrunner.NewOCIImageSource(
			cfg.OCIImageCacheDir,
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
// platform's VMLifecycle -- unsupported-error stub on Linux until VM-3/VM-4
// land -- plus the SSH GuestTransport and a config-driven ImageSource,
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
	}

	images, err := buildVMImageSource(cfg)
	if err != nil {
		return nil, fmt.Errorf("build vm backend image source: %w", err)
	}

	inner, err := vmrunner.NewDefaultWithImages(images, creds)
	if err != nil {
		return nil, err
	}
	return &vmRunnerAdapter{inner: inner}, nil
}

// vmRunnerAdapter wraps a *vmrunner.VMRunner to satisfy JobRunner exactly,
// translating *JobConfig <-> *vmrunner.JobConfig at each call.
type vmRunnerAdapter struct {
	inner *vmrunner.VMRunner
}

func (a *vmRunnerAdapter) SpawnJob(ctx context.Context, config *JobConfig) (string, error) {
	return a.inner.SpawnJob(ctx, toVMJobConfig(config))
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

// toVMJobConfig translates the fields vmrunner.VMRunner needs out of the
// full worker.JobConfig. Fields with no VM-guest equivalent yet (bind
// mounts, VCSAuth file materialization, capabilities) are intentionally
// left unmapped -- see VM_RUNNERS_PLAN.md's "Guest execution note" and
// VM-6 ("wire the guest env/secret/VCS-auth injection to the lease
// fields"), which is where that gets addressed.
func toVMJobConfig(config *JobConfig) *vmrunner.JobConfig {
	return &vmrunner.JobConfig{
		Image:       config.Image,
		Command:     config.Command,
		Env:         config.Env,
		CPURequest:  config.CPURequest,
		CPULimit:    config.CPULimit,
		MemoryLimit: config.MemoryLimit,
		JobID:       config.JobID,
	}
}

var _ JobRunner = (*vmRunnerAdapter)(nil)
