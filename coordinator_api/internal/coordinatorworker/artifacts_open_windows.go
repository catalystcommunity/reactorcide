//go:build windows

package coordinatorworker

import (
	"errors"
	"os"
)

// openFileNoFollow opens path for reading; it fails on a symbolic link or a
// reparse point.
func openFileNoFollow(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	return os.Open(path)
}
