package handlers

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/approvals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/reporting"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reportingWebhookStore adds reporting settings and the shared comment
// entries to the webhook mock store.
type reportingWebhookStore struct {
	*WebhookMockStore
	org     *models.OrgReportingSettings
	entries map[string]*models.VCSReportEntry
}

func (s *reportingWebhookStore) GetOrgReportingSettings(context.Context, string) (*models.OrgReportingSettings, error) {
	return s.org, nil
}
func (s *reportingWebhookStore) GetProjectReportingSettings(context.Context, string) (*models.ProjectReportingSettings, error) {
	return nil, nil
}
func (s *reportingWebhookStore) UpsertOrgReportingSettings(context.Context, *models.OrgReportingSettings) error {
	return nil
}
func (s *reportingWebhookStore) UpsertProjectReportingSettings(context.Context, *models.ProjectReportingSettings) error {
	return nil
}
func (s *reportingWebhookStore) RecordCIAgreementResult(context.Context, *models.CIAgreement) error {
	return nil
}
func (s *reportingWebhookStore) ListDueCIAgreements(context.Context, time.Time, int) ([]models.CIAgreement, error) {
	return nil, nil
}
func (s *reportingWebhookStore) UpdateCIAgreementObservation(context.Context, *models.CIAgreement) error {
	return nil
}
func (s *reportingWebhookStore) ListCIAgreements(context.Context, string, *time.Time, int) ([]models.CIAgreement, error) {
	return nil, nil
}
func (s *reportingWebhookStore) CreatePathAcceptance(context.Context, *models.PathAcceptance) error {
	return nil
}
func (s *reportingWebhookStore) GetPathAcceptance(context.Context, string) (*models.PathAcceptance, error) {
	return nil, store.ErrNotFound
}
func (s *reportingWebhookStore) ListPathAcceptances(context.Context, string, string) ([]models.PathAcceptance, error) {
	return nil, nil
}
func (s *reportingWebhookStore) TransitionPathAcceptance(context.Context, *models.PathAcceptance, ...string) error {
	return nil
}
func (s *reportingWebhookStore) UpsertVCSReportTarget(_ context.Context, target *models.VCSReportTarget) error {
	target.ReportTargetID = "target-1"
	return nil
}
func (s *reportingWebhookStore) UpsertVCSReportEntry(_ context.Context, entry *models.VCSReportEntry) error {
	copied := *entry
	s.entries[entry.EntryKey] = &copied
	return nil
}

// codeownersClient serves a code owners file from the contents API.
type codeownersClient struct {
	*resolvingVCSClient
	files map[string]string
	refs  []string
}

func (c *codeownersClient) GetFileContent(_ context.Context, _, filePath, ref string) ([]byte, bool, error) {
	c.refs = append(c.refs, ref)
	content, ok := c.files[filePath]
	return []byte(content), ok, nil
}

func newCodeownersHarness(t *testing.T, mode string, subjects map[string][]string) (*approvalHarness, *reportingWebhookStore, *codeownersClient) {
	t.Helper()
	reporting.DefaultCodeownersCache = &reporting.CodeownersCache{}
	h := newApprovalHarness(t, subjects)
	org := models.DefaultOrgReportingSettings(h.project.OrgID)
	org.CodeownersImport = models.CodeownersImportOn
	org.ReportMode = mode
	wrapped := &reportingWebhookStore{WebhookMockStore: h.store, org: org, entries: map[string]*models.VCSReportEntry{}}
	h.handler.store = wrapped
	client := &codeownersClient{resolvingVCSClient: h.client, files: map[string]string{
		".github/CODEOWNERS": "* @test-org/core\n/payments/ @test-org/payments\n/docs/\n",
	}}
	h.handler.AddVCSClient(vcs.GitHub, client)
	h.client.files = []string{"payments/a.go", "docs/readme.md"}
	return h, wrapped, client
}

func TestCodeownersImportDerivedReviews(t *testing.T) {
	h, st, client := newCodeownersHarness(t, models.ReportModeFull, map[string][]string{
		"bob":   {"repository_write"},
		"alice": {"repository_write", "vcs_team:test-org/payments"},
	})
	payments := reporting.DerivedReviewID([]string{"vcs_team:test-org/payments"})
	require.True(t, strings.HasPrefix(payments, "codeowners-test-org-payments-"), payments)
	h.comment(t, "bob", "/reactorcide accept", 1)
	statuses := h.reviewStatuses()
	require.Len(t, statuses, 1)
	assert.Equal(t, vcs.StatusPending, statuses[0].State)
	assert.Contains(t, statuses[0].Description, payments+" (0 of 1)")
	assert.Contains(t, client.refs, "base456", "the file comes from the trusted base SHA")

	entry := st.entries[reviewsReportKey]
	require.NotNil(t, entry, "the PR comment has a reviews entry")
	body, _ := entry.StructuredState["body"].(string)
	assert.Contains(t, body, "`"+payments+"`: waiting (0 of 1)")
	assert.Contains(t, body, "/reactorcide accept "+payments)
	assert.Equal(t, true, entry.StructuredState["persistent"])

	h.comment(t, "alice", "/reactorcide accept "+payments, 2)
	statuses = h.reviewStatuses()
	assert.Equal(t, vcs.StatusSuccess, statuses[len(statuses)-1].State, statuses[len(statuses)-1].Description)
	body, _ = st.entries[reviewsReportKey].StructuredState["body"].(string)
	assert.Contains(t, body, "✅ `"+payments+"`: accepted (1 of 1)")
	assert.Contains(t, body, "Accepted by github/alice")
}

func TestCodeownersImportNoFileStillPostsSuccess(t *testing.T) {
	h, _, client := newCodeownersHarness(t, models.ReportModeFull, map[string][]string{"bob": {"repository_write"}})
	client.files = map[string]string{}
	h.comment(t, "bob", "/reactorcide accept", 1)
	statuses := h.reviewStatuses()
	require.Len(t, statuses, 1)
	assert.Equal(t, vcs.StatusSuccess, statuses[0].State)
	assert.Equal(t, "No required review applies to this change", statuses[0].Description)
}

func TestReportModeSilentPostsNoReviewsStatus(t *testing.T) {
	h, st, _ := newCodeownersHarness(t, models.ReportModeSilent, map[string][]string{"bob": {"repository_write"}})
	h.comment(t, "bob", "/reactorcide accept", 1)
	assert.Empty(t, h.reviewStatuses(), "silent posts no status")
	assert.NotNil(t, st.entries[reviewsReportKey], "the result stays in Reactorcide")
}

func TestRenderReviewLines(t *testing.T) {
	body := RenderReviewLines(&approvals.HeadReviews{Summary: "Needs review: a (0 of 2)", Reviews: []approvals.ReviewResult{
		{ID: "a", Description: "Trust | anchors", Applies: true, Required: 2, Accepted: []string{}, Accept: []string{"reactorcide_group:x"}},
		{ID: "b", Applies: true, Satisfied: true, Required: 1, Accepted: []string{"github/alice"}},
		{ID: "c", Applies: false, Satisfied: true, Required: 1},
		{ID: "codeowners-unavailable", Applies: true, Required: 1, Accept: []string{}},
	}})
	lines := strings.Split(body, "\n")
	require.Len(t, lines, 6, body)
	assert.Equal(t, "Needs review: a (0 of 2)", lines[0])
	assert.Equal(t, "- ⏳ `a`: waiting (0 of 2) — Trust \\| anchors. Can accept: reactorcide_group:x; give `/reactorcide accept a`", lines[2])
	assert.Equal(t, "- ✅ `b`: accepted (1 of 1). Accepted by github/alice", lines[3])
	assert.Equal(t, "- ➖ `c`: does not apply", lines[4])
	assert.Contains(t, lines[5], "Nobody can accept this review now")
}

// ownerScopedReportingStore has a silent report mode only for one
// organization, and one project owned by that organization.
type ownerScopedReportingStore struct {
	*reportingWebhookStore
	ownerOrg string
	project  *models.Project
}

func (s *ownerScopedReportingStore) GetOrgReportingSettings(_ context.Context, orgID string) (*models.OrgReportingSettings, error) {
	if orgID == s.ownerOrg {
		return s.org, nil
	}
	return models.DefaultOrgReportingSettings(orgID), nil
}

func (s *ownerScopedReportingStore) GetProjectByID(_ context.Context, id string) (*models.Project, error) {
	if id == s.project.ProjectID {
		return s.project, nil
	}
	return nil, store.ErrNotFound
}

// The report mode of a job comes from the owning organization of its
// project, not from the organization recorded on the job.
func TestVCSStatusesAllowedForJobUsesProjectOwner(t *testing.T) {
	h, st, _ := newCodeownersHarness(t, models.ReportModeSilent, nil)
	owner := "00000000-0000-0000-0000-0000000000ee"
	project := &models.Project{ProjectID: "00000000-0000-0000-0000-0000000000p1", OrgID: owner}
	h.handler.store = &ownerScopedReportingStore{reportingWebhookStore: st, ownerOrg: owner, project: project}
	job := &models.Job{OrgID: "00000000-0000-0000-0000-0000000000ff", ProjectID: &project.ProjectID}
	assert.False(t, h.handler.vcsStatusesAllowedForJob(context.Background(), job), "the owner's silent mode applies")
}
