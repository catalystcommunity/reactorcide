package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/urfave/cli/v2"
)

// Regression test for Bug 1: "apply" used to register both --file and
// --format with the "-f" short alias, which made urfave/cli panic while
// building the command's flag set the first time "apply" was invoked
// (including "apply --help"). The panic happened at flag-set construction
// time, not in the Action, so this exercises every secret-grants subcommand
// end-to-end via App.Run with "--help" to catch any duplicate-alias
// regression on any of them.
func TestSecretGrantsCommandsBuildWithoutPanic(t *testing.T) {
	subcommands := make([]string, 0, len(SecretGrantsCommand.Subcommands))
	for _, sub := range SecretGrantsCommand.Subcommands {
		subcommands = append(subcommands, sub.Name)
	}

	for _, name := range subcommands {
		name := name
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("secret-grants %s panicked: %v", name, r)
				}
			}()

			app := cli.NewApp()
			app.Commands = []*cli.Command{SecretGrantsCommand}
			if err := app.Run([]string{"reactorcide", "secret-grants", name, "--help"}); err != nil {
				t.Fatalf("secret-grants %s --help returned error: %v", name, err)
			}
		})
	}
}

// Regression test for Bug 2: "secret-grants set" must accept its own flags
// (--secret-path, --secret-match, --job-name, --job-match, --description)
// placed after the positional <name>, matching the documented usage in
// docs/vcs-credentials-and-secret-grants.md.
func TestSecretGrantsSetAcceptsFlagsAfterName(t *testing.T) {
	var gotBody map[string]interface{}
	var sawCreate bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			// Upsert probes for an existing grant first; report not found so
			// it falls through to create.
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not_found"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/secret-grants":
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			sawCreate = true
			w.WriteHeader(http.StatusCreated)
			resp := map[string]interface{}{"name": gotBody["name"]}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	app := cli.NewApp()
	app.Commands = []*cli.Command{SecretGrantsCommand}

	args := NormalizeArgs(app, []string{
		"reactorcide", "secret-grants",
		"--api-url", server.URL,
		"--token", "fake-test-token-not-real",
		"--allow-insecure-transport",
		"set", "my-grant",
		"--secret-path", "catalystcommunity/registry",
		"--secret-match", "exact",
		"--job-name", "my-job",
		"--job-match", "exact",
	})

	if err := app.Run(args); err != nil {
		t.Fatalf("secret-grants set with flags after <name> failed: %v", err)
	}
	if !sawCreate {
		t.Fatal("expected a create request to be sent")
	}
	if gotBody["secret_path_pattern"] != "catalystcommunity/registry" {
		t.Fatalf("secret_path_pattern = %v, want catalystcommunity/registry", gotBody["secret_path_pattern"])
	}
	if gotBody["secret_path_match"] != "exact" {
		t.Fatalf("secret_path_match = %v, want exact", gotBody["secret_path_match"])
	}
	if gotBody["job_name_pattern"] != "my-job" {
		t.Fatalf("job_name_pattern = %v, want my-job", gotBody["job_name_pattern"])
	}
	if gotBody["job_name_match"] != "exact" {
		t.Fatalf("job_name_match = %v, want exact", gotBody["job_name_match"])
	}
}
