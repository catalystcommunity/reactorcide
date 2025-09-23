package secrets

import (
	"testing"
)

func TestMasker_RegisterAndMask(t *testing.T) {
	m := NewMasker()

	// Register some secrets
	m.RegisterSecret("super-secret-token-123")
	m.RegisterSecret("my-password-456")
	m.RegisterSecret("api-key-789")

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Mask single secret in text",
			input:    "The token is super-secret-token-123 and it's sensitive",
			expected: "The token is [REDACTED] and it's sensitive",
		},
		{
			name:     "Mask multiple secrets in text",
			input:    "Auth with super-secret-token-123 and password my-password-456",
			expected: "Auth with [REDACTED] and password [REDACTED]",
		},
		{
			name:     "No secrets to mask",
			input:    "This text has no secrets at all",
			expected: "This text has no secrets at all",
		},
		{
			name:     "Secret appears multiple times",
			input:    "Token: super-secret-token-123, repeat: super-secret-token-123",
			expected: "Token: [REDACTED], repeat: [REDACTED]",
		},
		{
			name:     "Secret in JSON-like string",
			input:    `{"token": "super-secret-token-123", "key": "api-key-789"}`,
			expected: `{"token": "[REDACTED]", "key": "[REDACTED]"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := m.MaskString(tt.input)
			if result != tt.expected {
				t.Errorf("MaskString() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestMasker_RegisterEnvVars(t *testing.T) {
	m := NewMasker()

	// Register environment variables - the VALUES become secrets
	envVars := map[string]interface{}{
		"ZEALOUS":          "my-zealous-secret-999",
		"CUSTOM_API_TOKEN": "token-xyz-123",
		"DATABASE_URL":     "postgres://user:pass@localhost/db",
		"PORT":             8080, // Non-string, should be ignored
		"EMPTY":            "",   // Empty, should be ignored
	}

	m.RegisterEnvVars(envVars)

	// Test that the VALUES are masked, not the keys
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "ZEALOUS value is masked",
			input:    "Connecting with my-zealous-secret-999",
			expected: "Connecting with [REDACTED]",
		},
		{
			name:     "ZEALOUS key is NOT masked",
			input:    "The env var ZEALOUS is set",
			expected: "The env var ZEALOUS is set",
		},
		{
			name:     "Database URL is masked",
			input:    "Using postgres://user:pass@localhost/db for connection",
			expected: "Using [REDACTED] for connection",
		},
		{
			name:     "Token value is masked",
			input:    "Auth: token-xyz-123",
			expected: "Auth: [REDACTED]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := m.MaskString(tt.input)
			if result != tt.expected {
				t.Errorf("MaskString() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestMasker_ShortStrings(t *testing.T) {
	m := NewMasker()

	// Register very short secrets - these should be ignored to avoid false positives
	m.RegisterSecret("a")
	m.RegisterSecret("ab")
	m.RegisterSecret("abc") // This should be masked (length >= 3)
	m.RegisterSecret("long-secret-value")

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Single char not masked",
			input:    "This is a test",
			expected: "This is a test",
		},
		{
			name:     "Two chars not masked",
			input:    "About this",
			expected: "About this",
		},
		{
			name:     "Three chars ARE masked",
			input:    "The code is abc",
			expected: "The code is [REDACTED]",
		},
		{
			name:     "Long secret is masked",
			input:    "Token: long-secret-value",
			expected: "Token: [REDACTED]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := m.MaskString(tt.input)
			if result != tt.expected {
				t.Errorf("MaskString() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestMasker_MaskCommandArgs(t *testing.T) {
	m := NewMasker()
	m.RegisterSecret("secret-token-123")
	m.RegisterSecret("my-password")

	args := []string{
		"docker",
		"run",
		"-e",
		"TOKEN=secret-token-123",
		"-e",
		"PASSWORD=my-password",
		"myimage",
	}

	expected := []string{
		"docker",
		"run",
		"-e",
		"TOKEN=[REDACTED]",
		"-e",
		"PASSWORD=[REDACTED]",
		"myimage",
	}

	result := m.MaskCommandArgs(args)

	for i, v := range result {
		if v != expected[i] {
			t.Errorf("MaskCommandArgs()[%d] = %v, want %v", i, v, expected[i])
		}
	}
}

func TestMasker_ThreadSafety(t *testing.T) {
	m := NewMasker()

	// Run concurrent operations to test thread safety
	done := make(chan bool)

	// Writer goroutine
	go func() {
		for i := 0; i < 100; i++ {
			m.RegisterSecret("secret-" + string(rune(i)))
		}
		done <- true
	}()

	// Reader goroutine
	go func() {
		for i := 0; i < 100; i++ {
			_ = m.MaskString("Some text with secrets")
		}
		done <- true
	}()

	// Wait for both
	<-done
	<-done

	// Should not panic or deadlock
}

func TestDefaultMasker(t *testing.T) {
	// Clear any existing secrets
	DefaultMasker.Clear()

	// Test the global functions
	RegisterSecret("global-secret-123")
	RegisterSecrets([]string{"another-secret", "third-secret"})

	text := "Secrets: global-secret-123, another-secret, third-secret"
	expected := "Secrets: [REDACTED], [REDACTED], [REDACTED]"

	result := MaskString(text)
	if result != expected {
		t.Errorf("MaskString() = %v, want %v", result, expected)
	}

	// Test command args with global masker
	args := []string{"--token=global-secret-123", "--key=another-secret"}
	maskedArgs := MaskCommandArgs(args)

	if maskedArgs[0] != "--token=[REDACTED]" {
		t.Errorf("MaskCommandArgs()[0] = %v, want --token=[REDACTED]", maskedArgs[0])
	}
	if maskedArgs[1] != "--key=[REDACTED]" {
		t.Errorf("MaskCommandArgs()[1] = %v, want --key=[REDACTED]", maskedArgs[1])
	}
}
