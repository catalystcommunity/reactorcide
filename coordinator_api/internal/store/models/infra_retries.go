package models

// DefaultInfraRetries is the organization infra_retries default: one
// automatic retry of an infra.platform failure.
const DefaultInfraRetries = 1

// MaxInfraRetries is the largest infra_retries value an organization or a
// project may set. The CHECK constraints in migration 000047 match it.
const MaxInfraRetries = 5

// MinLostJobRetries is the least retry budget of a lost job
// (infra.worker_lost). The lost-job reconciler had this fixed budget before
// infra_retries existed; a larger infra_retries raises it.
const MinLostJobRetries = 2

// IsValidInfraRetries reports whether value may be stored as infra_retries.
func IsValidInfraRetries(value int) bool {
	return value >= 0 && value <= MaxInfraRetries
}

// EffectiveInfraRetries resolves a project override (nil inherits) against
// its organization value (nil is the default).
func EffectiveInfraRetries(projectSetting, orgSetting *int) int {
	if projectSetting != nil && IsValidInfraRetries(*projectSetting) {
		return *projectSetting
	}
	if orgSetting != nil && IsValidInfraRetries(*orgSetting) {
		return *orgSetting
	}
	return DefaultInfraRetries
}

// InfraRetryBudget returns how many automatic retries a job with this
// failure class gets, given the effective infra_retries setting. Only
// infra.platform and infra.worker_lost are retried. A lost job keeps at
// least MinLostJobRetries. job, timeout, oom, cancelled, infra.image_pull
// and infra.scheduling are never retried automatically: a retry gives the
// same result, or the cause is the pipeline's own.
func InfraRetryBudget(class string, effective int) int {
	switch class {
	case FailureClassPlatform:
		return effective
	case FailureClassWorkerLost:
		if effective > MinLostJobRetries {
			return effective
		}
		return MinLostJobRetries
	}
	return 0
}

// ShouldRetryInfraFailure reports whether a job that failed with class, and
// has used retryCount automatic retries, goes back to the queue.
func ShouldRetryInfraFailure(class string, retryCount, effective int) bool {
	return retryCount < InfraRetryBudget(class, effective)
}
