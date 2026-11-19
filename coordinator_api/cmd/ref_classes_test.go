package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/urfave/cli/v2"
)

func runRefClassesCLI(t *testing.T, handler http.HandlerFunc, args ...string) error {
	t.Helper()
	server := httptest.NewServer(handler)
	defer server.Close()
	app := cli.NewApp()
	app.Commands = []*cli.Command{RefClassesCommand}
	full := append([]string{"reactorcide", "ref-classes"}, args...)
	full = append(full, "--api-url", server.URL, "--token", "fake-test-token-not-real", "--allow-insecure-transport")
	return app.Run(NormalizeArgs(app, full))
}

func TestRefClassRulesSetSendsRule(t *testing.T) {
	var path string
	var body map[string]interface{}
	err := runRefClassesCLI(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.Method + " " + r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"name": "production", "classes": []string{"production"}, "enabled": true})
	}, "rules", "set", "production", "--org", "example", "--class", "production", "--event", "push", "--default-branch", "true")
	if err != nil {
		t.Fatal(err)
	}
	if path != "PUT /api/v1/organizations/example/ref-class-rules/production" {
		t.Fatalf("request = %s", path)
	}
	if !reflect.DeepEqual(body["classes"], []interface{}{"production"}) || body["is_default_branch"] != true || body["enabled"] != true {
		t.Fatalf("body = %v", body)
	}
	if _, ok := body["is_pr"]; ok {
		t.Fatalf("an unset match fact must not be sent: %v", body)
	}
}

func TestRefClassesExplainAndScopeFlags(t *testing.T) {
	var path string
	var body map[string]interface{}
	err := runRefClassesCLI(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.Method + " " + r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"classes": []string{"fork", "pr"}})
	}, "explain", "--project", "proj-1", "--event", "pull_request_opened", "--head-branch", "main", "--base-branch", "main", "--fork")
	if err != nil {
		t.Fatal(err)
	}
	if path != "POST /api/v1/projects/proj-1/ref-classes/explain" {
		t.Fatalf("request = %s", path)
	}
	facts := body["facts"].(map[string]interface{})
	if facts["head_branch"] != "main" || facts["is_fork"] != true || facts["branch"] != nil {
		t.Fatalf("facts = %v", facts)
	}
	if err := runRefClassesCLI(t, func(http.ResponseWriter, *http.Request) {}, "rules", "list"); err == nil {
		t.Fatal("a scope is required")
	}
	if err := runRefClassesCLI(t, func(http.ResponseWriter, *http.Request) {}, "rules", "list", "--org", "a", "--project", "b"); err == nil {
		t.Fatal("--org and --project are exclusive")
	}
	if err := runRefClassesCLI(t, func(http.ResponseWriter, *http.Request) {}, "rules", "set", "x", "--org", "a", "--class", "c", "--pr", "maybe"); err == nil {
		t.Fatal("--pr must be true, false, or any")
	}
}
