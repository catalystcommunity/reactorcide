package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urfave/cli/v2"
)

func runDiagnosisCLI(t *testing.T, serverURL string, args ...string) error {
	t.Helper()
	app := cli.NewApp()
	app.Commands = []*cli.Command{DiagnosisCommand}
	full := append([]string{"reactorcide", "diagnosis", args[0],
		"--api-url", serverURL, "--token", "fake-test-token-not-real", "--allow-insecure-transport"}, args[1:]...)
	return app.Run(NormalizeArgs(app, full))
}

func TestDiagnosisSetupCreatesTheSubscription(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/organizations/acme/principals/diag-bot":
			_ = json.NewEncoder(w).Encode(map[string]any{"principal_id": "p-1", "kind": "service", "name": "diag-bot"})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/organizations/acme/subscriptions":
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"subscription_id": "sub-1", "name": body["name"], "scope": "org", "enabled": true})
		default:
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	err := runDiagnosisCLI(t, server.URL, "setup", "--org", "acme", "--principal", "diag-bot",
		"--url", "https://diag.example.com/hook", "--format", "json")
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	if body["owner_principal_id"] != "p-1" || body["name"] != "diagnosis-diag-bot" || body["scope"] != "org" ||
		body["webhook_url"] != "https://diag.example.com/hook" {
		t.Fatalf("body = %#v", body)
	}
	types, _ := body["event_types"].([]any)
	classes, _ := body["failure_classes"].([]any)
	if len(types) != 1 || types[0] != "node.post_complete" || len(classes) != 3 || classes[0] != "job" {
		t.Fatalf("filters = %#v %#v", body["event_types"], body["failure_classes"])
	}
}

func TestDiagnosisSetupRefusesAHuman(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("no subscription for a human: %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"principal_id": "p-2", "kind": "human", "name": "alice"})
	}))
	defer server.Close()
	err := runDiagnosisCLI(t, server.URL, "setup", "--org", "acme", "--principal", "alice", "--url", "https://x.example.com")
	if err == nil || !strings.Contains(err.Error(), "service principal") {
		t.Fatalf("err = %v", err)
	}
}

func TestDiagnosisPostSendsTheEntry(t *testing.T) {
	var body map[string]any
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Fatalf("method = %s", r.Method)
		}
		path = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"diagnosis_id": "d-1", "target_type": "job", "title": body["title"],
			"revision": 1, "created": true, "author": map[string]any{"principal_id": "p-1", "kind": "service", "display_name": "Diag"}})
	}))
	defer server.Close()
	bodyFile := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(bodyFile, []byte("The test ran out of memory."), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runDiagnosisCLI(t, server.URL, "post", "--workflow", "wf-1", "--node", "build", "--title", "OOM",
		"--body-file", bodyFile, "--confidence", "high", "--link", "log=https://example.com/log",
		"--shift", "memory=8Gi", "--shift", "timeout=2h", "--format", "json")
	if err != nil {
		t.Fatalf("post failed: %v", err)
	}
	if path != "/api/v1/workflows/wf-1/nodes/build/diagnosis" {
		t.Fatalf("path = %s", path)
	}
	shifts, _ := body["suggested_shifts"].(map[string]any)
	links, _ := body["links"].([]any)
	if body["body"] != "The test ran out of memory." || body["confidence"] != "high" || shifts["memory"] != "8Gi" || len(links) != 1 {
		t.Fatalf("body = %#v", body)
	}

	if err := runDiagnosisCLI(t, server.URL, "post", "--job", "j", "--title", "x", "--shift", "image=evil"); err == nil {
		t.Fatal("a shift that is not a re-run shift is refused before the request")
	}
	if err := runDiagnosisCLI(t, server.URL, "post", "--job", "j", "--workflow", "w", "--title", "x"); err == nil {
		t.Fatal("two targets are refused")
	}
	if err := runDiagnosisCLI(t, server.URL, "post", "--node", "n", "--title", "x"); err == nil {
		t.Fatal("--node needs --workflow")
	}
}

func TestDiagnosisListReadsTheEntries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/jobs/job-1/diagnosis" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"diagnoses": []any{map[string]any{"diagnosis_id": "d-1", "title": "t"}}})
	}))
	defer server.Close()
	if err := runDiagnosisCLI(t, server.URL, "list", "--job", "job-1"); err != nil {
		t.Fatalf("list failed: %v", err)
	}
}
