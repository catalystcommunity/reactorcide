package auth

import (
	"context"
	"fmt"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// LoadOrBootstrapRPAPIKey returns this coordinator's LinkKeys RP API key for
// rp mode: env-or-DB, mirroring the master-keys convention
// (internal/secrets/master_keys.go's LoadOrCreateMasterKeys doc comment).
// If REACTORCIDE_LINKKEYS_RP_API_KEY is set, it is (re-)persisted encrypted
// under models.AuthCredentialRPAPIKey and returned — so an operator can set
// the env var once, let it persist, and drop it from the environment on
// subsequent boots. Otherwise the previously persisted key is loaded.
func LoadOrBootstrapRPAPIKey(ctx context.Context, credStore CredentialStore, keys *secrets.MasterKeyManager) (string, error) {
	if key := strings.TrimSpace(config.LinkKeysRPAPIKey); key != "" {
		if err := StoreCredential(ctx, credStore, keys, models.AuthCredentialRPAPIKey, []byte(key)); err != nil {
			return "", fmt.Errorf("auth: persisting rp api key: %w", err)
		}
		return key, nil
	}

	raw, err := LoadCredential(ctx, credStore, keys, models.AuthCredentialRPAPIKey)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
