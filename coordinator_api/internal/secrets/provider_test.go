package secrets

import (
	"context"
	"os"
	"testing"
)

func setupTestLocalProvider(t *testing.T) (*LocalProvider, func()) {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "provider-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	password := "testpassword"

	// Initialize storage first
	storage := NewStorageWithPath(tempDir)
	if err := storage.Init(password, false); err != nil {
		os.RemoveAll(tempDir)
		t.Fatalf("failed to init storage: %v", err)
	}

	provider, err := NewLocalProvider(tempDir, password)
	if err != nil {
		os.RemoveAll(tempDir)
		t.Fatalf("failed to create LocalProvider: %v", err)
	}

	cleanup := func() {
		os.RemoveAll(tempDir)
	}
	return provider, cleanup
}

func TestNewLocalProviderNotInitialized(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "provider-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Try to create provider without initializing storage
	_, err = NewLocalProvider(tempDir, "password")
	if err != ErrNotInitialized {
		t.Errorf("expected ErrNotInitialized, got %v", err)
	}
}

func TestLocalProviderGetSet(t *testing.T) {
	provider, cleanup := setupTestLocalProvider(t)
	defer cleanup()

	ctx := context.Background()

	// Set a secret
	err := provider.Set(ctx, "project/test", "API_KEY", "secret-value-123")
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Get the secret
	value, err := provider.Get(ctx, "project/test", "API_KEY")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if value != "secret-value-123" {
		t.Errorf("expected 'secret-value-123', got %q", value)
	}
}

func TestLocalProviderGetNonExistent(t *testing.T) {
	provider, cleanup := setupTestLocalProvider(t)
	defer cleanup()

	ctx := context.Background()

	// Get non-existent secret
	value, err := provider.Get(ctx, "nonexistent", "key")
	if err != nil {
		t.Fatalf("Get should not error for non-existent: %v", err)
	}
	if value != "" {
		t.Errorf("expected empty string for non-existent, got %q", value)
	}
}

func TestLocalProviderDelete(t *testing.T) {
	provider, cleanup := setupTestLocalProvider(t)
	defer cleanup()

	ctx := context.Background()

	// Set a secret
	err := provider.Set(ctx, "project/test", "KEY", "value")
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Delete it
	deleted, err := provider.Delete(ctx, "project/test", "KEY")
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if !deleted {
		t.Error("Delete should return true for existing secret")
	}

	// Verify it's gone
	value, err := provider.Get(ctx, "project/test", "KEY")
	if err != nil {
		t.Fatalf("Get after delete failed: %v", err)
	}
	if value != "" {
		t.Errorf("expected empty string after delete, got %q", value)
	}

	// Delete again should return false
	deleted, err = provider.Delete(ctx, "project/test", "KEY")
	if err != nil {
		t.Fatalf("second Delete failed: %v", err)
	}
	if deleted {
		t.Error("Delete should return false for non-existent secret")
	}
}

func TestLocalProviderListKeys(t *testing.T) {
	provider, cleanup := setupTestLocalProvider(t)
	defer cleanup()

	ctx := context.Background()

	// Set multiple secrets
	err := provider.Set(ctx, "project/test", "KEY1", "value1")
	if err != nil {
		t.Fatalf("Set KEY1 failed: %v", err)
	}
	err = provider.Set(ctx, "project/test", "KEY2", "value2")
	if err != nil {
		t.Fatalf("Set KEY2 failed: %v", err)
	}

	// List keys
	keys, err := provider.ListKeys(ctx, "project/test")
	if err != nil {
		t.Fatalf("ListKeys failed: %v", err)
	}
	if len(keys) != 2 {
		t.Errorf("expected 2 keys, got %d", len(keys))
	}

	// Check both keys are present
	keyMap := make(map[string]bool)
	for _, k := range keys {
		keyMap[k] = true
	}
	if !keyMap["KEY1"] || !keyMap["KEY2"] {
		t.Errorf("expected KEY1 and KEY2, got %v", keys)
	}
}

func TestLocalProviderListPaths(t *testing.T) {
	provider, cleanup := setupTestLocalProvider(t)
	defer cleanup()

	ctx := context.Background()

	// Set secrets in multiple paths
	err := provider.Set(ctx, "path/one", "KEY", "value")
	if err != nil {
		t.Fatalf("Set path/one failed: %v", err)
	}
	err = provider.Set(ctx, "path/two", "KEY", "value")
	if err != nil {
		t.Fatalf("Set path/two failed: %v", err)
	}

	// List paths
	paths, err := provider.ListPaths(ctx)
	if err != nil {
		t.Fatalf("ListPaths failed: %v", err)
	}
	if len(paths) != 2 {
		t.Errorf("expected 2 paths, got %d", len(paths))
	}

	// Check both paths are present
	pathMap := make(map[string]bool)
	for _, p := range paths {
		pathMap[p] = true
	}
	if !pathMap["path/one"] || !pathMap["path/two"] {
		t.Errorf("expected path/one and path/two, got %v", paths)
	}
}

func TestLocalProviderGetMulti(t *testing.T) {
	provider, cleanup := setupTestLocalProvider(t)
	defer cleanup()

	ctx := context.Background()

	// Set multiple secrets
	err := provider.Set(ctx, "project/test", "KEY1", "value1")
	if err != nil {
		t.Fatalf("Set KEY1 failed: %v", err)
	}
	err = provider.Set(ctx, "project/test", "KEY2", "value2")
	if err != nil {
		t.Fatalf("Set KEY2 failed: %v", err)
	}
	err = provider.Set(ctx, "other/path", "KEY3", "value3")
	if err != nil {
		t.Fatalf("Set KEY3 failed: %v", err)
	}

	// Get multiple secrets
	refs := []SecretRef{
		{Path: "project/test", Key: "KEY1"},
		{Path: "project/test", Key: "KEY2"},
		{Path: "other/path", Key: "KEY3"},
	}
	results, err := provider.GetMulti(ctx, refs)
	if err != nil {
		t.Fatalf("GetMulti failed: %v", err)
	}

	// Check results are in "path:key" format
	if results["project/test:KEY1"] != "value1" {
		t.Errorf("expected project/test:KEY1=value1, got %q", results["project/test:KEY1"])
	}
	if results["project/test:KEY2"] != "value2" {
		t.Errorf("expected project/test:KEY2=value2, got %q", results["project/test:KEY2"])
	}
	if results["other/path:KEY3"] != "value3" {
		t.Errorf("expected other/path:KEY3=value3, got %q", results["other/path:KEY3"])
	}
}

func TestLocalProviderGetMultiNotFound(t *testing.T) {
	provider, cleanup := setupTestLocalProvider(t)
	defer cleanup()

	ctx := context.Background()

	// Set one secret
	err := provider.Set(ctx, "project/test", "KEY1", "value1")
	if err != nil {
		t.Fatalf("Set KEY1 failed: %v", err)
	}

	// Request existing and non-existing
	refs := []SecretRef{
		{Path: "project/test", Key: "KEY1"},
		{Path: "project/test", Key: "MISSING"},
	}
	results, err := provider.GetMulti(ctx, refs)
	if err != nil {
		t.Fatalf("GetMulti failed: %v", err)
	}

	if results["project/test:KEY1"] != "value1" {
		t.Errorf("expected project/test:KEY1=value1, got %q", results["project/test:KEY1"])
	}
	if results["project/test:MISSING"] != "" {
		t.Errorf("expected empty string for MISSING, got %q", results["project/test:MISSING"])
	}
}

func TestLocalProviderImplementsInterface(t *testing.T) {
	// Compile-time check that LocalProvider implements Provider
	var _ Provider = (*LocalProvider)(nil)
}

func TestNewProviderLocal(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "provider-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	password := "testpassword"

	// Initialize storage first
	storage := NewStorageWithPath(tempDir)
	if err := storage.Init(password, false); err != nil {
		t.Fatalf("failed to init storage: %v", err)
	}

	// Create provider using NewProvider with empty type (defaults to local)
	provider, err := NewProvider(ProviderConfig{
		Type:      "",
		LocalPath: tempDir,
		Password:  password,
	})
	if err != nil {
		t.Fatalf("NewProvider failed: %v", err)
	}

	// Test it works
	ctx := context.Background()
	err = provider.Set(ctx, "test/path", "key", "value")
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	value, err := provider.Get(ctx, "test/path", "key")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if value != "value" {
		t.Errorf("expected 'value', got %q", value)
	}
}

func TestNewProviderExplicitLocal(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "provider-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	password := "testpassword"

	// Initialize storage first
	storage := NewStorageWithPath(tempDir)
	if err := storage.Init(password, false); err != nil {
		t.Fatalf("failed to init storage: %v", err)
	}

	// Create provider using NewProvider with explicit "local" type
	provider, err := NewProvider(ProviderConfig{
		Type:      "local",
		LocalPath: tempDir,
		Password:  password,
	})
	if err != nil {
		t.Fatalf("NewProvider failed: %v", err)
	}

	// Test it works
	ctx := context.Background()
	err = provider.Set(ctx, "test/path", "key", "value")
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}
}

func TestNewProviderUnknownType(t *testing.T) {
	_, err := NewProvider(ProviderConfig{
		Type: "unknown",
	})
	if err == nil {
		t.Error("expected error for unknown provider type")
	}
}

func TestNewProviderDatabase(t *testing.T) {
	// Database provider requires gorm.DB, so NewProvider should return helpful error
	_, err := NewProvider(ProviderConfig{
		Type: "database",
	})
	if err == nil {
		t.Error("expected error for database provider without gorm.DB")
	}
}

func TestLocalProviderInvalidPath(t *testing.T) {
	provider, cleanup := setupTestLocalProvider(t)
	defer cleanup()

	ctx := context.Background()

	// Try with invalid path
	err := provider.Set(ctx, "../escape", "key", "value")
	if err != ErrInvalidPath {
		t.Errorf("expected ErrInvalidPath, got %v", err)
	}

	_, err = provider.Get(ctx, "has spaces", "key")
	if err != ErrInvalidPath {
		t.Errorf("expected ErrInvalidPath, got %v", err)
	}
}

func TestLocalProviderInvalidKey(t *testing.T) {
	provider, cleanup := setupTestLocalProvider(t)
	defer cleanup()

	ctx := context.Background()

	// Try with invalid key
	err := provider.Set(ctx, "valid/path", "with/slash", "value")
	if err != ErrInvalidKey {
		t.Errorf("expected ErrInvalidKey, got %v", err)
	}

	_, err = provider.Get(ctx, "valid/path", "")
	if err != ErrInvalidKey {
		t.Errorf("expected ErrInvalidKey, got %v", err)
	}
}
