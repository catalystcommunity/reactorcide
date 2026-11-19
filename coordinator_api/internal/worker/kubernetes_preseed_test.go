package worker

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func preseedTestRunner(clientset *fake.Clientset) *KubernetesRunner {
	return &KubernetesRunner{
		clientset:                  clientset,
		namespace:                  "jobs",
		serviceAccount:             "job-sa",
		imagePullSecrets:           []string{"global-pull"},
		allowedJobImagePullSecrets: []string{"team-pull", "global-pull"},
		scheduling: kubernetesJobScheduling{
			nodeSelector: map[string]string{"pool": "ci"},
			tolerations:  []corev1.Toleration{{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "ci", Effect: corev1.TaintEffectNoSchedule}},
			affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "zone", Operator: corev1.NodeSelectorOpIn, Values: []string{"z1"}}}}},
			}}},
		},
		preseed:             kubernetesPreseedConfig{}.withDefaults(),
		preseedPollInterval: 5 * time.Millisecond,
	}
}

// seedCalls collects SeedImages callbacks from a goroutine.
type seedCalls struct {
	mu     sync.Mutex
	ready  []readyCall
	failed []failedCall
}

func (c *seedCalls) hooks() ImageSeedHooks {
	return ImageSeedHooks{
		Ready: func(img ImageSetImage, owned bool) {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.ready = append(c.ready, readyCall{img.Digest, owned})
		},
		Failed: func(img ImageSetImage, err error) {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.failed = append(c.failed, failedCall{img.Digest, IsPermanentPullError(err), err.Error()})
		},
	}
}

func preseedNameFor(t *testing.T, kr *KubernetesRunner, images []ImageSetImage) (name, hash string) {
	t.Helper()
	ds := kr.preseedDaemonSet(images)
	return ds.Name, ds.Annotations[preseedAnnotationHash]
}

// waitForDaemonSet waits until the worker wrote the DaemonSet name.
func waitForDaemonSet(t *testing.T, clientset *fake.Clientset, name string) *appsv1.DaemonSet {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ds, err := clientset.AppsV1().DaemonSets("jobs").Get(context.Background(), name, metav1.GetOptions{})
		if err == nil {
			return ds
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("DaemonSet %s was not written", name)
	return nil
}

// precreatePreseed writes the DaemonSet of images with a status, as the
// controller would. The fake clientset has no controller: a new DaemonSet
// has a zero status, which the worker reads as "no node matches".
func precreatePreseed(t *testing.T, clientset *fake.Clientset, kr *KubernetesRunner, images []ImageSetImage, st appsv1.DaemonSetStatus) *appsv1.DaemonSet {
	t.Helper()
	ds := kr.preseedDaemonSet(images)
	ds.Status = st
	// The fake sets no creation time; a zero time is old for the
	// garbage collection.
	ds.CreationTimestamp = metav1.Now()
	created, err := clientset.AppsV1().DaemonSets("jobs").Create(context.Background(), ds, metav1.CreateOptions{})
	require.NoError(t, err)
	return created
}

func setPreseedStatus(t *testing.T, clientset *fake.Clientset, ds *appsv1.DaemonSet, st appsv1.DaemonSetStatus) {
	t.Helper()
	// Read the stored object: the fake UpdateStatus writes the whole
	// object, so a stale copy would undo the worker's annotations.
	current, err := clientset.AppsV1().DaemonSets("jobs").Get(context.Background(), ds.Name, metav1.GetOptions{})
	require.NoError(t, err)
	current.Status = st
	_, err = clientset.AppsV1().DaemonSets("jobs").UpdateStatus(context.Background(), current, metav1.UpdateOptions{})
	require.NoError(t, err)
}

func TestKubernetesPreseedDaemonSetSpec(t *testing.T) {
	kr := preseedTestRunner(fake.NewSimpleClientset())
	ds := kr.preseedDaemonSet(setImages())

	hash := ds.Annotations[preseedAnnotationHash]
	require.Len(t, hash, 64)
	require.Equal(t, "reactorcide-image-preseed-"+hash[:10], ds.Name)
	require.Equal(t, "jobs", ds.Namespace)
	require.Equal(t, preseedManagedBy, ds.Labels[preseedLabelManagedBy])
	require.Equal(t, "reactorcide-image-preseed", ds.Labels[preseedLabelName])
	spec := ds.Spec.Template.Spec
	require.Equal(t, map[string]string{"pool": "ci", "kubernetes.io/os": "linux"}, spec.NodeSelector)
	require.Equal(t, kr.scheduling.tolerations, spec.Tolerations)
	require.Equal(t, kr.scheduling.affinity, spec.Affinity)
	require.Equal(t, []corev1.LocalObjectReference{{Name: "global-pull"}, {Name: "team-pull"}}, spec.ImagePullSecrets)
	require.Equal(t, map[string]string{preseedLabelName: "reactorcide-image-preseed", preseedLabelSet: hash[:10]}, ds.Spec.Selector.MatchLabels)
	for k, v := range ds.Spec.Selector.MatchLabels {
		require.Equal(t, v, ds.Spec.Template.Labels[k])
	}

	// One tools init container, then one per image (sorted, oci:// removed).
	require.Len(t, spec.InitContainers, 3)
	tools := spec.InitContainers[0]
	require.Equal(t, defaultPreseedToolsImage, tools.Image)
	require.Equal(t, []string{"/bin/busybox", "cp", "/bin/busybox", preseedNoop}, tools.Command)
	require.Equal(t, "registry.example/team/a:1@"+digestA, spec.InitContainers[1].Image)
	require.Equal(t, "registry.example/team/b:2@"+digestB, spec.InitContainers[2].Image)
	for _, c := range spec.InitContainers[1:] {
		require.Equal(t, []string{preseedNoop}, c.Command, "the image needs no shell")
		require.Equal(t, corev1.PullIfNotPresent, c.ImagePullPolicy)
		require.Equal(t, preseedToolsDir, c.VolumeMounts[0].MountPath)
	}
	require.Len(t, spec.Containers, 1)
	require.Equal(t, defaultPreseedPauseImage, spec.Containers[0].Image)
	require.NotNil(t, spec.Volumes[0].EmptyDir)
	require.True(t, *spec.SecurityContext.RunAsNonRoot)

	// The spec does not depend on the order of the images.
	reversed := []ImageSetImage{setImages()[1], setImages()[0]}
	require.True(t, equalPreseedSpec(ds, kr.preseedDaemonSet(reversed)))
}

func TestKubernetesPreseedNameByHash(t *testing.T) {
	kr := preseedTestRunner(fake.NewSimpleClientset())
	images := setImages()
	name, hash := preseedNameFor(t, kr, images)
	sameName, sameHash := preseedNameFor(t, kr, []ImageSetImage{images[1], images[0], images[0]})
	require.Equal(t, name, sameName, "the same set gives the same name in any order")
	require.Equal(t, hash, sameHash)
	otherName, otherHash := preseedNameFor(t, kr, images[:1])
	require.NotEqual(t, name, otherName, "a different set gives a different name")
	require.NotEqual(t, hash, otherHash)

	cfg, err := loadKubernetesPreseedConfig(func(k string) string {
		if k == "REACTORCIDE_K8S_IMAGE_PRESEED_NAME" {
			return "pool-a-preseed"
		}
		return ""
	})
	require.NoError(t, err)
	kr.preseed = cfg.withDefaults()
	prefixed, _ := preseedNameFor(t, kr, images)
	require.Equal(t, "pool-a-preseed-"+hash[:10], prefixed)

	_, err = loadKubernetesPreseedConfig(func(k string) string {
		if k == "REACTORCIDE_K8S_IMAGE_PRESEED_NAME" {
			return strings.Repeat("a", preseedPrefixMax+1)
		}
		return ""
	})
	require.ErrorContains(t, err, "too long")
	_, err = loadKubernetesPreseedConfig(func(k string) string {
		if k == "REACTORCIDE_K8S_IMAGE_PRESEED_NAME" {
			return strings.Repeat("a", preseedPrefixMax)
		}
		return ""
	})
	require.NoError(t, err)
}

func TestKubernetesPreseedReadyRule(t *testing.T) {
	const hash = "h1"
	ds := func(annotation string, gen, observed int64, desired, ready, updated int32) *appsv1.DaemonSet {
		return &appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Generation: gen, Annotations: map[string]string{preseedAnnotationHash: annotation}},
			Status:     appsv1.DaemonSetStatus{ObservedGeneration: observed, DesiredNumberScheduled: desired, NumberReady: ready, UpdatedNumberScheduled: updated},
		}
	}
	require.True(t, preseedReady(ds(hash, 2, 2, 3, 3, 3), hash))
	require.False(t, preseedReady(ds("other", 2, 2, 3, 3, 3), hash), "the DaemonSet is for another set")
	require.False(t, preseedReady(ds(hash, 3, 2, 3, 3, 3), hash), "the controller did not see the current generation")
	require.False(t, preseedReady(ds(hash, 2, 3, 3, 3, 3), hash), "observed generation must equal the generation")
	require.False(t, preseedReady(ds(hash, 2, 2, 3, 2, 3), hash), "a pod is not ready")
	require.False(t, preseedReady(ds(hash, 2, 2, 3, 3, 2), hash), "a pod has the old spec")
	require.False(t, preseedReady(ds(hash, 1, 1, 0, 0, 0), hash), "no matching nodes is not ready")
	require.True(t, preseedNoNodes(ds(hash, 1, 1, 0, 0, 0), hash))
	require.False(t, preseedNoNodes(ds(hash, 2, 1, 0, 0, 0), hash), "not observed yet")
	require.False(t, preseedNoNodes(ds("other", 1, 1, 0, 0, 0), hash))
}

func TestKubernetesSeedImagesWaitsForDaemonSet(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	kr := preseedTestRunner(clientset)
	t.Cleanup(func() { kr.setActivePreseed("", "") })
	name, _ := preseedNameFor(t, kr, setImages())
	precreatePreseed(t, clientset, kr, setImages(), appsv1.DaemonSetStatus{DesiredNumberScheduled: 2})

	done := make(chan error, 1)
	calls := &seedCalls{}
	go func() { done <- kr.SeedImages(context.Background(), setImages(), calls.hooks()) }()

	stored := waitForDaemonSet(t, clientset, name)
	require.Len(t, stored.Spec.Template.Spec.InitContainers, 3, "the worker wrote the image set")
	select {
	case err := <-done:
		t.Fatalf("SeedImages returned before the DaemonSet was ready: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	setPreseedStatus(t, clientset, stored, appsv1.DaemonSetStatus{DesiredNumberScheduled: 2, NumberReady: 2, UpdatedNumberScheduled: 2})

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("SeedImages did not see the ready DaemonSet")
	}
	require.Equal(t, []readyCall{{digestA, false}, {digestB, false}}, calls.ready, "the kubelet owns node images")
	require.Empty(t, calls.failed)
	require.True(t, activePreseedHashes("jobs", "reactorcide-image-preseed")[stored.Annotations[preseedAnnotationHash]])

	// An empty set deletes nothing.
	require.NoError(t, kr.SeedImages(context.Background(), nil, calls.hooks()))
	_, err := clientset.AppsV1().DaemonSets("jobs").Get(context.Background(), name, metav1.GetOptions{})
	require.NoError(t, err, "an empty set does not delete the DaemonSet")
	require.Empty(t, activePreseedHashes("jobs", "reactorcide-image-preseed"), "an empty set clears the active hash")
}

// A DaemonSet with the name of the set but another hash annotation is not
// read as ready: the worker writes its own hash first, and the readiness
// rule needs that hash.
func TestKubernetesSeedImagesHashMismatchNotReady(t *testing.T) {
	kr := preseedTestRunner(fake.NewSimpleClientset())
	name, hash := preseedNameFor(t, kr, setImages())
	foreign := &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "jobs", Annotations: map[string]string{preseedAnnotationHash: "another"}},
		Status:     appsv1.DaemonSetStatus{DesiredNumberScheduled: 1, NumberReady: 1, UpdatedNumberScheduled: 1},
	}
	require.False(t, preseedReady(foreign, hash), "the status of another set is not read")

	clientset := fake.NewSimpleClientset(foreign)
	kr = preseedTestRunner(clientset)
	t.Cleanup(func() { kr.setActivePreseed("", "") })
	calls := &seedCalls{}
	require.NoError(t, kr.SeedImages(context.Background(), setImages(), calls.hooks()))
	stored, err := clientset.AppsV1().DaemonSets("jobs").Get(context.Background(), name, metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, hash, stored.Annotations[preseedAnnotationHash], "the worker wrote its own hash")
	require.Len(t, stored.Spec.Template.Spec.InitContainers, 3)
}

func TestKubernetesSeedImagesNoNodes(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	kr := preseedTestRunner(clientset)
	t.Cleanup(func() { kr.setActivePreseed("", "") })
	// The fake has no controller: a new DaemonSet has a zero status, which
	// is "observed, no node".
	calls := &seedCalls{}
	require.NoError(t, kr.SeedImages(context.Background(), setImages(), calls.hooks()))
	require.Empty(t, calls.ready)
	require.Len(t, calls.failed, 2)
	for _, f := range calls.failed {
		require.False(t, f.permanent)
		require.Contains(t, f.text, "no nodes match the job node selector")
	}
}

func TestKubernetesSeedImagesTimeout(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	kr := preseedTestRunner(clientset)
	t.Cleanup(func() { kr.setActivePreseed("", "") })
	precreatePreseed(t, clientset, kr, setImages(), appsv1.DaemonSetStatus{DesiredNumberScheduled: 1})
	kr.preseed.Timeout = 20 * time.Millisecond

	calls := &seedCalls{}
	require.NoError(t, kr.SeedImages(context.Background(), setImages(), calls.hooks()))
	require.Empty(t, calls.ready)
	require.Len(t, calls.failed, 2)
	for _, f := range calls.failed {
		require.False(t, f.permanent, "a timeout is transient")
		require.Contains(t, f.text, "not ready after")
		require.Contains(t, f.text, "0 of 1 pods ready")
	}
}

// An image that a node cannot pull (manifest unknown) is reported failed
// (permanent) and removed; the other images then become ready in the
// DaemonSet of the smaller set.
func TestKubernetesSeedImagesPermanentPullFailureDropsImage(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	kr := preseedTestRunner(clientset)
	t.Cleanup(func() { kr.setActivePreseed("", "") })
	images := setImages()
	fullName, _ := preseedNameFor(t, kr, images)
	smallName, _ := preseedNameFor(t, kr, images[1:])
	precreatePreseed(t, clientset, kr, images, appsv1.DaemonSetStatus{DesiredNumberScheduled: 1})
	precreatePreseed(t, clientset, kr, images[1:], appsv1.DaemonSetStatus{DesiredNumberScheduled: 1})

	done := make(chan error, 1)
	calls := &seedCalls{}
	go func() { done <- kr.SeedImages(context.Background(), images, calls.hooks()) }()

	full := waitForDaemonSet(t, clientset, fullName)
	_, err := clientset.CoreV1().Pods("jobs").Create(context.Background(), &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "preseed-node1", Namespace: "jobs", Labels: full.Spec.Template.Labels},
		Status: corev1.PodStatus{InitContainerStatuses: []corev1.ContainerStatus{{
			Name:  "image-0",
			Image: "registry.example/team/a:1@" + digestA,
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
				Reason: "ErrImagePull", Message: "failed to resolve reference: registry.example/team/a: not found",
			}},
		}}},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	small := waitForDaemonSet(t, clientset, smallName)
	require.Len(t, small.Spec.Template.Spec.InitContainers, 2, "the DaemonSet of the other image")
	setPreseedStatus(t, clientset, small, appsv1.DaemonSetStatus{DesiredNumberScheduled: 1, NumberReady: 1, UpdatedNumberScheduled: 1})

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("SeedImages did not complete")
	}
	calls.mu.Lock()
	defer calls.mu.Unlock()
	require.Len(t, calls.failed, 1)
	require.Equal(t, digestA, calls.failed[0].digest)
	require.True(t, calls.failed[0].permanent)
	require.Equal(t, []readyCall{{digestB, false}}, calls.ready)
}

// A transient pull problem (back-off after a timeout) keeps the image.
func TestKubernetesPreseedProblemClasses(t *testing.T) {
	require.True(t, preseedProblem{reason: "InvalidImageName", message: "x"}.permanent())
	require.True(t, preseedProblem{reason: "ErrImagePull", message: "unauthorized: authentication required"}.permanent())
	require.False(t, preseedProblem{reason: "ImagePullBackOff", message: "Back-off pulling image: dial tcp: i/o timeout"}.permanent())
}

func TestKubernetesPreseedGarbageCollection(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	oldNow := preseedNow
	preseedNow = func() time.Time { return now }
	t.Cleanup(func() { preseedNow = oldNow })

	managed := func(name, prefix, hash string, created time.Time, usedAt string) *appsv1.DaemonSet {
		ds := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "jobs", CreationTimestamp: metav1.NewTime(created),
			Labels: map[string]string{
				preseedLabelComponent: "image-preseed", preseedLabelName: prefix, preseedLabelManagedBy: preseedManagedBy,
			},
			Annotations: map[string]string{preseedAnnotationHash: hash},
		}}
		if usedAt != "" {
			ds.Annotations[preseedAnnotationUsedAt] = usedAt
		}
		return ds
	}
	old := now.Add(-2 * time.Hour)
	unmanaged := managed("no-label", "reactorcide-image-preseed", "h-nolabel", old, "")
	delete(unmanaged.Labels, preseedLabelManagedBy)
	clientset := fake.NewSimpleClientset(
		managed("old-unused", "reactorcide-image-preseed", "h-old", old, ""),
		managed("young", "reactorcide-image-preseed", "h-young", now.Add(-time.Minute), ""),
		managed("recent-use", "reactorcide-image-preseed", "h-recent", old, now.Add(-10*time.Minute).Format(time.RFC3339)),
		managed("stale-use", "reactorcide-image-preseed", "h-stale", old, now.Add(-time.Hour).Format(time.RFC3339)),
		managed("active", "reactorcide-image-preseed", "h-active", old, ""),
		managed("other-prefix", "pool-b", "h-other", old, ""),
		unmanaged,
	)
	kr := preseedTestRunner(clientset)
	kr.setActivePreseed("active", "h-active")
	t.Cleanup(func() { kr.setActivePreseed("", "") })

	kr.collectPreseedGarbage(context.Background())

	list, err := clientset.AppsV1().DaemonSets("jobs").List(context.Background(), metav1.ListOptions{})
	require.NoError(t, err)
	var names []string
	for _, ds := range list.Items {
		names = append(names, ds.Name)
	}
	require.ElementsMatch(t, []string{"young", "recent-use", "active", "other-prefix", "no-label"}, names)
}

// The used-at annotation is refreshed at most one time each interval.
func TestKubernetesPreseedTouch(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	oldNow := preseedNow
	preseedNow = func() time.Time { return now }
	t.Cleanup(func() { preseedNow = oldNow })

	clientset := fake.NewSimpleClientset(&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "ds", Namespace: "jobs"}})
	kr := preseedTestRunner(clientset)
	kr.setActivePreseed("ds", "h")
	t.Cleanup(func() { kr.setActivePreseed("", "") })

	read := func() string {
		ds, err := clientset.AppsV1().DaemonSets("jobs").Get(context.Background(), "ds", metav1.GetOptions{})
		require.NoError(t, err)
		return ds.Annotations[preseedAnnotationUsedAt]
	}
	kr.TouchImagePreseed(context.Background())
	first := read()
	require.Equal(t, now.Format(time.RFC3339), first)
	now = now.Add(time.Minute)
	kr.touchPreseed(context.Background(), "ds")
	require.Equal(t, first, read(), "not again within the interval")
	now = now.Add(preseedTouchInterval)
	kr.touchPreseed(context.Background(), "ds")
	require.Equal(t, now.Format(time.RFC3339), read())
}
