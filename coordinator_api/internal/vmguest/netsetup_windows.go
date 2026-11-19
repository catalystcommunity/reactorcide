//go:build windows

package vmguest

import (
	"errors"
	"fmt"
	"log/slog"
)

// startTransparent is not available on Windows: see windowsProxyScript.
func startTransparent(DaemonConfig, Tunnel, *slog.Logger) error {
	return errors.New("vmguest: the transparent route is not available on Windows (TODO: WFP redirect driver)")
}

// applySystemProxy adds the offline loopback adapter (see
// ensureOfflineLoopback) and sets the system proxy settings to addr.
func applySystemProxy(addr string) error {
	var errs []error
	if err := ensureOfflineLoopback(); err != nil {
		errs = append(errs, fmt.Errorf("vmguest: loopback adapter (certificate revocation checks fail without it): %w", err))
	}
	if err := runPowerShell(windowsProxyScript(addr)); err != nil {
		errs = append(errs, fmt.Errorf("vmguest: set system proxy: %w", err))
	}
	return errors.Join(errs...)
}
