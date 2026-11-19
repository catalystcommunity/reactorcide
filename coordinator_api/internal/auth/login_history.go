package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/config"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/sirupsen/logrus"
)

// LoginHistoryStore is the store surface of the login history guard,
// satisfied by postgres_store.PostgresDbStore (auth_login_history,
// migration 000073).
type LoginHistoryStore interface {
	GetAuthLoginHistory(ctx context.Context) (*models.AuthLoginHistory, error)
	MarkAuthLoginHistory(ctx context.Context, mode string) error
	ClearAuthLoginHistory(ctx context.Context) error
}

// ErrDowngradeToNone is returned by EnforceLoginHistory when an instance
// that ran with logins starts in mode none without the override.
var ErrDowngradeToNone = errors.New("auth: this instance has run with logins")

// EnforceLoginHistory is the startup guard of mode none. Call it once at
// coordinator startup, after migrations and before the API serves requests.
//
//   - Mode local-rp or rp: it records the login history (the instance has run
//     with logins) and leaves the no-login behaviours off.
//   - Mode none and no login history: the no-login behaviours are on
//     (config.NoLoginMode is true).
//   - Mode none and a login history: it returns ErrDowngradeToNone and the
//     coordinator must not start. A lost REACTORCIDE_UI_AUTH_MODE setting
//     (the default is none) thus never makes a production instance
//     permissive. With allowDowngrade (the deliberate override
//     REACTORCIDE_UI_AUTH_ALLOW_DOWNGRADE_TO_NONE=true) it deletes the
//     history, logs a warning, and turns the no-login behaviours on.
//
// Every failure leaves the no-login behaviours off (fail closed).
func EnforceLoginHistory(ctx context.Context, s LoginHistoryStore, allowDowngrade bool) error {
	config.SetNoLoginGuardPassed(false)
	if s == nil {
		return fmt.Errorf("auth: the login history guard has no store")
	}
	mode := CurrentMode()
	switch mode {
	case ModeLocalRP, ModeRP:
		if err := s.MarkAuthLoginHistory(ctx, string(mode)); err != nil {
			return fmt.Errorf("auth: recording the login history: %w", err)
		}
		return nil
	case ModeNone:
	default:
		// An unknown mode gives no login and no no-login behaviour.
		// ValidateUIAuthMode reports it.
		return nil
	}

	history, err := s.GetAuthLoginHistory(ctx)
	if errors.Is(err, store.ErrNotFound) {
		config.SetNoLoginGuardPassed(true)
		return nil
	}
	if err != nil {
		return fmt.Errorf("auth: reading the login history: %w", err)
	}
	if !allowDowngrade {
		return fmt.Errorf("%w (login mode %q, recorded %s from %s): REACTORCIDE_UI_AUTH_MODE is %q. "+
			"Set REACTORCIDE_UI_AUTH_MODE to the login mode of this instance. A missing value means none. "+
			"To make this a no-login instance on purpose, set REACTORCIDE_UI_AUTH_ALLOW_DOWNGRADE_TO_NONE=true for one start",
			ErrDowngradeToNone, history.LoginMode, history.LastMarkedAt.UTC().Format("2006-01-02T15:04:05Z"),
			history.Source, config.UIAuthMode)
	}
	if err := s.ClearAuthLoginHistory(ctx); err != nil {
		return fmt.Errorf("auth: clearing the login history: %w", err)
	}
	logrus.WithFields(logrus.Fields{"previous_login_mode": history.LoginMode, "source": history.Source}).
		Warn("REACTORCIDE_UI_AUTH_ALLOW_DOWNGRADE_TO_NONE=true: this instance ran with logins and now runs in " +
			"mode none. The login history is deleted and the no-login behaviours are on. Remove the override")
	config.SetNoLoginGuardPassed(true)
	return nil
}
