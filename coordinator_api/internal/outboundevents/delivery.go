package outboundevents

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// Decrypter decrypts a signing secret. *secrets.MasterKeyManager implements
// it.
type Decrypter interface {
	DecryptWithKey(keyName string, ciphertext []byte) ([]byte, error)
}

// Encrypter encrypts a signing secret. *secrets.MasterKeyManager implements
// it.
type Encrypter interface {
	EncryptWithPrimary(plaintext []byte) (keyName string, ciphertext []byte, err error)
}

// DefaultMaxAttempts is the attempt limit when neither the subscription nor
// the coordinator sets one. With the default backoff the last attempt is
// about 3.5 hours after the first.
const DefaultMaxAttempts = 12

// Backoff is the retry delay policy: Base * 2^(attempt-1), at most Max,
// with +-Jitter (a fraction) of random spread.
type Backoff struct {
	Base   time.Duration
	Max    time.Duration
	Jitter float64
	// Rand returns a value in [0, 1). Tests replace it.
	Rand func() float64
}

// DefaultBackoff: 10s, 20s, 40s, ... at most one hour, +-20%.
var DefaultBackoff = Backoff{Base: 10 * time.Second, Max: time.Hour, Jitter: 0.2}

// Delay returns the wait after the given failed attempt (1 for the first).
func (b Backoff) Delay(attempt int) time.Duration {
	base, max := b.Base, b.Max
	if base <= 0 {
		base = DefaultBackoff.Base
	}
	if max <= 0 {
		max = DefaultBackoff.Max
	}
	if attempt < 1 {
		attempt = 1
	}
	delay := float64(base) * math.Pow(2, float64(attempt-1))
	if delay > float64(max) {
		delay = float64(max)
	}
	if b.Jitter > 0 {
		r := rand.Float64
		if b.Rand != nil {
			r = b.Rand
		}
		delay *= 1 + b.Jitter*(2*r()-1)
	}
	return time.Duration(delay)
}

// DeliveryLoop claims delivery tasks and calls the sinks. It runs on every
// coordinator replica; Corndogs gives each task to one claimer.
type DeliveryLoop struct {
	Store     store.OutboundEventStore
	Queue     TaskQueue
	QueueName string
	Sinks     map[string]Sink
	Secrets   Decrypter
	// MaxAttempts applies when the subscription does not set one.
	MaxAttempts int
	Backoff     Backoff
	// Workers is the number of concurrent claim loops. Default 2.
	Workers int
	// Idle is the wait after an empty claim. Default 1s.
	Idle time.Duration
	Now  func() time.Time
}

func (l *DeliveryLoop) now() time.Time {
	if l.Now != nil {
		return l.Now().UTC()
	}
	return time.Now().UTC()
}

func (l *DeliveryLoop) queueName() string {
	if l.QueueName != "" {
		return l.QueueName
	}
	return corndogs.OutboundEventsQueue
}

// Run claims and delivers until ctx ends.
func (l *DeliveryLoop) Run(ctx context.Context) {
	workers := l.Workers
	if workers <= 0 {
		workers = 2
	}
	idle := l.Idle
	if idle <= 0 {
		idle = time.Second
	}
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				worked, err := l.RunOnce(ctx)
				if err != nil && ctx.Err() == nil {
					logging.Log.WithError(err).Warn("Outbound event delivery failed")
				}
				if !worked || err != nil {
					select {
					case <-ctx.Done():
					case <-time.After(idle):
					}
				}
			}
		}()
	}
	wg.Wait()
}

// RunOnce claims one task and handles it. worked is false when the queue was
// empty.
func (l *DeliveryLoop) RunOnce(ctx context.Context) (bool, error) {
	task, err := l.Queue.Claim(ctx, l.queueName())
	if err != nil {
		return false, err
	}
	if task == nil {
		return false, nil
	}
	err = l.handle(ctx, task)
	if completeErr := l.Queue.Complete(ctx, task); completeErr != nil && err == nil {
		err = completeErr
	}
	return true, err
}

func (l *DeliveryLoop) maxAttempts(sub *models.EventSubscription) int {
	if sub.MaxAttempts > 0 {
		return sub.MaxAttempts
	}
	if l.MaxAttempts > 0 {
		return l.MaxAttempts
	}
	return DefaultMaxAttempts
}

func (l *DeliveryLoop) handle(ctx context.Context, task *corndogs.ClaimedTask) error {
	var payload TaskPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil || payload.DeliveryID == "" || payload.TaskKey == "" {
		DeliveriesTotal.WithLabelValues("", "stale").Inc()
		return nil
	}
	work, ok, err := l.Store.BeginDeliverySend(ctx, payload.DeliveryID, payload.TaskKey)
	if err != nil {
		return err
	}
	if !ok {
		// An old task (reset, replayed, or a duplicate): no send.
		DeliveriesTotal.WithLabelValues("", "stale").Inc()
		return nil
	}
	sub := &work.Subscription
	sinkType := sub.SinkType
	result := models.DeliveryAttemptResult{DeliveryID: work.Delivery.DeliveryID, TaskKey: payload.TaskKey, StartedAt: l.now()}

	var outcome Outcome
	sink := l.Sinks[sinkType]
	switch {
	case sink == nil:
		outcome = Outcome{Err: errors.New("no sink of type " + sinkType + " on this coordinator"), Refused: true}
	case !sub.Enabled || !sub.OwnerActive:
		outcome = Outcome{Err: errors.New("the subscription or its owner principal is disabled"), Refused: true}
	default:
		secret, decErr := l.decrypt(sub)
		if decErr != nil {
			outcome = Outcome{Err: decErr}
			break
		}
		body, marshalErr := json.Marshal(work.Payload)
		if marshalErr != nil {
			outcome = Outcome{Err: marshalErr, Refused: true}
			break
		}
		outcome = sink.Send(ctx, Message{
			EventID: work.Delivery.EventID, EventType: work.Delivery.EventType, DeliveryID: work.Delivery.DeliveryID,
			SchemaVersion: SchemaVersion, Body: body, Secret: secret, Config: sub.SinkConfig,
		})
		DeliveryLatency.WithLabelValues(sinkType).Observe(outcome.Latency.Seconds())
	}
	result.FinishedAt = l.now()
	result.LatencyMs = int(outcome.Latency / time.Millisecond)
	if outcome.StatusCode != 0 {
		status := outcome.StatusCode
		result.HTTPStatus = &status
	}
	metricResult := ""
	switch {
	case outcome.Delivered():
		result.Result = models.DeliveryAttemptDelivered
		result.NextStatus = models.DeliveryStatusDelivered
		metricResult = "delivered"
	case outcome.Refused:
		result.Result = models.DeliveryAttemptRefused
		result.NextStatus = models.DeliveryStatusDeadLetter
		metricResult = "refused"
	case work.Delivery.Attempts >= l.maxAttempts(sub):
		result.Result = models.DeliveryAttemptFailed
		result.NextStatus = models.DeliveryStatusDeadLetter
		metricResult = "dead_letter"
	default:
		result.Result = models.DeliveryAttemptFailed
		result.NextStatus = models.DeliveryStatusPending
		result.NextAttemptAt = l.now().Add(l.Backoff.Delay(work.Delivery.Attempts))
		metricResult = "failed"
	}
	if outcome.Err != nil {
		result.Error = outcome.Err.Error()
	}
	DeliveriesTotal.WithLabelValues(sinkType, metricResult).Inc()
	return l.Store.FinishDeliveryAttempt(ctx, result)
}

func (l *DeliveryLoop) decrypt(sub *models.EventSubscription) ([]byte, error) {
	if l.Secrets == nil {
		return nil, errors.New("the coordinator has no master keys to read the signing secret")
	}
	secret, err := l.Secrets.DecryptWithKey(sub.SigningSecretKeyName, sub.SigningSecretCiphertext)
	if err != nil {
		return nil, errors.New("failed to decrypt the signing secret")
	}
	return secret, nil
}
