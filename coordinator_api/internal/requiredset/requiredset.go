// Package requiredset decides which workflows of a CI event block a merge
// (plan items A1 and A4, owner decisions D9, D16, D26; docs/required-set.md).
//
// The required set of a project names workflow security IDs. Only the
// workflows in the set count toward "Reactorcide Checks". Other workflows are
// advisory: they run and report in the pull request comment, but they post no
// commit status of their own and their result never fails the aggregate.
//
// With the set off (the default, D16), every workflow is required, as before
// this feature. A quarantine is a time-limited removal from the set. The
// coordinator computes the state each time it reads it, so a quarantine ends
// at its expiry with no background work.
package requiredset

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/refclass"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// States of a workflow for one event or in a listing.
const (
	StateRequired    = "required"
	StateAdvisory    = "advisory"
	StateQuarantined = "quarantined"
)

// Classification is the state of one workflow for one event.
type Classification struct {
	State string
	// QuarantinedUntil is set for StateQuarantined: the first expiry of the
	// matching entries. The workflow is required again after it.
	QuarantinedUntil *time.Time
	QuarantineReason string
}

// Blocking reports whether the workflow counts toward "Reactorcide Checks".
func (c Classification) Blocking() bool { return c.State == "" || c.State == StateRequired }

// Label is the short text of a non-blocking classification for a status or
// a comment, or "" for a required workflow.
func (c Classification) Label() string {
	switch c.State {
	case StateAdvisory:
		return "advisory — not required"
	case StateQuarantined:
		if c.QuarantinedUntil != nil {
			return "quarantined until " + c.QuarantinedUntil.UTC().Format("2006-01-02 15:04 MST") + " — not required"
		}
		return "quarantined — not required"
	}
	return ""
}

// Required is the classification when the set does not apply.
var Required = Classification{State: StateRequired}

// Set is the required set of one project as one event sees it.
type Set struct {
	Enabled bool
	Entries []models.RequiredWorkflow
}

// Target is the branch that an event targets: the base branch of a pull
// request, or the branch of a push, a schedule, or a manual run. Known is
// false when the coordinator cannot tell the branch (an old pull request
// event with no stored base branch). Then every entry matches, so the
// workflow blocks (fail closed).
type Target struct {
	Branch string
	Known  bool
}

// EntryMatches reports whether an entry applies to the target branch.
func EntryMatches(entry models.RequiredWorkflow, target Target) bool {
	if entry.BranchPattern == "" || !target.Known {
		return true
	}
	return refclass.GlobMatch(entry.BranchPattern, target.Branch)
}

// Classify returns the state of one workflow for one target at now.
func (s Set) Classify(securityID string, target Target, now time.Time) Classification {
	if !s.Enabled {
		return Required
	}
	securityID = strings.TrimSpace(securityID)
	if securityID == "" {
		// A workflow with no security ID cannot be in the set. It cannot be
		// promoted either, so it blocks, as before the feature.
		return Required
	}
	var quarantined *models.RequiredWorkflow
	for i := range s.Entries {
		entry := s.Entries[i]
		if entry.WorkflowSecurityID != securityID || !EntryMatches(entry, target) {
			continue
		}
		if !entry.IsQuarantined(now) {
			return Required
		}
		if quarantined == nil || entry.QuarantinedUntil.Before(*quarantined.QuarantinedUntil) {
			quarantined = &s.Entries[i]
		}
	}
	if quarantined != nil {
		until := quarantined.QuarantinedUntil.UTC()
		return Classification{State: StateQuarantined, QuarantinedUntil: &until, QuarantineReason: quarantined.QuarantineReason}
	}
	return Classification{State: StateAdvisory}
}

// TargetOfEvent returns the target branch of an event.
func TargetOfEvent(event *models.CIEvent) Target {
	if event == nil {
		return Target{}
	}
	isPR := event.PRNumber != nil && *event.PRNumber > 0
	if event.RefClassFacts != nil {
		facts := event.RefClassFacts
		if facts.IsPR || isPR {
			if facts.BaseBranch != "" {
				return Target{Branch: facts.BaseBranch, Known: true}
			}
		} else {
			return Target{Branch: facts.Branch, Known: true}
		}
	}
	if isPR {
		if base := strings.TrimPrefix(event.Summary.BaseRef, "refs/heads/"); base != "" {
			return Target{Branch: base, Known: true}
		}
		return Target{}
	}
	// A push, a schedule, or a manual run. A tag has no branch, so only the
	// entries for every branch apply to it.
	return Target{Branch: event.Branch, Known: true}
}

// Load reads the set of a project. A store without required set support, an
// empty project, or a read error gives a set that is off. Callers that must
// fail closed on an error use LoadStrict.
func Load(ctx context.Context, value any, projectID string) Set {
	set, err := LoadStrict(ctx, value, projectID)
	if err != nil {
		return Set{}
	}
	return set
}

// LoadStrict reads the set of a project and returns read errors.
func LoadStrict(ctx context.Context, value any, projectID string) (Set, error) {
	st, ok := value.(store.RequiredSetStore)
	if !ok || projectID == "" {
		return Set{}, nil
	}
	settings, err := st.GetProjectRequiredSetSettings(ctx, projectID)
	if err != nil {
		return Set{}, err
	}
	if !settings.Enabled {
		return Set{}, nil
	}
	entries, err := st.ListRequiredWorkflows(ctx, projectID)
	if err != nil {
		return Set{}, err
	}
	return Set{Enabled: true, Entries: entries}, nil
}

// EventClassifier classifies the workflows of one event.
type EventClassifier struct {
	Set    Set
	Target Target
	Now    time.Time
}

// Classify returns the state of a workflow of the event.
func (c EventClassifier) Classify(securityID string) Classification {
	return c.Set.Classify(securityID, c.Target, c.Now)
}

// ForEvent loads the classifier of an event. When the set cannot be read,
// the classifier says "required" for every workflow (fail closed: an error
// never turns a required workflow advisory).
func ForEvent(ctx context.Context, value any, event *models.CIEvent) EventClassifier {
	classifier := EventClassifier{Target: TargetOfEvent(event), Now: time.Now().UTC()}
	if event == nil || event.ProjectID == nil {
		return classifier
	}
	classifier.Set = Load(ctx, value, *event.ProjectID)
	return classifier
}

// ApproversFor returns the approver subjects for a change of an entry with
// branchPattern, and where they come from: "rule N" (1-based), "setting",
// or "org_admins" when no subject is configured (then only org admins
// approve).
//
// The first rule with a glob that matches the pattern wins. A glob matches a
// pattern when it matches the pattern text as a branch name, or is equal to
// it. An entry for every branch (pattern "") uses only a rule that lists
// "**".
func ApproversFor(defaults []string, rules []models.RequiredSetApproverRule, branchPattern string) ([]string, string) {
	for i, rule := range rules {
		for _, glob := range rule.Branches {
			glob = strings.TrimSpace(glob)
			matched := false
			if branchPattern == "" {
				matched = glob == "**"
			} else {
				matched = glob == branchPattern || refclass.GlobMatch(glob, branchPattern)
			}
			if matched {
				return append([]string(nil), rule.Approvers...), fmt.Sprintf("rule %d", i+1)
			}
		}
	}
	if len(defaults) > 0 {
		return append([]string(nil), defaults...), "setting"
	}
	return nil, "org_admins"
}

// EffectiveApproverSettings resolves the project settings against the
// organization settings. A project value that is nil inherits.
func EffectiveApproverSettings(org *models.OrgRequiredSetSettings, project *models.ProjectRequiredSetSettings) ([]string, []models.RequiredSetApproverRule) {
	var approvers []string
	var rules []models.RequiredSetApproverRule
	if org != nil {
		approvers = append(approvers, org.Approvers...)
		rules = append(rules, org.ApproverRules...)
	}
	if project != nil && project.Approvers != nil {
		approvers = append([]string(nil), project.Approvers...)
	}
	if project != nil && project.ApproverRules != nil {
		rules = append([]models.RequiredSetApproverRule(nil), (*project.ApproverRules)...)
	}
	return approvers, rules
}

// Row is one line of a required set listing: a workflow and, when it is in
// the set, one entry.
type Row struct {
	WorkflowSecurityID string
	Name               string
	BranchPattern      string
	// State is required, advisory, or quarantined at the listing time, for
	// the branch pattern of the entry ("" for every branch).
	State string
	Entry *models.RequiredWorkflow
	// LastSeen is the last time the project ran or decided the workflow.
	LastSeen *time.Time
}

// Rows joins the known workflows, the entries, and the pending proposals of
// a project. A workflow with no entry is advisory. A pending proposal for a
// workflow that the project never ran still gets a row.
func Rows(enabled bool, known []models.KnownWorkflow, entries []models.RequiredWorkflow, pending []models.RequiredSetProposal, now time.Time) []Row {
	names := map[string]string{}
	seen := map[string]time.Time{}
	for _, item := range known {
		names[item.WorkflowSecurityID] = item.Name
		seen[item.WorkflowSecurityID] = item.LastSeen
	}
	hasEntry := map[string]bool{}
	rows := []Row{}
	for i := range entries {
		entry := entries[i]
		hasEntry[entry.WorkflowSecurityID] = true
		state := StateRequired
		if entry.IsQuarantined(now) {
			state = StateQuarantined
		}
		row := Row{WorkflowSecurityID: entry.WorkflowSecurityID, Name: names[entry.WorkflowSecurityID],
			BranchPattern: entry.BranchPattern, State: state, Entry: &entry}
		if value, ok := seen[entry.WorkflowSecurityID]; ok {
			row.LastSeen = &value
		}
		rows = append(rows, row)
	}
	add := func(id string) {
		if hasEntry[id] {
			return
		}
		hasEntry[id] = true
		row := Row{WorkflowSecurityID: id, Name: names[id], State: StateAdvisory}
		if !enabled {
			row.State = StateRequired
		}
		if value, ok := seen[id]; ok {
			row.LastSeen = &value
		}
		rows = append(rows, row)
	}
	for _, item := range known {
		add(item.WorkflowSecurityID)
	}
	for _, proposal := range pending {
		add(proposal.WorkflowSecurityID)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].WorkflowSecurityID != rows[j].WorkflowSecurityID {
			return rows[i].WorkflowSecurityID < rows[j].WorkflowSecurityID
		}
		return rows[i].BranchPattern < rows[j].BranchPattern
	})
	return rows
}
