package coordinatorworker

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient/csilapi"
)

// logPumpInterval/logPumpMaxLines bound how long a batch of masked log
// lines sits in memory before an AppendLogs push, mirroring
// internal/worker/log_shipper.go's periodic-chunk-upload shape (there: a
// ticker driving uploads to object storage; here: the same ticker shape
// driving pushes to the coordinator instead).
const (
	logPumpInterval = 2 * time.Second
	logPumpMaxLines = 200
)

// trackedLease is the run loop's bookkeeping for one lease currently being
// executed: which runner handle it maps to (for Heartbeat's directive
// handling to call Stop/Cleanup on the right container/pod) and the
// grace period a "cancel" directive should honor. outcome records which
// directive (if any) acted on this lease, so runLease's finalize step
// reports the right terminal status even though the container's exit code
// alone can't distinguish "the job's own command exited" from "we
// SIGTERM'd/killed it".
type trackedLease struct {
	mu           sync.Mutex
	jobID        string
	runnerID     string
	graceSeconds int64
	outcome      string // "", "cancel", "kill"
}

func (t *trackedLease) setOutcome(outcome string) (prior string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	prior = t.outcome
	if t.outcome == "" {
		t.outcome = outcome
	}
	return prior
}

func (t *trackedLease) getOutcome() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.outcome
}

// leaseTracker is the concurrency-safe registry of in-flight leases shared
// between the poll loop (which adds/removes entries as it spawns/finishes
// runLease goroutines) and the heartbeat loop (which reads it to report
// running_leases and to resolve a directive's lease_id to a runner handle).
type leaseTracker struct {
	mu    sync.Mutex
	items map[string]*trackedLease
}

func newLeaseTracker() *leaseTracker {
	return &leaseTracker{items: make(map[string]*trackedLease)}
}

func (t *leaseTracker) add(leaseID string, tl *trackedLease) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.items[leaseID] = tl
}

func (t *leaseTracker) remove(leaseID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.items, leaseID)
}

func (t *leaseTracker) get(leaseID string) (*trackedLease, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tl, ok := t.items[leaseID]
	return tl, ok
}

func (t *leaseTracker) runningLeaseIDs() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	ids := make([]string, 0, len(t.items))
	for id := range t.items {
		ids = append(ids, id)
	}
	return ids
}

// runLease executes one claimed lease end-to-end: build a JobConfig from
// the lease, inject env + secrets (masked, kept out of every log line),
// spawn via runner, pump masked stdout/stderr to AppendLogs, wait for
// completion, and ReportResult. It never returns an error -- every failure
// path (spawn failure, stream failure, wait failure) is itself reported to
// the coordinator via ReportResult so the job reaches a terminal state
// instead of hanging as "running" forever.
//
// The lease's own execution (SpawnJob/StreamLogs/WaitForCompletion) uses a
// context independent of the poll loop's ctx, so an already-claimed lease
// keeps running (and reports its real result) even if the caller's Run
// context is cancelled for a graceful shutdown -- Run stops polling for new
// work and waits for in-flight leases to finish rather than aborting them.
// A directive-driven Stop/Cleanup (see heartbeat.go) is the only thing that
// interrupts a lease early.
func runLease(c client, runner worker.JobRunner, lease csilapi.Lease, tracker *leaseTracker, workspaceRoot string) {
	logger := logging.Log.WithFields(map[string]interface{}{"lease_id": lease.LeaseId, "job_id": lease.JobId})

	tl := &trackedLease{jobID: lease.JobId, graceSeconds: lease.CancelGraceSeconds}
	tracker.add(lease.LeaseId, tl)
	defer tracker.remove(lease.LeaseId)

	masker := secrets.NewMasker()

	env := make(map[string]string, len(lease.Env)+len(lease.Secrets))
	for _, e := range lease.Env {
		env[e.Key] = e.Value
	}
	// Secrets are merged into the container's env (the job process needs
	// them as real env vars to run) but registered with the masker BEFORE
	// anything is spawned or streamed, so every log line pumped to
	// AppendLogs below has secret values scrubbed. They are never logged,
	// never written to disk unmasked (JobConfig.Env only ever lives in
	// process memory / the container's own env, same as a normal job), and
	// never echoed back to the coordinator outside the container's own
	// output (which is masked).
	for _, s := range lease.Secrets {
		env[s.Key] = s.Value
		masker.RegisterSecret(s.Value)
	}

	// The runner image creates /home/runner (uid 1001) but sets no ENV HOME,
	// and run-local (the known-good path) sets HOME explicitly. Without it,
	// tooling that resolves the home dir via $HOME / os.UserHomeDir() (helm,
	// kubectl, pip --user, git) fails -- os.UserHomeDir() errors on an empty
	// $HOME rather than falling back to /etc/passwd, and whether the runtime
	// populates HOME from passwd differs between docker and containerd. Default
	// it here (a job may still override via its own env).
	if _, ok := env["HOME"]; !ok {
		env["HOME"] = "/home/runner"
	}

	// workspaceRoot ("" -> OS temp dir) must resolve to the same path inside
	// this worker and on the host when the worker drives a host container
	// runtime, so the runtime can stat the /job bind-mount source. See
	// Config.WorkspaceRoot.
	if workspaceRoot != "" {
		if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
			logger.WithError(err).Error("failed to create lease workspace root")
			reportResult(c, lease.LeaseId, 1, "failed", "failed to create workspace root: "+err.Error())
			return
		}
	}
	workspaceDir, err := os.MkdirTemp(workspaceRoot, "reactorcide-worker-lease-*")
	if err != nil {
		logger.WithError(err).Error("failed to create lease workspace directory")
		reportResult(c, lease.LeaseId, 1, "failed", "failed to create workspace: "+err.Error())
		return
	}
	// workspaceDir (and any VCS auth credential material written under it
	// below by prepareVCSAuth) is discarded here on every return path --
	// there is no separate VCS-auth cleanup step, per WORKERS_PLAN.md P3c:
	// the credential file only ever exists inside this ephemeral workspace.
	defer os.RemoveAll(workspaceDir)

	// The job container runs as RunAsUser (default 1001:1001), not as the
	// worker process that just created this dir (root under the privileged
	// deploy compose). MkdirTemp makes it 0700 owned by the creator, so the
	// job uid cannot even traverse /job -- the runner then fails with
	// "[Errno 13] Permission denied: '/job/src'". Make the workspace owned by
	// / writable for the job uid, mirroring run-local's makeWritableFor:
	// prefer chown; fall back to world-writable where chown is not permitted
	// (rootless / user namespaces).
	wsUID, wsGID := authFileOwner(lease.RunAsUser)
	if err := makeWritableFor(workspaceDir, wsUID, wsGID); err != nil {
		logger.WithError(err).Error("failed to make lease workspace accessible to job uid")
		reportResult(c, lease.LeaseId, 1, "failed", "failed to prepare workspace permissions: "+err.Error())
		return
	}

	// Restore run-local parity for the job's in-container filesystem:
	//   (a) pre-create the working/code dir (default /job/src) owned by the job
	//       uid, so raw-command jobs that write to their cwd without runnerlib's
	//       clone-and-reclone don't hit EACCES (cmd/run_local.go pre-creates it
	//       too); and
	//   (b) for a job uid that is neither the image runner (1001, which has a
	//       real passwd entry + writable /home/runner) nor root, synthesize
	//       /etc/passwd, /etc/group and a writable /home/runner so tools needing
	//       a passwd entry (ssh, sudo, id) and a writable HOME work -- exactly
	//       what run-local does for a non-runner uid.
	// Control files live under the workspace so they inherit its shared,
	// host-visible path (the host runtime must be able to stat bind-mount
	// sources) and its single deferred RemoveAll.
	var extraMounts []string
	if rel := worker.ContainerPathInsideJob(lease.WorkingDir); rel != "." {
		codeDir := filepath.Join(workspaceDir, rel)
		if err := os.MkdirAll(codeDir, 0o755); err != nil {
			logger.WithError(err).Error("failed to pre-create job code dir")
			reportResult(c, lease.LeaseId, 1, "failed", "failed to prepare code dir: "+err.Error())
			return
		}
		if err := makeWritableFor(codeDir, wsUID, wsGID); err != nil {
			logger.WithError(err).Error("failed to make job code dir writable")
			reportResult(c, lease.LeaseId, 1, "failed", "failed to prepare code dir: "+err.Error())
			return
		}
	}
	if wsUID != 1001 && wsUID != 0 {
		ctlDir := filepath.Join(workspaceDir, ".reactorcide-ctl")
		homeDir := filepath.Join(ctlDir, "home")
		if err := os.MkdirAll(homeDir, 0o755); err != nil {
			logger.WithError(err).Error("failed to create control home dir")
			reportResult(c, lease.LeaseId, 1, "failed", "failed to prepare home dir: "+err.Error())
			return
		}
		if err := makeWritableFor(homeDir, wsUID, wsGID); err != nil {
			logger.WithError(err).Error("failed to make control home dir writable")
			reportResult(c, lease.LeaseId, 1, "failed", "failed to prepare home dir: "+err.Error())
			return
		}
		passwdFile := filepath.Join(ctlDir, "passwd")
		groupFile := filepath.Join(ctlDir, "group")
		passwd := fmt.Sprintf(
			"root:x:0:0:root:/root:/bin/sh\n"+
				"reactorcide:x:1001:1001:reactorcide:/home/reactorcide:/bin/sh\n"+
				"runner:x:%d:%d:runner:/home/runner:/bin/sh\n",
			wsUID, wsGID)
		group := fmt.Sprintf("root:x:0:\nreactorcide:x:1001:\nrunner:x:%d:\n", wsGID)
		if err := os.WriteFile(passwdFile, []byte(passwd), 0o644); err != nil {
			logger.WithError(err).Error("failed to write synthetic passwd")
			reportResult(c, lease.LeaseId, 1, "failed", "failed to prepare passwd: "+err.Error())
			return
		}
		if err := os.WriteFile(groupFile, []byte(group), 0o644); err != nil {
			logger.WithError(err).Error("failed to write synthetic group")
			reportResult(c, lease.LeaseId, 1, "failed", "failed to prepare group: "+err.Error())
			return
		}
		extraMounts = append(extraMounts,
			passwdFile+":/etc/passwd:ro",
			groupFile+":/etc/group:ro",
			homeDir+":/home/runner",
		)
	}

	// Set up git checkout auth BEFORE spawning, registering the resolved
	// token with masker so any log line the container emits (e.g. a
	// checkout command that echoes its own URL) is masked from the very
	// first chunk pumped to AppendLogs. Absent for jobs whose source needs
	// no checkout credential -- see internal/workerapi/vcs_auth.go for how
	// the coordinator decides whether to populate lease.vcs_auth at all.
	vcsAuth, err := prepareVCSAuth(lease.VcsAuth, workspaceDir, lease.RunAsUser, masker)
	if err != nil {
		logger.WithError(err).Error("failed to prepare VCS checkout auth")
		reportResult(c, lease.LeaseId, 1, "failed", "failed to prepare VCS checkout auth: "+err.Error())
		return
	}
	if vcsAuth != nil {
		env["REACTORCIDE_VCS_AUTH_DIR"] = vcsAuth.ContainerDir
		env["REACTORCIDE_VCS_AUTH_CLEANUP"] = "true"
		env["GIT_CONFIG_GLOBAL"] = vcsAuth.ContainerDir + "/gitconfig"
		env["GIT_TERMINAL_PROMPT"] = "0"
	}

	jobConfig := &worker.JobConfig{
		Image:          lease.Image,
		Command:        lease.Command,
		Env:            env,
		WorkspaceDir:   workspaceDir,
		WorkingDir:     lease.WorkingDir,
		Capabilities:   append([]string{}, lease.Capabilities...),
		RunAsUser:      lease.RunAsUser,
		ExtraMounts:    extraMounts,
		VCSAuth:        vcsAuth,
		TimeoutSeconds: int(lease.TimeoutSeconds),
		CPURequest:     lease.Resources.CpuRequest,
		CPULimit:       lease.Resources.CpuLimit,
		MemoryLimit:    lease.Resources.MemoryLimit,
		JobID:          lease.JobId,
	}

	runCtx := context.Background()

	maskedCmd := masker.MaskCommandArgs(jobConfig.Command)
	logger.WithFields(map[string]interface{}{"image": jobConfig.Image, "command": strings.Join(maskedCmd, " ")}).Info("spawning leased job")

	runnerID, err := runner.SpawnJob(runCtx, jobConfig)
	if err != nil {
		logger.WithError(err).Error("failed to spawn leased job")
		reportResult(c, lease.LeaseId, 1, "failed", masker.MaskString("failed to spawn job: "+err.Error()))
		return
	}

	tl.mu.Lock()
	tl.runnerID = runnerID
	tl.mu.Unlock()

	defer func() {
		cleanupCtx := context.Background()
		if err := runner.Cleanup(cleanupCtx, runnerID); err != nil {
			logger.WithError(err).Warn("failed to clean up leased job")
		}
	}()

	stdout, stderr, err := runner.StreamLogs(runCtx, runnerID)
	if err != nil {
		logger.WithError(err).Warn("failed to stream logs from leased job; continuing to wait for completion")
	}

	var pumpWg sync.WaitGroup
	if stdout != nil {
		pumpWg.Add(1)
		go func() {
			defer pumpWg.Done()
			pumpLogs(c, lease.LeaseId, "stdout", stdout, masker)
		}()
	}
	if stderr != nil {
		pumpWg.Add(1)
		go func() {
			defer pumpWg.Done()
			pumpLogs(c, lease.LeaseId, "stderr", stderr, masker)
		}()
	}

	exitCode, waitErr := runner.WaitForCompletion(runCtx, runnerID)
	pumpWg.Wait()

	status, errMsg := finalizeStatus(tl.getOutcome(), exitCode, waitErr)
	if errMsg != "" {
		errMsg = masker.MaskString(errMsg)
	}
	reportResult(c, lease.LeaseId, exitCode, status, errMsg)
}

// finalizeStatus turns a lease's raw execution outcome into the
// status/error ReportResult reports. A directive outcome ("cancel"/"kill")
// always wins over the raw exit code, since Stop/Cleanup deliberately
// terminated the container -- its exit code reflects SIGTERM/SIGKILL, not
// the job's own success or failure.
func finalizeStatus(outcome string, exitCode int, waitErr error) (status, errMsg string) {
	switch outcome {
	case "cancel":
		return "cancelled", "cancelled by coordinator directive"
	case "kill":
		return "cancelled", "killed by coordinator directive"
	}
	if waitErr != nil {
		return "failed", waitErr.Error()
	}
	if exitCode != 0 {
		return "failed", fmt.Sprintf("job exited with code %d", exitCode)
	}
	return "completed", ""
}

// reportResult calls ReportResult on a context independent of the lease's
// own execution context (which may already be torn down) so a result is
// still delivered even after a directive-driven Stop/Cleanup. Best-effort:
// a failure here is logged, not retried -- the coordinator's own lease/task
// timeout reaper is the backstop for a worker that can no longer reach it
// (WORKERS_PLAN.md "Lease expiry").
func reportResult(c client, leaseID string, exitCode int, status, errMsg string) {
	if _, err := c.ReportResult(context.Background(), leaseID, exitCode, status, errMsg); err != nil {
		logging.Log.WithError(err).WithField("lease_id", leaseID).Error("failed to report job result to coordinator")
	}
}

// pumpLogs reads newline-delimited lines from r, masks each line, batches
// them, and pushes batches to AppendLogs on a ticker (or when the batch
// fills), flushing whatever remains when r reaches EOF/closes. Masking
// happens before a line is ever buffered, so a secret value never reaches
// the coordinator in a log chunk regardless of how batching splits lines
// across calls.
func pumpLogs(c client, leaseID, stream string, r io.ReadCloser, masker *secrets.Masker) {
	defer r.Close()

	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		if err := scanner.Err(); err != nil {
			logging.Log.WithError(err).WithFields(map[string]interface{}{"lease_id": leaseID, "stream": stream}).Warn("error reading job output stream")
		}
	}()

	ctx := context.Background()
	ticker := time.NewTicker(logPumpInterval)
	defer ticker.Stop()

	buf := make([]string, 0, logPumpMaxLines)
	flush := func() {
		if len(buf) == 0 {
			return
		}
		chunk := strings.Join(buf, "\n") + "\n"
		buf = buf[:0]
		if _, err := c.AppendLogs(ctx, leaseID, stream, chunk); err != nil {
			logging.Log.WithError(err).WithFields(map[string]interface{}{"lease_id": leaseID, "stream": stream}).Warn("failed to append logs to coordinator")
		}
	}

	for {
		select {
		case line, ok := <-lines:
			if !ok {
				flush()
				return
			}
			buf = append(buf, masker.MaskString(line))
			if len(buf) >= logPumpMaxLines {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}
