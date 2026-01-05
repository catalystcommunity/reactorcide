package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"golang.org/x/term"
)

// resolveJobSecrets resolves ${secret:path:key} references in environment variables.
// Returns the resolved environment map and a list of secret values for masking.
func resolveJobSecrets(env map[string]string) (map[string]string, []string, error) {
	// Check if any env vars contain secret references
	hasSecrets := false
	for _, v := range env {
		if worker.HasSecretRefs(v) {
			hasSecrets = true
			break
		}
	}

	if !hasSecrets {
		return env, nil, nil
	}

	// Check if secrets storage is initialized
	storage := secrets.NewStorage()
	if !storage.IsInitialized() {
		return nil, nil, fmt.Errorf("secrets storage not initialized, run 'reactorcide secrets init' first")
	}

	password, err := getPassword("Secrets password: ")
	if err != nil {
		return nil, nil, err
	}

	// Create local provider using the Provider interface
	provider, err := secrets.NewLocalProvider("", password)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initialize secrets provider: %w", err)
	}

	// Create getter function for secrets using Provider interface
	ctx := context.Background()
	getSecret := func(path, key string) (string, error) {
		return provider.Get(ctx, path, key)
	}

	return worker.ResolveSecretsInEnv(env, getSecret)
}

// isSensitiveKey checks if an environment variable key suggests sensitive data.
// Used to automatically mask values that might be secrets based on naming conventions.
func isSensitiveKey(key string) bool {
	keyUpper := strings.ToUpper(key)
	sensitivePatterns := []string{"TOKEN", "SECRET", "PASSWORD", "KEY", "AUTH", "CREDENTIAL", "API_KEY"}
	for _, pattern := range sensitivePatterns {
		if strings.Contains(keyUpper, pattern) {
			return true
		}
	}
	return false
}

// checkSecretOverrides validates secret override warnings and returns an error if
// overrides are present but not allowed.
func checkSecretOverrides(overrides []worker.SecretOverride, allowOverrides bool) error {
	if len(overrides) == 0 {
		return nil
	}

	for _, override := range overrides {
		fmt.Fprintf(os.Stderr, "WARNING: %s overrides secret reference in %s with plaintext value\n",
			override.OverlayFile, override.Key)
	}

	if !allowOverrides {
		return fmt.Errorf("secret references were overridden with plaintext values; use --allow-secret-overrides to proceed")
	}

	return nil
}

// promptForSecret prompts for a secret value with hidden input.
// First checks the specified environment variable, then prompts interactively.
// The prompt is written to stderr so it doesn't interfere with piped output.
func promptForSecret(envVar, prompt string) (string, error) {
	// Check environment variable first
	if envVar != "" {
		if value := os.Getenv(envVar); value != "" {
			return value, nil
		}
	}

	// Interactive prompt
	fmt.Fprint(os.Stderr, prompt)

	// Check if stdin is a terminal
	if term.IsTerminal(int(os.Stdin.Fd())) {
		valueBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr) // newline after input
		if err != nil {
			return "", fmt.Errorf("failed to read input: %w", err)
		}
		return string(valueBytes), nil
	}

	// Non-terminal: read from stdin
	reader := bufio.NewReader(os.Stdin)
	value, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("failed to read input: %w", err)
	}
	return strings.TrimSpace(value), nil
}
