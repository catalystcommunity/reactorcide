package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/urfave/cli/v2"
)

type recordedRequest struct {
	Method string
	Path   string
	Body   map[string]any
}

func recordingServer(t *testing.T, responses map[string]any) (*httptest.Server, *[]recordedRequest) {
	t.Helper()
	requests := []recordedRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := recordedRequest{Method: r.Method, Path: r.URL.Path}
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&request.Body)
		}
		requests = append(requests, request)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(responses[r.Method+" "+r.URL.Path])
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

func runCLI(t *testing.T, command *cli.Command, args ...string) error {
	t.Helper()
	app := cli.NewApp()
	app.Commands = []*cli.Command{command}
	return app.Run(NormalizeArgs(app, append([]string{"reactorcide"}, args...)))
}

func TestShedSetGlobalAndOrganization(t *testing.T) {
	server, requests := recordingServer(t, map[string]any{
		"PUT /api/v1/shed":                    map[string]any{"global_level": "degraded", "level": "degraded"},
		"PUT /api/v1/organizations/acme/shed": map[string]any{"global_level": "normal", "org_level": "critical", "level": "critical"},
	})
	common := []string{"--api-url", server.URL, "--token", "fake-test-token-not-real", "--allow-insecure-transport"}
	if err := runCLI(t, ShedCommand, append(append([]string{"shed"}, common...), "set", "--level", "degraded", "--reason", "outage")...); err != nil {
		t.Fatal(err)
	}
	if err := runCLI(t, ShedCommand, append(append([]string{"shed"}, common...), "set", "--level", "critical", "--org", "acme")...); err != nil {
		t.Fatal(err)
	}
	got := *requests
	if len(got) != 2 || got[0].Path != "/api/v1/shed" || got[0].Body["level"] != "degraded" || got[0].Body["reason"] != "outage" {
		t.Fatalf("global request: %+v", got)
	}
	if got[1].Path != "/api/v1/organizations/acme/shed" || got[1].Body["level"] != "critical" {
		t.Fatalf("organization request: %+v", got[1])
	}
	if err := runCLI(t, ShedCommand, append(append([]string{"shed"}, common...), "set", "--level", "panic")...); err == nil {
		t.Fatal("an unknown level must fail")
	}
}

func TestOrgsSetDefaults(t *testing.T) {
	server, requests := recordingServer(t, map[string]any{
		"PUT /api/v1/organizations/acme/defaults-profile": map[string]any{"organization": "acme", "profile": "secure",
			"run_gate": []string{"outside_contributors"}, "dynamic_jobs": "deny", "builtin_policy": true},
	})
	if err := runCLI(t, OrgsCommand, "orgs", "--api-url", server.URL, "--token", "fake-test-token-not-real", "--allow-insecure-transport",
		"set-defaults", "acme", "--profile", "secure"); err != nil {
		t.Fatal(err)
	}
	got := *requests
	if len(got) != 1 || got[0].Method != http.MethodPut || got[0].Body["profile"] != "secure" {
		t.Fatalf("request: %+v", got)
	}
}

func TestOrgsUpdateKeepsCurrentValuesAndSendsSettings(t *testing.T) {
	server, requests := recordingServer(t, map[string]any{
		"GET /api/v1/organizations/acme": map[string]any{"name": "acme", "display_name": "Acme", "status": "suspended", "is_private": true},
		"PUT /api/v1/organizations/acme": map[string]any{"name": "acme"},
	})
	if err := runCLI(t, OrgsCommand, "orgs", "--api-url", server.URL, "--token", "fake-test-token-not-real", "--allow-insecure-transport",
		"update", "acme", "--draft-prs", "skip_when_shedding"); err != nil {
		t.Fatal(err)
	}
	got := *requests
	if len(got) != 2 {
		t.Fatalf("requests: %+v", got)
	}
	body := got[1].Body
	if body["display_name"] != "Acme" || body["status"] != "suspended" || body["is_private"] != true || body["draft_prs"] != "skip_when_shedding" {
		t.Fatalf("update body: %+v", body)
	}
	if _, ok := body["dynamic_jobs"]; ok {
		t.Fatal("a setting with no flag must not be sent")
	}
}
