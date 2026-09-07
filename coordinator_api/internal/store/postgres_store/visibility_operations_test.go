package postgres_store

import (
	"strings"
	"testing"
)

// The predicate's `?` count and visibilityArgs' binding count are a contract
// enforced only by convention (see visibilityPredicateSQL's doc comment). A
// mismatch is a runtime SQL error, not a compile error, and this package has
// no database in unit tests, so pin it here for every alias set in use.
func TestVisibilityPredicatePlaceholdersMatchArgs(t *testing.T) {
	want := len(visibilityArgs("viewer"))
	for _, a := range []visibilityAliases{jobVisibilityAliases, summaryWorkflowAliases, summaryLooseAliases} {
		if got := strings.Count(visibilityPredicateSQL(a), "?"); got != want {
			t.Errorf("alias set %+v: predicate has %d placeholders, visibilityArgs binds %d", a, got, want)
		}
	}
	if anon := visibilityArgs(""); len(anon) != want || anon[0] != nil {
		t.Errorf("anonymous visibilityArgs = %v, want %d NULL bindings", anon, want)
	}
}

// Org privacy and org-admin matching must key off the first-class org id
// (COALESCE(org_id, user_id)) and read organizations.is_private ahead of the
// legacy users.is_private, for both the project's owning org and the
// resource's own owning org.
func TestVisibilityPredicateUsesOrganizationOwnership(t *testing.T) {
	a := jobVisibilityAliases
	joins := strings.Join(visibilityJoins(a), "\n")
	for _, want := range []string{
		"LEFT JOIN organizations proj_org ON proj_org.org_id = COALESCE(p.org_id, p.user_id)",
		"LEFT JOIN users proj_owner ON proj_owner.user_id = COALESCE(p.org_id, p.user_id)",
		"LEFT JOIN organizations job_org ON job_org.org_id = COALESCE(j.org_id, j.user_id)",
		"LEFT JOIN users job_owner ON job_owner.user_id = COALESCE(j.org_id, j.user_id)",
	} {
		if !strings.Contains(joins, want) {
			t.Errorf("joins missing %q:\n%s", want, joins)
		}
	}
	pred := visibilityPredicateSQL(a)
	for _, want := range []string{
		"COALESCE(proj_org.is_private, proj_owner.is_private, false)",
		"COALESCE(job_org.is_private, job_owner.is_private, false)",
		"ra.scope_type = 'org' AND ra.scope_id = COALESCE(p.org_id, p.user_id) AND ra.role = 'admin'",
		"ra.scope_type = 'org' AND ra.scope_id = COALESCE(j.org_id, j.user_id) AND ra.role = 'admin'",
	} {
		if !strings.Contains(pred, want) {
			t.Errorf("predicate missing %q", want)
		}
	}
	if strings.Contains(pred, "p.user_id = ?") || strings.Contains(pred, "j.user_id = ?") {
		t.Errorf("predicate still matches the viewer against the bare legacy user_id column")
	}
}
