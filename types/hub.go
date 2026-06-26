package types

import "sync"

const replayBufferSize = 200

type LogHub struct {
	subscribers map[chan string]struct{}
	buffer      []string
	mu          sync.RWMutex
}

func NewLogHub() *LogHub {
	return &LogHub{
		subscribers: make(map[chan string]struct{}),
		buffer:      make([]string, 0, replayBufferSize),
	}
}

func (lh *LogHub) Subscribe() chan string {
	ch := make(chan string, 256)
	lh.mu.Lock()
	// Replay buffered lines to new subscriber
	for _, line := range lh.buffer {
		ch <- line
	}
	lh.subscribers[ch] = struct{}{}
	lh.mu.Unlock()
	return ch
}

func (lh *LogHub) Unsubscribe(ch chan string) {
	lh.mu.Lock()
	delete(lh.subscribers, ch)
	close(ch)
	lh.mu.Unlock()
}

func (lh *LogHub) Broadcast(line string) {
	lh.mu.Lock()
	// Append to ring buffer
	if len(lh.buffer) >= replayBufferSize {
		lh.buffer = lh.buffer[1:]
	}
	lh.buffer = append(lh.buffer, line)
	for ch := range lh.subscribers {
		select {
		case ch <- line:
		default:
			// drop message if subscriber is too slow
		}
	}
	lh.mu.Unlock()
}

// Snapshot returns a copy of the buffered log lines (oldest first, up to
// replayBufferSize). Safe to call without subscribing.
func (lh *LogHub) Snapshot() []string {
	lh.mu.RLock()
	defer lh.mu.RUnlock()
	out := make([]string, len(lh.buffer))
	copy(out, lh.buffer)
	return out
}

func (lh *LogHub) Close() {
	lh.mu.Lock()
	for ch := range lh.subscribers {
		close(ch)
		delete(lh.subscribers, ch)
	}
	lh.mu.Unlock()
}
