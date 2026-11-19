package outboundevents

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// Delivery headers of the webhook sink.
const (
	HeaderEventID       = "X-Reactorcide-Event-Id"
	HeaderEventType     = "X-Reactorcide-Event-Type"
	HeaderSchemaVersion = "X-Reactorcide-Schema-Version"
	HeaderTimestamp     = "X-Reactorcide-Timestamp"
	HeaderDeliveryID    = "X-Reactorcide-Delivery-Id"
	HeaderSignature     = "X-Reactorcide-Signature"
)

// Message is one event to send.
type Message struct {
	EventID       string
	EventType     string
	DeliveryID    string
	SchemaVersion int
	Body          []byte
	// Secret is the signing secret of the subscription.
	Secret []byte
	// Config is the sink configuration of the subscription.
	Config models.JSONB
}

// Outcome is the result of one send.
type Outcome struct {
	StatusCode int
	Latency    time.Duration
	Err        error
	// Refused is true when the destination is not permitted. The delivery
	// then goes to dead_letter at once: a retry cannot succeed.
	Refused bool
}

// Delivered reports a successful send.
func (o Outcome) Delivered() bool {
	return o.Err == nil && o.StatusCode >= 200 && o.StatusCode < 300
}

// Sink is one kind of outbound destination. The webhook is the first sink.
//
// TODO(E2): message queue and bus sinks (for example NATS, Kafka, SQS) are
// more implementations of this interface, when a user needs one (D27).
type Sink interface {
	// Validate checks a sink configuration before it is stored.
	Validate(config models.JSONB) error
	// Send sends one message. It must return when ctx ends.
	Send(ctx context.Context, msg Message) Outcome
}

// Sign returns the signature header value for a timestamp and a body:
// "sha256=" + hex(HMAC-SHA256(secret, timestamp + "." + body)).
func Sign(secret []byte, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Verify checks a signature header value in constant time. Receivers can
// use the same algorithm (see docs/outbound-events.md).
func Verify(secret []byte, timestamp string, body []byte, signature string) bool {
	return hmac.Equal([]byte(Sign(secret, timestamp, body)), []byte(signature))
}

// WebhookSink sends a JSON POST to an HTTPS URL.
type WebhookSink struct {
	Policy  DestinationPolicy
	Timeout time.Duration
	// Now is the clock of the signature timestamp; tests replace it.
	Now func() time.Time
	// Transport replaces the HTTP transport in tests. Production leaves it
	// nil: the sink then dials through the destination policy.
	Transport http.RoundTripper

	// The client and its transport are built once, on the first send, so
	// connections are reused across deliveries. Set the fields above before
	// the first Send.
	clientOnce sync.Once
	httpClient *http.Client
}

const maxResponseBody = 64 << 10

func (s *WebhookSink) client() *http.Client {
	s.clientOnce.Do(func() { s.httpClient = s.buildClient() })
	return s.httpClient
}

func (s *WebhookSink) buildClient() *http.Client {
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	transport := s.Transport
	if transport == nil {
		transport = &http.Transport{
			// No proxy from the environment: the destination policy must
			// see the real destination address.
			Proxy:                 nil,
			DialContext:           s.Policy.dialContext,
			TLSHandshakeTimeout:   timeout,
			ResponseHeaderTimeout: timeout,
			MaxIdleConns:          32,
			IdleConnTimeout:       90 * time.Second,
			ForceAttemptHTTP2:     true,
		}
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		// A redirect is a failed delivery. A redirect could move the signed
		// payload to another destination.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Validate checks that config has a permitted URL.
func (s *WebhookSink) Validate(config models.JSONB) error {
	rawURL, _ := config["url"].(string)
	for key := range config {
		if key != "url" {
			return fmt.Errorf("unknown webhook setting %q", key)
		}
	}
	return s.Policy.ValidateURL(rawURL)
}

// Send posts msg.Body to the URL of the configuration.
func (s *WebhookSink) Send(ctx context.Context, msg Message) Outcome {
	rawURL, _ := msg.Config["url"].(string)
	if err := s.Policy.ValidateURL(rawURL); err != nil {
		return Outcome{Err: err, Refused: true}
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	timestamp := strconv.FormatInt(now().UTC().Unix(), 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(msg.Body))
	if err != nil {
		return Outcome{Err: err, Refused: true}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Reactorcide-Events/1")
	req.Header.Set(HeaderEventID, msg.EventID)
	req.Header.Set(HeaderEventType, msg.EventType)
	req.Header.Set(HeaderSchemaVersion, strconv.Itoa(msg.SchemaVersion))
	req.Header.Set(HeaderTimestamp, timestamp)
	if msg.DeliveryID != "" {
		req.Header.Set(HeaderDeliveryID, msg.DeliveryID)
	}
	req.Header.Set(HeaderSignature, Sign(msg.Secret, timestamp, msg.Body))

	started := time.Now()
	resp, err := s.client().Do(req)
	latency := time.Since(started)
	if err != nil {
		return Outcome{Err: sanitizeError(err), Latency: latency, Refused: errors.Is(err, ErrDestinationRefused)}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBody))
	out := Outcome{StatusCode: resp.StatusCode, Latency: latency}
	if !out.Delivered() {
		out.Err = fmt.Errorf("the receiver answered HTTP %d", resp.StatusCode)
	}
	return out
}

// sanitizeError removes the URL from a transport error. A webhook URL can
// carry a token in its query, and the error text is stored in the delivery
// log.
func sanitizeError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return fmt.Errorf("%s: %w", urlErr.Op, urlErr.Err)
	}
	return err
}
