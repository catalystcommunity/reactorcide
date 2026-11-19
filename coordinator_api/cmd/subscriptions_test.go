package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/urfave/cli/v2"
)

func runSubscriptionsCLI(t *testing.T, serverURL string, args ...string) error {
	t.Helper()
	app := cli.NewApp()
	app.Commands = []*cli.Command{SubscriptionsCommand}
	full := append([]string{"reactorcide", "subscriptions", args[0],
		"--api-url", serverURL, "--token", "fake-test-token-not-real", "--allow-insecure-transport", "--org", "acme"}, args[1:]...)
	return app.Run(NormalizeArgs(app, full))
}

func TestSubscriptionsCreateSendsFiltersAndSecretFile(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/organizations/acme/subscriptions" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"subscription_id": "sub-1", "name": "diag", "scope": "project", "enabled": true})
	}))
	defer server.Close()
	secretFile := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secretFile, []byte("a-long-enough-test-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runSubscriptionsCLI(t, server.URL, "create", "--name", "diag", "--scope", "project", "--project", "proj-1",
		"--url", "https://hooks.example.com/x", "--event-type", "node.post_complete", "--result", "failed",
		"--secret-file", secretFile, "--format", "json")
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if body["project_id"] != "proj-1" || body["scope"] != "project" || body["webhook_url"] != "https://hooks.example.com/x" {
		t.Fatalf("body = %#v", body)
	}
	if body["signing_secret"] != "a-long-enough-test-secret" {
		t.Fatalf("the secret file is read and trimmed, got %#v", body["signing_secret"])
	}
	if types, _ := body["event_types"].([]any); len(types) != 1 || types[0] != "node.post_complete" {
		t.Fatalf("event_types = %#v", body["event_types"])
	}
}

func TestSubscriptionsDisableAndClearFilter(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/api/v1/organizations/acme/subscriptions/sub-1" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		_ = json.NewEncoder(w).Encode(map[string]any{"subscription_id": "sub-1"})
	}))
	defer server.Close()
	if err := runSubscriptionsCLI(t, server.URL, "disable", "sub-1", "--format", "json"); err != nil {
		t.Fatal(err)
	}
	if err := runSubscriptionsCLI(t, server.URL, "update", "sub-1", "--clear", "result", "--format", "json"); err != nil {
		t.Fatal(err)
	}
	if bodies[0]["enabled"] != false {
		t.Fatalf("disable body = %#v", bodies[0])
	}
	if results, ok := bodies[1]["results"].([]any); !ok || len(results) != 0 {
		t.Fatalf("clear sends an empty list, got %#v", bodies[1])
	}
}

func TestSubscriptionsReplayAndDeliveries(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		switch r.URL.Path {
		case "/api/v1/organizations/acme/subscriptions/sub-1/replay-dead-letters":
			_ = json.NewEncoder(w).Encode(map[string]any{"replayed": 3})
		case "/api/v1/organizations/acme/subscriptions/sub-1/deliveries/del-1/replay":
			_ = json.NewEncoder(w).Encode(map[string]any{"delivery_id": "del-1", "status": "pending"})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"deliveries": []any{}})
		}
	}))
	defer server.Close()
	if err := runSubscriptionsCLI(t, server.URL, "replay", "sub-1", "--all-dead-letters"); err != nil {
		t.Fatal(err)
	}
	if err := runSubscriptionsCLI(t, server.URL, "replay", "sub-1", "del-1"); err != nil {
		t.Fatal(err)
	}
	if err := runSubscriptionsCLI(t, server.URL, "deliveries", "sub-1", "--status", "dead_letter"); err != nil {
		t.Fatal(err)
	}
	if err := runSubscriptionsCLI(t, server.URL, "replay", "sub-1"); err == nil {
		t.Fatal("replay needs a delivery or --all-dead-letters")
	}
	want := []string{
		"POST /api/v1/organizations/acme/subscriptions/sub-1/replay-dead-letters?",
		"POST /api/v1/organizations/acme/subscriptions/sub-1/deliveries/del-1/replay?",
		"GET /api/v1/organizations/acme/subscriptions/sub-1/deliveries?limit=50&status=dead_letter",
	}
	if len(paths) != len(want) {
		t.Fatalf("paths = %v", paths)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("path %d = %q, want %q", i, paths[i], want[i])
		}
	}
}
