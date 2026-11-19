package cmd

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestSLOSetSendsLaneAndBody(t *testing.T) {
	var path string
	var body map[string]interface{}
	err := runLaneCLI(t, SLOCommand, func(w http.ResponseWriter, r *http.Request) {
		path = r.Method + " " + r.URL.RequestURI()
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"sli": "queue_wait", "lane": "pr", "objective": 0.9})
	}, "set", "queue_wait", "--org", "acme", "--lane", "pr", "--objective", "0.9", "--threshold-seconds", "600")
	if err != nil {
		t.Fatal(err)
	}
	if path != "PUT /api/v1/organizations/acme/slos/queue_wait?lane=pr" {
		t.Fatalf("request = %s", path)
	}
	if body["objective"] != 0.9 || body["threshold_seconds"] != float64(600) {
		t.Fatalf("body = %v", body)
	}
}

func TestSLOStatusAndList(t *testing.T) {
	var paths []string
	handler := func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/api/v1/organizations/acme/slos/status" {
			_, _ = w.Write([]byte(`{"slos":[{"sli":"availability","lane":"","objective":0.995,"total":100,"good":100,"compliance":1,"error_budget_remaining":1,"budget_state":"ok"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"targets":[{"sli":"eval_latency","objective":0.95,"threshold_seconds":120,"window_days":30,"builtin":true}]}`))
	}
	if err := runLaneCLI(t, SLOCommand, handler, "status", "--org", "acme"); err != nil {
		t.Fatal(err)
	}
	if err := runLaneCLI(t, SLOCommand, handler, "list", "--org", "acme"); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[1] != "/api/v1/organizations/acme/slos" {
		t.Fatalf("paths = %v", paths)
	}
}

func TestAlertsCreateBody(t *testing.T) {
	var body map[string]interface{}
	var method string
	err := runLaneCLI(t, AlertsCommand, func(w http.ResponseWriter, r *http.Request) {
		method = r.Method + " " + r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"name":"ci-pass","scope":"workflow","condition":"pass_rate_below","state":"ok","enabled":true}`))
	}, "create", "ci-pass", "--org", "acme", "--scope", "workflow", "--project", "app", "--workflow", "ci",
		"--condition", "pass_rate_below", "--threshold", "0.8", "--ref-class", "all", "--fire-after", "3")
	if err != nil {
		t.Fatal(err)
	}
	if method != "POST /api/v1/organizations/acme/alert-rules" {
		t.Fatalf("request = %s", method)
	}
	if body["threshold"] != 0.8 || body["ref_class_bucket"] != "" || body["fire_after"] != float64(3) || body["workflow_security_id"] != "ci" {
		t.Fatalf("body = %v", body)
	}
	if _, ok := body["enabled"]; ok {
		t.Fatalf("enabled must be absent unless --disabled: %v", body)
	}
}

func TestAlertsUpdateRejectsBadBool(t *testing.T) {
	if err := runLaneCLI(t, AlertsCommand, func(http.ResponseWriter, *http.Request) {}, "update", "x", "--org", "a", "--enabled", "maybe"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestRollupsDashboardQuery(t *testing.T) {
	var uri string
	err := runLaneCLI(t, RollupsCommand, func(w http.ResponseWriter, r *http.Request) {
		uri = r.URL.RequestURI()
		_, _ = w.Write([]byte(`{"groups":[]}`))
	}, "dashboard", "usage", "--org", "acme", "--days", "90", "--attribution", "actor")
	if err != nil {
		t.Fatal(err)
	}
	if uri != "/api/v1/organizations/acme/observability/usage?attribution=actor&days=90" {
		t.Fatalf("uri = %s", uri)
	}
}
