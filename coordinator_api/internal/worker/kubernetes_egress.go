package worker

import (
	"fmt"
	"sort"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// Names of the egress parts of a job pod.
const (
	kubernetesEgressNetInit = "egress-net"
	kubernetesEgressSidecar = "egress-forward"
	kubernetesEgressVolume  = "egress-ca"
	// EgressPodLabel marks a job pod with egress control. A NetworkPolicy
	// of the chart can select it (helm_chart egressProxy.networkPolicy).
	EgressPodLabel = "reactorcide.io/egress"
)

// prepareKubernetesEgress checks the egress config of a job and adds the
// egress plumbing to its pod. Without egress it does nothing.
func prepareKubernetesEgress(spec *corev1.PodSpec, config *JobConfig) error {
	if config == nil || config.Egress == nil {
		return nil
	}
	if err := config.Egress.Validate(); err != nil {
		return err
	}
	if err := CheckEgressCapabilities(config.Capabilities, config.RunAsUser); err != nil {
		return NewFailureError(models.FailureClassJob, "egress", err)
	}
	if len(spec.Containers) == 0 {
		return fmt.Errorf("egress: the pod has no job container")
	}
	applyKubernetesEgress(spec, config.Egress)
	return nil
}

// markKubernetesEgress labels the pod of a job with egress control, so that
// a NetworkPolicy can select it.
func markKubernetesEgress(job *batchv1.Job, config *JobConfig) {
	if job == nil || config == nil || config.Egress == nil {
		return
	}
	if job.Spec.Template.Labels == nil {
		job.Spec.Template.Labels = map[string]string{}
	}
	job.Spec.Template.Labels[EgressPodLabel] = "proxy"
}

// applyKubernetesEgress adds the egress plumbing to a job pod
// (docs/egress-proxy.md). It is a copy of the Magi envd-net design:
//
//   - Init container egress-net (root, all capabilities dropped except
//     NET_ADMIN) runs "reactorcide-egress-net podnet": the nat redirect, the
//     guard filter, and the CA file in a shared emptyDir. It is first, so no
//     other container has network before the redirect is in place.
//   - Native sidecar egress-forward (non-root, no capabilities) runs
//     "serve": the local proxy port 127.0.0.1:3128, the transparent route
//     (port 15001), and the fake DNS (port 15053). A startup probe holds the
//     job container until the proxy port answers.
//   - The job container gets the proxy and CA variables (JobEnv) and the CA
//     file at EgressCAFile. It has no capability (all dropped), no privilege
//     escalation, and the RuntimeDefault seccomp profile, so it cannot
//     change the redirect or send raw packets (hardenKubernetesEgressJob).
func applyKubernetesEgress(spec *corev1.PodSpec, cfg *EgressConfig) {
	if spec == nil || cfg == nil || len(spec.Containers) == 0 {
		return
	}
	caMount := corev1.VolumeMount{Name: kubernetesEgressVolume, MountPath: EgressCADir}
	helperEnv := sortedEnv(cfg.HelperEnv())

	netInit := corev1.Container{
		Name:            kubernetesEgressNetInit,
		Image:           cfg.NetImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         cfg.PodnetArgs(EgressCADir),
		Env:             filterEnv(helperEnv, "REACTORCIDE_EGRESS_CA_PEM"),
		SecurityContext: &corev1.SecurityContext{
			RunAsUser:                int64Ptr(0),
			RunAsNonRoot:             boolPtr(false),
			AllowPrivilegeEscalation: boolPtr(false),
			ReadOnlyRootFilesystem:   boolPtr(true),
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
				Add:  []corev1.Capability{"NET_ADMIN"},
			},
		},
		VolumeMounts: []corev1.VolumeMount{caMount},
	}

	always := corev1.ContainerRestartPolicyAlways
	sidecar := corev1.Container{
		Name:            kubernetesEgressSidecar,
		Image:           cfg.NetImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         cfg.ServeArgs(),
		Env:             filterEnv(helperEnv, "REACTORCIDE_EGRESS_PROXY_USER", "REACTORCIDE_EGRESS_PROXY_PASSWORD"),
		RestartPolicy:   &always,
		SecurityContext: &corev1.SecurityContext{
			RunAsUser:                int64Ptr(65532),
			RunAsGroup:               int64Ptr(65532),
			RunAsNonRoot:             boolPtr(true),
			AllowPrivilegeEscalation: boolPtr(false),
			ReadOnlyRootFilesystem:   boolPtr(true),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		},
		StartupProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				Exec: &corev1.ExecAction{Command: []string{EgressNetBinary, "check", "-addr", EgressLocalProxyAddr}},
			},
			PeriodSeconds:    1,
			FailureThreshold: 30,
		},
	}

	spec.InitContainers = append([]corev1.Container{netInit, sidecar}, spec.InitContainers...)
	spec.Volumes = append(spec.Volumes, corev1.Volume{
		Name:         kubernetesEgressVolume,
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	})

	job := &spec.Containers[0]
	jobEnv := cfg.JobEnv()
	kept := job.Env[:0:0]
	for _, e := range job.Env {
		if _, replaced := jobEnv[e.Name]; !replaced {
			kept = append(kept, e)
		}
	}
	job.Env = append(kept, sortedEnv(jobEnv)...)
	hardenKubernetesEgressJob(job)
	readOnly := caMount
	readOnly.ReadOnly = true
	job.VolumeMounts = append(job.VolumeMounts, readOnly)
}

// hardenKubernetesEgressJob drops every capability of the job container,
// turns off privilege escalation (setuid sudo and file capabilities do not
// work), and sets the RuntimeDefault seccomp profile. A capability such as
// CAP_NET_RAW (in the default set of most runtimes) could send AF_PACKET
// frames, which nftables never sees. The run-as user stays as it is; root is
// refused before (CheckEgressCapabilities).
func hardenKubernetesEgressJob(c *corev1.Container) {
	if c.SecurityContext == nil {
		c.SecurityContext = &corev1.SecurityContext{}
	}
	sc := c.SecurityContext
	sc.Privileged = boolPtr(false)
	sc.AllowPrivilegeEscalation = boolPtr(false)
	sc.Capabilities = &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}
	sc.SeccompProfile = &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}
	if sc.RunAsUser != nil && *sc.RunAsUser != 0 {
		sc.RunAsNonRoot = boolPtr(true)
	}
}

func sortedEnv(m map[string]string) []corev1.EnvVar {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]corev1.EnvVar, 0, len(keys))
	for _, k := range keys {
		out = append(out, corev1.EnvVar{Name: k, Value: m[k]})
	}
	return out
}

func filterEnv(env []corev1.EnvVar, names ...string) []corev1.EnvVar {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	var out []corev1.EnvVar
	for _, e := range env {
		if want[e.Name] {
			out = append(out, e)
		}
	}
	return out
}
