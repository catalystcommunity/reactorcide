package cmd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/urfave/cli/v2"
)

func runReportingCLI(t *testing.T, command *cli.Command, handler http.HandlerFunc, args ...string) error {
	t.Helper()
	server := httptest.NewServer(handler)
	defer server.Close()
	app := cli.NewApp()
	app.Commands = []*cli.Command{command}
	full := append([]string{"reactorcide", command.Name}, args...)
	full = append(full, "--api-url", server.URL, "--token", "fake-test-token-not-real", "--allow-insecure-transport")
	return app.Run(NormalizeArgs(app, full))
}

func TestReportingCLIRequests(t *testing.T) {
	contextsFile := filepath.Join(t.TempDir(), "contexts.txt")
	if err := os.WriteFile(contextsFile, []byte("# other CI\nbuild (ubuntu, 3.11)\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		command *cli.Command
		args    []string
		status  int
		path    string
		query   string
		body    map[string]interface{}
	}{
		{"show", ReportingCommand, []string{"show", "--project", "app"}, http.StatusOK, "GET /api/v1/projects/app/reporting", "", nil},
		{"set", ReportingCommand, []string{"set", "--project", "app", "--report-mode", "silent", "--agreement-context", "ci/build",
			"--agreement-contexts-file", contextsFile, "--codeowners-import", "on", "--codeowners-ttl", "120", "--path-acceptance", "enforce",
			"--path-owner", "/payments/=reactorcide_group:payments vcs_team:acme/pay", "--path-owner", "/docs/="},
			http.StatusOK, "PUT /api/v1/projects/app/reporting", "", map[string]interface{}{
				"report_mode": "silent", "agreement_contexts": []interface{}{"ci/build", "build (ubuntu, 3.11)"},
				"codeowners_import": "on", "codeowners_cache_ttl_seconds": float64(120), "path_acceptance": "enforce",
				"path_owners": []interface{}{
					map[string]interface{}{"pattern": "/payments/", "owners": []interface{}{"reactorcide_group:payments", "vcs_team:acme/pay"}},
					map[string]interface{}{"pattern": "/docs/", "owners": []interface{}{}},
				}}},
		{"inherit", ReportingCommand, []string{"set", "--project", "app", "--inherit", "report_mode", "--clear-agreement-contexts"},
			http.StatusOK, "PUT /api/v1/projects/app/reporting", "",
			map[string]interface{}{"inherit": []interface{}{"report_mode"}, "agreement_contexts": []interface{}{}}},
		{"org-set", ReportingCommand, []string{"org-set", "--org", "acme", "--report-mode", "statuses_only"}, http.StatusOK,
			"PUT /api/v1/organizations/acme/reporting-settings", "", map[string]interface{}{"report_mode": "statuses_only",
				"agreement_contexts": []interface{}{}, "codeowners_import": "", "codeowners_cache_ttl_seconds": float64(0),
				"path_acceptance": "", "path_owners": []interface{}{}}},
		{"agreement", ReportingCommand, []string{"agreement", "--project", "app", "--since", "7d"}, http.StatusOK,
			"GET /api/v1/projects/app/agreement", "limit=500&since=7d", nil},
		{"list", PathAcceptancesCommand, []string{"list", "--project", "app", "--state", "requested"}, http.StatusOK,
			"GET /api/v1/projects/app/path-acceptances", "state=requested", nil},
		{"request", PathAcceptancesCommand, []string{"request", "--project", "app", "--pattern", "/payments/", "--owner",
			"reactorcide_group:payments", "--reason", "shared schema", "build"}, http.StatusCreated, "POST /api/v1/projects/app/path-acceptances", "",
			map[string]interface{}{"workflow_security_id": "build", "path_pattern": "/payments/", "owner_subject": "reactorcide_group:payments",
				"reason": "shared schema", "accept": false}},
		{"accept", PathAcceptancesCommand, []string{"accept", "--project", "app", "a-1"}, http.StatusOK,
			"POST /api/v1/projects/app/path-acceptances/a-1/accept", "", map[string]interface{}{"reason": ""}},
		{"revoke", PathAcceptancesCommand, []string{"revoke", "--project", "app", "--reason", "moved", "a-1"}, http.StatusOK,
			"POST /api/v1/projects/app/path-acceptances/a-1/revoke", "", map[string]interface{}{"reason": "moved"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var rec requiredSetRecorded
			query := ""
			handler := requiredSetRecorder(&rec, c.status, map[string]interface{}{})
			wrapped := func(w http.ResponseWriter, r *http.Request) {
				query = r.URL.RawQuery
				handler(w, r)
			}
			if err := runReportingCLI(t, c.command, wrapped, c.args...); err != nil {
				t.Fatal(err)
			}
			if rec.path != c.path || query != c.query {
				t.Fatalf("request = %s?%s, want %s?%s", rec.path, query, c.path, c.query)
			}
			if c.body != nil && !reflect.DeepEqual(rec.body, c.body) {
				t.Fatalf("body = %#v, want %#v", rec.body, c.body)
			}
		})
	}
}

func TestReportingCLISetNeedsAValue(t *testing.T) {
	var rec requiredSetRecorded
	err := runReportingCLI(t, ReportingCommand, requiredSetRecorder(&rec, http.StatusOK, map[string]interface{}{}), "set", "--project", "app")
	if err == nil || rec.path != "" {
		t.Fatalf("an empty set must fail before a request: err=%v request=%q", err, rec.path)
	}
	if _, err := parsePathOwnerRules([]string{"no-equals"}); err == nil {
		t.Fatal("a rule without = must fail")
	}
}
