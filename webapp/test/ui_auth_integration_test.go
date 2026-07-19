package test

// UI-auth integration tests (UI_AUTH_PLAN.md Task J) driving the real
// webapp server against the real coordinator subprocess started by
// setup_test.go's TestMain (REACTORCIDE_UI_AUTH_MODE unset -> "none",
// REACTORCIDE_BOOTSTRAP_ADMIN_TOKEN=testBootstrapAdminToken). These are the
// highest-value end-to-end flows; the unit fakes (internal/handlers's
// fake_coordinator_test.go) cover template/handler breadth.

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
)

// TestUIAuthModeNoneLoginDisabledAndAnonymousCancel covers: with
// REACTORCIDE_UI_AUTH_MODE=none (the default), the login page says so, and
// the job-detail cancel button's POST works with no session cookie at all
// (anonymous graceful cancel is allowed in mode none — see
// UI_AUTH_PLAN.md's permission matrix).
func TestUIAuthModeNoneLoginDisabledAndAnonymousCancel(t *testing.T) {
	resp, err := http.Get(webBaseURL + "/app/login")
	if err != nil {
		t.Fatalf("GET /app/login: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /app/login: expected 200, got %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "Login is disabled") {
		t.Errorf("login page should say login is disabled in mode none, got: %s", body)
	}

	jobID := insertTestJob(t, "ui-auth-anon-cancel")

	// No cookies at all: a plain, unauthenticated client.
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	cancelResp, err := client.Post(webBaseURL+"/app/jobs/"+jobID+"/cancel", "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatalf("POST /app/jobs/%s/cancel: %v", jobID, err)
	}
	defer cancelResp.Body.Close()
	if cancelResp.StatusCode != http.StatusFound {
		b, _ := io.ReadAll(cancelResp.Body)
		t.Fatalf("anonymous cancel: expected 302 redirect, got %d: %s", cancelResp.StatusCode, b)
	}

	var status string
	if err := testDB.QueryRow(`SELECT status FROM jobs WHERE job_id = $1`, jobID).Scan(&status); err != nil {
		t.Fatalf("querying job status: %v", err)
	}
	if status != "cancelled" {
		t.Errorf("job status = %q, want %q after anonymous cancel", status, "cancelled")
	}
}

// TestUIAuthBootstrapLoginCreateProject covers: logging in via
// /app/bootstrap with the configured bootstrap token, seeing the admin nav
// link (proves the session resolves as global admin), and creating a
// project through the real UI form, then seeing it in the project list.
func TestUIAuthBootstrapLoginCreateProject(t *testing.T) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New: %v", err)
	}
	client := &http.Client{Jar: jar}

	form := url.Values{"token": {testBootstrapAdminToken}}
	resp, err := client.PostForm(webBaseURL+"/app/bootstrap", form)
	if err != nil {
		t.Fatalf("POST /app/bootstrap: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bootstrap+redirect-follow to /app/: expected 200, got %d: %s", resp.StatusCode, body)
	}

	// The client followed the post-bootstrap redirect to /app/ with the
	// session cookie attached (http.Client.Jar carries it across the
	// redirect); the layout's nav only renders the Admin link for a global
	// admin session (see internal/templates/layout.html).
	if !strings.Contains(string(body), `href="/app/admin"`) {
		t.Errorf("bootstrap-admin session should see the Admin nav link on /app/, got: %s", body)
	}

	var bootstrapUserID string
	if err := testDB.QueryRow(`SELECT user_id FROM users WHERE username = 'bootstrap-admin'`).Scan(&bootstrapUserID); err != nil {
		t.Fatalf("querying bootstrap-admin user: %v", err)
	}

	const projectName = "ui-auth-bootstrap-project"
	createForm := url.Values{
		"org_id":   {bootstrapUserID},
		"name":     {projectName},
		"repo_url": {"https://github.com/uiauth-test/bootstrap-project.git"},
	}
	createResp, err := client.PostForm(webBaseURL+"/app/projects", createForm)
	if err != nil {
		t.Fatalf("POST /app/projects: %v", err)
	}
	createBody, _ := io.ReadAll(createResp.Body)
	createResp.Body.Close()
	if createResp.StatusCode != http.StatusOK {
		t.Fatalf("create project + redirect-follow: expected 200, got %d: %s", createResp.StatusCode, createBody)
	}

	listResp, err := client.Get(webBaseURL + "/app/projects")
	if err != nil {
		t.Fatalf("GET /app/projects: %v", err)
	}
	defer listResp.Body.Close()
	listBody, _ := io.ReadAll(listResp.Body)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /app/projects: expected 200, got %d", listResp.StatusCode)
	}
	if !strings.Contains(string(listBody), projectName) {
		t.Errorf("projects list should contain the newly created project %q, got: %s", projectName, listBody)
	}
}
