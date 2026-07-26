package corndogs

import (
	"context"
	"testing"
	"time"

	csil "github.com/catalystcommunity/reactorcide/coordinator_api/internal/corndogs/csilapi"
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
				Task: &csil.Task{Uuid: "task-1", Queue: decoded.Queues[1], CurrentState: "submitted-working"},
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
	if task.Uuid != "task-1" || task.Queue != queues[1] {
		t.Errorf("unexpected task: %+v", task)
	}
}

func TestGetNextTaskGroup_ReturnsNilOnEmptyGroup(t *testing.T) {
	transport := &fakeTransport{
		respond: func(service, op string, req []byte) ([]byte, error) {
			// The server returns a response with no Task (not an error) when no
			// task is available anywhere in the group.
			return csil.EncodeGetNextTaskGroupResponse(csil.GetNextTaskGroupResponse{Task: nil}), nil
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
