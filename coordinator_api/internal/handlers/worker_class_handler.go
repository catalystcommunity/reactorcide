package handlers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

type workerClassStore interface {
	GetOrganizationByName(context.Context, string) (*models.Organization, error)
	CreateWorkerClass(context.Context, *models.WorkerClass) error
	GetWorkerClass(context.Context, string, string) (*models.WorkerClass, error)
	ListWorkerClasses(context.Context, string) ([]models.WorkerClass, error)
	UpdateWorkerClass(context.Context, *models.WorkerClass) error
	DeleteWorkerClass(context.Context, string) error
	GrantWorkerClassPool(context.Context, string, string) error
	RevokeWorkerClassPool(context.Context, string, string) error
	ListPoolsForWorkerClass(context.Context, string) ([]models.WorkerPool, error)
	GetWorkerPoolByID(context.Context, string) (*models.WorkerPool, error)
}

type WorkerClassHandler struct {
	BaseHandler
	store    workerClassStore
	appStore store.Store
}

func NewWorkerClassHandler(appStore store.Store) *WorkerClassHandler {
	value, _ := appStore.(workerClassStore)
	return &WorkerClassHandler{store: value, appStore: appStore}
}

func (h *WorkerClassHandler) ServeHTTP(w http.ResponseWriter, r *http.Request, orgName, className, poolID string) {
	if h.store == nil {
		h.respondWithError(w, 503, store.ErrServiceUnavailable)
		return
	}
	org, err := h.store.GetOrganizationByName(r.Context(), orgName)
	if err != nil {
		h.respondWithError(w, 404, store.ErrNotFound)
		return
	}
	if !principalAllowsOrg(r.Context(), h.appStore, org.OrgID, tokencaps.WorkersManage) {
		h.respondWithError(w, 403, store.ErrForbidden)
		return
	}
	if className == "" {
		if r.Method == http.MethodGet {
			items, err := h.store.ListWorkerClasses(r.Context(), org.OrgID)
			if err != nil {
				h.respondWithError(w, 500, err)
				return
			}
			h.respondWithJSON(w, 200, map[string]any{"worker_classes": items})
			return
		}
		if r.Method == http.MethodPost {
			var class models.WorkerClass
			if json.NewDecoder(r.Body).Decode(&class) != nil {
				h.respondWithError(w, 400, store.ErrInvalidInput)
				return
			}
			class.OrgID = org.OrgID
			if err := h.store.CreateWorkerClass(r.Context(), &class); err != nil {
				h.respondWithError(w, 400, err)
				return
			}
			audit.Record(r.Context(), h.appStore, org.OrgID, "worker_class.create", "worker_class", class.ClassID, models.JSONB{"name": class.Name, "protected": class.Protected})
			h.respondWithJSON(w, 201, class)
			return
		}
		http.Error(w, "Method not allowed", 405)
		return
	}
	class, err := h.store.GetWorkerClass(r.Context(), org.OrgID, className)
	if err != nil {
		h.respondWithError(w, 404, err)
		return
	}
	if poolID != "" {
		pool, poolErr := h.store.GetWorkerPoolByID(r.Context(), poolID)
		if poolErr != nil {
			h.respondWithError(w, 404, poolErr)
			return
		}
		if pool.OrgID == nil && !principalAllowsGlobal(r.Context(), h.appStore, tokencaps.WorkersManage) {
			h.respondWithError(w, 403, store.ErrForbidden)
			return
		}
		if r.Method == http.MethodPut {
			err = h.store.GrantWorkerClassPool(r.Context(), class.ClassID, poolID)
		} else if r.Method == http.MethodDelete {
			err = h.store.RevokeWorkerClassPool(r.Context(), class.ClassID, poolID)
		} else {
			http.Error(w, "Method not allowed", 405)
			return
		}
		if err != nil {
			h.respondWithError(w, 400, err)
			return
		}
		action := "worker_class.pool_grant"
		if r.Method == http.MethodDelete {
			action = "worker_class.pool_revoke"
		}
		audit.Record(r.Context(), h.appStore, org.OrgID, action, "worker_class", class.ClassID, models.JSONB{"pool_id": poolID})
		w.WriteHeader(204)
		return
	}
	switch r.Method {
	case http.MethodGet:
		pools, err := h.store.ListPoolsForWorkerClass(r.Context(), class.ClassID)
		if err != nil {
			h.respondWithError(w, 500, err)
			return
		}
		h.respondWithJSON(w, 200, map[string]any{"worker_class": class, "pools": pools})
	case http.MethodPatch, http.MethodPut:
		var update struct {
			Protected bool `json:"protected"`
		}
		if json.NewDecoder(r.Body).Decode(&update) != nil {
			h.respondWithError(w, 400, store.ErrInvalidInput)
			return
		}
		class.Protected = update.Protected
		if err := h.store.UpdateWorkerClass(r.Context(), class); err != nil {
			h.respondWithError(w, 400, err)
			return
		}
		audit.Record(r.Context(), h.appStore, org.OrgID, "worker_class.update", "worker_class", class.ClassID, models.JSONB{"protected": class.Protected})
		h.respondWithJSON(w, 200, class)
	case http.MethodDelete:
		if err := h.store.DeleteWorkerClass(r.Context(), class.ClassID); err != nil {
			h.respondWithError(w, 400, err)
			return
		}
		audit.Record(r.Context(), h.appStore, org.OrgID, "worker_class.delete", "worker_class", class.ClassID, models.JSONB{"name": class.Name})
		w.WriteHeader(204)
	default:
		http.Error(w, "Method not allowed", 405)
	}
}
