package cmd

import (
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/urfave/cli/v2"
)

func shiftContext(t *testing.T, args ...string) *cli.Context {
	t.Helper()
	set := flag.NewFlagSet("test", flag.ContinueOnError)
	for _, f := range shiftFlags() {
		if err := f.Apply(set); err != nil {
			t.Fatal(err)
		}
	}
	if err := set.Parse(args); err != nil {
		t.Fatal(err)
	}
	return cli.NewContext(nil, set, nil)
}

func TestShiftsFromFlags(t *testing.T) {
	shifts, err := shiftsFromFlags(shiftContext(t))
	if err != nil || shifts != nil {
		t.Fatalf("no flags: %v %v", shifts, err)
	}
	shifts, err = shiftsFromFlags(shiftContext(t, "--memory", "8Gi", "--timeout", "2h", "--cpu", "4", "--storage-request", "5Gi"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"memory": "8Gi", "timeout": "2h", "cpu": "4", "storage_request": "5Gi"}
	if len(shifts) != len(want) {
		t.Fatalf("got %v", shifts)
	}
	for key, value := range want {
		if shifts[key] != value {
			t.Fatalf("%s = %v, want %v", key, shifts[key], value)
		}
	}
	if _, err := shiftsFromFlags(shiftContext(t, "--memory", "lots")); err == nil {
		t.Fatal("expected an invalid memory value to be refused before the request")
	}
	body, err := shiftsBody(shiftContext(t, "--timeout", "90m"))
	if err != nil {
		t.Fatal(err)
	}
	if encoded, _ := json.Marshal(body); string(encoded) != `{"shifts":{"timeout":"90m"}}` {
		t.Fatalf("body = %s", encoded)
	}
	if got := csilShifts(map[string]any{"memory": "8Gi"}); got == nil || got.Memory == nil || *got.Memory != "8Gi" || got.Cpu != nil {
		t.Fatalf("csil shifts = %+v", got)
	}
}

// jobs retry sends the shifts as the REST body.
func TestJobsRetrySendsShifts(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/jobs/job-1/retry" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&received)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"job_id":"job-2","status":"submitted"}`))
	}))
	defer server.Close()
	app := &cli.App{Commands: []*cli.Command{JobsCommand}}
	err := app.Run([]string{"reactorcide", "jobs", "retry", "--api-url", server.URL, "--allow-insecure-transport",
		"--token", "t", "--memory", "8Gi", "--format", "json", "job-1"})
	if err != nil {
		t.Fatal(err)
	}
	shifts, _ := received["shifts"].(map[string]any)
	if shifts["memory"] != "8Gi" || len(received) != 1 {
		t.Fatalf("received %v", received)
	}
}
