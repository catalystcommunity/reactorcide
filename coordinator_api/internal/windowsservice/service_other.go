//go:build !windows

package windowsservice

import (
	"context"
	"errors"
)

var errUnsupported = errors.New("Windows service management is available only on Windows")

func RunIfService(func(context.Context, []string) error) (bool, error) { return false, nil }
func Install(string, string) error                                     { return errUnsupported }
func Start() error                                                     { return errUnsupported }
func Stop() error                                                      { return errUnsupported }
func Uninstall() error                                                 { return errUnsupported }
func Status() (string, error)                                          { return "", errUnsupported }
