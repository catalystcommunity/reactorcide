package models

import (
	"fmt"
	"strings"
)

// SummarizeDecisionReasons turns structured reasons into the one-line human
// summary stored in workflow_nodes.decision_reason. The structured reasons
// stay the source of truth; this is for logs, PR comments and old clients.
func SummarizeDecisionReasons(reasons []DecisionReason) string {
	parts := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		if text := summarizeDecisionReason(reason); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "; ")
}

func summarizeDecisionReason(r DecisionReason) string {
	matched := r.Matched == nil || *r.Matched
	switch r.Kind {
	case "event":
		if matched {
			return fmt.Sprintf("event %s matches %s", r.Value, listText(r.Patterns))
		}
		return fmt.Sprintf("event %s is not in %s", r.Value, listText(r.Patterns))
	case "branch":
		if r.Detail != "" && matched {
			return r.Detail
		}
		if matched {
			return fmt.Sprintf("branch %s matches %s", r.Value, listText(r.Patterns))
		}
		return fmt.Sprintf("branch %s does not match %s", r.Value, listText(r.Patterns))
	case "paths":
		if r.Detail != "" {
			return r.Detail
		}
		filter := pathsFilterText(r.Include, r.Exclude)
		if matched {
			count := len(r.MatchedFiles)
			if r.MatchedCount != nil {
				count = *r.MatchedCount
			}
			noun := "files match"
			if count == 1 {
				noun = "file matches"
			}
			return fmt.Sprintf("%d changed %s %s", count, noun, filter)
		}
		return "no changed file matches " + filter
	case "schedule":
		required := r.Required != nil && *r.Required
		switch {
		case required && matched:
			return "schedule event; the job runs only on schedules"
		case required:
			return "not a schedule event; the job runs only on schedules"
		case matched:
			return "not a schedule event; the job never runs on schedules"
		default:
			return "schedule event; the job never runs on schedules"
		}
	case "dependency":
		if r.Matched == nil {
			if r.Detail != "" {
				return r.Detail
			}
			return "waiting for dependencies"
		}
		condition := r.Condition
		if condition == "" {
			condition = "all_success"
		}
		if len(r.Deps) == 0 {
			if matched {
				return "no dependencies"
			}
			return fmt.Sprintf("condition %s is false", condition)
		}
		deps := make([]string, 0, len(r.Deps))
		for _, dep := range r.Deps {
			deps = append(deps, dep.Name+" "+dep.Status)
		}
		state := "true"
		if !matched {
			state = "false"
		}
		return fmt.Sprintf("condition %s is %s (%s)", condition, state, strings.Join(deps, ", "))
	default:
		return r.Detail
	}
}

func listText(values []string) string {
	if len(values) == 0 {
		return "[]"
	}
	return "[" + strings.Join(values, ", ") + "]"
}

func pathsFilterText(include, exclude []string) string {
	text := "paths"
	if len(include) > 0 {
		text += " include " + listText(include)
	}
	if len(exclude) > 0 {
		text += " exclude " + listText(exclude)
	}
	return text
}
