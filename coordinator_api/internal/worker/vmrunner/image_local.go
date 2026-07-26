package vmrunner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// LocalImageSource resolves an imageRef to a pre-placed base image file on
// local disk. This bootstraps the "vm" backend (VM-1/VM-3) before the
// OCI-backed ImageSource (VM-2, oras.land/oras-go/v2 pulling from an OCI
// registry) lands with the same interface -- swapping one for the other
// needs no VMRunner change, just a different ImageSource passed to New.
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

	path := imageRef
	if !filepath.IsAbs(path) {
		path = filepath.Join(s.BaseDir, imageRef)
	}

	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("vmrunner: base image %q not found: %w", path, err)
	}

	return path, nil
}

var _ ImageSource = (*LocalImageSource)(nil)
