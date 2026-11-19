package worker

import (
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/resources"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/runshifts"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// applyWorkflowShifts gives a job of a re-run workflow instance the shifts
// of the instance (see internal/runshifts). Only resources and the timeout
// change. The execution profile ceilings are checked after this, as for any
// other job.
func applyWorkflowShifts(job *models.Job, wf *models.WorkflowInstance) error {
	if wf == nil || wf.Shifts == nil || wf.Shifts.IsZero() {
		return nil
	}
	return runshifts.Apply(job, *wf.Shifts)
}

// EnforceProfileLimits refuses a job whose timeout or resources are over the
// ceilings of its execution profile. A retry with shifts uses it, so a shift
// can never pass a ceiling that the original submit had to pass.
func EnforceProfileLimits(job *models.Job, profile *models.ExecutionProfile) error {
	if job == nil || profile == nil {
		return nil
	}
	if profile.TimeoutCeilingSeconds != nil && job.TimeoutSeconds > *profile.TimeoutCeilingSeconds {
		return fmt.Errorf("job timeout exceeds execution profile %q ceiling", profile.Name)
	}
	if err := enforceResourceCeilings(job, profile.ResourceCeilings); err != nil {
		return fmt.Errorf("execution profile %q: %w", profile.Name, err)
	}
	return nil
}

// applyEphemeralStorage sets the ephemeral-storage request and limit of a
// Kubernetes container from the job config. A value that does not parse is
// ignored with a warning, as for CPU and memory.
func applyEphemeralStorage(reqs *corev1.ResourceRequirements, config *JobConfig, logger *logrus.Entry) {
	if config == nil || (config.StorageRequest == "" && config.StorageLimit == "") {
		return
	}
	set := func(list *corev1.ResourceList, value, what string) {
		if value == "" {
			return
		}
		bytes, err := resources.MemoryBytes(value)
		if err != nil {
			logger.WithError(err).Warn("Failed to parse ephemeral storage " + what + ", ignoring")
			return
		}
		if *list == nil {
			*list = corev1.ResourceList{}
		}
		(*list)[corev1.ResourceEphemeralStorage] = *resource.NewQuantity(bytes, resource.BinarySI)
	}
	set(&reqs.Requests, config.StorageRequest, "request")
	set(&reqs.Limits, config.StorageLimit, "limit")
}
