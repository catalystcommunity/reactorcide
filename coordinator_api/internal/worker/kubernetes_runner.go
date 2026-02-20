package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	"github.com/google/uuid"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// KubernetesRunner implements JobRunner using Kubernetes Jobs
// It creates K8s Job resources and streams logs directly via the K8s API
// to ensure secret masking is applied before logs reach any aggregator.
type KubernetesRunner struct {
	clientset        *kubernetes.Clientset
	namespace        string
	serviceAccount   string
	runnerImage      string
	imagePullSecrets []string
}

// KubernetesRunnerConfig holds configuration for the K8s runner
type KubernetesRunnerConfig struct {
	Namespace        string   // Namespace for job pods (default: current namespace)
	ServiceAccount   string   // Service account for job pods (default: "default")
	RunnerImage      string   // Default runner image if job doesn't specify one
	ImagePullSecrets []string // Image pull secrets for private registries
	NodeName         string   // Node to schedule job pods on (for workspace sharing via HostPath)
}

// NewKubernetesRunner creates a new Kubernetes-based job runner
// It automatically detects if running in-cluster and uses the appropriate config
func NewKubernetesRunner() (*KubernetesRunner, error) {
	return NewKubernetesRunnerWithConfig(KubernetesRunnerConfig{})
}

// NewKubernetesRunnerWithConfig creates a KubernetesRunner with custom configuration
func NewKubernetesRunnerWithConfig(cfg KubernetesRunnerConfig) (*KubernetesRunner, error) {
	// Try in-cluster config first
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get in-cluster config (is this running in Kubernetes?): %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	// Determine namespace
	namespace := cfg.Namespace
	if namespace == "" {
		// Try to get namespace from service account
		nsBytes, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
		if err != nil {
			namespace = "default"
		} else {
			namespace = strings.TrimSpace(string(nsBytes))
		}
	}

	serviceAccount := cfg.ServiceAccount
	if serviceAccount == "" {
		serviceAccount = "default"
	}

	return &KubernetesRunner{
		clientset:        clientset,
		namespace:        namespace,
		serviceAccount:   serviceAccount,
		runnerImage:      cfg.RunnerImage,
		imagePullSecrets: cfg.ImagePullSecrets,
	}, nil
}

// SpawnJob creates and starts a Kubernetes Job resource
func (kr *KubernetesRunner) SpawnJob(ctx context.Context, config *JobConfig) (string, error) {
	logger := logging.Log.WithField("job_id", config.JobID)

	// Validate configuration
	if err := kr.validateConfig(config); err != nil {
		return "", fmt.Errorf("invalid job configuration: %w", err)
	}

	// Generate unique job name
	jobName := fmt.Sprintf("reactorcide-job-%s-%s", config.JobID, uuid.New().String()[:8])

	// NOTE: We intentionally do NOT set WorkingDir on the k8s container spec.
	// The container runtime creates it as root before the process starts,
	// making it unwritable by non-root users when it's a subdirectory of an
	// emptyDir volume (e.g. /job/src). Job commands handle their own cd.

	// Build environment variables
	envVars := make([]corev1.EnvVar, 0, len(config.Env)+4)
	// Set HOME to a writable directory so tools (git, go, etc.) work under UID 1001
	envVars = append(envVars, corev1.EnvVar{
		Name:  "HOME",
		Value: "/home/reactorcide",
	})
	// Mark all directories as git safe.directory so git works when emptyDir
	// mount points are root-owned but the process runs as UID 1001
	envVars = append(envVars,
		corev1.EnvVar{Name: "GIT_CONFIG_COUNT", Value: "1"},
		corev1.EnvVar{Name: "GIT_CONFIG_KEY_0", Value: "safe.directory"},
		corev1.EnvVar{Name: "GIT_CONFIG_VALUE_0", Value: "*"},
	)
	for key, value := range config.Env {
		envVars = append(envVars, corev1.EnvVar{
			Name:  key,
			Value: value,
		})
	}

	// Build resource requirements
	resources := corev1.ResourceRequirements{}
	if config.CPULimit != "" || config.MemoryLimit != "" {
		resources.Limits = corev1.ResourceList{}
		resources.Requests = corev1.ResourceList{}

		if config.CPULimit != "" {
			cpuQuantity, err := resource.ParseQuantity(config.CPULimit)
			if err != nil {
				logger.WithError(err).Warn("Failed to parse CPU limit, ignoring")
			} else {
				resources.Limits[corev1.ResourceCPU] = cpuQuantity
				resources.Requests[corev1.ResourceCPU] = cpuQuantity
			}
		}

		if config.MemoryLimit != "" {
			memQuantity, err := resource.ParseQuantity(config.MemoryLimit)
			if err != nil {
				logger.WithError(err).Warn("Failed to parse memory limit, ignoring")
			} else {
				resources.Limits[corev1.ResourceMemory] = memQuantity
				resources.Requests[corev1.ResourceMemory] = memQuantity
			}
		}
	}

	// Determine if we need root/privileged (for docker capability)
	runAsNonRoot := true
	var runAsUser *int64
	var privileged *bool
	userID := int64(1001)
	runAsUser = &userID

	for _, cap := range config.Capabilities {
		if cap == CapabilityDocker {
			runAsNonRoot = false
			runAsUser = nil
			priv := true
			privileged = &priv
			logger.Info("Docker capability requested: running as root with privileged mode")
			break
		}
	}

	// Build pod spec
	podSpec := corev1.PodSpec{
		RestartPolicy:      corev1.RestartPolicyNever,
		ServiceAccountName: kr.serviceAccount,
		SecurityContext: &corev1.PodSecurityContext{
			RunAsNonRoot: &runAsNonRoot,
		},
		Containers: []corev1.Container{
			{
				Name:            "job",
				Image:           config.Image,
				ImagePullPolicy: corev1.PullAlways,
				Command:         config.Command,
				Env:             envVars,
				Resources:       resources,
				SecurityContext: &corev1.SecurityContext{
					RunAsUser:  runAsUser,
					Privileged: privileged,
				},
				VolumeMounts: []corev1.VolumeMount{
					{
						Name:      "job",
						MountPath: "/job",
					},
					{
						Name:      "workspace",
						MountPath: "/workspace",
					},
					{
						Name:      "home",
						MountPath: "/home/reactorcide",
					},
				},
			},
		},
		Volumes: []corev1.Volume{
			{
				Name: "job",
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			},
			{
				Name: "workspace",
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			},
			{
				Name: "home",
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			},
		},
	}

	// Add image pull secrets if configured
	for _, secret := range kr.imagePullSecrets {
		podSpec.ImagePullSecrets = append(podSpec.ImagePullSecrets, corev1.LocalObjectReference{
			Name: secret,
		})
	}

	// Handle GPU capability
	for _, cap := range config.Capabilities {
		if cap == CapabilityGPU {
			if podSpec.Containers[0].Resources.Limits == nil {
				podSpec.Containers[0].Resources.Limits = corev1.ResourceList{}
			}
			podSpec.Containers[0].Resources.Limits["nvidia.com/gpu"] = resource.MustParse("1")
			logger.Info("GPU capability enabled: requesting nvidia.com/gpu=1")
		}
	}

	// TTL for automatic cleanup (1 hour after completion)
	ttlSeconds := int32(3600)

	// Create the Job resource
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: kr.namespace,
			Labels: map[string]string{
				"reactorcide.io/job-id":    config.JobID,
				"reactorcide.io/queue":     config.QueueName,
				"reactorcide.io/component": "job",
				// Label for log collector exclusion - users can configure their
				// collectors to exclude pods with this label
				"reactorcide.io/log-exclude": "true",
			},
			Annotations: map[string]string{
				// Annotations to exclude from common log collectors
				// Logs are streamed directly via K8s API for secret masking

				// Fluent Bit exclusion
				"fluentbit.io/exclude": "true",

				// Fluentd exclusion
				"fluentd.kubernetes.io/exclude": "true",

				// Promtail/Loki exclusion
				"promtail.io/ignore": "true",

				// Filebeat exclusion (Elastic)
				"co.elastic.logs/enabled": "false",

				// Vector exclusion
				"vector.dev/exclude": "true",

				// Datadog - disable log collection for this pod
				// Users running Datadog should also configure their agent to
				// respect the reactorcide.io/log-exclude label
				"ad.datadoghq.com/job.logs": `[{"source": "reactorcide", "service": "reactorcide-job"}]`,

				// Store job metadata for reference
				"reactorcide.io/original-job-id": config.JobID,
			},
		},
		Spec: batchv1.JobSpec{
			TTLSecondsAfterFinished: &ttlSeconds,
			BackoffLimit:            int32Ptr(0), // No retries - we handle retries at a higher level
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"reactorcide.io/job-id":      config.JobID,
						"reactorcide.io/job-name":    jobName,
						"reactorcide.io/log-exclude": "true",
					},
					Annotations: map[string]string{
						// Same log exclusion annotations on pod template
						"fluentbit.io/exclude":              "true",
						"fluentd.kubernetes.io/exclude":     "true",
						"promtail.io/ignore":                "true",
						"co.elastic.logs/enabled":           "false",
						"vector.dev/exclude":                "true",
						"reactorcide.io/original-job-id":    config.JobID,
					},
				},
				Spec: podSpec,
			},
		},
	}

	logger.WithFields(map[string]interface{}{
		"job_name":  jobName,
		"namespace": kr.namespace,
		"image":     config.Image,
		"command":   config.Command,
	}).Info("Creating Kubernetes Job")

	createdJob, err := kr.clientset.BatchV1().Jobs(kr.namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to create Kubernetes Job: %w", err)
	}

	logger.WithField("job_name", createdJob.Name).Info("Kubernetes Job created successfully")
	return createdJob.Name, nil
}

// StreamLogs streams stdout and stderr from the job pod
// Logs are streamed directly via K8s API, bypassing any file-based log collectors
func (kr *KubernetesRunner) StreamLogs(ctx context.Context, jobName string) (stdout io.ReadCloser, stderr io.ReadCloser, err error) {
	logger := logging.Log.WithField("job_name", jobName)

	// Wait for pod to be created and get its name
	podName, err := kr.waitForPod(ctx, jobName)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get pod for job: %w", err)
	}

	logger.WithField("pod_name", podName).Info("Streaming logs from pod")

	// Get logs from the pod
	// K8s API returns a single stream with both stdout and stderr combined
	// We'll return the same stream for both since K8s doesn't separate them
	logOpts := &corev1.PodLogOptions{
		Container:  "job",
		Follow:     true,
		Timestamps: false,
	}

	req := kr.clientset.CoreV1().Pods(kr.namespace).GetLogs(podName, logOpts)
	logStream, err := req.Stream(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to stream pod logs: %w", err)
	}

	// K8s combines stdout/stderr into a single stream
	// Create a pipe to allow reading while the stream is still open
	stdoutReader, stdoutWriter := io.Pipe()

	// Empty stderr reader (K8s doesn't separate streams)
	stderrReader := io.NopCloser(bytes.NewReader(nil))

	go func() {
		defer logStream.Close()
		defer stdoutWriter.Close()

		_, err := io.Copy(stdoutWriter, logStream)
		if err != nil && err != io.EOF {
			logger.WithError(err).Error("Error copying pod logs")
		}
	}()

	return stdoutReader, stderrReader, nil
}

// WaitForCompletion waits for the Kubernetes Job to complete and returns the exit code
func (kr *KubernetesRunner) WaitForCompletion(ctx context.Context, jobName string) (int, error) {
	logger := logging.Log.WithField("job_name", jobName)

	// Watch for job completion
	watcher, err := kr.clientset.BatchV1().Jobs(kr.namespace).Watch(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("metadata.name=%s", jobName),
	})
	if err != nil {
		return -1, fmt.Errorf("failed to watch job: %w", err)
	}
	defer watcher.Stop()

	for event := range watcher.ResultChan() {
		if event.Type == watch.Error {
			return -1, fmt.Errorf("watch error: %v", event.Object)
		}

		job, ok := event.Object.(*batchv1.Job)
		if !ok {
			continue
		}

		// Check if job completed
		for _, condition := range job.Status.Conditions {
			if condition.Type == batchv1.JobComplete && condition.Status == corev1.ConditionTrue {
				logger.Info("Job completed successfully")
				return 0, nil
			}
			if condition.Type == batchv1.JobFailed && condition.Status == corev1.ConditionTrue {
				// Get exit code from pod
				exitCode, err := kr.getPodExitCode(ctx, jobName)
				if err != nil {
					logger.WithError(err).Warn("Failed to get pod exit code, returning -1")
					return -1, nil
				}
				logger.WithField("exit_code", exitCode).Info("Job failed")
				return exitCode, nil
			}
		}
	}

	return -1, fmt.Errorf("watch ended unexpectedly")
}

// Cleanup removes the Kubernetes Job resource
func (kr *KubernetesRunner) Cleanup(ctx context.Context, jobName string) error {
	logger := logging.Log.WithField("job_name", jobName)

	logger.Info("Cleaning up Kubernetes Job")

	// Delete the job with propagation policy to delete pods
	propagationPolicy := metav1.DeletePropagationBackground
	deleteOptions := metav1.DeleteOptions{
		PropagationPolicy: &propagationPolicy,
	}

	err := kr.clientset.BatchV1().Jobs(kr.namespace).Delete(ctx, jobName, deleteOptions)
	if err != nil {
		return fmt.Errorf("failed to delete job: %w", err)
	}

	logger.Info("Kubernetes Job cleaned up successfully")
	return nil
}

// validateConfig validates the job configuration
func (kr *KubernetesRunner) validateConfig(config *JobConfig) error {
	if config.Image == "" {
		return fmt.Errorf("container image is required")
	}
	if len(config.Command) == 0 {
		return fmt.Errorf("command is required")
	}
	if config.JobID == "" {
		return fmt.Errorf("job ID is required")
	}
	return nil
}

// PodStartupError represents a pod that failed to start
type PodStartupError struct {
	Reason  string
	Message string
}

func (e *PodStartupError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("pod failed to start: %s - %s", e.Reason, e.Message)
	}
	return fmt.Sprintf("pod failed to start: %s", e.Reason)
}

// IsPodStartupError checks if an error is a pod startup failure
// Uses errors.As to handle wrapped errors
func IsPodStartupError(err error) bool {
	var podErr *PodStartupError
	return errors.As(err, &podErr)
}

// waitForPod waits for a pod to be created for the job and returns its name
// It also detects pod startup failures (e.g., ImagePullBackOff) and returns early with an error
func (kr *KubernetesRunner) waitForPod(ctx context.Context, jobName string) (string, error) {
	logger := logging.Log.WithField("job_name", jobName)

	// Poll for pod creation
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	timeout := time.After(5 * time.Minute)

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-timeout:
			// Before timing out, check if there's a pod with startup failure
			pods, err := kr.clientset.CoreV1().Pods(kr.namespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("reactorcide.io/job-name=%s", jobName),
			})
			if err == nil && len(pods.Items) > 0 {
				if reason, message := kr.checkPodStartupFailure(&pods.Items[0]); reason != "" {
					return "", &PodStartupError{Reason: reason, Message: message}
				}
			}
			return "", fmt.Errorf("timeout waiting for pod to be created")
		case <-ticker.C:
			pods, err := kr.clientset.CoreV1().Pods(kr.namespace).List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("reactorcide.io/job-name=%s", jobName),
			})
			if err != nil {
				continue
			}

			if len(pods.Items) > 0 {
				pod := &pods.Items[0]

				// Check for startup failures first (ImagePullBackOff, etc.)
				if reason, message := kr.checkPodStartupFailure(pod); reason != "" {
					logger.WithFields(map[string]interface{}{
						"pod_name": pod.Name,
						"reason":   reason,
						"message":  message,
					}).Error("Pod startup failure detected")
					return "", &PodStartupError{Reason: reason, Message: message}
				}

				// Wait for pod to be running or completed
				if pod.Status.Phase == corev1.PodRunning ||
					pod.Status.Phase == corev1.PodSucceeded ||
					pod.Status.Phase == corev1.PodFailed {
					return pod.Name, nil
				}
				// Also accept if container is waiting but pod exists
				if pod.Status.Phase == corev1.PodPending {
					// Check if any container has started
					for _, status := range pod.Status.ContainerStatuses {
						if status.State.Running != nil || status.State.Terminated != nil {
							return pod.Name, nil
						}
					}
				}
			}
		}
	}
}

// checkPodStartupFailure checks if the pod has a startup failure condition
// Returns the reason and message if a failure is detected, empty strings otherwise
func (kr *KubernetesRunner) checkPodStartupFailure(pod *corev1.Pod) (reason, message string) {
	// Check container statuses for waiting states that indicate failures
	for _, status := range pod.Status.ContainerStatuses {
		if status.State.Waiting != nil {
			waiting := status.State.Waiting
			switch waiting.Reason {
			case "ImagePullBackOff", "ErrImagePull", "ImageInspectError", "ErrImageNeverPull":
				// Image-related failures - these won't recover without user intervention
				return waiting.Reason, waiting.Message
			case "CrashLoopBackOff":
				// Container is crashing repeatedly
				return waiting.Reason, waiting.Message
			case "CreateContainerConfigError", "CreateContainerError":
				// Container configuration issues
				return waiting.Reason, waiting.Message
			case "InvalidImageName":
				// Invalid image name
				return waiting.Reason, waiting.Message
			case "RunContainerError":
				// Failed to run the container
				return waiting.Reason, waiting.Message
			}
		}
	}

	// Check init container statuses as well
	for _, status := range pod.Status.InitContainerStatuses {
		if status.State.Waiting != nil {
			waiting := status.State.Waiting
			switch waiting.Reason {
			case "ImagePullBackOff", "ErrImagePull", "ImageInspectError", "ErrImageNeverPull":
				return waiting.Reason, waiting.Message
			case "CrashLoopBackOff":
				return waiting.Reason, waiting.Message
			case "CreateContainerConfigError", "CreateContainerError":
				return waiting.Reason, waiting.Message
			case "InvalidImageName":
				return waiting.Reason, waiting.Message
			case "RunContainerError":
				return waiting.Reason, waiting.Message
			}
		}
	}

	// Check pod conditions for scheduling failures
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionFalse {
			// Pod can't be scheduled - check the reason
			switch condition.Reason {
			case "Unschedulable":
				// Node affinity, resource constraints, etc.
				return condition.Reason, condition.Message
			}
		}
	}

	return "", ""
}

// getPodExitCode gets the exit code from the job's pod
func (kr *KubernetesRunner) getPodExitCode(ctx context.Context, jobName string) (int, error) {
	pods, err := kr.clientset.CoreV1().Pods(kr.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("reactorcide.io/job-name=%s", jobName),
	})
	if err != nil {
		return -1, err
	}

	if len(pods.Items) == 0 {
		return -1, fmt.Errorf("no pods found for job")
	}

	pod := &pods.Items[0]
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == "job" && status.State.Terminated != nil {
			return int(status.State.Terminated.ExitCode), nil
		}
	}

	return -1, fmt.Errorf("container exit code not available")
}

// IsKubernetesEnvironment checks if the code is running inside a Kubernetes cluster
func IsKubernetesEnvironment() bool {
	// Check for in-cluster service account token
	_, err := os.Stat("/var/run/secrets/kubernetes.io/serviceaccount/token")
	return err == nil
}

// Helper function for int32 pointers
func int32Ptr(i int32) *int32 {
	return &i
}

// envMapToSlice converts an environment variable map to K8s EnvVar slice
func envMapToK8sEnvVars(envMap map[string]string) []corev1.EnvVar {
	if envMap == nil {
		return nil
	}

	envVars := make([]corev1.EnvVar, 0, len(envMap))
	for key, value := range envMap {
		envVars = append(envVars, corev1.EnvVar{
			Name:  key,
			Value: value,
		})
	}
	return envVars
}

// parseCPULimit parses a CPU limit string (e.g., "1.0") to Kubernetes format
func parseCPULimit(cpuStr string) (string, error) {
	f, err := strconv.ParseFloat(cpuStr, 64)
	if err != nil {
		return "", err
	}
	// Convert to millicores
	millicores := int64(f * 1000)
	return fmt.Sprintf("%dm", millicores), nil
}

// GetJobStatus returns the current status of a Kubernetes job
// Returns: running, succeeded, failed, or pending
// Also returns an error message if the job failed
func (kr *KubernetesRunner) GetJobStatus(ctx context.Context, jobName string) (status string, failureReason string, err error) {
	logger := logging.Log.WithField("job_name", jobName)

	// Get the job
	job, err := kr.clientset.BatchV1().Jobs(kr.namespace).Get(ctx, jobName, metav1.GetOptions{})
	if err != nil {
		return "", "", fmt.Errorf("failed to get job: %w", err)
	}

	// Check job conditions
	for _, condition := range job.Status.Conditions {
		if condition.Type == batchv1.JobComplete && condition.Status == corev1.ConditionTrue {
			return "succeeded", "", nil
		}
		if condition.Type == batchv1.JobFailed && condition.Status == corev1.ConditionTrue {
			return "failed", condition.Message, nil
		}
	}

	// Check pod status for more details
	pods, err := kr.clientset.CoreV1().Pods(kr.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("reactorcide.io/job-name=%s", jobName),
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to get pods for job")
		return "pending", "", nil
	}

	if len(pods.Items) == 0 {
		return "pending", "", nil
	}

	pod := &pods.Items[0]

	// Check for pod startup failures
	if reason, message := kr.checkPodStartupFailure(pod); reason != "" {
		return "failed", fmt.Sprintf("%s: %s", reason, message), nil
	}

	// Check pod phase
	switch pod.Status.Phase {
	case corev1.PodRunning:
		return "running", "", nil
	case corev1.PodSucceeded:
		return "succeeded", "", nil
	case corev1.PodFailed:
		// Get failure reason from container status
		for _, status := range pod.Status.ContainerStatuses {
			if status.State.Terminated != nil && status.State.Terminated.ExitCode != 0 {
				return "failed", status.State.Terminated.Reason, nil
			}
		}
		return "failed", "pod failed", nil
	case corev1.PodPending:
		return "pending", "", nil
	default:
		return "pending", "", nil
	}
}

// Ensure KubernetesRunner implements JobRunner interface
var _ JobRunner = (*KubernetesRunner)(nil)
