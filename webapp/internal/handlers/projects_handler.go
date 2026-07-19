package handlers

import (
	"net/http"
	"strconv"

	"github.com/catalystcommunity/reactorcide/webapp/internal/uiclient/csilapi"
)

// ProjectsList renders GET /app/projects: every visibility-filtered project
// (optionally scoped to one org via ?org_id=), with a "New project" button
// only when the caller's capabilities allow creating one.
func (h *WebHandler) ProjectsList(w http.ResponseWriter, r *http.Request) {
	si := h.sessionInfo(r)
	if h.uiClients == nil {
		h.render(w, r, "projects_list.html", map[string]interface{}{
			"Title":    "Projects",
			"Projects": []csilapi.ProjectSummary{},
		})
		return
	}

	req := csilapi.ListProjectsRequest{}
	if orgID := r.URL.Query().Get("org_id"); orgID != "" {
		req.OrgId = &orgID
	}
	resp, err := h.uiClients.Ui.ListProjects(h.authContext(r), req)
	if err != nil {
		h.renderServiceError(w, r, err)
		return
	}

	msg, errMsg := flashFromQuery(r)
	data := map[string]interface{}{
		"Title":     "Projects",
		"Projects":  resp.Projects,
		"CanCreate": si.Caps.CreateProject,
		"FormMsg":   msg,
		"FormError": errMsg,
	}
	h.render(w, r, "projects_list.html", data)
}

// ProjectNewForm renders GET /app/projects/new: gated on the CreateProject
// capability (buttons/forms the caller lacks capability for are not
// rendered — here that means the whole page, since it has nothing else to
// show). Global admins get an org selector (list-orgs); everyone else
// creates in their own org (user_id IS the org id).
func (h *WebHandler) ProjectNewForm(w http.ResponseWriter, r *http.Request) {
	si := h.sessionInfo(r)
	if !si.Caps.CreateProject {
		h.renderError(w, r, http.StatusForbidden, "You do not have permission to create a project", nil)
		return
	}

	_, errMsg := flashFromQuery(r)
	data := map[string]interface{}{
		"Title":     "New project",
		"OwnOrgID":  si.UserID,
		"FormError": errMsg,
	}
	if si.IsGlobalAdmin {
		data["Orgs"] = h.listOrgsForSelector(r)
	}
	h.render(w, r, "project_new.html", data)
}

// ProjectCreate handles POST /app/projects. name/repo_url/org_id are
// required; is_private is only sent (as an explicit true) when the checkbox
// is checked — leaving it unchecked omits the field entirely so the
// coordinator's new_projects_private global default applies, per
// UI_AUTH_PLAN.md task I's brief.
func (h *WebHandler) ProjectCreate(w http.ResponseWriter, r *http.Request) {
	si := h.sessionInfo(r)
	if !si.Caps.CreateProject {
		h.renderError(w, r, http.StatusForbidden, "You do not have permission to create a project", nil)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form submission", nil)
		return
	}

	orgID := formTrim(r, "org_id")
	if !si.IsGlobalAdmin {
		orgID = si.UserID
	}
	name := formTrim(r, "name")
	repoURL := formTrim(r, "repo_url")
	if orgID == "" || name == "" || repoURL == "" {
		h.redirectFlash(w, r, "/app/projects/new", "org, name, and repo URL are required", true)
		return
	}

	req := csilapi.CreateProjectRequest{
		OrgId:             orgID,
		Name:              name,
		Description:       formOptionalPtr(r, "description"),
		RepoUrl:           repoURL,
		TargetBranches:    formStringList(r, "target_branches"),
		AllowedEventTypes: formStringList(r, "allowed_event_types"),
	}
	if formCheckbox(r, "is_private") {
		v := true
		req.IsPrivate = &v
	}

	if h.uiClients == nil {
		h.redirectFlash(w, r, "/app/projects/new", "management is not available", true)
		return
	}
	resp, err := h.uiClients.Ui.CreateProject(h.authContext(r), req)
	if err != nil {
		h.handleFormServiceError(w, r, err, "/app/projects/new")
		return
	}
	h.redirectFlash(w, r, "/app/projects/"+resp.Project.ProjectId, "Project created", false)
}

// ProjectDetail renders GET /app/projects/{id}: project settings (gated on
// ManageProjectSettings), delete (gated on DeleteProject), and the
// webhook-secret/vcs-credential security tabs (each gated on their own
// capability, listed only when the caller can manage them).
func (h *WebHandler) ProjectDetail(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if projectID == "" {
		http.NotFound(w, r)
		return
	}
	if h.uiClients == nil {
		h.renderError(w, r, http.StatusServiceUnavailable, "Management is not available", nil)
		return
	}

	resp, err := h.uiClients.Ui.GetProject(h.authContext(r), csilapi.GetProjectRequest{ProjectId: projectID})
	if err != nil {
		h.renderServiceError(w, r, err)
		return
	}
	project := resp.Project
	caps := h.capabilitiesForProject(r, &projectID)

	data := map[string]interface{}{
		"Title":   project.Name,
		"Project": project,
		"Caps":    caps,
	}
	msg, errMsg := flashFromQuery(r)
	data["FormMsg"] = msg
	data["FormError"] = errMsg

	if caps.ManageWebhookSecrets {
		if wh, err := h.uiClients.Ui.ListWebhookSecrets(h.authContext(r), csilapi.ListWebhookSecretsRequest{ProjectId: projectID}); err == nil {
			data["WebhookSecrets"] = wh.Secrets
		}
	}
	if caps.ManageVcsCredentials {
		if vc, err := h.uiClients.Ui.ListVcsCredentials(h.authContext(r), csilapi.ListVcsCredentialsRequest{ProjectId: projectID}); err == nil {
			data["VcsCredentials"] = vc.Credentials
		}
	}

	h.render(w, r, "project_detail.html", data)
}

// ProjectSettingsUpdate handles POST /app/projects/{id}/settings. Unlike
// project creation's "omit to default" is_private handling, this form
// always sends explicit is_private/enabled values — it's editing the
// project's current state, not choosing a creation-time default.
func (h *WebHandler) ProjectSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if projectID == "" {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form submission", nil)
		return
	}
	backTo := "/app/projects/" + projectID

	name := formOptionalPtr(r, "name")
	description := formOptionalPtr(r, "description")
	runnerImage := formOptionalPtr(r, "default_runner_image")
	jobCommand := formOptionalPtr(r, "default_job_command")
	queueName := formOptionalPtr(r, "default_queue_name")
	isPrivate := formCheckbox(r, "is_private")
	enabled := formCheckbox(r, "enabled")

	req := csilapi.UpdateProjectRequest{
		ProjectId:          projectID,
		Name:               name,
		Description:        description,
		IsPrivate:          &isPrivate,
		Enabled:            &enabled,
		TargetBranches:     formStringList(r, "target_branches"),
		AllowedEventTypes:  formStringList(r, "allowed_event_types"),
		DefaultRunnerImage: runnerImage,
		DefaultJobCommand:  jobCommand,
		DefaultQueueName:   queueName,
	}
	if v := formTrim(r, "default_timeout_seconds"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			req.DefaultTimeoutSeconds = &n
		} else {
			h.redirectFlash(w, r, backTo, "default timeout seconds must be a number", true)
			return
		}
	}

	if h.uiClients == nil {
		h.redirectFlash(w, r, backTo, "management is not available", true)
		return
	}
	if _, err := h.uiClients.Ui.UpdateProject(h.authContext(r), req); err != nil {
		h.handleFormServiceError(w, r, err, backTo)
		return
	}
	h.redirectFlash(w, r, backTo, "Settings updated", false)
}

// ProjectDelete handles POST /app/projects/{id}/delete. The typed-name
// confirmation (deliverable #1's "confirm via JS confirm() + a typed-name
// confirmation input") is a webapp-side UX safety net only — the coordinator
// is the sole authorizer of the delete itself — so it's checked against the
// project name the detail page echoed back in a hidden field, with no extra
// round trip.
func (h *WebHandler) ProjectDelete(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if projectID == "" {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form submission", nil)
		return
	}
	backTo := "/app/projects/" + projectID

	expected := formTrim(r, "expected_name")
	confirmed := formTrim(r, "confirm_name")
	if expected == "" || confirmed != expected {
		h.redirectFlash(w, r, backTo, "confirmation name did not match; project was not deleted", true)
		return
	}

	if h.uiClients == nil {
		h.redirectFlash(w, r, backTo, "management is not available", true)
		return
	}
	if _, err := h.uiClients.Ui.DeleteProject(h.authContext(r), csilapi.DeleteProjectRequest{ProjectId: projectID}); err != nil {
		h.handleFormServiceError(w, r, err, backTo)
		return
	}
	h.redirectFlash(w, r, "/app/projects", "Project deleted", false)
}
