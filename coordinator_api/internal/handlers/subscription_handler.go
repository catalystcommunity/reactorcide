package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/outboundevents"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/principals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

// subscriptionRoutes serves the outbound event subscription REST API
// (docs/outbound-events.md). Every route works with the admin API token.
//
//	GET|POST   /api/v1/organizations/ORG/subscriptions[?project=ID]
//	GET|PATCH|DELETE /api/v1/organizations/ORG/subscriptions/ID
//	POST       /api/v1/organizations/ORG/subscriptions/ID/test
//	POST       /api/v1/organizations/ORG/subscriptions/ID/replay-dead-letters
//	GET        /api/v1/organizations/ORG/subscriptions/ID/deliveries[?status=&limit=]
//	GET        /api/v1/organizations/ORG/subscriptions/ID/deliveries/DELIVERY
//	POST       /api/v1/organizations/ORG/subscriptions/ID/deliveries/DELIVERY/replay
//
// An org subscription (and an owner_group subscription with no project)
// needs an org admin (organizations:manage). A project or workflow
// subscription needs a project owner (projects:manage).
type subscriptionRoutes struct {
	BaseHandler
	store   store.Store
	manager *outboundevents.Manager
}

func newSubscriptionRoutes(appStore store.Store, manager *outboundevents.Manager) *subscriptionRoutes {
	return &subscriptionRoutes{store: appStore, manager: manager}
}

// SubscriptionView is the REST form of a subscription. The signing secret
// is only in the create and rotate answers.
type SubscriptionView struct {
	models.EventSubscription
	OwnerActive   bool   `json:"owner_active"`
	WebhookURL    string `json:"webhook_url"`
	SigningSecret string `json:"signing_secret,omitempty"`
}

type subscriptionCreateRequest struct {
	Name               string   `json:"name"`
	Description        string   `json:"description"`
	Scope              string   `json:"scope"`
	ProjectID          *string  `json:"project_id"`
	WorkflowSecurityID string   `json:"workflow_security_id"`
	OwnerGroup         string   `json:"owner_group"`
	EventTypes         []string `json:"event_types"`
	RefClassesAny      []string `json:"ref_classes_any"`
	Results            []string `json:"results"`
	ActorKinds         []string `json:"actor_kinds"`
	FailureClasses     []string `json:"failure_classes"`
	SinkType           string   `json:"sink_type"`
	WebhookURL         string   `json:"webhook_url"`
	MaxAttempts        int      `json:"max_attempts"`
	Enabled            *bool    `json:"enabled"`
	SigningSecret      string   `json:"signing_secret"`
	OwnerPrincipalID   string   `json:"owner_principal_id"`
}

type subscriptionPatchRequest struct {
	Description    *string   `json:"description"`
	EventTypes     *[]string `json:"event_types"`
	RefClassesAny  *[]string `json:"ref_classes_any"`
	Results        *[]string `json:"results"`
	ActorKinds     *[]string `json:"actor_kinds"`
	FailureClasses *[]string `json:"failure_classes"`
	WebhookURL     *string   `json:"webhook_url"`
	MaxAttempts    *int      `json:"max_attempts"`
	Enabled        *bool     `json:"enabled"`
	RotateSecret   bool      `json:"rotate_secret"`
	SigningSecret  string    `json:"signing_secret"`
}

// DeliveryView is one delivery with its attempts (on the detail route).
type DeliveryView struct {
	models.EventDelivery
	Attempts []models.EventDeliveryAttempt `json:"attempt_log,omitempty"`
}

func subscriptionView(sub *models.EventSubscription, secret string) SubscriptionView {
	return SubscriptionView{EventSubscription: *sub, OwnerActive: sub.OwnerActive, WebhookURL: sub.WebhookURL(), SigningSecret: secret}
}

// serveOrg handles ORG/subscriptions[...]. It returns false for other paths.
func (s *subscriptionRoutes) serveOrg(w http.ResponseWriter, r *http.Request, path string,
	transactionMiddleware, authMiddleware func(http.Handler) http.Handler) bool {
	parts := strings.Split(strings.TrimSuffix(path, "/"), "/")
	if len(parts) < 2 || parts[1] != "subscriptions" {
		return false
	}
	transactionMiddleware(authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.serve(w, r, parts[0], parts[2:])
	}))).ServeHTTP(w, r)
	return true
}

func (s *subscriptionRoutes) events(w http.ResponseWriter) (store.OutboundEventStore, bool) {
	events, ok := s.store.(store.OutboundEventStore)
	if !ok || s.manager == nil {
		s.respondWithError(w, http.StatusNotImplemented, errors.New("outbound events are not supported by this coordinator"))
		return nil, false
	}
	return events, true
}

// callerPrincipalID is the principal that the request acts as.
func callerPrincipalID(ctx context.Context) string {
	if id := principals.ActorOf(ctx).PrincipalID; id != "" {
		return id
	}
	if p := checkauth.GetPrincipalFromContext(ctx); p != nil && p.PrincipalID != "" {
		return p.PrincipalID
	}
	if user := checkauth.GetUserFromContext(ctx); user != nil {
		return user.UserID
	}
	return ""
}

// canManageSubscription authorizes a subscription of a scope and project.
func (s *subscriptionRoutes) canManageSubscription(ctx context.Context, orgID string, projectID *string) bool {
	if projectID == nil || *projectID == "" {
		return principalAllowsOrg(ctx, s.store, orgID, tokencaps.OrganizationsManage)
	}
	project, err := s.store.GetProjectByID(ctx, *projectID)
	if err != nil || project == nil || project.OwnershipOrgID() != orgID {
		return false
	}
	return principalAllowsProjectOwner(ctx, s.store, project, tokencaps.ProjectsManage)
}

func (s *subscriptionRoutes) mapError(w http.ResponseWriter, err error) {
	switch {
	case outboundevents.IsValidationError(err):
		s.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid_input", Message: err.Error()})
	case errors.Is(err, store.ErrAlreadyExists):
		s.respondWithJSON(w, http.StatusConflict, ErrorResponse{Error: "conflict", Message: err.Error()})
	case errors.Is(err, store.ErrConflict):
		s.respondWithJSON(w, http.StatusConflict, ErrorResponse{Error: "conflict", Message: err.Error()})
	case errors.Is(err, store.ErrNotFound):
		s.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
	default:
		s.respondWithError(w, http.StatusInternalServerError, err)
	}
}

func (s *subscriptionRoutes) serve(w http.ResponseWriter, r *http.Request, orgName string, rest []string) {
	events, ok := s.events(w)
	if !ok {
		return
	}
	orgs, ok := s.store.(organizationStore)
	if !ok {
		s.respondWithError(w, http.StatusNotImplemented, errors.New("organizations are not supported by this store"))
		return
	}
	org, err := orgs.GetOrganizationByName(r.Context(), orgName)
	if err != nil || org == nil {
		s.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
		return
	}
	ctx := r.Context()
	if len(rest) == 0 {
		switch r.Method {
		case http.MethodGet:
			s.list(w, r, events, org)
		case http.MethodPost:
			s.create(w, r, org)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}
	sub, err := events.GetEventSubscription(ctx, rest[0])
	if err != nil || sub.OrgID != org.OrgID || !s.canManageSubscription(ctx, org.OrgID, sub.ProjectID) {
		// Not found, not forbidden: no existence oracle.
		s.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
		return
	}
	switch {
	case len(rest) == 1:
		switch r.Method {
		case http.MethodGet:
			s.respondWithJSON(w, http.StatusOK, subscriptionView(sub, ""))
		case http.MethodPatch, http.MethodPut:
			s.update(w, r, org, sub)
		case http.MethodDelete:
			if err := events.DeleteEventSubscription(ctx, sub.SubscriptionID); err != nil {
				s.mapError(w, err)
				return
			}
			audit.Record(ctx, s.store, org.OrgID, "subscription.delete", "event_subscription", sub.SubscriptionID,
				models.JSONB{"name": sub.Name, "scope": sub.Scope})
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	case len(rest) == 2 && rest[1] == "test" && r.Method == http.MethodPost:
		outcome := s.manager.Test(ctx, sub, org.Name)
		s.respondWithJSON(w, http.StatusOK, testOutcome(outcome))
	case len(rest) == 2 && rest[1] == "replay-dead-letters" && r.Method == http.MethodPost:
		count, err := events.ReplayDeadLetterDeliveries(ctx, sub.SubscriptionID)
		if err != nil {
			s.mapError(w, err)
			return
		}
		audit.Record(ctx, s.store, org.OrgID, "subscription.replay", "event_subscription", sub.SubscriptionID,
			models.JSONB{"dead_letters": count})
		s.respondWithJSON(w, http.StatusOK, map[string]int64{"replayed": count})
	case len(rest) == 2 && rest[1] == "deliveries" && r.Method == http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		deliveries, err := events.ListEventDeliveries(ctx, sub.SubscriptionID, r.URL.Query().Get("status"), limit)
		if err != nil {
			s.mapError(w, err)
			return
		}
		if deliveries == nil {
			deliveries = []models.EventDelivery{}
		}
		s.respondWithJSON(w, http.StatusOK, map[string]any{"deliveries": deliveries})
	case len(rest) >= 3 && rest[1] == "deliveries":
		delivery, err := events.GetEventDelivery(ctx, rest[2])
		if err != nil || delivery.SubscriptionID != sub.SubscriptionID {
			s.respondWithError(w, http.StatusNotFound, store.ErrNotFound)
			return
		}
		switch {
		case len(rest) == 3 && r.Method == http.MethodGet:
			attempts, err := events.ListEventDeliveryAttempts(ctx, delivery.DeliveryID)
			if err != nil {
				s.mapError(w, err)
				return
			}
			s.respondWithJSON(w, http.StatusOK, DeliveryView{EventDelivery: *delivery, Attempts: attempts})
		case len(rest) == 4 && rest[3] == "replay" && r.Method == http.MethodPost:
			replayed, err := events.ReplayEventDelivery(ctx, delivery.DeliveryID)
			if err != nil {
				s.mapError(w, err)
				return
			}
			audit.Record(ctx, s.store, org.OrgID, "subscription.replay", "event_subscription", sub.SubscriptionID,
				models.JSONB{"delivery_id": delivery.DeliveryID, "event_id": delivery.EventID})
			s.respondWithJSON(w, http.StatusOK, replayed)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	default:
		http.Error(w, "Not found", http.StatusNotFound)
	}
}

// TestOutcome is the answer of a test send.
type TestOutcome struct {
	Delivered  bool   `json:"delivered"`
	HTTPStatus int    `json:"http_status,omitempty"`
	LatencyMs  int64  `json:"latency_ms"`
	Error      string `json:"error,omitempty"`
	Refused    bool   `json:"refused,omitempty"`
}

func testOutcome(o outboundevents.Outcome) TestOutcome {
	out := TestOutcome{Delivered: o.Delivered(), HTTPStatus: o.StatusCode, LatencyMs: o.Latency.Milliseconds(), Refused: o.Refused}
	if o.Err != nil {
		out.Error = o.Err.Error()
	}
	return out
}

func (s *subscriptionRoutes) list(w http.ResponseWriter, r *http.Request, events store.OutboundEventStore, org *models.Organization) {
	ctx := r.Context()
	var projectID *string
	if value := r.URL.Query().Get("project"); value != "" {
		projectID = &value
	}
	if !s.canManageSubscription(ctx, org.OrgID, projectID) {
		s.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	subs, err := events.ListEventSubscriptions(ctx, org.OrgID, projectID)
	if err != nil {
		s.mapError(w, err)
		return
	}
	views := make([]SubscriptionView, 0, len(subs))
	for i := range subs {
		views = append(views, subscriptionView(&subs[i], ""))
	}
	s.respondWithJSON(w, http.StatusOK, map[string]any{"subscriptions": views})
}

func (s *subscriptionRoutes) create(w http.ResponseWriter, r *http.Request, org *models.Organization) {
	ctx := r.Context()
	var req subscriptionCreateRequest
	if err := decodeStrict(r, &req); err != nil {
		s.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid_input", Message: "invalid JSON body: " + err.Error()})
		return
	}
	if req.ProjectID != nil && *req.ProjectID == "" {
		req.ProjectID = nil
	}
	if !s.canManageSubscription(ctx, org.OrgID, req.ProjectID) {
		s.respondWithError(w, http.StatusForbidden, store.ErrForbidden)
		return
	}
	sub, secret, err := s.manager.Create(ctx, org.OrgID, outboundevents.SubscriptionInput{
		Name: req.Name, Description: req.Description, Scope: req.Scope, ProjectID: req.ProjectID,
		WorkflowSecurityID: req.WorkflowSecurityID, OwnerGroup: req.OwnerGroup, EventTypes: req.EventTypes,
		RefClassesAny: req.RefClassesAny, Results: req.Results, ActorKinds: req.ActorKinds, FailureClasses: req.FailureClasses,
		SinkType:   req.SinkType,
		WebhookURL: req.WebhookURL, MaxAttempts: req.MaxAttempts, Enabled: req.Enabled,
		SigningSecret: req.SigningSecret, OwnerPrincipalID: req.OwnerPrincipalID,
	}, callerPrincipalID(ctx))
	if err != nil {
		s.mapError(w, err)
		return
	}
	audit.Record(ctx, s.store, org.OrgID, "subscription.create", "event_subscription", sub.SubscriptionID,
		subscriptionAuditDetails(sub))
	s.respondWithJSON(w, http.StatusCreated, subscriptionView(sub, secret))
}

func (s *subscriptionRoutes) update(w http.ResponseWriter, r *http.Request, org *models.Organization, sub *models.EventSubscription) {
	ctx := r.Context()
	var req subscriptionPatchRequest
	if err := decodeStrict(r, &req); err != nil {
		s.respondWithJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid_input", Message: "invalid JSON body: " + err.Error()})
		return
	}
	updated, secret, err := s.manager.Update(ctx, sub, outboundevents.SubscriptionPatch{
		Description: req.Description, EventTypes: req.EventTypes, RefClassesAny: req.RefClassesAny, Results: req.Results,
		ActorKinds: req.ActorKinds, FailureClasses: req.FailureClasses, WebhookURL: req.WebhookURL,
		MaxAttempts: req.MaxAttempts, Enabled: req.Enabled,
		RotateSecret: req.RotateSecret, SigningSecret: req.SigningSecret,
	})
	if err != nil {
		s.mapError(w, err)
		return
	}
	details := subscriptionAuditDetails(updated)
	details["secret_rotated"] = secret != ""
	audit.Record(ctx, s.store, org.OrgID, "subscription.update", "event_subscription", updated.SubscriptionID, details)
	s.respondWithJSON(w, http.StatusOK, subscriptionView(updated, secret))
}

// subscriptionAuditDetails holds no secret. The URL can carry a token in
// its query, so only its host is recorded.
func subscriptionAuditDetails(sub *models.EventSubscription) models.JSONB {
	return models.JSONB{
		"name": sub.Name, "scope": sub.Scope, "enabled": sub.Enabled, "sink_type": sub.SinkType,
		"sink_host": outboundevents.URLHost(sub.WebhookURL()), "event_types": []string(sub.EventTypes),
		"owner_principal_id": sub.OwnerPrincipalID,
	}
}
