package tokencaps

import "testing"

func TestValuesValidate(t *testing.T) {
	for _, capability := range Values() {
		if err := Validate(capability); err != nil {
			t.Fatalf("Validate(%q): %v", capability, err)
		}
	}
	if err := Validate(All); err != nil {
		t.Fatalf("Validate(All): %v", err)
	}
	if err := Validate("jobs:root"); err == nil {
		t.Fatal("Validate accepted an unknown capability")
	}
}

func TestSetOperations(t *testing.T) {
	parent, err := New(JobsRead, JobsSubmit, JobsCancel)
	if err != nil {
		t.Fatal(err)
	}
	child, err := New(JobsRead, JobsSubmit)
	if err != nil {
		t.Fatal(err)
	}
	if !child.IsSubsetOf(parent) {
		t.Fatal("expected child to be a subset")
	}
	if parent.IsSubsetOf(child) {
		t.Fatal("expected parent not to be a subset")
	}
	got := Intersect(parent, Set{JobsSubmit: {}, LogsRead: {}})
	if len(got) != 1 || !got.Has(JobsSubmit) {
		t.Fatalf("Intersect = %#v", got)
	}
}
