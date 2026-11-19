package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

type fakeLoginHistory struct {
	history *models.AuthLoginHistory
	getErr  error
	marks   []string
	cleared int
}

func (f *fakeLoginHistory) GetAuthLoginHistory(context.Context) (*models.AuthLoginHistory, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.history == nil {
		return nil, store.ErrNotFound
	}
	return f.history, nil
}

func (f *fakeLoginHistory) MarkAuthLoginHistory(_ context.Context, mode string) error {
	f.marks = append(f.marks, mode)
	f.history = &models.AuthLoginHistory{Singleton: true, LoginMode: mode, Source: models.AuthLoginHistorySourceStartup,
		LastMarkedAt: time.Now()}
	return nil
}

func (f *fakeLoginHistory) ClearAuthLoginHistory(context.Context) error {
	f.cleared++
	f.history = nil
	return nil
}

func withMode(t *testing.T, mode string) {
	t.Helper()
	prevMode, prevGuard := config.UIAuthMode, config.NoLoginGuardPassed()
	config.UIAuthMode = mode
	t.Cleanup(func() {
		config.UIAuthMode = prevMode
		config.SetNoLoginGuardPassed(prevGuard)
	})
}

func TestEnforceLoginHistory(t *testing.T) {
	ctx := context.Background()

	t.Run("fresh instance in mode none gets the no-login behaviours", func(t *testing.T) {
		withMode(t, config.UIAuthModeNone)
		s := &fakeLoginHistory{}
		if err := EnforceLoginHistory(ctx, s, false); err != nil {
			t.Fatalf("EnforceLoginHistory: %v", err)
		}
		if !config.NoLoginMode() {
			t.Fatal("NoLoginMode() = false on a fresh no-login instance")
		}
		if len(s.marks) != 0 {
			t.Fatalf("mode none recorded a login history: %v", s.marks)
		}
	})

	for _, mode := range []string{config.UIAuthModeLocalRP, config.UIAuthModeRP} {
		t.Run(mode+" records the history and gives no no-login behaviour", func(t *testing.T) {
			withMode(t, mode)
			config.SetNoLoginGuardPassed(true)
			s := &fakeLoginHistory{}
			if err := EnforceLoginHistory(ctx, s, false); err != nil {
				t.Fatalf("EnforceLoginHistory: %v", err)
			}
			if len(s.marks) != 1 || s.marks[0] != mode {
				t.Fatalf("marks = %v, want [%s]", s.marks, mode)
			}
			if config.NoLoginGuardPassed() || config.NoLoginMode() {
				t.Fatal("a login mode left the no-login guard passed")
			}
		})
	}

	t.Run("mode none after rp refuses to start", func(t *testing.T) {
		withMode(t, config.UIAuthModeRP)
		s := &fakeLoginHistory{}
		if err := EnforceLoginHistory(ctx, s, false); err != nil {
			t.Fatalf("rp start: %v", err)
		}
		// The REACTORCIDE_UI_AUTH_MODE setting is lost: the default is none.
		config.UIAuthMode = config.UIAuthModeNone
		err := EnforceLoginHistory(ctx, s, false)
		if !errors.Is(err, ErrDowngradeToNone) {
			t.Fatalf("err = %v, want ErrDowngradeToNone", err)
		}
		if config.NoLoginMode() {
			t.Fatal("NoLoginMode() = true after a refused downgrade")
		}
		if s.cleared != 0 || s.history == nil {
			t.Fatal("a refused downgrade changed the login history")
		}
	})

	t.Run("the override clears the history and allows mode none", func(t *testing.T) {
		withMode(t, config.UIAuthModeNone)
		s := &fakeLoginHistory{history: &models.AuthLoginHistory{Singleton: true, LoginMode: "unknown",
			Source: models.AuthLoginHistorySourceExistingData}}
		if err := EnforceLoginHistory(ctx, s, true); err != nil {
			t.Fatalf("EnforceLoginHistory with override: %v", err)
		}
		if s.cleared != 1 || s.history != nil {
			t.Fatal("the override did not clear the login history")
		}
		if !config.NoLoginMode() {
			t.Fatal("NoLoginMode() = false after the override")
		}
		// The next start needs no override.
		config.SetNoLoginGuardPassed(false)
		if err := EnforceLoginHistory(ctx, s, false); err != nil || !config.NoLoginMode() {
			t.Fatalf("next start: err = %v, NoLoginMode = %v", err, config.NoLoginMode())
		}
	})

	t.Run("a store error fails closed", func(t *testing.T) {
		withMode(t, config.UIAuthModeNone)
		config.SetNoLoginGuardPassed(true)
		s := &fakeLoginHistory{getErr: errors.New("connection refused")}
		if err := EnforceLoginHistory(ctx, s, true); err == nil {
			t.Fatal("EnforceLoginHistory succeeded with a store error")
		}
		if config.NoLoginMode() {
			t.Fatal("NoLoginMode() = true after a store error")
		}
	})

	t.Run("no store fails closed", func(t *testing.T) {
		withMode(t, config.UIAuthModeNone)
		config.SetNoLoginGuardPassed(true)
		if err := EnforceLoginHistory(ctx, nil, false); err == nil || config.NoLoginMode() {
			t.Fatalf("err = %v, NoLoginMode = %v; want an error and no no-login behaviour", err, config.NoLoginMode())
		}
	})

	t.Run("without the guard mode none gives nothing", func(t *testing.T) {
		withMode(t, config.UIAuthModeNone)
		config.SetNoLoginGuardPassed(false)
		if config.NoLoginMode() {
			t.Fatal("NoLoginMode() = true without the guard")
		}
	})
}
