package types

import (
	"sync"
	"sync/atomic"
	"time"
)

// eventBufferSize is per subscriber. The engine is the only subscriber in
// practice and it drains continuously; the buffer exists to absorb a burst
// (a server boot dumps hundreds of lines at once) without dropping.
const eventBufferSize = 512

// Event is anything the bus carries. A marker interface rather than a struct
// with a "kind" field: the matcher type-switches on it, so an unhandled kind
// is a compile-time gap instead of a silent default case.
type Event interface{ eventMarker() }

// ConsoleLineEvent is one line from a server's console. Published for EVERY
// line, so everything downstream must be cheap.
type ConsoleLineEvent struct {
	ServerID string
	Line     string
	At       time.Time
}

// ServerLifecycleEvent reports the JVM starting or stopping. Expected is
// false when the stop was not requested through the panel -- that is what
// distinguishes "server stops unexpectedly" from a normal shutdown.
type ServerLifecycleEvent struct {
	ServerID string
	Started  bool
	Expected bool
	At       time.Time
}

// BackupEvent reports the outcome of a backup, whoever asked for it.
type BackupEvent struct {
	ServerID string
	Name     string
	Failed   bool
	Err      string
	At       time.Time
}

// SampleKind names a periodically measured value.
type SampleKind string

const (
	SampleTPS         SampleKind = "tps"
	SamplePlayerCount SampleKind = "count"
	SampleDiskPercent SampleKind = "disk"
)

// SampleEvent carries one measurement. These are not free to produce -- see
// services/sampler.go -- so they are only published when a rule asks.
type SampleEvent struct {
	ServerID string
	Kind     SampleKind
	Value    float64
	At       time.Time
}

// ScheduleTick drives time-based rules. It has no external source: a schedule
// is answered by the clock alone.
type ScheduleTick struct{ At time.Time }

func (ConsoleLineEvent) eventMarker()     {}
func (ServerLifecycleEvent) eventMarker() {}
func (BackupEvent) eventMarker()          {}
func (SampleEvent) eventMarker()          {}
func (ScheduleTick) eventMarker()         {}

// EventBus is a fan-out with bounded buffers and drop-on-full semantics.
//
// It lives in types/ deliberately. Publishers are in services/ and the only
// consumer is automation/, and putting the bus in either would force one to
// import the other. A leaf package keeps both free of the dependency.
type EventBus struct {
	mu          sync.RWMutex
	subscribers map[chan Event]struct{}
	dropped     atomic.Uint64
}

// Bus is the process-wide instance. Publishers reach it directly; only the
// automation engine subscribes.
var Bus = NewEventBus()

func NewEventBus() *EventBus {
	return &EventBus{subscribers: make(map[chan Event]struct{})}
}

func (b *EventBus) Subscribe() <-chan Event {
	ch := make(chan Event, eventBufferSize)
	b.mu.Lock()
	b.subscribers[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *EventBus) Unsubscribe(ch <-chan Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for c := range b.subscribers {
		if (<-chan Event)(c) == ch {
			delete(b.subscribers, c)
			close(c)
			return
		}
	}
}

// Publish never blocks. A subscriber whose buffer is full loses the event and
// the drop is counted.
//
// That direction is the whole point: the tailer publishes every console line,
// and the console stream is a feature people actually use. Stalling it to
// guarantee delivery of an automation event would trade something that works
// for a convenience.
func (b *EventBus) Publish(ev Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subscribers {
		select {
		case ch <- ev:
		default:
			b.dropped.Add(1)
		}
	}
}

// Dropped is the lifetime count of events lost to full buffers. Surfaced so a
// silent loss can be noticed instead of guessed at.
func (b *EventBus) Dropped() uint64 { return b.dropped.Load() }

// HasSubscribers lets a publisher skip work entirely when nothing is
// listening -- the tailer checks this before allocating an event per line.
func (b *EventBus) HasSubscribers() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers) > 0
}
