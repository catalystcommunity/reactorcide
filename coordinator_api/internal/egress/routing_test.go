package egress

import (
	"context"
	"errors"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

type fakeSetting struct {
	on  bool
	err error
}

func (f fakeSetting) EffectiveEgressControl(context.Context, string, *string, string) (bool, error) {
	return f.on, f.err
}

func TestRouteJob(t *testing.T) {
	job := &models.Job{OrgID: "org", Characteristics: characteristics.Characteristics{"os": characteristics.StringValue("linux")}}
	RouteJob(context.Background(), fakeSetting{on: true}, job)
	if v, ok := job.Characteristics[WorkerCharacteristic].(characteristics.BoolValue); !ok || !bool(v) {
		t.Fatalf("characteristics = %#v, want egress_proxy=true", job.Characteristics)
	}
	if _, ok := job.Characteristics["os"]; !ok {
		t.Fatal("the other characteristics stay")
	}

	// The worker side parses "egress_proxy=true" to the same value, so the
	// queue matches an egress worker only.
	worker, err := characteristics.ParseCustomValue("true")
	if err != nil {
		t.Fatal(err)
	}
	if !characteristics.Satisfies(characteristics.Characteristics{"os": characteristics.StringValue("linux"), WorkerCharacteristic: worker}, job.Characteristics) {
		t.Fatal("an egress worker does not satisfy the queue")
	}
	if characteristics.Satisfies(characteristics.Characteristics{"os": characteristics.StringValue("linux")}, job.Characteristics) {
		t.Fatal("a worker without an egress proxy satisfies the queue")
	}

	for name, setting := range map[string]any{"off": fakeSetting{}, "error": fakeSetting{on: true, err: errors.New("x")}, "no reader": struct{}{}} {
		job := &models.Job{OrgID: "org"}
		RouteJob(context.Background(), setting, job)
		if _, ok := job.Characteristics[WorkerCharacteristic]; ok {
			t.Fatalf("%s: the job was routed", name)
		}
	}
}
