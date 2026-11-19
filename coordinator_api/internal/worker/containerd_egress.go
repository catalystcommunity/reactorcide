package worker

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// Egress control for the containerd (nerdctl) runner. It is the same
// design as docker_egress.go: a helper container owns the network
// namespace, a setup container installs the redirect in it, and the job
// joins it.

// containerdEgressJobs holds the job IDs with an egress helper, so that
// Cleanup removes helpers only for them. A helper left by a worker crash is
// removed by the sweep of leaked containers (egressComponent label).
var containerdEgressJobs sync.Map

func nerdctlEnvArgs(m map[string]string) []string {
	var out []string
	for _, e := range sortedEnv(m) {
		out = append(out, "-e", e.Name+"="+e.Value)
	}
	return out
}

// nerdctlEgressHelperArgs is the "nerdctl run" of the helper container.
func nerdctlEgressHelperArgs(config *JobConfig) []string {
	e := config.Egress
	network := e.Network
	if network == "" {
		network = "bridge"
	}
	helperEnv := e.HelperEnv()
	delete(helperEnv, "REACTORCIDE_EGRESS_CA_PEM")
	serve := e.ServeArgs()
	args := []string{
		"--namespace", containerdNamespace, "run", "-d",
		"--name", EgressHelperName(config.JobID),
		"--net", network,
		"--dns", EgressFakeResolverIP,
		"--user", "65532:65532",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--read-only",
		"--label", "reactorcide.job_id=" + config.JobID,
		"--label", "reactorcide.component=" + egressComponent,
		"--entrypoint", serve[0],
	}
	args = append(args, nerdctlEnvArgs(helperEnv)...)
	args = append(args, e.NetImage)
	return append(args, serve[1:]...)
}

// nerdctlEgressSetupArgs is the "nerdctl run" of the setup container.
func nerdctlEgressSetupArgs(config *JobConfig) []string {
	podnet := config.Egress.PodnetArgs("")
	args := []string{
		"--namespace", containerdNamespace, "run", "--rm",
		"--name", egressSetupName(config.JobID),
		"--net", "container:" + EgressHelperName(config.JobID),
		"--user", "0:0",
		"--cap-drop", "ALL",
		"--cap-add", "NET_ADMIN",
		"--security-opt", "no-new-privileges",
		"--read-only",
		"--label", "reactorcide.job_id=" + config.JobID,
		"--label", "reactorcide.component=" + egressComponent,
		"--entrypoint", podnet[0],
		config.Egress.NetImage,
	}
	return append(args, podnet[1:]...)
}

// nerdctlEgressJobArgs are the security arguments of a job container with
// egress: no capability and no-new-privileges, so that the job cannot get
// CAP_NET_RAW (AF_PACKET frames skip nftables). See hardenDockerEgressJob.
func nerdctlEgressJobArgs() []string {
	return []string{"--cap-drop", "ALL", "--security-opt", "no-new-privileges"}
}

// prepareEgress starts the egress network of a containerd job. It returns
// the job config to use and the --net value of the job ("" without
// egress).
func (cr *ContainerdRunner) prepareEgress(ctx context.Context, config *JobConfig) (*JobConfig, string, error) {
	out, err := prepareEgressJobConfig(config)
	if err != nil || config.Egress == nil {
		return out, "", err
	}
	if o, err := exec.CommandContext(ctx, nerdctlBinary, "--namespace", containerdNamespace, "pull", config.Egress.NetImage).CombinedOutput(); err != nil {
		return nil, "", NewFailureError(models.FailureClassImagePull, "image pull",
			fmt.Errorf("egress image %s: %w (%s)", config.Egress.NetImage, err, strings.TrimSpace(lastLines(string(o), 3))))
	}
	if o, err := exec.CommandContext(ctx, nerdctlBinary, nerdctlEgressHelperArgs(config)...).CombinedOutput(); err != nil {
		cr.removeEgress(context.Background(), config.JobID)
		return nil, "", fmt.Errorf("egress: start helper: %w (%s)", err, strings.TrimSpace(lastLines(string(o), 3)))
	}
	if o, err := exec.CommandContext(ctx, nerdctlBinary, nerdctlEgressSetupArgs(config)...).CombinedOutput(); err != nil {
		cr.removeEgress(context.Background(), config.JobID)
		return nil, "", fmt.Errorf("egress: the redirect setup failed: %w (%s)", err, strings.TrimSpace(lastLines(string(o), 3)))
	}
	containerdEgressJobs.Store(config.JobID, true)
	logging.Log.WithField("job_id", config.JobID).Info("Egress network ready")
	return out, "container:" + EgressHelperName(config.JobID), nil
}

// removeEgress removes the egress containers of a job.
func (cr *ContainerdRunner) removeEgress(ctx context.Context, jobID string) {
	for _, name := range []string{egressSetupName(jobID), EgressHelperName(jobID)} {
		_ = exec.CommandContext(ctx, nerdctlBinary, "--namespace", containerdNamespace, "rm", "-f", name).Run()
	}
}

// removeEgressForContainer removes the egress containers of a job
// container. The runner names job containers "reactorcide-job-<job id>".
func (cr *ContainerdRunner) removeEgressForContainer(ctx context.Context, containerID string) {
	jobID, ok := strings.CutPrefix(containerID, "reactorcide-job-")
	if !ok || jobID == "" {
		return
	}
	if _, had := containerdEgressJobs.LoadAndDelete(jobID); had {
		cr.removeEgress(ctx, jobID)
	}
}
