package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// RateLimited counts requests that a rate limit refused (plan item D4). The
// kind label is "webhook" (per project), "api" (per principal), "federation"
// (per source address of a token exchange), or "federation_issuer" (per
// verified issuer). There is no
// project or principal label, to keep the cardinality low; the audit log and
// the ci_events rows name the project of a refused webhook.
var RateLimited = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "reactorcide_rate_limited_total",
		Help: "Requests refused by a rate limit, by kind (webhook, api, federation, federation_issuer)",
	},
	[]string{"kind"},
)

// RecordRateLimited counts one refused request.
func RecordRateLimited(kind string) {
	RateLimited.WithLabelValues(kind).Inc()
}
