package vmrunner

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Per-job VM storage is ephemeral. Each job gets a scratch dir (the
// copy-on-write disk clone of the base image) that Destroy deletes. Only base
// images stay on the host.
//
// A worker that crashes, or a delete that fails, leaves a scratch dir behind.
// To find those, the worker that owns a scratch dir holds an exclusive lock on
// scratchLockFile in it for the life of the job. The operating system releases
// the lock when the worker process stops. sweepScratch deletes each scratch
// dir whose lock it can get, because no live worker owns that dir.
const (
	scratchDirPrefix = "reactorcide-vm-"
	scratchLockFile  = ".owner.lock"
	// scratchVMNameFile holds the hypervisor VM name of the job (Windows), so
	// that a sweep can remove an orphaned VM together with its disk.
	scratchVMNameFile = "vm-name"
	// scratchLockGrace is how old a scratch dir without a lock file must be
	// before a sweep deletes it. A new dir has no lock file for a short time.
	scratchLockGrace = 10 * time.Minute
)

// scratchDir is a per-job scratch directory and the owner lock on it.
type scratchDir struct {
	path string
	lock *os.File
}

// createScratchDir makes a new per-job scratch dir under root (the system
// temporary directory when root is empty) and locks it for this process.
func createScratchDir(root string) (*scratchDir, error) {
	return createLockedDir(root, scratchDirPrefix)
}

// createLockedDir makes a new dir with prefix under root and locks it for
// this process (see createScratchDir).
func createLockedDir(root, prefix string) (*scratchDir, error) {
	if root != "" {
		if err := os.MkdirAll(root, 0o700); err != nil {
			return nil, fmt.Errorf("create scratch root: %w", err)
		}
	}
	dir, err := os.MkdirTemp(root, prefix)
	if err != nil {
		return nil, fmt.Errorf("create scratch dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, scratchLockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err == nil {
		err = lockFile(f)
		if err != nil {
			_ = f.Close()
		}
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("lock scratch dir: %w", err)
	}
	return &scratchDir{path: dir, lock: f}, nil
}

// remove releases the owner lock and deletes the dir. When the delete fails,
// the dir stays without a lock, and a later sweep deletes it.
func (s *scratchDir) remove() error {
	if s == nil {
		return nil
	}
	if s.lock != nil {
		_ = s.lock.Close()
		s.lock = nil
	}
	return removeAllRetry(s.path)
}

// removeAllRetry deletes path. It tries again for a short time, because on
// Windows a disk file can stay open for a moment after its VM is removed.
func removeAllRetry(path string) error {
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		if err = os.RemoveAll(path); err == nil {
			return nil
		}
		time.Sleep(time.Duration(attempt+1) * 200 * time.Millisecond)
	}
	return err
}

// sweepScratch deletes the scratch dirs under root that no live worker owns.
// For each one it first calls release (when not nil) with the dir path, to
// remove resources that use the dir, such as a Windows VM. When release
// fails, the dir stays for the next sweep. It returns the deleted dirs and
// the errors.
func sweepScratch(root string, release func(dir string) error) ([]string, []error) {
	return sweepLockedDirs(root, scratchDirPrefix, release)
}

// sweepLockedDirs is sweepScratch for the dirs with prefix under root.
func sweepLockedDirs(root, prefix string, release func(dir string) error) ([]string, []error) {
	if root == "" {
		root = os.TempDir()
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, []error{fmt.Errorf("list scratch root: %w", err)}
	}
	var removed []string
	var errs []error
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if !scratchOrphaned(dir, e) {
			continue
		}
		if release != nil {
			if err := release(dir); err != nil {
				errs = append(errs, fmt.Errorf("release %s: %w", dir, err))
				continue
			}
		}
		if err := removeAllRetry(dir); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", dir, err))
			continue
		}
		removed = append(removed, dir)
	}
	return removed, errs
}

// scratchOrphaned reports whether no live process holds the owner lock of
// dir.
func scratchOrphaned(dir string, e fs.DirEntry) bool {
	f, err := os.OpenFile(filepath.Join(dir, scratchLockFile), os.O_RDWR, 0)
	if errors.Is(err, fs.ErrNotExist) {
		info, err := e.Info()
		return err == nil && time.Since(info.ModTime()) > scratchLockGrace
	}
	if err != nil {
		// On Windows a sharing violation also means that the owner is alive.
		return false
	}
	defer f.Close()
	if err := lockFile(f); err != nil {
		return false
	}
	return true
}

// readScratchVMName returns the VM name recorded in dir, or "" when there is
// none.
func readScratchVMName(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, scratchVMNameFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
