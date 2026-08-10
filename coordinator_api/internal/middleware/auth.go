package middleware

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/tokencaps"
)

// APITokenMiddleware creates middleware that validates API tokens
func APITokenMiddleware(appStore store.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":"unauthorized","message":"Missing Authorization header"}`))
				return
			}

			if !strings.HasPrefix(authHeader, "Bearer ") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":"unauthorized","message":"Invalid Authorization header format. Use: Bearer <token>"}`))
				return
			}

			token := strings.TrimPrefix(authHeader, "Bearer ")
			if token == "" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":"unauthorized","message":"Empty token"}`))
				return
			}

			// Validate token against database
			apiToken, user, err := appStore.ValidateAPIToken(r.Context(), token)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":"unauthorized","message":"Invalid or expired token"}`))
				return
			}

			capabilities, err := tokencaps.New(apiToken.Capabilities...)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":"unauthorized","message":"Invalid token authority"}`))
				return
			}
			principal := &checkauth.Principal{
				CredentialID: apiToken.TokenID, CredentialType: apiToken.SubjectType,
				UserID: apiToken.UserID, AllOrganizations: apiToken.AllOrganizations,
				OrganizationIDs: append([]string(nil), apiToken.OrganizationIDs...),
				AllCapabilities: apiToken.AllCapabilities, Capabilities: capabilities,
				BoundJobID: valueOrEmpty(apiToken.BoundJobID),
			}
			if apiToken.OwnerOrgID != nil {
				principal.OwnerOrgID = *apiToken.OwnerOrgID
			}

			// Add the classified principal, optional user, and verification state.
			ctx := checkauth.SetUserContext(r.Context(), user)
			ctx = checkauth.SetPrincipalContext(ctx, principal)
			ctx = checkauth.SetVerifiedContext(ctx, true)
			if updater, ok := appStore.(interface {
				UpdateTokenLastUsed(context.Context, string, time.Time) error
			}); ok {
				_ = updater.UpdateTokenLastUsed(ctx, apiToken.TokenID, time.Now().UTC())
			}
			audit.Record(ctx, appStore, principal.OwnerOrgID, "token.use", "api_token", apiToken.TokenID,
				models.JSONB{"subject_type": apiToken.SubjectType})

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// RequireRoleMiddleware creates middleware that checks if the authenticated user has a required role
func RequireRoleMiddleware(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := checkauth.GetUserFromContext(r.Context())
			if user == nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":"unauthorized","message":"User not authenticated"}`))
				return
			}

			// Check if user has the required role
			hasRole := false
			for _, userRole := range user.Roles {
				if userRole == role {
					hasRole = true
					break
				}
			}

			if !hasRole {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				w.Write([]byte(`{"error":"forbidden","message":"Insufficient permissions. Requires role: ` + role + `"}`))
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
