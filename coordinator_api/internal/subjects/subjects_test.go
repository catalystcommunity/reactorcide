package subjects

import "testing"

func TestValid(t *testing.T) {
	valid := []string{"*", "repository_write", "project_owner", "vcs_user:github/alice", "vcs_team:acme/ops",
		"reactorcide_group:release", "principal:0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b", "service:deployer",
		"service:acme/deployer", "principal_kind:human", "principal_kind:service", "identity:linkkeys:alice@example.com",
		"identity:federated:https://issuer.example.com|sub-1", "identity:vcsstar:alice"}
	for _, subject := range valid {
		if !Valid(subject) {
			t.Errorf("Valid(%q) = false", subject)
		}
	}
	invalid := []string{"", "anyone", "principal:not-a-uuid", "service:Bad Name", "service:acme/", "principal_kind:robot",
		"identity:user:abc", "identity:token:abc", "identity:linkkeys:", "vcs_user:", "non_author"}
	for _, subject := range invalid {
		if Valid(subject) {
			t.Errorf("Valid(%q) = true", subject)
		}
	}
	if !ValidReview(NonAuthor) {
		t.Error("non_author must be valid in a review")
	}
}

func TestHasAnyone(t *testing.T) {
	if !Has([]string{"vcs_user:github/x"}, Anyone) {
		t.Error("* must match an identified person")
	}
	if Has(nil, Anyone) {
		t.Error("* must not match a person with no subjects")
	}
	if HasAny([]string{"vcs_user:github/x"}, nil) {
		t.Error("an omitted subject list never means anyone")
	}
	if !HasAny([]string{"principal:p"}, []string{"vcs_user:github/y", "principal:p"}) {
		t.Error("an exact subject must match")
	}
	if !HasAnyFact(map[string]bool{"vcs_user:github/x": true}, []string{Anyone}) {
		t.Error("* must match a non-empty fact set")
	}
	if HasAnyFact(map[string]bool{}, []string{Anyone}) {
		t.Error("* must not match an empty fact set")
	}
}

func TestIsRole(t *testing.T) {
	for _, subject := range []string{RepositoryWrite, ProjectOwner, "vcs_team:a/b", "reactorcide_group:g"} {
		if !IsRole(subject) {
			t.Errorf("IsRole(%q) = false", subject)
		}
	}
	for _, subject := range []string{Anyone, "vcs_user:github/x", "principal:abc", "service:x", "principal_kind:human", "identity:linkkeys:a@b"} {
		if IsRole(subject) {
			t.Errorf("IsRole(%q) = true; an identity is not a role", subject)
		}
	}
}
