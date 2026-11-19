package outboundevents

import (
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// Matches reports whether event matches the scope and the filters of sub. A
// disabled subscription, or one whose owner principal is disabled, matches
// nothing. An empty filter list matches everything.
func Matches(sub *models.EventSubscription, event *models.OutboxEventFacts) bool {
	if sub == nil || event == nil || !sub.Enabled || !sub.OwnerActive || sub.OrgID != event.OrgID {
		return false
	}
	projectID := strValue(event.ProjectID)
	switch sub.Scope {
	case models.SubscriptionScopeOrg:
	case models.SubscriptionScopeProject:
		if sub.ProjectID == nil || *sub.ProjectID != projectID {
			return false
		}
	case models.SubscriptionScopeWorkflow:
		securityID := firstNonEmpty(event.WorkflowSecurityID, event.WorkflowSecID)
		if sub.ProjectID == nil || *sub.ProjectID != projectID || sub.WorkflowSecurityID == "" || sub.WorkflowSecurityID != securityID {
			return false
		}
	case models.SubscriptionScopeOwnerGroup:
		if sub.OwnerGroup == "" || sub.OwnerGroup != event.OwnerGroup {
			return false
		}
		if sub.ProjectID != nil && *sub.ProjectID != projectID {
			return false
		}
	default:
		return false
	}
	if len(sub.EventTypes) > 0 && !contains(sub.EventTypes, event.EventType) {
		return false
	}
	if len(sub.RefClassesAny) > 0 && !intersects(sub.RefClassesAny, event.RefClasses) {
		return false
	}
	// A results filter matches only events with a result in the list. Phase
	// events with no result (pre_start, started) do not match it.
	if len(sub.Results) > 0 && (event.Result == "" || !contains(sub.Results, event.Result)) {
		return false
	}
	if len(sub.ActorKinds) > 0 && !contains(sub.ActorKinds, event.ActorKind) {
		return false
	}
	// A failure class filter matches only events with a class in the list.
	// An event with no class (a success, a phase event) does not match it.
	if len(sub.FailureClasses) > 0 && (event.FailureClass == "" || !contains(sub.FailureClasses, event.FailureClass)) {
		return false
	}
	return true
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func intersects(a, b []string) bool {
	for _, item := range a {
		if contains(b, item) {
			return true
		}
	}
	return false
}
