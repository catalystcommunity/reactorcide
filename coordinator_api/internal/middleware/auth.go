package middleware

import (
	"net/http"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/checkauth"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
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

			// TODO: Update last used timestamp asynchronously
			// Disabled for now to avoid transaction conflicts in tests
			_ = apiToken

			// Add user and verification status to context
			ctx := checkauth.SetUserContext(r.Context(), user)
			ctx = checkauth.SetVerifiedContext(ctx, true)

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// VerificationMiddleware is a placeholder that was referenced in the existing code
// For now, it just passes through to the next handler since we're using API tokens
func VerificationMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// This was referenced in the existing router.go but not implemented
		// For coordinator API, we're using API token middleware instead
		next.ServeHTTP(w, r)
	})
}
