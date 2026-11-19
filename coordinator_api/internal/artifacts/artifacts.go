// Package artifacts stores the files that jobs publish (WP15,
// docs/artifacts-and-tests.md).
//
// A job writes files under its artifacts directory (/job/artifacts). After
// the job ends, the worker uploads each file to the coordinator in chunks
// (ReactorcideWorker upload-artifact). The worker never connects to the
// object store (DESIGN.md, trust boundaries). The coordinator:
//
//   - checks the path and the limits (per file, per job, number of files),
//   - writes each chunk to the object store under
//     artifacts/<artifact namespace>/<job id>/<artifact id>/<chunk>,
//   - computes the size and the SHA-256 digest itself from the bytes that it
//     stored. The SHA-256 state is kept in the database between chunks, so
//     any coordinator replica can take the next chunk.
//
// The worker sends the chunks of one file in order, one at a time. A retry
// of a chunk sends the same bytes again; the coordinator accepts a repeated
// chunk as a no-op.
package artifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/objects"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/testreports"
)

// ObjectRoot is the object store prefix of all artifacts.
const ObjectRoot = "artifacts"

// DefaultNamespace is the namespace of a job with no artifact namespace.
const DefaultNamespace = "standard"

// Limits bound the artifacts of one job.
type Limits struct {
	Enabled      bool
	MaxFileBytes int64
	MaxJobBytes  int64
	MaxFiles     int
	// ChunkBytes is the chunk size that the worker uses. The coordinator
	// refuses a larger chunk.
	ChunkBytes int
}

// DefaultLimits are the limits when no setting is given.
var DefaultLimits = Limits{Enabled: true, MaxFileBytes: 256 << 20, MaxJobBytes: 1 << 30, MaxFiles: 1000, ChunkBytes: 4 << 20}

// MaxChunkBytes is the largest chunk that the coordinator accepts. It stays
// well below the CSIL-RPC envelope limit (16 MiB).
const MaxChunkBytes = 8 << 20

// LimitsFromEnv reads the operator settings:
//
//	REACTORCIDE_ARTIFACTS_ENABLED        true (default) or false
//	REACTORCIDE_ARTIFACT_MAX_FILE_BYTES  per file (default 256 MiB)
//	REACTORCIDE_ARTIFACT_MAX_JOB_BYTES   per job (default 1 GiB)
//	REACTORCIDE_ARTIFACT_MAX_FILES       per job (default 1000)
//	REACTORCIDE_ARTIFACT_CHUNK_BYTES     chunk size (default 4 MiB, max 8 MiB)
func LimitsFromEnv() Limits {
	limits := DefaultLimits
	if value := strings.TrimSpace(os.Getenv("REACTORCIDE_ARTIFACTS_ENABLED")); value != "" {
		limits.Enabled = value != "false" && value != "0"
	}
	if n, ok := envInt("REACTORCIDE_ARTIFACT_MAX_FILE_BYTES"); ok {
		limits.MaxFileBytes = n
	}
	if n, ok := envInt("REACTORCIDE_ARTIFACT_MAX_JOB_BYTES"); ok {
		limits.MaxJobBytes = n
	}
	if n, ok := envInt("REACTORCIDE_ARTIFACT_MAX_FILES"); ok {
		limits.MaxFiles = int(n)
	}
	if n, ok := envInt("REACTORCIDE_ARTIFACT_CHUNK_BYTES"); ok && n <= MaxChunkBytes {
		limits.ChunkBytes = int(n)
	}
	return limits
}

func envInt(name string) (int64, bool) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// Error is a refused or failed operation. Code is invalid_argument,
// too_large, conflict, not_found, unavailable, or internal.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func newError(code, format string, args ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// ErrorCode returns the code of an Error, or "internal".
func ErrorCode(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return "internal"
}

// MaxPathBytes is the longest artifact path.
const MaxPathBytes = 512

// ValidatePath checks a path relative to the artifacts directory. It must be
// a clean relative path with "/" separators, with no "." or ".." part, no
// control character, and at most MaxPathBytes bytes.
func ValidatePath(value string) error {
	if value == "" || len(value) > MaxPathBytes {
		return newError("invalid_argument", "an artifact path must have 1 to %d bytes", MaxPathBytes)
	}
	if !utf8.ValidString(value) {
		return newError("invalid_argument", "an artifact path must be UTF-8")
	}
	if strings.HasPrefix(value, "/") || strings.Contains(value, `\`) || strings.HasSuffix(value, "/") {
		return newError("invalid_argument", "artifact path %q must be relative and use / separators", value)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return newError("invalid_argument", "artifact path %q has a control character", value)
		}
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." || len(part) > 255 {
			return newError("invalid_argument", "artifact path %q has an empty, '.', '..' or too long part", value)
		}
	}
	if path.Clean(value) != value {
		return newError("invalid_argument", "artifact path %q is not clean", value)
	}
	return nil
}

// ValidateNamespace makes a namespace safe for an object key. An empty value
// is DefaultNamespace.
func ValidateNamespace(namespace string) string {
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return DefaultNamespace
	}
	var b strings.Builder
	for _, r := range namespace {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), ".")
	if out == "" {
		return DefaultNamespace
	}
	if len(out) > 63 {
		out = out[:63]
	}
	return out
}

// JobPrefix is the object store prefix of the artifacts of one job.
func JobPrefix(namespace, jobID string) string {
	return ObjectRoot + "/" + ValidateNamespace(namespace) + "/" + jobID
}

// ChunkKey is the object key of one chunk.
func ChunkKey(prefix, artifactID string, index int) string {
	return fmt.Sprintf("%s/%s/%06d", prefix, artifactID, index)
}

// MediaType returns the media type of a path from its extension.
func MediaType(p string) string {
	ext := strings.ToLower(path.Ext(p))
	switch ext {
	case ".xml":
		return "application/xml"
	case ".json":
		return "application/json"
	case ".txt", ".log":
		return "text/plain; charset=utf-8"
	case ".html", ".htm":
		// Served as a download only (Content-Disposition: attachment).
		return "text/html; charset=utf-8"
	}
	if value := mime.TypeByExtension(ext); value != "" {
		return value
	}
	return "application/octet-stream"
}

// KindFor returns the kind of an uploaded path: test_report when the path
// follows the report convention or is declared, otherwise file.
func KindFor(p string, declared map[string]bool) string {
	if declared[p] || testreports.LooksLikeReport(p) {
		return models.ArtifactKindTestReport
	}
	return models.ArtifactKindFile
}

// Service runs the artifact operations for the worker protocol, REST, and
// the UI.
type Service struct {
	Store   store.ArtifactStore
	Objects objects.ObjectStore
	Limits  Limits
	Now     func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// ChunkRequest is one chunk of one file.
type ChunkRequest struct {
	Path      string
	Offset    int64
	Data      []byte
	Final     bool
	MediaType string
	// Kind is file or test_report. Empty uses KindFor with no declarations.
	Kind string
}

// AppendChunk stores one chunk of an artifact of job. The caller has
// checked that the worker holds the lease of the job.
func (s *Service) AppendChunk(ctx context.Context, job *models.Job, namespace string, req ChunkRequest) (*models.JobArtifact, error) {
	if s == nil || s.Store == nil || s.Objects == nil {
		return nil, newError("unavailable", "artifact storage is not configured on this coordinator")
	}
	if !s.Limits.Enabled {
		return nil, newError("unavailable", "artifact storage is turned off on this coordinator")
	}
	if err := ValidatePath(req.Path); err != nil {
		return nil, err
	}
	if req.Offset < 0 {
		return nil, newError("invalid_argument", "the offset must not be negative")
	}
	if len(req.Data) > MaxChunkBytes || (s.Limits.ChunkBytes > 0 && len(req.Data) > s.Limits.ChunkBytes) {
		return nil, newError("invalid_argument", "a chunk has at most %d bytes", s.chunkLimit())
	}
	kind := req.Kind
	if kind != models.ArtifactKindFile && kind != models.ArtifactKindTestReport {
		kind = KindFor(req.Path, nil)
	}
	mediaType := strings.TrimSpace(req.MediaType)
	if mediaType == "" || len(mediaType) > 200 {
		mediaType = MediaType(req.Path)
	}
	if kind == models.ArtifactKindTestReport {
		mediaType = "application/xml"
	}

	artifact, err := s.findOrCreate(ctx, job, namespace, req.Path, kind, mediaType, req.Offset)
	if err != nil {
		return nil, err
	}
	switch artifact.Status {
	case models.ArtifactStatusComplete:
		if req.Final && req.Offset+int64(len(req.Data)) == artifact.SizeBytes {
			return artifact, nil
		}
		return nil, newError("conflict", "artifact %q is already complete", req.Path)
	case models.ArtifactStatusFailed:
		return nil, newError("conflict", "artifact %q failed: %s", req.Path, artifact.StatusReason)
	}

	end := req.Offset + int64(len(req.Data))
	switch {
	case req.Offset > artifact.SizeBytes:
		return nil, newError("invalid_argument", "artifact %q: chunk offset %d is after the stored size %d", req.Path, req.Offset, artifact.SizeBytes)
	case req.Offset < artifact.SizeBytes:
		// A retry of the last chunk: the bytes are stored already.
		if end != artifact.SizeBytes {
			return nil, newError("invalid_argument", "artifact %q: chunk offset %d is before the stored size %d", req.Path, req.Offset, artifact.SizeBytes)
		}
		if req.Final {
			return s.complete(ctx, artifact)
		}
		return artifact, nil
	}

	if len(req.Data) > 0 {
		if s.Limits.MaxFileBytes > 0 && end > s.Limits.MaxFileBytes {
			reason := fmt.Sprintf("the file is larger than the limit of %d bytes", s.Limits.MaxFileBytes)
			_ = s.Store.FailJobArtifact(ctx, artifact.ArtifactID, reason)
			return nil, newError("too_large", "artifact %q: %s", req.Path, reason)
		}
		if s.Limits.MaxJobBytes > 0 {
			_, total, err := s.Store.JobArtifactTotals(ctx, job.JobID)
			if err != nil {
				return nil, newError("internal", "could not read the artifact totals")
			}
			if total+int64(len(req.Data)) > s.Limits.MaxJobBytes {
				reason := fmt.Sprintf("the artifacts of the job are larger than the limit of %d bytes", s.Limits.MaxJobBytes)
				_ = s.Store.FailJobArtifact(ctx, artifact.ArtifactID, reason)
				return nil, newError("too_large", "artifact %q: %s", req.Path, reason)
			}
		}
		state, err := restoreHash(artifact.HashState)
		if err != nil {
			return nil, newError("internal", "artifact %q: the digest state is not valid", req.Path)
		}
		state.Write(req.Data)
		next, err := state.(encoding.BinaryMarshaler).MarshalBinary()
		if err != nil {
			return nil, newError("internal", "artifact %q: could not save the digest state", req.Path)
		}
		key := ChunkKey(artifact.StoragePrefix, artifact.ArtifactID, artifact.ChunkCount)
		if err := s.Objects.Put(ctx, key, bytes.NewReader(req.Data), "application/octet-stream"); err != nil {
			return nil, newError("unavailable", "artifact %q: could not store the chunk", req.Path)
		}
		// The check above reads the totals before the chunk is stored; it
		// stops most uploads early. The store checks the limit again in the
		// same atomic step as the append, so two concurrent uploads of one
		// job cannot both pass it.
		ok, err := s.Store.AppendJobArtifactChunk(ctx, artifact.ArtifactID, artifact.SizeBytes, end, next, s.Limits.MaxJobBytes)
		if errors.Is(err, store.ErrArtifactJobLimit) {
			reason := fmt.Sprintf("the artifacts of the job are larger than the limit of %d bytes", s.Limits.MaxJobBytes)
			_ = s.Store.FailJobArtifact(ctx, artifact.ArtifactID, reason)
			return nil, newError("too_large", "artifact %q: %s", req.Path, reason)
		}
		if err != nil {
			return nil, newError("unavailable", "artifact %q: could not record the chunk", req.Path)
		}
		if !ok {
			return nil, newError("conflict", "artifact %q changed during the upload; send the chunk again", req.Path)
		}
		artifact.SizeBytes = end
		artifact.ChunkCount++
		artifact.HashState = next
	}
	if req.Final {
		return s.complete(ctx, artifact)
	}
	return artifact, nil
}

func (s *Service) chunkLimit() int {
	if s.Limits.ChunkBytes > 0 && s.Limits.ChunkBytes < MaxChunkBytes {
		return s.Limits.ChunkBytes
	}
	return MaxChunkBytes
}

func (s *Service) findOrCreate(ctx context.Context, job *models.Job, namespace, p, kind, mediaType string, offset int64) (*models.JobArtifact, error) {
	prefix := JobPrefix(namespace, job.JobID)
	candidate := &models.JobArtifact{JobID: job.JobID, Name: p, Path: p, Kind: kind, MediaType: mediaType,
		Status: models.ArtifactStatusUploading, ArtifactNamespace: ValidateNamespace(namespace), StoragePrefix: prefix}
	if offset == 0 {
		count, _, err := s.Store.JobArtifactTotals(ctx, job.JobID)
		if err != nil {
			return nil, newError("internal", "could not read the artifact totals")
		}
		if s.Limits.MaxFiles > 0 && count >= s.Limits.MaxFiles {
			// A retry of the first chunk of an existing file is not a new
			// file: look for it before the refusal.
			existing, err := s.find(ctx, job.JobID, kind, p)
			if err == nil {
				return existing, nil
			}
			return nil, newError("too_large", "a job has at most %d artifacts", s.Limits.MaxFiles)
		}
		record, created, err := s.Store.CreateJobArtifact(ctx, candidate)
		if err != nil {
			return nil, newError("internal", "could not record the artifact")
		}
		if created {
			_ = s.Store.SetJobArtifactsObjectKey(ctx, job.JobID, prefix)
		}
		return record, nil
	}
	existing, err := s.find(ctx, job.JobID, kind, p)
	if err != nil {
		return nil, newError("invalid_argument", "artifact %q: the first chunk must have offset 0", p)
	}
	return existing, nil
}

func (s *Service) find(ctx context.Context, jobID, kind, p string) (*models.JobArtifact, error) {
	list, err := s.Store.ListJobArtifacts(ctx, jobID)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Kind == kind && list[i].Path == p {
			return &list[i], nil
		}
	}
	return nil, store.ErrNotFound
}

func (s *Service) complete(ctx context.Context, artifact *models.JobArtifact) (*models.JobArtifact, error) {
	state, err := restoreHash(artifact.HashState)
	if err != nil {
		return nil, newError("internal", "artifact %q: the digest state is not valid", artifact.Path)
	}
	digest := hex.EncodeToString(state.Sum(nil))
	if err := s.Store.CompleteJobArtifact(ctx, artifact.ArtifactID, digest, artifact.SizeBytes); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, newError("conflict", "artifact %q changed during the upload", artifact.Path)
		}
		return nil, newError("unavailable", "artifact %q: could not complete the upload", artifact.Path)
	}
	now := s.now()
	artifact.Status = models.ArtifactStatusComplete
	artifact.SHA256 = digest
	artifact.HashState = nil
	artifact.CompletedAt = &now
	return artifact, nil
}

type hashState interface {
	io.Writer
	Sum([]byte) []byte
}

func restoreHash(state []byte) (hashState, error) {
	h := sha256.New()
	if len(state) == 0 {
		return h, nil
	}
	if err := h.(encoding.BinaryUnmarshaler).UnmarshalBinary(state); err != nil {
		return nil, err
	}
	return h, nil
}

// PutComplete stores one complete artifact from memory (the provenance
// envelope). It replaces nothing: a second call for the same path returns
// the first record.
func (s *Service) PutComplete(ctx context.Context, job *models.Job, namespace, kind, p, mediaType string, data []byte) (*models.JobArtifact, error) {
	if s == nil || s.Store == nil || s.Objects == nil {
		return nil, newError("unavailable", "artifact storage is not configured on this coordinator")
	}
	prefix := JobPrefix(namespace, job.JobID)
	candidate := &models.JobArtifact{JobID: job.JobID, Name: p, Path: p, Kind: kind, MediaType: mediaType,
		Status: models.ArtifactStatusUploading, ArtifactNamespace: ValidateNamespace(namespace), StoragePrefix: prefix}
	record, created, err := s.Store.CreateJobArtifact(ctx, candidate)
	if err != nil {
		return nil, newError("internal", "could not record the artifact")
	}
	if !created {
		return record, nil
	}
	_ = s.Store.SetJobArtifactsObjectKey(ctx, job.JobID, prefix)
	state := sha256.New()
	state.Write(data)
	next, _ := state.(encoding.BinaryMarshaler).MarshalBinary()
	if err := s.Objects.Put(ctx, ChunkKey(prefix, record.ArtifactID, 0), bytes.NewReader(data), "application/octet-stream"); err != nil {
		_ = s.Store.FailJobArtifact(ctx, record.ArtifactID, "could not store the artifact")
		return nil, newError("unavailable", "could not store the artifact")
	}
	// The provenance record is not in the job byte limit.
	if ok, err := s.Store.AppendJobArtifactChunk(ctx, record.ArtifactID, 0, int64(len(data)), next, 0); err != nil || !ok {
		return nil, newError("unavailable", "could not record the artifact")
	}
	record.SizeBytes = int64(len(data))
	record.ChunkCount = 1
	record.HashState = next
	return s.complete(ctx, record)
}

// Open returns a reader of a complete artifact. It reads the chunks in
// order.
func (s *Service) Open(ctx context.Context, artifact *models.JobArtifact) (io.ReadCloser, error) {
	if s == nil || s.Objects == nil {
		return nil, newError("unavailable", "artifact storage is not configured on this coordinator")
	}
	if artifact.Status != models.ArtifactStatusComplete {
		return nil, newError("conflict", "artifact %q is not complete (%s)", artifact.Path, artifact.Status)
	}
	return &chunkReader{ctx: ctx, objects: s.Objects, artifact: artifact}, nil
}

// ReadRange reads at most length bytes of a complete artifact from offset.
func (s *Service) ReadRange(ctx context.Context, artifact *models.JobArtifact, offset, length int64) ([]byte, error) {
	if offset < 0 || length < 0 {
		return nil, newError("invalid_argument", "the offset and the length must not be negative")
	}
	reader, err := s.Open(ctx, artifact)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	if offset > 0 {
		if _, err := io.CopyN(io.Discard, reader, offset); err != nil {
			if errors.Is(err, io.EOF) {
				return []byte{}, nil
			}
			return nil, newError("unavailable", "could not read the artifact")
		}
	}
	data, err := io.ReadAll(io.LimitReader(reader, length))
	if err != nil {
		return nil, newError("unavailable", "could not read the artifact")
	}
	return data, nil
}

type chunkReader struct {
	ctx      context.Context
	objects  objects.ObjectStore
	artifact *models.JobArtifact
	index    int
	current  io.ReadCloser
}

func (r *chunkReader) Read(p []byte) (int, error) {
	for {
		if r.current == nil {
			if r.index >= r.artifact.ChunkCount {
				return 0, io.EOF
			}
			obj, err := r.objects.Get(r.ctx, ChunkKey(r.artifact.StoragePrefix, r.artifact.ArtifactID, r.index))
			if err != nil {
				return 0, fmt.Errorf("read artifact chunk %d: %w", r.index, err)
			}
			r.current = obj
			r.index++
		}
		n, err := r.current.Read(p)
		if err == io.EOF {
			r.current.Close()
			r.current = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (r *chunkReader) Close() error {
	if r.current != nil {
		return r.current.Close()
	}
	return nil
}

// Finalize closes the artifacts of a finished job: an upload that did not
// complete fails, and a declared test report gets the kind test_report.
func (s *Service) Finalize(ctx context.Context, jobID string, declaredReports []string) ([]models.JobArtifact, error) {
	if s == nil || s.Store == nil {
		return nil, nil
	}
	if _, err := s.Store.FailUploadingJobArtifacts(ctx, jobID, "the job ended before the upload completed"); err != nil {
		return nil, err
	}
	list, err := s.Store.ListJobArtifacts(ctx, jobID)
	if err != nil {
		return nil, err
	}
	declared := map[string]bool{}
	for _, p := range declaredReports {
		declared[strings.TrimPrefix(strings.TrimSpace(p), "./")] = true
	}
	for i := range list {
		if list[i].Kind == models.ArtifactKindFile && declared[list[i].Path] {
			if err := s.Store.SetJobArtifactKind(ctx, list[i].ArtifactID, models.ArtifactKindTestReport, "application/xml"); err == nil {
				list[i].Kind = models.ArtifactKindTestReport
				list[i].MediaType = "application/xml"
			}
		}
	}
	return list, nil
}

// DeleteJob removes the stored bytes and the records of the artifacts of a
// job.
func (s *Service) DeleteJob(ctx context.Context, jobID string) error {
	if s == nil || s.Objects == nil {
		return nil
	}
	if s.Store != nil {
		list, err := s.Store.ListJobArtifacts(ctx, jobID)
		if err != nil {
			return err
		}
		for _, artifact := range list {
			for i := 0; i < artifact.ChunkCount; i++ {
				if err := s.Objects.Delete(ctx, ChunkKey(artifact.StoragePrefix, artifact.ArtifactID, i)); err != nil && !errors.Is(err, objects.ErrNotFound) {
					return err
				}
			}
		}
		return s.Store.DeleteJobArtifactRecords(ctx, jobID)
	}
	return nil
}

// PruneBefore removes the artifacts of terminal jobs that completed before
// the cutoff. It follows the telemetry retention
// (REACTORCIDE_TELEMETRY_RETENTION_DAYS). A lookup error keeps the data.
func (s *Service) PruneBefore(ctx context.Context, cutoff time.Time, getJob func(context.Context, string) (*models.Job, error)) (int, error) {
	if s == nil || s.Objects == nil {
		return 0, nil
	}
	infos, err := s.Objects.List(ctx, ObjectRoot+"/")
	if err != nil {
		return 0, err
	}
	keysByJob := map[string][]string{}
	for _, info := range infos {
		parts := strings.SplitN(strings.TrimPrefix(info.Key, ObjectRoot+"/"), "/", 3)
		if len(parts) < 3 || parts[1] == "" {
			continue
		}
		keysByJob[parts[1]] = append(keysByJob[parts[1]], info.Key)
	}
	deleted := 0
	for jobID, keys := range keysByJob {
		job, err := getJob(ctx, jobID)
		if err != nil || job == nil || job.CompletedAt == nil || !job.CompletedAt.Before(cutoff) {
			continue
		}
		for _, key := range keys {
			if err := s.Objects.Delete(ctx, key); err != nil && !errors.Is(err, objects.ErrNotFound) {
				return deleted, err
			}
		}
		if s.Store != nil {
			if err := s.Store.DeleteJobArtifactRecords(ctx, jobID); err != nil {
				return deleted, err
			}
		}
		deleted++
	}
	return deleted, nil
}
