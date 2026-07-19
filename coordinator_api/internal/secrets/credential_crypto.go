package secrets

import "encoding/base64"

// EncryptWithPrimary Fernet-encrypts plaintext under the MasterKeyManager's
// primary master key, exactly like org-key encryption elsewhere in this
// file (see GetOrgEncryptionKey / InitializeOrgSecrets). It's the small
// building block internal/auth's credential storage (auth_credentials rows:
// the local-RP identity bundle, the RP API key) uses to encrypt at rest
// without duplicating the Fernet-key-encoding dance. Returns the primary
// key's name (informational — callers that need to satisfy a
// master_keys.key_id foreign key, such as auth_credentials.master_key_id,
// resolve name -> key_id themselves against their store) and the
// ciphertext.
func (m *MasterKeyManager) EncryptWithPrimary(plaintext []byte) (keyName string, ciphertext []byte, err error) {
	name, key := m.GetPrimaryKey()
	if key == nil {
		return "", nil, ErrNoMasterKeys
	}
	ciphertext, err = fernetEncrypt(encodeFernetKey(key), plaintext)
	if err != nil {
		return "", nil, err
	}
	return name, ciphertext, nil
}

// DecryptWithKey Fernet-decrypts ciphertext previously produced by
// EncryptWithPrimary (or by encrypting under any master key this manager
// holds), using the named key. Returns ErrMasterKeyNotFound if the manager
// doesn't hold a key by that name (e.g. it was decommissioned, or this
// process wasn't configured with it).
func (m *MasterKeyManager) DecryptWithKey(keyName string, ciphertext []byte) ([]byte, error) {
	key := m.GetKey(keyName)
	if key == nil {
		return nil, ErrMasterKeyNotFound
	}
	return fernetDecrypt(encodeFernetKey(key), ciphertext)
}

// encodeFernetKey base64url-encodes a raw 32-byte master key the way
// fernetEncrypt/fernetDecrypt expect (they both immediately base64-decode
// their key argument) — factored out since every call site in this file
// otherwise repeats the same three lines.
func encodeFernetKey(key []byte) []byte {
	encoded := make([]byte, base64.URLEncoding.EncodedLen(len(key)))
	base64.URLEncoding.Encode(encoded, key)
	return encoded
}
