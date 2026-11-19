package worker

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WP15: job artifacts and job files on Kubernetes
// (docs/artifacts-and-tests.md). The job workspace is an emptyDir that the
// worker host cannot read, so the worker reads /job/artifacts through the
// workflow output reader container (busybox tar), and writes job files in
// the prepare-workspace init container. A job that is not in a workflow
// gets the reader container too when it has artifacts.

// kubernetesArtifactsDirEnv is the job environment value that the worker
// sets when the lease has artifacts (coordinatorworker.ArtifactsDirEnv).
const kubernetesArtifactsDirEnv = "REACTORCIDE_ARTIFACTS_DIR"

// maxKubernetesJobFilesBytes bounds the job files of one pod. They travel as
// environment values of the init container, in the pod spec.
const maxKubernetesJobFilesBytes = 256 << 10

var jobFileNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// addKubernetesJobFiles makes the prepare-workspace init container write
// the job files under /job. A file with a bad name, or files larger than
// maxKubernetesJobFilesBytes, are left out (the job sees no file).
func addKubernetesJobFiles(container *corev1.Container, files map[string][]byte) []string {
	if container == nil || len(files) == 0 || len(container.Command) != 3 {
		return nil
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	skipped := []string{}
	total := 0
	script := container.Command[2]
	for i, name := range names {
		data := files[name]
		if !jobFileNamePattern.MatchString(name) || strings.HasPrefix(name, ".") || total+len(data) > maxKubernetesJobFilesBytes {
			skipped = append(skipped, name)
			continue
		}
		total += len(data)
		envName := fmt.Sprintf("REACTORCIDE_JOB_FILE_%d", i)
		container.Env = append(container.Env, corev1.EnvVar{Name: envName, Value: string(data)})
		script += fmt.Sprintf(`; printf '%%s' "$%s" > /job/%s; chmod 0644 /job/%s`, envName, name, name)
	}
	container.Command[2] = script
	return skipped
}

// kubernetesArtifactsCommand writes a tar stream of /job/artifacts to
// stdout. A symbolic link in place of the directory is refused. busybox
// tar stores a symbolic link inside the directory as a link, not as the
// file that it points to; the worker skips links.
var kubernetesArtifactsCommand = []string{"sh", "-c", `if [ -d /job/artifacts ] && [ ! -L /job/artifacts ]; then cd /job/artifacts && tar -cf - .; fi`}

// CollectArtifacts copies /job/artifacts of the job pod into destDir.
func (kr *KubernetesRunner) CollectArtifacts(ctx context.Context, jobName, destDir string, maxBytes int64) error {
	pods, err := kr.clientset.CoreV1().Pods(kr.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("reactorcide.io/job-name=%s", jobName),
	})
	if err != nil {
		return fmt.Errorf("list pods for artifacts: %w", err)
	}
	var podName string
	for i := range pods.Items {
		if podHasWorkflowOutputReader(&pods.Items[i]) {
			podName = pods.Items[i].Name
			break
		}
	}
	if podName == "" {
		// A job without the reader container has no artifacts (the lease
		// had none).
		return nil
	}
	reader, writer := io.Pipe()
	execCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		var stderr strings.Builder
		err := kr.executePodCommand(execCtx, podName, kubernetesWorkflowOutputReader, kubernetesArtifactsCommand, writer, &stderrLimit{b: &stderr, n: 4096})
		writer.CloseWithError(err)
		done <- err
	}()
	extractErr := ExtractArtifactTar(reader, destDir, maxBytes)
	if extractErr != nil {
		cancel()
		reader.CloseWithError(extractErr)
	}
	execErr := <-done
	if extractErr != nil {
		return extractErr
	}
	if execErr != nil && !errors.Is(execErr, context.Canceled) {
		return fmt.Errorf("read artifacts from the pod: %w", execErr)
	}
	return nil
}

type stderrLimit struct {
	b *strings.Builder
	n int
}

func (s *stderrLimit) Write(p []byte) (int, error) {
	if room := s.n - s.b.Len(); room > 0 {
		if len(p) > room {
			s.b.Write(p[:room])
		} else {
			s.b.Write(p)
		}
	}
	return len(p), nil
}

// ErrArtifactsTooLarge is returned when the artifacts are larger than the
// limit of the job.
var ErrArtifactsTooLarge = errors.New("the job artifacts are larger than the job limit")

// ExtractArtifactTar writes the regular files of a tar stream into destDir.
// It skips links, devices, and every entry whose path leaves destDir. It
// stops with ErrArtifactsTooLarge after maxBytes bytes of file content; the
// files before the limit stay.
func ExtractArtifactTar(r io.Reader, destDir string, maxBytes int64) error {
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return err
	}
	archive := tar.NewReader(r)
	var total int64
	for {
		header, err := archive.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read the artifacts archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		name := path.Clean(strings.TrimPrefix(header.Name, "./"))
		if name == "." || name == "" || strings.HasPrefix(name, "../") || name == ".." || path.IsAbs(name) {
			continue
		}
		if header.Size < 0 || (maxBytes > 0 && total+header.Size > maxBytes) {
			return ErrArtifactsTooLarge
		}
		target := filepath.Join(destDir, filepath.FromSlash(name))
		if rel, err := filepath.Rel(destDir, target); err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			continue
		}
		written, copyErr := io.Copy(file, io.LimitReader(archive, header.Size))
		closeErr := file.Close()
		total += written
		if copyErr != nil {
			return fmt.Errorf("read the artifacts archive: %w", copyErr)
		}
		if closeErr != nil {
			return closeErr
		}
	}
}
