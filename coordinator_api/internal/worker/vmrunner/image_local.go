package vmrunner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LocalImageSource resolves an imageRef to a pre-placed base image file on
// local disk. OCIImageSource implements the same interface. A caller can
// select either source without a VMRunner change.
type LocalImageSource struct {
	// BaseDir is where a relative imageRef is resolved from, e.g.
	// imageRef "macos-14-base.img" -> filepath.Join(BaseDir,
	// "macos-14-base.img"). An absolute imageRef is used as-is and BaseDir
	// is ignored for that call.
	BaseDir string
}

// NewLocalImageSource builds a LocalImageSource rooted at baseDir.
func NewLocalImageSource(baseDir string) *LocalImageSource {
	return &LocalImageSource{BaseDir: baseDir}
}

// Resolve validates that the resolved path exists, then returns it unchanged
// -- there is no pulling or caching to do for a pre-placed local image, which
// is exactly the bootstrap simplification this type exists for. The path may
// be either a single image file (the Windows/Hyper-V lifecycle's VHDX shape)
// or a bundle directory (the macOS lifecycle's shape: a directory holding
// disk.img/aux.img/hardwaremodel.bin/machineidentifier.bin -- see
// lifecycle_darwin_vz.go). Which of the two a given VMLifecycle expects is the
// lifecycle's contract, not this source's, so Resolve accepts both.
func (s *LocalImageSource) Resolve(ctx context.Context, imageRef string) (string, error) {
	if imageRef == "" {
		return "", fmt.Errorf("vmrunner: image reference must not be empty")
	}

	path := filepath.Clean(imageRef)
	if s.BaseDir != "" {
		if filepath.IsAbs(path) {
			return "", fmt.Errorf("vmrunner: absolute image reference %q is not allowed with image directory %q", imageRef, s.BaseDir)
		}

		basePath, err := filepath.Abs(s.BaseDir)
		if err != nil {
			return "", fmt.Errorf("vmrunner: resolve image directory %q: %w", s.BaseDir, err)
		}
		path, err = filepath.Abs(filepath.Join(basePath, path))
		if err != nil {
			return "", fmt.Errorf("vmrunner: resolve image reference %q: %w", imageRef, err)
		}
		rel, err := filepath.Rel(basePath, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("vmrunner: image reference %q escapes image directory %q", imageRef, s.BaseDir)
		}
	}

	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("vmrunner: base image %q not found: %w", path, err)
	}

	if s.BaseDir != "" {
		basePath, err := filepath.EvalSymlinks(s.BaseDir)
		if err != nil {
			return "", fmt.Errorf("vmrunner: resolve image directory %q: %w", s.BaseDir, err)
		}
		resolvedPath, err := filepath.EvalSymlinks(path)
		if err != nil {
			return "", fmt.Errorf("vmrunner: resolve base image %q: %w", path, err)
		}
		rel, err := filepath.Rel(basePath, resolvedPath)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("vmrunner: image reference %q resolves outside image directory %q", imageRef, s.BaseDir)
		}
		path = resolvedPath
	}

	return path, nil
}

var _ ImageSource = (*LocalImageSource)(nil)
