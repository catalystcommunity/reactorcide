// Package cachens computes the cache and artifact namespace of a job and
// whether the job may write to its cache (plan item C2,
// docs/builds-and-caches.md).
//
// The coordinator computes the decision from coordinator records only: the
// execution profile of the job and the ref classes of the job. It sends the
// decision in the lease. The worker uses it and cannot choose another
// namespace. Job code reads the decision from reserved environment values,
// so that it can select an external cache instance or prefix with no trust
// logic of its own.
package cachens

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/refclass"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// Reserved job environment values. The coordinator sets them after the job
// variables, so a job cannot replace them.
const (
	EnvNamespace         = "REACTORCIDE_CACHE_NAMESPACE"
	EnvWritable          = "REACTORCIDE_CACHE_WRITABLE"
	EnvArtifactNamespace = "REACTORCIDE_ARTIFACT_NAMESPACE"
)

// DefaultProfile is the profile of a job with no execution profile.
const DefaultProfile = "standard"

// SharedNamespaceEnv names the coordinator setting for the namespace that
// keeps the shared, operator-named builder cache volume of each worker (the
// volume of REACTORCIDE_BUILDER_CACHE_VOLUME, as before namespaces). Unset
// means "standard". An empty value turns the shared volume off: then every
// namespace gets its own volume, qualified by the organization.
const SharedNamespaceEnv = "REACTORCIDE_CACHE_SHARED_NAMESPACE"

// SharedNamespaceFromEnv reads SharedNamespaceEnv.
func SharedNamespaceFromEnv() string {
	value, set := os.LookupEnv(SharedNamespaceEnv)
	if !set {
		return DefaultProfile
	}
	return strings.TrimSpace(value)
}

// Decision is the cache decision of one job.
type Decision struct {
	// Namespace is the cache namespace name in the organization, for
	// example "standard" or "standard-default".
	Namespace string
	// ArtifactNamespace is the artifact namespace name in the organization.
	ArtifactNamespace string
	// Writable is true when the job may write to the cache of Namespace.
	Writable bool
	// VolumeKey selects the builder cache volume on the worker. Empty is the
	// shared volume that the operator names (the behavior before
	// namespaces). Otherwise it is unique to the organization and the
	// namespace.
	VolumeKey string
	// Reason tells why, in words, for logs and decision records.
	Reason string
}

var unsafeChars = regexp.MustCompile(`[^a-z0-9._-]+`)

// maxNamespace is the longest namespace name. A longer name is cut and gets
// a hash suffix, so that two long names stay different.
const maxNamespace = 63

// Sanitize makes a namespace name safe for volume names and object
// prefixes: lowercase letters, digits, ".", "_" and "-", starting with a
// letter or digit, at most 63 characters. A name that changes gets a short
// hash of the original, so that two different names stay different.
func Sanitize(name string) string {
	raw := strings.TrimSpace(name)
	clean := unsafeChars.ReplaceAllString(strings.ToLower(raw), "-")
	clean = strings.TrimLeft(clean, "._-")
	if clean == raw && clean != "" && len(clean) <= maxNamespace {
		return clean
	}
	sum := sha256.Sum256([]byte(raw))
	suffix := hex.EncodeToString(sum[:])[:8]
	if clean == "" {
		return "ns-" + suffix
	}
	if len(clean) > maxNamespace-9 {
		clean = clean[:maxNamespace-9]
	}
	return strings.TrimRight(clean, "._-") + "-" + suffix
}

// Compute returns the cache decision of a job.
//
//   - profileName is the execution profile of the job ("" is "standard").
//   - profile is that profile, or nil when it does not exist.
//   - classes are the ref classes of the job.
//   - shared is the namespace that keeps the shared volume
//     (SharedNamespaceFromEnv).
//
// Rules: the base namespace is the profile cache_namespace, or the profile
// name. The first class of cache_namespace_classes that the job has is added
// as a suffix ("<base>-<class>"). The artifact namespace is the profile
// artifact_namespace, or the base namespace, with the same suffix. A job may
// write when the profile has trusted_cache_writes and, when the profile has
// cache_write_classes, the job has one of them. A profile that does not
// exist gives no writes, except the built-in "standard" profile, which
// writes as before namespaces.
func Compute(orgID, profileName string, profile *models.ExecutionProfile, classes []string, shared string) Decision {
	if strings.TrimSpace(profileName) == "" {
		profileName = DefaultProfile
	}
	classes = refclass.Normalize(classes)
	base := profileName
	artifactBase := ""
	writable := profileName == DefaultProfile
	reason := "profile " + profileName + " does not exist"
	var splitClasses, writeClasses []string
	if profile != nil {
		if profile.CacheNamespace != nil && strings.TrimSpace(*profile.CacheNamespace) != "" {
			base = strings.TrimSpace(*profile.CacheNamespace)
		}
		if profile.ArtifactNamespace != nil {
			artifactBase = strings.TrimSpace(*profile.ArtifactNamespace)
		}
		splitClasses = profile.CacheNamespaceClasses
		writeClasses = profile.CacheWriteClasses
		writable = profile.TrustedCacheWrites
		reason = "profile " + profileName
		if !profile.TrustedCacheWrites {
			reason += " has no trusted cache writes"
		}
	}
	if artifactBase == "" {
		artifactBase = base
	}
	suffix := ""
	for _, class := range splitClasses {
		if refclass.Has(classes, class) {
			suffix = "-" + strings.ToLower(strings.TrimSpace(class))
			break
		}
	}
	if writable && len(writeClasses) > 0 {
		writable = false
		for _, class := range writeClasses {
			if refclass.Has(classes, class) {
				writable = true
				break
			}
		}
		if !writable {
			reason += "; cache writes need one of the ref classes " + strings.Join(writeClasses, ",")
		}
	}
	decision := Decision{
		Namespace:         Sanitize(base + suffix),
		ArtifactNamespace: Sanitize(artifactBase + suffix),
		Writable:          writable,
	}
	decision.VolumeKey = VolumeKey(orgID, decision.Namespace, shared)
	if writable {
		decision.Reason = reason + ": namespace " + decision.Namespace + ", writable"
	} else {
		decision.Reason = reason + ": namespace " + decision.Namespace + ", read-only"
	}
	return decision
}

// VolumeKey returns the worker volume key of a namespace. The shared
// namespace has the empty key (the operator-named volume). Every other
// namespace gets a key with a hash of the organization and the namespace,
// so that two organizations with the same namespace name never share a
// volume.
func VolumeKey(orgID, namespace, shared string) string {
	if shared != "" && namespace == shared {
		return ""
	}
	sum := sha256.Sum256([]byte(orgID + "\x00" + namespace))
	name := namespace
	if len(name) > 40 {
		name = strings.TrimRight(name[:40], "._-")
	}
	return name + "-" + hex.EncodeToString(sum[:])[:10]
}

// Env returns the reserved environment values of a decision.
func (d Decision) Env() map[string]string {
	writable := "false"
	if d.Writable {
		writable = "true"
	}
	return map[string]string{
		EnvNamespace:         d.Namespace,
		EnvWritable:          writable,
		EnvArtifactNamespace: d.ArtifactNamespace,
	}
}

// NormalizeClasses lowercases, trims, and removes repeats from a list of
// ref classes, and keeps the order (the first match of
// cache_namespace_classes wins). It refuses a class that is not valid.
func NormalizeClasses(classes []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, class := range classes {
		class = strings.ToLower(strings.TrimSpace(class))
		if class == "" || seen[class] {
			continue
		}
		if !refclass.ValidClass(class) {
			return nil, fmt.Errorf("ref class %q is not valid", class)
		}
		seen[class] = true
		out = append(out, class)
	}
	return out, nil
}

// NormalizeProfile normalizes the cache class lists of a profile in place.
// A nil list stays nil (an update then keeps the stored value).
func NormalizeProfile(profile *models.ExecutionProfile) error {
	if profile == nil {
		return nil
	}
	if profile.CacheNamespaceClasses != nil {
		classes, err := NormalizeClasses(profile.CacheNamespaceClasses)
		if err != nil {
			return fmt.Errorf("cache_namespace_classes: %w", err)
		}
		profile.CacheNamespaceClasses = classes
	}
	if profile.CacheWriteClasses != nil {
		classes, err := NormalizeClasses(profile.CacheWriteClasses)
		if err != nil {
			return fmt.Errorf("cache_write_classes: %w", err)
		}
		profile.CacheWriteClasses = classes
	}
	return nil
}
