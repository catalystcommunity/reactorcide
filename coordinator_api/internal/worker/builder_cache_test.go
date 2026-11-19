package worker

import (
	"context"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestBuilderBindsPerNamespace(t *testing.T) {
	b := BuilderConfig{CacheVolume: "buildkit-cache", ConfigPath: "/etc/bk.toml", RegistryAuthPath: "/etc/auth.json"}
	cases := []struct {
		name  string
		cache *CacheConfig
		want  string // the cache bind, "" for none
	}{
		{"old coordinator keeps the shared volume", nil, "buildkit-cache:/var/lib/buildkit"},
		{"shared namespace keeps the shared volume", &CacheConfig{Namespace: "standard", Writable: true}, "buildkit-cache:/var/lib/buildkit"},
		{"other namespace gets its own volume", &CacheConfig{Namespace: "secure", VolumeKey: "secure-abc", Writable: true}, "buildkit-cache-secure-abc:/var/lib/buildkit"},
		{"no writes: no persistent state", &CacheConfig{Namespace: "pr-untrusted", VolumeKey: "pr-untrusted-abc"}, ""},
		{"no writes in the shared namespace: no persistent state", &CacheConfig{Namespace: "standard"}, ""},
	}
	for _, c := range cases {
		binds := b.builderBinds(c.cache)
		if binds[0] != "/etc/bk.toml:/etc/buildkit/buildkitd.toml:ro" || binds[1] != "/etc/auth.json:/root/.docker/config.json:ro" {
			t.Fatalf("%s: config binds %v", c.name, binds)
		}
		got := ""
		if len(binds) == 3 {
			got = binds[2]
		}
		if got != c.want {
			t.Fatalf("%s: cache bind %q, want %q", c.name, got, c.want)
		}
	}
	// An absolute host path gets a sub directory per namespace.
	host := BuilderConfig{CacheVolume: "/srv/buildkit"}
	if got := host.builderBinds(&CacheConfig{VolumeKey: "k1", Writable: true}); !reflect.DeepEqual(got, []string{"/srv/buildkit/namespaces/k1:/var/lib/buildkit"}) {
		t.Fatalf("host path binds %v", got)
	}
	if got := (BuilderConfig{}).builderBinds(&CacheConfig{VolumeKey: "k", Writable: true}); len(got) != 0 {
		t.Fatalf("no cache volume: %v", got)
	}
}

func TestContainerdBuilderSidecarArgs(t *testing.T) {
	cr := &ContainerdRunner{builder: BuilderConfig{CacheVolume: "bk"}}
	args := strings.Join(cr.builderSidecarArgs(&JobConfig{JobID: "j1", Cache: &CacheConfig{VolumeKey: "ns-1", Writable: true}}), " ")
	for _, want := range []string{"--privileged", "-v bk-ns-1:/var/lib/buildkit", DefaultBuilderImage + " --addr tcp://0.0.0.0:1234 --oci-worker=true"} {
		if !strings.Contains(args, want) {
			t.Fatalf("args %q lack %q", args, want)
		}
	}
	readOnly := strings.Join(cr.builderSidecarArgs(&JobConfig{JobID: "j2", Cache: &CacheConfig{VolumeKey: "ns-1"}}), " ")
	if strings.Contains(readOnly, "/var/lib/buildkit") {
		t.Fatalf("a job with no cache writes must not mount the cache: %q", readOnly)
	}
	legacy := strings.Join(cr.builderSidecarArgs(&JobConfig{JobID: "j3"}), " ")
	if !strings.Contains(legacy, "-v bk:/var/lib/buildkit") {
		t.Fatalf("a lease with no cache decision keeps the shared volume: %q", legacy)
	}

	rootless := &ContainerdRunner{builder: BuilderConfig{Rootless: true, Image: DefaultBuilderImage, CacheVolume: "bk", ConfigPath: "/etc/bk.toml"}}
	args = strings.Join(rootless.builderSidecarArgs(&JobConfig{JobID: "j4", Cache: &CacheConfig{Writable: true}}), " ")
	for _, want := range []string{"--security-opt seccomp=unconfined", "--security-opt apparmor=unconfined", DefaultBuilderRootlessImage,
		"--oci-worker-no-process-sandbox", "-v bk:/home/user/.local/share/buildkit", "--config /etc/buildkit/buildkitd.toml"} {
		if !strings.Contains(args, want) {
			t.Fatalf("rootless args %q lack %q", args, want)
		}
	}
	if strings.Contains(args, "--privileged") {
		t.Fatalf("rootless must not be privileged: %q", args)
	}
}

func TestBuilderImageDefaults(t *testing.T) {
	if got := (BuilderConfig{}).image(); got != DefaultBuilderImage {
		t.Fatal(got)
	}
	if got := (BuilderConfig{Rootless: true}).image(); got != DefaultBuilderRootlessImage {
		t.Fatal(got)
	}
	if got := (BuilderConfig{Rootless: true, Image: "registry.example/bk:rootless"}).image(); got != "registry.example/bk:rootless" {
		t.Fatal(got)
	}
	t.Setenv("REACTORCIDE_BUILDER_ROOTLESS", "true")
	if !LoadBuilderConfig().Rootless {
		t.Fatal("REACTORCIDE_BUILDER_ROOTLESS=true must turn on the rootless mode")
	}
}

func spawnBuilderPod(t *testing.T, builder BuilderConfig, cache *CacheConfig) corev1.PodSpec {
	t.Helper()
	clientset := fake.NewSimpleClientset()
	runner := &KubernetesRunner{clientset: clientset, namespace: "reactorcide", serviceAccount: "default", dindImage: "docker:27-dind", builder: builder}
	if _, err := runner.SpawnJob(context.Background(), &JobConfig{
		JobID: "builder-job", Image: "reactorcide/runnerbase:test", Command: []string{"true"},
		Env: map[string]string{}, WorkingDir: "/job", Capabilities: []string{CapabilityBuilder}, Cache: cache,
	}); err != nil {
		t.Fatalf("SpawnJob: %v", err)
	}
	jobs, err := clientset.BatchV1().Jobs("reactorcide").List(context.Background(), metav1.ListOptions{})
	if err != nil || len(jobs.Items) != 1 {
		t.Fatalf("jobs: %v %v", jobs, err)
	}
	return jobs.Items[0].Spec.Template.Spec
}

func builderContainer(t *testing.T, spec corev1.PodSpec) corev1.Container {
	t.Helper()
	for _, c := range spec.InitContainers {
		if c.Name == "builder" {
			return c
		}
	}
	t.Fatal("no builder sidecar")
	return corev1.Container{}
}

func TestKubernetesBuilderCacheSubPathPerNamespace(t *testing.T) {
	builder := BuilderConfig{CacheVolume: "buildkit-cache"}
	cases := []struct {
		name    string
		cache   *CacheConfig
		mounted bool
		subPath string
	}{
		// Never the claim root: it holds the caches of every namespace.
		{"old coordinator", nil, true, "shared"},
		{"shared namespace (default profile)", &CacheConfig{Namespace: "standard", Writable: true}, true, "shared"},
		{"own namespace", &CacheConfig{Namespace: "secure", VolumeKey: "secure-1a2b", Writable: true}, true, "namespaces/secure-1a2b"},
		{"no writes", &CacheConfig{Namespace: "pr-untrusted", VolumeKey: "pr-untrusted-1a2b"}, false, ""},
	}
	for _, c := range cases {
		spec := spawnBuilderPod(t, builder, c.cache)
		sidecar := builderContainer(t, spec)
		var mount *corev1.VolumeMount
		for i := range sidecar.VolumeMounts {
			if sidecar.VolumeMounts[i].Name == "builder-cache" {
				mount = &sidecar.VolumeMounts[i]
			}
		}
		if (mount != nil) != c.mounted {
			t.Fatalf("%s: mounted=%t, want %t", c.name, mount != nil, c.mounted)
		}
		if mount == nil {
			for _, v := range spec.Volumes {
				if v.Name == "builder-cache" {
					t.Fatalf("%s: an unused cache volume", c.name)
				}
			}
			continue
		}
		if mount.MountPath != "/var/lib/buildkit" || mount.SubPath != c.subPath || mount.ReadOnly {
			t.Fatalf("%s: mount %+v", c.name, *mount)
		}
		found := false
		for _, v := range spec.Volumes {
			if v.Name == "builder-cache" && v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == "buildkit-cache" {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: the claim is not the operator PVC", c.name)
		}
		if sidecar.SecurityContext == nil || sidecar.SecurityContext.Privileged == nil || !*sidecar.SecurityContext.Privileged {
			t.Fatalf("%s: the default sidecar stays privileged", c.name)
		}
	}
}

// An absolute host path: every namespace, also the shared one, binds only
// its own directory, so no build can reach a sibling namespace.
func TestBuilderCacheHostPathNeverMountsTheRoot(t *testing.T) {
	builder := BuilderConfig{CacheVolume: "/srv/buildkit"}
	for _, c := range []struct {
		cache *CacheConfig
		want  string
	}{
		{nil, "/srv/buildkit/shared"},
		{&CacheConfig{Namespace: "standard", Writable: true}, "/srv/buildkit/shared"},
		{&CacheConfig{Namespace: "secure", VolumeKey: "secure-1a2b", Writable: true}, "/srv/buildkit/namespaces/secure-1a2b"},
	} {
		target, ok := builder.cacheTarget(c.cache)
		if !ok || target.Source != c.want || target.SubPath == "" {
			t.Fatalf("cache %+v: target %+v, want source %s", c.cache, target, c.want)
		}
	}
	// A named volume of Docker or containerd keeps its name: volumes are
	// separate, and "<volume>-<key>" is another volume.
	if target, _ := (BuilderConfig{CacheVolume: "bk"}).cacheTarget(nil); target.Source != "bk" {
		t.Fatalf("named volume %+v", target)
	}
}

func TestKubernetesBuilderRootless(t *testing.T) {
	spec := spawnBuilderPod(t, BuilderConfig{Rootless: true, CacheVolume: "bk", ConfigPath: "buildkit-config", RegistryAuthPath: "registry-auth"},
		&CacheConfig{Writable: true})
	sidecar := builderContainer(t, spec)
	sc := sidecar.SecurityContext
	if sidecar.Image != DefaultBuilderRootlessImage || sc == nil || (sc.Privileged != nil && *sc.Privileged) ||
		sc.RunAsUser == nil || *sc.RunAsUser != 1000 || sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot ||
		sc.SeccompProfile == nil || sc.SeccompProfile.Type != corev1.SeccompProfileTypeUnconfined ||
		sc.AppArmorProfile == nil || sc.AppArmorProfile.Type != corev1.AppArmorProfileTypeUnconfined {
		t.Fatalf("rootless sidecar: image %s security %+v", sidecar.Image, sc)
	}
	if !strings.Contains(strings.Join(sidecar.Args, " "), "--oci-worker-no-process-sandbox") {
		t.Fatalf("args %v", sidecar.Args)
	}
	paths := map[string]string{}
	for _, m := range sidecar.VolumeMounts {
		paths[m.Name] = m.MountPath
	}
	if paths["builder-cache"] != "/home/user/.local/share/buildkit" || paths["builder-auth"] != "/home/user/.docker" || paths["builder-config"] != "/etc/buildkit" {
		t.Fatalf("rootless mounts %v", paths)
	}
}
