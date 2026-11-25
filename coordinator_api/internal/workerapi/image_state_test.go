package workerapi

import (
	"context"
	"strings"
	"testing"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/store/models"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerapi/csilapi"
	clientapi "github.com/catalystcommunity/reactorcide/coordinator_api/internal/workerclient/csilapi"
)

// The fake store records worker image state (store.WorkerImageStore).

func (f *fakeStore) SetWorkerImageSet(_ context.Context, workerID string, version int64, hash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, ok := f.workers[workerID]
	if !ok {
		return store.ErrNotFound
	}
	w.ImageSetVersion, w.ImageSetHash = version, &hash
	return nil
}

func (f *fakeStore) UpdateWorkerImageState(_ context.Context, workerID string, state models.WorkerImageState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, ok := f.workers[workerID]
	if !ok {
		return store.ErrNotFound
	}
	version := state.Version
	w.ImageState, w.ImageStateVersion = &state.State, &version
	w.ImageStateDetail = nil
	if state.Detail != "" {
		detail := state.Detail
		w.ImageStateDetail = &detail
	}
	w.ImageReadyDigests = append([]string{}, state.ReadyDigests...)
	w.ImageFailed = append([]models.ImageFailure(nil), state.Failed...)
	return nil
}

func (f *fakeStore) GetWorkerImageState(_ context.Context, workerID string) (*models.WorkerImageState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, ok := f.workers[workerID]
	if !ok {
		return nil, store.ErrNotFound
	}
	if w.ImageState == nil {
		return nil, nil
	}
	state := &models.WorkerImageState{State: *w.ImageState, ReadyDigests: w.ImageReadyDigests, Failed: w.ImageFailed}
	if w.ImageStateVersion != nil {
		state.Version = *w.ImageStateVersion
	}
	if w.ImageStateDetail != nil {
		state.Detail = *w.ImageStateDetail
	}
	return state, nil
}

var _ store.WorkerImageStore = (*fakeStore)(nil)

var (
	imgDigestA = "sha256:" + strings.Repeat("a", 64)
	imgDigestB = "sha256:" + strings.Repeat("b", 64)
)

func strPtrImage(s string) *string { return &s }

func TestHeartbeat_StoresImageSetReport(t *testing.T) {
	h := newTestHarness()
	token, workerID := h.registerWorker(t, "image-worker", "windows", "amd64", nil)

	// An old worker sends no report: nothing is stored, the response is as before.
	resp, err := h.service.Heartbeat(ctxWithAuth(token), csilapi.HeartbeatRequest{Status: "idle"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ImageSet != nil {
		t.Fatal("a worker without a report must get no image set")
	}
	if state, _ := h.store.GetWorkerImageState(context.Background(), workerID); state != nil {
		t.Fatalf("no report must store no state, got %+v", state)
	}

	free := int64(100)
	resp, err = h.service.Heartbeat(ctxWithAuth(token), csilapi.HeartbeatRequest{
		Status: "idle",
		ImageSetReport: &csilapi.ImageSetReport{
			Version: 3, State: "failed", Detail: strPtrImage("no space left on device"),
			ReadyDigests: []string{imgDigestA, imgDigestB, imgDigestA}, FreeBytes: &free,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ImageSet != nil {
		t.Fatal("a store without image set tables must send no image set")
	}
	state, err := h.store.GetWorkerImageState(context.Background(), workerID)
	if err != nil || state == nil {
		t.Fatalf("state = %+v err = %v", state, err)
	}
	if state.State != "failed" || state.Version != 3 || state.Detail != "no space left on device" ||
		len(state.ReadyDigests) != 2 || state.ReadyDigests[0] != imgDigestA || state.ReadyDigests[1] != imgDigestB {
		t.Fatalf("stored state = %+v", state)
	}

	// An invalid report does not fail the heartbeat and does not change the state.
	for _, bad := range []csilapi.ImageSetReport{
		{Version: 4, State: "ready", ReadyDigests: []string{}},
		{Version: -1, State: "updated", ReadyDigests: []string{}},
		{Version: 4, State: "updated", ReadyDigests: []string{"sha256:" + strings.Repeat("A", 64)}},
	} {
		bad := bad
		if _, err := h.service.Heartbeat(ctxWithAuth(token), csilapi.HeartbeatRequest{Status: "idle", ImageSetReport: &bad}); err != nil {
			t.Fatalf("an invalid report must not fail the heartbeat: %v", err)
		}
	}
	after, _ := h.store.GetWorkerImageState(context.Background(), workerID)
	if after.State != "failed" || after.Version != 3 {
		t.Fatalf("an invalid report changed the state: %+v", after)
	}
}

// The per-image failures of a report are stored; a failure with a digest
// that is not valid makes the report not valid.
func TestImageSetReportStateFailures(t *testing.T) {
	state, ok := imageSetReportState(&csilapi.ImageSetReport{
		Version: 2, State: "failed", ReadyDigests: []string{imgDigestA},
		Failed: []csilapi.ImageFailure{
			{Digest: imgDigestB, Permanent: true, Detail: strings.Repeat("x", 2000)},
			{Digest: imgDigestB, Permanent: false, Detail: "duplicate"},
		},
	})
	if !ok || len(state.Failed) != 1 || !state.Failed[0].Permanent || len(state.Failed[0].Detail) != maxImageFailureDetailLen {
		t.Fatalf("state = %+v ok = %v", state, ok)
	}
	if _, ok := imageSetReportState(&csilapi.ImageSetReport{Version: 2, State: "failed", ReadyDigests: []string{},
		Failed: []csilapi.ImageFailure{{Digest: "sha256:bad"}}}); ok {
		t.Fatal("a failure with a digest that is not valid must make the report not valid")
	}
}

func TestImageSetReportStateCutsDetail(t *testing.T) {
	long := strings.Repeat("é", maxImageReportDetailLen)
	state, ok := imageSetReportState(&csilapi.ImageSetReport{Version: 1, State: "updating", Detail: &long, ReadyDigests: nil})
	if !ok || len(state.Detail) > maxImageReportDetailLen || !strings.HasPrefix(long, state.Detail) {
		t.Fatalf("detail not cut on a character boundary: %d bytes ok=%v", len(state.Detail), ok)
	}
	if state.ReadyDigests == nil {
		t.Fatal("ready digests must be an empty list, not nil")
	}
}

// TestImageSetWireRoundTrip encodes each new message with the worker client
// codec and decodes it with the coordinator codec (and the reverse), and
// checks that a message without the new optional fields decodes to nil.
func TestImageSetWireRoundTrip(t *testing.T) {
	free, required := int64(1<<40), int64(80<<30)
	detail := "pulling"
	req := clientapi.HeartbeatRequest{Status: "running", RunningLeases: []clientapi.RunningLease{},
		ImageSetReport: &clientapi.ImageSetReport{Version: 7, State: "updating", Detail: &detail,
			ReadyDigests: []string{imgDigestA}, FreeBytes: &free, RequiredBytes: &required}}
	gotReq, err := csilapi.DecodeHeartbeatRequest(mustEncode(clientapi.EncodeHeartbeatRequest(req)))
	if err != nil {
		t.Fatal(err)
	}
	r := gotReq.ImageSetReport
	if r == nil || r.Version != 7 || r.State != "updating" || *r.Detail != detail || r.ReadyDigests[0] != imgDigestA ||
		*r.FreeBytes != free || *r.RequiredBytes != required {
		t.Fatalf("decoded report = %+v", r)
	}

	set := &csilapi.ImageSet{Version: 7, Images: []csilapi.ImageSetEntry{
		{Ref: "oci://r.example/vm/win:1@" + imgDigestA, Digest: imgDigestA, Kind: "vm", Aliases: []string{"windows-vs2022"}},
	}}
	gotResp, err := clientapi.DecodeHeartbeatResponse(mustEncode(csilapi.EncodeHeartbeatResponse(csilapi.HeartbeatResponse{
		Directives: []csilapi.Directive{}, ImageSet: set})))
	if err != nil {
		t.Fatal(err)
	}
	if gotResp.ImageSet == nil || gotResp.ImageSet.Version != 7 || len(gotResp.ImageSet.Images) != 1 ||
		gotResp.ImageSet.Images[0].Aliases[0] != "windows-vs2022" || gotResp.ImageSet.Images[0].Kind != "vm" {
		t.Fatalf("decoded heartbeat image set = %+v", gotResp.ImageSet)
	}
	gotReg, err := clientapi.DecodeRegisterResponse(mustEncode(csilapi.EncodeRegisterResponse(csilapi.RegisterResponse{
		WorkerSession: "s", WorkerId: "w", HeartbeatInterval: 10, ImageSet: set})))
	if err != nil {
		t.Fatal(err)
	}
	if gotReg.ImageSet == nil || gotReg.ImageSet.Images[0].Digest != imgDigestA {
		t.Fatalf("decoded register image set = %+v", gotReg.ImageSet)
	}

	// Old messages: no optional field.
	oldReq, err := csilapi.DecodeHeartbeatRequest(mustEncode(clientapi.EncodeHeartbeatRequest(clientapi.HeartbeatRequest{Status: "idle", RunningLeases: []clientapi.RunningLease{}})))
	if err != nil || oldReq.ImageSetReport != nil {
		t.Fatalf("old heartbeat request: %+v %v", oldReq, err)
	}
	oldResp, err := clientapi.DecodeHeartbeatResponse(mustEncode(csilapi.EncodeHeartbeatResponse(csilapi.HeartbeatResponse{Directives: []csilapi.Directive{}})))
	if err != nil || oldResp.ImageSet != nil {
		t.Fatalf("old heartbeat response: %+v %v", oldResp, err)
	}
	oldReg, err := clientapi.DecodeRegisterResponse(mustEncode(csilapi.EncodeRegisterResponse(csilapi.RegisterResponse{WorkerSession: "s", WorkerId: "w"})))
	if err != nil || oldReg.ImageSet != nil {
		t.Fatalf("old register response: %+v %v", oldReg, err)
	}
}

// mustEncode returns the bytes of a generated encoder, or panics. A test
// fixture is always a valid value, so an error is a defect in the test.
func mustEncode(data []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return data
}
