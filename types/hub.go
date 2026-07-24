package types

import "sync"

const replayBufferSize = 200

type LogHub struct {
	subscribers map[chan string]struct{}
	// buffer is a fixed-size ring: head is the index the NEXT Broadcast writes to, count is how many valid
	// entries it currently holds (caps at replayBufferSize). Previously buffer was a slice re-sliced
	// (buffer[1:]) then re-appended on every single line once full -- buffer[1:] just advances the slice
	// header without freeing the backing array, so the following append (once it outgrows the remaining
	// capacity) reallocates a fresh array and copies all ~200 strings, repeating that churn on every log
	// line forever. A fixed array with head/count indices never reallocates.
	buffer [replayBufferSize]string
	head   int
	count  int
	mu     sync.RWMutex
}

func NewLogHub() *LogHub {
	return &LogHub{
		subscribers: make(map[chan string]struct{}),
	}
}

func (lh *LogHub) Subscribe() chan string {
	ch := make(chan string, 256)
	lh.mu.Lock()
	// Replay buffered lines to the new subscriber, oldest first: the oldest surviving entry sits at
	// (head - count), wrapping around the fixed-size array.
	start := (lh.head - lh.count + replayBufferSize) % replayBufferSize
	for i := 0; i < lh.count; i++ {
		ch <- lh.buffer[(start+i)%replayBufferSize]
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
	lh.buffer[lh.head] = line
	lh.head = (lh.head + 1) % replayBufferSize
	if lh.count < replayBufferSize {
		lh.count++
	}
	for ch := range lh.subscribers {
		select {
		case ch <- line:
		default:
			// drop message if subscriber is too slow
		}
	}
	lh.mu.Unlock()
}

func (lh *LogHub) Close() {
	lh.mu.Lock()
	for ch := range lh.subscribers {
		close(ch)
		delete(lh.subscribers, ch)
	}
	lh.mu.Unlock()
}
