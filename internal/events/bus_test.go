package events

import (
	"testing"
	"time"
)

func TestBus_PublishToSubscriber(t *testing.T) {
	bus := NewBus()
	_, ch, unsub := bus.Subscribe()
	defer unsub()

	bus.Publish("test:event", map[string]string{"key": "value"})

	select {
	case event := <-ch:
		if event.Type != "test:event" {
			t.Errorf("expected type test:event, got %s", event.Type)
		}
		if event.ID == 0 {
			t.Error("event ID should be > 0")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}
}

func TestBus_MultipleSubscribers(t *testing.T) {
	bus := NewBus()
	_, ch1, unsub1 := bus.Subscribe()
	defer unsub1()
	_, ch2, unsub2 := bus.Subscribe()
	defer unsub2()

	bus.Publish("test:event", "hello")

	for i, ch := range []<-chan Event{ch1, ch2} {
		select {
		case event := <-ch:
			if event.Type != "test:event" {
				t.Errorf("subscriber %d: expected test:event, got %s", i, event.Type)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d: timeout", i)
		}
	}
}

func TestBus_Unsubscribe(t *testing.T) {
	bus := NewBus()
	_, _, unsub := bus.Subscribe()
	unsub()

	// Should not panic
	bus.Publish("test:event", "data")

	if bus.SubscriberCount() != 0 {
		t.Errorf("expected 0 subscribers after unsubscribe, got %d", bus.SubscriberCount())
	}
}

func TestBus_MonotonicIDs(t *testing.T) {
	bus := NewBus()
	_, ch, unsub := bus.Subscribe()
	defer unsub()

	bus.Publish("a", nil)
	bus.Publish("b", nil)

	e1 := <-ch
	e2 := <-ch
	if e2.ID <= e1.ID {
		t.Errorf("IDs should be monotonic: %d <= %d", e2.ID, e1.ID)
	}
}

func TestBus_SlowSubscriberDropsEvents(t *testing.T) {
	bus := NewBus()
	_, ch, unsub := bus.Subscribe()
	defer unsub()

	// Fill buffer (64) + overflow
	for i := 0; i < 100; i++ {
		bus.Publish("flood", i)
	}

	count := 0
	for {
		select {
		case <-ch:
			count++
		default:
			goto done
		}
	}
done:
	if count >= 100 {
		t.Errorf("slow subscriber should drop events, got all %d", count)
	}
	if count == 0 {
		t.Error("should have received some events")
	}
}

func TestBus_PublishToZeroSubscribers(t *testing.T) {
	bus := NewBus()
	// Should not panic
	bus.Publish("test:event", "data")
}

func TestBus_DoubleUnsubscribe(t *testing.T) {
	bus := NewBus()
	_, _, unsub := bus.Subscribe()
	unsub()
	// Should not panic
	unsub()
}
