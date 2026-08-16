// Package workflowengine contains workflow graph rules that are independent
// from storage and execution. The coordinator and run-local use these rules.
package workflowengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// Store provides workflow node state to the shared engine and persists the
// non-execution decisions that it makes.
type Store interface {
	Nodes(context.Context) ([]Node, error)
	ApplyDecision(context.Context, Decision) error
}

// Executor starts one node that the shared engine selected.
type Executor interface {
	Start(context.Context, Decision) error
}

// Engine advances one workflow graph through storage and execution adapters.
// The coordinator uses database-backed adapters. run-local uses memory-backed
// adapters and the normal local job execution path.
type Engine struct {
	Store    Store
	Executor Executor
}

// Advance evaluates the current graph and applies every selected decision.
// maxStarts limits new executions. A value below zero does not apply a limit.
func (e Engine) Advance(ctx context.Context, maxStarts int) ([]Decision, error) {
	nodes, err := e.Store.Nodes(ctx)
	if err != nil {
		return nil, err
	}
	decisions := Decide(nodes, maxStarts)
	for i, decision := range decisions {
		if decision.Action == ActionStart {
			if err := e.Executor.Start(ctx, decision); err != nil {
				return decisions[:i], err
			}
			continue
		}
		if err := e.Store.ApplyDecision(ctx, decision); err != nil {
			return decisions[:i], err
		}
	}
	return decisions, nil
}

// Node is the state that the workflow graph rules need for one node.
type Node struct {
	ID          string
	Name        string
	DisplayName string
	Status      string
	DependsOn   []string
	Condition   string
}

// Action is the next state transition for a workflow node.
type Action string

const (
	ActionWait  Action = "wait"
	ActionSkip  Action = "skip"
	ActionStart Action = "start"
)

// Decision describes one node transition selected by Decide.
type Decision struct {
	NodeID string
	Action Action
	Reason string
}

// Decide applies dependency and condition rules to pending nodes. maxStarts
// limits start decisions. A value below zero does not apply a limit.
func Decide(nodes []Node, maxStarts int) []Decision {
	working := append([]Node(nil), nodes...)
	decisions := make([]Decision, 0, len(working))
	started := 0
	for i := range working {
		node := &working[i]
		if node.Status != "pending" && node.Status != "waiting" {
			continue
		}
		ready, reason := DependenciesReady(working, *node)
		if !ready {
			decisions = append(decisions, Decision{NodeID: node.ID, Action: ActionWait, Reason: reason})
			node.Status = "waiting"
			continue
		}
		allowed, reason := EvaluateCondition(working, *node)
		if !allowed {
			decisions = append(decisions, Decision{NodeID: node.ID, Action: ActionSkip, Reason: reason})
			node.Status = "skipped"
			continue
		}
		if maxStarts >= 0 && started >= maxStarts {
			continue
		}
		decisions = append(decisions, Decision{NodeID: node.ID, Action: ActionStart, Reason: reason})
		node.Status = "running"
		started++
	}
	return decisions
}

// ExpansionSpec contains the common fan-out fields from a resolved job.
type ExpansionSpec struct {
	Name    string
	ForEach []interface{}
	ItemVar string
	Payload interface{}
}

// Expansion identifies one node produced from a resolved job.
type Expansion struct {
	Name        string
	DisplayName string
	ItemIndex   *int
	ItemValue   interface{}
	ItemVar     string
	Payload     interface{}
}

// Expand applies the workflow fan-out naming and item-variable rules.
func Expand(specs []ExpansionSpec) []Expansion {
	var result []Expansion
	for _, spec := range specs {
		if len(spec.ForEach) == 0 {
			result = append(result, Expansion{Name: spec.Name, DisplayName: spec.Name, Payload: spec.Payload})
			continue
		}
		itemVar := spec.ItemVar
		if itemVar == "" {
			itemVar = "ITEM"
		}
		for i, item := range spec.ForEach {
			index := i
			result = append(result, Expansion{
				Name: spec.Name, DisplayName: fmt.Sprintf("%s[%d]", spec.Name, i),
				ItemIndex: &index, ItemValue: item, ItemVar: itemVar, Payload: spec.Payload,
			})
		}
	}
	return result
}

// Value is one workflow variable update.
type Value struct {
	Key   string
	Value interface{}
}

// OutputValues converts a node output document to workflow variable names.
func OutputValues(nodeName string, vars, outputs map[string]interface{}) []Value {
	result := make([]Value, 0, len(vars)+len(outputs))
	for key, value := range vars {
		result = append(result, Value{Key: key, Value: value})
	}
	for key, value := range outputs {
		result = append(result, Value{Key: nodeName + "." + key, Value: value})
	}
	return result
}

// MergeValue adds one workflow value and rejects a different value for an
// existing key. It returns true when it added the key.
func MergeValue(values map[string]interface{}, key string, value interface{}) (bool, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return false, nil
	}
	if existing, ok := values[key]; ok {
		if ValueHash(existing) != ValueHash(value) {
			return false, fmt.Errorf("workflow variable %q conflict", key)
		}
		return false, nil
	}
	values[key] = value
	return true, nil
}

// ValueHash returns the stable comparison hash used by both workflow stores.
// Scalar values use the database JSON wrapper so local and coordinator
// conflict decisions are identical.
func ValueHash(value interface{}) string {
	data, _ := json.Marshal(value)
	var normalized interface{}
	_ = json.Unmarshal(data, &normalized)
	if _, ok := normalized.(map[string]interface{}); !ok {
		normalized = map[string]interface{}{"value": normalized}
	}
	data, _ = json.Marshal(normalized)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// DependenciesReady reports whether all instances of each named dependency
// are terminal.
func DependenciesReady(nodes []Node, node Node) (bool, string) {
	for _, dependency := range node.DependsOn {
		group := nodesByName(nodes, dependency)
		if len(group) == 0 {
			return false, "waiting for dependency " + dependency + " to be registered"
		}
		for _, candidate := range group {
			if !Terminal(candidate.Status) {
				return false, "waiting on " + candidate.DisplayName
			}
		}
	}
	return true, "all dependencies terminal"
}

// EvaluateCondition applies the supported dependency conditions.
func EvaluateCondition(nodes []Node, node Node) (bool, string) {
	condition := strings.TrimSpace(node.Condition)
	if condition == "" || condition == "all_success" || condition == "all_success(needs)" {
		for _, dependency := range node.DependsOn {
			for _, candidate := range nodesByName(nodes, dependency) {
				if candidate.Status != "completed" {
					return false, "condition all_success(needs) is false"
				}
			}
		}
		return true, "condition all_success(needs) is true"
	}
	if condition == "always" || condition == "always()" {
		return true, "condition always() is true"
	}
	if condition == "any_failed" || condition == "any_failed(needs)" {
		for _, dependency := range node.DependsOn {
			for _, candidate := range nodesByName(nodes, dependency) {
				if Failure(candidate.Status) {
					return true, "condition any_failed(needs) is true"
				}
			}
		}
		return false, "condition any_failed(needs) is false"
	}
	return false, "unsupported condition " + condition
}

// ComputeStatus derives a workflow result from its node states.
func ComputeStatus(nodes []Node) string {
	if len(nodes) == 0 {
		return "skipped"
	}
	allSkipped := true
	allTerminal := true
	anyCancelled := false
	for _, node := range nodes {
		if node.Status == "failed" || node.Status == "timeout" {
			return "failed"
		}
		if node.Status == "cancelled" {
			anyCancelled = true
		}
		if node.Status != "skipped" {
			allSkipped = false
		}
		if !Terminal(node.Status) {
			allTerminal = false
		}
	}
	if !allTerminal {
		return "running"
	}
	if anyCancelled {
		return "cancelled"
	}
	if allSkipped {
		return "skipped"
	}
	return "success"
}

// Terminal reports whether a node cannot change through normal execution.
func Terminal(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled" || status == "timeout" || status == "skipped"
}

// Failure reports whether a node result satisfies any_failed.
func Failure(status string) bool {
	return status == "failed" || status == "cancelled" || status == "timeout"
}

func nodesByName(nodes []Node, name string) []Node {
	var matches []Node
	for _, node := range nodes {
		if node.Name == name {
			matches = append(matches, node)
		}
	}
	return matches
}
