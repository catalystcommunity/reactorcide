package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func TestBuildJobEnvSetsRefClasses(t *testing.T) {
	job := &models.Job{JobID: "j", RefClasses: []string{"pr", "fork"},
		JobEnvVars: models.JSONB{"REACTORCIDE_REF_CLASSES": "production"}}
	env := BuildJobEnv(job)
	if got := env["REACTORCIDE_REF_CLASSES"]; got != "fork,pr" {
		t.Fatalf("REACTORCIDE_REF_CLASSES = %q, want the coordinator value fork,pr", got)
	}
	if got := BuildJobEnv(&models.Job{JobID: "k"})["REACTORCIDE_REF_CLASSES"]; got != "" {
		t.Fatalf("a job with no classes must get an empty value, got %q", got)
	}
}

func TestRefClassesEnvIsReserved(t *testing.T) {
	if !isReservedEventEnv("reactorcide_ref_classes") {
		t.Fatal("REACTORCIDE_REF_CLASSES must be a reserved event fact")
	}
	if triggerMaySupplyEventEnv("REACTORCIDE_REF_CLASSES", false) {
		t.Fatal("a trigger must never supply REACTORCIDE_REF_CLASSES")
	}
}

// A trigger cannot give its child job other classes: the child inherits the
// parent's classes in the database, and the env value is the record.
func TestTriggerCannotSetRefClasses(t *testing.T) {
	parent := &models.Job{JobID: "parent", UserID: "user", QueueName: "q", RefClasses: []string{"pr"}}
	env := triggerChildEnv(t, parent, map[string]string{"REACTORCIDE_REF_CLASSES": "production"})
	if _, ok := env["REACTORCIDE_REF_CLASSES"]; ok {
		t.Fatalf("trigger output set REACTORCIDE_REF_CLASSES: %v", env)
	}
}

type refClassWorkerClassStore struct {
	*MockStore
	class *models.WorkerClass
	err   error
}

func (s refClassWorkerClassStore) GetWorkerClass(_ context.Context, _, _ string) (*models.WorkerClass, error) {
	return s.class, s.err
}

func TestCheckWorkerClassRefClasses(t *testing.T) {
	protected := &models.WorkerClass{Name: "deploy", RefClassesAny: []string{"production"}}
	cases := []struct {
		name    string
		store   refClassWorkerClassStore
		classes []string
		wantErr bool
	}{
		{"no condition", refClassWorkerClassStore{MockStore: &MockStore{}, class: &models.WorkerClass{Name: "default"}}, nil, false},
		{"condition satisfied", refClassWorkerClassStore{MockStore: &MockStore{}, class: protected}, []string{"default", "production"}, false},
		{"condition refused", refClassWorkerClassStore{MockStore: &MockStore{}, class: protected}, []string{"pr", "fork"}, true},
		{"unknown class", refClassWorkerClassStore{MockStore: &MockStore{}, err: store.ErrNotFound}, nil, false},
		{"read error fails closed", refClassWorkerClassStore{MockStore: &MockStore{}, err: errors.New("db down")}, []string{"production"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tp := NewTriggerProcessor(tc.store, corndogs.NewMockClient())
			err := tp.checkWorkerClassRefClasses(context.Background(), "org", "deploy", tc.classes)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestAuthorizeSecretAccessRefClassConditions(t *testing.T) {
	projectID := "project-1"
	grant := models.SecretGrant{GrantID: "g", Name: "deploy", UserID: "org", ProjectID: &projectID,
		SecretPathMatch: models.SecretGrantMatchPrefix, SecretPathPattern: "deploy/production",
		JobNameMatch: models.SecretGrantMatchAny}
	cases := []struct {
		name          string
		any, none     []string
		classes       []string
		wantAuthorize bool
	}{
		{"no conditions", nil, nil, []string{"pr", "fork"}, true},
		{"any satisfied", []string{"production"}, nil, []string{"default", "production"}, true},
		{"any not satisfied", []string{"production"}, nil, []string{"pr"}, false},
		{"any with no classes", []string{"default"}, nil, nil, false},
		{"none refuses fork", nil, []string{"fork"}, []string{"fork", "pr"}, false},
		{"none allows same repo", nil, []string{"fork"}, []string{"pr"}, true},
		{"any and none", []string{"default", "tag"}, []string{"fork"}, []string{"tag"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := grant
			g.RefClassesAny, g.RefClassesNone = tc.any, tc.none
			job := &models.Job{JobID: "job-1", OrgID: "org", ProjectID: &projectID, Name: "deploy", RefClasses: tc.classes}
			err := AuthorizeSecretAccess(context.Background(), &secretGrantMockStore{grants: []models.SecretGrant{g}}, job, "deploy/production", "token")
			if (err == nil) != tc.wantAuthorize {
				t.Fatalf("err = %v, want authorized %v", err, tc.wantAuthorize)
			}
		})
	}
}

type refClassEventStore struct {
	*eventRecordingStore
}

func (s refClassEventStore) GetCIEvent(_ context.Context, eventID string) (*models.CIEvent, error) {
	return &models.CIEvent{EventID: eventID, RefClasses: []string{"pr", "release"}, RefClassReasons: []models.RefClassReason{
		{Class: "pr", RuleID: "builtin:pull-request", Scope: "builtin"},
		{Class: "release", RuleID: "rule-1", Scope: "org"},
		{Class: "release", RuleID: "rule-1", Scope: "project"},
	}}, nil
}

// The decision reason takes the classes and the operator rule IDs from the
// event, not from the eval job.
func TestRefClassDecisionReasonUsesTheEvent(t *testing.T) {
	st := refClassEventStore{newEventRecordingStore()}
	tp := NewTriggerProcessor(st, corndogs.NewMockClient())
	reason := tp.refClassDecisionReason(context.Background(), st, "event-1", &models.Job{RefClasses: []string{"forged"}})
	if reason.Kind != "ref_class" || len(reason.Classes) != 2 || reason.Classes[1] != "release" {
		t.Fatalf("reason = %+v", reason)
	}
	if len(reason.RuleIDs) != 1 || reason.RuleIDs[0] != "rule-1" {
		t.Fatalf("rule ids = %v", reason.RuleIDs)
	}
}

type protectedClassStore struct {
	*MockStore
	class    *models.WorkerClass
	profiles map[string]*models.ExecutionProfile
}

func (s protectedClassStore) GetWorkerClass(_ context.Context, _, _ string) (*models.WorkerClass, error) {
	return s.class, nil
}

func (s protectedClassStore) GetExecutionProfile(_ context.Context, _, name string) (*models.ExecutionProfile, error) {
	if profile, ok := s.profiles[name]; ok {
		return profile, nil
	}
	return nil, store.ErrNotFound
}

// A protected worker class refuses an untrusted (deny_secrets) profile unless
// the profile names the class. A profile that cannot be read fails closed.
func TestCheckWorkerClassAdmissionProtected(t *testing.T) {
	st := protectedClassStore{MockStore: &MockStore{}, class: &models.WorkerClass{Name: "deploy", Protected: true},
		profiles: map[string]*models.ExecutionProfile{
			"standard":     {Name: "standard"},
			"pr-untrusted": {Name: "pr-untrusted", DenySecrets: true, AllowedWorkerClasses: []string{"default"}},
			"named":        {Name: "named", DenySecrets: true, AllowedWorkerClasses: []string{"deploy"}},
		}}
	ctx := context.Background()
	if err := CheckWorkerClassAdmission(ctx, st, "org", "deploy", nil, "", nil); err != nil {
		t.Fatalf("standard (the default profile) must use a protected class: %v", err)
	}
	if err := CheckWorkerClassAdmission(ctx, st, "org", "deploy", nil, "pr-untrusted", nil); err == nil {
		t.Fatal("an untrusted profile must not use a protected class")
	}
	if err := CheckWorkerClassAdmission(ctx, st, "org", "deploy", nil, "named", nil); err != nil {
		t.Fatalf("a profile that names the class can use it: %v", err)
	}
	if err := CheckWorkerClassAdmission(ctx, st, "org", "deploy", nil, "missing", nil); err == nil {
		t.Fatal("an unknown profile must fail closed")
	}
	if err := CheckWorkerClassAdmission(ctx, st, "org", "deploy", nil, "", st.profiles["pr-untrusted"]); err == nil {
		t.Fatal("a given untrusted profile must be refused")
	}
	unprotected := protectedClassStore{MockStore: &MockStore{}, class: &models.WorkerClass{Name: "default"}}
	if err := CheckWorkerClassAdmission(ctx, unprotected, "org", "default", nil, "missing", nil); err != nil {
		t.Fatalf("an unprotected class needs no profile: %v", err)
	}
}
