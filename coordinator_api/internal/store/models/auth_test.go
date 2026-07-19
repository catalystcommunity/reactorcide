package models

import (
	"testing"
	"time"
)

func TestAuthTrustedIdentity_Matches(t *testing.T) {
	tests := []struct {
		name   string
		row    AuthTrustedIdentity
		domain string
		handle string
		want   bool
	}{
		{
			name:   "exact match",
			row:    AuthTrustedIdentity{Domain: "example.com", Handle: "alice"},
			domain: "example.com",
			handle: "alice",
			want:   true,
		},
		{
			name:   "different handle at same domain does not match exact row",
			row:    AuthTrustedIdentity{Domain: "example.com", Handle: "alice"},
			domain: "example.com",
			handle: "bob",
			want:   false,
		},
		{
			name:   "different domain never matches",
			row:    AuthTrustedIdentity{Domain: "example.com", Handle: "alice"},
			domain: "other.com",
			handle: "alice",
			want:   false,
		},
		{
			name:   "bare-domain wildcard row matches any handle",
			row:    AuthTrustedIdentity{Domain: "example.com", Handle: ""},
			domain: "example.com",
			handle: "anyone",
			want:   true,
		},
		{
			name:   "bare-domain wildcard row matches empty handle too",
			row:    AuthTrustedIdentity{Domain: "example.com", Handle: ""},
			domain: "example.com",
			handle: "",
			want:   true,
		},
		{
			name:   "bare-domain wildcard still requires domain match",
			row:    AuthTrustedIdentity{Domain: "example.com", Handle: ""},
			domain: "other.com",
			handle: "anyone",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.row.Matches(tt.domain, tt.handle)
			if got != tt.want {
				t.Errorf("Matches(%q, %q) = %v, want %v", tt.domain, tt.handle, got, tt.want)
			}
		})
	}
}

func TestUISession_IsExpired(t *testing.T) {
	future := UISession{ExpiresAt: time.Now().Add(time.Hour)}
	if future.IsExpired() {
		t.Error("session expiring in the future reported as expired")
	}

	past := UISession{ExpiresAt: time.Now().Add(-time.Hour)}
	if !past.IsExpired() {
		t.Error("session expired in the past not reported as expired")
	}
}

func TestUISession_IsRevoked(t *testing.T) {
	active := UISession{}
	if active.IsRevoked() {
		t.Error("session with nil RevokedAt reported as revoked")
	}

	revokedAt := time.Now()
	revoked := UISession{RevokedAt: &revokedAt}
	if !revoked.IsRevoked() {
		t.Error("session with set RevokedAt not reported as revoked")
	}
}

func TestUISession_IsValid(t *testing.T) {
	valid := UISession{ExpiresAt: time.Now().Add(time.Hour)}
	if !valid.IsValid() {
		t.Error("non-expired, non-revoked session reported invalid")
	}

	revokedAt := time.Now()
	revoked := UISession{ExpiresAt: time.Now().Add(time.Hour), RevokedAt: &revokedAt}
	if revoked.IsValid() {
		t.Error("revoked session reported valid")
	}

	expired := UISession{ExpiresAt: time.Now().Add(-time.Hour)}
	if expired.IsValid() {
		t.Error("expired session reported valid")
	}
}

func TestAuthLoginAttempt_IsExpired(t *testing.T) {
	future := AuthLoginAttempt{ExpiresAt: time.Now().Add(5 * time.Minute)}
	if future.IsExpired() {
		t.Error("attempt expiring in the future reported as expired")
	}

	past := AuthLoginAttempt{ExpiresAt: time.Now().Add(-5 * time.Minute)}
	if !past.IsExpired() {
		t.Error("attempt expired in the past not reported as expired")
	}
}
