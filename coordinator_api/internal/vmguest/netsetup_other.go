//go:build !darwin && !windows

package vmguest

import (
	"errors"
	"log/slog"
)

func startTransparent(DaemonConfig, Tunnel, *slog.Logger) error {
	return errors.New("vmguest: the transparent route needs a macOS guest")
}

func applySystemProxy(string) error {
	return errors.New("vmguest: system proxy settings need a Windows guest")
}
