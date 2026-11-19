package outboundevents

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics have low cardinality: no subscription, workflow, or project label.
var (
	// DeliveriesTotal counts sends by result: delivered, failed (a retry
	// follows), dead_letter, refused, stale (a task with an old key).
	DeliveriesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "reactorcide_outbound_event_deliveries_total",
		Help: "Outbound event delivery attempts by result",
	}, []string{"sink", "result"})
	// DeliveryLatency is the time of one send to the sink.
	DeliveryLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "reactorcide_outbound_event_delivery_latency_seconds",
		Help:    "Time of one outbound event send to its sink",
		Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
	}, []string{"sink"})
	// OutboxLag is the age of the oldest event that the dispatcher did not
	// dispatch. 0 when the outbox is empty.
	OutboxLag = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "reactorcide_outbound_event_outbox_lag_seconds",
		Help: "Age of the oldest undispatched outbound event",
	})
	// DispatchedTotal counts outbox rows by outcome: matched (one or more
	// deliveries) or unmatched.
	DispatchedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "reactorcide_outbound_events_dispatched_total",
		Help: "Outbox events that the dispatcher processed, by outcome",
	}, []string{"outcome"})
)
