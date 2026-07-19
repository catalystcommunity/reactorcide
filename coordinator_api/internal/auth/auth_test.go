package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
)

func TestCurrentMode(t *testing.T) {
	orig := config.UIAuthMode
	defer func() { config.UIAuthMode = orig }()

	config.UIAuthMode = config.UIAuthModeLocalRP
	if got := CurrentMode(); got != ModeLocalRP {
		t.Fatalf("CurrentMode() = %v, want %v", got, ModeLocalRP)
	}
}

func TestNoneBackendAlwaysFails(t *testing.T) {
	backend := NewNoneBackend()
	if backend.Mode() != ModeNone {
		t.Fatalf("Mode() = %v, want ModeNone", backend.Mode())
	}

	ctx := context.Background()
	if _, _, err := backend.BeginLogin(ctx, "alice@example.com", "https://cb"); !errors.Is(err, ErrLoginDisabled) {
		t.Fatalf("BeginLogin() error = %v, want ErrLoginDisabled", err)
	}
	if _, err := backend.CompleteLogin(ctx, nil, "https://cb"); !errors.Is(err, ErrLoginDisabled) {
		t.Fatalf("CompleteLogin() error = %v, want ErrLoginDisabled", err)
	}
}
