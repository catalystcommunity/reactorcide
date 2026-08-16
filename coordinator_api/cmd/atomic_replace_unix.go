//go:build !windows

package cmd

import "os"

func atomicReplace(source, destination string) error {
	return os.Rename(source, destination)
}
