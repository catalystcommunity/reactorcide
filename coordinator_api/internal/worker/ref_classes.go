package worker

import (
	"context"
	"errors"
	"fmt"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/refclass"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

// refClassDecisionReason is the {kind: "ref_class"} reason that every
// decision record of an event gets: the classes of the event and the
// operator rules that added classes. The event is the authority; the eval
// job holds a copy.
func (tp *TriggerProcessor) refClassDecisionReason(ctx context.Context, events store.CIEventStore, eventID string, parentJob *models.Job) models.DecisionReason {
	classes := refclass.ForJob(parentJob)
	ruleIDs := []string{}
	if event, err := events.GetCIEvent(ctx, eventID); err == nil && event != nil {
		classes = refclass.ForEvent(event)
		for _, reason := range event.RefClassReasons {
			if reason.Scope != "builtin" && !containsString(ruleIDs, reason.RuleID) {
				ruleIDs = append(ruleIDs, reason.RuleID)
			}
		}
	}
	return refclass.Reason(classes, ruleIDs)
}

// workerClassStore reads one worker class of an organization.
type workerClassStore interface {
	GetWorkerClass(ctx context.Context, orgID, name string) (*models.WorkerClass, error)
}

// checkWorkerClassRefClasses refuses a worker class whose admission
// conditions the job does not satisfy. See CheckWorkerClassAdmission.
func (tp *TriggerProcessor) checkWorkerClassRefClasses(ctx context.Context, orgID, workerClass string, classes []string) error {
	return CheckWorkerClassAdmission(ctx, tp.store, orgID, workerClass, classes, "", nil)
}

// CheckWorkerClassAdmission refuses a worker class whose conditions the job
// does not satisfy. Every path that puts a job on a worker class runs it:
// workflow nodes, REST submits, and retries.
//
//   - ref_classes_any: the job's ref classes must have one of the listed
//     classes (docs/ref-classes.md).
//   - protected: an untrusted execution profile (one that denies secrets)
//     cannot use the class, unless the profile names the class in
//     allowed_worker_classes. profile is the job's profile when the caller
//     has it; otherwise the check reads profileName ("" is "standard").
//     A profile that cannot be read fails closed.
//
// A worker class with no condition, or a store that cannot read worker
// classes, puts no condition on the job. A worker class that does not exist
// is not this check's concern (the queue routing refuses it). A read error
// fails closed.
func CheckWorkerClassAdmission(ctx context.Context, st interface{}, orgID, workerClass string, classes []string, profileName string, profile *models.ExecutionProfile) error {
	classStore, ok := st.(workerClassStore)
	if !ok || workerClass == "" {
		return nil
	}
	class, err := classStore.GetWorkerClass(ctx, orgID, workerClass)
	if errors.Is(err, store.ErrNotFound) || (err == nil && class == nil) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("worker class %q conditions cannot be verified", workerClass)
	}
	if len(class.RefClassesAny) > 0 && !refclass.Satisfies(classes, class.RefClassesAny, nil) {
		return fmt.Errorf("worker class %q needs one of the ref classes %v; the run has %v", workerClass, []string(class.RefClassesAny), refclass.Normalize(classes))
	}
	if !class.Protected {
		return nil
	}
	if profile == nil {
		if profileName == "" {
			profileName = "standard"
		}
		profileStore, ok := st.(triggerProfileStore)
		if !ok {
			return fmt.Errorf("worker class %q is protected and the execution profile cannot be verified", workerClass)
		}
		loaded, err := profileStore.GetExecutionProfile(ctx, orgID, profileName)
		if err != nil || loaded == nil {
			return fmt.Errorf("worker class %q is protected and the execution profile %q cannot be verified", workerClass, profileName)
		}
		profile = loaded
	}
	if profile.DenySecrets && !containsString(profile.AllowedWorkerClasses, class.Name) {
		return fmt.Errorf("worker class %q is protected; the untrusted execution profile %q cannot use it", workerClass, profile.Name)
	}
	return nil
}
