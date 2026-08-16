package workflowengine

import (
	"context"
	"testing"
)

type testRuntime struct {
	nodes   []Node
	started []string
}

func (r *testRuntime) Nodes(context.Context) ([]Node, error) {
	return append([]Node(nil), r.nodes...), nil
}

func (r *testRuntime) ApplyDecision(_ context.Context, decision Decision) error {
	for i := range r.nodes {
		if r.nodes[i].ID != decision.NodeID {
			continue
		}
		if decision.Action == ActionWait {
			r.nodes[i].Status = "waiting"
		} else if decision.Action == ActionSkip {
			r.nodes[i].Status = "skipped"
		}
	}
	return nil
}

func (r *testRuntime) Start(_ context.Context, decision Decision) error {
	r.started = append(r.started, decision.NodeID)
	for i := range r.nodes {
		if r.nodes[i].ID == decision.NodeID {
			r.nodes[i].Status = "running"
		}
	}
	return nil
}

func TestDecideAppliesDependencyConditionsInOnePass(t *testing.T) {
	nodes := []Node{
		{ID: "build", Name: "build", DisplayName: "build", Status: "failed"},
		{ID: "deploy", Name: "deploy", DisplayName: "deploy", Status: "pending", DependsOn: []string{"build"}, Condition: "all_success"},
		{ID: "notify", Name: "notify", DisplayName: "notify", Status: "pending", DependsOn: []string{"build"}, Condition: "any_failed"},
	}
	decisions := Decide(nodes, -1)
	if len(decisions) != 2 || decisions[0].Action != ActionSkip || decisions[1].Action != ActionStart {
		t.Fatalf("decisions = %#v", decisions)
	}
}

func TestExpandUsesProductionNamesAndItemVariable(t *testing.T) {
	expanded := Expand([]ExpansionSpec{{Name: "test", ForEach: []interface{}{"linux", "windows"}, Payload: "payload"}})
	if len(expanded) != 2 || expanded[0].DisplayName != "test[0]" || expanded[1].DisplayName != "test[1]" {
		t.Fatalf("expanded = %#v", expanded)
	}
	if expanded[0].ItemVar != "ITEM" || expanded[1].ItemValue != "windows" {
		t.Fatalf("expanded item data = %#v", expanded)
	}
}

func TestMergeValueUsesCoordinatorScalarNormalization(t *testing.T) {
	type storedJSON map[string]interface{}
	values := map[string]interface{}{"version": storedJSON{"value": "one"}}
	added, err := MergeValue(values, "version", "one")
	if err != nil || added {
		t.Fatalf("duplicate merge = %t, %v", added, err)
	}
	if _, err := MergeValue(values, "version", "two"); err == nil {
		t.Fatal("expected variable conflict")
	}
}

func TestOutputValuesNamespacesNodeOutputs(t *testing.T) {
	values := OutputValues("build", map[string]interface{}{"version": "1"}, map[string]interface{}{"artifact": "app.zip"})
	got := map[string]interface{}{}
	for _, value := range values {
		got[value.Key] = value.Value
	}
	if len(got) != 2 || got["version"] != "1" || got["build.artifact"] != "app.zip" {
		t.Fatalf("values = %#v", values)
	}
}

func TestEngineProducesSameDecisionsWithCoordinatorAndLocalLimits(t *testing.T) {
	nodes := []Node{
		{ID: "build-linux", Name: "build", DisplayName: "build[0]", Status: "completed"},
		{ID: "build-windows", Name: "build", DisplayName: "build[1]", Status: "failed"},
		{ID: "deploy", Name: "deploy", DisplayName: "deploy", Status: "pending", DependsOn: []string{"build"}},
		{ID: "notify", Name: "notify", DisplayName: "notify", Status: "pending", DependsOn: []string{"build"}, Condition: "any_failed"},
	}
	coordinator := &testRuntime{nodes: append([]Node(nil), nodes...)}
	local := &testRuntime{nodes: append([]Node(nil), nodes...)}
	coordinatorDecisions, err := (Engine{Store: coordinator, Executor: coordinator}).Advance(context.Background(), -1)
	if err != nil {
		t.Fatal(err)
	}
	localDecisions, err := (Engine{Store: local, Executor: local}).Advance(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(coordinatorDecisions) != len(localDecisions) {
		t.Fatalf("coordinator = %#v, local = %#v", coordinatorDecisions, localDecisions)
	}
	for i := range coordinatorDecisions {
		if coordinatorDecisions[i] != localDecisions[i] {
			t.Fatalf("decision %d: coordinator = %#v, local = %#v", i, coordinatorDecisions[i], localDecisions[i])
		}
	}
}
