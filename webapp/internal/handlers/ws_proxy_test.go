package handlers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/webapp/internal/config"
	"github.com/gorilla/websocket"
)

func withUpstreamWS(t *testing.T, upstream *httptest.Server) {
	t.Helper()
	previousURL, previousInsecure := config.APIUrl, config.AllowInsecureTransport
	config.APIUrl = upstream.URL
	config.AllowInsecureTransport = true
	t.Cleanup(func() {
		config.APIUrl, config.AllowInsecureTransport = previousURL, previousInsecure
	})
}

// dialProxy opens a browser-side connection to the proxy, optionally carrying a
// session cookie.
func dialProxy(t *testing.T, server *httptest.Server, sessionToken string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	header := http.Header{}
	if sessionToken != "" {
		header.Set("Cookie", sessionCookieName+"="+sessionToken)
	}
	return websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), header)
}

func TestWSProxyRejectsBrowserUpgradeWhenUpstreamRejects(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	t.Cleanup(upstream.Close)
	withUpstreamWS(t, upstream)

	proxy := NewWSProxy()
	proxy.logger.SetOutput(io.Discard)
	server := httptest.NewServer(http.HandlerFunc(proxy.UIStream))
	t.Cleanup(server.Close)

	conn, response, err := dialProxy(t, server, "session")
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("browser WebSocket upgrade succeeded even though the upstream rejected it")
	}
	if response == nil || response.StatusCode != http.StatusBadGateway {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		t.Fatalf("status = %d, want %d", status, http.StatusBadGateway)
	}
}

// TestWSProxyForwardsTheBrowserSessionNotAServiceToken is the security property
// of this proxy.
//
// It used to dial upstream with the webapp's own coordinator API token, which
// meant the coordinator saw a privileged service rather than the real user, and
// the webapp then had to drop unauthorized frames itself. Now the browser's own
// session token is what authenticates upstream, so the coordinator filters every
// frame against the actual caller.
func TestWSProxyForwardsTheBrowserSessionNotAServiceToken(t *testing.T) {
	seenAuth := make(chan string, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth <- r.Header.Get("Authorization")
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"job_update","job_id":"job-1"}`))
	}))
	t.Cleanup(upstream.Close)
	withUpstreamWS(t, upstream)

	// There is no service token to accidentally forward any more:
	// config.APIToken was deleted along with the REST client that used it, so
	// "the webapp forwards its own credential" is now a compile error rather
	// than a test assertion. What remains testable, and is asserted below, is
	// that the credential forwarded IS the browser's session.

	proxy := NewWSProxy()
	proxy.logger.SetOutput(io.Discard)
	server := httptest.NewServer(http.HandlerFunc(proxy.UIStream))
	t.Cleanup(server.Close)

	conn, response, err := dialProxy(t, server, "the-browser-session")
	if err != nil {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		t.Fatalf("browser WebSocket dial failed with status %d: %v", status, err)
	}
	defer conn.Close()

	got := <-seenAuth
	if got != "Bearer the-browser-session" {
		t.Fatalf("upstream Authorization = %q, want the browser's session token", got)
	}

	_, message, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read forwarded event: %v", err)
	}
	if !strings.Contains(string(message), `"job_id":"job-1"`) {
		t.Fatalf("message = %q, want the upstream event forwarded verbatim", message)
	}
}

// TestWSProxySendsNoAuthWhenLoggedOut covers anonymous watching of public data:
// no cookie must mean no credential rather than a fabricated one.
func TestWSProxySendsNoAuthWhenLoggedOut(t *testing.T) {
	seenAuth := make(chan string, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth <- r.Header.Get("Authorization")
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"job_update"}`))
	}))
	t.Cleanup(upstream.Close)
	withUpstreamWS(t, upstream)

	proxy := NewWSProxy()
	proxy.logger.SetOutput(io.Discard)
	server := httptest.NewServer(http.HandlerFunc(proxy.UIStream))
	t.Cleanup(server.Close)

	conn, _, err := dialProxy(t, server, "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if got := <-seenAuth; got != "" {
		t.Fatalf("upstream Authorization = %q; a logged-out browser must send no credential", got)
	}
}
