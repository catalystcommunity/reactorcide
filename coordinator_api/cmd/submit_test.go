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
