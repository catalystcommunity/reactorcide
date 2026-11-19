package worker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// pathAcceptanceStore adds reporting settings and acceptances to the event
// recording fake.
type pathAcceptanceStore struct {
	*eventRecordingStore
	mode        string
	rules       []models.PathOwnerRule
	acceptances []models.PathAcceptance
}

func (s *pathAcceptanceStore) GetOrgReportingSettings(context.Context, string) (*models.OrgReportingSettings, error) {
	org := models.DefaultOrgReportingSettings("org-1")
	org.PathAcceptance, org.PathOwners = s.mode, s.rules
	return org, nil
}
func (s *pathAcceptanceStore) GetProjectReportingSettings(context.Context, string) (*models.ProjectReportingSettings, error) {
	return nil, nil
}
func (s *pathAcceptanceStore) UpsertOrgReportingSettings(context.Context, *models.OrgReportingSettings) error {
	return nil
}
func (s *pathAcceptanceStore) UpsertProjectReportingSettings(context.Context, *models.ProjectReportingSettings) error {
	return nil
}
func (s *pathAcceptanceStore) RecordCIAgreementResult(context.Context, *models.CIAgreement) error {
	return nil
}
func (s *pathAcceptanceStore) ListDueCIAgreements(context.Context, time.Time, int) ([]models.CIAgreement, error) {
	return nil, nil
}
func (s *pathAcceptanceStore) UpdateCIAgreementObservation(context.Context, *models.CIAgreement) error {
	return nil
}
func (s *pathAcceptanceStore) ListCIAgreements(context.Context, string, *time.Time, int) ([]models.CIAgreement, error) {
	return nil, nil
}
func (s *pathAcceptanceStore) CreatePathAcceptance(context.Context, *models.PathAcceptance) error {
	return nil
}
func (s *pathAcceptanceStore) GetPathAcceptance(context.Context, string) (*models.PathAcceptance, error) {
	return nil, store.ErrNotFound
}
func (s *pathAcceptanceStore) ListPathAcceptances(_ context.Context, _ string, state string) ([]models.PathAcceptance, error) {
	out := []models.PathAcceptance{}
	for _, row := range s.acceptances {
		if state == "" || row.State == state {
			out = append(out, row)
		}
	}
	return out, nil
}
func (s *pathAcceptanceStore) TransitionPathAcceptance(context.Context, *models.PathAcceptance, ...string) error {
	return nil
}

var _ store.ReportingStore = (*pathAcceptanceStore)(nil)

func pathWorkflow(parent *models.Job, id string) map[string]any {
	return map[string]any{"id": id, "name": id, "source_file": ".reactorcide/workflows/" + id + ".yaml",
		"ci_origin": "base", "ci_repository": parent.CIRepository, "ci_sha": parent.CISHA,
		"execution_profile": "standard", "worker_class": "default", "event_matched": true,
		"jobs": []map[string]any{{"job_name": "compile", "job_command": "make"}}}
}

func pathDecision(id string, files ...string) map[string]any {
	return map[string]any{"id": id, "name": id, "decision": "run", "reasons": []map[string]any{
		{"kind": "paths", "matched": true, "include": []string{"**"}, "matched_files": files, "matched_count": len(files)}}}
}

func pathPayload(t *testing.T, parent *models.Job) []byte {
	t.Helper()
	payload := map[string]any{
		"type": "trigger_job", "trigger_type": "runnerlib_eval",
		"workflows": []map[string]any{pathWorkflow(parent, "build"), pathWorkflow(parent, "web"), pathWorkflow(parent, "free")},
		"decisions": []map[string]any{
			pathDecision("build", "payments/a.go"),
			pathDecision("web", "web/b.ts", "payments/x.go"),
			pathDecision("free", "README.md"),
		},
		"changed_files": []string{"README.md", "payments/a.go", "payments/x.go", "web/b.ts"}, "changed_files_total": 4,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func runPathAcceptance(t *testing.T, mode string, acceptances ...models.PathAcceptance) (*pathAcceptanceStore, []string) {
	t.Helper()
	inner := newEventRecordingStore()
	st := &pathAcceptanceStore{eventRecordingStore: inner, mode: mode, acceptances: acceptances,
		rules: []models.PathOwnerRule{{Pattern: "/payments/", Owners: []string{"reactorcide_group:payments"}}}}
	parent := eventEvalParent(t, "eval-1", "event-1")
	projectID := "project-1"
	parent.ProjectID = &projectID
	inner.base.GetJobByIDFunc = func(_ context.Context, _ string) (*models.Job, error) {
		copied := *parent
		return &copied, nil
	}
	result, err := NewTriggerProcessor(st, nil).ProcessTriggersFromDataWithOutcomes(context.Background(), pathPayload(t, parent), "", parent)
	if err != nil {
		t.Fatal(err)
	}
	started := []string{}
	for _, wf := range result.Workflows {
		started = append(started, wf.WorkflowSecurityID)
	}
	return st, started
}

func reasonOf(decision models.CIEventWorkflowDecision) *models.DecisionReason {
	for i := range decision.Reasons {
		if decision.Reasons[i].Kind == DecisionKindPathAcceptance {
			return &decision.Reasons[i]
		}
	}
	return nil
}

func TestPathAcceptanceEnforce(t *testing.T) {
	st, started := runPathAcceptance(t, models.PathAcceptanceEnforce)
	if strings.Join(started, ",") != "web,free" {
		t.Fatalf("started %v, want web and free", started)
	}
	build := st.decisions["build"]
	reason := reasonOf(build)
	if build.Decision != models.WorkflowDecisionSkipped || reason == nil ||
		reason.Detail != "paths in /payments/ are not accepted by owner reactorcide_group:payments" {
		t.Fatalf("build decision %+v", build)
	}
	web := st.decisions["web"]
	if reason := reasonOf(web); web.Decision != models.WorkflowDecisionRun || reason == nil || !strings.Contains(reason.Detail, "1 matched files are not counted") ||
		len(reason.MatchedFiles) != 1 || reason.MatchedFiles[0] != "payments/x.go" {
		t.Fatalf("web decision %+v", web)
	}
	if reasonOf(st.decisions["free"]) != nil {
		t.Fatalf("a file with no owner needs no acceptance: %+v", st.decisions["free"])
	}
}

func TestPathAcceptanceWarn(t *testing.T) {
	st, started := runPathAcceptance(t, models.PathAcceptanceWarn)
	if len(started) != 3 {
		t.Fatalf("warn must start every workflow: %v", started)
	}
	if reason := reasonOf(st.decisions["build"]); reason == nil || !strings.HasPrefix(reason.Detail, "warning: paths in /payments/") {
		t.Fatalf("build decision %+v", st.decisions["build"])
	}
}

func TestPathAcceptanceOff(t *testing.T) {
	st, started := runPathAcceptance(t, models.PathAcceptanceOff)
	if len(started) != 3 || reasonOf(st.decisions["build"]) != nil {
		t.Fatalf("off must change nothing: %v %+v", started, st.decisions["build"])
	}
}

func TestPathAcceptanceAcceptedDependencyRuns(t *testing.T) {
	st, started := runPathAcceptance(t, models.PathAcceptanceEnforce, models.PathAcceptance{WorkflowSecurityID: "build",
		PathPattern: "/payments/", OwnerSubject: "reactorcide_group:payments", State: models.PathAcceptanceAccepted})
	if strings.Join(started, ",") != "build,web,free" {
		t.Fatalf("started %v", started)
	}
	if reasonOf(st.decisions["build"]) != nil {
		t.Fatalf("an accepted dependency needs no reason: %+v", st.decisions["build"])
	}
}

func TestMatchedFilesOfCappedList(t *testing.T) {
	matched, count := true, 3
	decision := &evalWorkflowDecision{ID: "w", Reasons: []models.DecisionReason{{Kind: "paths", Matched: &matched,
		Include: []string{"src/**"}, Exclude: []string{"src/gen/**"}, MatchedFiles: []string{"src/a.go"}, MatchedCount: &count}}}
	files, filtered, complete := matchedFilesOf(decision, &triggersFile{ChangedFiles: []string{"src/a.go", "src/b.go", "src/gen/x.go", "src/c.go", "docs/a.md"}, ChangedFilesTotal: 5})
	if !filtered || !complete || strings.Join(files, ",") != "src/a.go,src/b.go,src/c.go" {
		t.Fatalf("files %v %v %v", files, filtered, complete)
	}
	_, _, complete = matchedFilesOf(decision, &triggersFile{ChangedFiles: []string{"src/a.go"}, ChangedFilesTotal: 900})
	if complete {
		t.Fatal("a capped event list is not complete")
	}
	if _, filtered, _ := matchedFilesOf(&evalWorkflowDecision{ID: "w"}, &triggersFile{}); filtered {
		t.Fatal("no paths filter")
	}
}
