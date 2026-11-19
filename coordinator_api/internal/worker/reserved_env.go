package worker

import "strings"

// Event facts are environment variables that the coordinator sets from the
// webhook, the eval job, and its own records. Guards and policy read them, so
// repository YAML and trigger output must not set or replace them. Runnerlib
// rejects them in workflow and job YAML with a clear error; the coordinator
// enforces the same rule here, because the trigger output comes from inside
// a job container and is not an authority.
//
// Keep this list the same as _RESERVED_EVENT_ENV in runnerlib/src/eval.py.
var reservedEventEnvNames = map[string]bool{
	"REACTORCIDE_ACTOR_SUBJECTS":     true,
	"REACTORCIDE_CI_APPROVALS":       true,
	"REACTORCIDE_RUN_AUTHORITY":      true,
	"REACTORCIDE_CI_POLICY":          true,
	"REACTORCIDE_CI_POLICY_REVISION": true,
	"REACTORCIDE_DIFF_BASE":          true,
	"REACTORCIDE_SHA":                true,
	"REACTORCIDE_SOURCE_URL":         true,
	"REACTORCIDE_EVENT_ID":           true,
	"REACTORCIDE_EVENT_TYPE":         true,
	"REACTORCIDE_BRANCH":             true,
	"REACTORCIDE_REPO":               true,
	"REACTORCIDE_PROVIDER":           true,
	"REACTORCIDE_IS_FORK_PR":         true,
	"REACTORCIDE_JOB_KIND":           true,
	"REACTORCIDE_EVAL_DUAL_CI":       true,
	"REACTORCIDE_REPORT_GENERATION":  true,
	"REACTORCIDE_REPORT_TARGET_ID":   true,
	"REACTORCIDE_REF_CLASSES":        true,
	// The cache decision of the coordinator (docs/builds-and-caches.md).
	"REACTORCIDE_CACHE_NAMESPACE":    true,
	"REACTORCIDE_CACHE_WRITABLE":     true,
	"REACTORCIDE_ARTIFACT_NAMESPACE": true,
}

// authorityEnvNames are the reserved facts that decide trust: approvals,
// actor subjects, and the policy snapshot. A trigger can never supply them,
// also when its parent does not have them.
var authorityEnvNames = map[string]bool{
	"REACTORCIDE_ACTOR_SUBJECTS":     true,
	"REACTORCIDE_CI_APPROVALS":       true,
	"REACTORCIDE_RUN_AUTHORITY":      true,
	"REACTORCIDE_CI_POLICY":          true,
	"REACTORCIDE_CI_POLICY_REVISION": true,
	"REACTORCIDE_REF_CLASSES":        true,
	"REACTORCIDE_CACHE_NAMESPACE":    true,
	"REACTORCIDE_CACHE_WRITABLE":     true,
	"REACTORCIDE_ARTIFACT_NAMESPACE": true,
}

// reservedEventEnvPrefixes covers the head, base, and pull request facts
// (REACTORCIDE_HEAD_ACTOR, REACTORCIDE_BASE_REF, REACTORCIDE_PR_NUMBER, ...).
var reservedEventEnvPrefixes = []string{"REACTORCIDE_HEAD_", "REACTORCIDE_BASE_", "REACTORCIDE_PR_"}

// isReservedEventEnv reports whether name is a coordinator event fact. The
// match is case-insensitive, because a container environment may be read
// without regard to case on some platforms.
func isReservedEventEnv(name string) bool {
	upper := strings.ToUpper(strings.TrimSpace(name))
	if reservedEventEnvNames[upper] {
		return true
	}
	for _, prefix := range reservedEventEnvPrefixes {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	return false
}

// triggerMaySupplyEventEnv reports whether a trigger may set the reserved
// name when its parent does not have it. A parent of a CI event has every
// fact the coordinator gave it, so a missing fact stays missing. A parent
// with no CI event (a job submitted through the API) has no commit status
// and no admission facts to protect, so a trigger may give it event context
// such as REACTORCIDE_EVENT_TYPE, as before. Authority facts are never
// supplied by a trigger.
func triggerMaySupplyEventEnv(name string, parentHasEvent bool) bool {
	if authorityEnvNames[strings.ToUpper(strings.TrimSpace(name))] {
		return false
	}
	return !parentHasEvent
}
