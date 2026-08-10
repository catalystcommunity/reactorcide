package audit

import (
	"context"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

type Store interface {
	AppendAuditEvent(context.Context, *models.AuditEvent) error
}

// Record appends a sanitized audit event. Callers must not put credentials,
// raw tokens, secret values, or provider tokens in details.
func Record(ctx context.Context, value any, orgID, action, subjectType, subjectID string, details models.JSONB) {
	record(ctx, value, nil, orgID, action, subjectType, subjectID, details)
}

// RecordUser appends an event for an authenticated UI session. UI sessions
// do not use the API-token principal context.
func RecordUser(ctx context.Context, value any, user *models.User, orgID, action, subjectType, subjectID string, details models.JSONB) {
	record(ctx, value, user, orgID, action, subjectType, subjectID, details)
}

func record(ctx context.Context, value any, user *models.User, orgID, action, subjectType, subjectID string, details models.JSONB) {
	store, ok := value.(Store)
	if !ok {
		return
	}
	event := &models.AuditEvent{Action: action, SubjectType: subjectType, Details: details}
	if orgID != "" {
		event.OrgID = &orgID
	}
	if subjectID != "" {
		event.SubjectID = &subjectID
	}
	if principal := checkauth.GetPrincipalFromContext(ctx); principal != nil {
		if principal.CredentialID != "" {
			event.ActorCredentialID = &principal.CredentialID
		}
		event.ActorCredentialType = principal.CredentialType
		if principal.UserID != "" {
			event.ActorUserID = &principal.UserID
		}
	} else if user != nil && user.UserID != "" {
		event.ActorUserID = &user.UserID
		event.ActorCredentialType = "ui_session"
	}
	_ = store.AppendAuditEvent(ctx, event)
}
