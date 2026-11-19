package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/audit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/authz"
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

			principal, user, apiToken, err := PrincipalForToken(r.Context(), appStore, token)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"error":"unauthorized","message":"` + err.Error() + `"}`))
				return
			}

			// Overload protection: a principal over the API rate limit gets 429.
			if refuseRateLimitedAPI(w, principal) {
				return
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

// errInvalidToken and errInvalidAuthority are the refusals of
// PrincipalForToken. Their text is the message of the 401 response.
var (
	errInvalidToken     = errors.New("Invalid or expired token")
	errInvalidAuthority = errors.New("Invalid token authority")
)

// PrincipalForToken validates an API token and returns its classified
// principal and optional user, the same way for every endpoint that accepts
// a bearer token (REST, and the UI event stream). A service principal token
// is narrowed to the roles of its principal.
func PrincipalForToken(ctx context.Context, appStore store.Store, token string) (*checkauth.Principal, *models.User, *models.APIToken, error) {
	apiToken, user, err := appStore.ValidateAPIToken(ctx, token)
	if err != nil || apiToken == nil {
		return nil, nil, nil, errInvalidToken
	}
	capabilities, err := tokencaps.New(apiToken.Capabilities...)
	if err != nil {
		return nil, nil, nil, errInvalidAuthority
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
	principal.PrincipalID = valueOrEmpty(apiToken.PrincipalID)
	principal.IntersectRoles = apiToken.IntersectRoles
	// A token issued for a service principal acts with its capabilities
	// intersected with the principal's roles.
	if principal.IsServicePrincipalToken() {
		roleStore, ok := appStore.(authz.RoleStore)
		if !ok || authz.NewResolver(roleStore).NarrowServicePrincipalToken(ctx, principal) != nil {
			return nil, nil, nil, errInvalidToken
		}
	}
	return principal, user, apiToken, nil
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
