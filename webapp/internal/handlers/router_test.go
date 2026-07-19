package handlers

import "testing"

// TestNewRouter_RegistersWithoutPanicking is a smoke test for Task I's route
// additions: net/http's ServeMux panics at registration time if two
// patterns are ambiguous for the same request (e.g. a wildcard segment
// registered alongside a literal one it can't unambiguously resolve
// against). Building the full router here catches that even though every
// other test in this package invokes handlers directly and never exercises
// mux registration.
func TestNewRouter_RegistersWithoutPanicking(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("NewRouter panicked (likely an ambiguous route pattern): %v", r)
		}
	}()
	if NewRouter() == nil {
		t.Fatal("NewRouter returned nil")
	}
}
