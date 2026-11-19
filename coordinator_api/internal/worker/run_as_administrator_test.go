package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func boolPtrForTest(v bool) *bool { return &v }

func TestRunAsAdministrator_LoadJobSpec(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	spec, err := LoadJobSpec(write("false.yaml", "name: j\nimage: a:1\ncommand: \"true\"\nrun_as:\n  user: runner\n  administrator: false\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if spec.RunAs == nil || spec.RunAs.Administrator == nil || *spec.RunAs.Administrator {
		t.Fatalf("administrator = %#v, want false", spec.RunAs)
	}
	cfg := spec.ToJobConfig("/ws", "job", "q")
	if cfg.RunAsAdministrator == nil || *cfg.RunAsAdministrator {
		t.Fatalf("JobConfig.RunAsAdministrator = %v, want false", cfg.RunAsAdministrator)
	}
	if cfg.RunAsAdministrator == spec.RunAs.Administrator {
		t.Fatal("JobConfig must hold a copy of the spec value")
	}

	// Eval format (nested job block) carries it too.
	spec, err = LoadJobSpec(write("eval.yaml", "name: j\njob:\n  image: a:1\n  command: \"true\"\n  run_as:\n    administrator: true\n"))
	if err != nil {
		t.Fatalf("load eval format: %v", err)
	}
	if cfg := spec.ToJobConfig("/ws", "job", "q"); cfg.RunAsAdministrator == nil || !*cfg.RunAsAdministrator {
		t.Fatalf("eval format: RunAsAdministrator = %v, want true", cfg.RunAsAdministrator)
	}

	// Unset stays nil.
	spec, err = LoadJobSpec(write("unset.yaml", "name: j\nimage: a:1\ncommand: \"true\"\nrun_as:\n  user: root\n"))
	if err != nil {
		t.Fatalf("load unset: %v", err)
	}
	if cfg := spec.ToJobConfig("/ws", "job", "q"); cfg.RunAsAdministrator != nil {
		t.Fatalf("unset: RunAsAdministrator = %v, want nil", *cfg.RunAsAdministrator)
	}

	// A non-boolean value is a parse error (YAML and JSON).
	if _, err := LoadJobSpec(write("bad.yaml", "name: j\nimage: a:1\ncommand: \"true\"\nrun_as:\n  administrator: maybe\n")); err == nil {
		t.Fatal("non-boolean YAML administrator must fail")
	}
	if _, err := LoadJobSpec(write("bad.json", `{"name":"j","image":"a:1","command":"true","run_as":{"administrator":"false"}}`)); err == nil {
		t.Fatal("non-boolean JSON administrator must fail")
	}
}

func TestRunAsAdministrator_MergeJobSpecs(t *testing.T) {
	base := &JobSpec{Command: "true", RunAs: &RunAsSpec{User: "runner", Administrator: boolPtrForTest(false)}}

	// An overlay that does not set administrator keeps the base value.
	merged, _ := MergeJobSpecs(base, []*JobSpec{{RunAs: &RunAsSpec{User: "root"}}}, nil)
	if merged.RunAs.User != "root" || merged.RunAs.Administrator == nil || *merged.RunAs.Administrator {
		t.Fatalf("merged = %#v, want user root, administrator false", merged.RunAs)
	}
	if merged.RunAs.Administrator == base.RunAs.Administrator {
		t.Fatal("merge must copy the base pointer")
	}

	// An overlay that sets it wins.
	merged, _ = MergeJobSpecs(base, []*JobSpec{{RunAs: &RunAsSpec{User: "runner", Administrator: boolPtrForTest(true)}}}, nil)
	if merged.RunAs.Administrator == nil || !*merged.RunAs.Administrator {
		t.Fatalf("overlay true not applied: %#v", merged.RunAs)
	}
	if *base.RunAs.Administrator {
		t.Fatal("merge must not change the base spec")
	}

	// No overlay run_as: base is copied.
	merged, _ = MergeJobSpecs(base, []*JobSpec{{}}, nil)
	if merged.RunAs == nil || merged.RunAs.Administrator == nil || *merged.RunAs.Administrator {
		t.Fatalf("base not kept: %#v", merged.RunAs)
	}
}

func TestRunAsAdministrator_TriggerProcessorRoundTrip(t *testing.T) {
	tp := NewTriggerProcessor(&MockStore{}, nil)
	parent := &models.Job{JobID: "p", UserID: "u", QueueName: "q", RunnerImage: "r:1", TimeoutSeconds: 60}

	// triggers.json key run_as_administrator decodes into the spec.
	var spec triggerJobSpec
	if err := json.Unmarshal([]byte(`{"job_name":"w","run_as_user":"runner","run_as_administrator":false}`), &spec); err != nil {
		t.Fatal(err)
	}
	if spec.RunAsAdministrator == nil || *spec.RunAsAdministrator {
		t.Fatalf("decoded = %v, want false", spec.RunAsAdministrator)
	}
	// Node specs are stored as JSON; the value survives a round trip and
	// is omitted when unset.
	out, _ := json.Marshal(spec)
	if !strings.Contains(string(out), `"run_as_administrator":false`) {
		t.Fatalf("encoded spec lost the value: %s", out)
	}
	unsetOut, _ := json.Marshal(triggerJobSpec{JobName: "w"})
	if strings.Contains(string(unsetOut), "run_as_administrator") {
		t.Fatalf("unset value must be omitted: %s", unsetOut)
	}

	job, err := tp.buildJobFromTrigger(spec, parent)
	if err != nil {
		t.Fatalf("buildJobFromTrigger: %v", err)
	}
	if job.RunAsAdministrator == nil || *job.RunAsAdministrator {
		t.Fatalf("job.RunAsAdministrator = %v, want false", job.RunAsAdministrator)
	}
	payload := tp.buildTaskPayload(job)
	if v, ok := payload.Config["run_as_administrator"]; !ok || v != false {
		t.Fatalf("payload run_as_administrator = %v (%v), want false", v, ok)
	}

	unsetJob, err := tp.buildJobFromTrigger(triggerJobSpec{JobName: "w"}, parent)
	if err != nil {
		t.Fatal(err)
	}
	if unsetJob.RunAsAdministrator != nil {
		t.Fatal("unset trigger must leave job.RunAsAdministrator nil")
	}
	if _, ok := tp.buildTaskPayload(unsetJob).Config["run_as_administrator"]; ok {
		t.Fatal("unset value must not be in the payload")
	}

	// Overlay: a trigger value wins over the job file; no trigger value
	// keeps the job file value.
	base := triggerJobSpec{JobName: "w", RunAsAdministrator: boolPtrForTest(true)}
	if got := tp.overlaySpec(base, triggerJobSpec{}); got.RunAsAdministrator == nil || !*got.RunAsAdministrator {
		t.Fatalf("overlay without value: %v, want true", got.RunAsAdministrator)
	}
	if got := tp.overlaySpec(base, triggerJobSpec{RunAsAdministrator: boolPtrForTest(false)}); got.RunAsAdministrator == nil || *got.RunAsAdministrator {
		t.Fatalf("overlay false: %v, want false", got.RunAsAdministrator)
	}
}

func TestRunAsAdministrator_TriggerJobFile(t *testing.T) {
	tp := NewTriggerProcessor(&MockStore{}, nil)
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "src", "jobs"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(ws, "src", "jobs", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("w.yaml", "name: w\njob:\n  image: a:1\n  command: \"true\"\n  run_as:\n    user: runner\n    administrator: false\n")
	spec, err := tp.loadJobFile(ws, "jobs/w.yaml")
	if err != nil {
		t.Fatalf("loadJobFile: %v", err)
	}
	if spec.RunAsUser != "runner" || spec.RunAsAdministrator == nil || *spec.RunAsAdministrator {
		t.Fatalf("spec = user %q admin %v, want runner/false", spec.RunAsUser, spec.RunAsAdministrator)
	}

	write("bad.yaml", "name: w\njob:\n  image: a:1\n  command: \"true\"\n  run_as:\n    administrator: maybe\n")
	if _, err := tp.loadJobFile(ws, "jobs/bad.yaml"); err == nil {
		t.Fatal("non-boolean administrator in a job file must fail")
	}
}

func TestRunAsUser_MergeJobSpecsFieldByField(t *testing.T) {
	base := &JobSpec{Command: "true", RunAs: &RunAsSpec{User: "root"}}

	// An overlay that sets only administrator keeps the base user.
	merged, _ := MergeJobSpecs(base, []*JobSpec{{RunAs: &RunAsSpec{Administrator: boolPtrForTest(false)}}}, nil)
	if merged.RunAs.User != "root" || merged.RunAs.Administrator == nil || *merged.RunAs.Administrator {
		t.Fatalf("merged = %#v, want user root, administrator false", merged.RunAs)
	}

	// An overlay that sets user wins; "runner" selects the default user.
	merged, _ = MergeJobSpecs(base, []*JobSpec{{RunAs: &RunAsSpec{User: "runner"}}}, nil)
	if merged.RunAs.User != "runner" {
		t.Fatalf("merged user = %q, want runner", merged.RunAs.User)
	}

	// An empty overlay run_as block changes nothing.
	merged, _ = MergeJobSpecs(base, []*JobSpec{{RunAs: &RunAsSpec{}}}, nil)
	if merged.RunAs.User != "root" || merged.RunAs.Administrator != nil {
		t.Fatalf("merged = %#v, want the base run_as", merged.RunAs)
	}
	if base.RunAs.User != "root" {
		t.Fatal("merge must not change the base spec")
	}
}
