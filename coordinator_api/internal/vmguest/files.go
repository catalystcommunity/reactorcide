package vmguest

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// guestPath converts a path from the worker to a clean local path. The path
// must be absolute and must not contain NUL or line breaks.
func guestPath(p string) (string, error) {
	if strings.TrimSpace(p) == "" || strings.ContainsAny(p, "\x00\r\n") {
		return "", errors.New("vmguest: invalid path")
	}
	local := filepath.Clean(filepath.FromSlash(p))
	if !filepath.IsAbs(local) {
		return "", fmt.Errorf("vmguest: path %q is not absolute", p)
	}
	return local, nil
}

// receiveFile writes one uploaded file atomically. File data can contain
// credentials; it never goes to the log.
func receiveFile(h FileHeader, r *DataReader, acct account) error {
	target, err := guestPath(h.Path)
	if err != nil {
		_ = r.Drain()
		return err
	}
	dir := filepath.Dir(target)
	if err := mkdirAllOwned(dir, 0o700, acct); err != nil {
		_ = r.Drain()
		return fmt.Errorf("vmguest: create file directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".vmguest-*")
	if err != nil {
		_ = r.Drain()
		return fmt.Errorf("vmguest: create file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return fmt.Errorf("vmguest: write file: %w", err)
	}
	mode := fs.FileMode(h.Mode).Perm()
	if mode == 0 {
		mode = 0o600
	}
	if runtime.GOOS != "windows" {
		if err := tmp.Chmod(mode); err != nil {
			tmp.Close()
			return fmt.Errorf("vmguest: set file mode: %w", err)
		}
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("vmguest: close file: %w", err)
	}
	if err := acct.chown(tmpName); err != nil {
		return fmt.Errorf("vmguest: set file owner: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		// Windows cannot rename over an open or read-only file.
		_ = os.Remove(target)
		if err := os.Rename(tmpName, target); err != nil {
			return fmt.Errorf("vmguest: install file: %w", err)
		}
	}
	return nil
}

// mkdirAllOwned makes dir and its missing parents, and gives each new
// directory to acct.
func mkdirAllOwned(dir string, perm fs.FileMode, acct account) error {
	var missing []string
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if _, err := os.Lstat(d); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		missing = append(missing, d)
		if parent := filepath.Dir(d); parent == d {
			break
		}
	}
	if err := os.MkdirAll(dir, perm); err != nil {
		return err
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := acct.chown(missing[i]); err != nil {
			return err
		}
	}
	return nil
}

// extractTree extracts a tar stream below dest. Entry names must stay below
// dest, and an entry must not go through a symbolic link that an earlier
// entry made. Hard links and device files are skipped.
func extractTree(dest string, r *DataReader, acct account, log *slog.Logger) error {
	root, err := guestPath(dest)
	if err != nil {
		_ = r.Drain()
		return err
	}
	if err := mkdirAllOwned(root, 0o755, acct); err != nil {
		_ = r.Drain()
		return fmt.Errorf("vmguest: create tree destination: %w", err)
	}
	// The destination can be a link that the image owns: on macOS, /job is a
	// synthetic link from the read-only system volume to the Data volume.
	// Extract into, and give away, the directory that it points to. The
	// entries below it still must not go through a link (noLinkParents).
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	if err := acct.chown(root); err != nil {
		_ = r.Drain()
		return fmt.Errorf("vmguest: set tree owner: %w", err)
	}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			_ = r.Drain()
			return fmt.Errorf("vmguest: read tree: %w", err)
		}
		name := path.Clean(strings.ReplaceAll(h.Name, `\`, "/"))
		if name == "." || name == "/" {
			continue
		}
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\x00:") && runtime.GOOS == "windows" {
			_ = r.Drain()
			return fmt.Errorf("vmguest: tree entry %q leaves the destination", h.Name)
		}
		if err := noLinkParents(root, name); err != nil {
			_ = r.Drain()
			return err
		}
		target := filepath.Join(root, filepath.FromSlash(name))
		mode := h.FileInfo().Mode().Perm()
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				_ = r.Drain()
				return fmt.Errorf("vmguest: tree directory: %w", err)
			}
			if runtime.GOOS != "windows" {
				_ = os.Chmod(target, mode|0o700)
			}
		case tar.TypeReg:
			if err := writeTreeFile(target, tr, mode); err != nil {
				_ = r.Drain()
				return err
			}
			_ = os.Chtimes(target, h.ModTime, h.ModTime)
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				_ = r.Drain()
				return fmt.Errorf("vmguest: tree directory: %w", err)
			}
			_ = removeNonDir(target)
			if err := os.Symlink(h.Linkname, target); err != nil {
				if runtime.GOOS == "windows" {
					log.Warn("tree: symbolic link skipped", "entry", name, "err", err)
					continue
				}
				_ = r.Drain()
				return fmt.Errorf("vmguest: tree link: %w", err)
			}
		default:
			continue
		}
		if err := acct.chown(target); err != nil {
			_ = r.Drain()
			return fmt.Errorf("vmguest: set tree owner: %w", err)
		}
	}
	return r.Drain()
}

func writeTreeFile(target string, r io.Reader, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("vmguest: tree directory: %w", err)
	}
	// Do not write through a link that is already at the target.
	_ = removeNonDir(target)
	if mode == 0 {
		mode = 0o644
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("vmguest: tree file: %w", err)
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return fmt.Errorf("vmguest: tree file: %w", err)
	}
	return f.Close()
}

func removeNonDir(p string) error {
	st, err := os.Lstat(p)
	if err != nil || st.IsDir() {
		return err
	}
	return os.Remove(p)
}

// noLinkParents refuses an entry whose parent directories below root include a
// symbolic link.
func noLinkParents(root, name string) error {
	parts := strings.Split(name, "/")
	cur := root
	for _, part := range parts[:len(parts)-1] {
		cur = filepath.Join(cur, part)
		st, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("vmguest: tree entry %q: %w", name, err)
		}
		if st.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("vmguest: tree entry %q goes through a symbolic link", name)
		}
	}
	return nil
}
