package federation

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

var claimName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.:-]{0,127}$`)

// MaxConditions is the most conditions one mapping can have.
const MaxConditions = 16

// ValidateConditions checks the shape of mapping conditions. A mapping must
// have at least one condition, and at least one condition must be on "sub",
// so that a mapping never accepts every subject of an issuer by accident.
//
// A glob "sub" condition must also fix the owner of the subject. One of
// these must be true:
//
//   - The literal text before the first wildcard ends with "/" or ":" and
//     has at least two parts that are not empty, for example "repo:acme/"
//     or "project_path:group/app:". "repo:*", "repo:acme*" (also matches
//     "repo:acme-evil/x"), and "*" are refused.
//   - The mapping has an equals condition on a claim that is not "sub",
//     for example repository_owner_id=12345.
func ValidateConditions(conditions []models.FederationCondition) error {
	if len(conditions) == 0 {
		return fmt.Errorf("a mapping needs at least one condition")
	}
	if len(conditions) > MaxConditions {
		return fmt.Errorf("a mapping can have at most %d conditions", MaxConditions)
	}
	hasSubject := false
	for _, condition := range conditions {
		if !claimName.MatchString(condition.Claim) {
			return fmt.Errorf("condition claim %q is not a valid claim name", condition.Claim)
		}
		switch condition.Op {
		case models.FederationOpEquals, models.FederationOpGlob:
		default:
			return fmt.Errorf("condition op must be equals or glob")
		}
		if condition.Value == "" || len(condition.Value) > 1024 {
			return fmt.Errorf("condition value must have 1 to 1024 characters")
		}
		if condition.Claim == "sub" {
			if condition.Op == models.FederationOpGlob && strings.Trim(condition.Value, "*?") == "" {
				return fmt.Errorf("a sub condition must not match every subject")
			}
			hasSubject = true
		}
	}
	if !hasSubject {
		return fmt.Errorf("a mapping needs a condition on the sub claim")
	}
	hasExactOther := false
	for _, condition := range conditions {
		if condition.Claim != "sub" && condition.Op == models.FederationOpEquals {
			hasExactOther = true
		}
	}
	for _, condition := range conditions {
		if condition.Claim == "sub" && condition.Op == models.FederationOpGlob && !hasExactOther && !subGlobFixesOwner(condition.Value) {
			return fmt.Errorf("the sub pattern %q must fix the owner: put a literal prefix such as \"repo:ORG/\" before the first wildcard, or add an equals condition on another claim (for example repository_owner_id)", condition.Value)
		}
	}
	return nil
}

// subGlobFixesOwner reports whether the literal prefix of a sub glob (the
// text before the first "*" or "?") ends with "/" or ":" and has at least
// two parts that are not empty.
func subGlobFixesOwner(pattern string) bool {
	end := strings.IndexAny(pattern, "*?")
	if end < 0 {
		return true
	}
	prefix := pattern[:end]
	if prefix == "" || !strings.ContainsAny(prefix[len(prefix)-1:], "/:") {
		return false
	}
	parts := strings.FieldsFunc(prefix, func(r rune) bool { return r == '/' || r == ':' })
	if len(parts) < 2 {
		return false
	}
	// FieldsFunc drops empty parts; "repo::" must not count as two parts.
	return !strings.Contains(prefix, "::") && !strings.Contains(prefix, "//") && !strings.HasPrefix(prefix, ":") && !strings.HasPrefix(prefix, "/")
}

// ParseCondition parses "CLAIM=VALUE" (equals) or "CLAIM~VALUE" (glob), the
// CLI form of a condition.
func ParseCondition(text string) (models.FederationCondition, error) {
	eq := strings.Index(text, "=")
	glob := strings.Index(text, "~")
	switch {
	case eq > 0 && (glob < 0 || eq < glob):
		return models.FederationCondition{Claim: text[:eq], Op: models.FederationOpEquals, Value: text[eq+1:]}, nil
	case glob > 0:
		return models.FederationCondition{Claim: text[:glob], Op: models.FederationOpGlob, Value: text[glob+1:]}, nil
	}
	return models.FederationCondition{}, fmt.Errorf("condition %q must be CLAIM=VALUE or CLAIM~GLOB", text)
}

// Matches reports whether every condition matches claims.
func Matches(conditions []models.FederationCondition, claims map[string]any) bool {
	if len(conditions) == 0 {
		return false
	}
	for _, condition := range conditions {
		value, ok := claimString(claims[condition.Claim])
		if !ok {
			return false
		}
		switch condition.Op {
		case models.FederationOpEquals:
			if value != condition.Value {
				return false
			}
		case models.FederationOpGlob:
			if !Glob(condition.Value, value) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// claimString returns the string form of a scalar claim. A list or an object
// never matches.
func claimString(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case bool:
		if typed {
			return "true", true
		}
		return "false", true
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64), true
	case json.Number:
		return typed.String(), true
	}
	return "", false
}

// Glob matches value against pattern. "*" matches any run of characters,
// including "/" and ":". "?" matches one character. Every other character
// matches only itself. There are no character classes and no escapes.
func Glob(pattern, value string) bool {
	p, v := []rune(pattern), []rune(value)
	pi, vi := 0, 0
	star, mark := -1, 0
	for vi < len(v) {
		switch {
		case pi < len(p) && (p[pi] == '?' || p[pi] == v[vi]):
			pi++
			vi++
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, vi
			pi++
		case star >= 0:
			pi = star + 1
			mark++
			vi = mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

// SelectMapping returns the one active mapping that matches claims. If
// principalID is set, only mappings to that principal count. No match and
// more than one match are both refusals.
func SelectMapping(mappings []models.FederationMapping, claims map[string]any, principalID string) (*models.FederationMapping, error) {
	var found *models.FederationMapping
	for i := range mappings {
		mapping := &mappings[i]
		if !mapping.IsActive() || (principalID != "" && mapping.PrincipalID != principalID) {
			continue
		}
		if !Matches(mapping.Conditions, claims) {
			continue
		}
		if found != nil {
			return nil, ErrAmbiguousMapping
		}
		found = mapping
	}
	if found == nil {
		return nil, ErrNotMapped
	}
	return found, nil
}
