package imagepolicy

import (
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

const digest = "@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"postgres:17":                     "docker.io/library/postgres:17",
		"acme/tool:1":                     "docker.io/acme/tool:1",
		"docker.io/postgres":              "docker.io/library/postgres",
		"ghcr.io/acme/tool:1":             "ghcr.io/acme/tool:1",
		"localhost/tool":                  "localhost/tool",
		"registry.example:5000/team/tool": "registry.example:5000/team/tool",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Fatalf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCheck(t *testing.T) {
	if Check(nil, "anything") != nil || Check(&models.ImagePolicy{AllowedPrefixes: []string{"x/"}}, "evil") != nil {
		t.Fatal("a nil or disabled policy permits every image")
	}
	policy := &models.ImagePolicy{Enabled: true, AllowedPrefixes: []string{"ghcr.io/acme", "docker.io/library/"}}
	for _, ok := range []string{"ghcr.io/acme/tool:1", "ghcr.io/acme:2", "postgres:17", "docker.io/library/busybox"} {
		if err := Check(policy, ok); err != nil {
			t.Fatalf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"ghcr.io/acme-evil/tool:1", "evil.example/acme/tool", "acme/tool:1", ""} {
		if err := Check(policy, bad); err == nil {
			t.Fatalf("%q must be refused", bad)
		}
	}
	err := Check(policy, "quay.io/x/y")
	if err == nil || !strings.Contains(err.Error(), "allowed registry prefix") {
		t.Fatalf("reason: %v", err)
	}
	policy.RequireDigest = true
	if err := Check(policy, "ghcr.io/acme/tool:1"); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("digest pinning: %v", err)
	}
	if err := Check(policy, "ghcr.io/acme/tool:1"+digest); err != nil {
		t.Fatal(err)
	}
	if ValidatePolicy(&models.ImagePolicy{AllowedPrefixes: []string{"ghcr.io/*"}}) == nil {
		t.Fatal("a wildcard prefix must be refused")
	}
}

// TestCheckSchemes checks that an oci:// reference matches a registry
// prefix, and that an s3:// reference needs an s3:// prefix.
func TestCheckSchemes(t *testing.T) {
	policy := &models.ImagePolicy{Enabled: true, AllowedPrefixes: []string{"ghcr.io/acme/", "s3://images/vm/"}, RequireDigest: true}
	for _, ok := range []string{"oci://ghcr.io/acme/vm/mac:1" + digest, "s3://images/vm/win:1" + digest, "ghcr.io/acme/x" + digest} {
		if err := Check(policy, ok); err != nil {
			t.Fatalf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"oci://ghcr.io/acme/vm/mac:1", "oci://ghcr.io/evil/vm" + digest, "s3://other/vm/win" + digest} {
		if err := Check(policy, bad); err == nil {
			t.Fatalf("%q must be refused", bad)
		} else if !strings.Contains(err.Error(), bad) {
			t.Fatalf("the reason must show the reference as given: %v", err)
		}
	}
}
