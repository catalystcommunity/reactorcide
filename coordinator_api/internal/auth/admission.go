package auth

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// AdmissionStore is the narrow store surface Admission and
// SeedTrustedIdentitiesFromConfig consume, satisfied by Task A's
// postgres_store/auth_operations.go.
type AdmissionStore interface {
	// TrustedIdentityExists reports whether an exact auth_trusted_identities
	// row admits (domain, handle) — a row with handle="" is a bare-domain
	// wildcard that admits any handle at that domain.
	TrustedIdentityExists(ctx context.Context, domain, handle string) (bool, error)
	// ListTrustedDomainPatterns lists every auth_trusted_domain_patterns row.
	ListTrustedDomainPatterns(ctx context.Context) ([]models.AuthTrustedDomainPattern, error)
	// UpsertTrustedIdentity creates or replaces a trusted-identity row
	// keyed by (domain, handle).
	UpsertTrustedIdentity(ctx context.Context, identity *models.AuthTrustedIdentity) error
}

// Admission answers "may (domain, handle) log in?" against the trusted-
// identity admission list: exact/bare-domain rows in auth_trusted_identities
// (delegated straight to the store's indexed TrustedIdentityExists), OR any
// compiled auth_trusted_domain_patterns regex matching the domain. The
// compiled pattern set is cached in memory (patterns are admin-managed and
// change rarely) until Refresh is called.
type Admission struct {
	store AdmissionStore

	mu       sync.RWMutex
	compiled []*regexp.Regexp
	loaded   bool
}

// NewAdmission constructs an Admission backed by store.
func NewAdmission(store AdmissionStore) *Admission {
	return &Admission{store: store}
}

// Admitted reports whether (domain, handle) is on the admission list: an
// exact/bare-domain auth_trusted_identities match, or a matching
// auth_trusted_domain_patterns regex.
func (a *Admission) Admitted(ctx context.Context, domain, handle string) (bool, error) {
	exact, err := a.store.TrustedIdentityExists(ctx, domain, handle)
	if err != nil {
		return false, fmt.Errorf("auth: checking trusted identity: %w", err)
	}
	if exact {
		return true, nil
	}

	patterns, err := a.compiledPatterns(ctx)
	if err != nil {
		return false, err
	}
	for _, re := range patterns {
		if re.MatchString(domain) {
			return true, nil
		}
	}
	return false, nil
}

// Refresh drops the cached compiled domain-pattern set so the next Admitted
// call reloads auth_trusted_domain_patterns from the store. Call after an
// admin adds/removes a pattern.
func (a *Admission) Refresh() {
	a.mu.Lock()
	a.compiled = nil
	a.loaded = false
	a.mu.Unlock()
}

func (a *Admission) compiledPatterns(ctx context.Context) ([]*regexp.Regexp, error) {
	a.mu.RLock()
	if a.loaded {
		compiled := a.compiled
		a.mu.RUnlock()
		return compiled, nil
	}
	a.mu.RUnlock()

	rows, err := a.store.ListTrustedDomainPatterns(ctx)
	if err != nil {
		return nil, fmt.Errorf("auth: listing trusted domain patterns: %w", err)
	}
	compiled := make([]*regexp.Regexp, 0, len(rows))
	for _, row := range rows {
		re, err := compileAnchoredDomainPattern(row.Pattern)
		if err != nil {
			// A row that fails to compile here means ValidateDomainPattern
			// was bypassed when it was written (or a regexp package
			// upgrade changed acceptance). Skip the one bad row rather
			// than fail every admission check because of it.
			continue
		}
		compiled = append(compiled, re)
	}

	a.mu.Lock()
	a.compiled = compiled
	a.loaded = true
	a.mu.Unlock()
	return compiled, nil
}

// ValidateDomainPattern compiles pattern as an RE2 (Go regexp) pattern,
// returning an error if it doesn't compile. Callers that write
// auth_trusted_domain_patterns rows (the add-trusted-domain-pattern CSIL op,
// Wave 3) must call this before persisting a pattern.
//
// Validation compiles the pattern wrapped exactly the way compiledPatterns
// wraps it for matching (full-string anchored — see compileAnchoredDomainPattern),
// so a pattern that would fail to compile once anchored (e.g. unbalanced
// grouping that only breaks when wrapped) is rejected at write time rather
// than silently dropped later by compiledPatterns' skip-on-compile-error
// fallback.
func ValidateDomainPattern(pattern string) error {
	if strings.TrimSpace(pattern) == "" {
		return fmt.Errorf("auth: domain pattern must not be empty")
	}
	if _, err := compileAnchoredDomainPattern(pattern); err != nil {
		return fmt.Errorf("auth: invalid domain pattern %q: %w", pattern, err)
	}
	return nil
}

// compileAnchoredDomainPattern compiles pattern as a Go RE2 regexp, forcing
// full-string matching by wrapping it as `^(?:` + pattern + `)$`. This is a
// security boundary (login admission), so an unanchored pattern like
// `example\.com` must not admit `example.com.attacker.example` or
// `notexample.com` via regexp.MatchString's substring-match semantics.
// Wrapping in a non-capturing group keeps the anchors from interacting badly
// with top-level alternation in pattern (e.g. `a|b` becomes `^(?:a|b)$`, not
// `^a|b$`). Patterns that are already anchored by the author (`^...$`) still
// compile fine: RE2 treats redundant `^`/`$` anchors as harmless.
func compileAnchoredDomainPattern(pattern string) (*regexp.Regexp, error) {
	return regexp.Compile(`^(?:` + pattern + `)$`)
}

// ParseSelector splits a "[handle@]domain" identity selector (the shape
// used throughout UI_AUTH_PLAN.md: REACTORCIDE_TRUSTED_IDENTITIES,
// REACTORCIDE_FIRST_ADMIN, and login requests) into its handle and domain
// parts. A bare domain (no "@") yields handle="". Splits on the last "@" so
// a handle that itself contains "@" (unusual, but not disallowed by
// LinkKeys) still parses the trailing domain correctly.
func ParseSelector(selector string) (handle, domain string, err error) {
	s := strings.TrimSpace(selector)
	if s == "" {
		return "", "", fmt.Errorf("auth: identity selector must not be empty")
	}
	if idx := strings.LastIndex(s, "@"); idx >= 0 {
		handle = s[:idx]
		domain = s[idx+1:]
	} else {
		domain = s
	}
	if domain == "" {
		return "", "", fmt.Errorf("auth: identity selector %q has an empty domain", selector)
	}
	return handle, domain, nil
}

// SeedTrustedIdentitiesFromConfig parses raw (REACTORCIDE_TRUSTED_IDENTITIES:
// a comma-separated list of "[handle@]domain" selectors) and upserts each as
// a source=config auth_trusted_identities row. Call once at startup;
// idempotent (upsert on the (domain, handle) primary key) so it's safe to
// call on every boot even if an admin has since edited the same rows via the
// UI (a config-seeded row's source flips back to "config" on the next boot,
// matching the "admin-managed" vs "config-managed" distinction the schema
// draws — reseeding does not delete rows an admin added that aren't in the
// current env value).
func SeedTrustedIdentitiesFromConfig(ctx context.Context, store AdmissionStore, raw string) error {
	for _, sel := range splitCommaList(raw) {
		handle, domain, err := ParseSelector(sel)
		if err != nil {
			return fmt.Errorf("auth: REACTORCIDE_TRUSTED_IDENTITIES: %w", err)
		}
		if err := store.UpsertTrustedIdentity(ctx, &models.AuthTrustedIdentity{
			Domain: domain,
			Handle: handle,
			Source: models.TrustedIdentitySourceConfig,
		}); err != nil {
			return fmt.Errorf("auth: seeding trusted identity %q: %w", sel, err)
		}
	}
	return nil
}

func splitCommaList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
