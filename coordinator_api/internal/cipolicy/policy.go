// Package cipolicy parses and evaluates trusted CI admission policy.
package cipolicy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/refclass"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/subjects"
	"gopkg.in/yaml.v3"
)

type Policy struct {
	Version  int      `yaml:"version" json:"version"`
	Defaults Defaults `yaml:"defaults" json:"defaults"`
	HeadCI   []Rule   `yaml:"head_ci" json:"head_ci"`
	Revision string   `yaml:"-" json:"-"`
}

type Defaults struct {
	CISource string `yaml:"ci_source" json:"ci_source"`
	Profile  string `yaml:"profile" json:"profile"`
	// ByRefClass selects the default profile by the ref classes of the event
	// (docs/ref-classes.md). The first entry whose condition the classes
	// satisfy gives the profile. When no entry matches, Profile applies.
	ByRefClass []ClassProfile `yaml:"by_ref_class,omitempty" json:"by_ref_class,omitempty"`
}

// ClassProfile is one defaults.by_ref_class entry.
type ClassProfile struct {
	RefClasses refclass.Match `yaml:"ref_classes" json:"ref_classes"`
	Profile    string         `yaml:"profile" json:"profile"`
}

// DefaultProfileFor returns the default profile for an event with classes,
// and the index of the defaults.by_ref_class entry that selected it (-1 for
// defaults.profile).
func (p *Policy) DefaultProfileFor(classes []string) (string, int) {
	if p == nil {
		return "standard", -1
	}
	for i, entry := range p.Defaults.ByRefClass {
		if entry.RefClasses.Satisfied(classes) {
			return entry.Profile, i
		}
	}
	return p.Defaults.Profile, -1
}

type SubjectMatch struct {
	Any []string `yaml:"any" json:"any"`
}
type Use struct {
	CISource  string          `yaml:"ci_source" json:"ci_source"`
	Profile   string          `yaml:"profile" json:"profile"`
	Workers   string          `yaml:"workers" json:"workers"`
	BaseNodes []NodeAuthority `yaml:"base_nodes,omitempty" json:"base_nodes,omitempty"`
}

// NodeAuthority grants selected workflow nodes trusted base-CI authority
// inside a head-CI workflow. Only coordinator policy can carry this grant.
type NodeAuthority struct {
	Nodes    []string `yaml:"nodes" json:"nodes"`
	CISource string   `yaml:"ci_source" json:"ci_source"`
	Profile  string   `yaml:"profile" json:"profile"`
	Workers  string   `yaml:"workers" json:"workers"`
}
type Rule struct {
	ID             string       `yaml:"id" json:"id"`
	Actors         SubjectMatch `yaml:"actors" json:"actors"`
	Workflows      []string     `yaml:"workflows" json:"workflows"`
	Paths          []string     `yaml:"paths" json:"paths"`
	Events         []string     `yaml:"events" json:"events"`
	BaseBranches   []string     `yaml:"base_branches" json:"base_branches"`
	HeadRepository string       `yaml:"head_repository" json:"head_repository"`
	Approval       SubjectMatch `yaml:"approval" json:"approval"`
	// RefClasses is an optional ref class condition (docs/ref-classes.md).
	// The rule applies only when the classes of the event satisfy it.
	RefClasses refclass.Match `yaml:"ref_classes,omitempty" json:"ref_classes,omitempty"`
	Use        Use            `yaml:"use" json:"use"`
}

type filePolicy struct {
	Version  int      `yaml:"version"`
	Defaults Defaults `yaml:"defaults"`
	HeadCI   []Rule   `yaml:"head_ci"`
}

var securityIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// nodeNamePattern accepts workflow node map keys. Node names come from
// trusted base workflow YAML, so the pattern permits the wider identifier
// grammar that workflow definitions already use.
var nodeNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// ParseDocument parses one coordinator-managed YAML or JSON policy document.
func ParseDocument(data []byte) (*Policy, error) {
	main, err := parseFile("CI policy", data)
	if err != nil {
		return nil, err
	}
	result := &Policy{Version: main.Version, Defaults: main.Defaults, HeadCI: append([]Rule(nil), main.HeadCI...)}
	if err := validatePolicy(result); err != nil {
		return nil, err
	}
	canonical, err := CanonicalDocument(result)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(canonical)
	result.Revision = hex.EncodeToString(hash[:])
	return result, nil
}

// CanonicalDocument returns the stable JSON representation that the
// coordinator stores and pins to an evaluation job.
func CanonicalDocument(policy *Policy) ([]byte, error) {
	canonicalRules := make([]map[string]any, 0, len(policy.HeadCI))
	for _, rule := range policy.HeadCI {
		use := map[string]any{"ci_source": rule.Use.CISource, "profile": rule.Use.Profile, "workers": rule.Use.Workers}
		// base_nodes joins the canonical form only when present so that the
		// revision of a policy without node authority stays unchanged.
		if len(rule.Use.BaseNodes) > 0 {
			canonicalNodes := make([]map[string]any, 0, len(rule.Use.BaseNodes))
			for _, authority := range rule.Use.BaseNodes {
				canonicalNodes = append(canonicalNodes, map[string]any{
					"ci_source": authority.CISource, "nodes": emptySlice(authority.Nodes),
					"profile": authority.Profile, "workers": authority.Workers,
				})
			}
			use["base_nodes"] = canonicalNodes
		}
		canonicalRule := map[string]any{
			"actors": map[string]any{"any": emptySlice(rule.Actors.Any)}, "approval": map[string]any{"any": emptySlice(rule.Approval.Any)},
			"base_branches": emptySlice(rule.BaseBranches), "events": emptySlice(rule.Events), "head_repository": rule.HeadRepository,
			"id": rule.ID, "paths": emptySlice(rule.Paths), "use": use,
			"workflows": emptySlice(rule.Workflows),
		}
		// ref_classes joins the canonical form only when present, so the
		// revision of a policy without class conditions stays unchanged.
		if !rule.RefClasses.IsEmpty() {
			canonicalRule["ref_classes"] = canonicalClassMatch(rule.RefClasses)
		}
		canonicalRules = append(canonicalRules, canonicalRule)
	}
	defaults := map[string]any{"ci_source": policy.Defaults.CISource, "profile": policy.Defaults.Profile}
	if len(policy.Defaults.ByRefClass) > 0 {
		entries := make([]map[string]any, 0, len(policy.Defaults.ByRefClass))
		for _, entry := range policy.Defaults.ByRefClass {
			entries = append(entries, map[string]any{"profile": entry.Profile, "ref_classes": canonicalClassMatch(entry.RefClasses)})
		}
		defaults["by_ref_class"] = entries
	}
	return json.Marshal(map[string]any{
		"defaults": defaults,
		"head_ci":  canonicalRules,
		"version":  policy.Version,
	})
}

func canonicalClassMatch(match refclass.Match) map[string]any {
	return map[string]any{"any": emptySlice(match.Any), "none": emptySlice(match.None)}
}

func emptySlice(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func parseFile(name string, data []byte) (*filePolicy, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var policy filePolicy
	if err := decoder.Decode(&policy); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("%s: multiple documents are not allowed", name)
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return &policy, nil
}

func validatePolicy(policy *Policy) error {
	if policy.Version != 1 {
		return fmt.Errorf("policy version must be 1")
	}
	if policy.Defaults.CISource == "" {
		policy.Defaults.CISource = "base"
	}
	if policy.Defaults.Profile == "" {
		policy.Defaults.Profile = "standard"
	}
	if policy.Defaults.CISource != "base" {
		return fmt.Errorf("default ci_source must be base")
	}
	for i := range policy.Defaults.ByRefClass {
		entry := &policy.Defaults.ByRefClass[i]
		if entry.RefClasses.IsEmpty() {
			return fmt.Errorf("defaults.by_ref_class[%d] must set ref_classes.any or ref_classes.none", i)
		}
		if err := entry.RefClasses.Validate(); err != nil {
			return fmt.Errorf("defaults.by_ref_class[%d]: %w", i, err)
		}
		entry.RefClasses = normalizeClassMatch(entry.RefClasses)
		if entry.Profile == "" {
			return fmt.Errorf("defaults.by_ref_class[%d] must select a profile", i)
		}
	}
	seenRules := map[string]bool{}
	seenWorkflows := map[string]string{}
	for i := range policy.HeadCI {
		rule := &policy.HeadCI[i]
		if !securityIDPattern.MatchString(rule.ID) {
			return fmt.Errorf("head_ci[%d].id must match [a-z0-9][a-z0-9-]{0,62}", i)
		}
		if seenRules[rule.ID] {
			return fmt.Errorf("duplicate policy rule ID %q", rule.ID)
		}
		seenRules[rule.ID] = true
		if err := validateSubjects(rule.Actors.Any, false); err != nil {
			return fmt.Errorf("rule %s actors: %w", rule.ID, err)
		}
		if err := validateApprovalSubjects(rule.Approval.Any); err != nil {
			return fmt.Errorf("rule %s approval: %w", rule.ID, err)
		}
		if err := rule.RefClasses.Validate(); err != nil {
			return fmt.Errorf("rule %s ref_classes: %w", rule.ID, err)
		}
		rule.RefClasses = normalizeClassMatch(rule.RefClasses)
		if len(rule.Workflows) == 0 || len(rule.Paths) == 0 {
			return fmt.Errorf("rule %s must select workflows and paths", rule.ID)
		}
		for _, workflow := range rule.Workflows {
			if !securityIDPattern.MatchString(workflow) {
				return fmt.Errorf("rule %s has invalid workflow ID %q", rule.ID, workflow)
			}
			if prior := seenWorkflows[workflow]; prior != "" {
				return fmt.Errorf("duplicate workflow security ID %q in rules %s and %s", workflow, prior, rule.ID)
			}
			seenWorkflows[workflow] = rule.ID
		}
		for _, pattern := range rule.Paths {
			if err := validateRepoPath(pattern); err != nil {
				return fmt.Errorf("rule %s path %q: %w", rule.ID, pattern, err)
			}
		}
		if rule.HeadRepository == "" {
			rule.HeadRepository = "same"
		}
		if rule.HeadRepository != "same" && rule.HeadRepository != "fork" && rule.HeadRepository != "any" {
			return fmt.Errorf("rule %s has invalid head_repository", rule.ID)
		}
		if rule.Use.CISource != "head" {
			return fmt.Errorf("rule %s use.ci_source must be head", rule.ID)
		}
		if rule.Use.Profile == "" || rule.Use.Workers == "" {
			return fmt.Errorf("rule %s must select a profile and worker class", rule.ID)
		}
		seenNodes := map[string]bool{}
		for j, authority := range rule.Use.BaseNodes {
			if len(authority.Nodes) == 0 {
				return fmt.Errorf("rule %s base_nodes[%d] must name at least one node", rule.ID, j)
			}
			for _, node := range authority.Nodes {
				if !nodeNamePattern.MatchString(node) {
					return fmt.Errorf("rule %s base_nodes[%d] has invalid node name %q", rule.ID, j, node)
				}
				if seenNodes[node] {
					return fmt.Errorf("rule %s names node %q in more than one base_nodes entry", rule.ID, node)
				}
				seenNodes[node] = true
			}
			if authority.CISource != "base" {
				return fmt.Errorf("rule %s base_nodes[%d] ci_source must be base", rule.ID, j)
			}
			if authority.Profile == "" || authority.Workers == "" {
				return fmt.Errorf("rule %s base_nodes[%d] must select a profile and worker class", rule.ID, j)
			}
		}
	}
	return nil
}

// normalizeClassMatch sorts the class lists and keeps nil for an empty list,
// so the canonical form does not depend on the input order.
func normalizeClassMatch(match refclass.Match) refclass.Match {
	result := refclass.Match{}
	if len(match.Any) > 0 {
		result.Any = refclass.Normalize(match.Any)
	}
	if len(match.None) > 0 {
		result.None = refclass.Normalize(match.None)
	}
	return result
}

// validateSubjects checks a subject list against the subject grammar of
// internal/subjects. project_owner is not an actor fact, so an actor list
// cannot use it.
func validateSubjects(values []string, allowProjectOwner bool) error {
	for _, subject := range values {
		if subject == subjects.ProjectOwner && !allowProjectOwner {
			return fmt.Errorf("unknown policy subject %q", subject)
		}
		if !subjects.Valid(subject) {
			return fmt.Errorf("unknown policy subject %q", subject)
		}
	}
	return nil
}

// validateApprovalSubjects checks a head_ci approval list. It also accepts
// the workflow_owner placeholder: the owners of the trusted definition of the
// workflow that the rule decides.
func validateApprovalSubjects(values []string) error {
	for _, subject := range values {
		if subject == subjects.WorkflowOwner {
			continue
		}
		if err := validateSubjects([]string{subject}, true); err != nil {
			return err
		}
	}
	return nil
}

// expandWorkflowOwner replaces workflow_owner with the owner subjects of the
// workflow. A workflow with no owner matches nothing for that entry.
func expandWorkflowOwner(list []string, ownerSubjects []string) []string {
	out := make([]string, 0, len(list)+len(ownerSubjects))
	for _, subject := range list {
		if subject == subjects.WorkflowOwner {
			out = append(out, ownerSubjects...)
			continue
		}
		out = append(out, subject)
	}
	return out
}

func validateRepoPath(value string) error {
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return fmt.Errorf("path must be repository-relative")
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." || part == "." || part == "" {
			return fmt.Errorf("path contains an unsafe segment")
		}
	}
	cleaned := path.Clean(value)
	if cleaned != value {
		return fmt.Errorf("path is not canonical")
	}
	return nil
}

type Facts struct {
	WorkflowID                string
	ChangedCIPaths            []string
	Event                     string
	BaseBranch                string
	HeadRepositoryRelation    string
	ActorSubjects             map[string]bool
	ApprovalSubjects          map[string]bool
	ApprovalSubjectsByProfile map[string]map[string]bool
	// RefClasses are the ref classes of the event. A rule with a ref_classes
	// condition applies only when they satisfy it.
	RefClasses []string
	// WorkflowOwnerSubjects are the owner subjects of the TRUSTED definition
	// of the workflow (owners.Subjects). They replace workflow_owner in an
	// approval list. Empty when the workflow has no known owner.
	WorkflowOwnerSubjects []string
}

type Decision struct {
	CISource, Profile, WorkerClass, RuleID string
	ApprovalSubjects                       []string
	// BaseNodes maps a node name to the trusted base authority that the
	// matched rule grants it. Empty for workflows without node authority.
	BaseNodes map[string]NodeDecision
	Allowed   bool
	Reasons   []string
}

// NodeDecision is the effective authority for one policy-controlled node.
type NodeDecision struct {
	CISource, Profile, WorkerClass string
}

// BaseNodeDecisions flattens a rule's base_nodes entries to one map keyed by
// node name. Validation guarantees that a node name appears only one time.
func BaseNodeDecisions(authorities []NodeAuthority) map[string]NodeDecision {
	if len(authorities) == 0 {
		return nil
	}
	result := make(map[string]NodeDecision)
	for _, authority := range authorities {
		for _, node := range authority.Nodes {
			result[node] = NodeDecision{CISource: authority.CISource, Profile: authority.Profile, WorkerClass: authority.Workers}
		}
	}
	return result
}

func equalNodeDecisions(a, b map[string]NodeDecision) bool {
	if len(a) != len(b) {
		return false
	}
	for name, decision := range a {
		if other, ok := b[name]; !ok || other != decision {
			return false
		}
	}
	return true
}

func Decide(policy *Policy, facts Facts) (Decision, error) {
	base := Decision{CISource: policy.Defaults.CISource, Profile: policy.Defaults.Profile, WorkerClass: "default", Allowed: false}
	var matches []Decision
	for _, rule := range policy.HeadCI {
		if !contains(rule.Workflows, facts.WorkflowID) {
			continue
		}
		if !allPathsMatch(facts.ChangedCIPaths, rule.Paths) || !optionalContains(rule.Events, facts.Event) || !optionalGlobContains(rule.BaseBranches, facts.BaseBranch) || (rule.HeadRepository != "any" && rule.HeadRepository != facts.HeadRepositoryRelation) {
			continue
		}
		if len(rule.Actors.Any) > 0 && !anyFact(rule.Actors.Any, facts.ActorSubjects) {
			continue
		}
		if !rule.RefClasses.Satisfied(facts.RefClasses) {
			continue
		}
		approvalFacts := facts.ApprovalSubjects
		if facts.ApprovalSubjectsByProfile != nil {
			approvalFacts = facts.ApprovalSubjectsByProfile[rule.Use.Profile]
		}
		approvalSubjects := expandWorkflowOwner(rule.Approval.Any, facts.WorkflowOwnerSubjects)
		if len(rule.Approval.Any) > 0 && !anyFact(approvalSubjects, approvalFacts) {
			continue
		}
		matches = append(matches, Decision{CISource: "head", Profile: rule.Use.Profile, WorkerClass: rule.Use.Workers, RuleID: rule.ID, ApprovalSubjects: approvalSubjects, BaseNodes: BaseNodeDecisions(rule.Use.BaseNodes), Allowed: true})
	}
	if len(matches) == 0 {
		base.Reasons = []string{"no complete coordinator policy rule authorized head CI"}
		return base, nil
	}
	selected := matches[0]
	for _, match := range matches[1:] {
		if match.Profile != selected.Profile || match.WorkerClass != selected.WorkerClass || !equalNodeDecisions(match.BaseNodes, selected.BaseNodes) {
			return base, fmt.Errorf("ambiguous head CI authority for workflow %q", facts.WorkflowID)
		}
	}
	return selected, nil
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
func optionalContains(values []string, value string) bool {
	return len(values) == 0 || contains(values, value)
}

// anyFact reports whether a fact set matches one of the subjects. The
// subject * matches a non-empty fact set (an identified actor, or at least
// one approval).
func anyFact(values []string, facts map[string]bool) bool {
	return subjects.HasAnyFact(facts, values)
}
func allPathsMatch(values, patterns []string) bool {
	for _, value := range values {
		matched := false
		for _, pattern := range patterns {
			if globMatch(pattern, value) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
func optionalGlobContains(patterns []string, value string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		if globMatch(pattern, value) {
			return true
		}
	}
	return false
}

// GlobMatch reports whether a repository path matches a policy path pattern:
// "*" and "?" stay inside one path segment, and "**" crosses segments.
func GlobMatch(pattern, value string) bool { return globMatch(pattern, value) }

func globMatch(pattern, value string) bool {
	var expression strings.Builder
	expression.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				expression.WriteString(".*")
				i++
			} else {
				expression.WriteString("[^/]*")
			}
		case '?':
			expression.WriteString("[^/]")
		default:
			expression.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	expression.WriteString("$")
	matched, _ := regexp.MatchString(expression.String(), value)
	return matched
}
