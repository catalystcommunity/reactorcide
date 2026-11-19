//go:build !windows

package coordinatorworker

import (
	"os"
	"syscall"
)

// openFileNoFollow opens path for reading; it fails on a symbolic link.
func openFileNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
}
