// Package imagepolicy checks a job image against the image policy of its
// organization (plan item C3, docs/builds-and-caches.md). The policy is off by
// default.
package imagepolicy

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

var digestPattern = regexp.MustCompile(`@sha256:[a-f0-9]{64}$`)

// Normalize returns the full form of an image reference: a reference with
// no registry gets "docker.io/", and a Docker Hub reference with no
// namespace gets "library/". For example "postgres:17" becomes
// "docker.io/library/postgres:17".
func Normalize(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	first, rest, hasSlash := strings.Cut(ref, "/")
	if !hasSlash {
		return "docker.io/library/" + ref
	}
	if !strings.ContainsAny(first, ".:") && first != "localhost" {
		return "docker.io/" + ref
	}
	if first == "docker.io" || first == "index.docker.io" {
		if !strings.Contains(rest, "/") {
			return "docker.io/library/" + rest
		}
		return "docker.io/" + rest
	}
	return ref
}

// HasDigest reports whether ref is pinned by a sha256 digest.
func HasDigest(ref string) bool { return digestPattern.MatchString(strings.TrimSpace(ref)) }

// prefixMatches reports whether ref starts with prefix at a reference
// boundary, so that "ghcr.io/acme" does not match "ghcr.io/acme-evil/x".
func prefixMatches(ref, prefix string) bool {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" || !strings.HasPrefix(ref, prefix) {
		return false
	}
	if len(ref) == len(prefix) || strings.HasSuffix(prefix, "/") {
		return true
	}
	switch ref[len(prefix)] {
	case '/', ':', '@':
		return true
	}
	return false
}

// Check returns nil when policy permits image, or an error that is the
// decision reason. A nil or disabled policy permits every image.
func Check(policy *models.ImagePolicy, image string) error {
	if policy == nil || !policy.Enabled {
		return nil
	}
	image = strings.TrimSpace(image)
	if image == "" {
		return fmt.Errorf("the job has no image")
	}
	// An oci:// reference (an image alias pin, VM_IMAGES_PLAN.md
	// "References") is the same registry reference with a scheme prefix.
	shown := image
	image = strings.TrimPrefix(image, "oci://")
	full := Normalize(image)
	allowed := false
	for _, prefix := range policy.AllowedPrefixes {
		prefix = strings.TrimPrefix(strings.TrimSpace(prefix), "oci://")
		if prefixMatches(image, prefix) || prefixMatches(full, prefix) || prefixMatches(full, Normalize(prefix)) {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("image %q is not under an allowed registry prefix of the organization image policy (allowed: %s)",
			shown, strings.Join(policy.AllowedPrefixes, ", "))
	}
	if policy.RequireDigest && !HasDigest(image) {
		return fmt.Errorf("image %q has no @sha256 digest; the organization image policy requires digest pinning", shown)
	}
	return nil
}

// ValidatePolicy checks the prefixes of a policy.
func ValidatePolicy(policy *models.ImagePolicy) error {
	if len(policy.AllowedPrefixes) > 100 {
		return fmt.Errorf("a policy has at most 100 prefixes")
	}
	for _, prefix := range policy.AllowedPrefixes {
		prefix = strings.TrimSpace(prefix)
		if prefix == "" || len(prefix) > 255 || strings.ContainsAny(prefix, " \t\n*?") {
			return fmt.Errorf("prefix %q is not valid: use a registry or repository prefix such as ghcr.io/example/ (no wildcards)", prefix)
		}
	}
	return nil
}
