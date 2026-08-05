package cmd

import (
	"reflect"
	"testing"

	"github.com/urfave/cli/v2"
)

func testApp() *cli.App {
	app := cli.NewApp()
	app.Name = "reactorcide"
	app.Commands = []*cli.Command{
		SecretGrantsCommand,
		SecretsCommand,
		JobsCommand,
		ProjectsCommand,
		WorkflowsCommand,
		TokenCommand,
	}
	return app
}

// NormalizeArgs must leave argv alone when there is nothing to reorder.
func TestNormalizeArgsNoOp(t *testing.T) {
	cases := [][]string{
		{"reactorcide"},
		{"reactorcide", "secret-grants"},
		{"reactorcide", "secret-grants", "get", "some-name"},
		{"reactorcide", "secret-grants", "list", "--format", "json"},
		{"reactorcide", "secret-grants", "apply", "--file", "grants.yaml", "--dry-run"},
		{"reactorcide", "jobs", "list", "--limit", "5"},
		{"reactorcide", "secret-grants", "set", "--secret-path", "p", "my-grant"},
	}
	for _, args := range cases {
		got := NormalizeArgs(testApp(), args)
		if !reflect.DeepEqual(got, args) {
			t.Fatalf("NormalizeArgs(%v) = %v, want no-op", args, got)
		}
	}
}

// A "--" terminator means the caller wants the remainder passed through
// verbatim, so NormalizeArgs must not reorder anything.
func TestNormalizeArgsLeavesTerminatorAlone(t *testing.T) {
	args := []string{"reactorcide", "jobs", "--", "get", "id", "--format", "json"}
	got := NormalizeArgs(testApp(), args)
	if !reflect.DeepEqual(got, args) {
		t.Fatalf("NormalizeArgs(%v) = %v, want no-op", args, got)
	}
}

func TestNormalizeArgsReordering(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "secret-grants set flags after name are moved before it",
			in: []string{"reactorcide", "secret-grants", "set", "my-grant",
				"--secret-path", "p", "--secret-match", "exact"},
			want: []string{"reactorcide", "secret-grants", "set",
				"--secret-path", "p", "--secret-match", "exact", "my-grant"},
		},
		{
			name: "persistent flags before the subcommand are preserved and skipped",
			in: []string{"reactorcide", "secret-grants", "--api-url", "http://x", "--token", "tok",
				"set", "my-grant", "--secret-path", "p"},
			want: []string{"reactorcide", "secret-grants", "--api-url", "http://x", "--token", "tok",
				"set", "--secret-path", "p", "my-grant"},
		},
		{
			name: "jobs get keeps --format after the job id parseable",
			in:   []string{"reactorcide", "jobs", "get", "job-1", "--format", "json"},
			want: []string{"reactorcide", "jobs", "get", "--format", "json", "job-1"},
		},
		{
			name: "projects update moves every trailing flag ahead of the id",
			in: []string{"reactorcide", "projects", "update", "p-1",
				"--description", "smoke test", "--timeout-seconds", "900"},
			want: []string{"reactorcide", "projects", "update",
				"--description", "smoke test", "--timeout-seconds", "900", "p-1"},
		},
		{
			name: "boolean flags do not swallow the following token",
			in:   []string{"reactorcide", "secrets", "set", "path", "key", "--stdin"},
			want: []string{"reactorcide", "secrets", "set", "--stdin", "path", "key"},
		},
		{
			name: "embedded flag values are not treated as value-taking",
			in:   []string{"reactorcide", "jobs", "get", "job-1", "--format=yaml"},
			want: []string{"reactorcide", "jobs", "get", "--format=yaml", "job-1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizeArgs(testApp(), tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("NormalizeArgs(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
