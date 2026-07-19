package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	localrp "github.com/catalystcommunity/linkkeys/sdks/local-rp/go"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// LocalRPBackend implements LoginBackend for
// REACTORCIDE_UI_AUTH_MODE=local-rp using
// github.com/catalystcommunity/linkkeys/sdks/local-rp/go (package localrp):
// a DNS-less local-RP identity, generated once and persisted encrypted via
// CredentialStore.
type LocalRPBackend struct {
	identity *localrp.LocalRpKeyMaterial
	now      func() time.Time
}

// NewLocalRPBackend loads this coordinator's local-RP identity bundle
// (models.AuthCredentialLocalRPIdentity) via CredentialStore, generating and
// persisting a fresh one on first boot (REACTORCIDE_LOCAL_RP_NAME as the
// app_name). Refuses to start with an expired bundle — callers must
// generate a new identity (which means re-approval at every LinkKeys domain
// that previously trusted this RP; there is no rotation-with-continuity
// story for local-RP identities) rather than silently logging in with one.
func NewLocalRPBackend(ctx context.Context, credStore CredentialStore, keys *secrets.MasterKeyManager) (*LocalRPBackend, error) {
	if strings.TrimSpace(config.LocalRPName) == "" {
		return nil, fmt.Errorf("auth: REACTORCIDE_LOCAL_RP_NAME must be set for local-rp mode")
	}

	var identity *localrp.LocalRpKeyMaterial

	raw, err := LoadCredential(ctx, credStore, keys, models.AuthCredentialLocalRPIdentity)
	switch {
	case err == nil:
		identity, err = localrp.LocalRpIdentityFromBytes(raw)
		if err != nil {
			return nil, fmt.Errorf("auth: stored local-rp identity bundle is corrupt: %w", err)
		}
	case errors.Is(err, store.ErrNotFound):
		identity, err = localrp.GenerateLocalRpIdentity(localrp.GenerateLocalRpIdentityConfig{
			AppName: config.LocalRPName,
			Now:     time.Now(),
		})
		if err != nil {
			return nil, fmt.Errorf("auth: generating local-rp identity: %w", err)
		}
		if err := StoreCredential(ctx, credStore, keys, models.AuthCredentialLocalRPIdentity, localrp.LocalRpIdentityToBytes(identity)); err != nil {
			return nil, fmt.Errorf("auth: persisting local-rp identity: %w", err)
		}
	default:
		return nil, fmt.Errorf("auth: loading local-rp identity: %w", err)
	}

	status, err := localrp.CheckExpirations(identity, time.Now())
	if err != nil {
		return nil, fmt.Errorf("auth: checking local-rp identity expiration: %w", err)
	}
	if status.Level == localrp.ExpirationExpired {
		return nil, fmt.Errorf("auth: local-rp identity bundle expired at %s; generate a new one (fingerprint %s)", status.ExpiresAt, identity.Fingerprint)
	}

	return &LocalRPBackend{identity: identity, now: time.Now}, nil
}

// Fingerprint returns this backend's local-RP identity fingerprint (the
// hex-encoded sha256 of its signing public key) — the value a domain admin
// approves via `linkkeys local-rp approve <fingerprint>`.
func (b *LocalRPBackend) Fingerprint() string {
	return b.identity.Fingerprint
}

func (b *LocalRPBackend) Mode() Mode { return ModeLocalRP }

func (b *LocalRPBackend) BeginLogin(_ context.Context, identitySelector, callbackURL string) (string, []byte, error) {
	_, domain, err := ParseSelector(identitySelector)
	if err != nil {
		return "", nil, err
	}

	redirect, pending, err := localrp.BeginLocalLogin(localrp.BeginLocalLoginConfig{
		KeyMaterial: b.identity,
		CallbackURL: callbackURL,
		UserDomain:  domain,
		Now:         b.now(),
	})
	if err != nil {
		return "", nil, fmt.Errorf("auth: local-rp begin login: %w", err)
	}

	blob, err := json.Marshal(pending)
	if err != nil {
		return "", nil, fmt.Errorf("auth: marshal pending local-rp login: %w", err)
	}
	return redirect.RedirectURL, blob, nil
}

func (b *LocalRPBackend) CompleteLogin(_ context.Context, pendingBlob []byte, arrivedURL string) (*VerifiedIdentity, error) {
	var pending localrp.PendingLogin
	if err := json.Unmarshal(pendingBlob, &pending); err != nil {
		return nil, fmt.Errorf("auth: unmarshal pending local-rp login: %w", err)
	}

	encryptedToken, err := extractEncryptedToken(arrivedURL)
	if err != nil {
		return nil, err
	}

	verified, err := localrp.CompleteLocalLogin(localrp.CompleteLocalLoginConfig{
		KeyMaterial:    b.identity,
		Pending:        &pending,
		EncryptedToken: encryptedToken,
		ArrivedURL:     arrivedURL,
		Now:            b.now(),
	})
	if err != nil {
		return nil, fmt.Errorf("auth: local-rp complete login: %w", err)
	}

	claims := make(map[string]string, len(verified.Claims))
	for _, c := range verified.Claims {
		claims[c.ClaimType] = string(c.ClaimValue)
	}

	return &VerifiedIdentity{
		Subject:     verified.UserID,
		Domain:      verified.UserDomain,
		Handle:      claims["handle"],
		DisplayName: claims["display_name"],
		Claims:      claims,
	}, nil
}

// extractEncryptedToken pulls the `encrypted_token` query parameter out of
// the URL the callback actually arrived at. Shared by both LinkKeys login
// backends (local-rp and rp): both callback shapes carry the token the same
// way.
func extractEncryptedToken(arrivedURL string) (string, error) {
	u, err := url.Parse(arrivedURL)
	if err != nil {
		return "", fmt.Errorf("auth: parsing arrived callback URL: %w", err)
	}
	token := u.Query().Get("encrypted_token")
	if token == "" {
		return "", fmt.Errorf("auth: arrived callback URL is missing encrypted_token")
	}
	return token, nil
}
