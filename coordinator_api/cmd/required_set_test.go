package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/urfave/cli/v2"
)

func runRequiredSetCLI(t *testing.T, handler http.HandlerFunc, args ...string) error {
	t.Helper()
	server := httptest.NewServer(handler)
	defer server.Close()
	app := cli.NewApp()
	app.Commands = []*cli.Command{RequiredSetCommand}
	full := append([]string{"reactorcide", "required-set"}, args...)
	full = append(full, "--api-url", server.URL, "--token", "fake-test-token-not-real", "--allow-insecure-transport")
	return app.Run(NormalizeArgs(app, full))
}

type requiredSetRecorded struct {
	path string
	body map[string]interface{}
}

func requiredSetRecorder(rec *requiredSetRecorded, status int, response interface{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rec.path = r.Method + " " + r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&rec.body)
		w.WriteHeader(status)
		if response != nil {
			_ = json.NewEncoder(w).Encode(response)
		}
	}
}

func TestRequiredSetCLIRequests(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		status int
		path   string
		body   map[string]interface{}
	}{
		{"enable", []string{"enable", "--project", "app"}, http.StatusOK, "POST /api/v1/projects/app/required-set/enable", nil},
		{"list", []string{"list", "--project", "app"}, http.StatusOK, "GET /api/v1/projects/app/required-set", nil},
		{"propose", []string{"propose", "--project", "app", "--branch", "release/*", "--reason", "stable", "--approve", "lint"}, http.StatusCreated,
			"POST /api/v1/projects/app/required-set/proposals",
			map[string]interface{}{"action": "promote", "workflow": "lint", "branch_pattern": "release/*", "reason": "stable", "approve": true}},
		{"remove", []string{"remove", "--project", "app", "lint"}, http.StatusCreated, "POST /api/v1/projects/app/required-set/proposals",
			map[string]interface{}{"action": "demote", "workflow": "lint", "branch_pattern": "", "reason": "", "approve": false}},
		{"approve", []string{"approve", "--project", "app", "--reason", "ok", "p-1"}, http.StatusOK,
			"POST /api/v1/projects/app/required-set/proposals/p-1/approve", map[string]interface{}{"reason": "ok"}},
		{"reject", []string{"reject", "--project", "app", "p-1"}, http.StatusOK,
			"POST /api/v1/projects/app/required-set/proposals/p-1/reject", map[string]interface{}{"reason": ""}},
		{"quarantine", []string{"quarantine", "--project", "app", "--for", "7d", "--reason", "flaky", "e2e"}, http.StatusOK,
			"POST /api/v1/projects/app/required-set/quarantine",
			map[string]interface{}{"workflow": "e2e", "branch_pattern": "", "duration": "7d", "until": "", "reason": "flaky"}},
		{"unquarantine", []string{"unquarantine", "--project", "app", "e2e"}, http.StatusNoContent,
			"POST /api/v1/projects/app/required-set/unquarantine", map[string]interface{}{"workflow": "e2e", "branch_pattern": ""}},
		{"approvers", []string{"approvers", "--project", "app", "--approver", "reactorcide_group:ci", "--rule", "release/* hotfix/**=reactorcide_group:release"},
			http.StatusOK, "PUT /api/v1/projects/app/required-set/approvers",
			map[string]interface{}{"approvers": []interface{}{"reactorcide_group:ci"}, "approver_rules": []interface{}{
				map[string]interface{}{"branches": []interface{}{"release/*", "hotfix/**"}, "approvers": []interface{}{"reactorcide_group:release"}}}}},
		{"inherit", []string{"approvers", "--project", "app", "--inherit"}, http.StatusOK, "PUT /api/v1/projects/app/required-set/approvers",
			map[string]interface{}{"approvers": nil, "approver_rules": nil}},
		{"org settings", []string{"org-settings", "--org", "acme", "--max-quarantine-days", "14"}, http.StatusOK,
			"PUT /api/v1/organizations/acme/required-set-settings",
			map[string]interface{}{"approvers": []interface{}{}, "approver_rules": []interface{}{}, "max_quarantine_days": float64(14)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var rec requiredSetRecorded
			var response interface{} = map[string]interface{}{}
			if c.status == http.StatusNoContent {
				response = nil
			}
			if err := runRequiredSetCLI(t, requiredSetRecorder(&rec, c.status, response), c.args...); err != nil {
				t.Fatal(err)
			}
			if rec.path != c.path {
				t.Fatalf("request = %s, want %s", rec.path, c.path)
			}
			if c.body != nil && !reflect.DeepEqual(rec.body, c.body) {
				t.Fatalf("body = %#v, want %#v", rec.body, c.body)
			}
		})
	}
}

func TestRequiredSetCLIQuarantineNeedsExpiry(t *testing.T) {
	var rec requiredSetRecorded
	err := runRequiredSetCLI(t, requiredSetRecorder(&rec, http.StatusOK, map[string]interface{}{}), "quarantine", "--project", "app", "--reason", "x", "e2e")
	if err == nil || rec.path != "" {
		t.Fatalf("a quarantine with no expiry must fail before a request: err=%v request=%q", err, rec.path)
	}
	if _, err := parseApproverRules([]string{"no-equals"}); err == nil {
		t.Fatal("a rule without = must fail")
	}
}
