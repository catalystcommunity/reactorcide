package models

import "testing"

func validAction() CIApproval {
	return CIApproval{Kind: CIApprovalKindAccept, Action: CIApprovalActionApproved, OrgID: "org", ProjectID: "project",
		PRNumber: 1, HeadRepository: "acme/repo", HeadSHA: "head", Person: "github/todpunk",
		Subjects: []string{"vcs_user:github/todpunk", "vcs_team:acme/backend"}, Channel: CIApprovalChannelComment}
}

func TestCIApprovalValidate(t *testing.T) {
	if err := validAction().Validate(); err != nil {
		t.Fatal(err)
	}
	labelled := validAction()
	labelled.Label = "trust-anchors"
	if err := labelled.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*CIApproval){
		"unknown kind":        func(a *CIApproval) { a.Kind = "content" },
		"unknown action":      func(a *CIApproval) { a.Action = "maybe" },
		"unsafe label":        func(a *CIApproval) { a.Label = "../x" },
		"label on run":        func(a *CIApproval) { a.Kind = CIApprovalKindRun; a.Label = "x" },
		"no person":           func(a *CIApproval) { a.Person = "" },
		"unknown channel":     func(a *CIApproval) { a.Channel = "email" },
		"unknown subject":     func(a *CIApproval) { a.Subjects = []string{"unverified:alice"} },
		"empty vcs_user":      func(a *CIApproval) { a.Subjects = []string{"vcs_user:"} },
		"no head SHA":         func(a *CIApproval) { a.HeadSHA = "" },
		"non_author recorded": func(a *CIApproval) { a.Subjects = []string{SubjectNonAuthor} },
	} {
		invalid := validAction()
		mutate(&invalid)
		if err := invalid.Validate(); err == nil {
			t.Errorf("an action with %s was accepted", name)
		}
	}
}

func TestVCSPerson(t *testing.T) {
	if got := VCSPerson("GitHub", " TodPunk "); got != "github/todpunk" {
		t.Fatalf("VCSPerson = %q", got)
	}
	if VCSPerson("github", "") != "" {
		t.Fatal("an empty login has no person")
	}
}
