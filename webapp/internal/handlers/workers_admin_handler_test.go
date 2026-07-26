package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/webapp/internal/uiclient/csilapi"
)

// --- nav gating ---

// TestNav_ShowsWorkersLinkWhenCapable renders just the shared "head" layout
// template (the way TestJobsListTemplate exercises jobs_list.html) so this
// doesn't need a live uiClients/fake coordinator round trip — SessionInfo is
// supplied directly, matching how h.render always injects "Session".
func TestNav_ShowsWorkersLinkWhenCapable(t *testing.T) {
	h := NewWebHandler(NewAPIClient(), nil)
	var buf strings.Builder
	data := map[string]interface{}{
		"Title":   "x",
		"Session": SessionInfo{Caps: csilapi.GetCapabilitiesResponse{ManageWorkers: true}},
	}
	if err := h.templates.ExecuteTemplate(&buf, "head", data); err != nil {
		t.Fatalf("ExecuteTemplate: %v", err)
	}
	if !strings.Contains(buf.String(), `href="/app/workers"`) {
		t.Errorf("expected Workers nav link for a ManageWorkers session, got: %s", buf.String())
	}
}

func TestNav_HidesWorkersLinkWhenIncapable(t *testing.T) {
	h := NewWebHandler(NewAPIClient(), nil)
	var buf strings.Builder
	data := map[string]interface{}{
		"Title":   "x",
		"Session": SessionInfo{Caps: csilapi.GetCapabilitiesResponse{}},
	}
	if err := h.templates.ExecuteTemplate(&buf, "head", data); err != nil {
		t.Fatalf("ExecuteTemplate: %v", err)
	}
	if strings.Contains(buf.String(), `href="/app/workers"`) {
		t.Errorf("did not expect Workers nav link for a non-ManageWorkers session, got: %s", buf.String())
	}
}

// --- WorkersPage whole-page gating ---

func TestWorkersPage_ForbiddenWithoutManageWorkers(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	withCapabilities(fc, csilapi.GetCapabilitiesResponse{})
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodGet, "/app/workers", nil)
	rec := httptest.NewRecorder()
	h.withSession(h.WorkersPage)(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "ci-pool") {
		t.Errorf("forbidden response should not leak pool markup, got: %s", rec.Body.String())
	}
}

func withWorkersFakes(fc *fakeCoordinator) {
	fc.handle("ReactorcideUi", "list-pools", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		pools := []csilapi.WorkerPoolSummary{{PoolId: "pool1", Name: "ci-pool", CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z"}}
		return csilapi.EncodeListPoolsResponse(csilapi.ListPoolsResponse{Pools: pools}), "ListPoolsResponse", false
	})
	fc.handle("ReactorcideUi", "list-workers", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		hostname := "runner-1"
		workers := []csilapi.WorkerSummary{{WorkerId: "w1", PoolId: "pool1", Hostname: &hostname, Os: "linux", Arch: "amd64", Status: "active"}}
		return csilapi.EncodeListWorkersResponse(csilapi.ListWorkersResponse{Workers: workers}), "ListWorkersResponse", false
	})
	fc.handle("ReactorcideUi", "list-enrollment-tokens", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		name := "ci-runners"
		tokens := []csilapi.EnrollmentTokenSummary{{TokenId: "tok1", PoolId: "pool1", Name: &name, IsActive: true, CreatedAt: "2026-01-01T00:00:00Z"}}
		return csilapi.EncodeListEnrollmentTokensResponse(csilapi.ListEnrollmentTokensResponse{Tokens: tokens}), "ListEnrollmentTokensResponse", false
	})
}

func TestWorkersPage_RendersForCapableSession(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	withCapabilities(fc, csilapi.GetCapabilitiesResponse{ManageWorkers: true})
	withWorkersFakes(fc)
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodGet, "/app/workers", nil)
	rec := httptest.NewRecorder()
	h.withSession(h.WorkersPage)(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, body)
	}
	if !strings.Contains(body, "ci-pool") {
		t.Errorf("expected pool name in body, got: %s", body)
	}
	if !strings.Contains(body, "runner-1") {
		t.Errorf("expected worker hostname in body, got: %s", body)
	}
	if !strings.Contains(body, "ci-runners") {
		t.Errorf("expected enrollment token name in body, got: %s", body)
	}
	// Metadata-only: list-enrollment-tokens never carries a raw token value,
	// so there is nothing to leak here by construction, but assert the page
	// doesn't render a one-time-token banner on an ordinary page load.
	if strings.Contains(body, "shown once") {
		t.Errorf("did not expect the one-time-token banner on an ordinary page load, got: %s", body)
	}
}

// --- Pool CRUD ---

func TestPoolCreate_HappyPathHitsFakeWithFields(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	var seen csilapi.CreatePoolRequest
	fc.handle("ReactorcideUi", "create-pool", func(payload []byte, _ string, _ bool) ([]byte, string, bool) {
		req, err := csilapi.DecodeCreatePoolRequest(payload)
		if err != nil {
			return fakeServiceErrorPayload("bad_request", err.Error()), "ServiceError", true
		}
		seen = req
		return csilapi.EncodeCreatePoolResponse(csilapi.CreatePoolResponse{Pool: csilapi.WorkerPoolSummary{PoolId: "pool2", Name: req.Name}}), "CreatePoolResponse", false
	})
	h := newTestWebHandler(t, fc)

	form := strings.NewReader("name=ci-pool&description=CI+runners&org_id=org-1")
	req := httptest.NewRequest(http.MethodPost, "/app/workers/pools", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.withSession(h.PoolCreate)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if seen.Name != "ci-pool" || seen.OrgId == nil || *seen.OrgId != "org-1" {
		t.Errorf("fake coordinator did not receive expected fields: %+v", seen)
	}
}

func TestPoolCreate_MissingNameFlashesWithoutHittingFake(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	hit := false
	fc.handle("ReactorcideUi", "create-pool", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		hit = true
		return csilapi.EncodeCreatePoolResponse(csilapi.CreatePoolResponse{}), "CreatePoolResponse", false
	})
	h := newTestWebHandler(t, fc)

	form := strings.NewReader("name=")
	req := httptest.NewRequest(http.MethodPost, "/app/workers/pools", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.withSession(h.PoolCreate)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if hit {
		t.Error("expected create-pool not to be called for a blank name")
	}
	if !strings.Contains(rec.Header().Get("Location"), "err=") {
		t.Errorf("Location = %q, want an err flash", rec.Header().Get("Location"))
	}
}

func TestPoolDelete_HappyPathHitsFakeWithFields(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	var seen csilapi.DeletePoolRequest
	fc.handle("ReactorcideUi", "delete-pool", func(payload []byte, _ string, _ bool) ([]byte, string, bool) {
		req, err := csilapi.DecodeDeletePoolRequest(payload)
		if err != nil {
			return fakeServiceErrorPayload("bad_request", err.Error()), "ServiceError", true
		}
		seen = req
		return csilapi.EncodeDeletePoolResponse(csilapi.DeletePoolResponse{Deleted: true}), "DeletePoolResponse", false
	})
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodPost, "/app/workers/pools/pool1/delete", nil)
	req.SetPathValue("id", "pool1")
	rec := httptest.NewRecorder()
	h.withSession(h.PoolDelete)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if seen.PoolId != "pool1" {
		t.Errorf("fake coordinator did not receive expected pool id: %+v", seen)
	}
}

// --- Enrollment tokens: the one-time reveal ---

// TestEnrollmentTokenCreate_ShowsRawTokenOnceThenNeverAgain is the core
// secret-handling assertion for this feature: the raw token value must
// render in the direct response to the create POST (shown exactly once),
// and must never appear on a subsequent, ordinary page load — which only
// ever sees list-enrollment-tokens' metadata-only summaries.
func TestEnrollmentTokenCreate_ShowsRawTokenOnceThenNeverAgain(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	withCapabilities(fc, csilapi.GetCapabilitiesResponse{ManageWorkers: true})
	withWorkersFakes(fc)

	const rawToken = "rc-enroll-supersecretvalue-000111222"
	var seen csilapi.CreateEnrollmentTokenRequest
	fc.handle("ReactorcideUi", "create-enrollment-token", func(payload []byte, _ string, _ bool) ([]byte, string, bool) {
		req, err := csilapi.DecodeCreateEnrollmentTokenRequest(payload)
		if err != nil {
			return fakeServiceErrorPayload("bad_request", err.Error()), "ServiceError", true
		}
		seen = req
		name := "ci-runners"
		resp := csilapi.CreateEnrollmentTokenResponse{
			Token:   rawToken,
			Summary: csilapi.EnrollmentTokenSummary{TokenId: "tok-new", PoolId: req.PoolId, Name: &name, IsActive: true, CreatedAt: "2026-01-01T00:00:00Z"},
		}
		return csilapi.EncodeCreateEnrollmentTokenResponse(resp), "CreateEnrollmentTokenResponse", false
	})
	h := newTestWebHandler(t, fc)

	form := strings.NewReader("name=ci-runners")
	req := httptest.NewRequest(http.MethodPost, "/app/workers/pools/pool1/tokens", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "pool1")
	rec := httptest.NewRecorder()
	h.withSession(h.EnrollmentTokenCreate)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (direct render, not a redirect); body=%s", rec.Code, rec.Body.String())
	}
	if seen.PoolId != "pool1" || seen.Name == nil || *seen.Name != "ci-runners" {
		t.Errorf("fake coordinator did not receive expected fields: %+v", seen)
	}
	body := rec.Body.String()
	if !strings.Contains(body, rawToken) {
		t.Fatalf("expected the raw token to render exactly once in the create response, got: %s", body)
	}
	if strings.Contains(body, "shown once") == false {
		t.Errorf("expected the one-time-reveal warning banner, got: %s", body)
	}
	// SECURITY: the token must never be placed in a redirect Location
	// (server access logs, browser history, Referer headers).
	if loc := rec.Header().Get("Location"); strings.Contains(loc, rawToken) {
		t.Fatalf("raw token leaked into a redirect Location: %q", loc)
	}

	// A subsequent, ordinary page load only ever sees list-enrollment-tokens'
	// metadata-only summaries — the raw value must not reappear.
	req2 := httptest.NewRequest(http.MethodGet, "/app/workers", nil)
	rec2 := httptest.NewRecorder()
	h.withSession(h.WorkersPage)(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec2.Code, rec2.Body.String())
	}
	if strings.Contains(rec2.Body.String(), rawToken) {
		t.Fatalf("raw token leaked into a subsequent page render, got: %s", rec2.Body.String())
	}
}

func TestEnrollmentTokenCreate_ForbiddenWithoutManageWorkers(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	withCapabilities(fc, csilapi.GetCapabilitiesResponse{})
	hit := false
	fc.handle("ReactorcideUi", "create-enrollment-token", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		hit = true
		return csilapi.EncodeCreateEnrollmentTokenResponse(csilapi.CreateEnrollmentTokenResponse{Token: "should-not-be-generated"}), "CreateEnrollmentTokenResponse", false
	})
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodPost, "/app/workers/pools/pool1/tokens", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "pool1")
	rec := httptest.NewRecorder()
	h.withSession(h.EnrollmentTokenCreate)(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	if hit {
		t.Error("expected create-enrollment-token not to be called for an incapable session")
	}
}

// --- Worker status / drain ---

func TestWorkerStatusUpdate_HappyPathHitsFakeWithFields(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	var seen csilapi.SetWorkerStatusRequest
	fc.handle("ReactorcideUi", "set-worker-status", func(payload []byte, _ string, _ bool) ([]byte, string, bool) {
		req, err := csilapi.DecodeSetWorkerStatusRequest(payload)
		if err != nil {
			return fakeServiceErrorPayload("bad_request", err.Error()), "ServiceError", true
		}
		seen = req
		return csilapi.EncodeSetWorkerStatusResponse(csilapi.SetWorkerStatusResponse{Worker: csilapi.WorkerSummary{WorkerId: req.WorkerId, Status: req.Status}}), "SetWorkerStatusResponse", false
	})
	h := newTestWebHandler(t, fc)

	form := strings.NewReader("status=quarantined")
	req := httptest.NewRequest(http.MethodPost, "/app/workers/worker/w1/status", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "w1")
	rec := httptest.NewRecorder()
	h.withSession(h.WorkerStatusUpdate)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if seen.WorkerId != "w1" || seen.Status != "quarantined" {
		t.Errorf("fake coordinator did not receive expected fields: %+v", seen)
	}
}

func TestWorkerDrain_HappyPathHitsFakeWithFields(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	var seen csilapi.DrainWorkerRequest
	fc.handle("ReactorcideUi", "drain-worker", func(payload []byte, _ string, _ bool) ([]byte, string, bool) {
		req, err := csilapi.DecodeDrainWorkerRequest(payload)
		if err != nil {
			return fakeServiceErrorPayload("bad_request", err.Error()), "ServiceError", true
		}
		seen = req
		return csilapi.EncodeDrainWorkerResponse(csilapi.DrainWorkerResponse{Worker: csilapi.WorkerSummary{WorkerId: req.WorkerId, Status: "quarantined"}}), "DrainWorkerResponse", false
	})
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodPost, "/app/workers/worker/w1/drain", nil)
	req.SetPathValue("id", "w1")
	rec := httptest.NewRecorder()
	h.withSession(h.WorkerDrain)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if seen.WorkerId != "w1" {
		t.Errorf("fake coordinator did not receive expected worker id: %+v", seen)
	}
}

// --- Queues ---

func TestQueuesPage_ForbiddenForNonGlobalAdmin(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "local-rp", false, true)
	// Org-admin-scoped ManageWorkers, but not a global admin: queues have no
	// owning-org concept, so the coordinator's queue ops require a true
	// global admin regardless of this capability.
	withCapabilities(fc, csilapi.GetCapabilitiesResponse{ManageWorkers: true})
	withAuthenticatedSession(fc, "org-admin-token", csilapi.AuthenticatedIdentity{UserId: "org1", IsGlobalAdmin: false})
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodGet, "/app/workers/queues", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "org-admin-token"})
	rec := httptest.NewRecorder()
	h.withSession(h.QueuesPage)(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

func withQueuesFakes(fc *fakeCoordinator, queues []csilapi.QueueSummary) {
	fc.handle("ReactorcideUi", "list-queues", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		return csilapi.EncodeListQueuesResponse(csilapi.ListQueuesResponse{Queues: queues}), "ListQueuesResponse", false
	})
}

func TestQueuesPage_RendersForGlobalAdminWithCancelWarningAndNoDeleteOnDefault(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "local-rp", false, true)
	withCapabilities(fc, csilapi.GetCapabilitiesResponse{ManageWorkers: true, IsGlobalAdmin: true})
	withAuthenticatedSession(fc, "admin-token", csilapi.AuthenticatedIdentity{UserId: "u1", IsGlobalAdmin: true})
	withQueuesFakes(fc, []csilapi.QueueSummary{
		{QueueId: "q-default", QueueUuid: "uuid-default", DisplayName: "default", IsDefault: true, BacklogCount: 0},
		{QueueId: "q-linux", QueueUuid: "uuid-linux", DisplayName: "linux-amd64", IsDefault: false, BacklogCount: 3,
			Characteristics: []csilapi.CharacteristicEntry{{Key: "os", Value: "linux"}}},
	})
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodGet, "/app/workers/queues", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "admin-token"})
	rec := httptest.NewRecorder()
	h.withSession(h.QueuesPage)(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, body)
	}
	if !strings.Contains(body, "linux-amd64") {
		t.Errorf("expected non-default queue name, got: %s", body)
	}
	if !strings.Contains(body, "/app/workers/queues/q-linux/delete") {
		t.Errorf("expected a delete control for the non-default queue, got: %s", body)
	}
	if strings.Contains(body, "/app/workers/queues/q-default/delete") {
		t.Errorf("did not expect a delete control for the default queue, got: %s", body)
	}
	if !strings.Contains(body, "CANCELS every in-flight job") {
		t.Errorf("expected the in-flight-cancel warning on the delete control, got: %s", body)
	}
}

func TestQueueRename_HappyPathHitsFakeWithFields(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	var seen csilapi.RenameQueueRequest
	fc.handle("ReactorcideUi", "rename-queue", func(payload []byte, _ string, _ bool) ([]byte, string, bool) {
		req, err := csilapi.DecodeRenameQueueRequest(payload)
		if err != nil {
			return fakeServiceErrorPayload("bad_request", err.Error()), "ServiceError", true
		}
		seen = req
		return csilapi.EncodeRenameQueueResponse(csilapi.RenameQueueResponse{Queue: csilapi.QueueSummary{QueueId: req.QueueId, DisplayName: req.DisplayName}}), "RenameQueueResponse", false
	})
	h := newTestWebHandler(t, fc)

	form := strings.NewReader("display_name=" + "renamed-queue")
	req := httptest.NewRequest(http.MethodPost, "/app/workers/queues/q1/rename", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "q1")
	rec := httptest.NewRecorder()
	h.withSession(h.QueueRename)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if seen.QueueId != "q1" || seen.DisplayName != "renamed-queue" {
		t.Errorf("fake coordinator did not receive expected fields: %+v", seen)
	}
}

func TestQueueDelete_HappyPathHitsFakeAndFlashesCancelledCount(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	var seen csilapi.DeleteQueueRequest
	fc.handle("ReactorcideUi", "delete-queue", func(payload []byte, _ string, _ bool) ([]byte, string, bool) {
		req, err := csilapi.DecodeDeleteQueueRequest(payload)
		if err != nil {
			return fakeServiceErrorPayload("bad_request", err.Error()), "ServiceError", true
		}
		seen = req
		resp := csilapi.DeleteQueueResponse{Deleted: true, CancelledJobIds: []string{"job-1", "job-2"}}
		return csilapi.EncodeDeleteQueueResponse(resp), "DeleteQueueResponse", false
	})
	h := newTestWebHandler(t, fc)

	req := httptest.NewRequest(http.MethodPost, "/app/workers/queues/q1/delete", nil)
	req.SetPathValue("id", "q1")
	rec := httptest.NewRecorder()
	h.withSession(h.QueueDelete)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if seen.QueueId != "q1" {
		t.Errorf("fake coordinator did not receive expected queue id: %+v", seen)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "2+in-flight") && !strings.Contains(loc, "2%20in-flight") {
		t.Errorf("Location = %q, want the cancelled-job count in the flash message", loc)
	}
}

func TestQueueCreate_InvalidCharacteristicsFlashesWithoutHittingFake(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	hit := false
	fc.handle("ReactorcideUi", "create-queue", func(_ []byte, _ string, _ bool) ([]byte, string, bool) {
		hit = true
		return csilapi.EncodeCreateQueueResponse(csilapi.CreateQueueResponse{}), "CreateQueueResponse", false
	})
	h := newTestWebHandler(t, fc)

	form := strings.NewReader("display_name=bad-queue&characteristics=" + "os%3Dlinux%2C%3Dbadkey")
	req := httptest.NewRequest(http.MethodPost, "/app/workers/queues", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.withSession(h.QueueCreate)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if hit {
		t.Error("expected create-queue not to be called for an empty characteristics key")
	}
	if !strings.Contains(rec.Header().Get("Location"), "err=") {
		t.Errorf("Location = %q, want an err flash", rec.Header().Get("Location"))
	}
}

func TestQueueCreate_HappyPathHitsFakeWithParsedCharacteristics(t *testing.T) {
	fc := newFakeCoordinator()
	withAuthMode(fc, "none", false, true)
	var seen csilapi.CreateQueueRequest
	fc.handle("ReactorcideUi", "create-queue", func(payload []byte, _ string, _ bool) ([]byte, string, bool) {
		req, err := csilapi.DecodeCreateQueueRequest(payload)
		if err != nil {
			return fakeServiceErrorPayload("bad_request", err.Error()), "ServiceError", true
		}
		seen = req
		return csilapi.EncodeCreateQueueResponse(csilapi.CreateQueueResponse{Queue: csilapi.QueueSummary{QueueId: "q9", DisplayName: derefStrTest(req.DisplayName)}}), "CreateQueueResponse", false
	})
	h := newTestWebHandler(t, fc)

	form := strings.NewReader("display_name=linux-amd64&characteristics=" + "os%3Dlinux%2C+arch%3Damd64")
	req := httptest.NewRequest(http.MethodPost, "/app/workers/queues", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.withSession(h.QueueCreate)(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if seen.DisplayName == nil || *seen.DisplayName != "linux-amd64" {
		t.Fatalf("fake coordinator did not receive expected display name: %+v", seen)
	}
	if len(seen.Characteristics) != 2 {
		t.Fatalf("expected 2 parsed characteristics, got %+v", seen.Characteristics)
	}
	got := map[string]interface{}{}
	for _, c := range seen.Characteristics {
		got[c.Key] = c.Value
	}
	if got["os"] != "linux" || got["arch"] != "amd64" {
		t.Errorf("unexpected parsed characteristics: %+v", got)
	}
}

func derefStrTest(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
