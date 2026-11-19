//go:build windows

package vmrunner

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// diskFreeBytes returns the bytes that the caller can use on the volume of
// path.
func diskFreeBytes(path string) (int64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, fmt.Errorf("vmrunner: read free space of %s: %w", path, err)
	}
	var available, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &available, &total, &free); err != nil {
		return 0, fmt.Errorf("vmrunner: read free space of %s: %w", path, err)
	}
	return int64(available), nil
}
