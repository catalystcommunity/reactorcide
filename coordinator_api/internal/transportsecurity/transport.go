// Package transportsecurity enforces the security properties required when
// Reactorcide sends credentials or job secrets over a network transport.
package transportsecurity

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Properties describes security supplied by a transport. Native CSIL-RPC
// carriers can use Validate directly. HTTP carriers use ValidateURL.
type Properties struct {
	Encrypted         bool
	PeerAuthenticated bool
}

// Validate rejects a transport that does not encrypt data and authenticate
// its peer. allowInsecure must come from an explicit command-line option.
func Validate(properties Properties, allowInsecure bool, purpose string) error {
	if properties.Encrypted && properties.PeerAuthenticated {
		return nil
	}
	if allowInsecure {
		return nil
	}
	return fmt.Errorf("%s requires an encrypted, peer-authenticated transport; use --allow-insecure-transport only for an isolated development network", purpose)
}

// ValidateURL applies the transport rule to an HTTP carrier. HTTPS supplies
// both properties when Go performs its normal certificate validation.
func ValidateURL(rawURL string, allowInsecure bool, purpose string) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("%s has an invalid URL: %w", purpose, err)
	}
	if u.Host == "" {
		return fmt.Errorf("%s URL must include a host", purpose)
	}
	secure := strings.EqualFold(u.Scheme, "https")
	if !secure && !strings.EqualFold(u.Scheme, "http") {
		return fmt.Errorf("%s URL must use https", purpose)
	}
	return Validate(Properties{Encrypted: secure, PeerAuthenticated: secure}, allowInsecure, purpose)
}

// HTTPClient returns a client that also applies the rule to every redirect.
// This prevents an HTTPS request from forwarding credentials to HTTP.
func HTTPClient(timeoutClient *http.Client, allowInsecure bool, purpose string) *http.Client {
	client := &http.Client{}
	if timeoutClient != nil {
		*client = *timeoutClient
	}
	previous := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if err := ValidateURL(req.URL.String(), allowInsecure, purpose); err != nil {
			return err
		}
		if previous != nil {
			return previous(req, via)
		}
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		return nil
	}
	return client
}
