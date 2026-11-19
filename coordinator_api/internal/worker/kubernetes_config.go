package worker

import (
	"errors"
	"fmt"
	"strings"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// kubernetesClusterConfig is the API server connection of the Kubernetes
// runner.
type kubernetesClusterConfig struct {
	rest *rest.Config
	// namespace is the namespace of the kubeconfig context, or "" when the
	// context names none or the runner uses the in-cluster config.
	namespace string
	// inCluster is true when the config came from the Pod service account.
	inCluster bool
}

// loadKubernetesClusterConfig returns the API server connection.
//
// With an empty kubeconfig path, the runner uses the in-cluster service
// account config (the behavior before kubeconfig support). With a path, the
// runner reads that kubeconfig file and uses kubeContext, or the file's
// current context when kubeContext is empty. One worker host can then drive
// a remote cluster. All runner calls (Jobs, Pods, logs, exec, events, nodes,
// and the kubelet stats proxy used for metrics) use this one config.
func loadKubernetesClusterConfig(kubeconfig, kubeContext string) (kubernetesClusterConfig, error) {
	kubeconfig = strings.TrimSpace(kubeconfig)
	kubeContext = strings.TrimSpace(kubeContext)
	if kubeconfig == "" {
		if kubeContext != "" {
			return kubernetesClusterConfig{}, errors.New("REACTORCIDE_K8S_CONTEXT needs REACTORCIDE_K8S_KUBECONFIG")
		}
		config, err := rest.InClusterConfig()
		if err != nil {
			return kubernetesClusterConfig{}, fmt.Errorf("failed to get in-cluster config (is this running in Kubernetes? set REACTORCIDE_K8S_KUBECONFIG for a remote cluster): %w", err)
		}
		return kubernetesClusterConfig{rest: config, inCluster: true}, nil
	}

	rules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: kubeContext}
	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)
	config, err := clientConfig.ClientConfig()
	if err != nil {
		// The error text names the file and context, never credential values.
		return kubernetesClusterConfig{}, fmt.Errorf("failed to load kubeconfig %q: %w", kubeconfig, err)
	}
	namespace, _, err := clientConfig.Namespace()
	if err != nil {
		return kubernetesClusterConfig{}, fmt.Errorf("failed to read the namespace of kubeconfig %q: %w", kubeconfig, err)
	}
	return kubernetesClusterConfig{rest: config, namespace: namespace}, nil
}
