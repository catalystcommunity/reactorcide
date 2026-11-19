package handlers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/clientaddr"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/ratelimit"
	"github.com/stretchr/testify/assert"
)

// The token exchange is limited per source address, before any other work.
func TestFederationExchangeSourceLimit(t *testing.T) {
	h := NewFederationHandler(nil)
	h.SourceLimiter = ratelimit.New(1, 1)
	exchange := func(remote string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/federation/token", bytes.NewBufferString(`{}`))
		req.RemoteAddr = remote
		w := httptest.NewRecorder()
		h.Exchange(w, req)
		return w.Code
	}
	assert.Equal(t, http.StatusServiceUnavailable, exchange("192.0.2.1:1000"), "the first request passes the limit")
	assert.Equal(t, http.StatusTooManyRequests, exchange("192.0.2.1:1001"), "the port is not part of the key")
	assert.Equal(t, http.StatusServiceUnavailable, exchange("192.0.2.2:1000"), "another address has its own bucket")
}

// Behind a trusted proxy, the source limit keys on the right-most
// X-Forwarded-For address that is not a trusted proxy. From an untrusted
// peer, the header does not change the key.
func TestFederationExchangeSourceLimitBehindTrustedProxy(t *testing.T) {
	h := NewFederationHandler(nil)
	h.SourceLimiter = ratelimit.New(1, 1)
	trusted, err := clientaddr.Parse("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	h.TrustedProxies = trusted
	exchange := func(remote, forwarded string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/federation/token", bytes.NewBufferString(`{}`))
		req.RemoteAddr = remote
		if forwarded != "" {
			req.Header.Set("X-Forwarded-For", forwarded)
		}
		w := httptest.NewRecorder()
		h.Exchange(w, req)
		return w.Code
	}
	assert.Equal(t, http.StatusServiceUnavailable, exchange("10.0.0.5:1000", "198.51.100.1"))
	assert.Equal(t, http.StatusServiceUnavailable, exchange("10.0.0.5:1000", "198.51.100.2"), "each client behind the ingress has its own bucket")
	assert.Equal(t, http.StatusTooManyRequests, exchange("10.0.0.6:1000", "203.0.113.9, 198.51.100.1"), "a client-set left entry does not change the key")
	assert.Equal(t, http.StatusServiceUnavailable, exchange("192.0.2.1:1000", "198.51.100.1"), "an untrusted peer is its own key")
	assert.Equal(t, http.StatusTooManyRequests, exchange("192.0.2.1:1000", "198.51.100.77"), "an untrusted peer cannot change its key with the header")
}
