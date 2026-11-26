package coordinatorworker

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/imagealias"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient/csilapi"
)

// logPumpInterval/logPumpMaxLines bound how long a batch of masked log lines
// sits in memory before an AppendLogs push.
const (
	logPumpInterval = 2 * time.Second
	logPumpMaxLines = 200
)

// trackedLease is the run loop's bookkeeping for one lease currently being
// executed: which runner handle it maps to (for Heartbeat's directive
// handling to call Stop/Cleanup on the right container/pod) and the grace
// period a "cancel" directive should honor. outcome records which directive
// (if any) acted on this lease, so runLease's finalize step reports the right
// terminal status even though the container's exit code alone can't
// distinguish "the job's own command exited" from "we SIGTERM'd/killed it".
type trackedLease struct {
	mu           sync.Mutex
	jobID        string
	runnerID     string
	graceSeconds int64
	timing       worker.ExecutionTiming
	cancelWait   context.CancelFunc
	outcome      string // "", "cancel", "kill", "timeout"
	// waitDone is set when WaitForCompletion returned. A timeout that
	// fires after that point does not apply: completion wins.
	waitDone bool
}

// markTimeout sets the outcome "timeout" when no other outcome is set and
// WaitForCompletion has not yet returned. It reports whether it set it.
func (t *trackedLease) markTimeout() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.waitDone || t.outcome != "" {
		return false
	}
	t.outcome = "timeout"
	return true
}

// markWaitDone records that WaitForCompletion returned.
func (t *trackedLease) markWaitDone() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.waitDone = true
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
	// images is the image set manager (imageset.go), or nil. The
	// heartbeat sends its report, and a lease marks its image as in use.
	images *imageSetManager
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

// runLease executes one claimed lease end-to-end: build a JobConfig from the
// lease, inject env + secrets (masked, kept out of every log line), spawn via
// runner, pump masked stdout/stderr to AppendLogs, wait for completion, and
// ReportResult. It never returns an error -- every failure path (spawn
// failure, stream failure, wait failure) is itself reported to the
// coordinator via ReportResult so the job reaches a terminal state instead of
// hanging as "running" forever.
//
// The lease's own execution (SpawnJob/StreamLogs/WaitForCompletion) uses a
// context independent of the poll loop's ctx, so an already-claimed lease
// keeps running (and reports its real result) even if the caller's Run
// context is cancelled for a graceful shutdown -- Run stops polling for new
// work and waits for in-flight leases to finish rather than aborting them. A
// directive-driven Stop/Cleanup (see heartbeat.go) is the only thing that
// interrupts a lease early.
func runLease(c client, runner worker.JobRunner, lease csilapi.Lease, tracker *leaseTracker, cfg Config) {
	logger := logging.Log.WithFields(map[string]interface{}{"lease_id": lease.LeaseId, "job_id": lease.JobId})

	tl := &trackedLease{jobID: lease.JobId, graceSeconds: lease.CancelGraceSeconds}
	tracker.add(lease.LeaseId, tl)
	defer tracker.remove(lease.LeaseId)
	// The job uses the pinned reference of the lease unchanged. Mark its
	// digest as in use, so that the image set removal skips it.
	defer tracker.images.use(lease.Image)()

	masker := secrets.NewMasker()

	env := make(map[string]string, len(lease.Env)+len(lease.Secrets))
	for _, e := range lease.Env {
		env[e.Key] = e.Value
	}
	// Secrets are merged into the container's env (the job process needs them
	// as real env vars to run) but registered with the masker BEFORE anything
	// is spawned or streamed, so every log line pumped to AppendLogs below
	// has secret values scrubbed. They are never logged, never written to
	// disk unmasked (JobConfig.Env only ever lives in process memory / the
	// container's own env, same as a normal job), and never echoed back to
	// the coordinator outside the container's own output (which is masked).
	for _, s := range lease.Secrets {
		env[s.Key] = s.Value
		masker.RegisterSecret(s.Value)
	}

	// The runner image creates /home/runner (uid 1001) but sets no ENV HOME,
	// and run-local (the known-good path) sets HOME explicitly. Without it,
	// tooling that resolves the home dir via $HOME / os.UserHomeDir() (helm,
	// kubectl, pip --user, git) fails -- os.UserHomeDir() errors on an empty
	// $HOME rather than falling back to /etc/passwd, and whether the runtime
	// populates HOME from passwd differs between docker and containerd.
	// Default it here (a job may still override via its own env).
	if _, ok := env["HOME"]; !ok {
		env["HOME"] = "/home/runner"
	}

	// workspaceRoot ("" -> OS temp dir) must resolve to the same path inside
	// this worker and on the host when the worker drives a host container
	// runtime, so the runtime can stat the /job bind-mount source. See
	// Config.WorkspaceRoot.
	if cfg.WorkspaceRoot != "" {
		if err := os.MkdirAll(cfg.WorkspaceRoot, 0o755); err != nil {
			logger.WithError(err).Error("failed to create lease workspace root")
			reportPlatformFailure(c, cfg, lease.LeaseId, "failed to create workspace root: "+err.Error())
			return
		}
	}
	workspaceDir, err := os.MkdirTemp(cfg.WorkspaceRoot, "reactorcide-worker-lease-*")
	if err != nil {
		logger.WithError(err).Error("failed to create lease workspace directory")
		reportPlatformFailure(c, cfg, lease.LeaseId, "failed to create workspace: "+err.Error())
		return
	}
	// workspaceDir (and any VCS auth credential material written under it
	// below by prepareVCSAuth) is discarded here on every return path --
	// there is no separate VCS-auth cleanup step.
	defer os.RemoveAll(workspaceDir)

	// The job container runs as RunAsUser (default 1001:1001), not as the
	// worker process that just created this dir (root under the privileged
	// deploy compose). MkdirTemp makes it 0700 owned by the creator, so the
	// job uid cannot even traverse /job -- the runner then fails with "[Errno
	// 13] Permission denied: '/job/src'". Make the workspace owned by /
	// writable for the job uid, mirroring run-local's makeWritableFor: prefer
	// chown; fall back to world-writable where chown is not permitted
	// (rootless / user namespaces).
	wsUID, wsGID := authFileOwner(lease.RunAsUser)
	if err := makeWritableFor(workspaceDir, wsUID, wsGID); err != nil {
		logger.WithError(err).Error("failed to make lease workspace accessible to job uid")
		reportPlatformFailure(c, cfg, lease.LeaseId, "failed to prepare workspace permissions: "+err.Error())
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
			reportPlatformFailure(c, cfg, lease.LeaseId, "failed to prepare code dir: "+err.Error())
			return
		}
		if err := makeWritableFor(codeDir, wsUID, wsGID); err != nil {
			logger.WithError(err).Error("failed to make job code dir writable")
			reportPlatformFailure(c, cfg, lease.LeaseId, "failed to prepare code dir: "+err.Error())
			return
		}
	}
	if wsUID != 1001 && wsUID != 0 {
		ctlDir := filepath.Join(workspaceDir, ".reactorcide-ctl")
		homeDir := filepath.Join(ctlDir, "home")
		if err := os.MkdirAll(homeDir, 0o755); err != nil {
			logger.WithError(err).Error("failed to create control home dir")
			reportPlatformFailure(c, cfg, lease.LeaseId, "failed to prepare home dir: "+err.Error())
			return
		}
		if err := makeWritableFor(homeDir, wsUID, wsGID); err != nil {
			logger.WithError(err).Error("failed to make control home dir writable")
			reportPlatformFailure(c, cfg, lease.LeaseId, "failed to prepare home dir: "+err.Error())
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
			reportPlatformFailure(c, cfg, lease.LeaseId, "failed to prepare passwd: "+err.Error())
			return
		}
		if err := os.WriteFile(groupFile, []byte(group), 0o644); err != nil {
			logger.WithError(err).Error("failed to write synthetic group")
			reportPlatformFailure(c, cfg, lease.LeaseId, "failed to prepare group: "+err.Error())
			return
		}
		extraMounts = append(extraMounts,
			passwdFile+":/etc/passwd:ro",
			groupFile+":/etc/group:ro",
			homeDir+":/home/runner",
		)
	}

	// Set up git checkout auth BEFORE spawning, registering the resolved
	// token with masker so any log line the container emits (e.g. a checkout
	// command that echoes its own URL) is masked from the very first chunk
	// pumped to AppendLogs. Absent for jobs whose source needs no checkout
	// credential -- see internal/workerapi/vcs_auth.go for how the
	// coordinator decides whether to populate lease.vcs_auth at all.
	vcsAuth, err := prepareVCSAuth(lease.VcsAuth, workspaceDir, lease.RunAsUser, masker)
	if err != nil {
		logger.WithError(err).Error("failed to prepare VCS checkout auth")
		reportPlatformFailure(c, cfg, lease.LeaseId, "failed to prepare VCS checkout auth: "+err.Error())
		return
	}
	if vcsAuth != nil {
		env["REACTORCIDE_VCS_AUTH_DIR"] = vcsAuth.ContainerDir
		env["REACTORCIDE_VCS_AUTH_CLEANUP"] = "true"
		env["GIT_CONFIG_GLOBAL"] = vcsAuth.ContainerDir + "/gitconfig"
		env["GIT_TERMINAL_PROMPT"] = "0"
	}

	jobConfig := &worker.JobConfig{
		Image:              lease.Image,
		Command:            lease.Command,
		Env:                env,
		WorkspaceDir:       workspaceDir,
		WorkingDir:         lease.WorkingDir,
		Capabilities:       append([]string{}, lease.Capabilities...),
		ImagePullSecrets:   append([]string{}, lease.ImagePullSecrets...),
		RunAsUser:          lease.RunAsUser,
		RunAsAdministrator: copyOptionalBool(lease.RunAsAdministrator),
		ExtraMounts:        extraMounts,
		VCSAuth:            vcsAuth,
		TimeoutSeconds:     int(lease.TimeoutSeconds),
		CPURequest:         lease.Resources.CpuRequest,
		CPULimit:           lease.Resources.CpuLimit,
		MemoryLimit:        lease.Resources.MemoryLimit,
		StorageRequest:     derefWireText(lease.Resources.StorageRequest),
		StorageLimit:       derefWireText(lease.Resources.StorageLimit),
		JobID:              lease.JobId,
	}
	// The cache decision of the coordinator (cache.go).
	jobConfig.Cache = cacheConfigFromLease(lease.Cache)
	// The artifacts directory and the quarantined tests file (artifacts.go).
	if err := prepareLeaseArtifacts(lease, runner, workspaceDir, jobConfig, wsUID, wsGID); err != nil {
		logger.WithError(err).Error("failed to prepare the job artifacts directory")
		reportPlatformFailure(c, cfg, lease.LeaseId, "failed to prepare artifacts: "+err.Error())
		return
	}
	if cfg.AllowInsecureTransport {
		jobConfig.Command = withRunnerlibInsecureTransport(jobConfig.Command)
		// A job script calls "runnerlib oidc-token" itself, so the worker
		// cannot add the flag to the command. runnerlib reads this value
		// as the default of --allow-insecure-transport.
		if jobConfig.Env == nil {
			jobConfig.Env = map[string]string{}
		}
		jobConfig.Env[runnerlibInsecureTransportEnv] = "true"
	}
	// Egress control (egress.go): register the job with the egress proxy
	// before it starts; remove it after the job and its containers end.
	if lease.Egress != nil {
		egressConfig, releaseEgress, err := startLeaseEgress(context.Background(), cfg.Egress, lease, masker)
		if err != nil {
			logger.WithError(err).Error("failed to set up egress control for leased job")
			reportPlatformFailure(c, cfg, lease.LeaseId, masker.MaskString("egress control: "+err.Error()))
			return
		}
		defer releaseEgress()
		jobConfig.Egress = egressConfig
	}

	runCtx, cancelWait := context.WithCancel(context.Background())
	defer cancelWait()
	tl.mu.Lock()
	tl.cancelWait = cancelWait
	tl.mu.Unlock()

	maskedCmd := masker.MaskCommandArgs(jobConfig.Command)
	// Owner decision I2: "alias (ref)" when the job named an alias.
	spawnFields := map[string]interface{}{"image": imagealias.Display(jobConfig.Image, lease.ImageAlias), "command": worker.LogCommand(maskedCmd)}
	if lease.ImageRetryOf != nil && *lease.ImageRetryOf != "" {
		spawnFields["image_retry_of"] = *lease.ImageRetryOf
	}
	logger.WithFields(spawnFields).Info("spawning leased job")

	runnerID, err := runner.SpawnJob(runCtx, jobConfig)
	if err != nil {
		logger.WithError(err).Error("failed to spawn leased job")
		class, detail := worker.ClassifyExecutionError(err, 1)
		// SpawnJob returns only after the main container started, so a
		// spawn error means it did not start.
		sendLeaseResult(c, cfg, lease.LeaseId, leaseResult{
			exitCode: 1, status: "failed", errMsg: masker.MaskString("failed to spawn job: " + err.Error()),
			class: class, detail: masker.MaskString(detail), mainStarted: boolRef(false),
		})
		return
	}

	tl.mu.Lock()
	tl.runnerID = runnerID
	if _, precise := runner.(worker.ExecutionTimingReader); !precise {
		tl.timing.StartedAt = time.Now().UTC()
	}
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

	captureExecutionTiming(runCtx, runner, tl)
	heartbeatOnce(runCtx, c, runner, tracker)

	var pumpWg sync.WaitGroup
	metricsCtx, stopMetrics := context.WithCancel(context.Background())
	pumpWg.Add(1)
	go func() {
		defer pumpWg.Done()
		pumpMetrics(metricsCtx, c, runner, lease.LeaseId, runnerID, cfg)
	}()
	// Owner decision I2: the job log starts with the image display line
	// for a VM job or a job that named an alias.
	header := jobLogHeader(runner, jobConfig.Image, lease.ImageAlias, lease.ImageRetryOf)
	if stdout == nil && len(header) > 0 {
		stdout = io.NopCloser(strings.NewReader(""))
	}
	if stdout != nil {
		pumpWg.Add(1)
		go func() {
			defer pumpWg.Done()
			pumpLogs(c, lease.LeaseId, "stdout", stdout, masker, cfg.DataDir, cfg.TelemetryBufferBatches, header...)
		}()
	}
	if stderr != nil {
		pumpWg.Add(1)
		go func() {
			defer pumpWg.Done()
			pumpLogs(c, lease.LeaseId, "stderr", stderr, masker, cfg.DataDir, cfg.TelemetryBufferBatches)
		}()
	}

	timeoutCtx, stopTimeout := context.WithCancel(context.Background())
	go enforceTimeout(timeoutCtx, runner, tl, time.Duration(lease.TimeoutSeconds)*time.Second, logger)
	exitCode, waitErr := runner.WaitForCompletion(runCtx, runnerID)
	tl.markWaitDone()
	stopTimeout()
	captureExecutionTiming(runCtx, runner, tl)
	tl.mu.Lock()
	if _, precise := runner.(worker.ExecutionTimingReader); !precise {
		tl.timing.FinishedAt = time.Now().UTC()
	}
	timing := tl.timing
	tl.mu.Unlock()
	stopMetrics()
	pumpWg.Wait()
	// Give retained batches one final delivery attempt while the lease is
	// still active. ReportResult releases the lease.
	replayTelemetrySpool(c, cfg.DataDir)

	outcome := tl.getOutcome()
	if outcome == "timeout" && completedWithinTimeout(exitCode, waitErr, timing, lease.TimeoutSeconds) {
		// The main container exited 0 inside its timeout; the timeout
		// check fired only before WaitForCompletion noticed. Completion
		// wins.
		outcome = ""
	}
	result := finalizeStatus(outcome, exitCode, waitErr, lease.TimeoutSeconds)
	if result.status == "failed" && result.class == models.FailureClassJob {
		// The runner may know that the exit was not the job's own failure
		// (an OOM kill, an eviction). Read it before Cleanup removes the
		// container or pod.
		classifyCtx, cancelClassify := context.WithTimeout(context.Background(), 10*time.Second)
		result.class, result.detail = worker.ClassifyExit(classifyCtx, runner, runnerID, exitCode)
		cancelClassify()
		if result.class == models.FailureClassTimeout {
			// The runtime stopped the job at its own deadline (the
			// Kubernetes Job active deadline).
			result.status = "timeout"
			result.errMsg = fmt.Sprintf("job exceeded its timeout of %d seconds", lease.TimeoutSeconds)
		}
		if result.detail != "" {
			result.errMsg += " (" + result.detail + ")"
		}
	}
	result.errMsg = masker.MaskString(result.errMsg)
	result.detail = masker.MaskString(result.detail)
	result.workflowOutput = readWorkflowOutput(workspaceDir)
	if outputReader, ok := runner.(worker.WorkflowOutputReader); ok {
		if output, captured := outputReader.TakeWorkflowOutput(runnerID); captured {
			result.workflowOutput = output
		}
	}
	result.timing = &timing
	// Upload the job artifacts before the result (artifacts.go): the
	// coordinator reads the test reports when the result arrives.
	uploadLeaseArtifacts(c, runner, runnerID, lease, workspaceDir, result.workflowOutput, masker)
	retryable := result.status == "failed" && result.class == models.FailureClassPlatform
	result.mainStarted = leaseMainStarted(runner, runnerID, tl, retryable)
	sendLeaseResult(c, cfg, lease.LeaseId, result)
}

func boolRef(v bool) *bool { return &v }

// completedWithinTimeout reports whether a run that the timeout check
// marked "timeout" in fact completed inside its timeout: the exit was 0
// with no wait error, and the main container timestamps show a run time
// not longer than the timeout.
func completedWithinTimeout(exitCode int, waitErr error, timing worker.ExecutionTiming, timeoutSeconds int64) bool {
	if exitCode != 0 || waitErr != nil || timeoutSeconds <= 0 {
		return false
	}
	if timing.StartedAt.IsZero() || timing.FinishedAt.IsZero() {
		return false
	}
	return timing.FinishedAt.Sub(timing.StartedAt) <= time.Duration(timeoutSeconds)*time.Second
}

// leaseMainStarted decides main_started of a result after SpawnJob
// succeeded. The coordinator retries an infra.platform failure only when
// main_started is false, so false needs proof:
//
//   - A runner without ExecutionTimingReader (docker, containerd, VM)
//     returns from SpawnJob after the main container started: true.
//   - A start time from the runtime: true.
//   - Otherwise, for a retryable failure only, the worker reads the
//     runtime state. When it shows a main container that has not started,
//     the worker stops the job (so it cannot start later) and reads the
//     state again. No start time in the second read: false.
//   - Any read or stop error: unknown (nil), which is never retried.
func leaseMainStarted(runner worker.JobRunner, runnerID string, tl *trackedLease, retryable bool) *bool {
	reader, precise := runner.(worker.ExecutionTimingReader)
	if !precise {
		return boolRef(true)
	}
	tl.mu.Lock()
	started := tl.timing.StartedAt
	tl.mu.Unlock()
	if !started.IsZero() {
		return boolRef(true)
	}
	if !retryable {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	before, err := reader.ExecutionTiming(ctx, runnerID)
	if err != nil {
		return nil
	}
	if !before.StartedAt.IsZero() {
		return boolRef(true)
	}
	if !before.MainNotStarted {
		return nil
	}
	if err := runner.Stop(ctx, runnerID, 0); err != nil {
		return nil
	}
	after, err := reader.ExecutionTiming(ctx, runnerID)
	if err != nil {
		return nil
	}
	if !after.StartedAt.IsZero() {
		return boolRef(true)
	}
	return boolRef(false)
}

// timeoutPollInterval is how often enforceTimeout checks a lease's run
// time. Tests shorten it.
var timeoutPollInterval = time.Second

// enforceTimeout stops a job that runs longer than timeout and marks its
// lease outcome "timeout", so the result reports status "timeout". The run
// time starts when the main container starts (ExecutionTiming), so time in
// a scheduling queue (a pending pod) does not count. A timeout of zero or
// less never fires. It returns when ctx ends (the job finished) or after it
// acted once.
func enforceTimeout(ctx context.Context, runner worker.JobRunner, tl *trackedLease, timeout time.Duration, logger interface {
	Warn(args ...interface{})
}) {
	if timeout <= 0 {
		return
	}
	ticker := time.NewTicker(timeoutPollInterval)
	defer ticker.Stop()
	for ticks := 0; ; ticks++ {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		tl.mu.Lock()
		started := tl.timing.StartedAt
		tl.mu.Unlock()
		if started.IsZero() {
			if ticks%10 == 0 {
				captureExecutionTiming(ctx, runner, tl)
			}
			continue
		}
		if time.Since(started) < timeout {
			continue
		}
		if !tl.markTimeout() {
			return
		}
		tl.mu.Lock()
		runnerID := tl.runnerID
		grace := time.Duration(tl.graceSeconds) * time.Second
		tl.mu.Unlock()
		logger.Warn(fmt.Sprintf("job exceeded its timeout of %s; stopping it", timeout))
		if err := runner.Stop(context.Background(), runnerID, grace); err != nil {
			logger.Warn("failed to stop timed out job: " + err.Error())
		}
		return
	}
}

// runnerlibInsecureTransportEnv tells runnerlib that the worker runs in the
// development mode without TLS (--allow-insecure-transport). runnerlib
// oidc-token reads it.
const runnerlibInsecureTransportEnv = "REACTORCIDE_ALLOW_INSECURE_TRANSPORT"

// runnerlibInsecureSubcommands are the runnerlib commands that call the
// coordinator with the job token.
var runnerlibInsecureSubcommands = map[string]bool{"eval": true, "trigger": true, "oidc-token": true}

func withRunnerlibInsecureTransport(command []string) []string {
	if len(command) >= 2 && command[0] == "runnerlib" && runnerlibInsecureSubcommands[command[1]] {
		for _, arg := range command[2:] {
			if arg == "--allow-insecure-transport" {
				return command
			}
		}
		return append(append([]string(nil), command...), "--allow-insecure-transport")
	}
	if len(command) == 3 && (command[1] == "-c" || command[1] == "/c") {
		script := strings.TrimSpace(command[2])
		if (strings.HasPrefix(script, "runnerlib eval ") || strings.HasPrefix(script, "runnerlib trigger ") ||
			strings.HasPrefix(script, "runnerlib oidc-token ")) && !strings.Contains(script, "--allow-insecure-transport") {
			result := append([]string(nil), command...)
			result[2] = command[2] + " --allow-insecure-transport"
			return result
		}
	}
	return command
}

// leaseResult is one lease's result as the worker reports it.
type leaseResult struct {
	exitCode       int
	status         string
	errMsg         string
	class          string
	detail         string
	workflowOutput string
	timing         *worker.ExecutionTiming
	// mainStarted is main_started of the result: nil when unknown.
	mainStarted *bool
}

// finalizeStatus turns a lease's raw execution outcome into the status,
// error, and failure class ReportResult reports. A directive outcome
// ("cancel"/"kill") or the worker's own timeout always wins over the raw
// exit code, since Stop/Cleanup deliberately terminated the container -- its
// exit code reflects SIGTERM/SIGKILL, not the job's own success or failure.
// A non-zero exit gets class "job"; the caller refines it with the runner's
// ExitClassifier.
func finalizeStatus(outcome string, exitCode int, waitErr error, timeoutSeconds int64) leaseResult {
	result := leaseResult{exitCode: exitCode}
	switch outcome {
	case "cancel":
		result.status, result.errMsg, result.class = "cancelled", "cancelled by coordinator directive", models.FailureClassCancelled
		return result
	case "kill":
		result.status, result.errMsg, result.class = "cancelled", "killed by coordinator directive", models.FailureClassCancelled
		return result
	case "timeout":
		result.status, result.class = "timeout", models.FailureClassTimeout
		result.errMsg = fmt.Sprintf("job exceeded its timeout of %d seconds", timeoutSeconds)
		return result
	}
	if waitErr != nil {
		result.status, result.errMsg = "failed", waitErr.Error()
		result.class, result.detail = worker.ClassifyExecutionError(waitErr, exitCode)
		if result.class == models.FailureClassTimeout {
			result.status = "timeout"
		}
		return result
	}
	if exitCode != 0 {
		result.status, result.errMsg, result.class = "failed", fmt.Sprintf("job exited with code %d", exitCode), models.FailureClassJob
		return result
	}
	result.status = "completed"
	return result
}

// resultRetryWindow bounds how long a worker keeps a result it could not
// deliver. The lease stays in the tracker for the whole window, so
// heartbeats keep it open on the coordinator. After the window the
// coordinator's lost-job reconciler owns the job.
var (
	resultRetryWindow = 15 * time.Minute
	resultRetryMin    = reconnectBackoffMin
	resultRetryMax    = reconnectBackoffMax
)

// reportPlatformFailure reports a failure of the worker's own preparation
// (workspace, credentials) before the job ran: infra.platform.
func reportPlatformFailure(c client, cfg Config, leaseID, errMsg string) {
	sendLeaseResult(c, cfg, leaseID, leaseResult{exitCode: 1, status: "failed", errMsg: errMsg, class: models.FailureClassPlatform, mainStarted: boolRef(false)})
}

// sendLeaseResult delivers a lease's result with ReportExecutionResult, on
// a context independent of the lease's own execution context (which may
// already be torn down), so a result is still delivered after a
// directive-driven Stop/Cleanup. See deliverResult for the retry rules.
func sendLeaseResult(c client, cfg Config, leaseID string, result leaseResult) {
	deliverResult(c, cfg, leaseID, func(ctx context.Context) error {
		req := csilapi.ReportResultRequest{LeaseId: leaseID, ExitCode: int64(result.exitCode), Status: result.status}
		if result.errMsg != "" {
			req.Error = &result.errMsg
		}
		if result.workflowOutput != "" {
			req.WorkflowOutput = &result.workflowOutput
		}
		if result.timing != nil {
			req.StartedAt = timestampText(result.timing.StartedAt)
			req.FinishedAt = timestampText(result.timing.FinishedAt)
		}
		if result.class != "" {
			req.FailureClass = &result.class
		}
		if result.detail != "" {
			req.FailureDetail = &result.detail
		}
		req.MainStarted = result.mainStarted
		_, err := c.ReportExecutionResult(ctx, req)
		return err
	})
}

// deliverResult calls send until the coordinator accepts the result, the
// coordinator says the lease is gone (a final error, see isRetryable), or
// resultRetryWindow expires. Before each retry it replays the telemetry
// spool, so the last log and metric batches get the same retries. An
// "unauthorized" answer means this worker's session is gone: the worker
// registers again before the next attempt. A lease goroutine holds its
// concurrency slot while it retries, so the poll loop cannot do that for it.
func deliverResult(c client, cfg Config, leaseID string, send func(context.Context) error) {
	logger := logging.Log.WithField("lease_id", leaseID)
	deadline := time.Now().Add(resultRetryWindow)
	backoff := resultRetryMin
	for attempt := 1; ; attempt++ {
		if attempt > 1 {
			replayTelemetrySpool(c, cfg.DataDir)
		}
		err := send(context.Background())
		if err == nil {
			if attempt > 1 {
				logger.WithField("attempts", attempt).Info("reported job result to coordinator after retry")
			}
			return
		}
		if !isRetryable(err) {
			logger.WithError(err).Error("failed to report job result to coordinator; coordinator rejected it")
			return
		}
		if time.Now().Add(backoff).After(deadline) {
			logger.WithError(err).WithField("attempts", attempt).Error("failed to report job result to coordinator; giving up after retry window")
			return
		}
		logger.WithError(err).WithField("attempt", attempt).Warn("failed to report job result to coordinator; retrying")
		time.Sleep(backoff)
		backoff = nextBackoff(backoff, resultRetryMax)
		if isUnauthorized(err) {
			regCtx, cancel := context.WithDeadline(context.Background(), deadline)
			if _, regErr := registerWithBackoff(regCtx, cfg, c); regErr != nil {
				cancel()
				logger.WithError(regErr).Error("failed to report job result to coordinator; could not open a new session")
				return
			}
			cancel()
		}
	}
}

// isRetryable reports whether a failed coordinator call can succeed later.
// A transport failure or a ServiceError such as "unavailable", "internal",
// or "unauthorized" can. "not_found" (the lease is gone), "invalid_argument",
// "forbidden", and "conflict" are final answers.
func isRetryable(err error) bool {
	var se *workerclient.ServiceCallError
	if !errors.As(err, &se) {
		return true
	}
	switch se.Code {
	case "not_found", "invalid_argument", "forbidden", "conflict":
		return false
	}
	return true
}

func isUnauthorized(err error) bool {
	var se *workerclient.ServiceCallError
	return errors.As(err, &se) && se.Code == "unauthorized"
}

const maxWorkflowOutputBytes = 1 << 20

func readWorkflowOutput(workspaceDir string) string {
	file, err := os.Open(filepath.Join(workspaceDir, "workflow-output.json"))
	if err != nil {
		return ""
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxWorkflowOutputBytes+1))
	if err != nil || len(data) > maxWorkflowOutputBytes {
		return ""
	}
	return string(data)
}

// jobLogHeaderPrefix starts each line that the worker (not the job) writes
// to the job log.
const jobLogHeaderPrefix = "[reactorcide] "

// jobLogHeader returns the lines that start the job log (owner decision
// I2): "image: alias (ref)" when the job named an alias, or "image: ref"
// for a VM job without an alias. When a retry or a re-run changed the image,
// a second line "retry of: <earlier image>" follows, and the header is
// written for any job. A container job without an alias and without a
// changed image gets no header, so its log is the same as before.
func jobLogHeader(runner worker.JobRunner, image string, alias, retryOf *string) []string {
	hasAlias := alias != nil && *alias != ""
	hasRetryOf := retryOf != nil && *retryOf != ""
	isVM := false
	if seeder, ok := runner.(worker.ImageSetSeeder); ok {
		isVM = seeder.ImageSetKind() == worker.ImageKindVM
	}
	if image == "" || (!hasAlias && !isVM && !hasRetryOf) {
		return nil
	}
	header := []string{jobLogHeaderPrefix + "image: " + imagealias.Display(image, alias)}
	if hasRetryOf {
		// A retry or a re-run that resolved the alias again to a new pin
		// (docs/image-aliases.md "Retry and rerun").
		header = append(header, jobLogHeaderPrefix+"retry of: "+*retryOf)
	}
	return header
}

// pumpLogs reads newline-delimited lines from r, masks each line, batches
// them, and pushes batches to AppendLogs on a ticker (or when the batch
// fills), flushing whatever remains when r reaches EOF/closes. Masking
// happens before a line is ever buffered, so a secret value never reaches the
// coordinator in a log chunk regardless of how batching splits lines across
// calls. header lines (jobLogHeader) go first, in the same sequence, so
// they are the first lines of the stream.
func pumpLogs(c client, leaseID, stream string, r io.ReadCloser, masker *secrets.Masker, dataDir string, bufferBatches int, header ...string) {
	defer r.Close()

	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		for _, line := range header {
			lines <- line
		}
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		if err := scanner.Err(); err != nil {
			logging.Log.WithError(err).WithFields(map[string]interface{}{"lease_id": leaseID, "stream": stream}).Warn("error reading job output stream")
		}
	}()

	ticker := time.NewTicker(logPumpInterval)
	defer ticker.Stop()

	buf := make([]string, 0, logPumpMaxLines)
	var sequence int64
	flush := func() {
		if len(buf) == 0 {
			return
		}
		observedAt := time.Now().UTC().Format(time.RFC3339Nano)
		entries := make([]csilapi.LogBatchEntry, 0, len(buf))
		for _, line := range buf {
			entries = append(entries, csilapi.LogBatchEntry{ObservedAt: observedAt, Level: "info", Message: line})
		}
		buf = buf[:0]
		req := csilapi.AppendLogBatchRequest{LeaseId: leaseID, Stream: stream, Sequence: sequence, Entries: entries}
		sequence++
		if _, err := persistAndSendLog(c, dataDir, bufferBatches, req); err != nil {
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

func derefWireText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// copyOptionalBool copies a lease's optional bool. nil (absent on the wire)
// stays nil.
func copyOptionalBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	v := *value
	return &v
}
