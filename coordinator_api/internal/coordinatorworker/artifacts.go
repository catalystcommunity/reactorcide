package coordinatorworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/artifacts"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/testresults"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient/csilapi"
)

// WP15: the worker side of job artifacts and test quarantine
// (docs/artifacts-and-tests.md).
//
//   - Before the job starts, the worker makes /job/artifacts
//     (REACTORCIDE_ARTIFACTS_DIR) and writes /job/quarantined-tests.json
//     (REACTORCIDE_QUARANTINED_TESTS_FILE).
//   - After the job ends, and before the result, the worker uploads each
//     regular file under the artifacts directory to the coordinator in
//     chunks. It never follows a symbolic link: a job controls its workspace,
//     and a link could point at a file of the worker host.
//   - A test report (the name convention, or `test_reports` in the workflow
//     output) is masked with the job secrets before the upload, like the job
//     logs. Other files are uploaded as the job wrote them.

const (
	// ArtifactsDirEnv is the environment variable with the artifacts
	// directory of the job.
	ArtifactsDirEnv = "REACTORCIDE_ARTIFACTS_DIR"
	// QuarantinedTestsFileEnv is the environment variable with the path of
	// the quarantined tests file.
	QuarantinedTestsFileEnv = "REACTORCIDE_QUARANTINED_TESTS_FILE"

	artifactsContainerDir  = "/job/artifacts"
	quarantinedTestsName   = "quarantined-tests.json"
	collectedArtifactsName = ".reactorcide-collected-artifacts"
	artifactUploadTimeout  = 15 * time.Minute
)

// artifactUploader is the upload call of *workerclient.Client. A client
// without it (an older test fake) uploads nothing.
type artifactUploader interface {
	UploadArtifact(ctx context.Context, req csilapi.UploadArtifactRequest) (csilapi.UploadArtifactResponse, error)
}

// prepareLeaseArtifacts gives the job its artifacts directory and its
// quarantined tests file, when the lease has them and the runner can give
// workspace files to the job.
func prepareLeaseArtifacts(lease csilapi.Lease, runner worker.JobRunner, workspaceDir string, config *worker.JobConfig, uid, gid int) error {
	if lease.Artifacts == nil && len(lease.QuarantinedTests) == 0 {
		return nil
	}
	if marker, ok := runner.(worker.NoHostWorkspace); ok && marker.NoHostWorkspace() {
		return nil
	}
	if config.Env == nil {
		config.Env = map[string]string{}
	}
	if lease.Artifacts != nil {
		dir := filepath.Join(workspaceDir, "artifacts")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create the artifacts directory: %w", err)
		}
		if err := makeWritableFor(dir, uid, gid); err != nil {
			return fmt.Errorf("prepare the artifacts directory: %w", err)
		}
		config.Env[ArtifactsDirEnv] = artifactsContainerDir
	}
	file := testresults.QuarantineFile{Version: 1, Tests: []testresults.QuarantineFileRow{}}
	for _, test := range lease.QuarantinedTests {
		file.Tests = append(file.Tests, testresults.QuarantineFileRow{TestID: test.TestId, Suite: test.Suite,
			Classname: test.Classname, Name: test.Name, ExpiresAt: test.ExpiresAt})
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(workspaceDir, quarantinedTestsName), data, 0o644); err != nil {
		return fmt.Errorf("write the quarantined tests file: %w", err)
	}
	if config.JobFiles == nil {
		config.JobFiles = map[string][]byte{}
	}
	config.JobFiles[quarantinedTestsName] = data
	config.Env[QuarantinedTestsFileEnv] = "/job/" + quarantinedTestsName
	return nil
}

// uploadLeaseArtifacts uploads the artifacts of a finished job. It is best
// effort: an error is logged and never changes the job result.
func uploadLeaseArtifacts(c client, runner worker.JobRunner, runnerID string, lease csilapi.Lease, workspaceDir, workflowOutput string, masker *secrets.Masker) {
	if lease.Artifacts == nil {
		return
	}
	uploader, ok := c.(artifactUploader)
	if !ok {
		return
	}
	if marker, ok := runner.(worker.NoHostWorkspace); ok && marker.NoHostWorkspace() {
		return
	}
	logger := logging.Log.WithFields(map[string]interface{}{"lease_id": lease.LeaseId, "job_id": lease.JobId})
	ctx, cancel := context.WithTimeout(context.Background(), artifactUploadTimeout)
	defer cancel()
	root := filepath.Join(workspaceDir, "artifacts")
	if collector, ok := runner.(worker.ArtifactCollector); ok {
		root = filepath.Join(workspaceDir, collectedArtifactsName)
		if err := collector.CollectArtifacts(ctx, runnerID, root, lease.Artifacts.MaxJobBytes); err != nil {
			logger.WithError(err).Warn("failed to collect the job artifacts; uploading the files collected before the error")
		}
	}
	files, skipped, err := listArtifactFiles(root, lease.Artifacts)
	if err != nil {
		logger.WithError(err).Warn("failed to list the job artifacts")
		return
	}
	for _, reason := range skipped {
		logger.WithField("reason", masker.MaskString(reason)).Warn("artifact not uploaded")
	}
	declared := declaredReports(workflowOutput)
	for _, file := range files {
		kind := artifacts.KindFor(file.path, declared)
		if err := uploadArtifactFile(ctx, uploader, lease, root, file, kind, masker); err != nil {
			logger.WithError(errors.New(masker.MaskString(err.Error()))).WithField("path", masker.MaskString(file.path)).Warn("failed to upload an artifact")
			if ctx.Err() != nil {
				return
			}
		}
	}
}

type artifactFile struct {
	path string
	size int64
}

// listArtifactFiles lists the regular files under root, in path order,
// within the limits of the lease. A symbolic link, a device, a socket, and
// a path that is not a valid artifact path are skipped.
func listArtifactFiles(root string, limits *csilapi.ArtifactLease) ([]artifactFile, []string, error) {
	info, err := os.Lstat(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	if !info.IsDir() {
		return nil, []string{"the artifacts directory is not a directory (a symbolic link is not followed)"}, nil
	}
	files := []artifactFile{}
	skipped := []string{}
	err = filepath.WalkDir(root, func(full string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			skipped = append(skipped, fmt.Sprintf("%s: %v", full, walkErr))
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if full == root || entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, full)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if !entry.Type().IsRegular() {
			skipped = append(skipped, rel+": not a regular file")
			return nil
		}
		if err := artifacts.ValidatePath(rel); err != nil {
			skipped = append(skipped, err.Error())
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		files = append(files, artifactFile{path: rel, size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, skipped, err
	}
	sort.Slice(files, func(a, b int) bool { return files[a].path < files[b].path })
	out := []artifactFile{}
	var total int64
	for _, file := range files {
		switch {
		case limits.MaxFiles > 0 && int64(len(out)) >= limits.MaxFiles:
			skipped = append(skipped, fmt.Sprintf("%s: a job has at most %d artifacts", file.path, limits.MaxFiles))
		case limits.MaxFileBytes > 0 && file.size > limits.MaxFileBytes:
			skipped = append(skipped, fmt.Sprintf("%s: larger than the file limit of %d bytes", file.path, limits.MaxFileBytes))
		case limits.MaxJobBytes > 0 && total+file.size > limits.MaxJobBytes:
			skipped = append(skipped, fmt.Sprintf("%s: the job limit of %d bytes is reached", file.path, limits.MaxJobBytes))
		default:
			total += file.size
			out = append(out, file)
		}
	}
	return out, skipped, nil
}

// openNoFollow opens a regular file and refuses a symbolic link, also one
// that a job made after the listing (artifacts_open_*.go).
func openNoFollow(full string) (*os.File, error) {
	file, err := openFileNoFollow(full)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, errors.New("not a regular file")
	}
	return file, nil
}

func uploadArtifactFile(ctx context.Context, uploader artifactUploader, lease csilapi.Lease, root string, file artifactFile, kind string, masker *secrets.Masker) error {
	handle, err := openNoFollow(filepath.Join(root, filepath.FromSlash(file.path)))
	if err != nil {
		return err
	}
	defer handle.Close()
	chunkSize := int(lease.Artifacts.ChunkBytes)
	if chunkSize <= 0 || chunkSize > artifacts.MaxChunkBytes {
		chunkSize = artifacts.DefaultLimits.ChunkBytes
	}
	var source io.Reader = io.LimitReader(handle, file.size)
	if kind == models.ArtifactKindTestReport {
		// A test report goes to the PR comment and the UI: mask it like a
		// log. The coordinator limits a report to its file limit.
		data, err := io.ReadAll(io.LimitReader(handle, lease.Artifacts.MaxFileBytes))
		if err != nil {
			return err
		}
		source = strings.NewReader(masker.MaskString(string(data)))
	}
	kindValue := kind
	buf := make([]byte, chunkSize)
	var offset int64
	for {
		n, readErr := io.ReadFull(source, buf)
		final := readErr == io.EOF || readErr == io.ErrUnexpectedEOF
		if readErr != nil && !final {
			return readErr
		}
		req := csilapi.UploadArtifactRequest{LeaseId: lease.LeaseId, Path: file.path, Offset: offset,
			Data: append([]byte(nil), buf[:n]...), Final: final, Kind: &kindValue}
		if err := sendArtifactChunk(ctx, uploader, req); err != nil {
			return err
		}
		offset += int64(n)
		if final {
			return nil
		}
	}
}

// sendArtifactChunk sends one chunk, with a few retries for a transport
// error. A final answer of the coordinator (too large, bad path) is not
// retried.
func sendArtifactChunk(ctx context.Context, uploader artifactUploader, req csilapi.UploadArtifactRequest) error {
	backoff := 500 * time.Millisecond
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
		}
		_, err = uploader.UploadArtifact(ctx, req)
		if err == nil || !isRetryable(err) {
			return err
		}
	}
	return err
}

type outputReports struct {
	TestReports []string `json:"test_reports"`
}

func declaredReports(workflowOutput string) map[string]bool {
	out := map[string]bool{}
	if workflowOutput == "" {
		return out
	}
	var doc outputReports
	if json.Unmarshal([]byte(workflowOutput), &doc) != nil {
		return out
	}
	for _, p := range doc.TestReports {
		out[strings.TrimPrefix(strings.TrimSpace(p), "./")] = true
	}
	return out
}
