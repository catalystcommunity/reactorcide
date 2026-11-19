package vmrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry"
)

// This file has the OCIImageSource operations of the worker image set
// (VM_IMAGES_PLAN.md "Worker image sets"): a size estimate before a pull,
// the free space of the cache, a presence check, and the removal of an
// image that left the set.

// HasImage reports whether the cache has the manifest with this digest.
func (s *OCIImageSource) HasImage(ctx context.Context, manifestDigest string) bool {
	dgst := digest.Digest(manifestDigest)
	if dgst.Validate() != nil {
		return false
	}
	_, err := s.cache.Resolve(ctx, dgst.String())
	return err == nil
}

// RemoveImage removes the manifest, the blobs that only it uses, the
// materialized bundle, and the access record of one image. An image that
// is not in the cache is not an error.
func (s *OCIImageSource) RemoveImage(ctx context.Context, manifestDigest string) error {
	dgst := digest.Digest(manifestDigest)
	if err := dgst.Validate(); err != nil {
		return fmt.Errorf("vmrunner: invalid image digest %q: %w", manifestDigest, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.removeLocked(ctx, dgst)
}

func (s *OCIImageSource) removeLocked(ctx context.Context, dgst digest.Digest) error {
	if desc, err := s.cache.Resolve(ctx, dgst.String()); err == nil {
		if err := s.cache.Delete(ctx, desc); err != nil {
			return fmt.Errorf("vmrunner: delete cached image %s: %w", dgst, err)
		}
	}
	bundlePath := filepath.Join(s.cacheDir, "bundles", dgst.Algorithm().String(), dgst.Encoded())
	if err := os.Chmod(bundlePath, 0o700); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("vmrunner: unlock materialized bundle %s: %w", dgst, err)
	}
	if err := os.RemoveAll(bundlePath); err != nil {
		return fmt.Errorf("vmrunner: remove materialized bundle %s: %w", dgst, err)
	}
	accessPath := filepath.Join(s.cacheDir, "access", dgst.Algorithm().String(), dgst.Encoded())
	if err := os.Remove(accessPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("vmrunner: remove image access record %s: %w", dgst, err)
	}
	return nil
}

// FreeBytes returns the free space of the file system of the cache.
func (s *OCIImageSource) FreeBytes() (int64, error) {
	return diskFreeBytes(s.cacheDir)
}

// PullSize estimates the disk space that a pull of imageRef needs. It
// returns 0 when the cache has the image. It reads the manifest from the
// store; it does not pull the layers. The estimate is the size of the
// blobs, plus the layer size again for a bundle layer, because the worker
// extracts a bundle next to its blob. An extracted bundle can be larger
// than its compressed layer, so the value is a lower limit.
func (s *OCIImageSource) PullSize(ctx context.Context, imageRef string) (int64, error) {
	src, srcRef, cacheRef, err := s.pullTarget(ctx, imageRef)
	if err != nil {
		return 0, err
	}
	if _, err := s.cache.Resolve(ctx, cacheRef); err == nil {
		return 0, nil
	}
	desc, err := src.Resolve(ctx, srcRef)
	if err != nil {
		return 0, fmt.Errorf("vmrunner: resolve vm image %q: %w", imageRef, err)
	}
	raw, err := content.FetchAll(ctx, src, desc)
	if err != nil {
		return 0, fmt.Errorf("vmrunner: fetch vm image manifest %q: %w", imageRef, err)
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return 0, fmt.Errorf("vmrunner: decode vm image manifest %q: %w", imageRef, err)
	}
	total := desc.Size + manifest.Config.Size
	for _, layer := range manifest.Layers {
		total += layer.Size
		if layer.MediaType == VMMacBundleLayerMediaType || layer.MediaType == VMWindowsBundleLayerMediaType {
			total += layer.Size
		}
	}
	return total, nil
}

// pullTarget returns the source target of imageRef, the reference to
// resolve in it, and the reference that the cache keeps it under. It
// follows resolveLocked and resolveS3Locked.
func (s *OCIImageSource) pullTarget(ctx context.Context, imageRef string) (oras.ReadOnlyTarget, string, string, error) {
	if imageRef == "" {
		return nil, "", "", fmt.Errorf("vmrunner: image reference must not be empty")
	}
	if isS3ImageRef(imageRef) {
		loc, err := parseS3ImageRef(imageRef)
		if err != nil {
			return nil, "", "", err
		}
		if loc.Digest == "" {
			return nil, "", "", fmt.Errorf("vmrunner: s3 image reference %q has no digest", imageRef)
		}
		src, err := s.s3Factory(ctx, loc)
		if err != nil {
			return nil, "", "", err
		}
		return src, loc.Digest.String(), loc.Digest.String(), nil
	}
	ref, err := registry.ParseReference(strings.TrimPrefix(imageRef, ociImageScheme))
	if err != nil {
		return nil, "", "", fmt.Errorf("vmrunner: invalid OCI image reference %q: %w", imageRef, err)
	}
	if err := ref.ValidateRepository(); err != nil {
		return nil, "", "", fmt.Errorf("vmrunner: invalid OCI image reference %q: %w", imageRef, err)
	}
	src, err := s.sourceFactory(ref)
	if err != nil {
		return nil, "", "", err
	}
	reference := ref.ReferenceOrDefault()
	return src, reference, reference, nil
}

// Pull pulls imageRef into the cache, checks its digests, and makes its
// bundle, like Resolve. It does not hold the cache lock, so that a job can
// resolve a cached image while a long image set pull runs. The cache store
// is safe for concurrent use, and a bundle is extracted to a temporary
// directory and then renamed. The caller must keep the image out of Prune
// (PruneKeeping) while it pulls.
func (s *OCIImageSource) Pull(ctx context.Context, imageRef string) (string, error) {
	return s.resolveLocked(ctx, imageRef)
}
