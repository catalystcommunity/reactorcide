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

type bundleArchiveWriter func(context.Context, string, io.Writer) error

type countingWriter struct{ count int64 }

func (w *countingWriter) Write(p []byte) (int, error) {
	w.count += int64(len(p))
	return len(p), nil
}

// PushMacBundle packages bundleDir and publishes it as a Reactorcide OCI artifact.
func PushMacBundle(ctx context.Context, bundleDir, imageRef string, credentialStore credentials.Store, plainHTTPHosts []string) (string, error) {
	return pushBundle(ctx, bundleDir, imageRef, credentialStore, plainHTTPHosts, VMMacBundleLayerMediaType, "macOS", "macos-vm-bundle.tar.zst", WriteMacBundleArchive)
}

// PushWindowsBundle packages a VHDX and its guest host public key as one OCI artifact.
func PushWindowsBundle(ctx context.Context, bundleDir, imageRef string, credentialStore credentials.Store, plainHTTPHosts []string) (string, error) {
	return pushBundle(ctx, bundleDir, imageRef, credentialStore, plainHTTPHosts, VMWindowsBundleLayerMediaType, "Windows", "windows-vm-bundle.tar.zst", WriteWindowsBundleArchive)
}

// PushVMBundle detects the supported local bundle shape and publishes it.
func PushVMBundle(ctx context.Context, bundleDir, imageRef string, credentialStore credentials.Store, plainHTTPHosts []string) (string, error) {
	if err := ValidateMacBundle(bundleDir); err == nil {
		return PushMacBundle(ctx, bundleDir, imageRef, credentialStore, plainHTTPHosts)
	}
	if err := ValidateWindowsBundle(bundleDir); err == nil {
		return PushWindowsBundle(ctx, bundleDir, imageRef, credentialStore, plainHTTPHosts)
	}
	return "", fmt.Errorf("vmrunner: %q is not a valid macOS or Windows VM bundle", bundleDir)
}

func pushBundle(ctx context.Context, bundleDir, imageRef string, credentialStore credentials.Store, plainHTTPHosts []string, mediaType, platform, archiveName string, writeArchive bundleArchiveWriter) (string, error) {
	ref, err := parseOCIReference(imageRef)
	if err != nil {
		return "", err
	}
	repo, err := newRemoteRepository(ref, credentialStore, plainHTTPHosts)
	if err != nil {
		return "", err
	}

	h := sha256.New()
	size := &countingWriter{}
	if err := writeArchive(ctx, bundleDir, io.MultiWriter(h, size)); err != nil {
		return "", err
	}
	layer := ocispec.Descriptor{
		MediaType: mediaType,
		Digest:    digest.NewDigest(digest.SHA256, h),
		Size:      size.count,
		Annotations: map[string]string{
			ocispec.AnnotationTitle: archiveName,
		},
	}
	archive, archiveWriter := io.Pipe()
	go func() {
		archiveWriter.CloseWithError(writeArchive(ctx, bundleDir, archiveWriter))
	}()
	pushErr := repo.Push(ctx, layer, archive)
	closeErr := archive.Close()
	if pushErr != nil && !errors.Is(pushErr, errdef.ErrAlreadyExists) {
		return "", fmt.Errorf("vmrunner: push %s image layer: %w", platform, pushErr)
	}
	if closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
		return "", fmt.Errorf("vmrunner: close image archive: %w", closeErr)
	}

	manifest, err := oras.PackManifest(ctx, repo, oras.PackManifestVersion1_1, VMImageArtifactType, oras.PackManifestOptions{
		Layers: []ocispec.Descriptor{layer},
		ManifestAnnotations: map[string]string{
			ocispec.AnnotationCreated:        time.Now().UTC().Format(time.RFC3339),
			"org.opencontainers.image.title": "Reactorcide " + platform + " VM image",
		},
	})
	if err != nil {
		return "", fmt.Errorf("vmrunner: create %s image manifest: %w", platform, err)
	}
	if err := repo.Tag(ctx, manifest, ref.ReferenceOrDefault()); err != nil {
		return "", fmt.Errorf("vmrunner: tag %s image manifest: %w", platform, err)
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
	return copyBundleFiles(ctx, src, dst, macBundleFiles)
}

func copyBundleFiles(ctx context.Context, src, dst string, files []string) error {
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
	for _, name := range files {
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

// CopyWindowsBundle copies a materialized Windows bundle without overwriting dst.
func CopyWindowsBundle(ctx context.Context, src, dst string) error {
	if err := ValidateWindowsBundle(src); err != nil {
		return err
	}
	return copyBundleFiles(ctx, src, dst, windowsBundleFiles)
}

// CopyVMBundle detects and copies a supported materialized bundle.
func CopyVMBundle(ctx context.Context, src, dst string) error {
	if err := ValidateMacBundle(src); err == nil {
		return CopyMacBundle(ctx, src, dst)
	}
	if err := ValidateWindowsBundle(src); err == nil {
		return CopyWindowsBundle(ctx, src, dst)
	}
	return fmt.Errorf("vmrunner: %q is not a valid macOS or Windows VM bundle", src)
}
