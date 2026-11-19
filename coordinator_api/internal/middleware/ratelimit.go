package middleware

import (
	"net/http"
	"strconv"
	"sync"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/metrics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/ratelimit"
)

var (
	apiLimiterOnce sync.Once
	apiLimiterMu   sync.RWMutex
	apiLimiter     *ratelimit.Limiter
)

func currentAPILimiter() *ratelimit.Limiter {
	apiLimiterOnce.Do(func() {
		apiLimiterMu.Lock()
		if apiLimiter == nil {
			apiLimiter = ratelimit.New(config.APIRateLimitPerMinute, config.APIRateLimitBurst)
		}
		apiLimiterMu.Unlock()
	})
	apiLimiterMu.RLock()
	defer apiLimiterMu.RUnlock()
	return apiLimiter
}

// SetAPIRateLimiter replaces the per-principal API limiter (nil: no limit).
// Tests use it.
func SetAPIRateLimiter(limiter *ratelimit.Limiter) {
	apiLimiterOnce.Do(func() {})
	apiLimiterMu.Lock()
	apiLimiter = limiter
	apiLimiterMu.Unlock()
}

// apiRateLimitKey is the bucket of a principal: its principal ID, else its
// credential.
func apiRateLimitKey(principal *checkauth.Principal) string {
	if principal.PrincipalID != "" {
		return "principal:" + principal.PrincipalID
	}
	return "credential:" + principal.CredentialID
}

// refuseRateLimitedAPI applies the per-principal API limit (plan item D4).
// It writes 429 with Retry-After and returns true when the principal is over
// the limit. The limit is off unless REACTORCIDE_API_RATE_LIMIT_PER_MINUTE is
// set.
func refuseRateLimitedAPI(w http.ResponseWriter, principal *checkauth.Principal) bool {
	limiter := currentAPILimiter()
	if limiter == nil || principal == nil {
		return false
	}
	allowed, wait := limiter.Allow(apiRateLimitKey(principal))
	if allowed {
		return false
	}
	metrics.RecordRateLimited("api")
	w.Header().Set("Retry-After", strconv.Itoa(ratelimit.RetryAfterSeconds(wait)))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	w.Write([]byte(`{"error":"rate_limited","message":"Too many requests for this principal; retry after the Retry-After interval"}`))
	return true
}
