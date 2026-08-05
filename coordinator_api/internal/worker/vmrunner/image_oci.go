package vmrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	ocistore "oras.land/oras-go/v2/content/oci"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
)

// Assumed VM base image artifact layout:
//
//   - The manifest is a plain OCI Image Manifest (schemaVersion 2). Its
//     ArtifactType/config blob are not inspected -- both the classic
//     "unknown config" convention and the OCI 1.1 empty-config-artifact
//     convention (as produced by oras.PackManifest) are accepted.
//   - The current macOS format has one VMMacBundleLayerMediaType layer. The
//     layer is a tar+zstd archive that materializes as a four-file bundle.
//   - VMImageLayerMediaType remains accepted for the prototype legacy
//     single-disk format used by Windows and early tests.
//
// Resolve returns a clear, actionable error rather than guessing if a
// manifest has zero or more than one layer of that media type.
const (
	// VMImageArtifactType is the OCI artifactType a reactorcide VM base
	// image manifest is expected to declare. It is documentation/intent
	// only -- Resolve uses the layer media type and shape as its load-bearing
	// validation.
	VMImageArtifactType = "application/vnd.reactorcide.vm-image.v1"

	// VMImageLayerMediaType is the media type of the one layer Resolve
	// expects a VM base image manifest to carry: the raw/compressed VM disk
	// image blob.
	VMImageLayerMediaType = "application/vnd.reactorcide.vm-image.layer.v1"

	// VMMacBundleLayerMediaType is the current macOS image layer format. It
	// contains the complete four-file bundle as a tar+zstd stream.
	VMMacBundleLayerMediaType = "application/vnd.reactorcide.vm-image.macos.bundle.v1.tar+zstd"
)

const DefaultImageMaxUnused = 30 * 24 * time.Hour

// OCIImageSource resolves an imageRef to a locally cached base image path by
// pulling it as an OCI artifact from a registry (oras.land/oras-go/v2),
// mirroring. It implements the same ImageSource interface as LocalImageSource
// (image_local.go) so the two are swappable via config alone -- see
// internal/worker/vm_adapter.go's REACTORCIDE_VM_IMAGE_SOURCE selection.
//
// Caching: the cache directory is a plain OCI Image Layout
// (oras.land/oras-go/v2/content/oci.Store), so blobs land content-addressed
// at "<cacheDir>/blobs/<algorithm>/<encoded-digest>" per the OCI Image
// Layout spec. content/oci.Store also auto-tags every manifest it stores
// under its own digest in addition to whatever reference (tag or digest) it
// was pulled under. That gives Resolve a cheap, always-available local
// lookup: once a reference -- tag or digest -- has been resolved once, a
// later Resolve call for that exact same reference string is served
// entirely from local disk with no registry round trip at all (verified in
// image_oci_test.go by tearing down the registry between calls).
//
// This means a mutable tag is only as fresh as its first successful
// Resolve: OCIImageSource does not re-check the registry for a moved tag
// once cached, favoring the run-local-friendly property that a warm cache
// works fully offline. Callers that need a tag's latest content should
// either resolve a fresh digest-pinned reference or clear/prune the cache
// directory; pinning base images by digest sidesteps the question
// entirely and is the recommended way to reference a VM base image once
// published.
type OCIImageSource struct {
	cacheDir string
	cache    *ocistore.Store
	mu       sync.Mutex

	credentialStore credentials.Store
	httpClient      *http.Client
	plainHTTPHosts  map[string]struct{}
	copyConcurrency int

	// sourceFactory builds the read-only OCI target Resolve pulls from for
	// a given parsed reference. It defaults to defaultSource, which wires a
	// real registry/remote.Repository with the configured credential store;
	// image_oci_test.go substitutes a fake in-memory target on
	// the same-package OCIImageSource value to exercise digest-mismatch
	// rejection and artifact-shape handling without a live registry.
	sourceFactory func(ref registry.Reference) (oras.ReadOnlyTarget, error)
}

// OCIImageSourceOption configures optional OCIImageSource behavior.
type OCIImageSourceOption func(*OCIImageSource)

// WithCredentialStore overrides the credentials.Store used to resolve
// registry auth. If this option is absent, NewOCIImageSource uses the ambient
// Docker credential store for compatibility. It never logs credentials.
func WithCredentialStore(store credentials.Store) OCIImageSourceOption {
	return func(s *OCIImageSource) { s.credentialStore = store }
}

// WithPlainHTTPRegistries marks the given registry hosts (as they appear in
// an image reference, e.g. "127.0.0.1:5000" or "registry.internal:5000") as
// reachable over plain HTTP instead of HTTPS -- for a local/dev registry
// that doesn't terminate TLS. "localhost", "127.0.0.1", and "::1" (with any
// port) are always treated as plain HTTP without needing this option.
func WithPlainHTTPRegistries(hosts ...string) OCIImageSourceOption {
	return func(s *OCIImageSource) {
		for _, h := range hosts {
			if h == "" {
				continue
			}
			s.plainHTTPHosts[h] = struct{}{}
		}
	}
}

// NewOCIImageSource builds an OCIImageSource caching pulled base images
// under cacheDir (created if it doesn't already exist). Registry
// credentials come from the ambient docker config file (via
// oras.land/oras-go/v2/registry/remote/credentials.NewStoreFromDocker --
// $DOCKER_CONFIG or ~/.docker/config.json, native credential helpers
// included) unless WithCredentialStore overrides it; a missing/absent
// config file is treated as "no credentials configured" rather than an
// error, so anonymous access to public registries works with no setup.
// Nothing about credential resolution is logged.
func NewOCIImageSource(cacheDir string, opts ...OCIImageSourceOption) (*OCIImageSource, error) {
	if cacheDir == "" {
		return nil, errors.New("vmrunner: OCI image cache dir must not be empty")
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("vmrunner: create OCI image cache dir %q: %w", cacheDir, err)
	}
	store, err := ocistore.New(cacheDir)
	if err != nil {
		return nil, fmt.Errorf("vmrunner: open OCI image cache %q: %w", cacheDir, err)
	}
	store.AutoGC = true

	s := &OCIImageSource{
		cacheDir:       cacheDir,
		cache:          store,
		plainHTTPHosts: make(map[string]struct{}),
	}
	for _, opt := range opts {
		opt(s)
	}

	if s.credentialStore == nil {
		// credentials.NewStoreFromDocker only errors on a malformed config
		// file (see its Load semantics); a missing one yields a valid,
		// empty store. Anonymous access must keep working even then, so a
		// genuine error here still falls back rather than failing
		// construction.
		if cs, err := credentials.NewStoreFromDocker(credentials.StoreOptions{}); err == nil {
			s.credentialStore = cs
		} else {
			s.credentialStore = credentials.NewMemoryStore()
		}
	}

	s.sourceFactory = s.defaultSource
	return s, nil
}

// CacheDir returns the OCI Image Layout root Resolve caches pulled blobs
// under.
func (s *OCIImageSource) CacheDir() string {
	return s.cacheDir
}

// Resolve implements ImageSource. imageRef is an OCI reference
// (registry/repo:tag or registry/repo@sha256:...). See the OCIImageSource
// doc comment for the caching semantics and this file's package-level doc
// comment for the assumed artifact layout.
func (s *OCIImageSource) Resolve(ctx context.Context, imageRef string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resolveLocked(ctx, imageRef)
}

func (s *OCIImageSource) resolveLocked(ctx context.Context, imageRef string) (string, error) {
	if imageRef == "" {
		return "", errors.New("vmrunner: image reference must not be empty")
	}

	ref, err := registry.ParseReference(imageRef)
	if err != nil {
		return "", fmt.Errorf("vmrunner: invalid OCI image reference %q: %w", imageRef, err)
	}
	if err := ref.ValidateRepository(); err != nil {
		return "", fmt.Errorf("vmrunner: invalid OCI image reference %q: %w", imageRef, err)
	}
	reference := ref.ReferenceOrDefault()

	// Cache hit: content/oci.Store resolves a reference purely from the
	// local index/blob store (see the OCIImageSource doc comment) with no
	// network access at all.
	if desc, err := s.cache.Resolve(ctx, reference); err == nil {
		if path, perr := s.layerPath(ctx, desc); perr == nil {
			return path, nil
		}
		// Cached manifest present but its layer blob is missing/invalid
		// (e.g. a pruned cache dir) -- fall through and re-pull.
	}

	src, err := s.sourceFactory(ref)
	if err != nil {
		return "", err
	}

	manifestDesc, err := oras.Copy(ctx, src, reference, s.cache, reference, oras.CopyOptions{
		CopyGraphOptions: oras.CopyGraphOptions{Concurrency: s.copyConcurrency},
	})
	if err != nil {
		return "", fmt.Errorf("vmrunner: pull OCI vm image %q: %w", imageRef, err)
	}

	return s.layerPath(ctx, manifestDesc)
}

// defaultSource builds the real registry/remote.Repository Resolve pulls
// from for ref, wired with ambient docker-config credentials and plain-HTTP
// detection. It is OCIImageSource's default sourceFactory.
func (s *OCIImageSource) defaultSource(ref registry.Reference) (oras.ReadOnlyTarget, error) {
	repo := &remote.Repository{
		Reference: ref,
		PlainHTTP: s.plainHTTP(ref.Host()),
		Client: &auth.Client{
			Client:     s.httpClient,
			Cache:      auth.NewCache(),
			Credential: credentials.Credential(s.credentialStore),
		},
	}
	return repo, nil
}

// plainHTTP reports whether host should be reached over plain HTTP:
// explicitly configured via WithPlainHTTPRegistries, or a loopback address
// (a local/dev registry almost never terminates TLS).
func (s *OCIImageSource) plainHTTP(host string) bool {
	if _, ok := s.plainHTTPHosts[host]; ok {
		return true
	}
	return isLoopbackRegistryHost(host)
}

// isLoopbackRegistryHost strips an optional ":port" and reports whether
// what remains is a loopback hostname.
func isLoopbackRegistryHost(host string) bool {
	h := host
	if i := strings.LastIndex(h, ":"); i >= 0 {
		h = h[:i]
	}
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

// layerPath fetches the cached manifest at manifestDesc, finds its single
// VMImageLayerMediaType layer, and returns that layer's content-addressed
// path on disk. See this file's package-level doc comment for the assumed
// artifact shape.
func (s *OCIImageSource) layerPath(ctx context.Context, manifestDesc ocispec.Descriptor) (string, error) {
	raw, err := content.FetchAll(ctx, s.cache, manifestDesc)
	if err != nil {
		return "", fmt.Errorf("vmrunner: fetch cached vm image manifest: %w", err)
	}

	var manifest ocispec.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return "", fmt.Errorf("vmrunner: decode vm image manifest: %w", err)
	}

	var layer *ocispec.Descriptor
	for i := range manifest.Layers {
		if manifest.Layers[i].MediaType != VMImageLayerMediaType && manifest.Layers[i].MediaType != VMMacBundleLayerMediaType {
			continue
		}
		if layer != nil {
			return "", fmt.Errorf(
				"vmrunner: vm image artifact has more than one %q layer; expected exactly one base-image layer",
				VMImageLayerMediaType,
			)
		}
		layer = &manifest.Layers[i]
	}
	if layer == nil {
		return "", fmt.Errorf(
			"vmrunner: vm image artifact has no layer of media type %q (found %d layer(s)); expected a single-layer VM base image artifact -- see OCIImageSource's doc comment for the assumed layout",
			VMImageLayerMediaType, len(manifest.Layers),
		)
	}

	if layer.MediaType == VMMacBundleLayerMediaType {
		path, err := s.materializeMacBundle(ctx, manifestDesc, *layer)
		if err != nil {
			return "", err
		}
		if err := s.touchAccess(manifestDesc); err != nil {
			return "", err
		}
		return path, nil
	}

	path, err := s.blobPath(layer.Digest)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("vmrunner: cached vm image layer missing at %s: %w", path, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("vmrunner: cached vm image layer at %s is a directory, expected a file", path)
	}
	if err := s.touchAccess(manifestDesc); err != nil {
		return "", err
	}
	return path, nil
}

func (s *OCIImageSource) materializeMacBundle(ctx context.Context, manifestDesc, layer ocispec.Descriptor) (string, error) {
	root := filepath.Join(s.cacheDir, "bundles", manifestDesc.Digest.Algorithm().String())
	dst := filepath.Join(root, manifestDesc.Digest.Encoded())
	if err := ValidateMacBundle(dst); err == nil {
		return dst, nil
	}
	if err := os.Chmod(dst, 0o700); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("vmrunner: unlock invalid materialized bundle: %w", err)
	}
	if err := os.RemoveAll(dst); err != nil {
		return "", fmt.Errorf("vmrunner: remove invalid materialized bundle: %w", err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("vmrunner: create materialized bundle root: %w", err)
	}
	tmp, err := os.MkdirTemp(root, ".extract-")
	if err != nil {
		return "", fmt.Errorf("vmrunner: create materialized bundle temp dir: %w", err)
	}
	defer os.RemoveAll(tmp)

	r, err := s.cache.Fetch(ctx, layer)
	if err != nil {
		return "", fmt.Errorf("vmrunner: open cached macOS bundle layer: %w", err)
	}
	extractErr := ExtractMacBundleArchive(ctx, r, tmp)
	closeErr := r.Close()
	if err := errors.Join(extractErr, closeErr); err != nil {
		return "", fmt.Errorf("vmrunner: materialize macOS bundle: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		if validateErr := ValidateMacBundle(dst); validateErr == nil {
			return dst, nil
		}
		return "", fmt.Errorf("vmrunner: publish materialized macOS bundle: %w", err)
	}
	if err := os.Chmod(dst, 0o555); err != nil {
		return "", fmt.Errorf("vmrunner: protect materialized macOS bundle: %w", err)
	}
	return dst, nil
}

func (s *OCIImageSource) accessPath(desc ocispec.Descriptor) string {
	return filepath.Join(s.cacheDir, "access", desc.Digest.Algorithm().String(), desc.Digest.Encoded())
}

func (s *OCIImageSource) touchAccess(desc ocispec.Descriptor) error {
	path := s.accessPath(desc)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("vmrunner: create image access directory: %w", err)
	}
	now := time.Now()
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		return fmt.Errorf("vmrunner: record image access: %w", err)
	}
	if err := os.Chtimes(path, now, now); err != nil {
		return fmt.Errorf("vmrunner: update image access time: %w", err)
	}
	return nil
}

// Prune removes manifests, blobs, and materialized bundles that have not been
// resolved within maxUnused. It returns the number of removed images.
func (s *OCIImageSource) Prune(ctx context.Context, maxUnused time.Duration, now time.Time) (int, error) {
	if maxUnused <= 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	accessRoot := filepath.Join(s.cacheDir, "access")
	removed := 0
	err := filepath.WalkDir(accessRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if now.Sub(info.ModTime()) <= maxUnused {
			return nil
		}
		algorithm := filepath.Base(filepath.Dir(path))
		dgst := digest.Digest(algorithm + ":" + entry.Name())
		if err := dgst.Validate(); err != nil {
			return fmt.Errorf("vmrunner: invalid cache access record %q: %w", path, err)
		}
		desc, err := s.cache.Resolve(ctx, dgst.String())
		if err == nil {
			if err := s.cache.Delete(ctx, desc); err != nil {
				return fmt.Errorf("vmrunner: delete cached image %s: %w", dgst, err)
			}
		}
		bundlePath := filepath.Join(s.cacheDir, "bundles", algorithm, entry.Name())
		if err := os.Chmod(bundlePath, 0o700); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("vmrunner: unlock materialized bundle %s: %w", dgst, err)
		}
		if err := os.RemoveAll(bundlePath); err != nil {
			return fmt.Errorf("vmrunner: remove materialized bundle %s: %w", dgst, err)
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("vmrunner: remove image access record %s: %w", dgst, err)
		}
		removed++
		return nil
	})
	if os.IsNotExist(err) {
		return 0, nil
	}
	return removed, err
}

// blobPath computes a digest's path in the cache's OCI Image Layout, per
// https://github.com/opencontainers/image-spec/blob/v1.1.1/image-layout.md.
func (s *OCIImageSource) blobPath(dgst digest.Digest) (string, error) {
	if err := dgst.Validate(); err != nil {
		return "", fmt.Errorf("vmrunner: invalid layer digest %q: %w", dgst, err)
	}
	return filepath.Join(s.cacheDir, "blobs", dgst.Algorithm().String(), dgst.Encoded()), nil
}

// DefaultImageCacheDir returns the default local cache root OCIImageSource
// pulls/stores base image blobs under when no explicit cache dir is
// configured: ~/.cache/reactorcide/vm-images -- distinct from
// ~/.config/reactorcide (the worker's --data-dir default in cmd/worker.go)
// since this directory holds reproducible, re-fetchable cache content
// rather than state. Falls back to a relative directory if the home
// directory can't be resolved, mirroring cmd.defaultWorkerDataDir's
// fallback shape.
func DefaultImageCacheDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".cache", "reactorcide", "vm-images")
	}
	return filepath.Join(".reactorcide", "vm-images")
}

// DefaultRegistryAuthFile returns the dedicated Docker-compatible credential
// file used by VM image CLI and worker operations.
func DefaultRegistryAuthFile() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".config", "reactorcide", "oci-auth.json")
	}
	return filepath.Join(".reactorcide", "oci-auth.json")
}

var _ ImageSource = (*OCIImageSource)(nil)
