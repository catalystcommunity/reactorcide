package worker

import (
	"fmt"
	"path"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// WP14 (plan items C2 and C3): the cache namespace and the write mode of the
// builder sidecar, and the optional rootless BuildKit mode. See
// docs/builds-and-caches.md and docs/builds-and-caches.md.

// CacheConfig is the cache decision of the coordinator for one job
// (Lease.cache). nil means a lease from an older coordinator: the sidecar
// then uses the shared cache volume with writes, as before namespaces.
type CacheConfig struct {
	Namespace         string
	VolumeKey         string
	Writable          bool
	ArtifactNamespace string
}

// DefaultBuilderRootlessImage is the buildkitd image of the rootless mode
// when REACTORCIDE_BUILDER_IMAGE is not set.
const DefaultBuilderRootlessImage = "moby/buildkit:v0.17.3-rootless"

// rootlessBuilderUID is the user of the rootless BuildKit image.
const rootlessBuilderUID = 1000

// builderPaths are the paths inside the sidecar.
type builderPaths struct {
	StateDir   string
	ConfigFile string
	ConfigDir  string
	DockerDir  string
}

func (b BuilderConfig) paths() builderPaths {
	if b.Rootless {
		return builderPaths{
			StateDir:   "/home/user/.local/share/buildkit",
			ConfigFile: "/etc/buildkit/buildkitd.toml",
			ConfigDir:  "/etc/buildkit",
			DockerDir:  "/home/user/.docker",
		}
	}
	return builderPaths{
		StateDir:   "/var/lib/buildkit",
		ConfigFile: "/etc/buildkit/buildkitd.toml",
		ConfigDir:  "/etc/buildkit",
		DockerDir:  "/root/.docker",
	}
}

// image returns the sidecar image.
func (b BuilderConfig) image() string {
	if b.Image != "" && !(b.Rootless && b.Image == DefaultBuilderImage) {
		return b.Image
	}
	if b.Rootless {
		return DefaultBuilderRootlessImage
	}
	return DefaultBuilderImage
}

// sidecarArgs returns the buildkitd arguments.
func (b BuilderConfig) sidecarArgs() []string {
	args := []string{
		"--addr", fmt.Sprintf("tcp://0.0.0.0:%d", BuilderSidecarPort),
		"--oci-worker=true",
	}
	if b.Rootless {
		// A rootless buildkitd in a container cannot make a new PID
		// namespace for each build step.
		args = append(args, "--oci-worker-no-process-sandbox")
	}
	if b.ConfigPath != "" {
		args = append(args, "--config", b.paths().ConfigFile)
	}
	return args
}

// builderCacheTarget is the persistent cache of one sidecar.
type builderCacheTarget struct {
	// Source is the docker or nerdctl volume source: a named volume, or a
	// host directory when the operator setting is an absolute path.
	Source string
	// Claim and SubPath are the Kubernetes PVC and the sub path in it. The
	// sub path is never empty: a mount of the claim root would let a build
	// reach the caches of every other namespace.
	Claim   string
	SubPath string
}

// sharedCacheSubPath is the sub path of the shared namespace in a PVC or a
// host directory. It is a sibling of "namespaces/", not its parent. Before
// this layout the shared namespace used the root of the volume; the first
// build after the upgrade starts with a cold shared cache, and the operator
// can delete the old root-level BuildKit state.
const sharedCacheSubPath = "shared"

// cacheTarget returns the persistent cache of a job, or false when the
// sidecar must start with no persistent state:
//
//   - The operator set no cache volume.
//   - The coordinator decided that the job may not write to its cache
//     (CacheConfig.Writable false). BuildKit needs a writable state
//     directory, so read-only means no shared state at all: the job cannot
//     change the cache that other jobs read.
//
// Every namespace mounts only its own part of the operator volume, so a
// build (privileged or not) cannot reach the cache of another namespace:
//
//   - Docker, containerd named volume: the shared namespace (empty
//     VolumeKey) and a lease with no cache decision use the operator volume;
//     every other namespace uses its own volume "<volume>-<key>". Named
//     volumes are separate, so no volume contains another.
//   - An absolute host path: "<path>/shared" for the shared namespace,
//     "<path>/namespaces/<key>" for the others.
//   - Kubernetes: the sub path "shared" or "namespaces/<key>" of the PVC.
func (b BuilderConfig) cacheTarget(cache *CacheConfig) (builderCacheTarget, bool) {
	volume := strings.TrimSpace(b.CacheVolume)
	if volume == "" {
		return builderCacheTarget{}, false
	}
	if cache != nil && !cache.Writable {
		return builderCacheTarget{}, false
	}
	if cache == nil || cache.VolumeKey == "" {
		target := builderCacheTarget{Source: volume, Claim: volume, SubPath: sharedCacheSubPath}
		if strings.HasPrefix(volume, "/") {
			target.Source = path.Join(volume, sharedCacheSubPath)
		}
		return target, true
	}
	key := cache.VolumeKey
	target := builderCacheTarget{Claim: volume, SubPath: "namespaces/" + key}
	if strings.HasPrefix(volume, "/") {
		target.Source = path.Join(volume, "namespaces", key)
	} else {
		target.Source = volume + "-" + key
	}
	return target, true
}

// builderBinds returns the docker-style "source:target[:ro]" mounts of the
// sidecar, for the Docker and containerd runners.
func (b BuilderConfig) builderBinds(cache *CacheConfig) []string {
	p := b.paths()
	var binds []string
	if b.ConfigPath != "" {
		binds = append(binds, fmt.Sprintf("%s:%s:ro", b.ConfigPath, p.ConfigFile))
	}
	if b.RegistryAuthPath != "" {
		binds = append(binds, fmt.Sprintf("%s:%s/config.json:ro", b.RegistryAuthPath, p.DockerDir))
	}
	if target, ok := b.cacheTarget(cache); ok {
		binds = append(binds, fmt.Sprintf("%s:%s", target.Source, p.StateDir))
	}
	return binds
}

// rootlessSecurityOpts are the docker and nerdctl security options of the
// rootless sidecar. The rootless image needs no privilege, but it must
// create user namespaces and mount inside them, which the default seccomp
// and AppArmor profiles refuse.
var rootlessSecurityOpts = []string{"seccomp=unconfined", "apparmor=unconfined"}

// kubernetesBuilderVolumes returns the mounts and volumes of the Kubernetes
// sidecar.
func (b BuilderConfig) kubernetesBuilderVolumes(cache *CacheConfig) ([]corev1.VolumeMount, []corev1.Volume) {
	p := b.paths()
	var mounts []corev1.VolumeMount
	var volumes []corev1.Volume
	// ConfigMap name in ConfigPath when running under k8s.
	if b.ConfigPath != "" {
		mounts = append(mounts, corev1.VolumeMount{Name: "builder-config", MountPath: p.ConfigDir, ReadOnly: true})
		volumes = append(volumes, corev1.Volume{Name: "builder-config", VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: b.ConfigPath}},
		}})
	}
	// Secret name in RegistryAuthPath when running under k8s.
	if b.RegistryAuthPath != "" {
		mounts = append(mounts, corev1.VolumeMount{Name: "builder-auth", MountPath: p.DockerDir, ReadOnly: true})
		volumes = append(volumes, corev1.Volume{Name: "builder-auth", VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{SecretName: b.RegistryAuthPath},
		}})
	}
	// PVC name in CacheVolume when running under k8s. Each namespace,
	// also the shared one, mounts only its own sub path of the claim; the
	// kubelet makes the directory.
	if target, ok := b.cacheTarget(cache); ok {
		mounts = append(mounts, corev1.VolumeMount{Name: "builder-cache", MountPath: p.StateDir, SubPath: target.SubPath})
		volumes = append(volumes, corev1.Volume{Name: "builder-cache", VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: target.Claim},
		}})
	}
	return mounts, volumes
}

// kubernetesBuilderSecurityContext returns the security context of the
// Kubernetes sidecar: privileged root (the default), or the rootless mode.
func (b BuilderConfig) kubernetesBuilderSecurityContext() *corev1.SecurityContext {
	if b.Rootless {
		uid := int64(rootlessBuilderUID)
		return &corev1.SecurityContext{
			RunAsUser:       &uid,
			RunAsGroup:      &uid,
			RunAsNonRoot:    boolPtr(true),
			SeccompProfile:  &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeUnconfined},
			AppArmorProfile: &corev1.AppArmorProfile{Type: corev1.AppArmorProfileTypeUnconfined},
		}
	}
	priv := true
	return &corev1.SecurityContext{
		Privileged:   &priv,
		RunAsUser:    int64Ptr(0),
		RunAsNonRoot: boolPtr(false),
	}
}
