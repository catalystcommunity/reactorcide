package secrets

import (
	"encoding/base64"
	"os"
	"testing"
)

func TestLoadMasterKeys(t *testing.T) {
	// Generate a valid 32-byte key
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	encodedKey := base64.StdEncoding.EncodeToString(key)

	tests := []struct {
		name       string
		envValue   string
		wantErr    bool
		wantCount  int
		wantPrim   string
	}{
		{
			name:      "empty env",
			envValue:  "",
			wantErr:   true,
			wantCount: 0,
		},
		{
			name:       "single key",
			envValue:   "mk-2026-01:" + encodedKey,
			wantErr:    false,
			wantCount:  1,
			wantPrim:   "mk-2026-01",
		},
		{
			name:       "multiple keys",
			envValue:   "mk-2026-02:" + encodedKey + ",mk-2026-01:" + encodedKey,
			wantErr:    false,
			wantCount:  2,
			wantPrim:   "mk-2026-02", // First in list is primary
		},
		{
			name:      "invalid format",
			envValue:  "invalid-no-colon",
			wantErr:   true,
			wantCount: 0,
		},
		{
			name:      "invalid base64",
			envValue:  "mk-test:not-valid-base64!!!",
			wantErr:   true,
			wantCount: 0,
		},
		{
			name:      "wrong key length",
			envValue:  "mk-test:" + base64.StdEncoding.EncodeToString([]byte("short")),
			wantErr:   true,
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save and restore env var
			orig := os.Getenv(MasterKeysEnvVar)
			defer os.Setenv(MasterKeysEnvVar, orig)

			os.Setenv(MasterKeysEnvVar, tt.envValue)

			mgr, err := LoadMasterKeys()
			if (err != nil) != tt.wantErr {
				t.Errorf("LoadMasterKeys() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr {
				return
			}

			if len(mgr.keys) != tt.wantCount {
				t.Errorf("got %d keys, want %d", len(mgr.keys), tt.wantCount)
			}

			if mgr.primaryKey != tt.wantPrim {
				t.Errorf("primary key = %s, want %s", mgr.primaryKey, tt.wantPrim)
			}
		})
	}
}

func TestMasterKeyManagerGetKey(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	encodedKey := base64.StdEncoding.EncodeToString(key)

	orig := os.Getenv(MasterKeysEnvVar)
	defer os.Setenv(MasterKeysEnvVar, orig)

	os.Setenv(MasterKeysEnvVar, "test-key:"+encodedKey)

	mgr, err := LoadMasterKeys()
	if err != nil {
		t.Fatalf("LoadMasterKeys failed: %v", err)
	}

	// Get existing key
	gotKey := mgr.GetKey("test-key")
	if gotKey == nil {
		t.Error("GetKey returned nil for existing key")
	}
	if len(gotKey) != 32 {
		t.Errorf("GetKey returned key of length %d, want 32", len(gotKey))
	}

	// Get non-existing key
	gotKey = mgr.GetKey("nonexistent")
	if gotKey != nil {
		t.Error("GetKey returned non-nil for non-existing key")
	}
}

func TestMasterKeyManagerGetPrimaryKey(t *testing.T) {
	key1 := make([]byte, 32)
	key2 := make([]byte, 32)
	for i := range key1 {
		key1[i] = byte(i)
		key2[i] = byte(i + 1)
	}

	orig := os.Getenv(MasterKeysEnvVar)
	defer os.Setenv(MasterKeysEnvVar, orig)

	// First key should be primary
	os.Setenv(MasterKeysEnvVar, "primary:"+base64.StdEncoding.EncodeToString(key1)+",secondary:"+base64.StdEncoding.EncodeToString(key2))

	mgr, err := LoadMasterKeys()
	if err != nil {
		t.Fatalf("LoadMasterKeys failed: %v", err)
	}

	name, keyBytes := mgr.GetPrimaryKey()
	if name != "primary" {
		t.Errorf("GetPrimaryKey name = %s, want 'primary'", name)
	}
	if len(keyBytes) != 32 {
		t.Errorf("GetPrimaryKey key length = %d, want 32", len(keyBytes))
	}
}

func TestMasterKeyManagerHasKey(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	encodedKey := base64.StdEncoding.EncodeToString(key)

	orig := os.Getenv(MasterKeysEnvVar)
	defer os.Setenv(MasterKeysEnvVar, orig)

	os.Setenv(MasterKeysEnvVar, "test-key:"+encodedKey)

	mgr, err := LoadMasterKeys()
	if err != nil {
		t.Fatalf("LoadMasterKeys failed: %v", err)
	}

	if !mgr.HasKey("test-key") {
		t.Error("HasKey returned false for existing key")
	}

	if mgr.HasKey("nonexistent") {
		t.Error("HasKey returned true for non-existing key")
	}
}

func TestMasterKeyManagerKeyNames(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	encodedKey := base64.StdEncoding.EncodeToString(key)

	orig := os.Getenv(MasterKeysEnvVar)
	defer os.Setenv(MasterKeysEnvVar, orig)

	os.Setenv(MasterKeysEnvVar, "key1:"+encodedKey+",key2:"+encodedKey)

	mgr, err := LoadMasterKeys()
	if err != nil {
		t.Fatalf("LoadMasterKeys failed: %v", err)
	}

	names := mgr.KeyNames()
	if len(names) != 2 {
		t.Errorf("KeyNames returned %d names, want 2", len(names))
	}

	// Check both names are present (order may vary)
	nameMap := make(map[string]bool)
	for _, n := range names {
		nameMap[n] = true
	}
	if !nameMap["key1"] || !nameMap["key2"] {
		t.Errorf("KeyNames missing expected keys, got %v", names)
	}
}

func TestLoadMasterKeysWithSpaces(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	encodedKey := base64.StdEncoding.EncodeToString(key)

	orig := os.Getenv(MasterKeysEnvVar)
	defer os.Setenv(MasterKeysEnvVar, orig)

	// Test with spaces around key parts
	os.Setenv(MasterKeysEnvVar, "  key1 : "+encodedKey+" , key2 : "+encodedKey+"  ")

	mgr, err := LoadMasterKeys()
	if err != nil {
		t.Fatalf("LoadMasterKeys failed: %v", err)
	}

	if !mgr.HasKey("key1") {
		t.Error("HasKey returned false for 'key1' (with trimmed spaces)")
	}
	if !mgr.HasKey("key2") {
		t.Error("HasKey returned false for 'key2' (with trimmed spaces)")
	}
}
