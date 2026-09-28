package corndogs

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	csil "github.com/CatalystCommunity/corndogs/clients/corndogs"
)

// fakeTransport is a minimal csil.Transport implementation for exercising the
// generated CorndogsClient without a real corndogs server. It records every call
// (service, op, and raw request bytes) and returns a caller-supplied response.
type fakeTransport struct {
	calls []fakeCall
	// respond is invoked for each call and returns the raw response bytes (or an
	// error) to hand back to the generated client.
	respond func(service, op string, req []byte) ([]byte, error)
}

type fakeCall struct {
	service string
	op      string
	req     []byte
}

func (f *fakeTransport) Call(_ context.Context, service, op string, req []byte) ([]byte, error) {
	f.calls = append(f.calls, fakeCall{service: service, op: op, req: req})
	return f.respond(service, op, req)
}

func newTestClient(t *testing.T, transport *fakeTransport, queueName string) *Client {
	t.Helper()
	return &Client{
		client: csil.NewCorndogsClient(transport),
		config: Config{
			QueueName: queueName,
			Timeout:   30 * time.Second,
		},
	}
}

func TestSubmitTaskToQueue_SendsGivenQueueNotConfigDefault(t *testing.T) {
	const configuredQueue = "reactorcide-jobs"
	const explicitQueue = "11111111-1111-1111-1111-111111111111"

	transport := &fakeTransport{
		respond: func(service, op string, req []byte) ([]byte, error) {
			if service != "CorndogsService" {
				t.Errorf("expected service %q, got %q", "CorndogsService", service)
			}
			if op != "SubmitTask" {
				t.Errorf("expected op %q, got %q", "SubmitTask", op)
			}
			decoded, err := csil.DecodeSubmitTaskRequest(req)
			if err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if decoded.Queue != explicitQueue {
				t.Errorf("expected queue %q, got %q", explicitQueue, decoded.Queue)
			}
			if decoded.Queue == configuredQueue {
				t.Errorf("SubmitTaskToQueue must not fall back to the client's configured queue")
			}
			return csil.EncodeSubmitTaskResponse(csil.SubmitTaskResponse{
				Task: &csil.Task{Uuid: "task-1", Queue: decoded.Queue, CurrentState: "submitted"},
			}), nil
		},
	}

	c := newTestClient(t, transport, configuredQueue)

	task, err := c.SubmitTaskToQueue(context.Background(), explicitQueue, &TaskPayload{JobID: "job-1"}, 5)
	if err != nil {
		t.Fatalf("SubmitTaskToQueue returned error: %v", err)
	}
	if task == nil || task.Queue != explicitQueue {
		t.Fatalf("expected task on queue %q, got %+v", explicitQueue, task)
	}
	if len(transport.calls) != 1 {
		t.Fatalf("expected exactly 1 transport call, got %d", len(transport.calls))
	}
}

func TestSubmitTask_UsesConfiguredQueue(t *testing.T) {
	const configuredQueue = "reactorcide-jobs"

	transport := &fakeTransport{
		respond: func(service, op string, req []byte) ([]byte, error) {
			decoded, err := csil.DecodeSubmitTaskRequest(req)
			if err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if decoded.Queue != configuredQueue {
				t.Errorf("expected queue %q, got %q", configuredQueue, decoded.Queue)
			}
			return csil.EncodeSubmitTaskResponse(csil.SubmitTaskResponse{
				Task: &csil.Task{Uuid: "task-1", Queue: decoded.Queue, CurrentState: "submitted"},
			}), nil
		},
	}

	c := newTestClient(t, transport, configuredQueue)

	if _, err := c.SubmitTask(context.Background(), &TaskPayload{JobID: "job-1"}, 5); err != nil {
		t.Fatalf("SubmitTask returned error: %v", err)
	}
}

func TestGetNextTaskGroup_PassesAllQueuesAndReturnsTask(t *testing.T) {
	queues := []string{
		"11111111-1111-1111-1111-111111111111",
		"22222222-2222-2222-2222-222222222222",
		"33333333-3333-3333-3333-333333333333",
	}

	transport := &fakeTransport{
		respond: func(service, op string, req []byte) ([]byte, error) {
			if service != "CorndogsService" {
				t.Errorf("expected service %q, got %q", "CorndogsService", service)
			}
			if op != "GetNextTaskGroup" {
				t.Errorf("expected op %q, got %q", "GetNextTaskGroup", op)
			}
			decoded, err := csil.DecodeGetNextTaskGroupRequest(req)
			if err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if len(decoded.Queues) != len(queues) {
				t.Fatalf("expected %d queues, got %d (%v)", len(queues), len(decoded.Queues), decoded.Queues)
			}
			for i, q := range queues {
				if decoded.Queues[i] != q {
					t.Errorf("queue[%d]: expected %q, got %q", i, q, decoded.Queues[i])
				}
			}
			if decoded.CurrentState != "submitted" {
				t.Errorf("expected current_state %q, got %q", "submitted", decoded.CurrentState)
			}
			if decoded.OverrideTimeout != 42 {
				t.Errorf("expected override_timeout 42, got %d", decoded.OverrideTimeout)
			}
			return csil.EncodeGetNextTaskGroupResponse(csil.GetNextTaskGroupResponse{
				Delivery: &csil.TaskDelivery{
					Task:    csil.Task{Uuid: "task-1", Queue: decoded.Queues[1], CurrentState: "submitted-working", Priority: 7},
					Payload: []byte(`{"job_id":"job-1"}`),
				},
			}), nil
		},
	}

	c := newTestClient(t, transport, "reactorcide-jobs")

	task, err := c.GetNextTaskGroup(context.Background(), queues, "submitted", 42)
	if err != nil {
		t.Fatalf("GetNextTaskGroup returned error: %v", err)
	}
	if task == nil {
		t.Fatal("expected a task, got nil")
	}
	if task.Uuid != "task-1" || task.Queue != queues[1] || task.Priority != 7 {
		t.Errorf("unexpected task: %+v", task)
	}
	payload, err := ParseTaskPayload(task)
	if err != nil {
		t.Fatalf("ParseTaskPayload: %v", err)
	}
	if payload.JobID != "job-1" {
		t.Errorf("expected job_id from the delivery payload, got %q", payload.JobID)
	}
}

func TestGetNextTaskGroup_ReturnsNilOnEmptyGroup(t *testing.T) {
	transport := &fakeTransport{
		respond: func(service, op string, req []byte) ([]byte, error) {
			// The server returns a response with no delivery (not an error) when no
			// task is available anywhere in the group.
			return csil.EncodeGetNextTaskGroupResponse(csil.GetNextTaskGroupResponse{Delivery: nil}), nil
		},
	}

	c := newTestClient(t, transport, "reactorcide-jobs")

	task, err := c.GetNextTaskGroup(context.Background(), []string{"q1", "q2"}, "submitted", 10)
	if err != nil {
		t.Fatalf("expected no error on empty group, got: %v", err)
	}
	if task != nil {
		t.Fatalf("expected nil task on empty group, got: %+v", task)
	}
}

func TestGetNextTask_ReadsTaskAndPayloadFromDelivery(t *testing.T) {
	transport := &fakeTransport{
		respond: func(service, op string, req []byte) ([]byte, error) {
			if op != "GetNextTask" {
				t.Errorf("expected op %q, got %q", "GetNextTask", op)
			}
			return csil.EncodeGetNextTaskResponse(csil.GetNextTaskResponse{
				Delivery: &csil.TaskDelivery{
					Task:    csil.Task{Uuid: "task-2", Queue: "reactorcide-jobs", CurrentState: "submitted-working"},
					Payload: []byte(`{"job_id":"job-2"}`),
				},
			}), nil
		},
	}
	c := newTestClient(t, transport, "reactorcide-jobs")

	task, err := c.GetNextTask(context.Background(), "", 30)
	if err != nil {
		t.Fatalf("GetNextTask: %v", err)
	}
	if task == nil || task.Uuid != "task-2" {
		t.Fatalf("unexpected task: %+v", task)
	}
	payload, err := ParseTaskPayload(task)
	if err != nil || payload.JobID != "job-2" {
		t.Fatalf("expected payload job-2, got %+v (err %v)", payload, err)
	}
}

func TestGetNextTask_ReturnsNilWithoutDelivery(t *testing.T) {
	transport := &fakeTransport{
		respond: func(service, op string, req []byte) ([]byte, error) {
			return csil.EncodeGetNextTaskResponse(csil.GetNextTaskResponse{}), nil
		},
	}
	c := newTestClient(t, transport, "reactorcide-jobs")

	task, err := c.GetNextTask(context.Background(), "submitted", 30)
	if err != nil || task != nil {
		t.Fatalf("expected (nil, nil), got (%+v, %v)", task, err)
	}
}

func decodeUpdate(t *testing.T, req []byte) csil.UpdateTaskRequest {
	t.Helper()
	decoded, err := csil.DecodeUpdateTaskRequest(req)
	if err != nil {
		t.Fatalf("decode UpdateTaskRequest: %v", err)
	}
	return decoded
}

func updateResponder(t *testing.T, got *csil.UpdateTaskRequest) *fakeTransport {
	return &fakeTransport{
		respond: func(service, op string, req []byte) ([]byte, error) {
			if op != "UpdateTask" {
				t.Errorf("expected op %q, got %q", "UpdateTask", op)
			}
			*got = decodeUpdate(t, req)
			return csil.EncodeUpdateTaskResponse(csil.UpdateTaskResponse{
				Task: &csil.Task{Uuid: got.Uuid, Queue: got.Queue, CurrentState: got.NewState},
			}), nil
		},
	}
}

// A nil payload must be absent on the wire so corndogs keeps the stored
// payload, and the priority must never be sent so corndogs keeps the stored
// priority (a requeued task must not drop behind new work).
func TestUpdateTask_NilPayloadAndNoPriorityAreAbsent(t *testing.T) {
	var got csil.UpdateTaskRequest
	c := newTestClient(t, updateResponder(t, &got), "reactorcide-jobs")

	if _, err := c.UpdateTask(context.Background(), "task-1", "processing", "submitted", nil); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	if got.Payload != nil {
		t.Errorf("expected no payload on the wire, got %q", *got.Payload)
	}
	if got.Priority != nil {
		t.Errorf("expected no priority on the wire, got %d", *got.Priority)
	}
	if got.NewState != "submitted" || got.CurrentState != "processing" || got.Queue != "reactorcide-jobs" {
		t.Errorf("unexpected request: %+v", got)
	}
}

func TestUpdateTask_NonNilPayloadIsSent(t *testing.T) {
	var got csil.UpdateTaskRequest
	c := newTestClient(t, updateResponder(t, &got), "reactorcide-jobs")

	if _, err := c.UpdateTask(context.Background(), "task-1", "processing", "processing", []byte("new")); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	if got.Payload == nil || !bytes.Equal(*got.Payload, []byte("new")) {
		t.Errorf("expected payload %q, got %v", "new", got.Payload)
	}
	if got.Priority != nil {
		t.Errorf("expected no priority on the wire, got %d", *got.Priority)
	}
}

func TestSendHeartbeat_KeepsStateAndSendsNoPayloadOrPriority(t *testing.T) {
	var got csil.UpdateTaskRequest
	c := newTestClient(t, updateResponder(t, &got), "reactorcide-jobs")

	if _, err := c.SendHeartbeat(context.Background(), "task-1", "processing", 600); err != nil {
		t.Fatalf("SendHeartbeat: %v", err)
	}
	if got.Payload != nil || got.Priority != nil {
		t.Errorf("heartbeat must not send payload or priority: payload=%v priority=%v", got.Payload, got.Priority)
	}
	if got.CurrentState != "processing" || got.NewState != "processing" || got.Timeout != 600 {
		t.Errorf("unexpected heartbeat request: %+v", got)
	}
}

// TestDialAddrStripsScheme guards the raw-TCP address handling: a
// scheme-bearing REACTORCIDE_CORNDOGS_BASE_URL such as "http://host:5080" must
// be reduced to "host:port" before net.Dial, which otherwise fails with "too
// many colons in address". This took down all k8s job dispatch once.
func TestDialAddrStripsScheme(t *testing.T) {
	cases := map[string]string{
		"http://corndogs.reactorcide.svc.cluster.local:5080": "corndogs.reactorcide.svc.cluster.local:5080",
		"tcp://corndogs:5080":                                "corndogs:5080",
		"corndogs:5080":                                      "corndogs:5080",
		"http://host:5080/path":                              "host:5080",
	}
	for in, want := range cases {
		if got := dialAddr(in); got != want {
			t.Errorf("dialAddr(%q) = %q, want %q", in, got, want)
		}
	}
	seeds := splitSeeds(" http://a:5080 , tcp://b:5080/x ,, c:5080")
	want := []string{"a:5080", "b:5080", "c:5080"}
	if len(seeds) != len(want) {
		t.Fatalf("splitSeeds = %v, want %v", seeds, want)
	}
	for i := range want {
		if seeds[i] != want[i] {
			t.Errorf("splitSeeds[%d] = %q, want %q", i, seeds[i], want[i])
		}
	}
}

// TestSingleAddressClientDialsSchemeBearingAddr guards the single-address path
// end to end. The upstream StreamTransport dials its Addr as given, so
// newCorndogsClient must strip the scheme first.
func TestSingleAddressClientDialsSchemeBearingAddr(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	accepted := make(chan struct{}, 1)
	go func() {
		if conn, err := ln.Accept(); err == nil {
			accepted <- struct{}{}
			_ = conn.Close()
		}
	}()

	client := newCorndogsClient("http://" + ln.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// The listener closes the connection, so the call fails. Only the dial
	// matters here.
	_, _ = client.GetQueues(ctx, csil.GetQueuesRequest{})

	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("listener never accepted a connection (scheme-bearing addr not dialed)")
	}
}
