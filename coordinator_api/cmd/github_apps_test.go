package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urfave/cli/v2"
)

func runGitHubAppsCLI(t *testing.T, serverURL string, args ...string) (string, error) {
	t.Helper()
	app := cli.NewApp()
	app.Commands = []*cli.Command{GitHubAppsCommand}
	full := append([]string{"reactorcide", "github-apps", args[0],
		"--api-url", serverURL, "--token", "fake-test-token-not-real", "--allow-insecure-transport"}, args[1:]...)
	stdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := app.Run(NormalizeArgs(app, full))
	w.Close()
	os.Stdout = stdout
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String(), err
}

const testPEM = "-----BEGIN RSA PRIVATE KEY-----\nTESTKEYMATERIAL\n-----END RSA PRIVATE KEY-----\n"

func TestGitHubAppsAddReadsFilesAndNeverPrintsThem(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/github-apps" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"app_id": 42, "slug": "rc", "enabled": true, "headroom": 1,
			"install_url": "https://github.com/apps/rc/installations/new"})
	}))
	defer server.Close()
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key.pem")
	secretFile := filepath.Join(dir, "secret")
	if err := os.WriteFile(keyFile, []byte(testPEM), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secretFile, []byte("hook-secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runGitHubAppsCLI(t, server.URL, "add", "--app-id", "42", "--private-key-file", keyFile,
		"--webhook-secret-file", secretFile, "--default-org", "acme", "--no-verify")
	if err != nil {
		t.Fatalf("add failed: %v", err)
	}
	if body["private_key"] != testPEM || body["webhook_secret"] != "hook-secret-value" || body["app_id"] != float64(42) {
		t.Fatalf("body = %#v", body)
	}
	if body["verify"] != false || body["default_org"] != "acme" {
		t.Fatalf("flags not sent: %#v", body)
	}
	if strings.Contains(out, "TESTKEYMATERIAL") || strings.Contains(out, "hook-secret-value") {
		t.Fatalf("the output has a secret: %s", out)
	}
	if !strings.Contains(out, "install_url") {
		t.Fatalf("output = %s", out)
	}
}

func TestGitHubAppsAddRefusesNonPEMKey(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key")
	secretFile := filepath.Join(dir, "secret")
	_ = os.WriteFile(keyFile, []byte("not a key"), 0o600)
	_ = os.WriteFile(secretFile, []byte("s"), 0o600)
	_, err := runGitHubAppsCLI(t, "http://127.0.0.1:1", "add", "--app-id", "42", "--private-key-file", keyFile,
		"--webhook-secret-file", secretFile)
	if err == nil || !strings.Contains(err.Error(), "not a PEM") {
		t.Fatalf("err = %v", err)
	}
}

func TestGitHubAppsListDisableInstallationsSync(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/api/v1/github-apps":
			_ = json.NewEncoder(w).Encode(map[string]any{"apps": []map[string]any{
				{"app_id": 1, "slug": "a", "enabled": true, "headroom": 0.25, "recommended": false},
				{"app_id": 2, "slug": "b", "enabled": true, "headroom": 0.9, "recommended": true}}})
		case "/api/v1/github-apps/2/disable":
			_ = json.NewEncoder(w).Encode(map[string]any{"app_id": 2, "enabled": false})
		case "/api/v1/github-apps/2/installations":
			_ = json.NewEncoder(w).Encode(map[string]any{"installations": []map[string]any{{"installation_id": 7,
				"account_login": "acme", "repository_selection": "selected", "headroom": 0.5,
				"repositories": []map[string]any{{"repo_full_name": "acme/app"}}}}})
		case "/api/v1/github-apps/2/sync":
			_ = json.NewEncoder(w).Encode(map[string]any{"app_id": 2, "installations": 1,
				"repositories": []map[string]any{{"repository": "acme/app", "action": "created", "project_id": "p1"}}})
		case "/api/v1/github-apps/account-orgs/acme":
			_ = json.NewEncoder(w).Encode(map[string]any{"account_login": "acme", "org_id": "o1"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	out, err := runGitHubAppsCLI(t, server.URL, "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "90%") || !strings.Contains(out, "yes") {
		t.Fatalf("list output = %s", out)
	}
	if out, err = runGitHubAppsCLI(t, server.URL, "disable", "2"); err != nil || !strings.Contains(out, "false") {
		t.Fatalf("disable: %v %s", err, out)
	}
	if out, err = runGitHubAppsCLI(t, server.URL, "installations", "2"); err != nil || !strings.Contains(out, "acme/app") {
		t.Fatalf("installations: %v %s", err, out)
	}
	if out, err = runGitHubAppsCLI(t, server.URL, "sync", "2"); err != nil || !strings.Contains(out, "created") {
		t.Fatalf("sync: %v %s", err, out)
	}
	if _, err = runGitHubAppsCLI(t, server.URL, "map-account", "--org", "acme-org", "acme"); err != nil {
		t.Fatalf("map-account: %v", err)
	}
	if _, err = runGitHubAppsCLI(t, server.URL, "show", "x"); err == nil {
		t.Fatal("a non-numeric App ID is refused")
	}
	want := []string{"GET /api/v1/github-apps", "POST /api/v1/github-apps/2/disable", "GET /api/v1/github-apps/2/installations",
		"POST /api/v1/github-apps/2/sync", "PUT /api/v1/github-apps/account-orgs/acme"}
	if strings.Join(requests, "|") != strings.Join(want, "|") {
		t.Fatalf("requests = %v", requests)
	}
}
