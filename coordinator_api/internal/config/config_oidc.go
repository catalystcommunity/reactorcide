package config

import "github.com/catalystcommunity/app-utils-go/env"

var (
	// OIDCIssuerURL is the "iss" value of the Reactorcide OIDC issuer for
	// jobs (docs/oidc.md). Empty turns the issuer off: the discovery
	// document, the JWKS, and the job token route are not served.
	OIDCIssuerURL = env.GetEnvOrDefault("REACTORCIDE_OIDC_ISSUER_URL", "")
	// OIDCSigningAlgorithm is the algorithm of new signing keys: ES256
	// (default) or RS256.
	OIDCSigningAlgorithm = env.GetEnvOrDefault("REACTORCIDE_OIDC_SIGNING_ALGORITHM", "ES256")
	// OIDCKeyRotationDays starts an automatic key rotation when the newest
	// signing key is older than this. 0 turns automatic rotation off.
	OIDCKeyRotationDays = env.GetEnvAsIntOrDefault("REACTORCIDE_OIDC_KEY_ROTATION_DAYS", "90")

	// AllowInsecureTransport permits HTTP for the JWKS and discovery fetches
	// of federation issuers, and an HTTP OIDC issuer URL. Only the explicit
	// "serve --allow-insecure-transport" option sets it. No environment
	// variable can set it (DESIGN.md transport rule).
	AllowInsecureTransport bool
)
