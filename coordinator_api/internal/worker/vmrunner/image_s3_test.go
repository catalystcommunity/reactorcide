package vmrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"oras.land/oras-go/v2"
)

// fakeS3 is an in-memory S3 with single PUT and multipart upload.
type fakeS3 struct {
	mu       sync.Mutex
	objects  map[string][]byte
	uploads  map[string]map[int32][]byte
	aborted  int
	nextID   int
	getCalls int
}

func newFakeS3() *fakeS3 {
	return &fakeS3{objects: map[string][]byte{}, uploads: map[string]map[int32][]byte{}}
}

func fakeKey(bucket, key *string) string { return aws.ToString(bucket) + "/" + aws.ToString(key) }

func (f *fakeS3) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls++
	data, ok := f.objects[fakeKey(in.Bucket, in.Key)]
	if !ok {
		return nil, &s3types.NoSuchKey{}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(append([]byte(nil), data...))), ContentLength: aws.Int64(int64(len(data)))}, nil
}

func (f *fakeS3) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.objects[fakeKey(in.Bucket, in.Key)]
	if !ok {
		return nil, &s3types.NotFound{}
	}
	return &s3.HeadObjectOutput{ContentLength: aws.Int64(int64(len(data)))}, nil
}

func (f *fakeS3) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	data, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[fakeKey(in.Bucket, in.Key)] = data
	return &s3.PutObjectOutput{}, nil
}

func (f *fakeS3) CreateMultipartUpload(_ context.Context, in *s3.CreateMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	id := fmt.Sprintf("upload-%d", f.nextID)
	f.uploads[id] = map[int32][]byte{}
	return &s3.CreateMultipartUploadOutput{UploadId: aws.String(id)}, nil
}

func (f *fakeS3) UploadPart(_ context.Context, in *s3.UploadPartInput, _ ...func(*s3.Options)) (*s3.UploadPartOutput, error) {
	data, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uploads[aws.ToString(in.UploadId)][aws.ToInt32(in.PartNumber)] = data
	return &s3.UploadPartOutput{ETag: aws.String(fmt.Sprintf("etag-%d", aws.ToInt32(in.PartNumber)))}, nil
}

func (f *fakeS3) CompleteMultipartUpload(_ context.Context, in *s3.CompleteMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	parts := f.uploads[aws.ToString(in.UploadId)]
	var numbers []int
	for n := range parts {
		numbers = append(numbers, int(n))
	}
	sort.Ints(numbers)
	var all []byte
	for _, n := range numbers {
		all = append(all, parts[int32(n)]...)
	}
	f.objects[fakeKey(in.Bucket, in.Key)] = all
	delete(f.uploads, aws.ToString(in.UploadId))
	return &s3.CompleteMultipartUploadOutput{}, nil
}

func (f *fakeS3) AbortMultipartUpload(_ context.Context, in *s3.AbortMultipartUploadInput, _ ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.uploads, aws.ToString(in.UploadId))
	f.aborted++
	return &s3.AbortMultipartUploadOutput{}, nil
}

func (f *fakeS3) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var keys []string
	for k := range f.objects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func newS3TestSource(t *testing.T, fake *fakeS3, opts ...OCIImageSourceOption) *OCIImageSource {
	t.Helper()
	source, err := NewOCIImageSource(t.TempDir(), opts...)
	require.NoError(t, err)
	source.s3Factory = func(_ context.Context, loc s3ImageLocation) (oras.ReadOnlyTarget, error) {
		return &s3LayoutTarget{client: fake, loc: loc}, nil
	}
	return source
}

func TestParseS3ImageRef(t *testing.T) {
	hex := strings.Repeat("ab", 32)
	tests := []struct {
		ref     string
		want    s3ImageLocation
		wantErr bool
	}{
		{ref: "s3://bucket/vm/windows-11:base", want: s3ImageLocation{Bucket: "bucket", Key: "vm/windows-11", Tag: "base"}},
		{ref: "s3://bucket/name", want: s3ImageLocation{Bucket: "bucket", Key: "name"}},
		{ref: "s3://bucket/a/b/name:v1@sha256:" + hex, want: s3ImageLocation{Bucket: "bucket", Key: "a/b/name", Tag: "v1", Digest: digest.Digest("sha256:" + hex)}},
		{ref: "s3://bucket/name@sha256:" + hex, want: s3ImageLocation{Bucket: "bucket", Key: "name", Digest: digest.Digest("sha256:" + hex)}},
		// A hash is a valid tag (decision I3).
		{ref: "s3://bucket/name:" + hex, want: s3ImageLocation{Bucket: "bucket", Key: "name", Tag: hex}},
		{ref: "s3://bucket/name:sha256-" + hex + "@sha256:" + hex, want: s3ImageLocation{Bucket: "bucket", Key: "name", Tag: "sha256-" + hex, Digest: digest.Digest("sha256:" + hex)}},
		// Outer space is removed; free-form tags can use '.', '_', '-', '+'.
		{ref: "  s3://bucket/name:release-2026.11_a+b  ", want: s3ImageLocation{Bucket: "bucket", Key: "name", Tag: "release-2026.11_a+b"}},
		// The shared parser (internal/imageref): one '@' only, and no
		// whitespace, '/', or '@' in a tag.
		{ref: "s3://bucket/name:release/2026@11", wantErr: true},
		{ref: "s3://bucket/name:a@b@sha256:" + hex, wantErr: true},
		{ref: "s3://bucket/name:a b", wantErr: true},
		{ref: "s3://bucket/name:a\tb", wantErr: true},
		{ref: "s3://bucket/name:" + strings.Repeat("t", 129), wantErr: true},
		{ref: "s3://bucket/name@sha512:" + hex + hex, wantErr: true},
		{ref: "s3://buck:et/name", wantErr: true},
		{ref: "s3://bucket", wantErr: true},
		{ref: "s3://bucket/", wantErr: true},
		{ref: "s3:///name", wantErr: true},
		{ref: "s3://bucket/name:", wantErr: true},
		{ref: "s3://bucket/a/../name", wantErr: true},
		{ref: "s3://bucket/name@sha256:abc", wantErr: true},
		{ref: "registry.example.com/name:tag", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.ref, func(t *testing.T) {
			got, err := parseS3ImageRef(test.ref)
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.want, got)
			reparsed, err := parseS3ImageRef(got.String())
			require.NoError(t, err)
			require.Equal(t, got, reparsed)
		})
	}
}

func TestS3PublishThenPullRoundTrip(t *testing.T) {
	ctx := context.Background()
	bundle := writeTestWindowsBundle(t)
	fake := newFakeS3()

	pinned, err := PublishVMBundle(ctx, bundle, "s3://images/team/vm/windows-11:base", PublishOptions{s3Client: fake})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(pinned, "s3://images/team/vm/windows-11:base@sha256:"), pinned)

	keys := fake.keys()
	require.Contains(t, keys, "images/team/vm/windows-11/oci-layout")
	require.Contains(t, keys, "images/team/vm/windows-11/index.json")
	loc, err := parseS3ImageRef(pinned)
	require.NoError(t, err)
	require.Contains(t, keys, "images/team/vm/windows-11/blobs/sha256/"+loc.Digest.Encoded())

	var index ocispec.Index
	require.NoError(t, json.Unmarshal(fake.objects["images/team/vm/windows-11/index.json"], &index))
	require.Len(t, index.Manifests, 1)
	require.Equal(t, "base", index.Manifests[0].Annotations[ocispec.AnnotationRefName])
	require.Equal(t, loc.Digest, index.Manifests[0].Digest)

	// The worker accepts only the pinned reference.
	worker := newS3TestSource(t, fake, WithRequireS3Digest())
	_, err = worker.Resolve(ctx, "s3://images/team/vm/windows-11:base")
	require.ErrorContains(t, err, "has no digest")
	path, err := worker.Resolve(ctx, pinned)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chmod(path, 0o700) })
	require.NoError(t, ValidateWindowsBundle(path))
	got, err := os.ReadFile(path + "/" + BundleWindowsDisk)
	require.NoError(t, err)
	want, err := os.ReadFile(bundle + "/" + BundleWindowsDisk)
	require.NoError(t, err)
	require.Equal(t, want, got)

	// A second resolve is a cache hit: no S3 read.
	calls := fake.getCalls
	again, err := worker.Resolve(ctx, pinned)
	require.NoError(t, err)
	require.Equal(t, path, again)
	require.Equal(t, calls, fake.getCalls)

	// The image tools can pull by tag.
	tool := newS3TestSource(t, fake)
	byTag, err := tool.Resolve(ctx, "s3://images/team/vm/windows-11:base")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chmod(byTag, 0o700) })
	require.NoError(t, ValidateWindowsBundle(byTag))
}

func TestS3TagThatLooksLikeAHash(t *testing.T) {
	ctx := context.Background()
	bundle := writeTestMacBundle(t)
	fake := newFakeS3()
	hashTag := strings.Repeat("0f", 32)

	first, err := PublishVMBundle(ctx, bundle, "s3://images/mac:"+hashTag, PublishOptions{s3Client: fake})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(first, "s3://images/mac:"+hashTag+"@sha256:"), first)

	// The hash tag resolves through index.json, not as a digest.
	target := &s3LayoutTarget{client: fake, loc: s3ImageLocation{Bucket: "images", Key: "mac"}}
	loc, err := parseS3ImageRef(first)
	require.NoError(t, err)
	desc, err := target.Resolve(ctx, hashTag)
	require.NoError(t, err)
	require.Equal(t, loc.Digest, desc.Digest)

	// A second publish under another tag keeps both tags.
	second, err := PublishVMBundle(ctx, bundle, "s3://images/mac:v2", PublishOptions{s3Client: fake})
	require.NoError(t, err)
	var index ocispec.Index
	require.NoError(t, json.Unmarshal(fake.objects["images/mac/index.json"], &index))
	require.Len(t, index.Manifests, 2)

	source := newS3TestSource(t, fake)
	path, err := source.Resolve(ctx, second)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chmod(path, 0o700) })
	require.NoError(t, ValidateMacBundle(path))
}

func TestS3PullRejectsDigestMismatch(t *testing.T) {
	ctx := context.Background()
	bundle := writeTestWindowsBundle(t)
	fake := newFakeS3()
	pinned, err := PublishVMBundle(ctx, bundle, "s3://images/win:base", PublishOptions{s3Client: fake})
	require.NoError(t, err)

	// Change one byte of the layer blob, with the same size.
	var manifest ocispec.Manifest
	loc, err := parseS3ImageRef(pinned)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(fake.objects["images/win/blobs/sha256/"+loc.Digest.Encoded()], &manifest))
	layerKey := "images/win/blobs/sha256/" + manifest.Layers[0].Digest.Encoded()
	tampered := append([]byte(nil), fake.objects[layerKey]...)
	tampered[len(tampered)/2] ^= 0xff
	fake.objects[layerKey] = tampered

	source := newS3TestSource(t, fake)
	_, err = source.Resolve(ctx, pinned)
	require.Error(t, err)
	_, statErr := os.Stat(source.cacheDir + "/blobs/sha256/" + manifest.Layers[0].Digest.Encoded())
	require.True(t, os.IsNotExist(statErr), "a blob that fails digest verification must not stay in the cache")

	// A pinned digest that the manifest does not have is refused.
	other := loc
	other.Digest = digest.FromString("not the manifest")
	_, err = newS3TestSource(t, fake).Resolve(ctx, other.String())
	require.Error(t, err)

	// A changed manifest no longer matches its pinned digest.
	manifestKey := "images/win/blobs/sha256/" + loc.Digest.Encoded()
	fake.objects[manifestKey] = append(fake.objects[manifestKey], ' ')
	_, err = newS3TestSource(t, fake).Resolve(ctx, pinned)
	require.Error(t, err)
}

func TestS3PushMultipartVerifiesDigest(t *testing.T) {
	ctx := context.Background()
	fake := newFakeS3()
	target := &s3LayoutTarget{client: fake, loc: s3ImageLocation{Bucket: "b", Key: "img"}, partSize: 1024}
	data := bytes.Repeat([]byte("reactorcide"), 500)
	desc := ocispec.Descriptor{MediaType: "application/octet-stream", Digest: digest.FromBytes(data), Size: int64(len(data))}

	require.NoError(t, target.Push(ctx, desc, bytes.NewReader(data)))
	require.Equal(t, data, fake.objects["b/img/blobs/sha256/"+desc.Digest.Encoded()])

	bad := append([]byte(nil), data...)
	bad[len(bad)-1] ^= 0xff
	badDesc := ocispec.Descriptor{MediaType: "application/octet-stream", Digest: digest.FromString("other"), Size: int64(len(bad))}
	require.ErrorContains(t, target.Push(ctx, badDesc, bytes.NewReader(bad)), "digest mismatch")
	_, ok := fake.objects["b/img/blobs/sha256/"+badDesc.Digest.Encoded()]
	require.False(t, ok, "a blob with a wrong digest must not be stored")
	require.Equal(t, 1, fake.aborted)

	short := ocispec.Descriptor{Digest: digest.FromString("x"), Size: 10}
	require.Error(t, target.Push(ctx, short, bytes.NewReader([]byte("x"))))
}
