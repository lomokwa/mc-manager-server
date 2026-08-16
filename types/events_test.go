package types

import (
	"testing"
	"time"
)

func TestBus_DeliversToSubscriber(t *testing.T) {
	b := NewEventBus()
	ch := b.Subscribe()
	defer b.Unsubscribe(ch)

	b.Publish(ConsoleLineEvent{ServerID: "default", Line: "hello"})

	select {
	case ev := <-ch:
		line, ok := ev.(ConsoleLineEvent)
		if !ok {
			t.Fatalf("expected ConsoleLineEvent, got %T", ev)
		}
		if line.Line != "hello" || line.ServerID != "default" {
			t.Errorf("unexpected event: %+v", line)
		}
	case <-time.After(time.Second):
		t.Fatal("event never arrived")
	}
}

// The tailer publishes every console line. A slow consumer must never be able
// to block it -- losing an automation event is acceptable, stalling the live
// console stream is not.
func TestBus_DropsInsteadOfBlockingASlowSubscriber(t *testing.T) {
	b := NewEventBus()
	ch := b.Subscribe()
	defer b.Unsubscribe(ch)

	done := make(chan struct{})
	go func() {
		for i := 0; i < eventBufferSize*3; i++ {
			b.Publish(ConsoleLineEvent{ServerID: "default", Line: "flood"})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a subscriber that never reads")
	}

	if got := b.Dropped(); got == 0 {
		t.Error("expected the overflow to be counted as dropped")
	}
}

func TestBus_UnsubscribeStopsDelivery(t *testing.T) {
	b := NewEventBus()
	ch := b.Subscribe()
	b.Unsubscribe(ch)

	b.Publish(ConsoleLineEvent{ServerID: "default", Line: "after"})

	if _, open := <-ch; open {
		t.Error("expected the channel to be closed after Unsubscribe")
	}
}

// HasSubscribers is what lets the tailer skip allocating an event per console
// line when no engine is listening. If it ever lies, that saving disappears
// silently.
func TestBus_HasSubscribersTracksReality(t *testing.T) {
	b := NewEventBus()
	if b.HasSubscribers() {
		t.Error("a fresh bus has no subscribers")
	}

	ch := b.Subscribe()
	if !b.HasSubscribers() {
		t.Error("expected a subscriber to be visible")
	}

	b.Unsubscribe(ch)
	if b.HasSubscribers() {
		t.Error("expected no subscribers after the only one left")
	}
}
