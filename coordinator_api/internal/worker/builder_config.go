package worker

import "os"

// BuilderConfig holds operator-level configuration for the buildkitd sidecar
// launched for jobs with CapabilityBuilder. Runners pick the fields they need
// and ignore the rest; the zero value is a valid "no operator overrides" state
// in which the sidecar runs with its image defaults.
type BuilderConfig struct {
	// Image is the buildkitd container image. Defaults to DefaultBuilderImage.
	Image string

	// ConfigPath is a host file path to a buildkitd.toml that will be bind-
	// mounted at /etc/buildkit/buildkitd.toml inside the sidecar. Empty means
	// the sidecar uses its image's built-in defaults.
	ConfigPath string

	// RegistryAuthPath is a host file path to a docker registry auth file
	// (config.json format) mounted into the sidecar at /root/.docker/config.json.
	// Buildkit uses this when pulling base images. Push credentials are
	// typically supplied client-side by the job via buildctl.
	RegistryAuthPath string

	// CacheVolume is an optional named docker volume (or k8s PVC name) mounted
	// at buildkit's state dir for cross-job layer cache. Empty means no cache
	// volume — each sidecar starts clean.
	CacheVolume string
}

// LoadBuilderConfig resolves BuilderConfig from environment variables. This is
// the single source of truth for operator knobs across all runners.
//
// Env vars:
//   - REACTORCIDE_BUILDER_IMAGE           (default DefaultBuilderImage)
//   - REACTORCIDE_BUILDER_CONFIG_PATH     (optional, no default)
//   - REACTORCIDE_BUILDER_REGISTRY_AUTH_PATH (optional, no default)
//   - REACTORCIDE_BUILDER_CACHE_VOLUME    (optional, no default)
func LoadBuilderConfig() BuilderConfig {
	image := os.Getenv("REACTORCIDE_BUILDER_IMAGE")
	if image == "" {
		image = DefaultBuilderImage
	}
	return BuilderConfig{
		Image:            image,
		ConfigPath:       os.Getenv("REACTORCIDE_BUILDER_CONFIG_PATH"),
		RegistryAuthPath: os.Getenv("REACTORCIDE_BUILDER_REGISTRY_AUTH_PATH"),
		CacheVolume:      os.Getenv("REACTORCIDE_BUILDER_CACHE_VOLUME"),
	}
}

// BuilderSidecarName returns the deterministic sidecar container/pod-container
// name for a given job id. Runners use this so cleanup can find the sidecar
// without extra state.
func BuilderSidecarName(jobID string) string {
	return "reactorcide-builder-" + jobID
}
