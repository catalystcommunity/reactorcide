package config

import (
	"fmt"
	"strings"

	"github.com/catalystcommunity/app-utils-go/env"
)

// UI auth mode values for REACTORCIDE_UI_AUTH_MODE. See UI_AUTH_PLAN.md,
// "Auth modes" for the full semantics of each.
const (
	UIAuthModeNone    = "none"
	UIAuthModeLocalRP = "local-rp"
	UIAuthModeRP      = "rp"
)

var (
	// UIAuthMode selects the management-UI login mode: "none" (no login,
	// the default), "local-rp" (DNS-less LinkKeys local-RP identity), or
	// "rp" (this coordinator is the app of a full LinkKeys RP deployment).
	UIAuthMode = env.GetEnvOrDefault("REACTORCIDE_UI_AUTH_MODE", UIAuthModeNone)

	// LinkKeysRPAddr is the host:port TCP CSIL-RPC address of the
	// coordinator's own configured LinkKeys RP server. Required when
	// UIAuthMode == "rp".
	LinkKeysRPAddr = env.GetEnvOrDefault("REACTORCIDE_LINKKEYS_RP_ADDR", "")

	// LinkKeysRPFingerprints is a comma-separated set of pinned SPKI
	// SHA-256 fingerprints for the RP server's TLS certificate (mirrors the
	// RP's own `_linkkeys` DNS TXT record). Required when UIAuthMode == "rp".
	LinkKeysRPFingerprints = env.GetEnvOrDefault("REACTORCIDE_LINKKEYS_RP_FINGERPRINTS", "")

	// LinkKeysRPAPIKey is the RP server's API key, presented only on first
	// boot: once accepted it is persisted encrypted in auth_credentials
	// (name="rp_api_key") and the env var can be dropped, mirroring the
	// master-keys env-or-DB convention (see internal/secrets/master_keys.go).
	LinkKeysRPAPIKey = env.GetEnvOrDefault("REACTORCIDE_LINKKEYS_RP_API_KEY", "")

	// LocalRPName is the display name (design doc: "app_name", audit/
	// display metadata, never an identity input) used when generating this
	// coordinator's local-RP identity bundle. Required when
	// UIAuthMode == "local-rp".
	LocalRPName = env.GetEnvOrDefault("REACTORCIDE_LOCAL_RP_NAME", "")

	// FirstAdmin is an identity selector ("handle@domain" or "uuid@domain")
	// that is granted global admin the first time it completes a login,
	// provided no global admin exists yet.
	FirstAdmin = env.GetEnvOrDefault("REACTORCIDE_FIRST_ADMIN", "")

	// BootstrapAdminToken, if set, allows a one-time bootstrap admin
	// session (see internal/auth.LoginService.BootstrapAdminSession) while
	// zero global admins exist, so initial setup can be done from the UI
	// without a LinkKeys login.
	BootstrapAdminToken = env.GetEnvOrDefault("REACTORCIDE_BOOTSTRAP_ADMIN_TOKEN", "")

	// TrustedIdentities seeds the login admission list: a comma-separated
	// list of "[handle@]domain" selectors, upserted as source="config"
	// auth_trusted_identities rows at startup. A bare domain admits any
	// handle at that domain.
	TrustedIdentities = env.GetEnvOrDefault("REACTORCIDE_TRUSTED_IDENTITIES", "")

	// UICallbackURL is the web UI's public base URL (the origin browsers
	// reach the webapp on, e.g. https://ci.example.com), used to build the
	// LinkKeys login callback URL (UICallbackURL + "/app/auth/callback",
	// the webapp's callback route) passed to LoginBackend.BeginLogin. The
	// CSIL BeginLogin op has no callback-url field of its own (the callback
	// route is fixed coordinator-side config, not a per-request value, so an
	// attacker cannot redirect the encrypted token elsewhere) — see Task G's
	// implementation notes in UI_AUTH_PLAN.md. Required for begin-login to
	// succeed in local-rp/rp mode; unused in mode none.
	UICallbackURL = env.GetEnvOrDefault("REACTORCIDE_UI_CALLBACK_URL", "")
)

// ValidateUIAuthMode checks that REACTORCIDE_UI_AUTH_MODE holds one of the
// recognized values (none|local-rp|rp) and that the config required by that
// mode is present. Call this once at startup; a misconfigured mode should
// fail fast rather than silently falling back to "none".
func ValidateUIAuthMode() error {
	switch UIAuthMode {
	case UIAuthModeNone:
		return nil
	case UIAuthModeLocalRP:
		if strings.TrimSpace(LocalRPName) == "" {
			return fmt.Errorf("REACTORCIDE_UI_AUTH_MODE=local-rp requires REACTORCIDE_LOCAL_RP_NAME")
		}
		return nil
	case UIAuthModeRP:
		if strings.TrimSpace(LinkKeysRPAddr) == "" {
			return fmt.Errorf("REACTORCIDE_UI_AUTH_MODE=rp requires REACTORCIDE_LINKKEYS_RP_ADDR")
		}
		if len(SplitTrustedFingerprints()) == 0 {
			return fmt.Errorf("REACTORCIDE_UI_AUTH_MODE=rp requires REACTORCIDE_LINKKEYS_RP_FINGERPRINTS")
		}
		return nil
	default:
		return fmt.Errorf("invalid REACTORCIDE_UI_AUTH_MODE %q: must be one of %q, %q, %q",
			UIAuthMode, UIAuthModeNone, UIAuthModeLocalRP, UIAuthModeRP)
	}
}

// SplitTrustedFingerprints parses REACTORCIDE_LINKKEYS_RP_FINGERPRINTS into
// its comma-separated fingerprint values, trimming whitespace and skipping
// empty entries.
func SplitTrustedFingerprints() []string {
	return SplitCommaList(LinkKeysRPFingerprints)
}

// SplitCommaList splits a comma-separated env value into trimmed, non-empty
// entries. Shared helper for every comma-separated UI-auth env var
// (fingerprints, trusted identities) so parsing stays consistent.
func SplitCommaList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
