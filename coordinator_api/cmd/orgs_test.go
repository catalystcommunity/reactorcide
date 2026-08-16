package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/urfave/cli/v2"
)

func TestOrgsDeleteSendsConfirmedReplacement(t *testing.T) {
	var requestBody struct {
		Replacement string `json:"replacement"`
		Confirm     bool   `json:"confirm"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/v1/organizations/default" {
			t.Fatalf("request = %s %s, want DELETE /api/v1/organizations/default", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	app := cli.NewApp()
	app.Commands = []*cli.Command{OrgsCommand}
	args := NormalizeArgs(app, []string{
		"reactorcide", "orgs",
		"--api-url", server.URL,
		"--token", "fake-test-token-not-real",
		"--allow-insecure-transport",
		"delete", "default",
		"--replacement", "linked",
		"--yes",
	})

	if err := app.Run(args); err != nil {
		t.Fatalf("orgs delete failed: %v", err)
	}
	if requestBody.Replacement != "linked" || !requestBody.Confirm {
		t.Fatalf("request body = %#v, want confirmed linked replacement", requestBody)
	}
}

func TestOrgsDeleteRequiresYes(t *testing.T) {
	app := cli.NewApp()
	app.Commands = []*cli.Command{OrgsCommand}
	err := app.Run(NormalizeArgs(app, []string{
		"reactorcide", "orgs", "delete", "default", "--replacement", "linked",
	}))
	if err == nil || err.Error() != "organization deletion requires --yes" {
		t.Fatalf("error = %v, want --yes requirement", err)
	}
}
