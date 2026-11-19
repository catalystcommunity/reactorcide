package cachens

import (
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func strPtr(v string) *string { return &v }

func standardProfile() *models.ExecutionProfile {
	return &models.ExecutionProfile{Name: "standard", TrustedCacheWrites: true}
}

func TestComputeDefaultProfileKeepsSharedVolume(t *testing.T) {
	for _, name := range []string{"", "standard"} {
		d := Compute("org-a", name, standardProfile(), []string{"default"}, "standard")
		if d.Namespace != "standard" || d.ArtifactNamespace != "standard" || !d.Writable || d.VolumeKey != "" {
			t.Fatalf("profile %q: got %+v, want namespace standard, writable, shared volume", name, d)
		}
	}
	// A store without the profile (or an org with no standard row) keeps the
	// old behavior for the default profile.
	if d := Compute("org-a", "standard", nil, nil, "standard"); !d.Writable || d.VolumeKey != "" {
		t.Fatalf("missing standard profile: %+v", d)
	}
}

func TestComputeUntrustedProfileIsReadOnlyAndSeparate(t *testing.T) {
	pr := &models.ExecutionProfile{Name: "pr-untrusted", TrustedCacheWrites: false}
	d := Compute("org-a", "pr-untrusted", pr, []string{"fork", "pr"}, "standard")
	if d.Namespace != "pr-untrusted" || d.Writable || d.VolumeKey == "" {
		t.Fatalf("got %+v", d)
	}
	if !strings.Contains(d.Reason, "read-only") {
		t.Fatalf("reason %q", d.Reason)
	}
	// A missing custom profile gives no writes.
	if d := Compute("org-a", "custom", nil, nil, "standard"); d.Writable {
		t.Fatalf("missing custom profile must not write: %+v", d)
	}
}

func TestComputeProfileByRefClassMatrix(t *testing.T) {
	profile := &models.ExecutionProfile{Name: "standard", TrustedCacheWrites: true, CacheNamespace: strPtr("main"),
		ArtifactNamespace: strPtr("artifacts"), CacheNamespaceClasses: []string{"release", "default"}, CacheWriteClasses: []string{"default", "release"}}
	cases := []struct {
		classes   []string
		namespace string
		artifact  string
		writable  bool
	}{
		{[]string{"default"}, "main-default", "artifacts-default", true},
		{[]string{"release", "tag", "default"}, "main-release", "artifacts-release", true},
		{[]string{"pr"}, "main", "artifacts", false},
		{nil, "main", "artifacts", false},
		{[]string{"schedule"}, "main", "artifacts", false},
	}
	for _, c := range cases {
		d := Compute("org-a", "standard", profile, c.classes, "standard")
		if d.Namespace != c.namespace || d.ArtifactNamespace != c.artifact || d.Writable != c.writable {
			t.Fatalf("classes %v: got %+v, want %s/%s writable=%t", c.classes, d, c.namespace, c.artifact, c.writable)
		}
		if d.VolumeKey == "" {
			t.Fatalf("classes %v: a namespace other than the shared one needs its own volume", c.classes)
		}
	}
	// trusted_cache_writes false wins over the write classes.
	profile.TrustedCacheWrites = false
	if d := Compute("org-a", "standard", profile, []string{"default"}, "standard"); d.Writable {
		t.Fatalf("trusted_cache_writes false must refuse writes: %+v", d)
	}
}

func TestVolumeKeyIsQualifiedByOrganization(t *testing.T) {
	a := VolumeKey("org-a", "secure", "standard")
	b := VolumeKey("org-b", "secure", "standard")
	if a == b || !strings.HasPrefix(a, "secure-") {
		t.Fatalf("keys %q %q", a, b)
	}
	if VolumeKey("org-a", "standard", "standard") != "" {
		t.Fatal("the shared namespace keeps the shared volume")
	}
	if VolumeKey("org-a", "standard", "") == "" {
		t.Fatal("with no shared namespace every namespace gets its own volume")
	}
}

func TestSharedNamespaceFromEnv(t *testing.T) {
	t.Setenv(SharedNamespaceEnv, "")
	if got := SharedNamespaceFromEnv(); got != "" {
		t.Fatalf("empty setting: %q", got)
	}
	t.Setenv(SharedNamespaceEnv, " main ")
	if got := SharedNamespaceFromEnv(); got != "main" {
		t.Fatalf("got %q", got)
	}
}

func TestSanitize(t *testing.T) {
	if got := Sanitize("standard"); got != "standard" {
		t.Fatalf("got %q", got)
	}
	got := Sanitize("My Cache/Prod")
	if strings.ContainsAny(got, " /") || got == Sanitize("my-cache-prod") {
		t.Fatalf("a changed name keeps a hash: %q", got)
	}
	long := Sanitize(strings.Repeat("a", 100))
	if len(long) > 63 {
		t.Fatalf("too long: %d", len(long))
	}
	if Sanitize("...") == "" {
		t.Fatal("empty result")
	}
}

func TestEnvAndNormalize(t *testing.T) {
	env := Decision{Namespace: "n", ArtifactNamespace: "a", Writable: false}.Env()
	if env[EnvNamespace] != "n" || env[EnvWritable] != "false" || env[EnvArtifactNamespace] != "a" {
		t.Fatalf("env %v", env)
	}
	classes, err := NormalizeClasses([]string{" Release ", "default", "release"})
	if err != nil || strings.Join(classes, ",") != "release,default" {
		t.Fatalf("got %v %v (order must stay)", classes, err)
	}
	if _, err := NormalizeClasses([]string{"Bad Class!"}); err == nil {
		t.Fatal("an invalid class must fail")
	}
	profile := &models.ExecutionProfile{CacheWriteClasses: []string{"x y"}}
	if NormalizeProfile(profile) == nil {
		t.Fatal("an invalid write class must fail")
	}
}
