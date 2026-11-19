package coordinatorworker

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient/csilapi"
)

// TestJobLogHeader covers owner decision I2 for the job log: "alias (ref)"
// for an alias, the ref alone for a VM job, nothing for a plain container
// job.
func TestJobLogHeader(t *testing.T) {
	alias := "windows-vs2022"
	empty := ""
	const ref = "oci://registry.example/win@sha256:abc"

	require.Nil(t, jobLogHeader(&fakeRunner{}, "alpine:3", nil, nil), "a container job without an alias gets no header")
	require.Nil(t, jobLogHeader(&fakeRunner{}, "alpine:3", &empty, &empty), "an empty alias is no alias")
	require.Nil(t, jobLogHeader(newFakeSeeder(), "alpine:3", nil, nil), "a container image-set backend is not a VM")
	require.Equal(t, []string{"[reactorcide] image: windows-vs2022 (" + ref + ")"}, jobLogHeader(&fakeRunner{}, ref, &alias, nil))
	require.Equal(t, []string{"[reactorcide] image: " + ref}, jobLogHeader(vmKindSeeder{newFakeSeeder()}, ref, nil, nil))
	require.Equal(t, []string{"[reactorcide] image: windows-vs2022 (" + ref + ")"}, jobLogHeader(vmKindSeeder{newFakeSeeder()}, ref, &alias, nil))
	require.Nil(t, jobLogHeader(vmKindSeeder{newFakeSeeder()}, "", &alias, nil), "no image, no header")

	// A retry or a re-run that changed the image adds a second line.
	previous := "windows-vs2022 (oci://registry.example/win@sha256:old)"
	require.Equal(t, []string{
		"[reactorcide] image: windows-vs2022 (" + ref + ")",
		"[reactorcide] retry of: " + previous,
	}, jobLogHeader(vmKindSeeder{newFakeSeeder()}, ref, &alias, &previous))
	// A container job with a pinned direct reference has no alias, but a
	// changed image still gets the header.
	require.Equal(t, []string{
		"[reactorcide] image: alpine@sha256:new",
		"[reactorcide] retry of: alpine@sha256:old",
	}, jobLogHeader(&fakeRunner{}, "alpine@sha256:new", nil, strPtrTest("alpine@sha256:old")))
	require.Nil(t, jobLogHeader(&fakeRunner{}, "", nil, &previous), "no image, no header")
}

func strPtrTest(s string) *string { return &s }

// TestRunLoop_JobLogStartsWithImageDisplay asserts the header line is the
// first line of the stdout job log stream, before the job output.
func TestRunLoop_JobLogStartsWithImageDisplay(t *testing.T) {
	alias := "build-box"
	retryOf := "build-box (registry.example/build@sha256:old)"
	lease := csilapi.Lease{
		LeaseId:            "lease-1",
		JobId:              "job-1",
		Image:              "registry.example/build@sha256:def",
		ImageAlias:         &alias,
		ImageRetryOf:       &retryOf,
		Command:            []string{"sh", "-c", "echo hi"},
		WorkingDir:         "/job",
		TimeoutSeconds:     30,
		CancelGraceSeconds: 5,
	}
	fc := &fakeClient{}
	fc.RequestJobFunc = singleLeaseThenNone(lease)
	fr := &fakeRunner{Stdout: io.NopCloser(strings.NewReader("first job line\nsecond job line\n"))}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runLoop(ctx, testConfig(), fc, runnerFactoryFor(fr)) }()
	require.Eventually(t, func() bool { return len(fc.snapshotReportResults()) == 1 }, 5*time.Second, 20*time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)

	var stdout []string
	for _, l := range fc.snapshotAppendLogs() {
		if l.Stream == "stdout" {
			stdout = append(stdout, l.Chunk)
		}
	}
	require.Equal(t, []string{
		"[reactorcide] image: build-box (registry.example/build@sha256:def)\n",
		"[reactorcide] retry of: build-box (registry.example/build@sha256:old)\n",
		"first job line\n",
		"second job line\n",
	}, stdout)
}
