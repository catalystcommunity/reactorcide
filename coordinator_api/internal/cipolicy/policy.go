// Package cipolicy parses and evaluates trusted CI admission policy.
package cipolicy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type Policy struct {
	Version           int          `yaml:"version" json:"version"`
	Defaults          Defaults     `yaml:"defaults" json:"defaults"`
	PolicyMaintainers SubjectMatch `yaml:"policy_maintainers" json:"policy_maintainers"`
	HeadCI            []Rule       `yaml:"head_ci" json:"head_ci"`
	Revision          string       `yaml:"-" json:"revision"`
}

type Defaults struct {
	CISource string `yaml:"ci_source" json:"ci_source"`
	Profile  string `yaml:"profile" json:"profile"`
}
type SubjectMatch struct {
	Any []string `yaml:"any" json:"any"`
}
type Use struct {
	CISource string `yaml:"ci_source" json:"ci_source"`
	Profile  string `yaml:"profile" json:"profile"`
	Workers  string `yaml:"workers" json:"workers"`
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
	Use            Use          `yaml:"use" json:"use"`
}

type filePolicy struct {
	Version           int          `yaml:"version"`
	Defaults          Defaults     `yaml:"defaults"`
	PolicyMaintainers SubjectMatch `yaml:"policy_maintainers"`
	HeadCI            []Rule       `yaml:"head_ci"`
}

var securityIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Parse parses the main policy and optional fragments. The map key is the
// repository-relative path. The main path is `.reactorcide/policy.yaml`.
func Parse(files map[string][]byte) (*Policy, error) {
	mainData, ok := files[".reactorcide/policy.yaml"]
	if !ok {
		return nil, fmt.Errorf(".reactorcide/policy.yaml is required")
	}
	main, err := parseFile(".reactorcide/policy.yaml", mainData)
	if err != nil {
		return nil, err
	}
	result := &Policy{Version: main.Version, Defaults: main.Defaults, PolicyMaintainers: main.PolicyMaintainers, HeadCI: append([]Rule(nil), main.HeadCI...)}
	paths := make([]string, 0, len(files)-1)
	for filePath := range files {
		if filePath != ".reactorcide/policy.yaml" {
			paths = append(paths, filePath)
		}
	}
	sort.Strings(paths)
	for _, filePath := range paths {
		if !strings.HasPrefix(filePath, ".reactorcide/policies/") || !strings.HasSuffix(filePath, ".yaml") {
			return nil, fmt.Errorf("unsupported policy fragment path %q", filePath)
		}
		fragment, err := parseFile(filePath, files[filePath])
		if err != nil {
			return nil, err
		}
		if fragment.Version != 0 && fragment.Version != result.Version {
			return nil, fmt.Errorf("%s: version does not match main policy", filePath)
		}
		result.HeadCI = append(result.HeadCI, fragment.HeadCI...)
	}
	if err := validatePolicy(result); err != nil {
		return nil, err
	}
	canonicalRules := make([]map[string]any, 0, len(result.HeadCI))
	for _, rule := range result.HeadCI {
		canonicalRules = append(canonicalRules, map[string]any{
			"actors": map[string]any{"any": emptySlice(rule.Actors.Any)}, "approval": map[string]any{"any": emptySlice(rule.Approval.Any)},
			"base_branches": emptySlice(rule.BaseBranches), "events": emptySlice(rule.Events), "head_repository": rule.HeadRepository,
			"id": rule.ID, "paths": emptySlice(rule.Paths), "use": map[string]any{"ci_source": rule.Use.CISource, "profile": rule.Use.Profile, "workers": rule.Use.Workers},
			"workflows": emptySlice(rule.Workflows),
		})
	}
	canonical, err := json.Marshal(map[string]any{
		"defaults": map[string]any{"ci_source": result.Defaults.CISource, "profile": result.Defaults.Profile},
		"head_ci":  canonicalRules, "policy_maintainers": map[string]any{"any": emptySlice(result.PolicyMaintainers.Any)}, "version": result.Version,
	})
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(canonical)
	result.Revision = hex.EncodeToString(hash[:])
	return result, nil
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
	if err := validateSubjects(policy.PolicyMaintainers.Any, true); err != nil {
		return fmt.Errorf("policy_maintainers: %w", err)
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
		if err := validateSubjects(rule.Approval.Any, true); err != nil {
			return fmt.Errorf("rule %s approval: %w", rule.ID, err)
		}
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
	}
	return nil
}

func validateSubjects(subjects []string, allowProjectOwner bool) error {
	for _, subject := range subjects {
		if subject == "repository_write" {
			continue
		}
		if allowProjectOwner && subject == "project_owner" {
			continue
		}
		if strings.HasPrefix(subject, "vcs_team:") && len(strings.TrimPrefix(subject, "vcs_team:")) > 0 {
			continue
		}
		if strings.HasPrefix(subject, "vcs_user:") && len(strings.TrimPrefix(subject, "vcs_user:")) > 0 {
			continue
		}
		if strings.HasPrefix(subject, "reactorcide_group:") && len(strings.TrimPrefix(subject, "reactorcide_group:")) > 0 {
			continue
		}
		return fmt.Errorf("unknown policy subject %q", subject)
	}
	return nil
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
	WorkflowID             string
	ChangedCIPaths         []string
	Event                  string
	BaseBranch             string
	HeadRepositoryRelation string
	ActorSubjects          map[string]bool
	ApprovalSubjects       map[string]bool
}

type Decision struct {
	CISource, Profile, WorkerClass, RuleID string
	Allowed                                bool
	Reasons                                []string
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
		if len(rule.Approval.Any) > 0 && !anyFact(rule.Approval.Any, facts.ApprovalSubjects) {
			continue
		}
		matches = append(matches, Decision{CISource: "head", Profile: rule.Use.Profile, WorkerClass: rule.Use.Workers, RuleID: rule.ID, Allowed: true})
	}
	if len(matches) == 0 {
		base.Reasons = []string{"no complete trusted-base policy rule authorized head CI"}
		return base, nil
	}
	selected := matches[0]
	for _, match := range matches[1:] {
		if match.Profile != selected.Profile || match.WorkerClass != selected.WorkerClass {
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
func anyFact(subjects []string, facts map[string]bool) bool {
	for _, subject := range subjects {
		if facts[subject] {
			return true
		}
	}
	return false
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
