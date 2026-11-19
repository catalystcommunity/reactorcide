package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/jobcontrol"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/metrics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/principals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/ratelimit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
)

var (
	webhookLimiterOnce sync.Once
	webhookLimiterMu   sync.RWMutex
	webhookLimiter     *ratelimit.Limiter
)

// currentWebhookLimiter returns the per-project webhook limiter. It is built
// from the environment the first time (config_ratelimit.go).
func currentWebhookLimiter() *ratelimit.Limiter {
	webhookLimiterOnce.Do(func() {
		webhookLimiterMu.Lock()
		if webhookLimiter == nil {
			webhookLimiter = ratelimit.New(config.WebhookRateLimitPerMinute, config.WebhookRateLimitBurst)
		}
		webhookLimiterMu.Unlock()
	})
	webhookLimiterMu.RLock()
	defer webhookLimiterMu.RUnlock()
	return webhookLimiter
}

// SetWebhookRateLimiter replaces the webhook limiter (nil: no limit). Tests
// use it.
func SetWebhookRateLimiter(limiter *ratelimit.Limiter) {
	webhookLimiterOnce.Do(func() {})
	webhookLimiterMu.Lock()
	webhookLimiter = limiter
	webhookLimiterMu.Unlock()
}

// writeTooManyRequests writes HTTP 429 with Retry-After.
func writeTooManyRequests(w http.ResponseWriter, wait time.Duration, message string) {
	w.Header().Set("Retry-After", strconv.Itoa(ratelimit.RetryAfterSeconds(wait)))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "rate_limited", "message": message})
}

// refuseRateLimitedWebhook applies the per-project webhook limit to a pull
// request or push delivery that would start an eval. Over the limit, it
// records the event with status rate_limited (so a redelivery of the same
// delivery evaluates it), posts the reason in "Reactorcide Checks", and
// writes 429 with Retry-After. It returns true when it wrote the response.
func (h *WebhookHandler) refuseRateLimitedWebhook(w http.ResponseWriter, project *models.Project, event *vcs.WebhookEvent) bool {
	limiter := currentWebhookLimiter()
	if limiter == nil || project == nil || event == nil || event.Coordinator != nil {
		return false
	}
	switch {
	case event.PullRequest != nil:
		if !project.ShouldProcessEvent(string(event.GenericEvent), event.PullRequest.BaseRef) {
			return false
		}
	case event.Push != nil:
		if event.Push.Deleted || !project.ShouldProcessEvent(string(event.GenericEvent), extractBranchOrTag(event.Push.Ref)) {
			return false
		}
	default:
		return false
	}
	allowed, wait := limiter.Allow(project.ProjectID)
	if allowed {
		return false
	}
	metrics.RecordRateLimited("webhook")
	h.recordRateLimitedEvent(context.Background(), project, event, wait)
	writeTooManyRequests(w, wait, "the project is over its webhook rate limit; the event is recorded as rate_limited, redeliver it to run")
	return true
}

func (h *WebhookHandler) recordRateLimitedEvent(ctx context.Context, project *models.Project, event *vcs.WebhookEvent, wait time.Duration) {
	events, ok := h.store.(store.CIEventStore)
	if !ok {
		return
	}
	ciEvent := ciEventFromWebhook(project, event)
	ciEvent.Status = models.CIEventStatusRateLimited
	ciEvent.Summary.SkipReasons = []models.DecisionReason{{Kind: models.SkipReasonRateLimit,
		Detail: "over the project webhook rate limit; redeliver the webhook to run"}}
	principals.ApplyToEvent(ciEvent, h.webhookActor(ctx, event))
	applyRefClassesToEvent(ctx, h.store, ciEvent, webhookRefClassFacts(event))
	recorded, outcome, err := events.RecordCIEvent(ctx, ciEvent)
	if err != nil {
		h.logger.WithError(err).WithField("project", project.Name).Warn("Could not record a rate-limited webhook event")
		return
	}
	if outcome != store.CIEventCreated {
		// A redelivery of a known event: the first row stays as it is.
		return
	}
	h.logger.WithField("event_id", recorded.EventID).WithField("project", project.Name).
		WithField("retry_after_seconds", ratelimit.RetryAfterSeconds(wait)).Warn("Webhook event over the project rate limit; recorded as rate_limited")
	audit.Record(ctx, h.store, project.OwnershipOrgID(), "ci_event.rate_limited", "ci_event", recorded.EventID,
		models.JSONB{"project_id": project.ProjectID, "event_type": recorded.EventType})
	if err := jobcontrol.PublishEventChecks(ctx, recorded.EventID); err != nil {
		h.logger.WithError(err).WithField("event_id", recorded.EventID).Warn("Could not post the checks status of a rate-limited event")
	}
}
