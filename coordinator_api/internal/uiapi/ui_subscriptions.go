package uiapi

import (
	"context"
	"errors"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/outboundevents"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/principals"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/uiapi/csilapi"
)

// Outbound event subscriptions (docs/outbound-events.md). An org admin
// manages org and owner_group subscriptions without a project; a project
// owner manages project and workflow subscriptions. A subscription that the
// caller cannot manage answers not_found, so the operation is no existence
// oracle.

func (s *UiService) outboundEvents() (*outboundevents.Manager, store.OutboundEventStore, error) {
	if s.deps.OutboundEvents == nil {
		return nil, nil, NewServiceError("unavailable", "outbound events are not available on this coordinator")
	}
	return s.deps.OutboundEvents, s.deps.OutboundEvents.Store, nil
}

// authorizeSubscriptionScope checks that the caller manages subscriptions
// of orgID with projectID (nil for an org-level subscription). A token needs
// organizations:manage for an org-level subscription and projects:manage for
// a project subscription, as in the REST routes.
func (s *UiService) authorizeSubscriptionScope(ctx context.Context, id authz.Identity, orgID string, projectID *string) error {
	if projectID == nil || *projectID == "" {
		return s.requireOrgAdminGrant(ctx, id, orgID, tokencaps.OrganizationsManage)
	}
	project, err := s.deps.Store.GetProjectByID(ctx, *projectID)
	if err != nil || project == nil || project.OwnershipOrgID() != orgID {
		return NewServiceError("not_found", "project not found")
	}
	if err := requireTokenGrant(id, &orgID, tokencaps.ProjectsManage); err != nil {
		return err
	}
	return mapPermissionErr(s.deps.Resolver.RequireProjectOwner(ctx, id, *projectID))
}

func callerPrincipal(ctx context.Context, id authz.Identity, user *models.User) string {
	if id.PrincipalID != "" {
		return id.PrincipalID
	}
	if id.UserID != "" {
		return id.UserID
	}
	if actor := principals.ActorOf(ctx).PrincipalID; actor != "" {
		return actor
	}
	if id.Token != nil {
		return id.Token.PrincipalID
	}
	if user != nil {
		return user.UserID
	}
	return ""
}

// loadManagedSubscription loads a subscription and checks that the caller
// manages it.
func (s *UiService) loadManagedSubscription(ctx context.Context, subscriptionID string) (context.Context, *models.User, *outboundevents.Manager, store.OutboundEventStore, *models.EventSubscription, error) {
	ctx, id, user, authErr := s.deps.requireManagementIdentity(ctx)
	if authErr != nil {
		return ctx, nil, nil, nil, nil, authErr
	}
	if err := requireNonEmpty("subscription_id", subscriptionID, 64); err != nil {
		return ctx, nil, nil, nil, nil, err
	}
	manager, events, err := s.outboundEvents()
	if err != nil {
		return ctx, nil, nil, nil, nil, err
	}
	sub, err := events.GetEventSubscription(ctx, subscriptionID)
	if err != nil {
		return ctx, nil, nil, nil, nil, mapStoreErr(err, "subscription not found")
	}
	if s.authorizeSubscriptionScope(ctx, id, sub.OrgID, sub.ProjectID) != nil {
		return ctx, nil, nil, nil, nil, NewServiceError("not_found", "subscription not found")
	}
	return ctx, user, manager, events, sub, nil
}

func subscriptionToSummary(sub *models.EventSubscription) csilapi.EventSubscriptionSummary {
	orEmpty := func(v []string) []string {
		if v == nil {
			return []string{}
		}
		return v
	}
	return csilapi.EventSubscriptionSummary{
		SubscriptionId: sub.SubscriptionID, OrgId: sub.OrgID, Name: sub.Name, Description: sub.Description,
		OwnerPrincipalId: sub.OwnerPrincipalID, OwnerActive: sub.OwnerActive, Scope: sub.Scope, ProjectId: sub.ProjectID,
		WorkflowSecurityId: sub.WorkflowSecurityID, OwnerGroup: sub.OwnerGroup,
		EventTypes: orEmpty(sub.EventTypes), RefClassesAny: orEmpty(sub.RefClassesAny), Results: orEmpty(sub.Results),
		ActorKinds: orEmpty(sub.ActorKinds), FailureClasses: orEmpty(sub.FailureClasses),
		SinkType: sub.SinkType, WebhookUrl: sub.WebhookURL(),
		MaxAttempts: int64(sub.MaxAttempts), Enabled: sub.Enabled,
		CreatedAt: formatTime(sub.CreatedAt), UpdatedAt: formatTime(sub.UpdatedAt),
	}
}

func intPtr64(v *int) *int64 {
	if v == nil {
		return nil
	}
	out := int64(*v)
	return &out
}

func deliveryToSummary(d *models.EventDelivery) csilapi.EventDeliverySummary {
	return csilapi.EventDeliverySummary{
		DeliveryId: d.DeliveryID, SubscriptionId: d.SubscriptionID, EventId: d.EventID, EventType: d.EventType,
		OrderKey: d.OrderKey, Sequence: d.Sequence, Status: d.Status, Attempts: int64(d.Attempts),
		LastHttpStatus: intPtr64(d.LastHTTPStatus), LastLatencyMs: intPtr64(d.LastLatencyMs), LastError: d.LastError,
		NextAttemptAt: formatTime(d.NextAttemptAt), DeliveredAt: formatTimePtr(d.DeliveredAt),
		DeadLetteredAt: formatTimePtr(d.DeadLetteredAt), CreatedAt: formatTime(d.CreatedAt),
	}
}

func mapSubscriptionErr(err error) error {
	switch {
	case outboundevents.IsValidationError(err):
		return NewServiceError("invalid_argument", err.Error())
	case errors.Is(err, store.ErrAlreadyExists):
		return NewServiceError("conflict", "a subscription with this name already exists in the organization")
	case errors.Is(err, store.ErrConflict):
		return NewServiceError("conflict", err.Error())
	}
	return mapStoreErr(err, "subscription not found")
}

func subscriptionAudit(sub *models.EventSubscription) models.JSONB {
	return models.JSONB{"name": sub.Name, "scope": sub.Scope, "enabled": sub.Enabled, "sink_type": sub.SinkType,
		"sink_host": outboundevents.URLHost(sub.WebhookURL()), "event_types": []string(sub.EventTypes),
		"owner_principal_id": sub.OwnerPrincipalID}
}

func (s *UiService) ListEventSubscriptions(ctx context.Context, req csilapi.ListEventSubscriptionsRequest) (csilapi.ListEventSubscriptionsResponse, error) {
	ctx, id, _, authErr := s.deps.requireManagementIdentity(ctx)
	if authErr != nil {
		return csilapi.ListEventSubscriptionsResponse{}, authErr
	}
	if err := requireNonEmpty("org_id", req.OrgId, 64); err != nil {
		return csilapi.ListEventSubscriptionsResponse{}, err
	}
	_, events, err := s.outboundEvents()
	if err != nil {
		return csilapi.ListEventSubscriptionsResponse{}, err
	}
	projectID := req.ProjectId
	if projectID != nil && *projectID == "" {
		projectID = nil
	}
	if err := s.authorizeSubscriptionScope(ctx, id, req.OrgId, projectID); err != nil {
		return csilapi.ListEventSubscriptionsResponse{}, err
	}
	subs, err := events.ListEventSubscriptions(ctx, req.OrgId, projectID)
	if err != nil {
		return csilapi.ListEventSubscriptionsResponse{}, mapSubscriptionErr(err)
	}
	out := make([]csilapi.EventSubscriptionSummary, 0, len(subs))
	for i := range subs {
		out = append(out, subscriptionToSummary(&subs[i]))
	}
	return csilapi.ListEventSubscriptionsResponse{Subscriptions: out}, nil
}

func (s *UiService) GetEventSubscription(ctx context.Context, req csilapi.GetEventSubscriptionRequest) (csilapi.GetEventSubscriptionResponse, error) {
	_, _, _, _, sub, err := s.loadManagedSubscription(ctx, req.SubscriptionId)
	if err != nil {
		return csilapi.GetEventSubscriptionResponse{}, err
	}
	return csilapi.GetEventSubscriptionResponse{Subscription: subscriptionToSummary(sub)}, nil
}

func (s *UiService) CreateEventSubscription(ctx context.Context, req csilapi.CreateEventSubscriptionRequest) (csilapi.CreateEventSubscriptionResponse, error) {
	ctx, id, user, authErr := s.deps.requireManagementIdentity(ctx)
	if authErr != nil {
		return csilapi.CreateEventSubscriptionResponse{}, authErr
	}
	if err := requireNonEmpty("org_id", req.OrgId, 64); err != nil {
		return csilapi.CreateEventSubscriptionResponse{}, err
	}
	manager, _, err := s.outboundEvents()
	if err != nil {
		return csilapi.CreateEventSubscriptionResponse{}, err
	}
	projectID := req.ProjectId
	if projectID != nil && *projectID == "" {
		projectID = nil
	}
	if err := s.authorizeSubscriptionScope(ctx, id, req.OrgId, projectID); err != nil {
		return csilapi.CreateEventSubscriptionResponse{}, err
	}
	in := outboundevents.SubscriptionInput{
		Name: req.Name, Description: derefOr(req.Description, ""), Scope: req.Scope, ProjectID: projectID,
		WorkflowSecurityID: derefOr(req.WorkflowSecurityId, ""), OwnerGroup: derefOr(req.OwnerGroup, ""),
		EventTypes: req.EventTypes, RefClassesAny: req.RefClassesAny, Results: req.Results, ActorKinds: req.ActorKinds,
		FailureClasses: req.FailureClasses, SinkType: derefOr(req.SinkType, ""),
		WebhookURL: req.WebhookUrl, Enabled: req.Enabled,
		SigningSecret: derefOr(req.SigningSecret, ""), OwnerPrincipalID: derefOr(req.OwnerPrincipalId, ""),
	}
	if req.MaxAttempts != nil {
		in.MaxAttempts = int(*req.MaxAttempts)
	}
	sub, secret, err := manager.Create(ctx, req.OrgId, in, callerPrincipal(ctx, id, user))
	if err != nil {
		return csilapi.CreateEventSubscriptionResponse{}, mapSubscriptionErr(err)
	}
	audit.RecordUser(ctx, s.deps.Store, user, sub.OrgID, "subscription.create", "event_subscription", sub.SubscriptionID, subscriptionAudit(sub))
	return csilapi.CreateEventSubscriptionResponse{Subscription: subscriptionToSummary(sub), SigningSecret: secret}, nil
}

func (s *UiService) UpdateEventSubscription(ctx context.Context, req csilapi.UpdateEventSubscriptionRequest) (csilapi.UpdateEventSubscriptionResponse, error) {
	ctx, user, manager, _, sub, err := s.loadManagedSubscription(ctx, req.SubscriptionId)
	if err != nil {
		return csilapi.UpdateEventSubscriptionResponse{}, err
	}
	patch := outboundevents.SubscriptionPatch{Description: req.Description, WebhookURL: req.WebhookUrl, Enabled: req.Enabled,
		SigningSecret: derefOr(req.SigningSecret, "")}
	if req.RotateSecret != nil {
		patch.RotateSecret = *req.RotateSecret
	}
	if req.MaxAttempts != nil {
		v := int(*req.MaxAttempts)
		patch.MaxAttempts = &v
	}
	setList := func(values []string) *[]string {
		if values == nil {
			return nil
		}
		v := values
		return &v
	}
	patch.EventTypes, patch.RefClassesAny = setList(req.EventTypes), setList(req.RefClassesAny)
	patch.Results, patch.ActorKinds = setList(req.Results), setList(req.ActorKinds)
	patch.FailureClasses = setList(req.FailureClasses)
	empty := []string{}
	for _, name := range req.ClearFilters {
		switch name {
		case "event_types":
			patch.EventTypes = &empty
		case "ref_classes_any":
			patch.RefClassesAny = &empty
		case "results":
			patch.Results = &empty
		case "actor_kinds":
			patch.ActorKinds = &empty
		case "failure_classes":
			patch.FailureClasses = &empty
		default:
			return csilapi.UpdateEventSubscriptionResponse{}, NewServiceError("invalid_argument", "unknown filter in clear_filters: "+name)
		}
	}
	updated, secret, err := manager.Update(ctx, sub, patch)
	if err != nil {
		return csilapi.UpdateEventSubscriptionResponse{}, mapSubscriptionErr(err)
	}
	details := subscriptionAudit(updated)
	details["secret_rotated"] = secret != ""
	audit.RecordUser(ctx, s.deps.Store, user, updated.OrgID, "subscription.update", "event_subscription", updated.SubscriptionID, details)
	resp := csilapi.UpdateEventSubscriptionResponse{Subscription: subscriptionToSummary(updated)}
	if secret != "" {
		resp.SigningSecret = &secret
	}
	return resp, nil
}

func (s *UiService) DeleteEventSubscription(ctx context.Context, req csilapi.DeleteEventSubscriptionRequest) (csilapi.DeleteEventSubscriptionResponse, error) {
	ctx, user, _, events, sub, err := s.loadManagedSubscription(ctx, req.SubscriptionId)
	if err != nil {
		return csilapi.DeleteEventSubscriptionResponse{}, err
	}
	if err := events.DeleteEventSubscription(ctx, sub.SubscriptionID); err != nil {
		return csilapi.DeleteEventSubscriptionResponse{}, mapSubscriptionErr(err)
	}
	audit.RecordUser(ctx, s.deps.Store, user, sub.OrgID, "subscription.delete", "event_subscription", sub.SubscriptionID,
		models.JSONB{"name": sub.Name, "scope": sub.Scope})
	return csilapi.DeleteEventSubscriptionResponse{Ok: true}, nil
}

func (s *UiService) ListEventDeliveries(ctx context.Context, req csilapi.ListEventDeliveriesRequest) (csilapi.ListEventDeliveriesResponse, error) {
	ctx, _, _, events, sub, err := s.loadManagedSubscription(ctx, req.SubscriptionId)
	if err != nil {
		return csilapi.ListEventDeliveriesResponse{}, err
	}
	limit := 50
	if req.Limit != nil {
		limit = int(*req.Limit)
	}
	status := derefOr(req.Status, "")
	if status != "" && status != models.DeliveryStatusPending && status != models.DeliveryStatusQueued &&
		status != models.DeliveryStatusSending && status != models.DeliveryStatusDelivered && status != models.DeliveryStatusDeadLetter {
		return csilapi.ListEventDeliveriesResponse{}, NewServiceError("invalid_argument", "unknown delivery status")
	}
	rows, err := events.ListEventDeliveries(ctx, sub.SubscriptionID, status, limit)
	if err != nil {
		return csilapi.ListEventDeliveriesResponse{}, mapSubscriptionErr(err)
	}
	out := make([]csilapi.EventDeliverySummary, 0, len(rows))
	for i := range rows {
		out = append(out, deliveryToSummary(&rows[i]))
	}
	return csilapi.ListEventDeliveriesResponse{Deliveries: out}, nil
}

func (s *UiService) GetEventDelivery(ctx context.Context, req csilapi.GetEventDeliveryRequest) (csilapi.GetEventDeliveryResponse, error) {
	ctx, _, _, events, sub, err := s.loadManagedSubscription(ctx, req.SubscriptionId)
	if err != nil {
		return csilapi.GetEventDeliveryResponse{}, err
	}
	delivery, err := events.GetEventDelivery(ctx, req.DeliveryId)
	if err != nil || delivery.SubscriptionID != sub.SubscriptionID {
		return csilapi.GetEventDeliveryResponse{}, NewServiceError("not_found", "delivery not found")
	}
	attempts, err := events.ListEventDeliveryAttempts(ctx, delivery.DeliveryID)
	if err != nil {
		return csilapi.GetEventDeliveryResponse{}, mapSubscriptionErr(err)
	}
	out := make([]csilapi.EventDeliveryAttemptEntry, 0, len(attempts))
	for _, a := range attempts {
		out = append(out, csilapi.EventDeliveryAttemptEntry{Attempt: int64(a.Attempt), StartedAt: formatTime(a.StartedAt),
			FinishedAt: formatTime(a.FinishedAt), Result: a.Result, HttpStatus: intPtr64(a.HTTPStatus),
			LatencyMs: int64(a.LatencyMs), Error: a.Error})
	}
	return csilapi.GetEventDeliveryResponse{Delivery: deliveryToSummary(delivery), Attempts: out}, nil
}

func (s *UiService) ReplayEventDelivery(ctx context.Context, req csilapi.ReplayEventDeliveryRequest) (csilapi.ReplayEventDeliveryResponse, error) {
	ctx, user, _, events, sub, err := s.loadManagedSubscription(ctx, req.SubscriptionId)
	if err != nil {
		return csilapi.ReplayEventDeliveryResponse{}, err
	}
	all := req.AllDeadLetters != nil && *req.AllDeadLetters
	deliveryID := derefOr(req.DeliveryId, "")
	if all == (deliveryID != "") {
		return csilapi.ReplayEventDeliveryResponse{}, NewServiceError("invalid_argument", "give delivery_id or all_dead_letters")
	}
	var count int64
	details := models.JSONB{}
	if all {
		if count, err = events.ReplayDeadLetterDeliveries(ctx, sub.SubscriptionID); err != nil {
			return csilapi.ReplayEventDeliveryResponse{}, mapSubscriptionErr(err)
		}
		details["dead_letters"] = count
	} else {
		delivery, getErr := events.GetEventDelivery(ctx, deliveryID)
		if getErr != nil || delivery.SubscriptionID != sub.SubscriptionID {
			return csilapi.ReplayEventDeliveryResponse{}, NewServiceError("not_found", "delivery not found")
		}
		if _, err := events.ReplayEventDelivery(ctx, deliveryID); err != nil {
			return csilapi.ReplayEventDeliveryResponse{}, mapSubscriptionErr(err)
		}
		count = 1
		details["delivery_id"], details["event_id"] = deliveryID, delivery.EventID
	}
	audit.RecordUser(ctx, s.deps.Store, user, sub.OrgID, "subscription.replay", "event_subscription", sub.SubscriptionID, details)
	return csilapi.ReplayEventDeliveryResponse{Replayed: count}, nil
}

func (s *UiService) TestEventSubscription(ctx context.Context, req csilapi.TestEventSubscriptionRequest) (csilapi.TestEventSubscriptionResponse, error) {
	ctx, _, manager, _, sub, err := s.loadManagedSubscription(ctx, req.SubscriptionId)
	if err != nil {
		return csilapi.TestEventSubscriptionResponse{}, err
	}
	orgName := ""
	if orgs, ok := s.deps.Store.(interface {
		GetOrganizationByID(ctx context.Context, orgID string) (*models.Organization, error)
	}); ok {
		if org, err := orgs.GetOrganizationByID(ctx, sub.OrgID); err == nil && org != nil {
			orgName = org.Name
		}
	}
	outcome := manager.Test(ctx, sub, orgName)
	resp := csilapi.TestEventSubscriptionResponse{Delivered: outcome.Delivered(), LatencyMs: outcome.Latency.Milliseconds(), Refused: outcome.Refused}
	if outcome.StatusCode != 0 {
		code := int64(outcome.StatusCode)
		resp.HttpStatus = &code
	}
	if outcome.Err != nil {
		resp.Error = outcome.Err.Error()
	}
	return resp, nil
}
