package main

import (
	"strings"
	"testing"
)

// TestHelmWorkerAdmissionDefaultsUnchanged guards the default worker
// rendering: concurrency mode, no admission, location, or kubeconfig
// settings, and the worker.concurrency lease count.
func TestHelmWorkerAdmissionDefaultsUnchanged(t *testing.T) {
	out := helmTemplateFile(t, "templates/deployment-worker.yaml")
	for _, absent := range []string{
		"REACTORCIDE_WORKER_ADMISSION", "REACTORCIDE_WORKER_REGION", "REACTORCIDE_WORKER_ZONE", "REACTORCIDE_WORKER_CLOUD",
		"REACTORCIDE_WORKER_DETECT_TOPOLOGY", "REACTORCIDE_K8S_KUBECONFIG", "REACTORCIDE_K8S_CAPACITY_NODE_SELECTOR", "kubeconfig",
	} {
		if strings.Contains(out, absent) {
			t.Errorf("default worker deployment must not contain %q", absent)
		}
	}
	if !strings.Contains(out, "- name: REACTORCIDE_WORKER_CONCURRENCY\n              value: \"2\"") {
		t.Error("default worker deployment must keep worker.concurrency as the lease count")
	}
}

// TestHelmWorkerResourcesAdmissionAndKubeconfig renders the opt-in values.
func TestHelmWorkerResourcesAdmissionAndKubeconfig(t *testing.T) {
	out := helmTemplateFile(t, "templates/deployment-worker.yaml",
		"--set", "worker.admission.mode=resources",
		"--set", "worker.admission.memory=256Gi",
		"--set", "worker.admission.capacityNodeSelector=pool=ci",
		"--set", "worker.region=us-east-1",
		"--set", "worker.cloud=onprem",
		"--set", "worker.kubeconfig.secretName=remote-kube",
		"--set", "worker.kubeconfig.context=ci",
	)
	for _, want := range []string{
		"- name: REACTORCIDE_WORKER_CONCURRENCY\n              value: \"64\"",
		"- name: REACTORCIDE_WORKER_ADMISSION\n              value: \"resources\"",
		"- name: REACTORCIDE_WORKER_ADMISSION_MEMORY\n              value: \"256Gi\"",
		"- name: REACTORCIDE_K8S_CAPACITY_NODE_SELECTOR\n              value: \"pool=ci\"",
		"- name: REACTORCIDE_WORKER_REGION\n              value: \"us-east-1\"",
		"- name: REACTORCIDE_WORKER_CLOUD\n              value: \"onprem\"",
		"- name: REACTORCIDE_K8S_KUBECONFIG\n              value: \"/etc/reactorcide/kubeconfig/config\"",
		"- name: REACTORCIDE_K8S_CONTEXT\n              value: \"ci\"",
		"secretName: remote-kube",
		"mountPath: /etc/reactorcide/kubeconfig",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("worker deployment is missing %q", want)
		}
	}
	if strings.Contains(out, "REACTORCIDE_WORKER_ADMISSION_CPU") {
		t.Error("an empty CPU budget must not render")
	}
}
