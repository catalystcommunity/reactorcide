package secrets

import (
	"strings"
	"sync"
)

// Masker handles masking of known secret values in logs and output
// This is a value-based masking system - it tracks actual secret strings
// and replaces them wherever they appear, regardless of the key name
type Masker struct {
	mu      sync.RWMutex
	secrets map[string]bool // Set of secret values to mask
}

// NewMasker creates a new secret masker
func NewMasker() *Masker {
	return &Masker{
		secrets: make(map[string]bool),
	}
}

// RegisterSecret adds a secret value that should be masked
// For example: RegisterSecret("my-actual-api-token-12345")
func (m *Masker) RegisterSecret(value string) {
	if value == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.secrets[value] = true
}

// RegisterSecrets adds multiple secret values at once
func (m *Masker) RegisterSecrets(values []string) {
	for _, v := range values {
		m.RegisterSecret(v)
	}
}

// RegisterEnvVars registers the values of environment variables as secrets
// This allows masking based on actual values, not key names
// For example, if ZEALOUS="my-secret-123", we register "my-secret-123" as a secret
func (m *Masker) RegisterEnvVars(envVars map[string]interface{}) {
	for _, value := range envVars {
		if str, ok := value.(string); ok && str != "" {
			m.RegisterSecret(str)
		}
	}
}

// RegisterStringEnvVars registers string map environment variable values
func (m *Masker) RegisterStringEnvVars(envVars map[string]string) {
	for _, value := range envVars {
		if value != "" {
			m.RegisterSecret(value)
		}
	}
}

// MaskString replaces all known secret values in a string with [REDACTED]
// This is the core masking function - it finds and replaces actual secret values
func (m *Masker) MaskString(text string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	masked := text
	for secret := range m.secrets {
		// Only mask secrets that are reasonably long to avoid false positives
		// (e.g., don't mask single characters or very short strings)
		if len(secret) >= 3 {
			masked = strings.ReplaceAll(masked, secret, "[REDACTED]")
		}
	}
	return masked
}

// MaskCommandArgs masks secret values in command arguments
// Unlike key-based masking, this finds actual secret values in the args
func (m *Masker) MaskCommandArgs(args []string) []string {
	masked := make([]string, len(args))
	for i, arg := range args {
		masked[i] = m.MaskString(arg)
	}
	return masked
}

// Clear removes all registered secrets (useful for testing)
func (m *Masker) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.secrets = make(map[string]bool)
}

// Size returns the number of registered secrets (useful for debugging)
func (m *Masker) Size() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.secrets)
}

// DefaultMasker is a global instance that can be used throughout the application
var DefaultMasker = NewMasker()

// RegisterSecret adds a secret to the default masker
func RegisterSecret(value string) {
	DefaultMasker.RegisterSecret(value)
}

// RegisterSecrets adds multiple secrets to the default masker
func RegisterSecrets(values []string) {
	DefaultMasker.RegisterSecrets(values)
}

// RegisterEnvVars registers environment variable values with the default masker
func RegisterEnvVars(envVars map[string]interface{}) {
	DefaultMasker.RegisterEnvVars(envVars)
}

// MaskString masks secrets in a string using the default masker
func MaskString(text string) string {
	return DefaultMasker.MaskString(text)
}

// MaskCommandArgs masks secrets in command arguments using the default masker
func MaskCommandArgs(args []string) []string {
	return DefaultMasker.MaskCommandArgs(args)
}
