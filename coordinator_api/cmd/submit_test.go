package cmd

import (
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
)

func TestSpecToCreateJobRequestIncludesCheckoutMode(t *testing.T) {
	spec := &worker.JobSpec{Name: "eval", Command: "runnerlib eval", Environment: map[string]string{"EXISTING": "value"},
		Checkout: &worker.CheckoutSpec{Mode: "shared"}}

	request := specToCreateJobRequest(spec)

	if request.JobEnvVars["REACTORCIDE_CHECKOUT_MODE"] != "shared" {
		t.Fatal("checkout mode was not sent to the coordinator")
	}
	if request.JobEnvVars["EXISTING"] != "value" {
		t.Fatal("existing environment was not preserved")
	}
}

// TestSpecToCreateJobRequestSendsCommandPrefix: the coordinator reads the
// shell prefix from REACTORCIDE_JOB_SHELLCMD, so a submitted
// command_prefix must arrive there and win over the environment value.
func TestSpecToCreateJobRequestSendsCommandPrefix(t *testing.T) {
	spec := &worker.JobSpec{Name: "j", Command: "make", CommandPrefix: "bash -c",
		Environment: map[string]string{worker.ShellPrefixEnvVar: "zsh -c"}}
	if got := specToCreateJobRequest(spec).JobEnvVars[worker.ShellPrefixEnvVar]; got != "bash -c" {
		t.Fatalf("shell prefix = %q, want bash -c", got)
	}
	if spec.Environment[worker.ShellPrefixEnvVar] != "zsh -c" {
		t.Fatal("the spec environment must not change")
	}
	none := &worker.JobSpec{Name: "j", Command: "make"}
	if _, ok := specToCreateJobRequest(none).JobEnvVars[worker.ShellPrefixEnvVar]; ok {
		t.Fatal("no command_prefix must send no shell prefix")
	}
}

// TestAPIErrorTextShowsTheRefusalMessage: a JSON ErrorResponse shows its
// code and message, so that an image alias refusal is easy to read.
func TestAPIErrorTextShowsTheRefusalMessage(t *testing.T) {
	body := `{"error":"image_alias","message":"image alias \"win-vs\" needs the worker characteristic vs=2022, but the job needs vs=2019"}`
	got := (&apiError{StatusCode: 400, Body: body}).Error()
	want := `API error (400 image_alias): image alias "win-vs" needs the worker characteristic vs=2022, but the job needs vs=2019`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := apiErrorText(502, "bad gateway"); got != "API error (502): bad gateway" {
		t.Fatalf("plain body: %q", got)
	}
}
