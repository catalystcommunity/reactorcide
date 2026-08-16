package vmrunner

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/klauspost/compress/zstd"
)

const (
	BundleDiskImage         = "disk.img"
	BundleAuxImage          = "aux.img"
	BundleHardwareModel     = "hardwaremodel.bin"
	BundleMachineIdentifier = "machineidentifier.bin"
	BundleWindowsDisk       = "disk.vhdx"
)

var macBundleFiles = []string{
	BundleDiskImage,
	BundleAuxImage,
	BundleHardwareModel,
	BundleMachineIdentifier,
}

var windowsBundleFiles = []string{
	BundleWindowsDisk,
}

// ValidateWindowsBundle verifies the fixed bundle shape used by Hyper-V and
// Windows OCI image artifacts.
func ValidateWindowsBundle(bundleDir string) error {
	return validateBundle(bundleDir, "Windows", windowsBundleFiles)
}

func validateBundle(bundleDir, platform string, files []string) error {
	info, err := os.Stat(bundleDir)
	if err != nil {
		return fmt.Errorf("vmrunner: %s image bundle %q: %w", platform, bundleDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("vmrunner: %s image %q is not a bundle directory", platform, bundleDir)
	}
	for _, name := range files {
		info, err := os.Stat(filepath.Join(bundleDir, name))
		if err != nil {
			return fmt.Errorf("vmrunner: %s image bundle %q is missing %s: %w", platform, bundleDir, name, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("vmrunner: %s image bundle %q contains non-regular %s", platform, bundleDir, name)
		}
	}
	return nil
}

// ValidateMacBundle verifies the fixed bundle shape used by the macOS
// lifecycle and OCI image artifacts.
func ValidateMacBundle(bundleDir string) error {
	return validateBundle(bundleDir, "macOS", macBundleFiles)
}

// WriteMacBundleArchive writes a deterministic tar+zstd stream. The archive
// contains only the four files required to boot the guest.
func WriteMacBundleArchive(ctx context.Context, bundleDir string, dst io.Writer) error {
	if err := ValidateMacBundle(bundleDir); err != nil {
		return err
	}
	return writeBundleArchive(ctx, bundleDir, dst, macBundleFiles, "macOS")
}

// WriteWindowsBundleArchive writes the VHDX as a deterministic tar+zstd
// stream. SSH client and host keys are created for each writable VM clone.
func WriteWindowsBundleArchive(ctx context.Context, bundleDir string, dst io.Writer) error {
	if err := ValidateWindowsBundle(bundleDir); err != nil {
		return err
	}
	return writeBundleArchive(ctx, bundleDir, dst, windowsBundleFiles, "Windows")
}

func writeBundleArchive(ctx context.Context, bundleDir string, dst io.Writer, files []string, platform string) error {
	zw, err := zstd.NewWriter(dst, zstd.WithEncoderLevel(zstd.SpeedFastest))
	if err != nil {
		return fmt.Errorf("vmrunner: create zstd encoder: %w", err)
	}
	tw := tar.NewWriter(zw)
	closeWriters := func() error {
		return errors.Join(tw.Close(), zw.Close())
	}

	for _, name := range files {
		if err := ctx.Err(); err != nil {
			_ = closeWriters()
			return err
		}
		path := filepath.Join(bundleDir, name)
		info, err := os.Stat(path)
		if err != nil {
			_ = closeWriters()
			return fmt.Errorf("vmrunner: stat bundle file %s: %w", name, err)
		}
		header := &tar.Header{
			Name:       name,
			Mode:       0o444,
			Size:       info.Size(),
			ModTime:    time.Unix(0, 0).UTC(),
			AccessTime: time.Unix(0, 0).UTC(),
			ChangeTime: time.Unix(0, 0).UTC(),
			Format:     tar.FormatPAX,
		}
		if err := tw.WriteHeader(header); err != nil {
			_ = closeWriters()
			return fmt.Errorf("vmrunner: write bundle header for %s: %w", name, err)
		}
		file, err := os.Open(path)
		if err != nil {
			_ = closeWriters()
			return fmt.Errorf("vmrunner: open bundle file %s: %w", name, err)
		}
		_, copyErr := copyWithContext(ctx, tw, file)
		closeErr := file.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			_ = closeWriters()
			return fmt.Errorf("vmrunner: archive bundle file %s: %w", name, err)
		}
	}
	if err := closeWriters(); err != nil {
		return fmt.Errorf("vmrunner: finish %s image archive: %w", platform, err)
	}
	return nil
}

// ExtractMacBundleArchive extracts a trusted-by-digest artifact into an empty
// destination. It still rejects unexpected names and non-regular entries.
func ExtractMacBundleArchive(ctx context.Context, src io.Reader, dstDir string) error {
	return extractBundleArchive(ctx, src, dstDir, macBundleFiles, BundleDiskImage, "macOS", ValidateMacBundle)
}

// ExtractWindowsBundleArchive extracts a trusted-by-digest Windows bundle.
func ExtractWindowsBundleArchive(ctx context.Context, src io.Reader, dstDir string) error {
	return extractBundleArchive(ctx, src, dstDir, windowsBundleFiles, BundleWindowsDisk, "Windows", ValidateWindowsBundle)
}

func extractBundleArchive(ctx context.Context, src io.Reader, dstDir string, files []string, sparseFile, platform string, validate func(string) error) error {
	if err := os.MkdirAll(dstDir, 0o700); err != nil {
		return fmt.Errorf("vmrunner: create bundle directory: %w", err)
	}
	zr, err := zstd.NewReader(src)
	if err != nil {
		return fmt.Errorf("vmrunner: create zstd decoder: %w", err)
	}
	defer zr.Close()

	wanted := make(map[string]bool, len(files))
	for _, name := range files {
		wanted[name] = false
	}
	tr := tar.NewReader(zr)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("vmrunner: read %s image archive: %w", platform, err)
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return fmt.Errorf("vmrunner: %s image archive entry %q is not a regular file", platform, header.Name)
		}
		seen, ok := wanted[header.Name]
		if !ok || seen || filepath.Base(header.Name) != header.Name {
			return fmt.Errorf("vmrunner: unexpected %s image archive entry %q", platform, header.Name)
		}
		wanted[header.Name] = true
		path := filepath.Join(dstDir, header.Name)
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("vmrunner: create extracted bundle file %s: %w", header.Name, err)
		}
		var copyErr error
		if header.Name == sparseFile {
			copyErr = copySparseWithContext(ctx, file, tr, header.Size)
		} else {
			_, copyErr = copyWithContext(ctx, file, tr)
		}
		closeErr := file.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return fmt.Errorf("vmrunner: extract bundle file %s: %w", header.Name, err)
		}
		if err := os.Chmod(path, 0o444); err != nil {
			return fmt.Errorf("vmrunner: protect extracted bundle file %s: %w", header.Name, err)
		}
	}
	for name, seen := range wanted {
		if !seen {
			return fmt.Errorf("vmrunner: %s image archive is missing %s", platform, name)
		}
	}
	return validate(dstDir)
}

func copySparseWithContext(ctx context.Context, dst *os.File, src io.Reader, size int64) error {
	buf := make([]byte, 4*1024*1024)
	var processed int64
	for processed < size {
		if err := ctx.Err(); err != nil {
			return err
		}
		want := int64(len(buf))
		if remaining := size - processed; remaining < want {
			want = remaining
		}
		n, err := io.ReadFull(src, buf[:want])
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			return err
		}
		if n == 0 {
			return io.ErrUnexpectedEOF
		}
		allZero := true
		for _, b := range buf[:n] {
			if b != 0 {
				allZero = false
				break
			}
		}
		if allZero {
			if _, err := dst.Seek(int64(n), io.SeekCurrent); err != nil {
				return err
			}
		} else if _, err := dst.Write(buf[:n]); err != nil {
			return err
		}
		processed += int64(n)
	}
	return dst.Truncate(size)
}

func copyWithContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buf := make([]byte, 4*1024*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			written, writeErr := dst.Write(buf[:n])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != n {
				return total, io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}
