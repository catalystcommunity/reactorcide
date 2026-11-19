package outboundevents

import (
	"net/url"
	"time"
)

// URLHost returns the host of a URL, for logs and audit records (a query can
// carry a token).
func URLHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

// RuntimeConfig is the coordinator configuration of outbound events.
type RuntimeConfig struct {
	AllowInsecure   bool
	AllowedNetworks string
	Timeout         time.Duration
}

// NewSinks returns the sinks of this coordinator.
func NewSinks(cfg RuntimeConfig) (map[string]Sink, error) {
	networks, err := ParseAllowedNetworks(cfg.AllowedNetworks)
	if err != nil {
		return nil, err
	}
	return map[string]Sink{
		"webhook": &WebhookSink{Policy: DestinationPolicy{AllowInsecure: cfg.AllowInsecure, AllowedNetworks: networks}, Timeout: cfg.Timeout},
	}, nil
}
