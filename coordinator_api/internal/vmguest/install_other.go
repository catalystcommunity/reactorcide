//go:build !darwin && !windows

package vmguest

import "errors"

var errInstall = errors.New("vmguest: install is available in macOS and Windows guests only")

func Install(bool) error { return errInstall }
func Uninstall() error   { return errInstall }

// RunService reports false: only Windows has a service manager wrapper.
func RunService(func(stop <-chan struct{}) error) (bool, error) { return false, nil }
