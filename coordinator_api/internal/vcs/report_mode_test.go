package vcs

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/reporting"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/lib/pq"
)

// reportingChecksStore adds reporting settings and the agreement table to
// the checks fake.
type reportingChecksStore struct {
	*fakeChecksStore
	mu         sync.Mutex
	org        *models.OrgReportingSettings
	project    *models.ProjectReportingSettings
	readErr    error
	agreements map[string]*models.CIAgreement
}

func newReportingStore(fake *fakeChecksStore, mode string, contexts ...string) *reportingChecksStore {
	value := mode
	project := &models.ProjectReportingSettings{ProjectID: "project-1", OrgID: "org-1", ReportMode: &value}
	if len(contexts) > 0 {
		list := pq.StringArray(contexts)
		project.AgreementContexts = &list
	}
	return &reportingChecksStore{fakeChecksStore: fake, project: project, agreements: map[string]*models.CIAgreement{}}
}

func (s *reportingChecksStore) GetOrgReportingSettings(context.Context, string) (*models.OrgReportingSettings, error) {
	return s.org, s.readErr
}
func (s *reportingChecksStore) GetProjectReportingSettings(context.Context, string) (*models.ProjectReportingSettings, error) {
	return s.project, s.readErr
}
func (s *reportingChecksStore) UpsertOrgReportingSettings(context.Context, *models.OrgReportingSettings) error {
	return nil
}
func (s *reportingChecksStore) UpsertProjectReportingSettings(context.Context, *models.ProjectReportingSettings) error {
	return nil
}
func (s *reportingChecksStore) RecordCIAgreementResult(_ context.Context, row *models.CIAgreement) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	copied := *row
	copied.Status, copied.NextCheckAt, copied.Attempts = models.AgreementStatusPending, &now, 0
	if copied.ReactorcideCompletedAt.IsZero() {
		copied.ReactorcideCompletedAt = now
	}
	s.agreements[row.CommitSHA] = &copied
	return nil
}
func (s *reportingChecksStore) ListDueCIAgreements(_ context.Context, now time.Time, _ int) ([]models.CIAgreement, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []models.CIAgreement{}
	for _, row := range s.agreements {
		if row.Status == models.AgreementStatusPending && row.NextCheckAt != nil && !row.NextCheckAt.After(now) {
			out = append(out, *row)
		}
	}
	return out, nil
}
func (s *reportingChecksStore) UpdateCIAgreementObservation(_ context.Context, row *models.CIAgreement) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := *row
	s.agreements[row.CommitSHA] = &copied
	return nil
}
func (s *reportingChecksStore) ListCIAgreements(context.Context, string, *time.Time, int) ([]models.CIAgreement, error) {
	return nil, nil
}
func (s *reportingChecksStore) CreatePathAcceptance(context.Context, *models.PathAcceptance) error {
	return nil
}
func (s *reportingChecksStore) GetPathAcceptance(context.Context, string) (*models.PathAcceptance, error) {
	return nil, store.ErrNotFound
}
func (s *reportingChecksStore) ListPathAcceptances(context.Context, string, string) ([]models.PathAcceptance, error) {
	return nil, nil
}
func (s *reportingChecksStore) TransitionPathAcceptance(context.Context, *models.PathAcceptance, ...string) error {
	return nil
}

// commentClient records statuses and comments.
type commentClient struct {
	recordingClient
	comments []string
}

func (c *commentClient) UpsertPRCommentByMarker(_ context.Context, _ string, _ int, _ string, body string) error {
	c.comments = append(c.comments, body)
	return nil
}

func reportingFixture(mode string, contexts ...string) (*JobStatusUpdater, *reportingChecksStore, *commentClient) {
	updater, fake, _ := newChecksFixture()
	projectID := "project-1"
	fake.event.ProjectID = &projectID
	fake.event.OrgID = "org-1"
	fake.jobs["eval-job"].Status = "completed"
	wrapped := newReportingStore(fake, mode, contexts...)
	updater.SetStore(wrapped)
	client := &commentClient{}
	updater.AddVCSClient(GitHub, client)
	return updater, wrapped, client
}

func TestReportModeEventChecks(t *testing.T) {
	for _, tc := range []struct {
		mode     string
		statuses int
	}{{models.ReportModeFull, 1}, {models.ReportModeStatusesOnly, 1}, {models.ReportModeSilent, 0}} {
		t.Run(tc.mode, func(t *testing.T) {
			updater, st, client := reportingFixture(tc.mode)
			if err := updater.PublishEventChecks(context.Background(), testEventID); err != nil {
				t.Fatal(err)
			}
			if len(client.updates) != tc.statuses {
				t.Fatalf("statuses = %v", client.contexts())
			}
			// The state is saved in every mode, for the UI and the API.
			if st.saveCalls != 1 {
				t.Fatalf("save calls = %d", st.saveCalls)
			}
		})
	}
}

func TestReportModeReadErrorIsSilent(t *testing.T) {
	updater, st, client := reportingFixture(models.ReportModeFull)
	st.readErr = errors.New("database down")
	if err := updater.PublishEventChecks(context.Background(), testEventID); err != nil {
		t.Fatal(err)
	}
	if len(client.updates) != 0 {
		t.Fatalf("a read error must post nothing, got %v", client.contexts())
	}
}

func TestReportModeWorkflowStatusAndLegacyComment(t *testing.T) {
	for _, tc := range []struct {
		mode     string
		statuses int
		comments int
	}{{models.ReportModeFull, 1, 1}, {models.ReportModeStatusesOnly, 1, 0}, {models.ReportModeSilent, 0, 0}} {
		t.Run(tc.mode, func(t *testing.T) {
			updater, _, client := reportingFixture(tc.mode)
			projectID := "project-1"
			pr := 5
			wf := &models.WorkflowInstance{WorkflowID: "wf-1", OrgID: "org-1", ProjectID: &projectID, Name: "build",
				VCSProvider: "github", VCSRepo: "org/repo", CommitSHA: "abc123", PRNumber: &pr, Status: "running"}
			if err := updater.UpdateWorkflowStatus(context.Background(), wf, nil); err != nil {
				t.Fatal(err)
			}
			if len(client.updates) != tc.statuses || len(client.comments) != tc.comments {
				t.Fatalf("statuses %d comments %d", len(client.updates), len(client.comments))
			}
		})
	}
}

func TestReportModeJobStatusAndPolicyStatus(t *testing.T) {
	for _, tc := range []struct {
		mode     string
		statuses int
	}{{models.ReportModeFull, 2}, {models.ReportModeStatusesOnly, 2}, {models.ReportModeSilent, 0}} {
		t.Run(tc.mode, func(t *testing.T) {
			updater, _, client := reportingFixture(tc.mode)
			projectID := "project-1"
			notes, _ := json.Marshal(JobMetadata{VCSProvider: "github", Repo: "org/repo", CommitSHA: "abc123"})
			job := &models.Job{JobID: "job-1", OrgID: "org-1", ProjectID: &projectID, Status: "running", Notes: string(notes)}
			if err := updater.UpdateJobStatus(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			if err := updater.UpdateCIPolicyStatus(context.Background(), job, nil); err != nil {
				t.Fatal(err)
			}
			if len(client.updates) != tc.statuses {
				t.Fatalf("statuses = %v", client.contexts())
			}
		})
	}
}

func TestReportModeSuppressesSharedComment(t *testing.T) {
	for _, tc := range []struct {
		mode       string
		suppressed bool
	}{{models.ReportModeFull, false}, {models.ReportModeStatusesOnly, true}, {models.ReportModeSilent, true}} {
		t.Run(tc.mode, func(t *testing.T) {
			updater, _, _ := reportingFixture(tc.mode)
			projectID := "project-1"
			resolver := NewReportClientResolver(updater)
			client, err := resolver.ResolveReportClient(context.Background(), &models.VCSReportTarget{OrgID: "org-1", ProjectID: &projectID,
				Provider: "github", Repository: "org/repo", TargetType: "pull_request", ExternalTargetID: "5"})
			if err != nil {
				t.Fatal(err)
			}
			_, suppressed := client.(suppressedCommentClient)
			if suppressed != tc.suppressed {
				t.Fatalf("suppressed = %v", suppressed)
			}
		})
	}
}

// fakeResults is a VCS API whose other CI finishes later.
type fakeResults struct {
	calls   int
	answers [][]models.AgreementOtherResult
	err     error
}

func (f *fakeResults) ListCommitCIResults(context.Context, string, string) ([]models.AgreementOtherResult, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	index := f.calls - 1
	if index >= len(f.answers) {
		index = len(f.answers) - 1
	}
	return f.answers[index], nil
}

func TestAgreementRecordedAndCompletedAfterLateOtherCI(t *testing.T) {
	ctx := context.Background()
	updater, st, _ := reportingFixture(models.ReportModeSilent, "ci/build", "ci/test*")
	if err := updater.PublishEventChecks(ctx, testEventID); err != nil {
		t.Fatal(err)
	}
	row := st.agreements["abc123"]
	if row == nil || row.ReactorcideResult != models.AgreementResultSuccess || row.Status != models.AgreementStatusPending {
		t.Fatalf("agreement row %+v", row)
	}
	results := &fakeResults{answers: [][]models.AgreementOtherResult{
		{{Name: "ci/build", Kind: "status", State: "pending"}, {Name: "Reactorcide Checks", Kind: "status", State: "success"}},
		{{Name: "ci/build", Kind: "status", State: "success"}, {Name: "ci/test-unit", Kind: "check", State: "success"}},
	}}
	now := time.Now().UTC()
	pass := &AgreementPass{Updater: updater, Now: func() time.Time { return now },
		Clients: func(context.Context, models.CIAgreement) CommitResultsReader { return results }}
	if count, err := pass.RunOnce(ctx); err != nil || count != 1 {
		t.Fatalf("first pass %d %v", count, err)
	}
	row = st.agreements["abc123"]
	if row.Status != models.AgreementStatusPending || row.Agree != nil || row.NextCheckAt == nil {
		t.Fatalf("after the first read %+v", row)
	}
	if len(row.OtherResults) != 1 {
		t.Fatalf("own contexts must not count: %+v", row.OtherResults)
	}
	// Not due yet: the backoff holds the second read.
	if count, _ := pass.RunOnce(ctx); count != 0 {
		t.Fatalf("read before the backoff: %d", count)
	}
	now = now.Add(reporting.NextDelay(1) + time.Second)
	if _, err := pass.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	row = st.agreements["abc123"]
	if row.Status != models.AgreementStatusComplete || row.Agree == nil || !*row.Agree || row.OtherResult != models.AgreementResultSuccess {
		t.Fatalf("after the late result %+v", row)
	}
	if results.calls != 2 {
		t.Fatalf("calls %d", results.calls)
	}
}

func TestAgreementNotRecordedWithoutContexts(t *testing.T) {
	updater, st, _ := reportingFixture(models.ReportModeFull)
	if err := updater.PublishEventChecks(context.Background(), testEventID); err != nil {
		t.Fatal(err)
	}
	if len(st.agreements) != 0 {
		t.Fatalf("rows %+v", st.agreements)
	}
}

func TestAgreementReadErrorBacksOff(t *testing.T) {
	ctx := context.Background()
	updater, st, _ := reportingFixture(models.ReportModeFull, "ci/build")
	if err := updater.PublishEventChecks(ctx, testEventID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	results := &fakeResults{err: errors.New("rate limited")}
	pass := &AgreementPass{Updater: updater, Now: func() time.Time { return now },
		Clients: func(context.Context, models.CIAgreement) CommitResultsReader { return results }}
	if _, err := pass.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	row := st.agreements["abc123"]
	if row.Status != models.AgreementStatusPending || row.LastError == "" || row.NextCheckAt == nil || !row.NextCheckAt.After(now) {
		t.Fatalf("row %+v", row)
	}
}
