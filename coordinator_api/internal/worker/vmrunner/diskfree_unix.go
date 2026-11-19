//go:build !windows

package vmrunner

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// diskFreeBytes returns the bytes that an unprivileged process can use on
// the file system of path.
func diskFreeBytes(path string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("vmrunner: read free space of %s: %w", path, err)
	}
	return int64(uint64(st.Bavail) * uint64(st.Bsize)), nil
}
