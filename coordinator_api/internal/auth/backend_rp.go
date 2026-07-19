package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	localrp "github.com/catalystcommunity/linkkeys/sdks/local-rp/go"
	api "github.com/catalystcommunity/linkkeys/sdks/local-rp/go/generated"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
)

// RPTransport is the CSIL-RPC transport seam RPBackend uses to reach the
// coordinator's own configured LinkKeys RP server (the "Rp" CSIL service:
// sign-request, decrypt-token, verify-assertion, userinfo-fetch — see
// linkkeys' sdks/local-rp/go/example.md, which RPBackend mirrors).
// Production uses newTCPRPTransport (TLS pinned to
// config.LinkKeysRPFingerprints, API key in the CSIL-RPC envelope's auth
// field); tests inject a fake so no real network/TLS is exercised.
type RPTransport interface {
	// Call performs one API-key-authenticated CSIL-RPC call to the "Rp"
	// service's op and returns the raw response payload bytes. Implementations
	// must surface a non-nil error for both transport failures and CSIL
	// ServiceError responses (mirroring rpctransport.RpcResponse.AsTransportError).
	Call(ctx context.Context, op string, payload []byte) ([]byte, error)
}

// RPBackend implements LoginBackend for REACTORCIDE_UI_AUTH_MODE=rp: a
// hand-written glue client for the regular (DNS-pinned) LinkKeys RP flow.
// There is no packaged Go SDK for this mode (see example.md's "Why there's
// no packaged client"); this mirrors that document's rpclient.go.
type RPBackend struct {
	transport RPTransport
	dns       localrp.DnsResolver
	now       func() time.Time
}

// rpPending is the JSON-serialized pending-login state persisted between
// BeginLogin and CompleteLogin: the nonce this RP minted and the domain the
// login was addressed to — the same facts example.md's own pendingLogin
// struct tracks, so CompleteLogin can re-check them against what
// verify-assertion reports.
type rpPending struct {
	Nonce      string `json:"nonce"`
	UserDomain string `json:"user_domain"`
}

// NewRPBackend constructs the rp-mode backend. apiKey is the decrypted RP
// API key (see LoadOrBootstrapRPAPIKey), which authenticates every Rp
// CSIL-RPC call via the envelope's auth field. transport, if nil, defaults
// to a TLS-pinned TCP CSIL-RPC client dialing config.LinkKeysRPAddr, pinned
// to config.LinkKeysRPFingerprints.
func NewRPBackend(apiKey string, transport RPTransport) (*RPBackend, error) {
	if strings.TrimSpace(config.LinkKeysRPAddr) == "" {
		return nil, fmt.Errorf("auth: REACTORCIDE_LINKKEYS_RP_ADDR must be set for rp mode")
	}
	fingerprints := config.SplitTrustedFingerprints()
	if len(fingerprints) == 0 {
		return nil, fmt.Errorf("auth: REACTORCIDE_LINKKEYS_RP_FINGERPRINTS must be set for rp mode")
	}
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("auth: no RP API key available (set REACTORCIDE_LINKKEYS_RP_API_KEY on first boot)")
	}
	if transport == nil {
		transport = newTCPRPTransport(config.LinkKeysRPAddr, fingerprints, apiKey)
	}
	return &RPBackend{transport: transport, dns: localrp.DefaultDNSResolver(), now: time.Now}, nil
}

func (b *RPBackend) Mode() Mode { return ModeRP }

// BeginLogin is steps 1-2 of example.md's flow: Rp/sign-request, then build
// the browser-redirect URL to the user's chosen LinkKeys domain
// (identitySelector's domain; its handle, if present, rides along as an
// optional user_hint).
func (b *RPBackend) BeginLogin(ctx context.Context, identitySelector, callbackURL string) (string, []byte, error) {
	handle, domain, err := ParseSelector(identitySelector)
	if err != nil {
		return "", nil, err
	}

	nonce, err := generateToken()
	if err != nil {
		return "", nil, err
	}

	signResp, err := b.transport.Call(ctx, "sign-request", api.EncodeRpSignRequest(api.RpSignRequest{
		CallbackUrl: callbackURL,
		Nonce:       nonce,
	}))
	if err != nil {
		return "", nil, fmt.Errorf("auth: rp sign-request: %w", err)
	}
	signed, err := api.DecodeRpSignResponse(signResp)
	if err != nil {
		return "", nil, fmt.Errorf("auth: decode sign-request response: %w", err)
	}

	u := url.URL{Scheme: "https", Host: domain, Path: "/auth/authorize"}
	q := u.Query()
	q.Set("signed_request", signed.SignedRequest)
	if handle != "" {
		q.Set("user_hint", handle)
	}
	u.RawQuery = q.Encode()

	pendingBlob, err := json.Marshal(rpPending{Nonce: nonce, UserDomain: domain})
	if err != nil {
		return "", nil, fmt.Errorf("auth: marshal pending rp login: %w", err)
	}
	return u.String(), pendingBlob, nil
}

// CompleteLogin is steps 3-6 of example.md's flow: Rp/decrypt-token,
// Rp/verify-assertion (MUST check Verified==true — a nil error only means
// the call round-tripped, not that the assertion is trustworthy),
// nonce/domain re-checks against the pending state, then a best-effort
// Rp/userinfo-fetch for handle/display_name/claims.
func (b *RPBackend) CompleteLogin(ctx context.Context, pendingBlob []byte, arrivedURL string) (*VerifiedIdentity, error) {
	var pending rpPending
	if err := json.Unmarshal(pendingBlob, &pending); err != nil {
		return nil, fmt.Errorf("auth: unmarshal pending rp login: %w", err)
	}

	encryptedToken, err := extractEncryptedToken(arrivedURL)
	if err != nil {
		return nil, err
	}

	decResp, err := b.transport.Call(ctx, "decrypt-token", api.EncodeRpDecryptRequest(api.RpDecryptRequest{EncryptedToken: encryptedToken}))
	if err != nil {
		return nil, fmt.Errorf("auth: rp decrypt-token: %w", err)
	}
	decoded, err := api.DecodeRpDecryptResponse(decResp)
	if err != nil {
		return nil, fmt.Errorf("auth: decode decrypt-token response: %w", err)
	}

	verResp, err := b.transport.Call(ctx, "verify-assertion", api.EncodeRpVerifyRequest(api.RpVerifyRequest{
		SignedAssertion: decoded.SignedAssertion,
		ExpectedDomain:  pending.UserDomain,
	}))
	if err != nil {
		return nil, fmt.Errorf("auth: rp verify-assertion: %w", err)
	}
	verifyResp, err := api.DecodeRpVerifyResponse(verResp)
	if err != nil {
		return nil, fmt.Errorf("auth: decode verify-assertion response: %w", err)
	}
	// MUST check Verified == true: a nil error above only means the call
	// round-tripped, not that the assertion is trustworthy (example.md,
	// step 5's explicit warning).
	if !verifyResp.Verified {
		return nil, ErrAssertionNotVerified
	}
	assertion := verifyResp.Assertion
	if assertion.Domain != pending.UserDomain {
		return nil, fmt.Errorf("auth: rp assertion domain mismatch: expected %s, got %s", pending.UserDomain, assertion.Domain)
	}
	if assertion.Nonce != pending.Nonce {
		return nil, fmt.Errorf("auth: rp assertion nonce mismatch (possible replay)")
	}

	claims := map[string]string{}
	displayName := ""
	if assertion.DisplayName != nil {
		displayName = *assertion.DisplayName
	}

	apiBase := resolveAPIBase(b.dns, pending.UserDomain)
	if info, err := b.userInfoFetch(ctx, decoded.SignedAssertion, apiBase, pending.UserDomain); err == nil {
		for _, c := range info.Claims {
			claims[c.ClaimType] = string(c.ClaimValue)
		}
		if info.DisplayName != "" {
			displayName = info.DisplayName
		}
	}
	// userinfo-fetch failures are non-fatal to CompleteLogin: identity has
	// already been verified above; claims (handle, email, ...) are best
	// effort. A missing handle simply falls back to VerifiedIdentity.Subject
	// for username purposes (see usernameFor in login_service.go).

	return &VerifiedIdentity{
		Subject:     assertion.UserId,
		Domain:      assertion.Domain,
		Handle:      claims["handle"],
		DisplayName: displayName,
		Claims:      claims,
	}, nil
}

func (b *RPBackend) userInfoFetch(ctx context.Context, signedAssertion, apiBase, domain string) (api.UserInfo, error) {
	resp, err := b.transport.Call(ctx, "userinfo-fetch", api.EncodeRpUserInfoRequest(api.RpUserInfoRequest{
		Token:   signedAssertion,
		ApiBase: apiBase,
		Domain:  domain,
	}))
	if err != nil {
		return api.UserInfo{}, err
	}
	return api.DecodeUserInfo(resp)
}

// resolveAPIBase looks up domain's `_linkkeys_apis` TXT record for its
// published HTTPS API base, falling back to `https://<domain>` if the
// domain publishes no override — mirrors example.md's resolveAPIBase,
// reusing the same DNS-resolver seam and parsing helpers the local-rp SDK
// exports for its own purposes.
func resolveAPIBase(dns localrp.DnsResolver, domain string) string {
	fallback := "https://" + domain
	txts, err := dns.TxtLookup(localrp.LinkKeysApisDNSName(domain))
	if err != nil {
		return fallback
	}
	for _, txt := range txts {
		apis, err := localrp.ParseLinkKeysApisTXT(txt)
		if err != nil || apis.HTTPSBase == nil {
			continue
		}
		return *apis.HTTPSBase
	}
	return fallback
}
