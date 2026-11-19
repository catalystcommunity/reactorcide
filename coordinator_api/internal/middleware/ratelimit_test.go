package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/ratelimit"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

type tokenStore struct {
	store.Store
}

func (tokenStore) ValidateAPIToken(_ context.Context, token string) (*models.APIToken, *models.User, error) {
	return &models.APIToken{TokenID: "token-" + token, SubjectType: "instance_token", AllOrganizations: true, AllCapabilities: true}, nil, nil
}

func (tokenStore) UpdateTokenLastUsed(context.Context, string, time.Time) error { return nil }
func (tokenStore) AppendAuditEvent(context.Context, *models.AuditEvent) error   { return nil }

func TestAPIRateLimitPerPrincipal(t *testing.T) {
	SetAPIRateLimiter(ratelimit.New(1, 2))
	t.Cleanup(func() { SetAPIRateLimiter(nil) })
	handler := APITokenMiddleware(tokenStore{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	call := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	for i := 0; i < 2; i++ {
		if code := call("a").Code; code != http.StatusOK {
			t.Fatalf("request %d: %d", i, code)
		}
	}
	limited := call("a")
	if limited.Code != http.StatusTooManyRequests || limited.Header().Get("Retry-After") == "" {
		t.Fatalf("over the limit: %d Retry-After=%q", limited.Code, limited.Header().Get("Retry-After"))
	}
	if code := call("b").Code; code != http.StatusOK {
		t.Fatalf("another principal has its own bucket: %d", code)
	}
}

func TestAPIRateLimitOffByDefault(t *testing.T) {
	SetAPIRateLimiter(nil)
	handler := APITokenMiddleware(tokenStore{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	for i := 0; i < 50; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer a")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: %d", i, w.Code)
		}
	}
}
