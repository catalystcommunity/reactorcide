package imagealias

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/characteristics"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
)

const (
	orgID     = "01000000-0000-0000-0000-000000000001"
	projectID = "01000000-0000-0000-0000-000000000002"
	otherProj = "01000000-0000-0000-0000-000000000003"
)

var (
	digestOrg     = "sha256:" + strings.Repeat("a", 64)
	digestProject = "sha256:" + strings.Repeat("b", 64)
	digestPending = "sha256:" + strings.Repeat("c", 64)
	digestNone    = "sha256:" + strings.Repeat("d", 64)
	digestCont    = "sha256:" + strings.Repeat("e", 64)
)

// fakeStore is an in-memory store.ImageAliasStore and
// store.ImagePolicyStore. Only the methods that ResolveJob uses do work.
type fakeStore struct {
	aliases []models.ImageAlias
	policy  *models.ImagePolicy
	err     error
}

func (f *fakeStore) visible(org string, project *string, a models.ImageAlias) bool {
	if a.OrgID != org {
		return false
	}
	return a.ProjectID == nil || (project != nil && *a.ProjectID == *project)
}

func (f *fakeStore) ResolveImageAlias(_ context.Context, org string, project *string, name string) (*models.ImageAlias, error) {
	if f.err != nil {
		return nil, f.err
	}
	var orgAlias *models.ImageAlias
	for i := range f.aliases {
		a := f.aliases[i]
		if a.Name != name || !f.visible(org, project, a) {
			continue
		}
		if a.ProjectID != nil {
			return &a, nil
		}
		orgAlias = &a
	}
	if orgAlias == nil {
		return nil, store.ErrNotFound
	}
	return orgAlias, nil
}

func (f *fakeStore) ListImageAliasesWithDigest(_ context.Context, org string, project *string, digest string, platform models.ImagePlatform) ([]models.ImageAlias, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []models.ImageAlias
	for _, a := range f.aliases {
		if f.visible(org, project, a) && a.HasDigest(digest) && platform.Matches(&a) {
			out = append(out, a)
		}
	}
	return out, nil
}

func (f *fakeStore) GetImagePolicy(context.Context, string) (*models.ImagePolicy, error) {
	if f.policy == nil {
		return &models.ImagePolicy{}, nil
	}
	return f.policy, nil
}
func (f *fakeStore) SetImagePolicy(context.Context, *models.ImagePolicy) error { return nil }

func (f *fakeStore) CreateImageAlias(context.Context, *models.ImageAlias) error { return nil }
func (f *fakeStore) UpdateImageAlias(context.Context, *models.ImageAlias) error { return nil }
func (f *fakeStore) GetImageAlias(context.Context, string, *string, string) (*models.ImageAlias, error) {
	return nil, store.ErrNotFound
}
func (f *fakeStore) GetImageAliasByID(context.Context, string) (*models.ImageAlias, error) {
	return nil, store.ErrNotFound
}
func (f *fakeStore) ListImageAliases(context.Context, string, *string) ([]models.ImageAlias, error) {
	return nil, nil
}
func (f *fakeStore) ListOrgImageAliases(context.Context, string) ([]models.ImageAlias, error) {
	return nil, nil
}
func (f *fakeStore) DeleteImageAlias(context.Context, string, *string, string) error { return nil }
func (f *fakeStore) SetImageAliasPending(context.Context, string, string, string) (*models.ImageAlias, error) {
	return nil, nil
}
func (f *fakeStore) ClearImageAliasPending(context.Context, string, string) (*models.ImageAlias, error) {
	return nil, nil
}
func (f *fakeStore) PromoteImageAliasPending(context.Context, string, string) (*models.ImageAlias, bool, error) {
	return nil, false, nil
}

var _ store.ImageAliasStore = (*fakeStore)(nil)

func strp(s string) *string { return &s }

func newFake() *fakeStore {
	pending := "oci://registry.example.com/vm/win:next@" + digestPending
	return &fakeStore{aliases: []models.ImageAlias{
		{OrgID: orgID, Name: "windows-vs2022", Kind: models.ImageAliasKindVM, OS: "windows", Arch: "amd64",
			ActiveRef: "oci://registry.example.com/vm/win:2026.11@" + digestOrg, ActiveDigest: digestOrg,
			PendingRef: &pending, PendingDigest: strp(digestPending)},
		{OrgID: orgID, ProjectID: strp(projectID), Name: "windows-vs2022", Kind: models.ImageAliasKindVM, OS: "windows", Arch: "amd64",
			ActiveRef: "oci://registry.example.com/team/win:custom@" + digestProject, ActiveDigest: digestProject},
		{OrgID: orgID, Name: "builder", Kind: models.ImageAliasKindContainer, OS: "linux", Arch: "amd64",
			ActiveRef: "ghcr.io/acme/builder:1@" + digestCont, ActiveDigest: digestCont},
	}}
}

func vmJob(image string, project *string) *models.Job {
	return &models.Job{OrgID: orgID, ProjectID: project, RunnerImage: image,
		Characteristics: characteristics.Characteristics{"os": characteristics.StringValue("windows")}}
}

func containerJob(image string) *models.Job {
	return &models.Job{OrgID: orgID, RunnerImage: image,
		Characteristics: characteristics.Characteristics{"os": characteristics.StringValue("linux")}}
}

func digestOf(job *models.Job) string {
	if v, ok := job.Characteristics[DigestCharacteristic].(characteristics.StringValue); ok {
		return string(v)
	}
	return ""
}

func wantRefused(t *testing.T, err error, code, contains string) {
	t.Helper()
	refused, ok := IsRefused(err)
	if !ok {
		t.Fatalf("want a refusal, got %v", err)
	}
	if refused.Code != code || !strings.Contains(refused.Message, contains) {
		t.Fatalf("refusal = %+v, want code %q with %q", refused, code, contains)
	}
}

func TestResolveProjectAliasBeforeOrgAlias(t *testing.T) {
	st := newFake()
	job := vmJob("windows-vs2022", strp(projectID))
	result, err := ResolveJob(context.Background(), st, job)
	if err != nil {
		t.Fatal(err)
	}
	if job.RunnerImage != "registry.example.com/team/win:custom@"+digestProject || job.ImageAlias == nil || *job.ImageAlias != "windows-vs2022" {
		t.Fatalf("job image = %q alias = %v", job.RunnerImage, job.ImageAlias)
	}
	if digestOf(job) != digestProject || result.Digest != digestProject || result.Alias == nil {
		t.Fatalf("digest = %q, result = %+v", digestOf(job), result)
	}
	if got := DisplayJob(job); got != "windows-vs2022 (registry.example.com/team/win:custom@"+digestProject+")" {
		t.Fatalf("DisplayJob = %q", got)
	}
}

func TestResolveOrgAliasForOtherProject(t *testing.T) {
	st := newFake()
	job := vmJob("windows-vs2022", strp(otherProj))
	if _, err := ResolveJob(context.Background(), st, job); err != nil {
		t.Fatal(err)
	}
	if job.RunnerImage != JobReference(st.aliases[0].ActiveRef) || digestOf(job) != digestOrg {
		t.Fatalf("job image = %q digest = %q", job.RunnerImage, digestOf(job))
	}
}

func TestResolveContainerImageField(t *testing.T) {
	st := newFake()
	job := containerJob("ignored")
	job.ContainerImage = strp("builder")
	if _, err := ResolveJob(context.Background(), st, job); err != nil {
		t.Fatal(err)
	}
	if *job.ContainerImage != "ghcr.io/acme/builder:1@"+digestCont || job.RunnerImage != "ignored" {
		t.Fatalf("container_image = %q runner_image = %q", *job.ContainerImage, job.RunnerImage)
	}
	if digestOf(job) != digestCont || *job.ImageAlias != "builder" {
		t.Fatalf("digest = %q alias = %v", digestOf(job), job.ImageAlias)
	}
}

func TestContainerReferenceRoutesAsBefore(t *testing.T) {
	st := newFake()
	for _, image := range []string{"alpine", "alpine:3.20", "ghcr.io/acme/app:1", "ghcr.io/acme/app@" + digestNone} {
		job := containerJob(image)
		if _, err := ResolveJob(context.Background(), st, job); err != nil {
			t.Fatalf("%s: %v", image, err)
		}
		if job.RunnerImage != image || job.ImageAlias != nil || digestOf(job) != "" {
			t.Fatalf("%s: job changed: %q %v %q", image, job.RunnerImage, job.ImageAlias, digestOf(job))
		}
	}
}

func TestVMRefusals(t *testing.T) {
	st := newFake()
	cases := map[string]string{
		"bare name that is not an alias": "macos-unknown",
		"unpinned reference":             "oci://registry.example.com/vm/win:2026.11",
		"pinned but no alias pins it":    "oci://registry.example.com/vm/win:2026.11@" + digestNone,
		"uppercase digest":               "oci://registry.example.com/vm/win:x@sha256:" + strings.Repeat("A", 64),
	}
	for name, image := range cases {
		t.Run(name, func(t *testing.T) {
			job := vmJob(image, strp(projectID))
			_, err := ResolveJob(context.Background(), st, job)
			wantRefused(t, err, CodeImageAlias, "alias")
			if digestOf(job) != "" || job.ImageAlias != nil {
				t.Fatalf("a refused job must not get a digest or an alias")
			}
		})
	}
	t.Run("no image", func(t *testing.T) {
		_, err := ResolveJob(context.Background(), st, vmJob("", nil))
		wantRefused(t, err, CodeImageAlias, "needs an image")
	})
	t.Run("macos uses the VM rules", func(t *testing.T) {
		job := vmJob("ghcr.io/acme/app:1", nil)
		job.Characteristics["os"] = characteristics.StringValue("macos")
		_, err := ResolveJob(context.Background(), st, job)
		wantRefused(t, err, CodeImageAlias, "not pinned")
	})
	t.Run("container alias in a VM job", func(t *testing.T) {
		_, err := ResolveJob(context.Background(), st, vmJob("builder", nil))
		wantRefused(t, err, CodeImageAlias, `image alias "builder" is a container image, but the job is a vm job (job os "windows"): no worker can run the job. Use a vm image alias`)
	})
	t.Run("VM alias in a container job", func(t *testing.T) {
		_, err := ResolveJob(context.Background(), st, containerJob("windows-vs2022"))
		wantRefused(t, err, CodeImageAlias, "vm image")
	})
	t.Run("project pin is not visible to another project", func(t *testing.T) {
		_, err := ResolveJob(context.Background(), st, vmJob("oci://registry.example.com/team/win:custom@"+digestProject, strp(otherProj)))
		wantRefused(t, err, CodeImageAlias, digestProject)
	})
}

// TestVMPinnedReference: a direct pinned reference is accepted when an
// alias of the scope with the same kind and platform pins its digest, and
// the job stores the reference of that alias (not the location that the
// job named), with no alias name.
func TestVMPinnedReference(t *testing.T) {
	st := newFake()
	wantRef := map[string]string{
		digestOrg:     JobReference(st.aliases[0].ActiveRef),
		digestPending: JobReference(*st.aliases[0].PendingRef),
	}
	for _, digest := range []string{digestOrg, digestPending} {
		image := "other-registry.example.com/copy/win:any-tag@" + digest
		job := vmJob(image, nil)
		if _, err := ResolveJob(context.Background(), st, job); err != nil {
			t.Fatalf("%s: %v", digest, err)
		}
		if job.RunnerImage != wantRef[digest] || job.ImageAlias != nil || digestOf(job) != digest {
			t.Fatalf("%s: image %q alias %v digest %q", digest, job.RunnerImage, job.ImageAlias, digestOf(job))
		}
		if DisplayJob(job) != wantRef[digest] {
			t.Fatalf("DisplayJob = %q", DisplayJob(job))
		}
		// The job records the alias that pinned the digest, for a retry.
		if job.ImagePinAlias == nil || *job.ImagePinAlias != "windows-vs2022" {
			t.Fatalf("%s: image_pin_alias = %v", digest, job.ImagePinAlias)
		}
	}
}

// TestVMPinnedReferenceKindAndPlatform: only an alias with the kind and
// the platform of the job counts.
func TestVMPinnedReferenceKindAndPlatform(t *testing.T) {
	st := newFake()
	shared := "sha256:" + strings.Repeat("f", 64)
	st.aliases = append(st.aliases,
		// A container alias and an arm64 VM alias pin the digest.
		models.ImageAlias{OrgID: orgID, Name: "c", Kind: models.ImageAliasKindContainer, OS: "windows", Arch: "amd64",
			ActiveRef: "ghcr.io/acme/c:1@" + shared, ActiveDigest: shared},
		models.ImageAlias{OrgID: orgID, Name: "arm", Kind: models.ImageAliasKindVM, OS: "windows", Arch: "arm64",
			ActiveRef: "registry.example.com/vm/arm:1@" + shared, ActiveDigest: shared},
	)
	job := vmJob("registry.example.com/vm/x:1@"+shared, nil)
	job.Characteristics["arch"] = characteristics.StringValue("amd64")
	_, err := ResolveJob(context.Background(), st, job)
	wantRefused(t, err, CodeImageAlias, "no vm image alias")

	// The arm64 job gets the arm64 alias reference.
	job = vmJob("registry.example.com/vm/x:1@"+shared, nil)
	job.Characteristics["arch"] = characteristics.StringValue("arm64")
	if _, err := ResolveJob(context.Background(), st, job); err != nil {
		t.Fatal(err)
	}
	if job.RunnerImage != "registry.example.com/vm/arm:1@"+shared {
		t.Fatalf("image = %q", job.RunnerImage)
	}
}

// TestResolveRefusesJobsThatCanNeverRun: the os, the arch, a
// characteristic, or the worker class of the job conflicts with the alias.
func TestResolveRefusesJobsThatCanNeverRun(t *testing.T) {
	st := newFake()
	st.aliases = append(st.aliases, models.ImageAlias{OrgID: orgID, Name: "mac-xcode", Kind: models.ImageAliasKindVM, OS: "macos", Arch: "arm64",
		Characteristics: characteristics.Characteristics{
			"worker_class": characteristics.StringValue("mac-xcode"),
			"xcode":        characteristics.StringValue("16"),
		},
		ActiveRef: "registry.example.com/vm/mac:1@" + digestNone, ActiveDigest: digestNone})
	mac := func(chars map[string]string, class string) *models.Job {
		job := vmJob("mac-xcode", nil)
		job.Characteristics["os"] = characteristics.StringValue("macos")
		for k, v := range chars {
			job.Characteristics[k] = characteristics.StringValue(v)
		}
		job.WorkerClass = class
		return job
	}
	// Each message names the alias, the key, the value of the job, the
	// value of the alias, and the fix.
	_, err := ResolveJob(context.Background(), st, vmJob("mac-xcode", nil)) // os windows
	wantRefused(t, err, CodeImageAlias, `image alias "mac-xcode" is for os "macos", but the job needs os "windows": no worker can run the job. Set the job characteristic os to "macos", or use an image alias for os "windows"`)
	_, err = ResolveJob(context.Background(), st, mac(map[string]string{"arch": "amd64"}, "mac-xcode"))
	wantRefused(t, err, CodeImageAlias, `image alias "mac-xcode" is for arch "arm64", but the job needs arch "amd64": no worker can run the job. Set the job characteristic arch to "arm64"`)
	_, err = ResolveJob(context.Background(), st, mac(map[string]string{"xcode": "15"}, "mac-xcode"))
	wantRefused(t, err, CodeImageAlias, `image alias "mac-xcode" needs the worker characteristic xcode=16, but the job needs xcode=15: no worker can run the job. Set the job characteristic xcode to 16, remove it, or use another image alias`)
	_, err = ResolveJob(context.Background(), st, mac(nil, ""))
	wantRefused(t, err, CodeImageAlias, `image alias "mac-xcode" is only for worker class "mac-xcode", but the job uses worker class "default": no worker can run the job. Set the job worker_class to "mac-xcode"`)

	// A job that fits: same platform, same class, no conflicting value.
	job := mac(map[string]string{"arch": "arm64", "xcode": "16"}, "mac-xcode")
	if _, err := ResolveJob(context.Background(), st, job); err != nil {
		t.Fatalf("a job that fits: %v", err)
	}
	// A direct pinned reference uses the same checks.
	_, err = ResolveJob(context.Background(), st, mac(nil, "other"))
	wantRefused(t, err, CodeImageAlias, "worker class")
	direct := mac(nil, "other")
	direct.RunnerImage = "registry.example.com/vm/mac:1@" + digestNone
	_, err = ResolveJob(context.Background(), st, direct)
	wantRefused(t, err, CodeImageAlias, "worker class")
}

// TestCheckCarried: a retry, a rerun, or an inherited image keeps the
// stored reference, but is refused when no worker can get the image.
func TestCheckCarried(t *testing.T) {
	st := newFake()
	carriedJob := func(alias *string, digest string, project *string) *models.Job {
		job := vmJob("", project)
		ApplyCarried(job, Carried{Ref: "registry.example.com/vm/win:old@" + digest, Alias: alias, Digest: digest})
		return job
	}
	old := "sha256:" + strings.Repeat("9", 64)
	// The digest is still pinned: accepted.
	if err := CheckCarried(context.Background(), st, carriedJob(strp("windows-vs2022"), digestOrg, nil)); err != nil {
		t.Fatal(err)
	}
	// An old digest of an alias that still exists with the same platform:
	// the drain of the alias keeps it (this job is a draining job).
	if err := CheckCarried(context.Background(), st, carriedJob(strp("windows-vs2022"), old, nil)); err != nil {
		t.Fatal(err)
	}
	// The project alias was deleted, but the organization alias with the
	// same name and platform exists: the drain maps the job to it.
	st.aliases = append(st.aliases[:1], st.aliases[2:]...) // remove the project alias
	if err := CheckCarried(context.Background(), st, carriedJob(strp("windows-vs2022"), digestProject, strp(projectID))); err != nil {
		t.Fatalf("the organization alias of the same name keeps the digest: %v", err)
	}
	// The organization alias is now for another platform: refused.
	st.aliases[0].Arch = "arm64"
	job := carriedJob(strp("windows-vs2022"), old, nil)
	job.Characteristics["arch"] = characteristics.StringValue("amd64")
	wantRefused(t, CheckCarried(context.Background(), st, job), CodeImageAlias, "is now a vm image for windows/arm64")
	// The alias was deleted: refused.
	st.aliases = st.aliases[1:]
	wantRefused(t, CheckCarried(context.Background(), st, carriedJob(strp("windows-vs2022"), old, nil)), CodeImageAlias, "does not exist any more")
	// A direct reference whose digest no alias pins any more: refused.
	wantRefused(t, CheckCarried(context.Background(), st, carriedJob(nil, old, nil)), CodeImageAlias, "pins "+old)
	// No digest: not checked.
	if err := CheckCarried(context.Background(), st, containerJob("alpine")); err != nil {
		t.Fatal(err)
	}
}

func TestPolicyDenial(t *testing.T) {
	st := newFake()
	st.policy = &models.ImagePolicy{Enabled: true, AllowedPrefixes: []string{"ghcr.io/acme/"}}
	_, err := ResolveJob(context.Background(), st, vmJob("windows-vs2022", nil))
	wantRefused(t, err, CodeImagePolicy, "registry.example.com")

	// The policy checks the resolved reference, not the alias name.
	job := containerJob("builder")
	if _, err := ResolveJob(context.Background(), st, job); err != nil {
		t.Fatalf("an allowed alias reference: %v", err)
	}

	// oci:// matches a registry prefix.
	st.policy.AllowedPrefixes = []string{"registry.example.com/vm/"}
	if _, err := ResolveJob(context.Background(), st, vmJob("windows-vs2022", nil)); err != nil {
		t.Fatalf("oci:// reference under an allowed prefix: %v", err)
	}
	_, err = ResolveJob(context.Background(), st, vmJob("windows-vs2022", strp(projectID)))
	wantRefused(t, err, CodeImagePolicy, "registry.example.com/team/")
}

func TestStoreErrorIsNotARefusal(t *testing.T) {
	st := newFake()
	st.err = store.ErrServiceUnavailable
	_, err := ResolveJob(context.Background(), st, vmJob("windows-vs2022", nil))
	if err == nil || !errors.Is(err, store.ErrServiceUnavailable) {
		t.Fatalf("want the store error, got %v", err)
	}
	if _, refused := IsRefused(err); refused {
		t.Fatal("a store error must not be a refusal")
	}
}

func TestStoreWithoutAliasesChangesNothing(t *testing.T) {
	job := vmJob("windows-vs2022", nil)
	if _, err := ResolveJob(context.Background(), struct{}{}, job); err != nil {
		t.Fatal(err)
	}
	if job.RunnerImage != "windows-vs2022" || job.ImageAlias != nil || digestOf(job) != "" {
		t.Fatal("a store without aliases must not change the job")
	}
}

func TestInheritedDigestIsRemovedAndParentMapUnchanged(t *testing.T) {
	st := newFake()
	parent := characteristics.Characteristics{
		"os":                 characteristics.StringValue("linux"),
		DigestCharacteristic: characteristics.StringValue(digestCont),
	}
	job := containerJob("ghcr.io/acme/other:1")
	job.Characteristics = parent
	job.ImageAlias = strp("forged")
	if _, err := ResolveJob(context.Background(), st, job); err != nil {
		t.Fatal(err)
	}
	if digestOf(job) != "" || job.ImageAlias != nil {
		t.Fatalf("inherited digest or alias kept: %q %v", digestOf(job), job.ImageAlias)
	}
	if _, ok := parent[DigestCharacteristic]; !ok {
		t.Fatal("the parent map must not change")
	}

	shared := characteristics.Characteristics{"os": characteristics.StringValue("linux")}
	job = containerJob("builder")
	job.Characteristics = shared
	if _, err := ResolveJob(context.Background(), st, job); err != nil {
		t.Fatal(err)
	}
	if _, ok := shared[DigestCharacteristic]; ok {
		t.Fatal("setDigest must not change a shared map")
	}
}

// TestCarriedKeepsValues checks the retry and re-run path: the carried
// values of an earlier run go to the new job, and the alias is not resolved
// again (the alias pin changed after the first run).
func TestCarriedKeepsValues(t *testing.T) {
	st := newFake()
	first := vmJob("windows-vs2022", nil)
	if _, err := ResolveJob(context.Background(), st, first); err != nil {
		t.Fatal(err)
	}
	carried := CarriedFromJob(first)
	if carried.Ref != JobReference(st.aliases[0].ActiveRef) || carried.Alias == nil || carried.Digest != digestOrg {
		t.Fatalf("carried = %+v", carried)
	}

	// The alias moves to a new pin.
	st.aliases[0].ActiveRef = "oci://registry.example.com/vm/win:next@" + digestPending
	st.aliases[0].ActiveDigest = digestPending

	again := vmJob("windows-vs2022", nil)
	ApplyCarried(again, carried)
	if again.RunnerImage != carried.Ref || *again.ImageAlias != "windows-vs2022" || digestOf(again) != digestOrg {
		t.Fatalf("carried job: %q %v %q", again.RunnerImage, again.ImageAlias, digestOf(again))
	}

	// No digest in the carried values removes an inherited digest.
	plain := containerJob("x")
	plain.Characteristics[DigestCharacteristic] = characteristics.StringValue(digestCont)
	ApplyCarried(plain, Carried{Ref: "ghcr.io/acme/app:1"})
	if digestOf(plain) != "" || plain.ImageAlias != nil || plain.RunnerImage != "ghcr.io/acme/app:1" {
		t.Fatalf("plain carried job: %q %v %q", plain.RunnerImage, plain.ImageAlias, digestOf(plain))
	}
}

func TestJobReference(t *testing.T) {
	cases := map[string]string{
		"oci://registry.example.com/vm/win:1@" + digestOrg: "registry.example.com/vm/win:1@" + digestOrg,
		"s3://bucket/vm/win:1@" + digestOrg:                "s3://bucket/vm/win:1@" + digestOrg,
		"ghcr.io/acme/app:1":                               "ghcr.io/acme/app:1",
		"not a reference":                                  "not a reference",
	}
	for in, want := range cases {
		if got := JobReference(in); got != want {
			t.Errorf("JobReference(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDisplay(t *testing.T) {
	if got := Display("ghcr.io/a/b:1", nil); got != "ghcr.io/a/b:1" {
		t.Fatalf("Display = %q", got)
	}
	if got := Display("ghcr.io/a/b:1", strp("")); got != "ghcr.io/a/b:1" {
		t.Fatalf("Display with empty alias = %q", got)
	}
	if got := Display("oci://r/x:1@"+digestOrg, strp("win")); got != "win (oci://r/x:1@"+digestOrg+")" {
		t.Fatalf("Display = %q", got)
	}
	if DisplayJob(nil) != "" || DisplayJob(&models.Job{}) != "" {
		t.Fatal("DisplayJob of a job without an image must be empty")
	}
}

func TestIsVMJob(t *testing.T) {
	for osName, want := range map[string]bool{"windows": true, "macos": true, "linux": false, "darwin": false, "": false} {
		job := &models.Job{Characteristics: characteristics.Characteristics{}}
		if osName != "" {
			job.Characteristics["os"] = characteristics.StringValue(osName)
		}
		if IsVMJob(job) != want {
			t.Errorf("IsVMJob(os=%q) = %v, want %v", osName, !want, want)
		}
	}
}

// TestResolveRetry: a retry or a rerun resolves the image of the earlier
// run again through its alias and gets the current pin (owner decision
// 2026-11-18).
func TestResolveRetry(t *testing.T) {
	old := "sha256:" + strings.Repeat("9", 64)
	oldRef := "registry.example.com/vm/win:old@" + old
	retry := func(st *fakeStore, earlier Carried, project *string) (*models.Job, RetryResult, error) {
		job := vmJob("", project)
		result, err := ResolveRetry(context.Background(), st, job, earlier)
		return job, result, err
	}

	t.Run("alias with a new pin", func(t *testing.T) {
		st := newFake()
		job, result, err := retry(st, Carried{Ref: oldRef, Alias: strp("windows-vs2022"), Digest: old}, nil)
		if err != nil {
			t.Fatal(err)
		}
		wantRef := JobReference(st.aliases[0].ActiveRef)
		if job.RunnerImage != wantRef || digestOf(job) != digestOrg || !result.Changed {
			t.Fatalf("image %q digest %q result %+v", job.RunnerImage, digestOf(job), result)
		}
		if DisplayJob(job) != "windows-vs2022 ("+wantRef+")" {
			t.Fatalf("DisplayJob = %q", DisplayJob(job))
		}
		if job.ImageRetryOf == nil || *job.ImageRetryOf != "windows-vs2022 ("+oldRef+")" {
			t.Fatalf("image_retry_of = %v", job.ImageRetryOf)
		}
	})

	t.Run("alias with the same pin", func(t *testing.T) {
		st := newFake()
		ref := JobReference(st.aliases[0].ActiveRef)
		job, result, err := retry(st, Carried{Ref: ref, Alias: strp("windows-vs2022"), Digest: digestOrg}, nil)
		if err != nil || result.Changed || job.ImageRetryOf != nil || job.RunnerImage != ref {
			t.Fatalf("err %v result %+v job %+v", err, result, job)
		}
	})

	t.Run("project alias resolves first", func(t *testing.T) {
		st := newFake()
		job, _, err := retry(st, Carried{Ref: oldRef, Alias: strp("windows-vs2022"), Digest: old}, strp(projectID))
		if err != nil || digestOf(job) != digestProject {
			t.Fatalf("err %v digest %q", err, digestOf(job))
		}
	})

	t.Run("direct reference keeps a digest that the alias still pins", func(t *testing.T) {
		st := newFake()
		pendingRef := JobReference(*st.aliases[0].PendingRef)
		job, result, err := retry(st, Carried{Ref: pendingRef, PinAlias: strp("windows-vs2022"), Digest: digestPending}, nil)
		if err != nil || result.Changed || job.RunnerImage != pendingRef || digestOf(job) != digestPending || job.ImageAlias != nil {
			t.Fatalf("err %v result %+v job %+v", err, result, job)
		}
		if job.ImagePinAlias == nil || *job.ImagePinAlias != "windows-vs2022" {
			t.Fatalf("image_pin_alias = %v", job.ImagePinAlias)
		}
	})

	t.Run("direct reference after an alias update", func(t *testing.T) {
		st := newFake()
		job, result, err := retry(st, Carried{Ref: oldRef, PinAlias: strp("windows-vs2022"), Digest: old}, nil)
		if err != nil || !result.Changed || digestOf(job) != digestOrg {
			t.Fatalf("err %v result %+v digest %q", err, result, digestOf(job))
		}
		if job.ImageAlias == nil || *job.ImageAlias != "windows-vs2022" || job.ImagePinAlias != nil {
			t.Fatalf("alias %v pin alias %v", job.ImageAlias, job.ImagePinAlias)
		}
		if job.ImageRetryOf == nil || *job.ImageRetryOf != oldRef {
			t.Fatalf("image_retry_of = %v", job.ImageRetryOf)
		}
	})

	t.Run("direct reference from before migration 000072", func(t *testing.T) {
		st := newFake()
		ref := JobReference(st.aliases[0].ActiveRef)
		job, _, err := retry(st, Carried{Ref: ref, Digest: digestOrg}, nil)
		if err != nil || digestOf(job) != digestOrg || job.ImagePinAlias == nil || *job.ImagePinAlias != "windows-vs2022" {
			t.Fatalf("err %v job %+v", err, job)
		}
		_, _, err = retry(st, Carried{Ref: oldRef, Digest: old}, nil)
		wantRefused(t, err, CodeImageAlias, "does not record the alias that pinned it")
	})

	t.Run("alias deleted", func(t *testing.T) {
		st := newFake()
		st.aliases = st.aliases[2:]
		_, _, err := retry(st, Carried{Ref: oldRef, Alias: strp("windows-vs2022"), Digest: old}, nil)
		wantRefused(t, err, CodeImageAlias, `image alias "windows-vs2022" does not exist any more, so the retry cannot get an image (the image of the earlier run: windows-vs2022 (`+oldRef+`))`)
		_, _, err = retry(st, Carried{Ref: oldRef, PinAlias: strp("windows-vs2022"), Digest: old}, nil)
		wantRefused(t, err, CodeImageAlias, `image alias "windows-vs2022" does not exist any more`)
	})

	t.Run("alias now for another platform", func(t *testing.T) {
		st := newFake()
		st.aliases[0].Arch = "arm64"
		job := vmJob("", nil)
		job.Characteristics["arch"] = characteristics.StringValue("amd64")
		_, err := ResolveRetry(context.Background(), st, job, Carried{Ref: oldRef, Alias: strp("windows-vs2022"), Digest: old})
		wantRefused(t, err, CodeImageAlias, `image alias "windows-vs2022" is now a vm image for windows/arm64`)
	})

	t.Run("no digest keeps the image", func(t *testing.T) {
		st := newFake()
		job := containerJob("")
		result, err := ResolveRetry(context.Background(), st, job, Carried{Ref: "alpine:3.20"})
		if err != nil || result.Alias != nil || job.RunnerImage != "alpine:3.20" {
			t.Fatalf("err %v result %+v image %q", err, result, job.RunnerImage)
		}
	})

	t.Run("policy checks the new pin", func(t *testing.T) {
		st := newFake()
		st.policy = &models.ImagePolicy{Enabled: true, AllowedPrefixes: []string{"ghcr.io/acme/"}}
		_, _, err := retry(st, Carried{Ref: oldRef, Alias: strp("windows-vs2022"), Digest: old}, nil)
		wantRefused(t, err, CodeImagePolicy, "registry.example.com")
	})
}

// TestCarriedPinAlias: a triggered child of a job with a direct pinned
// reference inherits the alias that pinned the digest, so that a retry of
// the child can resolve through it.
func TestCarriedPinAlias(t *testing.T) {
	parent := vmJob("registry.example.com/vm/win:1@"+digestOrg, nil)
	parent.ImagePinAlias = strp("windows-vs2022")
	parent.Characteristics[DigestCharacteristic] = characteristics.StringValue(digestOrg)
	child := vmJob("", nil)
	ApplyCarried(child, CarriedFromJob(parent))
	if child.ImagePinAlias == nil || *child.ImagePinAlias != "windows-vs2022" || child.ImageAlias != nil || digestOf(child) != digestOrg {
		t.Fatalf("child = %+v", child)
	}
}
