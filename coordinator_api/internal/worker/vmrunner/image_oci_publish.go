package vmrunner

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
)

// PushMacBundle packages bundleDir and publishes it as a Reactorcide OCI
// artifact. It returns a digest-pinned reference.
func PushMacBundle(ctx context.Context, bundleDir, imageRef string, credentialStore credentials.Store, plainHTTPHosts []string) (string, error) {
	ref, err := parseOCIReference(imageRef)
	if err != nil {
		return "", err
	}
	repo, err := newRemoteRepository(ref, credentialStore, plainHTTPHosts)
	if err != nil {
		return "", err
	}

	tmp, err := os.CreateTemp("", "reactorcide-macos-image-*.tar.zst")
	if err != nil {
		return "", fmt.Errorf("vmrunner: create image archive: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	h := sha256.New()
	archiveErr := WriteMacBundleArchive(ctx, bundleDir, io.MultiWriter(tmp, h))
	closeErr := tmp.Close()
	if err := errors.Join(archiveErr, closeErr); err != nil {
		return "", err
	}
	info, err := os.Stat(tmpPath)
	if err != nil {
		return "", fmt.Errorf("vmrunner: stat image archive: %w", err)
	}
	layer := ocispec.Descriptor{
		MediaType: VMMacBundleLayerMediaType,
		Digest:    digest.NewDigest(digest.SHA256, h),
		Size:      info.Size(),
		Annotations: map[string]string{
			ocispec.AnnotationTitle: "macos-vm-bundle.tar.zst",
		},
	}
	archive, err := os.Open(tmpPath)
	if err != nil {
		return "", fmt.Errorf("vmrunner: reopen image archive: %w", err)
	}
	pushErr := repo.Push(ctx, layer, archive)
	closeErr = archive.Close()
	if pushErr != nil && !errors.Is(pushErr, errdef.ErrAlreadyExists) {
		return "", fmt.Errorf("vmrunner: push macOS image layer: %w", pushErr)
	}
	if closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
		return "", fmt.Errorf("vmrunner: close image archive: %w", closeErr)
	}

	manifest, err := oras.PackManifest(ctx, repo, oras.PackManifestVersion1_1, VMImageArtifactType, oras.PackManifestOptions{
		Layers: []ocispec.Descriptor{layer},
		ManifestAnnotations: map[string]string{
			ocispec.AnnotationCreated:        time.Now().UTC().Format(time.RFC3339),
			"org.opencontainers.image.title": "Reactorcide macOS VM image",
		},
	})
	if err != nil {
		return "", fmt.Errorf("vmrunner: create macOS image manifest: %w", err)
	}
	if err := repo.Tag(ctx, manifest, ref.ReferenceOrDefault()); err != nil {
		return "", fmt.Errorf("vmrunner: tag macOS image manifest: %w", err)
	}
	return ref.Registry + "/" + ref.Repository + "@" + manifest.Digest.String(), nil
}

func parseOCIReference(imageRef string) (registry.Reference, error) {
	ref, err := registry.ParseReference(imageRef)
	if err != nil {
		return registry.Reference{}, fmt.Errorf("vmrunner: invalid OCI image reference %q: %w", imageRef, err)
	}
	if err := ref.ValidateRepository(); err != nil {
		return registry.Reference{}, fmt.Errorf("vmrunner: invalid OCI image reference %q: %w", imageRef, err)
	}
	return ref, nil
}

func newRemoteRepository(ref registry.Reference, credentialStore credentials.Store, plainHTTPHosts []string) (*remote.Repository, error) {
	repo, err := remote.NewRepository(ref.Registry + "/" + ref.Repository)
	if err != nil {
		return nil, fmt.Errorf("vmrunner: open OCI repository: %w", err)
	}
	plain := map[string]struct{}{}
	for _, host := range plainHTTPHosts {
		plain[host] = struct{}{}
	}
	_, explicitPlain := plain[ref.Host()]
	repo.PlainHTTP = explicitPlain || isLoopbackRegistryHost(ref.Host())
	if credentialStore == nil {
		credentialStore = credentials.NewMemoryStore()
	}
	repo.Client = &auth.Client{
		Client:     &http.Client{},
		Cache:      auth.NewCache(),
		Credential: credentials.Credential(credentialStore),
	}
	return repo, nil
}

// CopyMacBundle copies a materialized bundle to dst. It refuses to overwrite
// an existing destination.
func CopyMacBundle(ctx context.Context, src, dst string) error {
	if err := ValidateMacBundle(src); err != nil {
		return err
	}
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("vmrunner: destination %q already exists", dst)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("vmrunner: inspect destination %q: %w", dst, err)
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return fmt.Errorf("vmrunner: create destination bundle: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(dst)
		}
	}()
	for _, name := range macBundleFiles {
		in, err := os.Open(filepath.Join(src, name))
		if err != nil {
			return err
		}
		out, err := os.OpenFile(filepath.Join(dst, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			_ = in.Close()
			return err
		}
		_, copyErr := copyWithContext(ctx, out, in)
		err = errors.Join(copyErr, in.Close(), out.Close())
		if err != nil {
			return fmt.Errorf("vmrunner: copy bundle file %s: %w", name, err)
		}
	}
	ok = true
	return nil
}
