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

// Assumed VM base image artifact layout (VM_RUNNERS_PLAN.md's VM-2):
//
//   - The manifest is a plain OCI Image Manifest (schemaVersion 2). Its
//     ArtifactType/config blob are not inspected -- both the classic
//     "unknown config" convention and the OCI 1.1 empty-config-artifact
//     convention (as produced by oras.PackManifest) are accepted.
//   - The manifest has exactly one layer whose MediaType is
//     VMImageLayerMediaType. That single layer *is* the base VM disk image
//     -- opaque bytes as far as this package is concerned; VMLifecycle
//     (VM-3/VM-4) interprets/mounts the cached file once cloned.
//
// Resolve returns a clear, actionable error rather than guessing if a
// manifest has zero or more than one layer of that media type.
const (
	// VMImageArtifactType is the OCI artifactType a reactorcide VM base
	// image manifest is expected to declare. It is documentation/intent
	// only -- Resolve does not reject a manifest for using a different (or
	// absent) artifactType, since the load-bearing shape check is the
	// single VMImageLayerMediaType layer described above.
	VMImageArtifactType = "application/vnd.reactorcide.vm-image.v1"

	// VMImageLayerMediaType is the media type of the one layer Resolve
	// expects a VM base image manifest to carry: the raw/compressed VM disk
	// image blob.
	VMImageLayerMediaType = "application/vnd.reactorcide.vm-image.layer.v1"
)

// OCIImageSource resolves an imageRef to a locally cached base image file by
// pulling it as an OCI artifact from a registry (oras.land/oras-go/v2),
// mirroring VM_RUNNERS_PLAN.md's "images = OCI artifacts, pulled by
// ref+digest, cached locally by digest" decision. It implements the same
// ImageSource interface as LocalImageSource (image_local.go) so the two are
// swappable via config alone -- see internal/worker/vm_adapter.go's
// REACTORCIDE_VM_IMAGE_SOURCE selection.
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
// published (see VM_RUNNERS_PLAN.md's VM-5).
type OCIImageSource struct {
	cacheDir string
	cache    *ocistore.Store

	credentialStore credentials.Store
	httpClient      *http.Client
	plainHTTPHosts  map[string]struct{}
	copyConcurrency int

	// sourceFactory builds the read-only OCI target Resolve pulls from for
	// a given parsed reference. It defaults to defaultSource, which wires a
	// real registry/remote.Repository with ambient docker-config
	// credentials; image_oci_test.go substitutes a fake in-memory target on
	// the same-package OCIImageSource value to exercise digest-mismatch
	// rejection and artifact-shape handling without a live registry.
	sourceFactory func(ref registry.Reference) (oras.ReadOnlyTarget, error)
}

// OCIImageSourceOption configures optional OCIImageSource behavior.
type OCIImageSourceOption func(*OCIImageSource)

// WithCredentialStore overrides the credentials.Store used to resolve
// registry auth, in place of the default ambient docker-config store (see
// NewOCIImageSource). Never logs or otherwise surfaces the credentials it
// returns.
func WithCredentialStore(store credentials.Store) OCIImageSourceOption {
	return func(s *OCIImageSource) { s.credentialStore = store }
}

// WithHTTPClient overrides the *http.Client used for registry requests. If
// unset, a fresh http.Client (Go's zero-value defaults) is used.
func WithHTTPClient(c *http.Client) OCIImageSourceOption {
	return func(s *OCIImageSource) { s.httpClient = c }
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

// WithCopyConcurrency overrides how many blobs Resolve pulls concurrently
// while copying an artifact into the cache. <= 0 uses oras-go's own default
// (3, matching dockerd/containerd).
func WithCopyConcurrency(n int) OCIImageSourceOption {
	return func(s *OCIImageSource) { s.copyConcurrency = n }
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
		if manifest.Layers[i].MediaType != VMImageLayerMediaType {
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
	return path, nil
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

var _ ImageSource = (*OCIImageSource)(nil)
