package secrets

import (
	"bytes"
	"testing"
)

func testManagerWithKeys(t *testing.T, names ...string) *MasterKeyManager {
	t.Helper()
	mgr := &MasterKeyManager{keys: make(map[string][]byte)}
	for i, name := range names {
		key := make([]byte, 32)
		for j := range key {
			key[j] = byte(i*32 + j)
		}
		mgr.keys[name] = key
		if i == 0 {
			mgr.primaryKey = name
		}
	}
	return mgr
}

func TestEncryptDecryptWithPrimaryRoundTrip(t *testing.T) {
	mgr := testManagerWithKeys(t, "mk-primary", "mk-secondary")

	plaintext := []byte("super secret local-rp identity bundle bytes")
	keyName, ciphertext, err := mgr.EncryptWithPrimary(plaintext)
	if err != nil {
		t.Fatalf("EncryptWithPrimary() error = %v", err)
	}
	if keyName != "mk-primary" {
		t.Fatalf("EncryptWithPrimary() keyName = %q, want %q", keyName, "mk-primary")
	}
	if bytes.Equal(ciphertext, plaintext) {
		t.Fatal("ciphertext must not equal plaintext")
	}

	decrypted, err := mgr.DecryptWithKey(keyName, ciphertext)
	if err != nil {
		t.Fatalf("DecryptWithKey() error = %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("DecryptWithKey() = %q, want %q", decrypted, plaintext)
	}
}

func TestEncryptWithPrimaryNoKeys(t *testing.T) {
	mgr := &MasterKeyManager{keys: make(map[string][]byte)}
	if _, _, err := mgr.EncryptWithPrimary([]byte("x")); err != ErrNoMasterKeys {
		t.Fatalf("EncryptWithPrimary() error = %v, want ErrNoMasterKeys", err)
	}
}

func TestDecryptWithKeyUnknownKey(t *testing.T) {
	mgr := testManagerWithKeys(t, "mk-primary")
	if _, err := mgr.DecryptWithKey("mk-does-not-exist", []byte("whatever")); err != ErrMasterKeyNotFound {
		t.Fatalf("DecryptWithKey() error = %v, want ErrMasterKeyNotFound", err)
	}
}

func TestDecryptWithKeyWrongKeyFails(t *testing.T) {
	mgr := testManagerWithKeys(t, "mk-primary", "mk-secondary")

	_, ciphertext, err := mgr.EncryptWithPrimary([]byte("secret"))
	if err != nil {
		t.Fatalf("EncryptWithPrimary() error = %v", err)
	}

	if _, err := mgr.DecryptWithKey("mk-secondary", ciphertext); err == nil {
		t.Fatal("DecryptWithKey() with the wrong key succeeded, want an error")
	}
}

func TestDecryptWithKeyTamperedCiphertextFails(t *testing.T) {
	mgr := testManagerWithKeys(t, "mk-primary")

	keyName, ciphertext, err := mgr.EncryptWithPrimary([]byte("secret"))
	if err != nil {
		t.Fatalf("EncryptWithPrimary() error = %v", err)
	}
	tampered := append([]byte{}, ciphertext...)
	tampered[len(tampered)-1] ^= 0xFF

	if _, err := mgr.DecryptWithKey(keyName, tampered); err == nil {
		t.Fatal("DecryptWithKey() on tampered ciphertext succeeded, want an error")
	}
}
