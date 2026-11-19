// Package refclass classifies the ref of an event with labels ("ref
// classes"), for example default, tag, pr, fork, schedule, manual, and the
// classes that an operator adds with rules (production, release, test).
//
// Classes are coordinator metadata (owner decision D3). The coordinator
// computes them from trusted facts only: the signed VCS event, the VCS ref,
// the repository settings, and the coordinator's own records. Workflow code
// cannot set or change a class, because a branch can change its own workflow
// code. Any policy can read the classes. Nothing restricts by class until an
// operator configures a consumer (owner decision D17).
//
// The rules are additive. The built-in rules always apply. Each enabled
// operator rule that matches adds its classes. No rule removes a class, so a
// project rule cannot take away a class that an organization rule gives.
//
// Later packages read classes through ForJob, ForWorkflow, and ForEvent, and
// test conditions with Match.
package refclass

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// Built-in classes.
const (
	ClassDefault  = "default"
	ClassTag      = "tag"
	ClassPR       = "pr"
	ClassFork     = "fork"
	ClassSchedule = "schedule"
	ClassManual   = "manual"
)

// Event sources, the same values as models.CIEventSource*.
const (
	SourceWebhook  = models.CIEventSourceWebhook
	SourceSchedule = models.CIEventSourceSchedule
	SourceManual   = models.CIEventSourceManual
)

// EnvName is the reserved job environment variable that carries the classes
// as a comma-separated list.
const EnvName = "REACTORCIDE_REF_CLASSES"

// Facts are the trusted facts of one event. The coordinator fills them from
// the signed webhook, the schedule, or the authenticated submit. They never
// come from job output.
type Facts = models.RefClassFacts

// Result is the output of Classify.
type Result struct {
	// Classes are sorted and unique.
	Classes []string `json:"classes"`
	// Reasons has one entry for each (class, rule) pair, in rule order.
	Reasons []models.RefClassReason `json:"reasons"`
	// RuleIDs are the operator rules that matched, in rule order.
	RuleIDs []string `json:"rule_ids"`
}

var classPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// ValidClass reports whether name is a valid class label: lowercase letters,
// digits, ".", "_", and "-", 1 to 63 characters, first a letter or a digit.
func ValidClass(name string) bool { return classPattern.MatchString(name) }

// ValidateClasses checks a list of class labels.
func ValidateClasses(classes []string) error {
	for _, class := range classes {
		if !ValidClass(class) {
			return fmt.Errorf("invalid ref class %q: use lowercase letters, digits, '.', '_', or '-' (at most 63 characters)", class)
		}
	}
	return nil
}

// Normalize returns sorted, unique, non-empty, lowercase classes. It never
// returns nil, so a stored value is '{}' and not NULL.
func Normalize(classes []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, class := range classes {
		class = strings.ToLower(strings.TrimSpace(class))
		if class == "" || seen[class] {
			continue
		}
		seen[class] = true
		result = append(result, class)
	}
	sort.Strings(result)
	return result
}

// CompleteFacts derives IsPR and IsDefaultBranch from the other facts. It
// does not change a fact that the caller already set to true.
//
// A manual event always has the event type "manual": the submitter states
// its event type, and a signed webhook does not confirm it. Thus a rule that
// names event types never matches a manual event unless it names "manual".
// An API submit is never a pull request (its facts never set IsPR). A manual
// run of a pull request (POST /api/v1/projects/{id}/runs) keeps the IsPR and
// IsFork facts that the coordinator read from the VCS, so it keeps the "pr"
// and "fork" classes (fail closed).
func CompleteFacts(facts Facts) Facts {
	if facts.Source == SourceManual {
		facts.EventType = "manual"
	}
	if facts.Source != SourceManual && strings.HasPrefix(facts.EventType, "pull_request") {
		facts.IsPR = true
	}
	if !facts.IsPR {
		facts.IsFork = false
	} else if facts.Branch != "" {
		if facts.HeadBranch == "" {
			facts.HeadBranch = facts.Branch
		}
		facts.Branch = ""
	}
	if facts.DefaultBranch != "" && !facts.IsDefaultBranch {
		switch {
		case facts.IsPR:
			facts.IsDefaultBranch = facts.BaseBranch == facts.DefaultBranch
		case facts.Tag == "" && facts.Branch != "":
			facts.IsDefaultBranch = facts.Branch == facts.DefaultBranch
		}
	}
	return facts
}

// builtinRule is one built-in default rule (owner decision D17).
type builtinRule struct {
	name    string
	classes []string
	match   func(Facts) bool
}

var builtinRules = []builtinRule{
	{name: "default-branch", classes: []string{ClassDefault}, match: func(f Facts) bool {
		return f.EventType == "push" && f.Tag == "" && f.IsDefaultBranch
	}},
	// A manual event never gets "tag", as it never gets "default": the
	// principal chooses the ref (docs/ref-classes.md).
	{name: "tag", classes: []string{ClassTag}, match: func(f Facts) bool {
		return f.Source != SourceManual && (f.EventType == "tag_created" || f.Tag != "")
	}},
	{name: "pull-request", classes: []string{ClassPR}, match: func(f Facts) bool { return f.IsPR }},
	{name: "fork", classes: []string{ClassFork}, match: func(f Facts) bool { return f.IsPR && f.IsFork }},
	{name: "schedule", classes: []string{ClassSchedule}, match: func(f Facts) bool {
		return f.Source == SourceSchedule || f.EventType == "schedule"
	}},
	{name: "manual", classes: []string{ClassManual}, match: func(f Facts) bool { return f.Source == SourceManual }},
}

// BuiltinRuleNames lists the built-in rules with the classes they add, for
// documentation and the explain output.
func BuiltinRuleNames() map[string][]string {
	result := map[string][]string{}
	for _, rule := range builtinRules {
		result["builtin:"+rule.name] = append([]string(nil), rule.classes...)
	}
	return result
}

// Classify returns the classes for facts. The built-in rules always apply.
// Then each enabled rule in rules that matches adds its classes. Give the
// rules in evaluation order (organization rules, then project rules, each by
// position); the order changes only the order of the reasons.
func Classify(facts Facts, rules []models.RefClassRule) Result {
	facts = CompleteFacts(facts)
	result := Result{Classes: []string{}, Reasons: []models.RefClassReason{}, RuleIDs: []string{}}
	all := []string{}
	for _, rule := range builtinRules {
		if !rule.match(facts) {
			continue
		}
		for _, class := range rule.classes {
			all = append(all, class)
			result.Reasons = append(result.Reasons, models.RefClassReason{Class: class, RuleID: "builtin:" + rule.name, RuleName: rule.name, Scope: "builtin"})
		}
	}
	for _, rule := range rules {
		if !rule.Enabled || !RuleMatches(rule, facts) {
			continue
		}
		scope := "org"
		if rule.ProjectID != nil && *rule.ProjectID != "" {
			scope = "project"
		}
		result.RuleIDs = append(result.RuleIDs, rule.RuleID)
		for _, class := range Normalize(rule.Classes) {
			all = append(all, class)
			result.Reasons = append(result.Reasons, models.RefClassReason{Class: class, RuleID: rule.RuleID, RuleName: rule.Name, Scope: scope})
		}
	}
	result.Classes = Normalize(all)
	return result
}

// RuleMatches reports whether every configured match fact of rule is true for
// facts. A rule with no match facts matches every event.
func RuleMatches(rule models.RefClassRule, facts Facts) bool {
	facts = CompleteFacts(facts)
	if len(rule.EventTypes) > 0 && !containsString(rule.EventTypes, facts.EventType) {
		return false
	}
	// A manual event states its own ref: an API submit names any branch,
	// and a manual run lets a principal choose the ref. A ref condition
	// (branch_glob, tag_glob, base_branch_glob, is_default_branch) applies to a manual
	// event only when the rule names the event type "manual" explicitly.
	// Otherwise a rule "branch main -> production" would give "production"
	// to anybody who can submit.
	if facts.Source == SourceManual && !containsString(rule.EventTypes, "manual") &&
		(rule.BranchGlob != "" || rule.TagGlob != "" || rule.BaseBranchGlob != "" || rule.IsDefaultBranch != nil) {
		return false
	}
	// branch_glob never matches a pull request: the author of a pull request
	// names its head branch, so a fork branch named "main" must not match.
	if rule.BranchGlob != "" && (facts.IsPR || facts.Branch == "" || facts.Tag != "" || !GlobMatch(rule.BranchGlob, facts.Branch)) {
		return false
	}
	if rule.TagGlob != "" && (facts.Tag == "" || !GlobMatch(rule.TagGlob, facts.Tag)) {
		return false
	}
	if rule.BaseBranchGlob != "" && (facts.BaseBranch == "" || !GlobMatch(rule.BaseBranchGlob, facts.BaseBranch)) {
		return false
	}
	if rule.IsPR != nil && *rule.IsPR != facts.IsPR {
		return false
	}
	if rule.IsFork != nil && *rule.IsFork != (facts.IsPR && facts.IsFork) {
		return false
	}
	if rule.IsDefaultBranch != nil && *rule.IsDefaultBranch != facts.IsDefaultBranch {
		return false
	}
	return true
}

// ValidateRule checks an operator rule before the coordinator stores it.
func ValidateRule(rule models.RefClassRule) error {
	if !rulePattern.MatchString(rule.Name) {
		return fmt.Errorf("rule name must match [a-z0-9][a-z0-9-]{0,62}")
	}
	if len(rule.Classes) == 0 {
		return fmt.Errorf("rule %s must add at least one class", rule.Name)
	}
	if err := ValidateClasses(rule.Classes); err != nil {
		return fmt.Errorf("rule %s: %w", rule.Name, err)
	}
	for _, eventType := range rule.EventTypes {
		if !knownEventTypes[eventType] {
			return fmt.Errorf("rule %s: unknown event type %q", rule.Name, eventType)
		}
	}
	for field, pattern := range map[string]string{"branch": rule.BranchGlob, "tag": rule.TagGlob, "base_branch": rule.BaseBranchGlob} {
		if strings.ContainsAny(pattern, "\x00\n") || len(pattern) > 255 {
			return fmt.Errorf("rule %s: invalid %s pattern", rule.Name, field)
		}
	}
	return nil
}

var rulePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// knownEventTypes are the event types a rule can match. "manual" is the
// event type of an API submit; the classifier uses it for every manual
// event, whatever event type the submitter states.
var knownEventTypes = map[string]bool{
	"push": true, "tag_created": true, "pull_request_opened": true, "pull_request_updated": true,
	"pull_request_merged": true, "pull_request_closed": true, "schedule": true, "manual": true,
}

// KnownEventTypes returns the event types that a rule can match, sorted.
func KnownEventTypes() []string {
	result := make([]string, 0, len(knownEventTypes))
	for eventType := range knownEventTypes {
		result = append(result, eventType)
	}
	sort.Strings(result)
	return result
}

// Match is a class condition of a consumer: a secret grant, a CI policy
// rule, or a worker class. Any: the classes must have one of the listed
// classes. None: the classes must have none of the listed classes. An empty
// list is no condition.
type Match struct {
	Any  []string `yaml:"any,omitempty" json:"any,omitempty"`
	None []string `yaml:"none,omitempty" json:"none,omitempty"`
}

// IsEmpty reports whether the condition has no lists.
func (m Match) IsEmpty() bool { return len(m.Any) == 0 && len(m.None) == 0 }

// Validate checks the class labels of the condition.
func (m Match) Validate() error {
	if err := ValidateClasses(m.Any); err != nil {
		return err
	}
	return ValidateClasses(m.None)
}

// Satisfied reports whether classes satisfy the condition.
func (m Match) Satisfied(classes []string) bool { return Satisfies(classes, m.Any, m.None) }

// Satisfies reports whether classes have one of anyOf (when anyOf is not
// empty) and none of noneOf.
func Satisfies(classes, anyOf, noneOf []string) bool {
	have := map[string]bool{}
	for _, class := range classes {
		have[strings.ToLower(strings.TrimSpace(class))] = true
	}
	for _, class := range noneOf {
		if have[strings.ToLower(strings.TrimSpace(class))] {
			return false
		}
	}
	if len(anyOf) == 0 {
		return true
	}
	for _, class := range anyOf {
		if have[strings.ToLower(strings.TrimSpace(class))] {
			return true
		}
	}
	return false
}

// ForJob returns the classes of a job. Later consumers (OIDC claims, cache
// namespaces, outbound events, lanes, approver rules) read classes here.
func ForJob(job *models.Job) []string {
	if job == nil {
		return []string{}
	}
	return Normalize(job.RefClasses)
}

// ForWorkflow returns the classes of a workflow.
func ForWorkflow(workflow *models.WorkflowInstance) []string {
	if workflow == nil {
		return []string{}
	}
	return Normalize(workflow.RefClasses)
}

// ForEvent returns the classes of an event.
func ForEvent(event *models.CIEvent) []string {
	if event == nil {
		return []string{}
	}
	return Normalize(event.RefClasses)
}

// Has reports whether classes contain class.
func Has(classes []string, class string) bool {
	return containsString(classes, strings.ToLower(strings.TrimSpace(class)))
}

// EnvValue is the value of REACTORCIDE_REF_CLASSES for classes.
func EnvValue(classes []string) string { return strings.Join(Normalize(classes), ",") }

// Reason is the decision reason {kind: "ref_class", classes, rule_ids} that
// the coordinator adds to the decision records of an event.
func Reason(classes, ruleIDs []string) models.DecisionReason {
	normalized := Normalize(classes)
	detail := "ref classes: none"
	if len(normalized) > 0 {
		detail = "ref classes: " + strings.Join(normalized, ", ")
	}
	return models.DecisionReason{Kind: "ref_class", Classes: normalized, RuleIDs: append([]string{}, ruleIDs...), Detail: detail}
}

// SortRules puts rules in evaluation order: organization rules first, then
// project rules, each by position and then by name.
func SortRules(rules []models.RefClassRule) {
	sort.SliceStable(rules, func(i, j int) bool {
		pi, pj := rules[i].ProjectID != nil, rules[j].ProjectID != nil
		if pi != pj {
			return !pi
		}
		if rules[i].Position != rules[j].Position {
			return rules[i].Position < rules[j].Position
		}
		return rules[i].Name < rules[j].Name
	})
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// GlobMatch matches a ref name against a pattern: "*" and "?" stay inside
// one "/" segment, and "**" crosses segments. Examples: "release/*" matches
// "release/1.2"; "v*" matches "v1.2.3"; "**" matches every name.
func GlobMatch(pattern, value string) bool {
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
