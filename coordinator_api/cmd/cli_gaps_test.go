package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/urfave/cli/v2"
)

// cliRequest is one request that a test server received.
type cliRequest struct {
	Method, Path, Query string
	Body                map[string]interface{}
}

// cliTestServer answers each path with a fixed JSON body and records the
// requests.
func cliTestServer(t *testing.T, answers map[string]interface{}) (*httptest.Server, func() []cliRequest) {
	t.Helper()
	var mu sync.Mutex
	requests := []cliRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := cliRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&request.Body)
		}
		mu.Lock()
		requests = append(requests, request)
		mu.Unlock()
		answer, ok := answers[r.Method+" "+r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		status := http.StatusOK
		if r.Method == http.MethodPost {
			status = http.StatusCreated
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(answer)
	}))
	t.Cleanup(server.Close)
	return server, func() []cliRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]cliRequest(nil), requests...)
	}
}

func runGapCLI(t *testing.T, command *cli.Command, serverURL string, args ...string) error {
	t.Helper()
	app := cli.NewApp()
	app.Commands = []*cli.Command{command}
	api := []string{"--api-url", serverURL, "--token", "fake-test-token-not-real", "--allow-insecure-transport"}
	full := append([]string{"reactorcide", command.Name}, api...)
	full = append(full, args...)
	if len(command.Flags) == 0 && len(args) > 0 {
		// A command with no API flags of its own: they go after the
		// subcommand.
		full = append(append([]string{"reactorcide", command.Name, args[0]}, api...), args[1:]...)
	}
	return app.Run(NormalizeArgs(app, full))
}

func TestOrgsSetInfraRetries(t *testing.T) {
	server, requests := cliTestServer(t, map[string]interface{}{
		"GET /api/v1/organizations/acme": map[string]interface{}{"name": "acme", "status": "active"},
		"PUT /api/v1/organizations/acme": map[string]interface{}{"name": "acme"},
	})
	if err := runGapCLI(t, OrgsCommand, server.URL, "set", "acme", "--infra-retries", "2"); err != nil {
		t.Fatal(err)
	}
	got := requests()
	if len(got) != 2 || got[1].Body["infra_retries"] != float64(2) {
		t.Fatalf("requests = %#v", got)
	}
	if err := runGapCLI(t, OrgsCommand, server.URL, "set", "acme", "--infra-retries", "9"); err == nil {
		t.Fatal("--infra-retries 9 was accepted")
	}
}

func TestProjectsSetAndCreateEventSettings(t *testing.T) {
	server, requests := cliTestServer(t, map[string]interface{}{
		"PUT /api/v1/projects/p1":           map[string]interface{}{"project_id": "p1"},
		"POST /api/v1/projects":             map[string]interface{}{"project_id": "p2", "name": "app"},
		"PUT /api/v1/projects/p2/reporting": map[string]interface{}{},
	})
	if err := runGapCLI(t, ProjectsCommand, server.URL, "set", "p1", "--infra-retries", "inherit"); err != nil {
		t.Fatal(err)
	}
	if err := runGapCLI(t, ProjectsCommand, server.URL, "create", "--name", "app", "--repo-url", "github.com/o/app",
		"--infra-retries", "1", "--dynamic-jobs", "deny", "--duplicate-events", "skip", "--draft-prs", "run", "--report-mode", "silent"); err != nil {
		t.Fatal(err)
	}
	got := requests()
	if len(got) != 3 {
		t.Fatalf("requests = %#v", got)
	}
	if got[0].Body["infra_retries"] != "inherit" {
		t.Fatalf("set body = %#v", got[0].Body)
	}
	create := got[1].Body
	if create["infra_retries"] != float64(1) || create["dynamic_jobs"] != "deny" || create["duplicate_events"] != "skip" || create["draft_prs"] != "run" {
		t.Fatalf("create body = %#v", create)
	}
	if got[2].Path != "/api/v1/projects/p2/reporting" || got[2].Body["report_mode"] != "silent" {
		t.Fatalf("report mode request = %#v", got[2])
	}
}

func TestBacklogAndResourceSuggestionCommands(t *testing.T) {
	server, requests := cliTestServer(t, map[string]interface{}{
		"GET /api/v1/backlog": map[string]interface{}{"queues": []interface{}{map[string]interface{}{"queue_uuid": "q1", "known": true, "count": 3}},
			"totals": map[string]interface{}{"count": 3}},
		"GET /api/v1/jobs/j1/resource-suggestion": map[string]interface{}{"job_id": "j1", "name": "build", "runs": 7, "min_runs": 5,
			"sufficient": true, "cpu_request": "500m", "memory_limit": "1Gi"},
	})
	if err := runGapCLI(t, BacklogCommand, server.URL, "--org", "acme"); err != nil {
		t.Fatal(err)
	}
	if err := runGapCLI(t, JobsCommand, server.URL, "resource-suggestion", "j1", "--runs", "20"); err != nil {
		t.Fatal(err)
	}
	got := requests()
	if len(got) != 2 || got[0].Query != "org=acme" || got[1].Query != "limit=20" {
		t.Fatalf("requests = %#v", got)
	}
}

func TestJobsListFailureClass(t *testing.T) {
	server, requests := cliTestServer(t, map[string]interface{}{
		"GET /api/v1/jobs": map[string]interface{}{"jobs": []interface{}{}},
	})
	if err := runGapCLI(t, JobsCommand, server.URL, "list", "--failure-class", "oom"); err != nil {
		t.Fatal(err)
	}
	if got := requests(); len(got) != 1 || got[0].Query != "failure_class=oom" {
		t.Fatalf("requests = %#v", got)
	}
}

func TestPrincipalsGrantWorkflowRole(t *testing.T) {
	server, requests := cliTestServer(t, map[string]interface{}{
		"GET /api/v1/organizations/acme/principals/diag": map[string]interface{}{"principal_id": "11111111-1111-1111-1111-111111111111"},
		"POST /api/v1/organizations/acme/workflow-roles": map[string]interface{}{"assignment_id": "a1", "workflow": "build", "role": "owner"},
	})
	if err := runGapCLI(t, PrincipalsCommand, server.URL, "grant-role", "--org", "acme", "--role", "owner", "--project", "app",
		"--workflow", "build", "diag"); err != nil {
		t.Fatal(err)
	}
	got := requests()
	if len(got) != 2 || got[1].Body["principal_id"] != "11111111-1111-1111-1111-111111111111" || got[1].Body["workflow"] != "build" {
		t.Fatalf("requests = %#v", got)
	}
	if err := runGapCLI(t, PrincipalsCommand, server.URL, "grant-role", "--org", "acme", "--role", "owner", "--workflow", "build", "diag"); err == nil {
		t.Fatal("--workflow without --project was accepted")
	}
}

func TestPolicyBuiltInView(t *testing.T) {
	server, _ := cliTestServer(t, map[string]interface{}{
		"GET /api/v1/projects/p1":        map[string]interface{}{"project_id": "p1", "org": "acme"},
		"GET /api/v1/organizations/acme": map[string]interface{}{"name": "acme", "defaults_profile": "secure"},
		"GET /api/v1/projects/p2":        map[string]interface{}{"project_id": "p2", "org": "old"},
		"GET /api/v1/organizations/old":  map[string]interface{}{"name": "old", "defaults_profile": "legacy"},
	})
	check := func(projectID string, want bool) {
		t.Helper()
		app := cli.NewApp()
		app.Flags = append(apiFlags(), formatFlag())
		app.Action = func(ctx *cli.Context) error {
			shown, err := printBuiltInPolicyIfUsed(ctx, projectID)
			if err != nil {
				return err
			}
			if shown != want {
				t.Fatalf("%s: shown = %t, want %t", projectID, shown, want)
			}
			return nil
		}
		if err := app.Run([]string{"test", "--api-url", server.URL, "--token", "fake-test-token-not-real", "--allow-insecure-transport", "--format", "json"}); err != nil {
			t.Fatal(err)
		}
	}
	check("p1", true)
	check("p2", false)
}
