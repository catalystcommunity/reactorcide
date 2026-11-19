//go:build !windows

package vmrunner

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockFile takes an exclusive, non-blocking flock on f. The lock belongs to
// the open file, so a second open of the same file (in this or another
// process) cannot take it while f is open.
func lockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}
