package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/urfave/cli/v2"
)

func runLaneCLI(t *testing.T, command *cli.Command, handler http.HandlerFunc, args ...string) error {
	t.Helper()
	server := httptest.NewServer(handler)
	defer server.Close()
	app := cli.NewApp()
	app.Commands = []*cli.Command{command}
	full := append([]string{"reactorcide", command.Name}, args...)
	full = append(full, "--api-url", server.URL, "--token", "fake-test-token-not-real", "--allow-insecure-transport")
	return app.Run(NormalizeArgs(app, full))
}

func TestLanesSetOnlyBase(t *testing.T) {
	var path string
	var body map[string]interface{}
	err := runLaneCLI(t, LanesCommand, func(w http.ResponseWriter, r *http.Request) {
		path = r.Method + " " + r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"name": "pr-ready", "base": -50, "enabled": true})
	}, "set", "pr-ready", "--org", "example", "--base", "-50")
	if err != nil {
		t.Fatal(err)
	}
	if path != "PUT /api/v1/organizations/example/priority-lanes/pr-ready" {
		t.Fatalf("request = %s", path)
	}
	if body["base"] != float64(-50) || body["enabled"] != true {
		t.Fatalf("body = %v", body)
	}
	for _, key := range []string{"match", "position", "description"} {
		if _, ok := body[key]; ok {
			t.Fatalf("an unset %s must not be sent, so the built-in lane keeps it: %v", key, body)
		}
	}
}

func TestLanesSetWithMatch(t *testing.T) {
	var body map[string]interface{}
	err := runLaneCLI(t, LanesCommand, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"name": "hotfix", "base": 200, "enabled": true})
	}, "set", "hotfix", "--project", "proj-1", "--base", "200", "--position", "5", "--class", "hotfix", "--event", "push", "--draft", "false", "--kind", "work")
	if err != nil {
		t.Fatal(err)
	}
	match, ok := body["match"].(map[string]interface{})
	if !ok {
		t.Fatalf("match missing: %v", body)
	}
	if !reflect.DeepEqual(match["classes_any"], []interface{}{"hotfix"}) || match["draft"] != false || match["job_kind"] != "work" ||
		!reflect.DeepEqual(match["event_types"], []interface{}{"push"}) || body["position"] != float64(5) {
		t.Fatalf("body = %v", body)
	}
	if err := runLaneCLI(t, LanesCommand, func(http.ResponseWriter, *http.Request) {}, "set", "x", "--org", "a", "--base", "1", "--draft", "maybe"); err == nil {
		t.Fatal("--draft must be true, false, or any")
	}
	if err := runLaneCLI(t, LanesCommand, func(http.ResponseWriter, *http.Request) {}, "list"); err == nil {
		t.Fatal("a scope is required")
	}
}

func TestLanesMatchAllAndExplain(t *testing.T) {
	var body map[string]interface{}
	if err := runLaneCLI(t, LanesCommand, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"name": "all", "base": 1})
	}, "set", "all", "--org", "a", "--base", "1", "--match-all"); err != nil {
		t.Fatal(err)
	}
	if match, ok := body["match"].(map[string]interface{}); !ok || match["classes_any"] != nil || match["retry"] != false {
		t.Fatalf("match-all sends an empty match: %v", body)
	}

	var path string
	err := runLaneCLI(t, LanesCommand, func(w http.ResponseWriter, r *http.Request) {
		path = r.Method + " " + r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"decision": map[string]interface{}{"lane": "pr-ready", "final": -100}})
	}, "explain", "--org", "example", "--class", "pr", "--draft", "--kind", "eval", "--requested", "7")
	if err != nil {
		t.Fatal(err)
	}
	if path != "POST /api/v1/organizations/example/priority/explain" {
		t.Fatalf("request = %s", path)
	}
	if body["draft"] != true || body["job_kind"] != "eval" || body["requested"] != float64(7) || !reflect.DeepEqual(body["ref_classes"], []interface{}{"pr"}) {
		t.Fatalf("body = %v", body)
	}
}

func TestLanesDeleteAndList(t *testing.T) {
	var path string
	if err := runLaneCLI(t, LanesCommand, func(w http.ResponseWriter, r *http.Request) {
		path = r.Method + " " + r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}, "delete", "hotfix", "--project", "proj-1"); err != nil {
		t.Fatal(err)
	}
	if path != "DELETE /api/v1/projects/proj-1/priority-lanes/hotfix" {
		t.Fatalf("request = %s", path)
	}
	if err := runLaneCLI(t, LanesCommand, func(w http.ResponseWriter, r *http.Request) {
		path = r.Method + " " + r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"effective": []map[string]interface{}{{"name": "standard", "base": 0, "scope": "builtin", "enabled": true}}})
	}, "list", "--org", "example"); err != nil {
		t.Fatal(err)
	}
	if path != "GET /api/v1/organizations/example/priority-lanes" {
		t.Fatalf("request = %s", path)
	}
}

func TestGroupsSetPriorityShift(t *testing.T) {
	var path string
	var body map[string]interface{}
	err := runLaneCLI(t, GroupsCommand, func(w http.ResponseWriter, r *http.Request) {
		path = r.Method + " " + r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"name": "release", "priority_shift": 20})
	}, "set-priority-shift", "release", "20", "--org", "example")
	if err != nil {
		t.Fatal(err)
	}
	if path != "PUT /api/v1/organizations/example/groups/release/priority-shift" || body["shift"] != float64(20) {
		t.Fatalf("request = %s %v", path, body)
	}
	if err := runLaneCLI(t, GroupsCommand, func(http.ResponseWriter, *http.Request) {}, "set-priority-shift", "release", "lots", "--org", "example"); err == nil {
		t.Fatal("the shift must be an integer")
	}
}
