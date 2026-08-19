package worker

import (
	"reflect"
	"strings"
	"testing"
)

func TestValidateImagePullSecretNames(t *testing.T) {
	cases := []struct {
		name    string
		names   []string
		wantErr string
	}{
		{name: "empty list ok", names: nil},
		{name: "valid names", names: []string{"regcred", "team.registry-cred"}},
		{name: "empty name", names: []string{""}, wantErr: "empty name"},
		{name: "uppercase", names: []string{"RegCred"}, wantErr: "not a valid"},
		{name: "underscore", names: []string{"reg_cred"}, wantErr: "not a valid"},
		{name: "leading dash", names: []string{"-regcred"}, wantErr: "not a valid"},
		{name: "trailing dot", names: []string{"regcred."}, wantErr: "not a valid"},
		{name: "too long", names: []string{strings.Repeat("a", 254)}, wantErr: "not a valid"},
		{name: "duplicate", names: []string{"regcred", "regcred"}, wantErr: "duplicate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateImagePullSecretNames(tc.names)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestEnforceImagePullSecretAllowlist(t *testing.T) {
	// Secure default: nothing approved means every request is rejected.
	if err := EnforceImagePullSecretAllowlist([]string{"regcred"}, nil, nil); err == nil {
		t.Fatal("expected rejection with empty global list and empty allowlist")
	}
	// No request always passes.
	if err := EnforceImagePullSecretAllowlist(nil, nil, nil); err != nil {
		t.Fatalf("unexpected error for empty request: %v", err)
	}
	// A name in the global list is approved.
	if err := EnforceImagePullSecretAllowlist([]string{"global-cred"}, []string{"global-cred"}, nil); err != nil {
		t.Fatalf("unexpected error for global name: %v", err)
	}
	// A name in the allowlist is approved.
	if err := EnforceImagePullSecretAllowlist([]string{"regcred"}, nil, []string{"regcred"}); err != nil {
		t.Fatalf("unexpected error for allowlisted name: %v", err)
	}
	// A name in neither list is rejected even when other names are approved.
	err := EnforceImagePullSecretAllowlist([]string{"regcred", "other"}, []string{"global-cred"}, []string{"regcred"})
	if err == nil || !strings.Contains(err.Error(), `"other"`) {
		t.Fatalf("expected rejection naming \"other\", got %v", err)
	}
}

func TestCombineImagePullSecrets(t *testing.T) {
	got := CombineImagePullSecrets([]string{"global-a", "shared"}, []string{"shared", "job-b"})
	want := []string{"global-a", "shared", "job-b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	if got := CombineImagePullSecrets(nil, nil); len(got) != 0 {
		t.Fatalf("expected empty result, got %v", got)
	}
}
