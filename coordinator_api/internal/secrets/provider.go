// Package secrets provides secret storage abstraction with multiple backend support.
package secrets

import (
	"context"
	"errors"
	"fmt"
)

// Provider defines the interface for secret storage backends.
// All implementations must be safe for concurrent use.
type Provider interface {
	// Get retrieves a secret value. Returns empty string if not found.
	Get(ctx context.Context, path, key string) (string, error)

	// Set stores a secret value, creating or updating as needed.
	Set(ctx context.Context, path, key, value string) error

	// Delete removes a secret. Returns true if it existed.
	Delete(ctx context.Context, path, key string) (bool, error)

	// ListKeys returns all keys under a path.
	ListKeys(ctx context.Context, path string) ([]string, error)

	// ListPaths returns all paths that have secrets.
	ListPaths(ctx context.Context) ([]string, error)

	// GetMulti retrieves multiple secrets efficiently.
	// Returns a map of "path:key" -> value.
	GetMulti(ctx context.Context, refs []SecretRef) (map[string]string, error)
}

// ProviderConfig holds configuration for creating providers.
type ProviderConfig struct {
	// Type selects the provider: "local", "database", "openbao", etc.
	Type string

	// For local provider
	LocalPath string // Override default XDG path
	Password  string // Master password for encryption

	// For database provider
	OrgID         string // User/Organization ID for namespacing
	EncryptionKey []byte // Per-org encryption key (decrypted)

	// For external providers (future)
	Endpoint string
	Token    string
}

// NewProvider creates a provider based on configuration.
func NewProvider(cfg ProviderConfig) (Provider, error) {
	switch cfg.Type {
	case "", "local":
		return NewLocalProvider(cfg.LocalPath, cfg.Password)
	case "database":
		return nil, errors.New("database provider requires gorm.DB - use NewDatabaseProvider directly")
	default:
		return nil, fmt.Errorf("unknown provider type: %s", cfg.Type)
	}
}

// LocalProvider wraps the existing Storage to implement Provider.
type LocalProvider struct {
	storage  *Storage
	password string
}

// NewLocalProvider creates a LocalProvider wrapping the existing Storage.
// If path is empty, uses the default XDG-compliant path.
func NewLocalProvider(path, password string) (*LocalProvider, error) {
	var storage *Storage
	if path != "" {
		storage = NewStorageWithPath(path)
	} else {
		storage = NewStorage()
	}

	if !storage.IsInitialized() {
		return nil, ErrNotInitialized
	}

	return &LocalProvider{storage: storage, password: password}, nil
}

// Get retrieves a secret value.
func (p *LocalProvider) Get(ctx context.Context, path, key string) (string, error) {
	return p.storage.Get(path, key, p.password)
}

// Set stores a secret value.
func (p *LocalProvider) Set(ctx context.Context, path, key, value string) error {
	return p.storage.Set(path, key, value, p.password)
}

// Delete removes a secret.
func (p *LocalProvider) Delete(ctx context.Context, path, key string) (bool, error) {
	return p.storage.Delete(path, key, p.password)
}

// ListKeys returns all keys under a path.
func (p *LocalProvider) ListKeys(ctx context.Context, path string) ([]string, error) {
	return p.storage.ListKeys(path, p.password)
}

// ListPaths returns all paths that have secrets.
func (p *LocalProvider) ListPaths(ctx context.Context) ([]string, error) {
	return p.storage.ListPaths(p.password)
}

// GetMulti retrieves multiple secrets efficiently.
// Returns a map of "path:key" -> value.
func (p *LocalProvider) GetMulti(ctx context.Context, refs []SecretRef) (map[string]string, error) {
	// Use the underlying GetMulti which does single key derivation
	result, err := p.storage.GetMulti(refs, p.password)
	if err != nil {
		return nil, err
	}

	// Convert to "path:key" -> value format as specified in interface
	converted := make(map[string]string, len(result))
	for _, ref := range refs {
		mapKey := fmt.Sprintf("%s:%s", ref.Path, ref.Key)
		if val, ok := result[ref.Key]; ok {
			converted[mapKey] = val
		} else {
			converted[mapKey] = ""
		}
	}
	return converted, nil
}

// Ensure LocalProvider implements Provider interface
var _ Provider = (*LocalProvider)(nil)
