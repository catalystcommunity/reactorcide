// Package envdefaults validates and merges the non-secret environment
// defaults of an organization, a worker class, and a project (plan item C4,
// docs/builds-and-caches.md).
//
// Precedence in the job environment: job > project > worker class >
// organization. A default fills a key only when the job environment does
// not have it. A default never sets a reserved key (REACTORCIDE_*, RC_WF*),
// and a value cannot hold a secret reference.
package envdefaults

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// Limits of one scope.
const (
	MaxKeyLength   = 128
	MaxValueLength = 8192
	MaxPerScope    = 100
)

var keyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// ReservedKey reports whether key is reserved for the coordinator.
func ReservedKey(key string) bool {
	upper := strings.ToUpper(strings.TrimSpace(key))
	return strings.HasPrefix(upper, "REACTORCIDE_") || strings.HasPrefix(upper, "RC_WF")
}

// Validate checks one key and value.
func Validate(key, value string) error {
	if !keyPattern.MatchString(key) {
		return fmt.Errorf("key %q must match [A-Za-z_][A-Za-z0-9_]* and have at most %d characters", key, MaxKeyLength)
	}
	if ReservedKey(key) {
		return fmt.Errorf("key %q is reserved: REACTORCIDE_* and RC_WF* keys come from the coordinator", key)
	}
	if len(value) > MaxValueLength {
		return fmt.Errorf("the value of %q has more than %d bytes", key, MaxValueLength)
	}
	if strings.Contains(value, "${secret:") {
		return fmt.Errorf("the value of %q holds a secret reference; environment defaults are not secret, use a secret grant", key)
	}
	if strings.ContainsRune(value, 0) {
		return fmt.Errorf("the value of %q holds a NUL character", key)
	}
	return nil
}

// ValidScope reports whether scope is a known scope.
func ValidScope(scope string) bool {
	switch scope {
	case models.EnvDefaultScopeOrg, models.EnvDefaultScopeWorkerClass, models.EnvDefaultScopeProject:
		return true
	}
	return false
}

// Layers are the defaults of the three scopes of one job.
type Layers struct {
	Org         map[string]string
	WorkerClass map[string]string
	Project     map[string]string
}

// Effective returns the merged defaults: project > worker class >
// organization. Reserved keys are dropped (the database refuses them too).
func (l Layers) Effective() map[string]string {
	out := map[string]string{}
	for _, layer := range []map[string]string{l.Org, l.WorkerClass, l.Project} {
		for key, value := range layer {
			if ReservedKey(key) {
				continue
			}
			out[key] = value
		}
	}
	return out
}

// Apply adds the defaults to env where env has no value for the key. The
// job environment (job and workflow variables, and the coordinator values)
// always wins. It returns the keys that it added.
func Apply(env map[string]string, defaults map[string]string) []string {
	added := []string{}
	for key, value := range defaults {
		if ReservedKey(key) {
			continue
		}
		if _, present := env[key]; present {
			continue
		}
		env[key] = value
		added = append(added, key)
	}
	return added
}
