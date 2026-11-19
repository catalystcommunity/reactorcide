package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/approvals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/cipolicy"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/priority"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/ratelimit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/vcs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wp9Store adds the organization, draft and shed settings to the event
// webhook store, and records event status and summary writes.
type wp9Store struct {
	*eventWebhookStore
	org       *models.Organization
	draftPRs  string
	shed      models.ShedState
	statuses  map[string]string
	summaries map[string]models.CIEventSummary
}

func newWP9Store(profile string) *wp9Store {
	return &wp9Store{
		eventWebhookStore: &eventWebhookStore{WebhookMockStore: &WebhookMockStore{}},
		org:               &models.Organization{OrgID: "00000000-0000-0000-0000-000000000001", Name: "acme", DefaultsProfile: profile},
		draftPRs:          models.DraftPRsRun,
		shed:              models.EffectiveShedState(models.ShedLevelNormal, models.ShedLevelNormal, nil),
		statuses:          map[string]string{},
		summaries:         map[string]models.CIEventSummary{},
	}
}

func (s *wp9Store) GetOrganizationByID(_ context.Context, orgID string) (*models.Organization, error) {
	if s.org == nil || orgID != s.org.OrgID {
		return nil, store.ErrNotFound
	}
	return s.org, nil
}
func (s *wp9Store) EffectiveDraftPRs(context.Context, string) (string, error) { return s.draftPRs, nil }
func (s *wp9Store) SetOrganizationDraftPRs(_ context.Context, _ string, value string) error {
	s.draftPRs = value
	return nil
}
func (s *wp9Store) GetShedState(context.Context, string) (models.ShedState, error) {
	return s.shed, nil
}
func (s *wp9Store) SetGlobalShedLevel(_ context.Context, level string) error {
	s.shed = models.EffectiveShedState(level, s.shed.OrgLevel, nil)
	return nil
}
func (s *wp9Store) SetOrganizationShed(_ context.Context, _ string, level string, _ *models.ShedActionsConfig, _ bool) error {
	s.shed = models.EffectiveShedState(s.shed.GlobalLevel, level, nil)
	return nil
}
func (s *wp9Store) SetCIEventStatus(_ context.Context, eventID, status string) error {
	s.statuses[eventID] = status
	for _, event := range s.events {
		if event.EventID == eventID {
			event.Status = status
		}
	}
	return nil
}
func (s *wp9Store) SetCIEventSummary(_ context.Context, eventID string, summary models.CIEventSummary) error {
	s.summaries[eventID] = summary
	return nil
}

// newWP9Harness is the approval harness with a wp9Store behind the handler,
// so the organization defaults profile, drafts and shed levels apply.
func newWP9Harness(t *testing.T, profile string, subjects map[string][]string) (*approvalHarness, *wp9Store) {
	t.Helper()
	h := newApprovalHarness(t, subjects)
	st := newWP9Store(profile)
	st.WebhookMockStore = h.store
	h.project.OrgID = st.org.OrgID
	h.handler = NewWebhookHandler(st, corndogs.NewMockClient())
	h.handler.SetTokenResolver(testTokenResolver())
	h.handler.AddVCSClient(vcs.GitHub, h.client)
	return h, st
}

func secureGate() *models.CITrustSettings {
	return &models.CITrustSettings{RunGate: models.SettingsForDefaultsProfile(models.DefaultsProfileSecure).RunGate}
}

func builtInRevision(t *testing.T) string {
	t.Helper()
	policy, _, err := cipolicy.BuiltIn()
	require.NoError(t, err)
	return policy.Revision
}

// Secure defaults: a same-repository pull request of a collaborator runs with
// no extra setup, on base CI, with the built-in policy. The run gate does not
// hold it.
func TestSecureDefaults_CollaboratorPullRequestRunsBaseCI(t *testing.T) {
	h, _ := newWP9Harness(t, models.DefaultsProfileSecure, map[string][]string{"author": {"repository_write"}})
	h.store.OrgTrust = secureGate()

	h.pullRequestEvent(t)

	require.Len(t, h.store.CreateJobCalls, 1, "a collaborator pull request must run with no extra setup")
	job := h.store.CreateJobCalls[0]
	assert.Equal(t, "base456", job.CISHA, "secure defaults run CI from the base")
	assert.Equal(t, builtInRevision(t), job.JobEnvVars["REACTORCIDE_CI_POLICY_REVISION"])
	assert.Equal(t, cipolicy.BuiltInDocument, job.JobEnvVars["REACTORCIDE_CI_POLICY"])
	_, hasAuthority := job.JobEnvVars[approvals.RunAuthorityEnv]
	assert.False(t, hasAuthority, "the gate does not apply to a collaborator")
}

// Secure defaults: an outside contributor waits for approve-ci. The approved
// run uses trusted base CI with no secrets.
func TestSecureDefaults_OutsideContributorIsGated(t *testing.T) {
	h, _ := newWP9Harness(t, models.DefaultsProfileSecure, map[string][]string{"maintainer": {"repository_write"}})
	h.store.OrgTrust = secureGate()

	h.pullRequestEvent(t)
	require.Empty(t, h.store.CreateJobCalls, "an outside contributor must meet the run gate")

	h.comment(t, "maintainer", "/reactorcide approve-ci", 1)
	require.Len(t, h.store.CreateJobCalls, 1)
	authority := h.runAuthority(t, h.store.CreateJobCalls[0])
	assert.True(t, authority.Gated)
	assert.Equal(t, models.GatedRunNoSecretsProfile, authority.Profile)
}

// Secure defaults: a same-repository pull request that changes .reactorcide/
// runs base CI (the built-in policy has no head_ci rule) until a
// collaborator gives override-ci. The override evaluation carries the
// override authority; the trigger processor then runs the head's CI.
func TestSecureDefaults_CIChangeRunsBaseCIUntilOverride(t *testing.T) {
	h, _ := newWP9Harness(t, models.DefaultsProfileSecure, map[string][]string{
		"author": {"repository_write"}, "maintainer": {"repository_write"},
	})
	h.store.OrgTrust = secureGate()
	h.client.files = []string{".reactorcide/workflows/pr.yaml"}

	h.pullRequestEvent(t)
	require.Len(t, h.store.CreateJobCalls, 1)
	first := h.store.CreateJobCalls[0]
	assert.Equal(t, "base456", first.CISHA)
	assert.NotEmpty(t, first.JobEnvVars["REACTORCIDE_CI_POLICY"], "the policy decides head CI, so the head cannot select itself")
	assert.Nil(t, h.runAuthority(t, first).Override)

	h.comment(t, "maintainer", "/reactorcide override-ci", 1)
	require.Len(t, h.store.CreateJobCalls, 2, "override-ci evaluates the head again")
	authority := h.runAuthority(t, h.store.CreateJobCalls[1])
	require.NotNil(t, authority.Override)
	assert.Equal(t, models.DefaultOverrideProfile, authority.Override.Profile)
}

// Legacy organizations keep the behavior from before: a same-repository head
// with no stored policy runs its own CI, and no policy is attached.
func TestLegacyDefaults_SameRepoPullRequestRunsHeadCI(t *testing.T) {
	h, _ := newWP9Harness(t, models.DefaultsProfileLegacy, nil)
	h.pullRequestEvent(t)
	require.Len(t, h.store.CreateJobCalls, 1)
	job := h.store.CreateJobCalls[0]
	assert.Equal(t, "head123", job.CISHA)
	_, hasPolicy := job.JobEnvVars["REACTORCIDE_CI_POLICY"]
	assert.False(t, hasPolicy)
}

// A read error of the organization must not make a secure org run head CI:
// the eval keeps the trusted base (fail closed). Only a known legacy org
// runs the head's own CI.
func TestDefaultsProfileReadErrorFailsClosed(t *testing.T) {
	h, st := newWP9Harness(t, models.DefaultsProfileSecure, nil)
	st.org = nil // GetOrganizationByID fails
	h.pullRequestEvent(t)
	require.Len(t, h.store.CreateJobCalls, 1)
	job := h.store.CreateJobCalls[0]
	assert.Equal(t, "base456", job.CISHA, "an unreadable defaults profile must keep the base CI")
	_, hasPolicy := job.JobEnvVars["REACTORCIDE_CI_POLICY"]
	assert.False(t, hasPolicy)
}

func draftEvent(action string, generic vcs.EventType, draft bool) *vcs.WebhookEvent {
	event := prWebhookEvent()
	event.GenericEvent = generic
	event.PullRequest.Action = action
	event.PullRequest.Draft = draft
	return event
}

func TestDraftPRs_Modes(t *testing.T) {
	degraded := models.EffectiveShedState(models.ShedLevelDegraded, models.ShedLevelNormal, nil)
	cases := []struct {
		name     string
		draftPRs string
		shed     models.ShedState
		draft    bool
		wantRun  bool
		wantKind string
	}{
		{"run mode runs a draft", models.DraftPRsRun, models.ShedState{}, true, true, ""},
		{"skip mode skips a draft", models.DraftPRsSkip, models.ShedState{}, true, false, models.SkipReasonDraft},
		{"skip mode runs a ready pull request", models.DraftPRsSkip, models.ShedState{}, false, true, ""},
		{"skip_when_shedding runs at normal", models.DraftPRsSkipWhenShedding, models.ShedState{}, true, true, ""},
		{"skip_when_shedding skips when degraded", models.DraftPRsSkipWhenShedding, degraded, true, false, models.SkipReasonShed},
		{"run mode runs when degraded", models.DraftPRsRun, degraded, true, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newWP9Store(models.DefaultsProfileLegacy)
			st.draftPRs = tc.draftPRs
			if tc.shed.Level != "" {
				st.shed = tc.shed
			}
			handler := newEventWebhookHandler(t, st.eventWebhookStore, draftEvent("opened", vcs.EventPullRequestOpened, tc.draft))
			handler.store = st
			deliverWebhook(t, handler, "pull_request", "delivery-"+tc.name, makePRWebhookBody("test-org/test-repo", "https://github.com/test-org/test-repo.git", "abc123", "feature", "main", 42))
			require.Len(t, st.events, 1)
			eventID := st.events[0].EventID
			if tc.wantRun {
				assert.Len(t, st.CreateJobCalls, 1)
				assert.Empty(t, st.statuses[eventID])
				return
			}
			assert.Empty(t, st.CreateJobCalls, "a skipped draft creates no eval job")
			assert.Equal(t, models.CIEventStatusSkipped, st.statuses[eventID])
			reasons := st.summaries[eventID].SkipReasons
			require.Len(t, reasons, 1)
			assert.Equal(t, tc.wantKind, reasons[0].Kind)
			assert.Contains(t, reasons[0].Detail, "draft pull request")
			assert.True(t, st.summaries[eventID].Draft)
		})
	}
}

// ready_for_review is a pull_request_updated event with draft=false, so it
// runs the work that the draft skipped.
func TestDraftPRs_ReadyForReviewRuns(t *testing.T) {
	st := newWP9Store(models.DefaultsProfileLegacy)
	st.draftPRs = models.DraftPRsSkip
	handler := newEventWebhookHandler(t, st.eventWebhookStore, draftEvent("ready_for_review", vcs.EventPullRequestUpdated, false))
	handler.store = st
	deliverWebhook(t, handler, "pull_request", "delivery-ready", makePRWebhookBody("test-org/test-repo", "https://github.com/test-org/test-repo.git", "abc123", "feature", "main", 42))
	assert.Len(t, st.CreateJobCalls, 1)
}

func TestShedDegraded_LowersPullRequestPriority(t *testing.T) {
	st := newWP9Store(models.DefaultsProfileLegacy)
	st.shed = models.EffectiveShedState(models.ShedLevelNormal, models.ShedLevelDegraded, nil)
	handler := newEventWebhookHandler(t, st.eventWebhookStore, prWebhookEvent())
	handler.store = st
	deliverWebhook(t, handler, "pull_request", "delivery-prio", makePRWebhookBody("test-org/test-repo", "https://github.com/test-org/test-repo.git", "abc123", "feature", "main", 42))
	require.Len(t, st.CreateJobCalls, 1)
	// lower_pr_priority moves pull request work one lane down
	// (priority.ShedAdjustment): the eval-pr lane 10 + ShedPRShift.
	assert.Equal(t, 10+priority.ShedPRShift, st.CreateJobCalls[0].Priority)
	assert.Less(t, st.CreateJobCalls[0].Priority, 5, "below a push eval")
}

func postWebhook(handler *WebhookHandler, deliveryID string) *httptest.ResponseRecorder {
	body := makePRWebhookBody("test-org/test-repo", "https://github.com/test-org/test-repo.git", "abc123", "feature", "main", 42)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/webhooks/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-GitHub-Delivery", deliveryID)
	w := httptest.NewRecorder()
	handler.HandleGitHubWebhook(w, req)
	return w
}

// Over the project limit, a delivery gets 429 with Retry-After and is
// recorded as rate_limited. A redelivery after the limit evaluates it.
func TestWebhookRateLimit_RecordsAndReplays(t *testing.T) {
	SetWebhookRateLimiter(ratelimit.New(1, 1))
	t.Cleanup(func() { SetWebhookRateLimiter(nil) })
	st := newWP9Store(models.DefaultsProfileLegacy)
	handler := newEventWebhookHandler(t, st.eventWebhookStore, prWebhookEvent())
	handler.store = st

	require.Equal(t, http.StatusOK, postWebhook(handler, "d-1").Code)
	limited := postWebhook(handler, "d-2")
	require.Equal(t, http.StatusTooManyRequests, limited.Code)
	assert.NotEmpty(t, limited.Header().Get("Retry-After"))
	require.Len(t, st.CreateJobCalls, 1)
	require.Len(t, st.events, 2)
	recorded := st.events[1]
	assert.Equal(t, models.CIEventStatusRateLimited, recorded.Status)
	require.Len(t, recorded.Summary.SkipReasons, 1)
	assert.Equal(t, models.SkipReasonRateLimit, recorded.Summary.SkipReasons[0].Kind)

	// The limit is gone: a redelivery of d-2 evaluates the recorded event.
	SetWebhookRateLimiter(nil)
	require.Equal(t, http.StatusOK, postWebhook(handler, "d-2").Code)
	require.Len(t, st.CreateJobCalls, 2)
	assert.Equal(t, recorded.EventID, *st.CreateJobCalls[1].EventID)
	assert.Equal(t, models.CIEventStatusReceived, st.statuses[recorded.EventID])
}

func instanceTokenContext(r *http.Request) *http.Request {
	principal := &checkauth.Principal{CredentialID: "admin", CredentialType: "instance_token", AllOrganizations: true, AllCapabilities: true}
	return r.WithContext(checkauth.SetPrincipalContext(r.Context(), principal))
}

func TestDefaultJobCommand_OnlyGlobalAdministrators(t *testing.T) {
	project := &models.Project{ProjectID: "p1", Name: "p", RepoURL: "github.com/o/r", OrgID: "org-1"}
	mockStore := &ProjectMockStore{}
	mockStore.GetProjectByIDFunc = func(context.Context, string) (*models.Project, error) {
		copy := *project
		return &copy, nil
	}
	handler := NewProjectHandler(mockStore)
	update := func(r *http.Request) int {
		body, _ := json.Marshal(map[string]string{"default_job_command": "make ci"})
		req := httptest.NewRequest(http.MethodPut, "/api/v1/projects/p1", bytes.NewReader(body))
		req = req.WithContext(setIDContext(req.Context(), "project_id", "p1"))
		if r != nil {
			req = req.WithContext(r.Context())
			req = req.WithContext(setIDContext(req.Context(), "project_id", "p1"))
		}
		w := httptest.NewRecorder()
		handler.UpdateProject(w, req)
		return w.Code
	}
	plain := withUser(httptest.NewRequest(http.MethodPut, "/", nil))
	assert.Equal(t, http.StatusForbidden, update(plain), "a project owner cannot replace the eval command")

	admin := httptest.NewRequest(http.MethodPut, "/", nil)
	admin = admin.WithContext(checkauth.SetUserContext(admin.Context(), &models.User{UserID: "u", Roles: []string{"admin"}}))
	assert.Equal(t, http.StatusOK, update(admin))

	assert.Equal(t, http.StatusOK, update(instanceTokenContext(httptest.NewRequest(http.MethodPut, "/", nil))), "the admin API token can set it")

	scoped := httptest.NewRequest(http.MethodPut, "/", nil)
	scoped = scoped.WithContext(checkauth.SetPrincipalContext(scoped.Context(), &checkauth.Principal{
		CredentialID: "svc", CredentialType: "service_token", OrganizationIDs: []string{"org-1"}, AllCapabilities: true}))
	assert.Equal(t, http.StatusForbidden, update(scoped), "an organization token is not a global administrator")
}

// shedRouteStore is the store behind the shed REST routes in tests.
type shedRouteStore struct {
	store.Store
	*wp9Store
	audits []string
}

func (s *shedRouteStore) GetOrganizationByName(_ context.Context, name string) (*models.Organization, error) {
	if name != s.org.Name {
		return nil, store.ErrNotFound
	}
	return s.org, nil
}
func (s *shedRouteStore) CreateOrganization(context.Context, *models.Organization) error { return nil }
func (s *shedRouteStore) ListOrganizations(context.Context, int, int) ([]models.Organization, error) {
	return nil, nil
}
func (s *shedRouteStore) UpdateOrganization(context.Context, *models.Organization) error { return nil }
func (s *shedRouteStore) DeleteOrganization(context.Context, string, string) error       { return nil }
func (s *shedRouteStore) SetDefaultOrganization(context.Context, string) error           { return nil }
func (s *shedRouteStore) GetDefaultOrganization(context.Context) (*models.Organization, error) {
	return s.org, nil
}
func (s *shedRouteStore) AppendAuditEvent(_ context.Context, event *models.AuditEvent) error {
	s.audits = append(s.audits, event.Action)
	return nil
}
func (s *shedRouteStore) ApplyOrganizationDefaultsProfile(_ context.Context, _ string, profile, _ string) error {
	s.org.DefaultsProfile = profile
	return nil
}

func TestShedRoutes_GlobalAndOrganization(t *testing.T) {
	st := &shedRouteStore{wp9Store: newWP9Store(models.DefaultsProfileLegacy)}
	routes := newShedRoutes(st)
	call := func(method, path string, body any, withToken bool) *httptest.ResponseRecorder {
		var reader *bytes.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			reader = bytes.NewReader(raw)
		} else {
			reader = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, path, reader)
		if withToken {
			req = instanceTokenContext(req)
		}
		w := httptest.NewRecorder()
		if path == "/api/v1/shed" {
			routes.serveGlobal(w, req)
		} else {
			passthrough := func(next http.Handler) http.Handler { return next }
			require.True(t, routes.serveOrg(w, req, path[len("/api/v1/organizations/"):], passthrough, passthrough))
		}
		return w
	}

	assert.Equal(t, http.StatusForbidden, call(http.MethodPut, "/api/v1/shed", map[string]string{"level": "critical"}, false).Code)
	assert.Equal(t, http.StatusBadRequest, call(http.MethodPut, "/api/v1/shed", map[string]string{"level": "panic"}, true).Code)
	w := call(http.MethodPut, "/api/v1/shed", map[string]string{"level": "degraded", "reason": "worker outage"}, true)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var state models.ShedState
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &state))
	assert.Equal(t, models.ShedLevelDegraded, state.Level)

	w = call(http.MethodPut, "/api/v1/organizations/acme/shed", map[string]string{"level": "critical"}, true)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &state))
	assert.Equal(t, models.ShedLevelCritical, state.Level)
	assert.True(t, state.Actions.RefuseManualRuns)
	assert.Equal(t, "org", state.Source)

	w = call(http.MethodPut, "/api/v1/organizations/acme/defaults-profile", map[string]string{"profile": "secure"}, true)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, models.DefaultsProfileSecure, st.org.DefaultsProfile)
	assert.Equal(t, []string{"shed.set_global", "shed.set_org", "organization.defaults_profile"}, st.audits, "every change is audited")
}

func TestShedCritical_RefusesManualRunsForNonAdministrators(t *testing.T) {
	st := &shedRouteStore{wp9Store: newWP9Store(models.DefaultsProfileLegacy)}
	st.shed = models.EffectiveShedState(models.ShedLevelCritical, models.ShedLevelNormal, nil)
	ctx := context.Background()
	scoped := checkauth.SetPrincipalContext(ctx, &checkauth.Principal{CredentialID: "ci", CredentialType: "service_token",
		OrganizationIDs: []string{st.org.OrgID}, AllCapabilities: false})
	assert.Contains(t, shedManualRunRefusal(scoped, st, st.org.OrgID), "manual runs are refused")
	admin := checkauth.SetPrincipalContext(ctx, &checkauth.Principal{CredentialID: "admin", CredentialType: "instance_token",
		AllOrganizations: true, AllCapabilities: true})
	assert.Empty(t, shedManualRunRefusal(admin, st, st.org.OrgID))
	st.shed = models.EffectiveShedState(models.ShedLevelDegraded, models.ShedLevelNormal, nil)
	assert.Empty(t, shedManualRunRefusal(scoped, st, st.org.OrgID), "degraded does not refuse manual runs")
}
