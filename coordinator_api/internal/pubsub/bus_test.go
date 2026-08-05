package pubsub

import "testing"

type topicControllerFake struct {
	added   []string
	removed []string
}

func (f *topicControllerFake) AddJobTopic(jobID string)    { f.added = append(f.added, jobID) }
func (f *topicControllerFake) RemoveJobTopic(jobID string) { f.removed = append(f.removed, jobID) }

func TestSubscribeJobControlsTopicAndFiltersEvents(t *testing.T) {
	bus := NewBus(nil, 2)
	controller := &topicControllerFake{}
	bus.SetJobTopicController(controller)
	sub := bus.SubscribeJob("job-a")
	if len(controller.added) != 1 || controller.added[0] != "job-a" {
		t.Fatalf("added topics = %v", controller.added)
	}
	bus.Publish(Event{Type: EventLogAvailable, JobID: "job-b"})
	bus.Publish(Event{Type: EventLogAvailable, JobID: "job-a"})
	select {
	case event := <-sub.Ch:
		if event.JobID != "job-a" {
			t.Fatalf("received event for %q", event.JobID)
		}
	default:
		t.Fatal("matching event was not delivered")
	}
	bus.Unsubscribe(sub)
	if len(controller.removed) != 1 || controller.removed[0] != "job-a" {
		t.Fatalf("removed topics = %v", controller.removed)
	}
}

func TestJobNotifyChannelIsStableAndJobScoped(t *testing.T) {
	first := JobNotifyChannel("job-a")
	if first != JobNotifyChannel("job-a") {
		t.Fatal("job channel is not stable")
	}
	if first == JobNotifyChannel("job-b") {
		t.Fatal("different jobs share a notification channel")
	}
	if len(first) > 63 {
		t.Fatalf("channel is too long for PostgreSQL: %d", len(first))
	}
}

func TestNotifyListenerReferenceCountsJobTopics(t *testing.T) {
	listener := NewNotifyListener(nil, NewBus(nil, 1), nil)
	listener.AddJobTopic("job-a")
	listener.AddJobTopic("job-a")
	if len(listener.desiredJobChannels()) != 1 {
		t.Fatalf("desired channels = %v", listener.desiredJobChannels())
	}
	listener.RemoveJobTopic("job-a")
	if len(listener.desiredJobChannels()) != 1 {
		t.Fatal("the first removal stopped a topic that still had a viewer")
	}
	listener.RemoveJobTopic("job-a")
	if len(listener.desiredJobChannels()) != 0 {
		t.Fatal("the last removal did not stop the topic")
	}
}
