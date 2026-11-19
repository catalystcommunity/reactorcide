package vmrunner

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSweepScratchKeepsOwnedDirs(t *testing.T) {
	root := t.TempDir()
	owned, err := createScratchDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer owned.remove()

	removed, errs := sweepScratch(root, func(string) error {
		t.Fatal("release was called for an owned dir")
		return nil
	})
	if len(removed) != 0 || len(errs) != 0 {
		t.Fatalf("sweep removed %v, errors %v; want nothing", removed, errs)
	}
	if _, err := os.Stat(owned.path); err != nil {
		t.Fatalf("owned dir is gone: %v", err)
	}
}

func TestSweepScratchRemovesOrphans(t *testing.T) {
	root := t.TempDir()
	// An orphan: the owner closed its lock (as when the worker process stops)
	// and the dir is still there.
	orphan, err := createScratchDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan.path, scratchVMNameFile), []byte("reactorcide-vm-x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = orphan.lock.Close()

	// An old dir without a lock file, and a new one (still being created).
	old := filepath.Join(root, scratchDirPrefix+"old")
	fresh := filepath.Join(root, scratchDirPrefix+"fresh")
	other := filepath.Join(root, "not-a-job-dir")
	for _, d := range []string{old, fresh, other} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-2 * scratchLockGrace)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(other, past, past); err != nil {
		t.Fatal(err)
	}

	var released []string
	removed, errs := sweepScratch(root, func(dir string) error {
		released = append(released, readScratchVMName(dir))
		return nil
	})
	if len(errs) != 0 {
		t.Fatalf("sweep errors: %v", errs)
	}
	if len(removed) != 2 {
		t.Fatalf("sweep removed %v; want the orphan and the old dir", removed)
	}
	for _, d := range []string{orphan.path, old} {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("%s is still there", d)
		}
	}
	for _, d := range []string{fresh, other} {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("%s was removed: %v", d, err)
		}
	}
	if len(released) != 2 || (released[0] != "reactorcide-vm-x" && released[1] != "reactorcide-vm-x") {
		t.Errorf("release saw VM names %q; want reactorcide-vm-x for the orphan", released)
	}
}

func TestSweepScratchKeepsDirWhenReleaseFails(t *testing.T) {
	root := t.TempDir()
	orphan, err := createScratchDir(root)
	if err != nil {
		t.Fatal(err)
	}
	_ = orphan.lock.Close()

	removed, errs := sweepScratch(root, func(string) error { return os.ErrPermission })
	if len(removed) != 0 || len(errs) != 1 {
		t.Fatalf("sweep removed %v, errors %v; want one error and nothing removed", removed, errs)
	}
	if _, err := os.Stat(orphan.path); err != nil {
		t.Fatalf("dir was removed although release failed: %v", err)
	}
}

func TestScratchDirRemove(t *testing.T) {
	s, err := createScratchDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.path); !os.IsNotExist(err) {
		t.Fatalf("scratch dir is still there: %v", err)
	}
	if err := s.remove(); err != nil {
		t.Fatalf("second remove: %v", err)
	}
}
