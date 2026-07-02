package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/urfave/cli/v2"
)

func TestSecretsCommandSetUsesAPIWhenURLProvided(t *testing.T) {
	var sawRequest bool
	secretValue := "line-one\nline-two\nline-three\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/v1/secrets/value" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
		if r.Header.Get("Authorization") != "Bearer api-token" {
			t.Fatalf("missing bearer token")
		}
		if r.URL.Query().Get("path") != "app" || r.URL.Query().Get("key") != "API_KEY" {
			t.Fatalf("unexpected query: %s", r.URL.RawQuery)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body["value"] != secretValue {
			t.Fatalf("unexpected secret value")
		}
		sawRequest = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	app := cli.NewApp()
	app.Commands = []*cli.Command{SecretsCommand}

	stdin, stdinWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdin pipe: %v", err)
	}
	if _, err := stdinWriter.WriteString(secretValue); err != nil {
		t.Fatalf("write stdin pipe: %v", err)
	}
	if err := stdinWriter.Close(); err != nil {
		t.Fatalf("close stdin pipe: %v", err)
	}
	originalStdin := os.Stdin
	os.Stdin = stdin
	defer func() {
		os.Stdin = originalStdin
		_ = stdin.Close()
	}()

	err = app.Run([]string{
		"reactorcide",
		"secrets",
		"--api-url", server.URL,
		"--token", "api-token",
		"set",
		"--stdin",
		"app", "API_KEY",
	})
	if err != nil {
		t.Fatalf("command failed: %v", err)
	}
	if !sawRequest {
		t.Fatal("expected API request")
	}
}

func TestSecretsCommandUsesAPIEnvVars(t *testing.T) {
	var sawRequest bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/secrets/paths" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
		if r.Header.Get("Authorization") != "Bearer env-token" {
			t.Fatalf("missing bearer token")
		}
		sawRequest = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"paths":["app"]}`))
	}))
	defer server.Close()

	t.Setenv("REACTORCIDE_API_URL", server.URL)
	t.Setenv("REACTORCIDE_API_TOKEN", "env-token")

	app := cli.NewApp()
	app.Commands = []*cli.Command{SecretsCommand}
	if err := app.Run([]string{"reactorcide", "secrets", "list-paths"}); err != nil {
		t.Fatalf("command failed: %v", err)
	}
	if !sawRequest {
		t.Fatal("expected API request")
	}
}

func TestSecretsAPIClientOperations(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer api-token" {
			t.Fatalf("missing bearer token")
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/secrets/init":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"status":"initialized","org_id":"org-1"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/secrets/value":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"value":"test-secret-value"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/secrets":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"keys":["A","B"]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/secrets/paths":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"paths":["app","deploy"]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/secrets/batch/get":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"secrets":{"app:A":"one","deploy:B":"two"}}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/secrets/value":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not_found"}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client := &secretsAPIClient{
		apiURL: server.URL,
		token:  "api-token",
		client: server.Client(),
	}
	if err := client.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	value, err := client.Get("app", "API_KEY")
	if err != nil || value != "test-secret-value" {
		t.Fatalf("Get = %q, %v", value, err)
	}
	keys, err := client.ListKeys("app")
	if err != nil || len(keys) != 2 {
		t.Fatalf("ListKeys = %v, %v", keys, err)
	}
	paths, err := client.ListPaths()
	if err != nil || len(paths) != 2 {
		t.Fatalf("ListPaths = %v, %v", paths, err)
	}
	values, err := client.GetMulti([]secrets.SecretRef{
		{Path: "app", Key: "A"},
		{Path: "deploy", Key: "B"},
	})
	if err != nil || values["A"] != "one" || values["B"] != "two" {
		t.Fatalf("GetMulti = %v, %v", values, err)
	}
	deleted, err := client.Delete("app", "MISSING")
	if err != nil {
		t.Fatalf("Delete returned error for 404: %v", err)
	}
	if deleted {
		t.Fatal("Delete should return false for missing secret")
	}
}
