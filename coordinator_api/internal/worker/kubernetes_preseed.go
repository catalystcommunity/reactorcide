package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/catalystcommunity/app-utils-go/logging"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/validation"
)

// Kubernetes image pre-seed (VM_IMAGES_PLAN.md "Kubernetes pre-seed",
// owner decision I13). The worker keeps one DaemonSet per image set in the job
// namespace. Each container image of the worker image set is an init
// container of the DaemonSet pod, so the kubelet of each node pulls it.
// The init containers run a static no-op binary from a shared emptyDir,
// so an image with no shell (or no binaries) works too. The main container
// is a pause container. The DaemonSet name has a hash of the image set.
// The set is ready when the DaemonSet of that hash has every pod ready at
// its current generation, and at least one pod.
//
// Limit: a node that joins the cluster later gets the images when its
// DaemonSet pod starts. A job can get that node before the pull completes
// and then pulls the image itself.

const (
	defaultPreseedName       = "reactorcide-image-preseed"
	defaultPreseedToolsImage = "busybox:1.36.1-musl"
	defaultPreseedPauseImage = "registry.k8s.io/pause:3.10"
	defaultPreseedTimeout    = 30 * time.Minute
	preseedPollInterval      = 5 * time.Second

	preseedToolsDir = "/reactorcide-preseed"
	// preseedNoop is the busybox binary under the name "true". Busybox
	// selects the applet from the program name, so it exits with 0.
	preseedNoop = preseedToolsDir + "/true"

	preseedLabelComponent = "reactorcide.io/component"
	preseedLabelName      = "reactorcide.io/image-preseed"
	// preseedLabelSet is the short hash of the set (selector and name).
	preseedLabelSet       = "reactorcide.io/image-preseed-set"
	preseedLabelManagedBy = "app.kubernetes.io/managed-by"
	preseedManagedBy      = "reactorcide-worker"
	// preseedAnnotationHash is the full hash of the sorted reference list.
	preseedAnnotationHash = "reactorcide.io/image-preseed-hash"
	// preseedAnnotationUsedAt is the time (RFC 3339) when a worker last
	// used the DaemonSet.
	preseedAnnotationUsedAt = "reactorcide.io/image-preseed-used-at"

	// preseedHashLen is the length of the hash in the DaemonSet name.
	preseedHashLen = 10
	// preseedPrefixMax keeps prefix + "-" + hash a DNS-1123 label (63).
	preseedPrefixMax = 63 - 1 - preseedHashLen

	defaultPreseedGCGrace = 30 * time.Minute
	preseedTouchInterval  = 5 * time.Minute
)

// preseedGCGrace and preseedNow are variables for tests.
var (
	preseedGCGrace = defaultPreseedGCGrace
	preseedNow     = time.Now
)

// kubernetesPreseedConfig configures the image pre-seed DaemonSet.
type kubernetesPreseedConfig struct {
	// Name is the DaemonSet name prefix. The DaemonSet name is the prefix
	// and a short hash of the image set: workers with the same set share
	// one DaemonSet, and different sets never write over each other.
	Name string
	// ToolsImage is a small image with a static busybox at /bin/busybox.
	ToolsImage string
	// PauseImage is the main container image.
	PauseImage string
	// Timeout limits the wait for the DaemonSet to become ready. Then the
	// worker reports a failure and tries again later.
	Timeout time.Duration
}

func (c kubernetesPreseedConfig) withDefaults() kubernetesPreseedConfig {
	if c.Name == "" {
		c.Name = defaultPreseedName
	}
	if c.ToolsImage == "" {
		c.ToolsImage = defaultPreseedToolsImage
	}
	if c.PauseImage == "" {
		c.PauseImage = defaultPreseedPauseImage
	}
	if c.Timeout <= 0 {
		c.Timeout = defaultPreseedTimeout
	}
	return c
}

// loadKubernetesPreseedConfig reads:
//   - REACTORCIDE_K8S_IMAGE_PRESEED_NAME (default reactorcide-image-preseed)
//   - REACTORCIDE_K8S_IMAGE_PRESEED_TOOLS_IMAGE (default busybox:1.36.1-musl)
//   - REACTORCIDE_K8S_IMAGE_PRESEED_PAUSE_IMAGE (default registry.k8s.io/pause:3.10)
//   - REACTORCIDE_K8S_IMAGE_PRESEED_TIMEOUT (default 30m)
func loadKubernetesPreseedConfig(getenv func(string) string) (kubernetesPreseedConfig, error) {
	cfg := kubernetesPreseedConfig{
		Name:       strings.TrimSpace(getenv("REACTORCIDE_K8S_IMAGE_PRESEED_NAME")),
		ToolsImage: strings.TrimSpace(getenv("REACTORCIDE_K8S_IMAGE_PRESEED_TOOLS_IMAGE")),
		PauseImage: strings.TrimSpace(getenv("REACTORCIDE_K8S_IMAGE_PRESEED_PAUSE_IMAGE")),
	}
	if cfg.Name != "" {
		if errs := validation.IsDNS1123Label(cfg.Name); len(errs) > 0 {
			return cfg, fmt.Errorf("REACTORCIDE_K8S_IMAGE_PRESEED_NAME %q is not a valid name: %s", cfg.Name, strings.Join(errs, "; "))
		}
		if len(cfg.Name) > preseedPrefixMax {
			return cfg, fmt.Errorf("REACTORCIDE_K8S_IMAGE_PRESEED_NAME %q is too long: at most %d characters (the worker adds a hash of the image set)", cfg.Name, preseedPrefixMax)
		}
	}
	if raw := strings.TrimSpace(getenv("REACTORCIDE_K8S_IMAGE_PRESEED_TIMEOUT")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return cfg, fmt.Errorf("REACTORCIDE_K8S_IMAGE_PRESEED_TIMEOUT must be a positive duration")
		}
		cfg.Timeout = d
	}
	return cfg, nil
}

// kubernetesJobScheduling is the node selector, tolerations, and affinity
// of job pods.
type kubernetesJobScheduling struct {
	nodeSelector map[string]string
	tolerations  []corev1.Toleration
	affinity     *corev1.Affinity
}

// loadKubernetesJobScheduling reads:
//   - REACTORCIDE_K8S_JOB_NODE_SELECTOR: key=value pairs, comma separated
//   - REACTORCIDE_K8S_JOB_TOLERATIONS: a JSON list of Kubernetes tolerations
//   - REACTORCIDE_K8S_JOB_AFFINITY: a JSON Kubernetes affinity object
func loadKubernetesJobScheduling(getenv func(string) string) (kubernetesJobScheduling, error) {
	var out kubernetesJobScheduling
	if raw := strings.TrimSpace(getenv("REACTORCIDE_K8S_JOB_NODE_SELECTOR")); raw != "" {
		out.nodeSelector = map[string]string{}
		for _, pair := range strings.Split(raw, ",") {
			key, value, ok := strings.Cut(strings.TrimSpace(pair), "=")
			key, value = strings.TrimSpace(key), strings.TrimSpace(value)
			if !ok || key == "" {
				return out, fmt.Errorf("REACTORCIDE_K8S_JOB_NODE_SELECTOR: %q is not key=value", pair)
			}
			if errs := validation.IsQualifiedName(key); len(errs) > 0 {
				return out, fmt.Errorf("REACTORCIDE_K8S_JOB_NODE_SELECTOR: label key %q: %s", key, strings.Join(errs, "; "))
			}
			if errs := validation.IsValidLabelValue(value); len(errs) > 0 {
				return out, fmt.Errorf("REACTORCIDE_K8S_JOB_NODE_SELECTOR: label value %q: %s", value, strings.Join(errs, "; "))
			}
			out.nodeSelector[key] = value
		}
	}
	if raw := strings.TrimSpace(getenv("REACTORCIDE_K8S_JOB_TOLERATIONS")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &out.tolerations); err != nil {
			return out, fmt.Errorf("REACTORCIDE_K8S_JOB_TOLERATIONS must be a JSON list of tolerations: %w", err)
		}
	}
	if raw := strings.TrimSpace(getenv("REACTORCIDE_K8S_JOB_AFFINITY")); raw != "" {
		out.affinity = &corev1.Affinity{}
		if err := json.Unmarshal([]byte(raw), out.affinity); err != nil {
			return out, fmt.Errorf("REACTORCIDE_K8S_JOB_AFFINITY must be a JSON affinity object: %w", err)
		}
	}
	return out, nil
}

func (s kubernetesJobScheduling) nodeSelectorCopy() map[string]string {
	if len(s.nodeSelector) == 0 {
		return nil
	}
	out := make(map[string]string, len(s.nodeSelector))
	for k, v := range s.nodeSelector {
		out[k] = v
	}
	return out
}

func (s kubernetesJobScheduling) tolerationsCopy() []corev1.Toleration {
	if len(s.tolerations) == 0 {
		return nil
	}
	out := make([]corev1.Toleration, len(s.tolerations))
	for i := range s.tolerations {
		s.tolerations[i].DeepCopyInto(&out[i])
	}
	return out
}

func (s kubernetesJobScheduling) affinityCopy() *corev1.Affinity {
	if s.affinity == nil {
		return nil
	}
	return s.affinity.DeepCopy()
}

// jobImagePullPolicy is IfNotPresent for an image pinned by digest (the
// content cannot change, and the pre-seed DaemonSet put it on the node),
// else Always (a tag can move).
func jobImagePullPolicy(image string) corev1.PullPolicy {
	if ImageDigest(image) != "" {
		return corev1.PullIfNotPresent
	}
	return corev1.PullAlways
}

// preseedPullSecrets is the image pull secrets of the DaemonSet: the
// operator's global list and the list that jobs can request.
func (kr *KubernetesRunner) preseedPullSecrets() []corev1.LocalObjectReference {
	var out []corev1.LocalObjectReference
	for _, name := range CombineImagePullSecrets(kr.imagePullSecrets, kr.allowedJobImagePullSecrets) {
		out = append(out, corev1.LocalObjectReference{Name: name})
	}
	return out
}

// preseedRefs returns the container references of images: without the
// oci:// scheme, without duplicates, and sorted. Workers with the same set
// get the same list in the same order.
func preseedRefs(images []ImageSetImage) []string {
	refs := make([]string, 0, len(images))
	seen := map[string]bool{}
	for _, img := range images {
		ref := containerImageRef(img.Ref)
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs
}

// preseedHash is the full hash of a sorted reference list (hex sha256).
func preseedHash(refs []string) string {
	sum := sha256.Sum256([]byte(strings.Join(refs, "\n")))
	return hex.EncodeToString(sum[:])
}

// preseedName is the DaemonSet name of a set: the prefix and the first
// preseedHashLen characters of the hash.
func preseedName(prefix, hash string) string {
	return prefix + "-" + hash[:preseedHashLen]
}

// preseedDaemonSet builds the DaemonSet for images. The name and the
// selector have a short hash of the sorted reference list, so workers with
// the same set write the same DaemonSet, and different sets never write
// over each other. The full hash is the annotation preseedAnnotationHash.
func (kr *KubernetesRunner) preseedDaemonSet(images []ImageSetImage) *appsv1.DaemonSet {
	cfg := kr.preseed.withDefaults()
	refs := preseedRefs(images)
	hash := preseedHash(refs)
	name := preseedName(cfg.Name, hash)
	short := hash[:preseedHashLen]

	labels := map[string]string{
		preseedLabelComponent: "image-preseed",
		preseedLabelName:      cfg.Name,
		preseedLabelSet:       short,
		preseedLabelManagedBy: preseedManagedBy,
	}
	nonRoot := true
	noEscalation := false
	readOnly := true
	user := int64(65534)
	tiny := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("1m"),
			corev1.ResourceMemory: resource.MustParse("8Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("32Mi"),
		},
	}
	securityContext := &corev1.SecurityContext{
		AllowPrivilegeEscalation: &noEscalation,
		ReadOnlyRootFilesystem:   &readOnly,
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
	toolsMount := []corev1.VolumeMount{{Name: "preseed-tools", MountPath: preseedToolsDir}}

	initContainers := []corev1.Container{{
		Name:            "install-noop",
		Image:           cfg.ToolsImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"/bin/busybox", "cp", "/bin/busybox", preseedNoop},
		Resources:       tiny,
		SecurityContext: securityContext.DeepCopy(),
		VolumeMounts:    toolsMount,
	}}
	for i, ref := range refs {
		initContainers = append(initContainers, corev1.Container{
			Name:            preseedContainerName(i),
			Image:           ref,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Command:         []string{preseedNoop},
			Args:            []string{},
			Resources:       tiny,
			SecurityContext: securityContext.DeepCopy(),
			VolumeMounts:    toolsMount,
		})
	}

	maxUnavailable := intstr.FromString("100%")
	podLabels := map[string]string{}
	for k, v := range labels {
		podLabels[k] = v
	}
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   kr.namespace,
			Labels:      labels,
			Annotations: map[string]string{preseedAnnotationHash: hash},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{preseedLabelName: cfg.Name, preseedLabelSet: short}},
			UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
				Type:          appsv1.RollingUpdateDaemonSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDaemonSet{MaxUnavailable: &maxUnavailable},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: podLabels},
				Spec: corev1.PodSpec{
					ServiceAccountName:            kr.serviceAccount,
					AutomountServiceAccountToken:  &noEscalation,
					EnableServiceLinks:            &noEscalation,
					TerminationGracePeriodSeconds: int64Ptr(1),
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   &nonRoot,
						RunAsUser:      &user,
						RunAsGroup:     &user,
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					NodeSelector:     preseedNodeSelector(kr.scheduling.nodeSelectorCopy()),
					Tolerations:      kr.scheduling.tolerationsCopy(),
					Affinity:         kr.scheduling.affinityCopy(),
					ImagePullSecrets: kr.preseedPullSecrets(),
					InitContainers:   initContainers,
					Containers: []corev1.Container{{
						Name:            "pause",
						Image:           cfg.PauseImage,
						ImagePullPolicy: corev1.PullIfNotPresent,
						Resources:       tiny,
						SecurityContext: securityContext.DeepCopy(),
					}},
					Volumes: []corev1.Volume{{
						Name:         "preseed-tools",
						VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: resourcePtr("16Mi")}},
					}},
				},
			},
		},
	}
}

func preseedContainerName(i int) string { return fmt.Sprintf("image-%d", i) }

// preseedNodeSelector is the job node selector. The tools image is a Linux
// image, so the DaemonSet also selects Linux nodes when the job selector
// does not name an OS.
func preseedNodeSelector(selector map[string]string) map[string]string {
	if selector == nil {
		selector = map[string]string{}
	}
	if _, ok := selector[corev1.LabelOSStable]; !ok {
		selector[corev1.LabelOSStable] = "linux"
	}
	return selector
}

func resourcePtr(q string) *resource.Quantity {
	v := resource.MustParse(q)
	return &v
}

// preseedObserved reports whether ds is the DaemonSet of the set with the
// full hash, and the controller saw its current generation. Before that,
// the status is not for this set.
func preseedObserved(ds *appsv1.DaemonSet, hash string) bool {
	return ds != nil && ds.Annotations[preseedAnnotationHash] == hash &&
		ds.Status.ObservedGeneration == ds.Generation
}

// preseedReady is the readiness rule: the DaemonSet is for this set, the
// controller saw the current generation, at least one pod is scheduled,
// and every scheduled pod is updated and ready.
func preseedReady(ds *appsv1.DaemonSet, hash string) bool {
	if !preseedObserved(ds, hash) {
		return false
	}
	st := ds.Status
	return st.DesiredNumberScheduled > 0 &&
		st.UpdatedNumberScheduled == st.DesiredNumberScheduled &&
		st.NumberReady == st.DesiredNumberScheduled
}

// preseedNoNodes reports that the controller saw the DaemonSet of this set
// and schedules no pod: no node matches the job node selector.
func preseedNoNodes(ds *appsv1.DaemonSet, hash string) bool {
	return preseedObserved(ds, hash) && ds.Status.DesiredNumberScheduled == 0
}

// applyPreseed creates or updates the DaemonSet. It returns the stored
// object. A conflict (another worker with the same set wrote it) is tried
// again.
func (kr *KubernetesRunner) applyPreseed(ctx context.Context, desired *appsv1.DaemonSet) (*appsv1.DaemonSet, error) {
	api := kr.clientset.AppsV1().DaemonSets(kr.namespace)
	for attempt := 0; ; attempt++ {
		current, err := api.Get(ctx, desired.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			create := desired.DeepCopy()
			create.Annotations[preseedAnnotationUsedAt] = preseedNow().UTC().Format(time.RFC3339)
			created, err := api.Create(ctx, create, metav1.CreateOptions{})
			if apierrors.IsAlreadyExists(err) && attempt < 5 {
				continue
			}
			if err == nil {
				kr.markPreseedTouched()
			}
			return created, err
		}
		if err != nil {
			return nil, err
		}
		next := current.DeepCopy()
		next.Labels = desired.Labels
		if next.Annotations == nil {
			next.Annotations = map[string]string{}
		}
		next.Annotations[preseedAnnotationHash] = desired.Annotations[preseedAnnotationHash]
		next.Spec.UpdateStrategy = desired.Spec.UpdateStrategy
		next.Spec.Template = desired.Spec.Template
		if equalPreseedSpec(current, next) {
			return current, nil
		}
		updated, err := api.Update(ctx, next, metav1.UpdateOptions{})
		if apierrors.IsConflict(err) && attempt < 5 {
			continue
		}
		return updated, err
	}
}

func equalPreseedSpec(a, b *appsv1.DaemonSet) bool {
	ja, errA := json.Marshal(struct {
		L map[string]string
		H string
		S appsv1.DaemonSetSpec
	}{a.Labels, a.Annotations[preseedAnnotationHash], a.Spec})
	jb, errB := json.Marshal(struct {
		L map[string]string
		H string
		S appsv1.DaemonSetSpec
	}{b.Labels, b.Annotations[preseedAnnotationHash], b.Spec})
	return errA == nil && errB == nil && string(ja) == string(jb)
}

// ImageSetKind implements ImageSetSeeder.
func (kr *KubernetesRunner) ImageSetKind() string { return ImageKindContainer }

// ImageSpace implements ImageSetSeeder. The worker cannot see the disks of
// the nodes.
func (kr *KubernetesRunner) ImageSpace(context.Context, []ImageSetImage) (ImageSpace, error) {
	return ImageSpace{}, nil
}

// SeedImages implements ImageSetSeeder: it writes the DaemonSet of the set
// and waits until it is ready (preseedReady). The kubelet owns the node
// images, so no image is owned.
//
//   - An image whose pull fails permanently on a node (init container
//     waiting with a pull error that IsPermanentPullError accepts, or
//     InvalidImageName) is reported failed and removed from the set. The
//     worker then writes the DaemonSet of the other images.
//   - No node matches the job node selector: each image is reported
//     failed (transient).
//   - At the timeout: each image that is not ready is reported failed
//     (transient).
//   - An empty set writes nothing and deletes nothing. A DaemonSet that no
//     runner of this process uses is deleted after preseedGCGrace
//     (collectPreseedGarbage).
func (kr *KubernetesRunner) SeedImages(ctx context.Context, images []ImageSetImage, hooks ImageSeedHooks) error {
	cfg := kr.preseed.withDefaults()
	api := kr.clientset.AppsV1().DaemonSets(kr.namespace)

	// byRef groups the images by container reference: two entries can
	// have the same reference.
	byRef := map[string][]ImageSetImage{}
	var remaining []ImageSetImage
	for _, img := range images {
		if hooks.imageContext(ctx, img).Err() != nil {
			continue // the image left the set
		}
		ref := containerImageRef(img.Ref)
		if ref == "" {
			continue
		}
		byRef[ref] = append(byRef[ref], img)
		remaining = append(remaining, img)
	}

	deadline := preseedNow().Add(cfg.Timeout)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(remaining) == 0 {
			kr.setActivePreseed("", "")
			kr.collectPreseedGarbage(ctx)
			return nil
		}
		desired := kr.preseedDaemonSet(remaining)
		hash := desired.Annotations[preseedAnnotationHash]
		name := desired.Name
		ds, err := kr.applyPreseed(ctx, desired)
		if err != nil {
			return fmt.Errorf("write image pre-seed DaemonSet %s: %w", name, err)
		}
		kr.setActivePreseed(name, hash)
		kr.collectPreseedGarbage(ctx)
		logging.Log.WithFields(map[string]any{"daemonset": name, "namespace": kr.namespace, "images": len(remaining)}).Info("wrote image pre-seed DaemonSet")

		// Container name to reference, to map pod status to images.
		containerRef := map[string]string{}
		for _, c := range desired.Spec.Template.Spec.InitContainers[1:] {
			containerRef[c.Name] = c.Image
		}

		rewrite := false
		for !rewrite {
			// Images that left the set: write the DaemonSet without them.
			kept := remaining[:0:0]
			for _, img := range remaining {
				if hooks.imageContext(ctx, img).Err() == nil {
					kept = append(kept, img)
				}
			}
			if len(kept) != len(remaining) {
				remaining = kept
				rewrite = true
				break
			}

			if preseedReady(ds, hash) {
				kr.touchPreseed(ctx, name)
				for _, img := range remaining {
					hooks.ready(img, false)
				}
				return nil
			}
			if preseedNoNodes(ds, hash) {
				err := TransientPullError(fmt.Errorf("image pre-seed DaemonSet %s: no nodes match the job node selector", name))
				for _, img := range remaining {
					hooks.failed(img, err)
				}
				return nil
			}

			problems := kr.preseedPodProblems(ctx, ds, containerRef)
			for _, ref := range sortedProblemRefs(problems) {
				problem := problems[ref]
				if !problem.permanent() {
					continue
				}
				failure := PermanentPullError(fmt.Errorf("pull %s on a node: %s: %s", ref, problem.reason, problem.message))
				for _, img := range byRef[ref] {
					hooks.failed(img, failure)
				}
				kept := remaining[:0:0]
				for _, img := range remaining {
					if containerImageRef(img.Ref) != ref {
						kept = append(kept, img)
					}
				}
				remaining = kept
				rewrite = true
			}
			if rewrite {
				break
			}

			if preseedNow().After(deadline) {
				err := TransientPullError(fmt.Errorf("image pre-seed DaemonSet %s is not ready after %s: %d of %d pods ready, %d updated%s",
					name, cfg.Timeout, ds.Status.NumberReady, ds.Status.DesiredNumberScheduled, ds.Status.UpdatedNumberScheduled, problemText(problems)))
				for _, img := range remaining {
					hooks.failed(img, err)
				}
				return nil
			}
			kr.touchPreseed(ctx, name)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(kr.preseedPoll()):
			}
			if ds, err = api.Get(ctx, name, metav1.GetOptions{}); err != nil {
				return fmt.Errorf("read image pre-seed DaemonSet %s: %w", name, err)
			}
		}
	}
}

func (kr *KubernetesRunner) preseedPoll() time.Duration {
	if kr.preseedPollInterval > 0 {
		return kr.preseedPollInterval
	}
	return preseedPollInterval
}

// preseedProblem is an image pull problem of one init container.
type preseedProblem struct {
	reason  string
	message string
}

// permanent reports whether a retry of the pull cannot help.
func (p preseedProblem) permanent() bool {
	if p.reason == "InvalidImageName" {
		return true
	}
	return permanentPullTextIn(p.message)
}

// preseedPullReasons are the init container waiting reasons of a pull
// failure.
var preseedPullReasons = map[string]bool{
	"ErrImagePull":      true,
	"ImagePullBackOff":  true,
	"InvalidImageName":  true,
	"ErrImageNeverPull": true,
}

// preseedPodProblems returns the image pull problems of the pods of ds, by
// container reference. A problem with a permanent text wins over another
// problem of the same reference.
func (kr *KubernetesRunner) preseedPodProblems(ctx context.Context, ds *appsv1.DaemonSet, containerRef map[string]string) map[string]preseedProblem {
	out := map[string]preseedProblem{}
	if ds == nil || ds.Spec.Selector == nil {
		return out
	}
	selector := metav1.FormatLabelSelector(ds.Spec.Selector)
	pods, err := kr.clientset.CoreV1().Pods(kr.namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return out
	}
	for _, pod := range pods.Items {
		for _, st := range pod.Status.InitContainerStatuses {
			w := st.State.Waiting
			if w == nil || !preseedPullReasons[w.Reason] {
				continue
			}
			ref := containerRef[st.Name]
			if ref == "" {
				continue
			}
			problem := preseedProblem{reason: w.Reason, message: w.Message}
			if current, ok := out[ref]; ok && current.permanent() {
				continue
			}
			out[ref] = problem
		}
	}
	return out
}

func sortedProblemRefs(problems map[string]preseedProblem) []string {
	refs := make([]string, 0, len(problems))
	for ref := range problems {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs
}

// problemText is a short text of pull problems for a failure detail.
func problemText(problems map[string]preseedProblem) string {
	if len(problems) == 0 {
		return ""
	}
	list := []string{}
	for _, ref := range sortedProblemRefs(problems) {
		list = append(list, problems[ref].reason+" "+ref)
	}
	if len(list) > 3 {
		list = append(list[:3], "...")
	}
	return "; " + strings.Join(list, ", ")
}

// Active DaemonSets of the runners of this process, for the garbage
// collection: a DaemonSet that a runner uses is never deleted.
var preseedActive = struct {
	sync.Mutex
	runners map[*KubernetesRunner]preseedUse
}{runners: map[*KubernetesRunner]preseedUse{}}

type preseedUse struct {
	namespace, prefix string
	name, hash        string
	touchedAt         time.Time
}

func (kr *KubernetesRunner) setActivePreseed(name, hash string) {
	cfg := kr.preseed.withDefaults()
	preseedActive.Lock()
	defer preseedActive.Unlock()
	if name == "" {
		delete(preseedActive.runners, kr)
		return
	}
	use := preseedActive.runners[kr]
	if use.name != name {
		use.touchedAt = time.Time{}
	}
	use.namespace, use.prefix, use.name, use.hash = kr.namespace, cfg.Name, name, hash
	preseedActive.runners[kr] = use
}

// markPreseedTouched records that the runner wrote the used-at annotation
// of its DaemonSet now.
func (kr *KubernetesRunner) markPreseedTouched() {
	preseedActive.Lock()
	defer preseedActive.Unlock()
	if use, ok := preseedActive.runners[kr]; ok {
		use.touchedAt = preseedNow()
		preseedActive.runners[kr] = use
	}
}

// activePreseedHashes returns the full hashes that the runners of this
// process use in namespace with prefix.
func activePreseedHashes(namespace, prefix string) map[string]bool {
	preseedActive.Lock()
	defer preseedActive.Unlock()
	out := map[string]bool{}
	for _, use := range preseedActive.runners {
		if use.namespace == namespace && use.prefix == prefix && use.hash != "" {
			out[use.hash] = true
		}
	}
	return out
}

// touchPreseed writes the used-at annotation of the DaemonSet that this
// runner uses, at most one time each preseedTouchInterval. Workers of other
// processes see that the DaemonSet is in use and do not delete it.
func (kr *KubernetesRunner) touchPreseed(ctx context.Context, name string) {
	preseedActive.Lock()
	use, ok := preseedActive.runners[kr]
	due := ok && use.name == name && preseedNow().Sub(use.touchedAt) >= preseedTouchInterval
	preseedActive.Unlock()
	if !due {
		return
	}
	api := kr.clientset.AppsV1().DaemonSets(kr.namespace)
	ds, err := api.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return
	}
	next := ds.DeepCopy()
	if next.Annotations == nil {
		next.Annotations = map[string]string{}
	}
	next.Annotations[preseedAnnotationUsedAt] = preseedNow().UTC().Format(time.RFC3339)
	if _, err := api.Update(ctx, next, metav1.UpdateOptions{}); err != nil {
		// A conflict or an error: the next call tries again.
		return
	}
	kr.markPreseedTouched()
}

// TouchImageSet implements ImageSetToucher. The manager calls it each
// ImageSetTouchInterval.
func (kr *KubernetesRunner) TouchImageSet(ctx context.Context) { kr.TouchImagePreseed(ctx) }

// TouchImagePreseed refreshes the used-at annotation of the DaemonSet that
// this runner uses (at most one time each preseedTouchInterval) and runs
// the garbage collection.
func (kr *KubernetesRunner) TouchImagePreseed(ctx context.Context) {
	preseedActive.Lock()
	use, ok := preseedActive.runners[kr]
	preseedActive.Unlock()
	if ok {
		kr.touchPreseed(ctx, use.name)
	}
	kr.collectPreseedGarbage(ctx)
}

// collectPreseedGarbage deletes the pre-seed DaemonSets of this prefix that
// this worker manages (managed-by label), that no runner of this process
// uses, and that nobody used for preseedGCGrace: the newer of the creation
// time and the used-at annotation is older than the grace period. The
// grace period keeps a DaemonSet of another live worker (for example
// during a rolling update), because that worker refreshes the used-at
// annotation.
func (kr *KubernetesRunner) collectPreseedGarbage(ctx context.Context) {
	cfg := kr.preseed.withDefaults()
	api := kr.clientset.AppsV1().DaemonSets(kr.namespace)
	selector := preseedLabelName + "=" + cfg.Name + "," + preseedLabelManagedBy + "=" + preseedManagedBy
	list, err := api.List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		logging.Log.WithError(err).Warn("could not list image pre-seed DaemonSets for the garbage collection")
		return
	}
	active := activePreseedHashes(kr.namespace, cfg.Name)
	now := preseedNow()
	for i := range list.Items {
		ds := &list.Items[i]
		if ds.Labels[preseedLabelName] != cfg.Name || ds.Labels[preseedLabelManagedBy] != preseedManagedBy {
			continue
		}
		if active[ds.Annotations[preseedAnnotationHash]] {
			continue
		}
		last := ds.CreationTimestamp.Time
		if raw := ds.Annotations[preseedAnnotationUsedAt]; raw != "" {
			if used, err := time.Parse(time.RFC3339, raw); err == nil && used.After(last) {
				last = used
			}
		}
		if now.Sub(last) < preseedGCGrace {
			continue
		}
		if err := api.Delete(ctx, ds.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			logging.Log.WithError(err).WithField("daemonset", ds.Name).Warn("could not delete an unused image pre-seed DaemonSet")
			continue
		}
		logging.Log.WithFields(map[string]any{"daemonset": ds.Name, "namespace": kr.namespace}).Info("deleted an unused image pre-seed DaemonSet")
	}
}

// KeepImages implements ImageSetSeeder. The kubelet garbage collector owns
// node images.
func (kr *KubernetesRunner) KeepImages([]string) {}

// RemoveImages implements ImageSetSeeder. An image that left the set is
// not in the DaemonSet any more; the kubelet garbage collector removes it.
func (kr *KubernetesRunner) RemoveImages(context.Context, []ImageSetImage) ([]ImageSetImage, error) {
	return nil, nil
}

var (
	_ ImageSetSeeder  = (*KubernetesRunner)(nil)
	_ ImageSetToucher = (*KubernetesRunner)(nil)
)
