package worker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/docker/docker/api/types/container"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// Egress control for the Docker and containerd runners
// (docs/egress-proxy.md). The design copies the Magi envd netns idea to a
// container runtime:
//
//  1. The helper container (reactorcide-egress-net serve, uid 65532, no
//     capability) owns the network namespace of the job. Its DNS server is
//     EgressFakeResolverIP, so each DNS query leaves through the redirect.
//  2. A setup container joins that namespace with NET_ADMIN only, installs
//     the redirect and the guard (podnet), and exits. The job starts only
//     after it exited with status 0.
//  3. The job container joins the namespace ("container:<helper>") with no
//     capability and no-new-privileges (hardenDockerEgressJob). It gets the proxy and CA variables, and the CA file at
//     EgressCAFile (a read-only bind mount of a file in the workspace).

// egressComponent labels the helper and setup containers, for the sweep of
// leaked containers.
const egressComponent = "egress-helper"

// EgressHelperName is the name of the helper container of a job.
func EgressHelperName(jobID string) string { return "reactorcide-egress-" + jobID }

func egressSetupName(jobID string) string { return "reactorcide-egress-setup-" + jobID }

// prepareEgressJobConfig checks the egress config, writes the CA file into
// the workspace, and returns a copy of config with the egress variables and
// the CA bind mount. Without egress it returns config.
func prepareEgressJobConfig(config *JobConfig) (*JobConfig, error) {
	if config == nil || config.Egress == nil {
		return config, nil
	}
	if err := config.Egress.Validate(); err != nil {
		return nil, err
	}
	if err := CheckEgressCapabilities(config.Capabilities, config.RunAsUser); err != nil {
		return nil, NewFailureError(models.FailureClassJob, "egress", err)
	}
	if config.WorkspaceDir == "" {
		return nil, fmt.Errorf("egress: the job has no workspace for the CA file")
	}
	dir := filepath.Join(config.WorkspaceDir, ".reactorcide-egress")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("egress: %w", err)
	}
	caFile := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(caFile, []byte(config.Egress.CACertPEM), 0o644); err != nil {
		return nil, fmt.Errorf("egress: write the CA file: %w", err)
	}
	out := WithEgressEnv(config)
	out.ExtraMounts = append(append([]string{}, config.ExtraMounts...), caFile+":"+EgressCAFile+":ro")
	return out, nil
}

func envSlice(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, e := range sortedEnv(m) {
		out = append(out, e.Name+"="+e.Value)
	}
	return out
}

func egressLabels(jobID string) map[string]string {
	return map[string]string{"reactorcide.job_id": jobID, "reactorcide.component": egressComponent}
}

// dockerEgressHelperSpec is the helper container of a job.
func dockerEgressHelperSpec(config *JobConfig) (*container.Config, *container.HostConfig) {
	e := config.Egress
	args := e.ServeArgs()
	helperEnv := e.HelperEnv()
	delete(helperEnv, "REACTORCIDE_EGRESS_CA_PEM")
	cfg := &container.Config{
		Image:      e.NetImage,
		Entrypoint: args[:1],
		Cmd:        args[1:],
		Env:        envSlice(helperEnv),
		User:       "65532:65532",
		Labels:     egressLabels(config.JobID),
	}
	network := e.Network
	if network == "" {
		network = "bridge"
	}
	host := &container.HostConfig{
		NetworkMode:    container.NetworkMode(network),
		DNS:            []string{EgressFakeResolverIP},
		CapDrop:        []string{"ALL"},
		SecurityOpt:    []string{"no-new-privileges"},
		ReadonlyRootfs: true,
	}
	return cfg, host
}

// dockerEgressSetupSpec is the setup container that installs the redirect
// in the namespace of the helper.
func dockerEgressSetupSpec(config *JobConfig, helper string) (*container.Config, *container.HostConfig) {
	args := config.Egress.PodnetArgs("")
	cfg := &container.Config{
		Image:      config.Egress.NetImage,
		Entrypoint: args[:1],
		Cmd:        args[1:],
		User:       "0:0",
		Labels:     egressLabels(config.JobID),
	}
	host := &container.HostConfig{
		NetworkMode:    container.NetworkMode("container:" + helper),
		CapDrop:        []string{"ALL"},
		CapAdd:         []string{"NET_ADMIN"},
		SecurityOpt:    []string{"no-new-privileges"},
		ReadonlyRootfs: true,
	}
	return cfg, host
}

// hardenDockerEgressJob removes every capability of the job container and
// sets no-new-privileges. A capability such as CAP_NET_RAW (in the default
// set of Docker) or root through sudo or a file capability could send
// AF_PACKET frames, which nftables never sees. Only for jobs with egress.
func hardenDockerEgressJob(host *container.HostConfig) {
	host.Privileged = false
	host.CapAdd = nil
	host.CapDrop = []string{"ALL"}
	host.SecurityOpt = append(host.SecurityOpt, "no-new-privileges")
}

// prepareEgress starts the egress network of a Docker job. It returns the
// job config to use and the network mode of the job container ("" without
// egress).
func (dr *DockerRunner) prepareEgress(ctx context.Context, config *JobConfig) (*JobConfig, string, error) {
	out, err := prepareEgressJobConfig(config)
	if err != nil || config.Egress == nil {
		return out, "", err
	}
	logger := logging.Log.WithField("job_id", config.JobID)
	if err := dr.ensureImage(ctx, config.Egress.NetImage); err != nil {
		return nil, "", NewFailureError(models.FailureClassImagePull, "image pull", fmt.Errorf("egress image: %w", err))
	}
	helper := EgressHelperName(config.JobID)
	hc, hh := dockerEgressHelperSpec(config)
	resp, err := dr.client.ContainerCreate(ctx, hc, hh, nil, nil, helper)
	if err != nil {
		return nil, "", fmt.Errorf("egress: create helper: %w", err)
	}
	if err := dr.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		dr.removeEgress(context.Background(), config.JobID)
		return nil, "", fmt.Errorf("egress: start helper: %w", err)
	}
	sc, sh := dockerEgressSetupSpec(config, helper)
	setup, err := dr.client.ContainerCreate(ctx, sc, sh, nil, nil, egressSetupName(config.JobID))
	if err != nil {
		dr.removeEgress(context.Background(), config.JobID)
		return nil, "", fmt.Errorf("egress: create setup container: %w", err)
	}
	defer dr.client.ContainerRemove(context.Background(), setup.ID, container.RemoveOptions{Force: true})
	waitCh, errCh := dr.client.ContainerWait(ctx, setup.ID, container.WaitConditionNextExit)
	if err := dr.client.ContainerStart(ctx, setup.ID, container.StartOptions{}); err != nil {
		dr.removeEgress(context.Background(), config.JobID)
		return nil, "", fmt.Errorf("egress: start setup container: %w", err)
	}
	select {
	case w := <-waitCh:
		if w.StatusCode != 0 {
			dr.removeEgress(context.Background(), config.JobID)
			return nil, "", fmt.Errorf("egress: the redirect setup exited with status %d", w.StatusCode)
		}
	case err := <-errCh:
		dr.removeEgress(context.Background(), config.JobID)
		return nil, "", fmt.Errorf("egress: wait for the redirect setup: %w", err)
	case <-ctx.Done():
		dr.removeEgress(context.Background(), config.JobID)
		return nil, "", ctx.Err()
	}
	logger.WithField("helper", helper).Info("Egress network ready")
	return out, "container:" + helper, nil
}

// removeEgress removes the egress containers of a job (no error when they
// are not there).
func (dr *DockerRunner) removeEgress(ctx context.Context, jobID string) {
	for _, name := range []string{egressSetupName(jobID), EgressHelperName(jobID)} {
		_ = dr.client.ContainerRemove(ctx, name, container.RemoveOptions{Force: true})
	}
}

// egressCleanupFor returns the cleanup of the egress containers of a job
// container, found by its job label. Call it before the job container is
// removed, and run the result after.
func (dr *DockerRunner) egressCleanupFor(ctx context.Context, containerID string) func() {
	info, err := dr.client.ContainerInspect(ctx, containerID)
	if err != nil || info.ContainerJSONBase == nil || info.HostConfig == nil || info.Config == nil || !info.HostConfig.NetworkMode.IsContainer() {
		return func() {}
	}
	// Docker stores "container:<id>", not the helper name, so the job label
	// selects the helper. Removing a name that does not exist (a builder
	// sidecar job) does nothing.
	jobID := info.Config.Labels["reactorcide.job_id"]
	if jobID == "" {
		return func() {}
	}
	return func() { dr.removeEgress(ctx, jobID) }
}
