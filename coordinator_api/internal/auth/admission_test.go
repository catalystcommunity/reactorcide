package auth

import (
	"context"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

func TestParseSelector(t *testing.T) {
	tests := []struct {
		name       string
		selector   string
		wantHandle string
		wantDomain string
		wantErr    bool
	}{
		{name: "bare domain", selector: "example.com", wantHandle: "", wantDomain: "example.com"},
		{name: "handle at domain", selector: "alice@example.com", wantHandle: "alice", wantDomain: "example.com"},
		{name: "uuid at domain", selector: "01H@example.com", wantHandle: "01H", wantDomain: "example.com"},
		{name: "empty", selector: "", wantErr: true},
		{name: "whitespace only", selector: "   ", wantErr: true},
		{name: "empty domain", selector: "alice@", wantErr: true},
		{name: "trims whitespace", selector: "  example.com  ", wantHandle: "", wantDomain: "example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handle, domain, err := ParseSelector(tt.selector)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseSelector(%q) error = %v, wantErr %v", tt.selector, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if handle != tt.wantHandle || domain != tt.wantDomain {
				t.Fatalf("ParseSelector(%q) = (%q, %q), want (%q, %q)", tt.selector, handle, domain, tt.wantHandle, tt.wantDomain)
			}
		})
	}
}

func TestValidateDomainPattern(t *testing.T) {
	if err := ValidateDomainPattern(`^.*\.example\.com$`); err != nil {
		t.Fatalf("ValidateDomainPattern() valid pattern error = %v", err)
	}
	if err := ValidateDomainPattern(""); err == nil {
		t.Fatal("ValidateDomainPattern(\"\") expected an error")
	}
	if err := ValidateDomainPattern(`[unterminated`); err == nil {
		t.Fatal("ValidateDomainPattern() expected an error for invalid regex")
	}
	// Already-anchored patterns must still validate: RE2 tolerates the
	// redundant nested ^/$ from compileAnchoredDomainPattern's wrapping.
	if err := ValidateDomainPattern(`^.*\.example\.com$`); err != nil {
		t.Fatalf("ValidateDomainPattern() already-anchored pattern error = %v", err)
	}
	// Unanchored patterns are still accepted at write time (they compile
	// fine once wrapped) — the anchoring is structural at match time, not a
	// write-time rejection of unanchored input.
	if err := ValidateDomainPattern(`example\.com`); err != nil {
		t.Fatalf("ValidateDomainPattern() unanchored pattern error = %v", err)
	}
}

func TestAdmissionMatrix(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()

	// Exact selector.
	must(t, fs.UpsertTrustedIdentity(ctx, &models.AuthTrustedIdentity{Domain: "exact.example.com", Handle: "alice", Source: models.TrustedIdentitySourceAdmin}))
	// Bare-domain wildcard.
	must(t, fs.UpsertTrustedIdentity(ctx, &models.AuthTrustedIdentity{Domain: "wildcard.example.com", Handle: "", Source: models.TrustedIdentitySourceAdmin}))
	// Regex pattern.
	fs.trustedPatterns = append(fs.trustedPatterns, models.AuthTrustedDomainPattern{
		PatternID: "p1",
		Pattern:   `^.*\.regex\.example\.com$`,
	})

	admission := NewAdmission(fs)

	tests := []struct {
		name   string
		domain string
		handle string
		want   bool
	}{
		{name: "exact selector matches", domain: "exact.example.com", handle: "alice", want: true},
		{name: "exact selector wrong handle denied", domain: "exact.example.com", handle: "bob", want: false},
		{name: "bare-domain wildcard matches any handle", domain: "wildcard.example.com", handle: "anyone", want: true},
		{name: "bare-domain wildcard matches empty handle", domain: "wildcard.example.com", handle: "", want: true},
		{name: "regex matches subdomain", domain: "sub.regex.example.com", handle: "carol", want: true},
		{name: "regex does not match unrelated domain", domain: "sub.regex.example.com.evil.com", handle: "carol", want: false},
		{name: "completely unrelated domain denied", domain: "untrusted.com", handle: "mallory", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := admission.Admitted(ctx, tt.domain, tt.handle)
			if err != nil {
				t.Fatalf("Admitted() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("Admitted(%q, %q) = %v, want %v", tt.domain, tt.handle, got, tt.want)
			}
		})
	}
}

// TestAdmissionDomainPatternAnchoring is a regression test for the
// unanchored-regex admission bug: an admin-supplied pattern like
// `example\.com` (no `^`/`$`) must only admit the literal domain
// `example.com`, not `example.com.attacker.example` (regexp.MatchString's
// substring-match semantics would otherwise match it as a prefix) nor
// `notexample.com` (would otherwise match as a suffix). Login admission is a
// security boundary, so matching must always be against the full domain
// string.
func TestAdmissionDomainPatternAnchoring(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	fs.trustedPatterns = append(fs.trustedPatterns, models.AuthTrustedDomainPattern{
		PatternID: "p1",
		Pattern:   `example\.com`, // deliberately unanchored
	})
	admission := NewAdmission(fs)

	tests := []struct {
		name   string
		domain string
		want   bool
	}{
		{name: "exact match admitted", domain: "example.com", want: true},
		{name: "suffix-appended domain denied", domain: "example.com.attacker.example", want: false},
		{name: "prefix-appended domain denied", domain: "notexample.com", want: false},
		{name: "evil test suffix denied", domain: "example.com.evil.test", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := admission.Admitted(ctx, tt.domain, "someone")
			if err != nil {
				t.Fatalf("Admitted() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("Admitted(%q) = %v, want %v", tt.domain, got, tt.want)
			}
		})
	}
}

func TestAdmissionPatternCacheRefresh(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	admission := NewAdmission(fs)

	admitted, err := admission.Admitted(ctx, "late.example.com", "dave")
	if err != nil {
		t.Fatalf("Admitted() error = %v", err)
	}
	if admitted {
		t.Fatal("expected not admitted before pattern exists")
	}

	// Add a pattern after the cache has already loaded (empty) once.
	fs.trustedPatterns = append(fs.trustedPatterns, models.AuthTrustedDomainPattern{PatternID: "p2", Pattern: `^late\.example\.com$`})

	admitted, err = admission.Admitted(ctx, "late.example.com", "dave")
	if err != nil {
		t.Fatalf("Admitted() error = %v", err)
	}
	if admitted {
		t.Fatal("expected cached (stale) pattern set to still deny before Refresh")
	}

	admission.Refresh()
	admitted, err = admission.Admitted(ctx, "late.example.com", "dave")
	if err != nil {
		t.Fatalf("Admitted() error = %v", err)
	}
	if !admitted {
		t.Fatal("expected admitted after Refresh reloads the pattern set")
	}
}

func TestSeedTrustedIdentitiesFromConfig(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()

	if err := SeedTrustedIdentitiesFromConfig(ctx, fs, "alice@example.com, bare-domain.example.com ,bob@other.example.com"); err != nil {
		t.Fatalf("SeedTrustedIdentitiesFromConfig() error = %v", err)
	}

	admission := NewAdmission(fs)
	for _, tt := range []struct {
		domain, handle string
		want           bool
	}{
		{"example.com", "alice", true},
		{"example.com", "someone-else", false},
		{"bare-domain.example.com", "anyone", true},
		{"other.example.com", "bob", true},
		{"other.example.com", "eve", false},
	} {
		got, err := admission.Admitted(ctx, tt.domain, tt.handle)
		if err != nil {
			t.Fatalf("Admitted() error = %v", err)
		}
		if got != tt.want {
			t.Fatalf("Admitted(%q, %q) = %v, want %v", tt.domain, tt.handle, got, tt.want)
		}
	}

	// Idempotent: seeding again must not error or duplicate.
	if err := SeedTrustedIdentitiesFromConfig(ctx, fs, "alice@example.com"); err != nil {
		t.Fatalf("SeedTrustedIdentitiesFromConfig() second call error = %v", err)
	}
}

func TestSeedTrustedIdentitiesFromConfigEmpty(t *testing.T) {
	ctx := context.Background()
	fs := newFakeStore()
	if err := SeedTrustedIdentitiesFromConfig(ctx, fs, ""); err != nil {
		t.Fatalf("SeedTrustedIdentitiesFromConfig(\"\") error = %v", err)
	}
	if len(fs.trustedIdentities) != 0 {
		t.Fatalf("expected no trusted identities seeded from empty config, got %d", len(fs.trustedIdentities))
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
