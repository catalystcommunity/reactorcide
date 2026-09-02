package uiapi

import (
	"fmt"
	"regexp"
	"testing"
)

// SQL column types, enforced by the in-memory fakes.
//
// The fakes evaluate visibility in Go while only PostgreSQL evaluates the SQL
// predicate, and that gap let a real bug through: an anonymous caller's empty
// viewer id was bound into a comparison against a `uuid` column, and PostgreSQL
// refuses to coerce '' to a uuid. It raises
//
//	invalid input syntax for type uuid: "" (SQLSTATE 22P02)
//
// rather than evaluating false. Every unit test here passed; only the
// testcontainers integration suite caught it.
//
// The lesson is not "write more integration tests" -- it is that a fake which
// accepts values the real database rejects is lying about the contract. So the
// fakes now enforce the ONE constraint that matters at this boundary: anything
// bound where the schema declares `uuid` must actually look like a uuid.
//
// The identifier columns these guard are all `uuid` in coredb/migrations:
// users.user_id, projects.project_id, projects.user_id, jobs.job_id,
// jobs.user_id, jobs.project_id, workflow_instances.workflow_id,
// role_assignments.principal_id, role_assignments.scope_id, groups.group_id,
// group_members.user_id, and org_id everywhere it appears.
//
// Test data does NOT have to be a real uuid — the fakes accept any non-empty
// identifier so tests can keep using readable ids like "owner-1", which matter
// more for a failure message than realism does. What is rejected is the empty
// string, because that is the value PostgreSQL rejects and the one a bug
// actually produces. See assertUUIDBindable.

// uuidBindable matches what PostgreSQL will accept into a uuid column, relaxed
// to also allow the readable placeholder ids this package's tests use.
//
// The real rule is stricter (8-4-4-4-12 hex). Enforcing that would mean
// rewriting every fixture to use generated uuids and would make every failure
// message unreadable, for no extra bug-catching: the failure mode in practice
// is an EMPTY or otherwise obviously wrong value arriving where an id was
// expected, not a well-formed id with the wrong digit count.
var uuidBindable = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]*$`)

// assertUUIDBindable panics when value could not be bound to a uuid column.
//
// A panic rather than a returned error, deliberately: this is a test-only
// invariant about the fake's fidelity, and a caller that violates it has a bug
// in the code under test, not a runtime condition to handle. The panic names
// the column so the failure points at the query rather than at the fake.
func assertUUIDBindable(column, value string) {
	if value == "" {
		panic(fmt.Sprintf(
			"fake store: empty string bound to uuid column %q.\n"+
				"PostgreSQL raises 'invalid input syntax for type uuid: \"\"' (SQLSTATE 22P02) "+
				"for this rather than evaluating false, so the real query would ERROR here.\n"+
				"If the intent was 'match nothing' (an anonymous caller, say), bind SQL NULL "+
				"instead -- see postgres_store.visibilityArgs.",
			column))
	}
	if !uuidBindable.MatchString(value) {
		panic(fmt.Sprintf(
			"fake store: value %q bound to uuid column %q could not be coerced to a uuid by PostgreSQL",
			value, column))
	}
}

// assertOptionalUUIDBindable is assertUUIDBindable for a binding that is
// allowed to be absent. An empty string is still refused; a nil pointer is the
// correct way to express "no value", exactly as it is on the wire.
func assertOptionalUUIDBindable(column string, value *string) {
	if value == nil {
		return
	}
	assertUUIDBindable(column, *value)
}

// TestFakeRefusesWhatPostgresRefuses is the guard on the guard.
//
// It asserts that the fake now rejects the exact binding that produced
// SQLSTATE 22P02 in production. Before this, the fake happily compared "" as a
// string and answered differently from the database, so every unit test passed
// while anonymous list-jobs was broken against real PostgreSQL.
func TestFakeRefusesWhatPostgresRefuses(t *testing.T) {
	cases := []struct {
		name      string
		value     string
		wantPanic bool
		why       string
	}{
		{"empty string", "", true,
			"PostgreSQL raises 22P02 for '' in a uuid column rather than evaluating false"},
		{"readable test id", "owner-1", false,
			"test fixtures use readable ids on purpose; they bind fine and read better in failures"},
		{"real uuid", "0192f1a0-1c2d-7e3f-8a9b-0c1d2e3f4a5b", false,
			"the real shape must obviously be accepted"},
		{"whitespace", " ", true,
			"whitespace is not a uuid either, and is the shape a trimmed-empty value takes"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			panicked := func() (did bool) {
				defer func() {
					if recover() != nil {
						did = true
					}
				}()
				assertUUIDBindable("test_column", tc.value)
				return false
			}()

			if panicked != tc.wantPanic {
				t.Errorf("assertUUIDBindable(%q): panicked = %v, want %v: %s",
					tc.value, panicked, tc.wantPanic, tc.why)
			}
		})
	}
}

// TestAnonymousViewerReachesTheFakeAsAnonymous pins the contract that keeps the
// uuid guard from firing on a legitimate anonymous caller: viewerScope maps an
// anonymous identity to an EMPTY viewer id, and the list methods treat that as
// "bind NULL" rather than as an id.
//
// If somebody later makes anonymous resolve to a sentinel string instead, this
// fails and points at the reason not to.
func TestAnonymousViewerReachesTheFakeAsAnonymous(t *testing.T) {
	if id := viewerIdentity(""); !id.Anonymous {
		t.Fatal("an empty viewer id must resolve to the anonymous identity, not to a user whose id is empty")
	}
	if id := viewerIdentity("owner-1"); id.Anonymous {
		t.Fatal("a real viewer id must not resolve to anonymous")
	}
}
