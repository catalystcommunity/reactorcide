package config

import "github.com/catalystcommunity/app-utils-go/env"

// Rate limits (plan item D4) are overload protection, not tenant capacity.
// Each coordinator replica keeps its own buckets in memory, so N replicas
// permit up to N times the rate. A rate of 0 turns a limit off.
var (
	// WebhookRateLimitPerMinute is the number of webhook deliveries a minute
	// that one project can start (pull request and push events). The default
	// is far above the traffic of a busy repository.
	WebhookRateLimitPerMinute = env.GetEnvAsIntOrDefault("REACTORCIDE_WEBHOOK_RATE_LIMIT_PER_MINUTE", "600")
	// WebhookRateLimitBurst is the number of deliveries that can arrive at
	// the same time before the rate applies.
	WebhookRateLimitBurst = env.GetEnvAsIntOrDefault("REACTORCIDE_WEBHOOK_RATE_LIMIT_BURST", "200")
	// APIRateLimitPerMinute is the number of authenticated REST requests a
	// minute for one principal (API token). Off by default: workers, jobs,
	// and automation use the REST API at rates that are not yet measured.
	APIRateLimitPerMinute = env.GetEnvAsIntOrDefault("REACTORCIDE_API_RATE_LIMIT_PER_MINUTE", "0")
	// APIRateLimitBurst is the burst of the API limit.
	APIRateLimitBurst = env.GetEnvAsIntOrDefault("REACTORCIDE_API_RATE_LIMIT_BURST", "300")
	// FederationRateLimitPerMinute is the number of token exchange requests
	// (POST /api/v1/federation/token) a minute from one source address. The
	// source is the TCP peer. Behind a proxy or an ingress, every caller has
	// the address of the proxy, so this limit is then for all callers; set
	// TrustedProxyCIDRs to use the X-Forwarded-For address instead.
	FederationRateLimitPerMinute = env.GetEnvAsIntOrDefault("REACTORCIDE_FEDERATION_RATE_LIMIT_PER_MINUTE", "300")
	// FederationRateLimitBurst is the burst of the source address limit.
	FederationRateLimitBurst = env.GetEnvAsIntOrDefault("REACTORCIDE_FEDERATION_RATE_LIMIT_BURST", "100")
	// FederationIssuerRateLimitPerMinute is the number of tokens a minute
	// that one federation issuer of an organization can get. Only a verified
	// external token counts, so a caller cannot use the limit of an issuer
	// with a false "iss" claim.
	FederationIssuerRateLimitPerMinute = env.GetEnvAsIntOrDefault("REACTORCIDE_FEDERATION_ISSUER_RATE_LIMIT_PER_MINUTE", "600")
	// FederationIssuerRateLimitBurst is the burst of the issuer limit.
	FederationIssuerRateLimitBurst = env.GetEnvAsIntOrDefault("REACTORCIDE_FEDERATION_ISSUER_RATE_LIMIT_BURST", "200")
	// TrustedProxyCIDRs is a comma-separated list of CIDRs (or addresses) of
	// the proxies and ingresses in front of the coordinator. Only when the
	// TCP peer is in this list does the federation source limit use
	// X-Forwarded-For: the right-most address that is not a trusted proxy.
	// Empty (the default): the source is the TCP peer, and X-Forwarded-For
	// is not used, because a caller can set it.
	TrustedProxyCIDRs = env.GetEnvOrDefault("REACTORCIDE_TRUSTED_PROXY_CIDRS", "")
)
