package transportsecurity

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func ValidateURL(rawURL string, allowInsecure bool, purpose string) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		return fmt.Errorf("%s has an invalid URL", purpose)
	}
	secure := strings.EqualFold(u.Scheme, "https")
	if !secure && !strings.EqualFold(u.Scheme, "http") {
		return fmt.Errorf("%s URL must use https", purpose)
	}
	if !secure && !allowInsecure {
		return fmt.Errorf("%s requires an encrypted, peer-authenticated transport; use --allow-insecure-transport only for an isolated development network", purpose)
	}
	return nil
}

func HTTPClient(base *http.Client, allowInsecure bool, purpose string) *http.Client {
	client := &http.Client{}
	if base != nil {
		*client = *base
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
