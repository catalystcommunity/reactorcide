package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGroupsTeamCommands(t *testing.T) {
	type hit struct {
		method, path, query string
		body                map[string]any
	}
	hits := []hit{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := hit{method: r.Method, path: r.URL.EscapedPath(), query: r.URL.RawQuery}
		_ = json.NewDecoder(r.Body).Decode(&h.body)
		hits = append(hits, h)
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /api/v1/organizations/acme/groups/platform/teams":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"link_id": "l1", "subject": "vcs_team:acme/platform"})
		case "DELETE /api/v1/organizations/acme/groups/platform/teams/acme/platform":
			w.WriteHeader(http.StatusNoContent)
		case "POST /api/v1/organizations/acme/groups/platform/sync":
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"team": "acme/platform", "members": 3, "added": []string{"p"}}}})
		case "GET /api/v1/organizations/acme/groups/platform/members":
			_ = json.NewEncoder(w).Encode(map[string]any{"members": []map[string]any{{"name": "github/bob", "source": "vcs_sync", "teams": []string{"vcs_team:acme/platform"}}}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"teams": []any{}, "history": []any{}})
		}
	}))
	defer server.Close()
	common := []string{"--api-url", server.URL, "--token", "fake-test-token-not-real", "--allow-insecure-transport", "--org", "acme"}
	run := func(sub string, args ...string) {
		t.Helper()
		if err := runCLI(t, GroupsCommand, append(append([]string{"groups", sub}, common...), args...)...); err != nil {
			t.Fatalf("%s: %v", sub, err)
		}
	}
	run("link-team", "platform", "acme/platform")
	run("teams", "platform")
	run("sync", "platform")
	run("members", "platform")
	run("history", "platform", "--principal", "p1")
	run("unlink-team", "platform", "vcs_team:acme/platform")

	if hits[0].body["team"] != "acme/platform" || hits[0].body["provider"] != "github" {
		t.Fatalf("link body: %+v", hits[0])
	}
	if hits[4].path != "/api/v1/organizations/acme/groups/platform/history" || hits[4].query != "limit=100&principal=p1" {
		t.Fatalf("history: %+v", hits[4])
	}
	if hits[5].method != http.MethodDelete || hits[5].path != "/api/v1/organizations/acme/groups/platform/teams/acme/platform" {
		t.Fatalf("unlink: %+v", hits[5])
	}
}
