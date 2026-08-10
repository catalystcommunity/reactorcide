package vmrunner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/memory"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
)

func TestOCIImageSourceMaterializesMacBundleAndPrunesIt(t *testing.T) {
	ctx := context.Background()
	bundle := writeTestMacBundle(t)
	var archive bytes.Buffer
	require.NoError(t, WriteMacBundleArchive(ctx, bundle, &archive))

	store := memory.New()
	layerDesc, err := oras.PushBytes(ctx, store, VMMacBundleLayerMediaType, archive.Bytes())
	require.NoError(t, err)
	manifestDesc, err := oras.PackManifest(ctx, store, oras.PackManifestVersion1_1, VMImageArtifactType, oras.PackManifestOptions{Layers: []ocispec.Descriptor{layerDesc}})
	require.NoError(t, err)
	require.NoError(t, store.Tag(ctx, manifestDesc, "latest"))

	cacheDir := t.TempDir()
	source, err := NewOCIImageSource(cacheDir)
	require.NoError(t, err)
	source.sourceFactory = func(ref registry.Reference) (oras.ReadOnlyTarget, error) { return store, nil }

	path, err := source.Resolve(ctx, "example.invalid/reactorcide/macos:latest")
	require.NoError(t, err)
	require.NoError(t, ValidateMacBundle(path))
	accessPath := source.accessPath(manifestDesc)
	old := time.Now().Add(-31 * 24 * time.Hour)
	require.NoError(t, os.Chtimes(accessPath, old, old))

	removed, err := source.Prune(ctx, 30*24*time.Hour, time.Now())
	require.NoError(t, err)
	require.Equal(t, 1, removed)
	_, err = os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist)
}

// pushFakeVMImageArtifact packs a single-layer VM image artifact (per
// image_oci.go's assumed layout) containing blob into store under tag, and
// returns the layer's descriptor.
func pushFakeVMImageArtifact(t *testing.T, ctx context.Context, store *memory.Store, tag string, blob []byte) ocispec.Descriptor {
	t.Helper()

	layerDesc := content.NewDescriptorFromBytes(VMImageLayerMediaType, blob)
	require.NoError(t, store.Push(ctx, layerDesc, bytes.NewReader(blob)))

	manifestDesc, err := oras.PackManifest(ctx, store, oras.PackManifestVersion1_1, VMImageArtifactType, oras.PackManifestOptions{
		Layers: []ocispec.Descriptor{layerDesc},
	})
	require.NoError(t, err)
	require.NoError(t, store.Tag(ctx, manifestDesc, tag))

	return layerDesc
}

// TestOCIImageSource_ResolveFromFakeSource exercises Resolve end to end
// (manifest resolution, single-layer extraction, content-addressed cache
// path) against an in-memory oras.Target standing in for a registry --
// injected via the unexported sourceFactory seam -- so this test needs no
// network or container runtime. It also proves the "no re-pull on a cache
// hit" requirement directly: a call counter on sourceFactory must not
// increment on the second Resolve for the same ref.
func TestOCIImageSource_ResolveFromFakeSource(t *testing.T) {
	ctx := context.Background()

	original := []byte("fake-disk-image-bytes-for-fake-source-test")
	store := memory.New()
	const tag = "v1"
	layerDesc := pushFakeVMImageArtifact(t, ctx, store, tag, original)

	cacheDir := t.TempDir()
	src, err := NewOCIImageSource(cacheDir)
	require.NoError(t, err)

	var sourceCalls int
	src.sourceFactory = func(ref registry.Reference) (oras.ReadOnlyTarget, error) {
		sourceCalls++
		return store, nil
	}

	imageRef := "example.com/reactorcide/vm-test:" + tag

	path1, err := src.Resolve(ctx, imageRef)
	require.NoError(t, err)
	require.Equal(t, 1, sourceCalls, "first Resolve must consult the source")

	data, err := os.ReadFile(path1)
	require.NoError(t, err)
	require.Equal(t, original, data)

	wantPath := filepath.Join(cacheDir, "blobs", layerDesc.Digest.Algorithm().String(), layerDesc.Digest.Encoded())
	require.Equal(t, wantPath, path1)

	// Cache hit: same ref, sourceFactory (our stand-in for talking to the
	// registry) must not be invoked again.
	path2, err := src.Resolve(ctx, imageRef)
	require.NoError(t, err)
	require.Equal(t, path1, path2)
	require.Equal(t, 1, sourceCalls, "second Resolve of the same ref must be served from cache with no source access")
}

// tamperedFetchTarget wraps an oras.ReadOnlyTarget and substitutes
// different bytes than the wrapped target actually has whenever Fetch is
// asked for tamperDigest -- simulating a registry (or a corrupted transfer)
// serving content that doesn't match the digest a manifest declared for it.
type tamperedFetchTarget struct {
	oras.ReadOnlyTarget
	tamperDigest    digest.Digest
	tamperedContent []byte
}

func (t *tamperedFetchTarget) Fetch(ctx context.Context, target ocispec.Descriptor) (io.ReadCloser, error) {
	if target.Digest == t.tamperDigest {
		return io.NopCloser(bytes.NewReader(t.tamperedContent)), nil
	}
	return t.ReadOnlyTarget.Fetch(ctx, target)
}

// TestOCIImageSource_DigestMismatchRejected asserts Resolve fails, and
// leaves no cached blob behind, when the bytes served for the layer don't
// match the digest its manifest declares.
func TestOCIImageSource_DigestMismatchRejected(t *testing.T) {
	ctx := context.Background()

	original := []byte("fake-disk-image-bytes-for-digest-mismatch-test")
	store := memory.New()
	const tag = "v1"
	layerDesc := pushFakeVMImageArtifact(t, ctx, store, tag, original)

	// Same length as original (so this specifically exercises digest
	// verification, not a size check), but different content.
	tampered := append([]byte(nil), original...)
	tampered[0] ^= 0xFF

	cacheDir := t.TempDir()
	src, err := NewOCIImageSource(cacheDir)
	require.NoError(t, err)
	src.sourceFactory = func(ref registry.Reference) (oras.ReadOnlyTarget, error) {
		return &tamperedFetchTarget{ReadOnlyTarget: store, tamperDigest: layerDesc.Digest, tamperedContent: tampered}, nil
	}

	_, err = src.Resolve(ctx, "example.com/reactorcide/vm-test:"+tag)
	require.Error(t, err)

	blobPath := filepath.Join(cacheDir, "blobs", layerDesc.Digest.Algorithm().String(), layerDesc.Digest.Encoded())
	_, statErr := os.Stat(blobPath)
	require.True(t, os.IsNotExist(statErr), "a layer that failed digest verification must not be left cached")
}

// TestOCIImageSource_ArtifactShapeMismatch asserts Resolve returns a clear,
// specific error -- rather than silently guessing -- when an artifact
// doesn't match the single-VMImageLayerMediaType-layer shape image_oci.go
// documents as assumed.
func TestOCIImageSource_ArtifactShapeMismatch(t *testing.T) {
	ctx := context.Background()

	t.Run("no matching layer", func(t *testing.T) {
		store := memory.New()
		blob := []byte("not a vm image layer")
		desc := content.NewDescriptorFromBytes("application/vnd.oci.image.layer.v1.tar+gzip", blob)
		require.NoError(t, store.Push(ctx, desc, bytes.NewReader(blob)))
		manifestDesc, err := oras.PackManifest(ctx, store, oras.PackManifestVersion1_1, VMImageArtifactType, oras.PackManifestOptions{
			Layers: []ocispec.Descriptor{desc},
		})
		require.NoError(t, err)
		require.NoError(t, store.Tag(ctx, manifestDesc, "v1"))

		cacheDir := t.TempDir()
		src, err := NewOCIImageSource(cacheDir)
		require.NoError(t, err)
		src.sourceFactory = func(ref registry.Reference) (oras.ReadOnlyTarget, error) { return store, nil }

		_, err = src.Resolve(ctx, "example.com/reactorcide/vm-test:v1")
		require.Error(t, err)
		require.Contains(t, err.Error(), VMImageLayerMediaType)
	})

	t.Run("more than one matching layer", func(t *testing.T) {
		store := memory.New()
		blobA := []byte("layer-a-content")
		blobB := []byte("layer-b-content")
		descA := content.NewDescriptorFromBytes(VMImageLayerMediaType, blobA)
		descB := content.NewDescriptorFromBytes(VMImageLayerMediaType, blobB)
		require.NoError(t, store.Push(ctx, descA, bytes.NewReader(blobA)))
		require.NoError(t, store.Push(ctx, descB, bytes.NewReader(blobB)))
		manifestDesc, err := oras.PackManifest(ctx, store, oras.PackManifestVersion1_1, VMImageArtifactType, oras.PackManifestOptions{
			Layers: []ocispec.Descriptor{descA, descB},
		})
		require.NoError(t, err)
		require.NoError(t, store.Tag(ctx, manifestDesc, "v1"))

		cacheDir := t.TempDir()
		src, err := NewOCIImageSource(cacheDir)
		require.NoError(t, err)
		src.sourceFactory = func(ref registry.Reference) (oras.ReadOnlyTarget, error) { return store, nil }

		_, err = src.Resolve(ctx, "example.com/reactorcide/vm-test:v1")
		require.Error(t, err)
		require.Contains(t, err.Error(), "more than one")
	})
}

// TestOCIImageSource_ResolveViaLocalRegistry is the end-to-end check: a real
// registry:2 container (matching this repo's testcontainers-based
// integration test pattern -- see test/setup_test.go's postgres container),
// a real push over HTTP via oras-go's remote.Repository, and Resolve
// exercised completely unmodified (no injected fakes), including plain-HTTP
// auto-detection for the loopback registry and ambient docker-config
// credential resolution (anonymous, since the test registry requires no
// auth). Skips gracefully if a container runtime isn't available, and is
// skipped in -short mode like other container-backed tests in this repo.
// skipIfNoDocker skips a container-backed test when no Docker/OCI container
// runtime is reachable, BEFORE any testcontainers call. testcontainers-go's
// GenericContainer PANICS (via MustExtractDockerHost) rather than returning an
// error when no Docker host exists, so the usual "err != nil -> t.Skip" guard
// is unreachable in a no-Docker environment — e.g. reactorcide's own test-go CI
// pod (golang:1.26, no Docker), where this package's test binary would otherwise
// crash and fail the whole `go test` run. This pure env/socket check runs first
// so the test skips cleanly instead of panicking.
func skipIfNoDocker(t *testing.T) {
	t.Helper()
	if os.Getenv("DOCKER_HOST") != "" {
		return
	}
	candidates := []string{"/var/run/docker.sock"}
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		candidates = append(candidates,
			filepath.Join(xdg, "docker.sock"),
			filepath.Join(xdg, "podman", "podman.sock"),
		)
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return
		}
	}
	t.Skip("skipping: no Docker/OCI runtime reachable (no DOCKER_HOST and no known container socket)")
}

func TestOCIImageSource_ResolveViaLocalRegistry(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping OCI registry integration test in -short mode")
	}
	skipIfNoDocker(t)
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx := context.Background()

	req := testcontainers.ContainerRequest{
		Image:        "registry:2",
		ExposedPorts: []string{"5000/tcp"},
		WaitingFor: wait.ForHTTP("/v2/").
			WithPort("5000/tcp").
			WithStatusCodeMatcher(func(status int) bool { return status == http.StatusOK }).
			WithStartupTimeout(60 * time.Second),
	}
	regContainer, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Skipf("skipping: local OCI registry container unavailable: %v", err)
	}
	defer func() { _ = regContainer.Terminate(context.Background()) }()

	host, err := regContainer.Host(ctx)
	require.NoError(t, err)
	mappedPort, err := regContainer.MappedPort(ctx, "5000/tcp")
	require.NoError(t, err)
	registryAddr := fmt.Sprintf("%s:%s", host, mappedPort.Port())

	const tag = "v1"
	original := []byte("fake-disk-image-bytes-for-local-registry-test")
	store := memory.New()
	layerDesc := pushFakeVMImageArtifact(t, ctx, store, tag, original)

	repo, err := remote.NewRepository(registryAddr + "/reactorcide/vm-test")
	require.NoError(t, err)
	repo.PlainHTTP = true // registry:2 with no TLS configured

	_, err = oras.Copy(ctx, store, tag, repo, tag, oras.DefaultCopyOptions)
	require.NoError(t, err, "push fixture artifact to local registry")

	manifestDesc, err := repo.Resolve(ctx, tag)
	require.NoError(t, err)
	digestRef := fmt.Sprintf("%s/reactorcide/vm-test@%s", registryAddr, manifestDesc.Digest.String())

	cacheDir := t.TempDir()
	src, err := NewOCIImageSource(cacheDir)
	require.NoError(t, err)

	imageRef := registryAddr + "/reactorcide/vm-test:" + tag

	path1, err := src.Resolve(ctx, imageRef)
	require.NoError(t, err)
	data, err := os.ReadFile(path1)
	require.NoError(t, err)
	require.Equal(t, original, data)

	wantPath := filepath.Join(cacheDir, "blobs", layerDesc.Digest.Algorithm().String(), layerDesc.Digest.Encoded())
	require.Equal(t, wantPath, path1)

	// Prove the "no re-pull on a cache hit" requirement the hard way: kill
	// the registry entirely, then confirm both the original tag ref and a
	// freshly-minted digest-pinned ref resolve purely from local disk.
	require.NoError(t, regContainer.Terminate(ctx))

	path2, err := src.Resolve(ctx, imageRef)
	require.NoError(t, err, "tag ref must be servable from cache once the registry is gone")
	require.Equal(t, path1, path2)

	path3, err := src.Resolve(ctx, digestRef)
	require.NoError(t, err, "digest-pinned ref must be servable from cache with no network access")
	require.Equal(t, path1, path3)
}
