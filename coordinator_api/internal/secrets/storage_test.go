package secrets

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewStorage(t *testing.T) {
	storage := NewStorage()
	if storage == nil {
		t.Fatal("NewStorage returned nil")
	}
}

func TestNewStorageWithPath(t *testing.T) {
	path := "/custom/path"
	storage := NewStorageWithPath(path)
	if storage.basePath != path {
		t.Errorf("expected basePath %q, got %q", path, storage.basePath)
	}
}

func setupTestStorage(t *testing.T) (*Storage, string, func()) {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "secrets-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	storage := NewStorageWithPath(tempDir)
	cleanup := func() {
		os.RemoveAll(tempDir)
	}
	return storage, tempDir, cleanup
}

func TestInit(t *testing.T) {
	storage, _, cleanup := setupTestStorage(t)
	defer cleanup()

	password := "testpassword"

	// Test initial init
	err := storage.Init(password, false)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Verify files exist
	if !storage.IsInitialized() {
		t.Error("storage should be initialized after Init")
	}

	// Verify salt file exists
	if _, err := os.Stat(storage.saltFile()); os.IsNotExist(err) {
		t.Error("salt file should exist after Init")
	}

	// Verify secrets file exists
	if _, err := os.Stat(storage.secretsFile()); os.IsNotExist(err) {
		t.Error("secrets file should exist after Init")
	}
}

func TestInitAlreadyExists(t *testing.T) {
	storage, _, cleanup := setupTestStorage(t)
	defer cleanup()

	password := "testpassword"

	// Init once
	err := storage.Init(password, false)
	if err != nil {
		t.Fatalf("first Init failed: %v", err)
	}

	// Init again without force should fail
	err = storage.Init(password, false)
	if err != ErrAlreadyExists {
		t.Errorf("expected ErrAlreadyExists, got %v", err)
	}

	// Init again with force should succeed
	err = storage.Init(password, true)
	if err != nil {
		t.Errorf("Init with force failed: %v", err)
	}
}

func TestSetAndGet(t *testing.T) {
	storage, _, cleanup := setupTestStorage(t)
	defer cleanup()

	password := "testpassword"
	err := storage.Init(password, false)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Set a secret
	err = storage.Set("project/test", "API_KEY", "secret-value-123", password)
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Get the secret
	value, err := storage.Get("project/test", "API_KEY", password)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if value != "secret-value-123" {
		t.Errorf("expected 'secret-value-123', got %q", value)
	}
}

func TestGetNonExistent(t *testing.T) {
	storage, _, cleanup := setupTestStorage(t)
	defer cleanup()

	password := "testpassword"
	err := storage.Init(password, false)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Get non-existent secret
	value, err := storage.Get("nonexistent", "key", password)
	if err != nil {
		t.Fatalf("Get should not error for non-existent: %v", err)
	}
	if value != "" {
		t.Errorf("expected empty string for non-existent, got %q", value)
	}
}

func TestWrongPassword(t *testing.T) {
	storage, _, cleanup := setupTestStorage(t)
	defer cleanup()

	password := "testpassword"
	err := storage.Init(password, false)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Set a secret
	err = storage.Set("project/test", "KEY", "value", password)
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Try to get with wrong password
	_, err = storage.Get("project/test", "KEY", "wrongpassword")
	if err != ErrInvalidPassword {
		t.Errorf("expected ErrInvalidPassword, got %v", err)
	}
}

func TestDelete(t *testing.T) {
	storage, _, cleanup := setupTestStorage(t)
	defer cleanup()

	password := "testpassword"
	err := storage.Init(password, false)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Set a secret
	err = storage.Set("project/test", "KEY", "value", password)
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Delete it
	deleted, err := storage.Delete("project/test", "KEY", password)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if !deleted {
		t.Error("Delete should return true for existing secret")
	}

	// Verify it's gone
	value, err := storage.Get("project/test", "KEY", password)
	if err != nil {
		t.Fatalf("Get after delete failed: %v", err)
	}
	if value != "" {
		t.Errorf("expected empty string after delete, got %q", value)
	}

	// Delete again should return false
	deleted, err = storage.Delete("project/test", "KEY", password)
	if err != nil {
		t.Fatalf("second Delete failed: %v", err)
	}
	if deleted {
		t.Error("Delete should return false for non-existent secret")
	}
}

func TestListKeys(t *testing.T) {
	storage, _, cleanup := setupTestStorage(t)
	defer cleanup()

	password := "testpassword"
	err := storage.Init(password, false)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Set multiple secrets
	err = storage.Set("project/test", "KEY1", "value1", password)
	if err != nil {
		t.Fatalf("Set KEY1 failed: %v", err)
	}
	err = storage.Set("project/test", "KEY2", "value2", password)
	if err != nil {
		t.Fatalf("Set KEY2 failed: %v", err)
	}

	// List keys
	keys, err := storage.ListKeys("project/test", password)
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

func TestListPaths(t *testing.T) {
	storage, _, cleanup := setupTestStorage(t)
	defer cleanup()

	password := "testpassword"
	err := storage.Init(password, false)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Set secrets in multiple paths
	err = storage.Set("path/one", "KEY", "value", password)
	if err != nil {
		t.Fatalf("Set path/one failed: %v", err)
	}
	err = storage.Set("path/two", "KEY", "value", password)
	if err != nil {
		t.Fatalf("Set path/two failed: %v", err)
	}

	// List paths
	paths, err := storage.ListPaths(password)
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

func TestGetMulti(t *testing.T) {
	storage, _, cleanup := setupTestStorage(t)
	defer cleanup()

	password := "testpassword"
	err := storage.Init(password, false)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Set multiple secrets
	err = storage.Set("project/test", "KEY1", "value1", password)
	if err != nil {
		t.Fatalf("Set KEY1 failed: %v", err)
	}
	err = storage.Set("project/test", "KEY2", "value2", password)
	if err != nil {
		t.Fatalf("Set KEY2 failed: %v", err)
	}
	err = storage.Set("other/path", "KEY3", "value3", password)
	if err != nil {
		t.Fatalf("Set KEY3 failed: %v", err)
	}

	// Get multiple secrets
	refs := []SecretRef{
		{Path: "project/test", Key: "KEY1"},
		{Path: "project/test", Key: "KEY2"},
		{Path: "other/path", Key: "KEY3"},
	}
	results, err := storage.GetMulti(refs, password)
	if err != nil {
		t.Fatalf("GetMulti failed: %v", err)
	}

	if results["KEY1"] != "value1" {
		t.Errorf("expected KEY1=value1, got %q", results["KEY1"])
	}
	if results["KEY2"] != "value2" {
		t.Errorf("expected KEY2=value2, got %q", results["KEY2"])
	}
	if results["KEY3"] != "value3" {
		t.Errorf("expected KEY3=value3, got %q", results["KEY3"])
	}
}

func TestGetMultiNotFound(t *testing.T) {
	storage, _, cleanup := setupTestStorage(t)
	defer cleanup()

	password := "testpassword"
	err := storage.Init(password, false)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Set one secret
	err = storage.Set("project/test", "KEY1", "value1", password)
	if err != nil {
		t.Fatalf("Set KEY1 failed: %v", err)
	}

	// Request existing and non-existing
	refs := []SecretRef{
		{Path: "project/test", Key: "KEY1"},
		{Path: "project/test", Key: "MISSING"},
	}
	results, err := storage.GetMulti(refs, password)
	if err != nil {
		t.Fatalf("GetMulti failed: %v", err)
	}

	if results["KEY1"] != "value1" {
		t.Errorf("expected KEY1=value1, got %q", results["KEY1"])
	}
	if results["MISSING"] != "" {
		t.Errorf("expected empty string for MISSING, got %q", results["MISSING"])
	}
}

func TestValidatePath(t *testing.T) {
	tests := []struct {
		path    string
		wantErr bool
	}{
		{"project/test", false},
		{"my-project/prod", false},
		{"my_project/test", false},
		{"simple", false},
		{"a/b/c/d", false},
		{"", true},
		{"../escape", true},
		{"project/../escape", true},
		{"has spaces", true},
		{"special!chars", true},
		{"project:test", true},
	}

	for _, tt := range tests {
		err := validatePath(tt.path)
		if (err != nil) != tt.wantErr {
			t.Errorf("validatePath(%q) error = %v, wantErr %v", tt.path, err, tt.wantErr)
		}
	}
}

func TestValidateKey(t *testing.T) {
	tests := []struct {
		key     string
		wantErr bool
	}{
		{"API_KEY", false},
		{"my-secret", false},
		{"mySecret123", false},
		{"", true},
		{"with/slash", true},
		{"has space", true},
		{"special!char", true},
	}

	for _, tt := range tests {
		err := validateKey(tt.key)
		if (err != nil) != tt.wantErr {
			t.Errorf("validateKey(%q) error = %v, wantErr %v", tt.key, err, tt.wantErr)
		}
	}
}

func TestNotInitialized(t *testing.T) {
	storage, _, cleanup := setupTestStorage(t)
	defer cleanup()

	// Try to get before init
	_, err := storage.Get("path", "key", "password")
	if err != ErrNotInitialized {
		t.Errorf("expected ErrNotInitialized, got %v", err)
	}
}

func TestFernetEncryptDecrypt(t *testing.T) {
	// Generate a valid 32-byte key
	key := make([]byte, 44) // base64-encoded 32 bytes
	copy(key, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")

	plaintext := []byte("hello, world!")

	encrypted, err := fernetEncrypt(key, plaintext)
	if err != nil {
		t.Fatalf("fernetEncrypt failed: %v", err)
	}

	decrypted, err := fernetDecrypt(key, encrypted)
	if err != nil {
		t.Fatalf("fernetDecrypt failed: %v", err)
	}

	if string(decrypted) != string(plaintext) {
		t.Errorf("expected %q, got %q", plaintext, decrypted)
	}
}

func TestFernetDecryptInvalid(t *testing.T) {
	key := make([]byte, 44)
	copy(key, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")

	// Test with too short token
	_, err := fernetDecrypt(key, []byte("short"))
	if err != ErrInvalidPassword {
		t.Errorf("expected ErrInvalidPassword for short token, got %v", err)
	}

	// Test with wrong version
	token := make([]byte, 100)
	token[0] = 0xFF // wrong version
	_, err = fernetDecrypt(key, token)
	if err != ErrInvalidPassword {
		t.Errorf("expected ErrInvalidPassword for wrong version, got %v", err)
	}
}

func TestPathCleanup(t *testing.T) {
	storage, _, cleanup := setupTestStorage(t)
	defer cleanup()

	password := "testpassword"
	err := storage.Init(password, false)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Set secrets in a path
	err = storage.Set("project/test", "KEY1", "value1", password)
	if err != nil {
		t.Fatalf("Set KEY1 failed: %v", err)
	}
	err = storage.Set("project/test", "KEY2", "value2", password)
	if err != nil {
		t.Fatalf("Set KEY2 failed: %v", err)
	}

	// Delete all keys from path
	storage.Delete("project/test", "KEY1", password)
	storage.Delete("project/test", "KEY2", password)

	// Path should no longer exist in listing
	paths, err := storage.ListPaths(password)
	if err != nil {
		t.Fatalf("ListPaths failed: %v", err)
	}
	for _, p := range paths {
		if p == "project/test" {
			t.Error("path should be removed when all keys deleted")
		}
	}
}

func TestIsInitialized(t *testing.T) {
	storage, tempDir, cleanup := setupTestStorage(t)
	defer cleanup()

	// Not initialized
	if storage.IsInitialized() {
		t.Error("storage should not be initialized before Init")
	}

	// Create directory but not secrets file
	os.MkdirAll(tempDir, 0700)
	if storage.IsInitialized() {
		t.Error("storage should not be initialized with just directory")
	}

	// Initialize
	err := storage.Init("password", false)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Now should be initialized
	if !storage.IsInitialized() {
		t.Error("storage should be initialized after Init")
	}
}

func TestDefaultBasePath(t *testing.T) {
	// Save and restore XDG_CONFIG_HOME
	origXDG := os.Getenv("XDG_CONFIG_HOME")
	defer os.Setenv("XDG_CONFIG_HOME", origXDG)

	// Test with XDG_CONFIG_HOME set
	os.Setenv("XDG_CONFIG_HOME", "/custom/config")
	path := getDefaultBasePath()
	expected := "/custom/config/reactorcide/secrets"
	if path != expected {
		t.Errorf("expected %q, got %q", expected, path)
	}

	// Test without XDG_CONFIG_HOME
	os.Unsetenv("XDG_CONFIG_HOME")
	path = getDefaultBasePath()
	home, _ := os.UserHomeDir()
	expected = filepath.Join(home, ".config", "reactorcide", "secrets")
	if path != expected {
		t.Errorf("expected %q, got %q", expected, path)
	}
}
