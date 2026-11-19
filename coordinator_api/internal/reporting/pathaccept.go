package reporting

import (
	"fmt"
	"sort"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// Owner acceptance of path dependencies (plan item A7). A workflow can claim
// any path with paths.include. With path_acceptance on, a matched file in an
// area of another owner counts only when that owner accepted the dependency
// for the workflow. A file with no owner counts for every workflow. A
// workflow with no known owner is foreign to every owned area.

// ForeignArea is an owned area with matched files that no acceptance covers.
type ForeignArea struct {
	Pattern string   `json:"pattern"`
	Owners  []string `json:"owners"`
	Files   []string `json:"files"`
}

// PathDecision is the result of the acceptance check of one workflow.
type PathDecision struct {
	// Kept are the matched files that count.
	Kept []string
	// Unaccepted are the matched files in foreign areas with no acceptance.
	Unaccepted []string
	Foreign    []ForeignArea
}

// AcceptanceIndex holds the active acceptances of a project.
type AcceptanceIndex struct {
	byWorkflow map[string][]compiledAcceptance
}

type compiledAcceptance struct {
	owner   string
	matches func(string) bool
}

// NewAcceptanceIndex compiles the active acceptances.
func NewAcceptanceIndex(rows []models.PathAcceptance) *AcceptanceIndex {
	index := &AcceptanceIndex{byWorkflow: map[string][]compiledAcceptance{}}
	for _, row := range rows {
		if !row.Active() {
			continue
		}
		matches, err := CompilePattern(row.PathPattern)
		if err != nil {
			continue
		}
		index.byWorkflow[row.WorkflowSecurityID] = append(index.byWorkflow[row.WorkflowSecurityID],
			compiledAcceptance{owner: strings.ToLower(row.OwnerSubject), matches: matches})
	}
	return index
}

func (index *AcceptanceIndex) accepted(workflowID, file string, owners []string) bool {
	if index == nil {
		return false
	}
	for _, acceptance := range index.byWorkflow[workflowID] {
		if !acceptance.matches(file) {
			continue
		}
		for _, owner := range owners {
			if strings.ToLower(owner) == acceptance.owner {
				return true
			}
		}
	}
	return false
}

func intersects(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if strings.EqualFold(x, y) {
				return true
			}
		}
	}
	return false
}

// CheckPaths sorts the matched files of a workflow into the files that count
// and the files in foreign areas with no acceptance.
func CheckPaths(files []string, workflowID string, workflowOwners []string, ownership OwnershipMap, index *AcceptanceIndex) PathDecision {
	decision := PathDecision{Kept: []string{}, Unaccepted: []string{}}
	foreign := map[string]*ForeignArea{}
	order := []string{}
	for _, file := range files {
		if ownership == nil {
			decision.Kept = append(decision.Kept, file)
			continue
		}
		area, ok := ownership.AreaOf(file)
		if !ok || len(area.Owners) == 0 || intersects(area.Owners, workflowOwners) || index.accepted(workflowID, file, area.Owners) {
			decision.Kept = append(decision.Kept, file)
			continue
		}
		decision.Unaccepted = append(decision.Unaccepted, file)
		key := area.Pattern + "\x00" + strings.Join(area.Owners, "\x00")
		entry, exists := foreign[key]
		if !exists {
			entry = &ForeignArea{Pattern: area.Pattern, Owners: append([]string{}, area.Owners...)}
			foreign[key] = entry
			order = append(order, key)
		}
		entry.Files = append(entry.Files, file)
	}
	sort.Strings(order)
	for _, key := range order {
		decision.Foreign = append(decision.Foreign, *foreign[key])
	}
	return decision
}

// ForeignReason is the decision text, for example
// "paths in /payments/ are not accepted by owner reactorcide_group:payments".
func ForeignReason(areas []ForeignArea) string {
	parts := []string{}
	for _, area := range limitAreas(areas, 3) {
		parts = append(parts, fmt.Sprintf("paths in %s are not accepted by owner %s", area.Pattern, strings.Join(area.Owners, " or ")))
	}
	if len(areas) > 3 {
		parts = append(parts, fmt.Sprintf("and %d more areas", len(areas)-3))
	}
	return strings.Join(parts, "; ")
}

func limitAreas(areas []ForeignArea, limit int) []ForeignArea {
	if len(areas) <= limit {
		return areas
	}
	return areas[:limit]
}
