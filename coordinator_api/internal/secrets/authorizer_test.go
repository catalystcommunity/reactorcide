package secrets

import (
	"context"
	"testing"
)

func TestOrgAuthorizerCanAccessOrg(t *testing.T) {
	authorizer := NewOrgAuthorizer(nil) // DB not needed for current implementation

	tests := []struct {
		name      string
		userID    string
		orgID     string
		wantErr   bool
		errReason string
	}{
		{
			name:    "user accessing own org",
			userID:  "user-123",
			orgID:   "user-123",
			wantErr: false,
		},
		{
			name:      "user accessing different org",
			userID:    "user-123",
			orgID:     "other-org",
			wantErr:   true,
			errReason: "user does not belong to organization",
		},
		{
			name:    "same user ID and org ID",
			userID:  "01HXYZ123ABC",
			orgID:   "01HXYZ123ABC",
			wantErr: false,
		},
	}

	ctx := context.Background()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := authorizer.CanAccessOrg(ctx, tt.userID, tt.orgID)

			if (err != nil) != tt.wantErr {
				t.Errorf("CanAccessOrg() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr && err != nil {
				authErr, ok := err.(*AuthorizationError)
				if !ok {
					t.Errorf("expected *AuthorizationError, got %T", err)
					return
				}
				if authErr.UserID != tt.userID {
					t.Errorf("UserID = %s, want %s", authErr.UserID, tt.userID)
				}
				if authErr.OrgID != tt.orgID {
					t.Errorf("OrgID = %s, want %s", authErr.OrgID, tt.orgID)
				}
				if authErr.Reason != tt.errReason {
					t.Errorf("Reason = %s, want %s", authErr.Reason, tt.errReason)
				}
			}
		})
	}
}

func TestAuthorizationErrorError(t *testing.T) {
	err := &AuthorizationError{
		UserID: "user-123",
		OrgID:  "org-456",
		Reason: "test reason",
	}

	expected := "authorization denied: user user-123 cannot access org org-456: test reason"
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}
}

func TestIsAuthorizationError(t *testing.T) {
	authErr := &AuthorizationError{
		UserID: "user",
		OrgID:  "org",
		Reason: "reason",
	}

	if !IsAuthorizationError(authErr) {
		t.Error("IsAuthorizationError returned false for *AuthorizationError")
	}

	otherErr := ErrNotInitialized
	if IsAuthorizationError(otherErr) {
		t.Error("IsAuthorizationError returned true for non-AuthorizationError")
	}
}
