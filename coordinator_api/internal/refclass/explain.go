package refclass

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// RuleCheck tells whether one operator rule matched the facts.
type RuleCheck struct {
	RuleID  string   `json:"rule_id"`
	Name    string   `json:"name"`
	Scope   string   `json:"scope"`
	Enabled bool     `json:"enabled"`
	Matched bool     `json:"matched"`
	Classes []string `json:"classes"`
}

// Explanation is the output of Explain: the completed facts, the result,
// and each rule that the coordinator evaluated.
type Explanation struct {
	Facts   Facts                   `json:"facts"`
	Classes []string                `json:"classes"`
	Reasons []models.RefClassReason `json:"reasons"`
	RuleIDs []string                `json:"rule_ids"`
	Rules   []RuleCheck             `json:"rules"`
	// Builtin maps each built-in rule to its classes.
	Builtin map[string][]string `json:"builtin"`
}

// Explain classifies facts with the stored rules of the organization and the
// project, and tells which rules matched. rulesStore nil uses only the
// built-in rules.
func Explain(ctx context.Context, rulesStore store.RefClassStore, orgID string, projectID *string, facts Facts) (Explanation, error) {
	var rules []models.RefClassRule
	if rulesStore != nil {
		loaded, err := rulesStore.ListEffectiveRefClassRules(ctx, orgID, projectID)
		if err != nil {
			return Explanation{}, err
		}
		rules = loaded
	}
	return ExplainWithRules(facts, rules), nil
}

// ExplainWithRules is Explain with the rules given.
func ExplainWithRules(facts Facts, rules []models.RefClassRule) Explanation {
	facts = CompleteFacts(facts)
	result := Classify(facts, rules)
	explanation := Explanation{Facts: facts, Classes: result.Classes, Reasons: result.Reasons, RuleIDs: result.RuleIDs,
		Rules: []RuleCheck{}, Builtin: BuiltinRuleNames()}
	for _, rule := range rules {
		scope := "org"
		if rule.ProjectID != nil && *rule.ProjectID != "" {
			scope = "project"
		}
		explanation.Rules = append(explanation.Rules, RuleCheck{RuleID: rule.RuleID, Name: rule.Name, Scope: scope,
			Enabled: rule.Enabled, Matched: rule.Enabled && RuleMatches(rule, facts), Classes: Normalize(rule.Classes)})
	}
	return explanation
}

// FactsOfEvent returns the stored facts of an event. An event from before
// ref classes has no stored facts; then the facts come from its columns.
func FactsOfEvent(event *models.CIEvent) Facts {
	if event.RefClassFacts != nil && *event.RefClassFacts != (models.RefClassFacts{}) {
		return *event.RefClassFacts
	}
	facts := Facts{Source: event.Source, EventType: event.EventType, Branch: event.Branch, IsPR: event.PRNumber != nil}
	if event.PRNumber != nil {
		facts.Branch, facts.HeadBranch, facts.BaseBranch = "", event.Summary.HeadRef, event.Summary.BaseRef
	}
	return facts
}

// EventExplanation is Explanation with the stored classes of an event.
type EventExplanation struct {
	Explanation
	EventID       string   `json:"event_id,omitempty"`
	StoredClasses []string `json:"stored_classes,omitempty"`
}

// ExplainRequest explains the facts, or with eventID the stored facts of
// that event. The event must belong to the organization, and to the project
// when projectID is set. st is the store; it may implement
// store.CIEventStore and store.RefClassStore.
func ExplainRequest(ctx context.Context, st interface{}, orgID string, projectID *string, eventID string, facts Facts) (*EventExplanation, error) {
	var stored []string
	if eventID != "" {
		events, ok := st.(store.CIEventStore)
		if !ok {
			return nil, store.ErrNotFound
		}
		event, err := events.GetCIEvent(ctx, eventID)
		if err != nil {
			return nil, err
		}
		if event.OrgID != orgID || (projectID != nil && (event.ProjectID == nil || *event.ProjectID != *projectID)) {
			return nil, store.ErrNotFound
		}
		facts = FactsOfEvent(event)
		if projectID == nil {
			projectID = event.ProjectID
		}
		stored = ForEvent(event)
	}
	rulesStore, _ := st.(store.RefClassStore)
	explanation, err := Explain(ctx, rulesStore, orgID, projectID, facts)
	if err != nil {
		return nil, err
	}
	return &EventExplanation{Explanation: explanation, EventID: eventID, StoredClasses: stored}, nil
}
