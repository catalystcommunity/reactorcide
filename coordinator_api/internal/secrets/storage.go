// Package secrets provides local encrypted secrets storage.
//
// This implementation is compatible with the Python runnerlib secrets_local.py format:
// - scrypt key derivation (N=2^18, r=8, p=1)
// - Fernet encryption (AES-128-CBC + HMAC-SHA256)
// - XDG-compliant storage path
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"golang.org/x/crypto/scrypt"
)

const (
	// scrypt parameters - matches Python implementation
	scryptN    = 1 << 18 // 2^18 = 262144, ~256MB memory
	scryptR    = 8
	scryptP    = 1
	scryptKeyLen = 32
	saltSize   = 32

	// Fernet constants
	fernetVersion    = 0x80
	fernetIVSize     = 16
	fernetHMACSize   = 32
	fernetSignKeyLen = 16
	fernetEncKeyLen  = 16
)

var (
	// Path validation: alphanumeric, dash, underscore, forward slash
	pathPattern = regexp.MustCompile(`^[a-zA-Z0-9/_-]+$`)
	// Key validation: alphanumeric, dash, underscore (no slashes)
	keyPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

	ErrInvalidPassword = errors.New("invalid password or corrupted secrets file")
	ErrNotInitialized  = errors.New("secrets storage not initialized, run 'reactorcide secrets init' first")
	ErrAlreadyExists   = errors.New("secrets already initialized, use --force to reinitialize")
	ErrInvalidPath     = errors.New("invalid path: use alphanumeric, dash, underscore, or slash")
	ErrInvalidKey      = errors.New("invalid key: use alphanumeric, dash, or underscore")
)

// Storage provides encrypted secrets storage.
type Storage struct {
	basePath string
}

// NewStorage creates a new secrets storage at the default path.
func NewStorage() *Storage {
	return &Storage{basePath: getDefaultBasePath()}
}

// NewStorageWithPath creates a new secrets storage at a custom path.
func NewStorageWithPath(basePath string) *Storage {
	return &Storage{basePath: basePath}
}

// getDefaultBasePath returns the XDG-compliant secrets storage path.
func getDefaultBasePath() string {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, _ := os.UserHomeDir()
		configHome = filepath.Join(home, ".config")
	}
	return filepath.Join(configHome, "reactorcide", "secrets")
}

// saltFile returns the path to the salt file.
func (s *Storage) saltFile() string {
	return filepath.Join(s.basePath, ".salt")
}

// secretsFile returns the path to the encrypted secrets file.
func (s *Storage) secretsFile() string {
	return filepath.Join(s.basePath, "secrets.enc")
}

// IsInitialized checks if secrets storage is initialized.
func (s *Storage) IsInitialized() bool {
	_, err := os.Stat(s.secretsFile())
	return err == nil
}

// Init initializes secrets storage with the given password.
func (s *Storage) Init(password string, force bool) error {
	if s.IsInitialized() && !force {
		return ErrAlreadyExists
	}

	// Create directory
	if err := os.MkdirAll(s.basePath, 0700); err != nil {
		return fmt.Errorf("failed to create secrets directory: %w", err)
	}

	// Create or recreate salt
	salt := make([]byte, saltSize)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("failed to generate salt: %w", err)
	}
	if err := os.WriteFile(s.saltFile(), salt, 0600); err != nil {
		return fmt.Errorf("failed to write salt file: %w", err)
	}

	// Save empty secrets
	return s.saveAll(make(map[string]map[string]string), password)
}

// getSalt reads the salt from disk.
func (s *Storage) getSalt() ([]byte, error) {
	salt, err := os.ReadFile(s.saltFile())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotInitialized
		}
		return nil, fmt.Errorf("failed to read salt: %w", err)
	}
	return salt, nil
}

// deriveKey derives an encryption key from the password using scrypt.
func (s *Storage) deriveKey(password string) ([]byte, error) {
	salt, err := s.getSalt()
	if err != nil {
		return nil, err
	}

	key, err := scrypt.Key([]byte(password), salt, scryptN, scryptR, scryptP, scryptKeyLen)
	if err != nil {
		return nil, fmt.Errorf("failed to derive key: %w", err)
	}

	// Base64 URL-encode for Fernet compatibility
	encoded := make([]byte, base64.URLEncoding.EncodedLen(len(key)))
	base64.URLEncoding.Encode(encoded, key)
	return encoded, nil
}

// fernetEncrypt encrypts data using Fernet format.
func fernetEncrypt(key, plaintext []byte) ([]byte, error) {
	// Decode base64 key
	decodedKey := make([]byte, base64.URLEncoding.DecodedLen(len(key)))
	n, err := base64.URLEncoding.Decode(decodedKey, key)
	if err != nil {
		return nil, fmt.Errorf("invalid key encoding: %w", err)
	}
	decodedKey = decodedKey[:n]

	if len(decodedKey) != 32 {
		return nil, fmt.Errorf("invalid key length: expected 32, got %d", len(decodedKey))
	}

	signKey := decodedKey[:fernetSignKeyLen]
	encKey := decodedKey[fernetSignKeyLen:]

	// Generate IV
	iv := make([]byte, fernetIVSize)
	if _, err := rand.Read(iv); err != nil {
		return nil, fmt.Errorf("failed to generate IV: %w", err)
	}

	// PKCS7 padding
	block, err := aes.NewCipher(encKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher: %w", err)
	}
	padLen := aes.BlockSize - (len(plaintext) % aes.BlockSize)
	padded := make([]byte, len(plaintext)+padLen)
	copy(padded, plaintext)
	for i := len(plaintext); i < len(padded); i++ {
		padded[i] = byte(padLen)
	}

	// Encrypt
	mode := cipher.NewCBCEncrypter(block, iv)
	ciphertext := make([]byte, len(padded))
	mode.CryptBlocks(ciphertext, padded)

	// Build Fernet token: version(1) + timestamp(8) + iv(16) + ciphertext + hmac(32)
	timestamp := time.Now().Unix()
	tokenLen := 1 + 8 + fernetIVSize + len(ciphertext) + fernetHMACSize
	token := make([]byte, tokenLen)

	token[0] = fernetVersion
	binary.BigEndian.PutUint64(token[1:9], uint64(timestamp))
	copy(token[9:25], iv)
	copy(token[25:25+len(ciphertext)], ciphertext)

	// HMAC over everything except the HMAC itself
	h := hmac.New(sha256.New, signKey)
	h.Write(token[:25+len(ciphertext)])
	copy(token[25+len(ciphertext):], h.Sum(nil))

	return token, nil
}

// fernetDecrypt decrypts Fernet-encrypted data.
func fernetDecrypt(key, token []byte) ([]byte, error) {
	// Decode base64 key
	decodedKey := make([]byte, base64.URLEncoding.DecodedLen(len(key)))
	n, err := base64.URLEncoding.Decode(decodedKey, key)
	if err != nil {
		return nil, ErrInvalidPassword
	}
	decodedKey = decodedKey[:n]

	if len(decodedKey) != 32 {
		return nil, ErrInvalidPassword
	}

	signKey := decodedKey[:fernetSignKeyLen]
	encKey := decodedKey[fernetSignKeyLen:]

	// Minimum token size: version(1) + timestamp(8) + iv(16) + 1 block(16) + hmac(32)
	if len(token) < 73 {
		return nil, ErrInvalidPassword
	}

	// Verify version
	if token[0] != fernetVersion {
		return nil, ErrInvalidPassword
	}

	// Extract components
	iv := token[9:25]
	ciphertextEnd := len(token) - fernetHMACSize
	ciphertext := token[25:ciphertextEnd]
	tokenHMAC := token[ciphertextEnd:]

	// Verify HMAC
	h := hmac.New(sha256.New, signKey)
	h.Write(token[:ciphertextEnd])
	expectedHMAC := h.Sum(nil)
	if !hmac.Equal(tokenHMAC, expectedHMAC) {
		return nil, ErrInvalidPassword
	}

	// Decrypt
	block, err := aes.NewCipher(encKey)
	if err != nil {
		return nil, ErrInvalidPassword
	}
	if len(ciphertext)%aes.BlockSize != 0 {
		return nil, ErrInvalidPassword
	}

	mode := cipher.NewCBCDecrypter(block, iv)
	plaintext := make([]byte, len(ciphertext))
	mode.CryptBlocks(plaintext, ciphertext)

	// Remove PKCS7 padding
	if len(plaintext) == 0 {
		return nil, ErrInvalidPassword
	}
	padLen := int(plaintext[len(plaintext)-1])
	if padLen > aes.BlockSize || padLen > len(plaintext) {
		return nil, ErrInvalidPassword
	}
	for i := len(plaintext) - padLen; i < len(plaintext); i++ {
		if plaintext[i] != byte(padLen) {
			return nil, ErrInvalidPassword
		}
	}

	return plaintext[:len(plaintext)-padLen], nil
}

// loadAll loads and decrypts all secrets.
func (s *Storage) loadAll(password string) (map[string]map[string]string, error) {
	encrypted, err := os.ReadFile(s.secretsFile())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotInitialized
		}
		return nil, fmt.Errorf("failed to read secrets file: %w", err)
	}

	key, err := s.deriveKey(password)
	if err != nil {
		return nil, err
	}

	plaintext, err := fernetDecrypt(key, encrypted)
	if err != nil {
		return nil, err
	}

	var data map[string]map[string]string
	if err := json.Unmarshal(plaintext, &data); err != nil {
		return nil, ErrInvalidPassword
	}

	return data, nil
}

// saveAll encrypts and saves all secrets.
func (s *Storage) saveAll(data map[string]map[string]string, password string) error {
	plaintext, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal secrets: %w", err)
	}

	key, err := s.deriveKey(password)
	if err != nil {
		return err
	}

	encrypted, err := fernetEncrypt(key, plaintext)
	if err != nil {
		return fmt.Errorf("failed to encrypt secrets: %w", err)
	}

	if err := os.MkdirAll(s.basePath, 0700); err != nil {
		return fmt.Errorf("failed to create secrets directory: %w", err)
	}

	if err := os.WriteFile(s.secretsFile(), encrypted, 0600); err != nil {
		return fmt.Errorf("failed to write secrets file: %w", err)
	}

	return nil
}

// validatePath validates a secret path.
func validatePath(path string) error {
	if path == "" || !pathPattern.MatchString(path) {
		return ErrInvalidPath
	}
	return nil
}

// validateKey validates a secret key.
func validateKey(key string) error {
	if key == "" || !keyPattern.MatchString(key) {
		return ErrInvalidKey
	}
	return nil
}

// Get retrieves a secret value.
func (s *Storage) Get(path, key, password string) (string, error) {
	if err := validatePath(path); err != nil {
		return "", err
	}
	if err := validateKey(key); err != nil {
		return "", err
	}

	data, err := s.loadAll(password)
	if err != nil {
		return "", err
	}

	if pathData, ok := data[path]; ok {
		if value, ok := pathData[key]; ok {
			return value, nil
		}
	}
	return "", nil // Not found returns empty string
}

// Set stores a secret value.
func (s *Storage) Set(path, key, value, password string) error {
	if err := validatePath(path); err != nil {
		return err
	}
	if err := validateKey(key); err != nil {
		return err
	}

	data, err := s.loadAll(password)
	if err != nil {
		return err
	}

	if _, ok := data[path]; !ok {
		data[path] = make(map[string]string)
	}
	data[path][key] = value

	return s.saveAll(data, password)
}

// Delete removes a secret.
func (s *Storage) Delete(path, key, password string) (bool, error) {
	if err := validatePath(path); err != nil {
		return false, err
	}
	if err := validateKey(key); err != nil {
		return false, err
	}

	data, err := s.loadAll(password)
	if err != nil {
		return false, err
	}

	if pathData, ok := data[path]; ok {
		if _, exists := pathData[key]; exists {
			delete(pathData, key)
			if len(pathData) == 0 {
				delete(data, path)
			}
			if err := s.saveAll(data, password); err != nil {
				return false, err
			}
			return true, nil
		}
	}
	return false, nil
}

// ListKeys lists all keys in a path.
func (s *Storage) ListKeys(path, password string) ([]string, error) {
	if err := validatePath(path); err != nil {
		return nil, err
	}

	data, err := s.loadAll(password)
	if err != nil {
		return nil, err
	}

	if pathData, ok := data[path]; ok {
		keys := make([]string, 0, len(pathData))
		for k := range pathData {
			keys = append(keys, k)
		}
		return keys, nil
	}
	return []string{}, nil
}

// ListPaths lists all paths that have secrets.
func (s *Storage) ListPaths(password string) ([]string, error) {
	data, err := s.loadAll(password)
	if err != nil {
		return nil, err
	}

	paths := make([]string, 0, len(data))
	for p := range data {
		paths = append(paths, p)
	}
	return paths, nil
}

// SecretRef represents a path:key reference.
type SecretRef struct {
	Path string
	Key  string
}

// GetMulti retrieves multiple secrets with a single key derivation.
func (s *Storage) GetMulti(refs []SecretRef, password string) (map[string]string, error) {
	// Validate all refs first
	for _, ref := range refs {
		if err := validatePath(ref.Path); err != nil {
			return nil, fmt.Errorf("%s: %w", ref.Path, err)
		}
		if err := validateKey(ref.Key); err != nil {
			return nil, fmt.Errorf("%s: %w", ref.Key, err)
		}
	}

	// Single load (single key derivation)
	data, err := s.loadAll(password)
	if err != nil {
		return nil, err
	}

	// Get all requested secrets
	results := make(map[string]string, len(refs))
	for _, ref := range refs {
		if pathData, ok := data[ref.Path]; ok {
			if value, ok := pathData[ref.Key]; ok {
				results[ref.Key] = value
				continue
			}
		}
		// Return empty string for not found (caller can check)
		results[ref.Key] = ""
	}

	return results, nil
}
