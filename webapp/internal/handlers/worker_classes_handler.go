package handlers

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/catalystcommunity/reactorcide/webapp/internal/uiclient/csilapi"
)

type workerClassPoolRow struct {
	Pool    csilapi.WorkerPoolSummary
	Granted bool
}

type workerClassView struct {
	Class csilapi.WorkerClassSummary
	Pools []workerClassPoolRow
}

func workerClassBackTo(organization string) string {
	return "/app/workers/classes?organization=" + url.QueryEscape(organization)
}

func (h *WebHandler) WorkerClassesPage(w http.ResponseWriter, r *http.Request) {
	si := h.sessionInfo(r)
	if !si.Caps.ManageWorkers || h.uiClients == nil {
		h.renderError(w, r, http.StatusForbidden, "You do not have permission to manage worker classes", nil)
		return
	}
	orgs := h.listOrgsForSelector(r)
	organization := strings.TrimSpace(r.URL.Query().Get("organization"))
	var orgID string
	for _, org := range orgs {
		if organization == "" && org.IsDefault {
			organization = org.Name
		}
		if org.Name == organization {
			orgID = org.OrgId
		}
	}
	if organization == "" && len(orgs) > 0 {
		organization, orgID = orgs[0].Name, orgs[0].OrgId
	}
	if organization == "" || orgID == "" {
		h.renderError(w, r, http.StatusForbidden, "No manageable organization is available", nil)
		return
	}
	classes, err := h.uiClients.Ui.ListWorkerClasses(h.authContext(r), csilapi.ListWorkerClassesRequest{Organization: organization})
	if err != nil {
		h.renderServiceError(w, r, err)
		return
	}
	poolReq := csilapi.ListPoolsRequest{OrgId: &orgID}
	if si.IsGlobalAdmin {
		poolReq.OrgId = nil
	}
	poolResp, err := h.uiClients.Ui.ListPools(h.authContext(r), poolReq)
	if err != nil {
		h.renderServiceError(w, r, err)
		return
	}
	pools := make([]csilapi.WorkerPoolSummary, 0, len(poolResp.Pools))
	for _, pool := range poolResp.Pools {
		if pool.OrgId == nil || *pool.OrgId == orgID {
			pools = append(pools, pool)
		}
	}
	views := make([]workerClassView, len(classes.WorkerClasses))
	for i, class := range classes.WorkerClasses {
		granted := make(map[string]bool, len(class.PoolIds))
		for _, poolID := range class.PoolIds {
			granted[poolID] = true
		}
		rows := make([]workerClassPoolRow, len(pools))
		for j, pool := range pools {
			rows[j] = workerClassPoolRow{Pool: pool, Granted: granted[pool.PoolId]}
		}
		views[i] = workerClassView{Class: class, Pools: rows}
	}
	msg, errMsg := flashFromQuery(r)
	h.render(w, r, "worker_classes.html", map[string]interface{}{
		"Title": "Worker Classes", "Organization": organization, "Orgs": orgs,
		"Classes": views, "FormMsg": msg, "FormError": errMsg,
	})
}

func (h *WebHandler) WorkerClassPut(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form submission", nil)
		return
	}
	organization, name := formTrim(r, "organization"), formTrim(r, "name")
	back := workerClassBackTo(organization)
	if organization == "" || name == "" || h.uiClients == nil {
		h.redirectFlash(w, r, back, "organization and name are required", true)
		return
	}
	request := csilapi.PutWorkerClassRequest{Organization: organization, WorkerClass: csilapi.WorkerClassSummary{Name: name, Protected: formCheckbox(r, "protected")}}
	if _, err := h.uiClients.Ui.PutWorkerClass(h.authContext(r), request); err != nil {
		h.handleFormServiceError(w, r, err, back)
		return
	}
	h.redirectFlash(w, r, back, "Worker class saved", false)
}

func (h *WebHandler) WorkerClassDelete(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form submission", nil)
		return
	}
	organization, name := formTrim(r, "organization"), r.PathValue("name")
	back := workerClassBackTo(organization)
	if _, err := h.uiClients.Ui.DeleteWorkerClass(h.authContext(r), csilapi.DeleteWorkerClassRequest{Organization: organization, Name: name}); err != nil {
		h.handleFormServiceError(w, r, err, back)
		return
	}
	h.redirectFlash(w, r, back, "Worker class deleted", false)
}

func (h *WebHandler) WorkerClassPoolSet(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, http.StatusBadRequest, "Invalid form submission", nil)
		return
	}
	organization := formTrim(r, "organization")
	back := workerClassBackTo(organization)
	request := csilapi.SetWorkerClassPoolRequest{Organization: organization, WorkerClass: r.PathValue("name"), PoolId: formTrim(r, "pool_id"), Granted: formTrim(r, "granted") == "true"}
	if _, err := h.uiClients.Ui.SetWorkerClassPool(h.authContext(r), request); err != nil {
		h.handleFormServiceError(w, r, err, back)
		return
	}
	h.redirectFlash(w, r, back, "Worker class pool mapping updated", false)
}
