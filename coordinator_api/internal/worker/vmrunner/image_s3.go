package vmrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/errdef"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/imageref"
)

// S3 VM image store.
//
// An S3 image location is an OCI Image Layout (image-spec v1.1
// image-layout.md) under a bucket and key prefix:
//
//	s3://bucket/prefix/name:tag@sha256:<hex>
//	  -> s3://bucket/prefix/name/oci-layout
//	     s3://bucket/prefix/name/index.json
//	     s3://bucket/prefix/name/blobs/sha256/<hex>
//
// The blobs are the same bytes that an OCI registry stores, so the manifest
// digest is the same in both stores. A tag is a free-form string (decision
// I3). index.json records it in the org.opencontainers.image.ref.name
// annotation of the manifest descriptor. The digest is the pin; the tag is
// only a name.
//
// s3LayoutTarget implements oras.Target, so publish and pull use the same
// oras.Copy / oras.PackManifest code paths as a registry. The local cache
// (content/oci.Store) verifies the size and digest of every blob that a pull
// writes, so a tampered S3 object is rejected before it is extracted.

const s3ImageScheme = "s3://"

// s3ImageLocation is a parsed s3:// VM image reference.
type s3ImageLocation struct {
	Bucket string
	// Key is the key prefix of the OCI layout, without a trailing slash
	// ("prefix/name").
	Key    string
	Tag    string
	Digest digest.Digest
}

// isS3ImageRef reports whether ref uses the s3:// scheme.
func isS3ImageRef(ref string) bool {
	return strings.HasPrefix(strings.TrimSpace(ref), s3ImageScheme)
}

// parseS3ImageRef parses s3://bucket/prefix/name[:tag][@sha256:<hex>] with
// the shared reference parser (internal/imageref), so that the worker, job
// creation, and image aliases accept the same references.
//
// Rules (imageref.Parse): the bucket is the first path segment. The tag is
// after the first ':' of the last path segment, so a key prefix and a name
// cannot contain ':'. A tag is free-form (a hash is a valid tag), but it has
// no whitespace, no control character, no '/', and no '@'. A reference has
// one '@' only: the one before the sha256 digest. Leading and trailing
// space is removed first.
func parseS3ImageRef(ref string) (s3ImageLocation, error) {
	raw := strings.TrimSpace(ref)
	if !strings.HasPrefix(raw, s3ImageScheme) {
		return s3ImageLocation{}, fmt.Errorf("vmrunner: %q is not an s3:// image reference", ref)
	}
	parsed, err := imageref.Parse(raw)
	if err != nil {
		return s3ImageLocation{}, fmt.Errorf("vmrunner: s3 image reference: %w", err)
	}
	bucket, key, ok := strings.Cut(parsed.Location, "/")
	if !ok || bucket == "" || key == "" {
		return s3ImageLocation{}, fmt.Errorf("vmrunner: s3 image reference %q must be s3://bucket/prefix/name[:tag][@sha256:<hex>]", ref)
	}
	return s3ImageLocation{Bucket: bucket, Key: key, Tag: parsed.Tag, Digest: digest.Digest(parsed.Digest)}, nil
}

// String formats the location as a reference. It includes the tag and the
// digest when they are set.
func (l s3ImageLocation) String() string {
	s := s3ImageScheme + l.Bucket + "/" + l.Key
	if l.Tag != "" {
		s += ":" + l.Tag
	}
	if l.Digest != "" {
		s += "@" + l.Digest.String()
	}
	return s
}

// objectKey returns the S3 key of a file in the layout.
func (l s3ImageLocation) objectKey(name string) string {
	return l.Key + "/" + name
}

// S3ImageStoreConfig configures the S3 client of the s3:// VM image store.
// Credentials come from the standard AWS chain (environment variables,
// shared config and credentials files, SSO, instance or task roles). They
// are never logged.
type S3ImageStoreConfig struct {
	// Region is the AWS region. Empty uses AWS_REGION, then us-east-1.
	Region string
	// Endpoint is an S3-compatible endpoint URL (MinIO, SeaweedFS, Ceph).
	// Empty uses AWS S3. A set endpoint uses path-style addressing, like
	// internal/objects.
	Endpoint string
}

// S3ImageStoreConfigFromEnv reads REACTORCIDE_VM_S3_ENDPOINT (fallback
// REACTORCIDE_S3_ENDPOINT) and AWS_REGION.
func S3ImageStoreConfigFromEnv() S3ImageStoreConfig {
	endpoint := strings.TrimSpace(os.Getenv("REACTORCIDE_VM_S3_ENDPOINT"))
	if endpoint == "" {
		endpoint = strings.TrimSpace(os.Getenv("REACTORCIDE_S3_ENDPOINT"))
	}
	return S3ImageStoreConfig{
		Region:   strings.TrimSpace(os.Getenv("AWS_REGION")),
		Endpoint: endpoint,
	}
}

// s3ObjectAPI is the part of the S3 client that the image store uses. Tests
// replace it with an in-memory fake.
type s3ObjectAPI interface {
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	CreateMultipartUpload(context.Context, *s3.CreateMultipartUploadInput, ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error)
	UploadPart(context.Context, *s3.UploadPartInput, ...func(*s3.Options)) (*s3.UploadPartOutput, error)
	CompleteMultipartUpload(context.Context, *s3.CompleteMultipartUploadInput, ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error)
	AbortMultipartUpload(context.Context, *s3.AbortMultipartUploadInput, ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error)
}

// newS3ObjectAPI builds an S3 client the same way as internal/objects.
func newS3ObjectAPI(ctx context.Context, cfg S3ImageStoreConfig) (s3ObjectAPI, error) {
	region := cfg.Region
	if region == "" {
		region = strings.TrimSpace(os.Getenv("AWS_REGION"))
	}
	if region == "" {
		region = "us-east-1"
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("vmrunner: load S3 configuration: %w", err)
	}
	// The pull verifies the digest of every blob, so the SDK warning about
	// responses without an S3 checksum (common on S3-compatible services) is
	// noise.
	opts := []func(*s3.Options){func(o *s3.Options) { o.DisableLogOutputChecksumValidationSkipped = true }}
	if cfg.Endpoint != "" {
		endpoint := cfg.Endpoint
		opts = append(opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(endpoint)
			o.UsePathStyle = true
		})
	}
	return s3.NewFromConfig(awsCfg, opts...), nil
}

// s3ClientCache builds one S3 client on first use and shares it.
type s3ClientCache struct {
	cfg    S3ImageStoreConfig
	once   sync.Once
	client s3ObjectAPI
	err    error
}

func (c *s3ClientCache) get(ctx context.Context) (s3ObjectAPI, error) {
	c.once.Do(func() { c.client, c.err = newS3ObjectAPI(ctx, c.cfg) })
	return c.client, c.err
}

// s3LayoutTarget is an oras.Target backed by one OCI layout in S3.
type s3LayoutTarget struct {
	client s3ObjectAPI
	loc    s3ImageLocation
	// partSize overrides the multipart part size (tests).
	partSize int64
	// indexMu serializes index.json updates in this process. Two publishers
	// in different processes that tag the same layout at the same time can
	// lose one tag (last writer wins); the blobs and the digest stay valid.
	indexMu sync.Mutex
}

var _ oras.Target = (*s3LayoutTarget)(nil)

const (
	s3MinPartSize   = int64(32) << 20
	s3MaxParts      = 9000
	maxManifestSize = int64(4) << 20
	maxIndexSize    = int64(16) << 20
)

func (t *s3LayoutTarget) blobKey(d digest.Digest) string {
	return t.loc.objectKey("blobs/" + d.Algorithm().String() + "/" + d.Encoded())
}

// Fetch implements content.Fetcher. The consumer verifies the content.
func (t *s3LayoutTarget) Fetch(ctx context.Context, desc ocispec.Descriptor) (io.ReadCloser, error) {
	if err := desc.Digest.Validate(); err != nil {
		return nil, fmt.Errorf("vmrunner: invalid blob digest %q: %w", desc.Digest, err)
	}
	out, err := t.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(t.loc.Bucket), Key: aws.String(t.blobKey(desc.Digest))})
	if err != nil {
		if isS3ImageNotFound(err) {
			return nil, fmt.Errorf("%s: %w", desc.Digest, errdef.ErrNotFound)
		}
		return nil, fmt.Errorf("vmrunner: get S3 blob %s: %w", desc.Digest, err)
	}
	if out.ContentLength != nil && *out.ContentLength != desc.Size {
		_ = out.Body.Close()
		return nil, fmt.Errorf("vmrunner: S3 blob %s has size %d, expected %d", desc.Digest, *out.ContentLength, desc.Size)
	}
	return out.Body, nil
}

// Exists implements content.Storage.
func (t *s3LayoutTarget) Exists(ctx context.Context, desc ocispec.Descriptor) (bool, error) {
	if err := desc.Digest.Validate(); err != nil {
		return false, fmt.Errorf("vmrunner: invalid blob digest %q: %w", desc.Digest, err)
	}
	out, err := t.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(t.loc.Bucket), Key: aws.String(t.blobKey(desc.Digest))})
	if err != nil {
		if isS3ImageNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("vmrunner: check S3 blob %s: %w", desc.Digest, err)
	}
	// A blob with the wrong size is treated as missing, so Push replaces it.
	return out.ContentLength == nil || *out.ContentLength == desc.Size, nil
}

// Push implements content.Pusher. It verifies the size and digest of the
// stream while it uploads. A mismatch aborts the upload before the object
// is complete, so a wrong blob never exists under a digest key.
func (t *s3LayoutTarget) Push(ctx context.Context, desc ocispec.Descriptor, r io.Reader) error {
	if err := desc.Digest.Validate(); err != nil {
		return fmt.Errorf("vmrunner: invalid blob digest %q: %w", desc.Digest, err)
	}
	if exists, err := t.Exists(ctx, desc); err == nil && exists {
		return nil
	}
	if err := t.ensureLayoutFile(ctx); err != nil {
		return err
	}
	return t.putVerified(ctx, t.blobKey(desc.Digest), desc, r)
}

func (t *s3LayoutTarget) putVerified(ctx context.Context, key string, desc ocispec.Descriptor, r io.Reader) error {
	vr := &verifyingReader{r: r, want: desc.Digest, size: desc.Size, h: desc.Digest.Algorithm().Hash()}
	partSize := t.partSize
	if partSize <= 0 {
		partSize = s3MinPartSize
		if need := (desc.Size + s3MaxParts - 1) / s3MaxParts; need > partSize {
			partSize = (need + (1 << 20) - 1) &^ ((1 << 20) - 1)
		}
	}
	if desc.Size <= partSize {
		data, err := io.ReadAll(vr)
		if err != nil {
			return err
		}
		_, err = t.client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:        aws.String(t.loc.Bucket),
			Key:           aws.String(key),
			Body:          bytes.NewReader(data),
			ContentLength: aws.Int64(int64(len(data))),
			ContentType:   aws.String(contentTypeOf(desc)),
		})
		if err != nil {
			return fmt.Errorf("vmrunner: put S3 object %s: %w", key, err)
		}
		return nil
	}

	created, err := t.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(t.loc.Bucket), Key: aws.String(key), ContentType: aws.String(contentTypeOf(desc)),
	})
	if err != nil {
		return fmt.Errorf("vmrunner: start S3 multipart upload %s: %w", key, err)
	}
	abort := func(cause error) error {
		_, abortErr := t.client.AbortMultipartUpload(context.Background(), &s3.AbortMultipartUploadInput{
			Bucket: aws.String(t.loc.Bucket), Key: aws.String(key), UploadId: created.UploadId,
		})
		if abortErr != nil {
			return errors.Join(cause, fmt.Errorf("vmrunner: abort S3 multipart upload: %w", abortErr))
		}
		return cause
	}
	buf := make([]byte, partSize)
	var parts []s3types.CompletedPart
	for number := int32(1); ; number++ {
		n, readErr := io.ReadFull(vr, buf)
		if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) && !errors.Is(readErr, io.EOF) {
			return abort(readErr)
		}
		if n == 0 {
			break
		}
		out, err := t.client.UploadPart(ctx, &s3.UploadPartInput{
			Bucket: aws.String(t.loc.Bucket), Key: aws.String(key), UploadId: created.UploadId,
			PartNumber: aws.Int32(number), Body: bytes.NewReader(buf[:n]), ContentLength: aws.Int64(int64(n)),
		})
		if err != nil {
			return abort(fmt.Errorf("vmrunner: upload S3 part %d of %s: %w", number, key, err))
		}
		parts = append(parts, s3types.CompletedPart{ETag: out.ETag, PartNumber: aws.Int32(number)})
		if readErr != nil {
			break
		}
	}
	// Read to EOF so that verifyingReader checks the digest before the
	// upload completes.
	if _, err := io.Copy(io.Discard, vr); err != nil {
		return abort(err)
	}
	if _, err := t.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: aws.String(t.loc.Bucket), Key: aws.String(key), UploadId: created.UploadId,
		MultipartUpload: &s3types.CompletedMultipartUpload{Parts: parts},
	}); err != nil {
		return abort(fmt.Errorf("vmrunner: complete S3 multipart upload %s: %w", key, err))
	}
	return nil
}

func contentTypeOf(desc ocispec.Descriptor) string {
	if desc.MediaType != "" {
		return desc.MediaType
	}
	return "application/octet-stream"
}

// verifyingReader returns an error instead of io.EOF when the stream does
// not have the expected size and digest.
type verifyingReader struct {
	r    io.Reader
	want digest.Digest
	size int64
	n    int64
	h    hash.Hash
}

func (v *verifyingReader) Read(p []byte) (int, error) {
	n, err := v.r.Read(p)
	if n > 0 {
		v.n += int64(n)
		v.h.Write(p[:n])
		if v.n > v.size {
			return n, fmt.Errorf("vmrunner: blob %s is larger than %d bytes", v.want, v.size)
		}
	}
	if errors.Is(err, io.EOF) {
		if v.n != v.size {
			return n, fmt.Errorf("vmrunner: blob %s has %d bytes, expected %d", v.want, v.n, v.size)
		}
		if got := digest.NewDigest(v.want.Algorithm(), v.h); got != v.want {
			return n, fmt.Errorf("vmrunner: blob digest mismatch: got %s, expected %s", got, v.want)
		}
	}
	return n, err
}

func (t *s3LayoutTarget) ensureLayoutFile(ctx context.Context) error {
	key := t.loc.objectKey(ocispec.ImageLayoutFile)
	if _, err := t.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(t.loc.Bucket), Key: aws.String(key)}); err == nil {
		return nil
	} else if !isS3ImageNotFound(err) {
		return fmt.Errorf("vmrunner: check S3 %s: %w", ocispec.ImageLayoutFile, err)
	}
	data, _ := json.Marshal(ocispec.ImageLayout{Version: ocispec.ImageLayoutVersion})
	_, err := t.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(t.loc.Bucket), Key: aws.String(key), Body: bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))), ContentType: aws.String("application/json"),
	})
	if err != nil {
		return fmt.Errorf("vmrunner: put S3 %s: %w", ocispec.ImageLayoutFile, err)
	}
	return nil
}

// readIndex returns index.json, or an empty index when there is none.
func (t *s3LayoutTarget) readIndex(ctx context.Context) (ocispec.Index, error) {
	empty := ocispec.Index{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: ocispec.MediaTypeImageIndex}
	out, err := t.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(t.loc.Bucket), Key: aws.String(t.loc.objectKey(ocispec.ImageIndexFile))})
	if err != nil {
		if isS3ImageNotFound(err) {
			return empty, nil
		}
		return ocispec.Index{}, fmt.Errorf("vmrunner: get S3 %s: %w", ocispec.ImageIndexFile, err)
	}
	defer out.Body.Close()
	data, err := io.ReadAll(io.LimitReader(out.Body, maxIndexSize+1))
	if err != nil {
		return ocispec.Index{}, fmt.Errorf("vmrunner: read S3 %s: %w", ocispec.ImageIndexFile, err)
	}
	if int64(len(data)) > maxIndexSize {
		return ocispec.Index{}, fmt.Errorf("vmrunner: S3 %s is larger than %d bytes", ocispec.ImageIndexFile, maxIndexSize)
	}
	var index ocispec.Index
	if err := json.Unmarshal(data, &index); err != nil {
		return ocispec.Index{}, fmt.Errorf("vmrunner: decode S3 %s: %w", ocispec.ImageIndexFile, err)
	}
	return index, nil
}

// Tag implements content.Tagger. It records reference as the
// org.opencontainers.image.ref.name annotation in index.json and removes the
// name from any other manifest.
func (t *s3LayoutTarget) Tag(ctx context.Context, desc ocispec.Descriptor, reference string) error {
	if reference == "" {
		return errdef.ErrMissingReference
	}
	exists, err := t.Exists(ctx, desc)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%s: %w", desc.Digest, errdef.ErrNotFound)
	}
	t.indexMu.Lock()
	defer t.indexMu.Unlock()
	index, err := t.readIndex(ctx)
	if err != nil {
		return err
	}
	manifests := index.Manifests[:0]
	for _, m := range index.Manifests {
		if m.Annotations[ocispec.AnnotationRefName] == reference {
			continue
		}
		manifests = append(manifests, m)
	}
	entry := ocispec.Descriptor{
		MediaType:    desc.MediaType,
		Digest:       desc.Digest,
		Size:         desc.Size,
		ArtifactType: desc.ArtifactType,
		Annotations:  map[string]string{ocispec.AnnotationRefName: reference},
	}
	index.Manifests = append(manifests, entry)
	if index.SchemaVersion == 0 {
		index.SchemaVersion = 2
	}
	if index.MediaType == "" {
		index.MediaType = ocispec.MediaTypeImageIndex
	}
	data, err := json.Marshal(index)
	if err != nil {
		return fmt.Errorf("vmrunner: encode %s: %w", ocispec.ImageIndexFile, err)
	}
	if err := t.ensureLayoutFile(ctx); err != nil {
		return err
	}
	_, err = t.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(t.loc.Bucket), Key: aws.String(t.loc.objectKey(ocispec.ImageIndexFile)),
		Body: bytes.NewReader(data), ContentLength: aws.Int64(int64(len(data))), ContentType: aws.String("application/json"),
	})
	if err != nil {
		return fmt.Errorf("vmrunner: put S3 %s: %w", ocispec.ImageIndexFile, err)
	}
	return nil
}

// Resolve implements content.Resolver. A digest reference reads the manifest
// blob and checks its digest. A tag reference reads index.json.
func (t *s3LayoutTarget) Resolve(ctx context.Context, reference string) (ocispec.Descriptor, error) {
	if reference == "" {
		return ocispec.Descriptor{}, errdef.ErrMissingReference
	}
	if d := digest.Digest(reference); d.Validate() == nil {
		return t.resolveDigest(ctx, d)
	}
	index, err := t.readIndex(ctx)
	if err != nil {
		return ocispec.Descriptor{}, err
	}
	for i := len(index.Manifests) - 1; i >= 0; i-- {
		m := index.Manifests[i]
		if m.Annotations[ocispec.AnnotationRefName] == reference {
			m.Annotations = nil
			return m, nil
		}
	}
	return ocispec.Descriptor{}, fmt.Errorf("tag %q in %s: %w", reference, t.loc.String(), errdef.ErrNotFound)
}

func (t *s3LayoutTarget) resolveDigest(ctx context.Context, d digest.Digest) (ocispec.Descriptor, error) {
	out, err := t.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(t.loc.Bucket), Key: aws.String(t.blobKey(d))})
	if err != nil {
		if isS3ImageNotFound(err) {
			return ocispec.Descriptor{}, fmt.Errorf("manifest %s in %s: %w", d, t.loc.String(), errdef.ErrNotFound)
		}
		return ocispec.Descriptor{}, fmt.Errorf("vmrunner: get S3 manifest %s: %w", d, err)
	}
	defer out.Body.Close()
	data, err := io.ReadAll(io.LimitReader(out.Body, maxManifestSize+1))
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("vmrunner: read S3 manifest %s: %w", d, err)
	}
	if int64(len(data)) > maxManifestSize {
		return ocispec.Descriptor{}, fmt.Errorf("vmrunner: S3 manifest %s is larger than %d bytes", d, maxManifestSize)
	}
	if got := d.Algorithm().FromBytes(data); got != d {
		return ocispec.Descriptor{}, fmt.Errorf("vmrunner: S3 manifest digest mismatch: got %s, expected %s", got, d)
	}
	var probe struct {
		MediaType    string `json:"mediaType"`
		ArtifactType string `json:"artifactType"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("vmrunner: decode S3 manifest %s: %w", d, err)
	}
	if probe.MediaType == "" {
		probe.MediaType = ocispec.MediaTypeImageManifest
	}
	return ocispec.Descriptor{MediaType: probe.MediaType, ArtifactType: probe.ArtifactType, Digest: d, Size: int64(len(data))}, nil
}

func isS3ImageNotFound(err error) bool {
	var noKey *s3types.NoSuchKey
	if errors.As(err, &noKey) {
		return true
	}
	var notFound *s3types.NotFound
	if errors.As(err, &notFound) {
		return true
	}
	var respErr *smithyhttp.ResponseError
	return errors.As(err, &respErr) && respErr.HTTPStatusCode() == http.StatusNotFound
}
