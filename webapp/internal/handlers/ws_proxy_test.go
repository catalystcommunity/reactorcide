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

func TestWSProxyRejectsBrowserUpgradeWhenUpstreamRejects(t *testing.T) {
	oldURL := config.APIUrl
	oldToken := config.APIToken
	oldAllowInsecure := config.AllowInsecureTransport
	t.Cleanup(func() {
		config.APIUrl = oldURL
		config.APIToken = oldToken
		config.AllowInsecureTransport = oldAllowInsecure
	})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	t.Cleanup(upstream.Close)
	config.APIUrl = upstream.URL
	config.APIToken = "test-token"
	config.AllowInsecureTransport = true

	proxy := NewWSProxy()
	proxy.logger.SetOutput(io.Discard)
	server := httptest.NewServer(http.HandlerFunc(proxy.AllJobsStream))
	t.Cleanup(server.Close)

	conn, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
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

func TestWSProxyConnectsUpstreamBeforeForwardingEvents(t *testing.T) {
	oldURL := config.APIUrl
	oldToken := config.APIToken
	oldAllowInsecure := config.AllowInsecureTransport
	t.Cleanup(func() {
		config.APIUrl = oldURL
		config.APIToken = oldToken
		config.AllowInsecureTransport = oldAllowInsecure
	})

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"job_id":"job-1"}`))
	}))
	t.Cleanup(upstream.Close)
	config.APIUrl = upstream.URL
	config.APIToken = "test-token"
	config.AllowInsecureTransport = true

	proxy := NewWSProxy()
	proxy.logger.SetOutput(io.Discard)
	server := httptest.NewServer(http.HandlerFunc(proxy.AllJobsStream))
	t.Cleanup(server.Close)

	conn, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		t.Fatalf("browser WebSocket dial failed with status %d: %v", status, err)
	}
	defer conn.Close()

	_, message, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read forwarded event: %v", err)
	}
	if string(message) != `{"job_id":"job-1"}` {
		t.Fatalf("message = %q, want job event", message)
	}
}
