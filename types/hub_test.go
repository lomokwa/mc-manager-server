package types

import (
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestLogHub_SubscribeReceivesBroadcast(t *testing.T) {
	hub := NewLogHub()
	ch := hub.Subscribe()
	defer hub.Unsubscribe(ch)

	hub.Broadcast("hello")

	select {
	case line := <-ch:
		if line != "hello" {
			t.Errorf("expected 'hello', got %q", line)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for broadcast")
	}
}

func TestLogHub_MultipleSubscribersAllReceive(t *testing.T) {
	hub := NewLogHub()
	ch1 := hub.Subscribe()
	ch2 := hub.Subscribe()
	defer hub.Unsubscribe(ch1)
	defer hub.Unsubscribe(ch2)

	hub.Broadcast("line1")

	for i, ch := range []chan string{ch1, ch2} {
		select {
		case line := <-ch:
			if line != "line1" {
				t.Errorf("subscriber %d: expected 'line1', got %q", i, line)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d: timed out waiting for broadcast", i)
		}
	}
}

func TestLogHub_NewSubscriberReplaysBufferedLinesOldestFirst(t *testing.T) {
	hub := NewLogHub()
	hub.Broadcast("line1")
	hub.Broadcast("line2")
	hub.Broadcast("line3")

	ch := hub.Subscribe()
	defer hub.Unsubscribe(ch)

	want := []string{"line1", "line2", "line3"}
	for i, w := range want {
		select {
		case got := <-ch:
			if got != w {
				t.Errorf("replay[%d]: expected %q, got %q", i, w, got)
			}
		case <-time.After(time.Second):
			t.Fatalf("replay[%d]: timed out", i)
		}
	}
}

func TestLogHub_ReplayBufferIsBoundedAndWrapsCorrectly(t *testing.T) {
	hub := NewLogHub()
	total := replayBufferSize + 10
	for i := 0; i < total; i++ {
		hub.Broadcast(lineName(i))
	}

	ch := hub.Subscribe()
	defer hub.Unsubscribe(ch)

	// Only the most recent replayBufferSize lines should have survived,
	// oldest-of-those-first.
	firstSurviving := total - replayBufferSize
	for i := 0; i < replayBufferSize; i++ {
		select {
		case got := <-ch:
			want := lineName(firstSurviving + i)
			if got != want {
				t.Fatalf("replay[%d]: expected %q, got %q", i, want, got)
			}
		case <-time.After(time.Second):
			t.Fatalf("replay[%d]: timed out", i)
		}
	}

	// No extra buffered lines beyond the cap.
	select {
	case extra := <-ch:
		t.Fatalf("expected no more replayed lines, got %q", extra)
	default:
	}
}

func TestLogHub_UnsubscribeClosesChannelAndStopsDelivery(t *testing.T) {
	hub := NewLogHub()
	ch := hub.Subscribe()
	hub.Unsubscribe(ch)

	if _, ok := <-ch; ok {
		t.Error("expected channel to be closed after Unsubscribe")
	}

	// Broadcasting after unsubscribe must not panic (sending on a closed
	// channel would), since the subscriber map no longer contains ch.
	hub.Broadcast("after-unsubscribe")
}

func TestLogHub_SlowSubscriberDoesNotBlockBroadcast(t *testing.T) {
	hub := NewLogHub()
	ch := hub.Subscribe() // buffered but never drained
	defer hub.Unsubscribe(ch)

	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			hub.Broadcast(lineName(i))
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Broadcast blocked on a slow/full subscriber instead of dropping the message")
	}
}

func TestLogHub_Close_ClosesAllSubscribersAndIsSafeToCallTwice(t *testing.T) {
	hub := NewLogHub()
	ch1 := hub.Subscribe()
	ch2 := hub.Subscribe()

	hub.Close()

	if _, ok := <-ch1; ok {
		t.Error("expected ch1 to be closed after Close")
	}
	if _, ok := <-ch2; ok {
		t.Error("expected ch2 to be closed after Close")
	}

	// Close again is a no-op (subscribers map is now empty) and must not panic.
	hub.Close()
}

func TestLogHub_ConcurrentSubscribeAndBroadcast(t *testing.T) {
	hub := NewLogHub()
	var wg sync.WaitGroup

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ch := hub.Subscribe()
			defer hub.Unsubscribe(ch)
			for range ch {
				// drain until closed/unsubscribed
			}
		}(i)
	}

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			hub.Broadcast(lineName(i))
		}(i)
	}

	wg.Wait()
}

func lineName(i int) string {
	return "line-" + strconv.Itoa(i)
}
